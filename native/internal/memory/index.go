package memory

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/embed"
	"sumika-native/internal/pyspace"
)

// 旁边的文件 memory-v2.db 里放 Go 版自己的派生表（规格-记忆-v2 13.1）：第 2 步不往 Bun 后台正在写的 memory.db 里写。
// 全文两张（三字、两字，待确认也收）、向量一张（归一化好的 float32，带模型名和内容指纹）、别名一张。坏了删掉重建就行

// VectorPrompt 写进向量表的 prompt_version：现在记忆两边（存、查）都是原文直接算，没有任务前缀；以后加前缀就换一个值，旧向量当过期
const VectorPrompt = "raw-v1"

const sidecarSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS memory_trigram USING fts5(text, ref UNINDEXED, scope UNINDEXED, tokenize='trigram');
CREATE VIRTUAL TABLE IF NOT EXISTS memory_bigram USING fts5(text, ref UNINDEXED, scope UNINDEXED, tokenize='unicode61');
CREATE TABLE IF NOT EXISTS memory_vectors (ref TEXT PRIMARY KEY, model TEXT NOT NULL, prompt_version TEXT NOT NULL, content_hash TEXT NOT NULL, dim INTEGER NOT NULL, vec BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS memory_sentences (ref TEXT NOT NULL, idx INTEGER NOT NULL, model TEXT NOT NULL, content_hash TEXT NOT NULL, vec BLOB NOT NULL, PRIMARY KEY(ref, idx));
CREATE TABLE IF NOT EXISTS aliases (alias TEXT NOT NULL, person TEXT NOT NULL, source TEXT NOT NULL, PRIMARY KEY (alias, person, source));
CREATE TABLE IF NOT EXISTS sync_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// SidecarPath 是某个角色记忆库旁边的那个文件
func SidecarPath(memoryDB string) string {
	return filepath.Join(filepath.Dir(memoryDB), "memory-v2.db")
}

// Index 是旁边那个文件，加上读进内存的向量（一般几百条，全读进来逐条算点积，13.1）。
// 向量那张表会在后台协程里被补齐、被召回同时读（规格-记忆-v2 13.2.12 的 W2，无锁就是并发读写 map，Go 运行时直接崩）：
// vectors 一旦放出去就不再改，要改先复制一份、算好、整张换上去（publish）；读的人拿一次 map，之后遍历不用持锁
type Index struct {
	db           *sql.DB
	syncMu       sync.Mutex    // 同一时间只有一个 SyncVectors 在跑
	sentenceGate chan struct{} // 句向量补齐串行，等待时能被 ctx 取消
	mu           sync.RWMutex
	vectors      map[string][]float32 // ref → 单位向量；mu 管这个字段和下面的 model
	model        string
}

func openSidecar(file string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
	db, err := sql.Open("sqlite", "file:"+p+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sidecarSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("建 %s 的表：%w", file, err)
	}
	return db, nil
}

// OpenIndex 打开（没有就建）旁边那个文件；开之前先只读探查 memory-index 轨，太新就拒绝（不自动删除重建），
// 正常开完在新建或缺行时写版本表。
func OpenIndex(ctx context.Context, file string) (*Index, error) {
	tracks := indexTracks()
	dec, err := inspectMemoryGuard(ctx, file, tracks)
	if err != nil {
		return nil, err
	}
	db, err := openSidecar(file)
	if err != nil {
		return nil, err
	}
	// 新建、缺表、缺行或记的版本比程序旧（这条轨没有迁移文件，只补版本表）都写一次；
	// 版本行齐全且已是最新时不重写，不每次打开都改 migrated_at；较新能读也不动版本表。
	write := true
	switch dec.Action {
	case dbguard.ActionReadNewer:
		write = false
	case dbguard.ActionOpen:
		write = dec.NeedSchema
	}
	if write {
		if err := dbguard.WriteSchema(ctx, db, tracks, dbguard.MigratedBy(), time.Now()); err != nil {
			db.Close()
			return nil, fmt.Errorf("写 %s 的版本表：%w", file, err)
		}
	}
	return &Index{db: db, vectors: map[string][]float32{}, sentenceGate: make(chan struct{}, 1)}, nil
}

func (x *Index) Close() error { return x.db.Close() }

// Vectors 是现在能用的向量表。拿到的 map 只读，不要改；换新的时候这一份不受影响
func (x *Index) Vectors() map[string][]float32 {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.vectors
}

func (x *Index) publish(model string, vectors map[string][]float32) {
	x.mu.Lock()
	x.vectors, x.model = vectors, model
	x.mu.Unlock()
}

// MissingVectors 数这批条目里还没有现行向量的有几条（没算过的、正文改了的、模型或 prompt_version 不一样的）。
// 补齐做完没有要靠它数，不能靠 SyncVectors 的返回值：嵌入不可用时它返回 nil（规格-记忆-v2 13.2.4 第 9 步）。
// 正文全是嵌入服务眼里的空白的条目跳过（SyncVectors 也不给它算）。
// 模型取最近一次 SyncVectors 用的；还没同步过一次，全算缺的
func (x *Index) MissingVectors(items []Item) (int, error) {
	x.mu.RLock()
	model := x.model
	x.mu.RUnlock()
	rows, err := x.db.Query(`SELECT ref, model, prompt_version, content_hash FROM memory_vectors`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct{ model, pv, ch string }
	have := map[string]row{}
	for rows.Next() {
		var ref string
		var r row
		if err := rows.Scan(&ref, &r.model, &r.pv, &r.ch); err != nil {
			return 0, err
		}
		have[ref] = r
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		if pyspace.IsBlank(it.Text) {
			continue // 和 SyncVectors 同一个口径：算不出向量的空白正文不算缺，不然降级标志永远清不掉
		}
		if r, ok := have[it.Ref]; !ok || model == "" || r.model != model || r.pv != VectorPrompt || r.ch != contentHash(it.Text) {
			n++
		}
	}
	return n, nil
}

func contentHash(text string) string {
	h := sha1.Sum([]byte(text))
	return hex.EncodeToString(h[:8])
}

// SyncText 让两张全文表和这批条目一致：条目（编号、正文、范围）没变就不动，变了整个重建（条数不多，几毫秒）。每个范围一行，查的时候在 SQL 里按范围限定、按编号去重
func (x *Index) SyncText(items []Item) error {
	h := sha1.New()
	for _, it := range items {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x01", it.Ref, it.Text, strings.Join(it.Scopes, ","))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	var old string
	x.db.QueryRow(`SELECT value FROM sync_state WHERE key = 'text'`).Scan(&old)
	if old == sum {
		return nil
	}
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM memory_trigram`, `DELETE FROM memory_bigram`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	tri, err := tx.Prepare(`INSERT INTO memory_trigram (text, ref, scope) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	bi, err := tx.Prepare(`INSERT INTO memory_bigram (text, ref, scope) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, it := range items {
		doc := bigramDoc(it.Text)
		for _, s := range it.Scopes {
			if _, err := tri.Exec(it.Text, it.Ref, s); err != nil {
				return err
			}
			if _, err := bi.Exec(doc, it.Ref, s); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM memory_sentences WHERE ref NOT IN (SELECT ref FROM memory_trigram)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO sync_state (key, value) VALUES ('text', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, sum); err != nil {
		return err
	}
	return tx.Commit()
}

// SyncVectors 补齐向量：没有的、正文改了的（content_hash 不同）、模型或 prompt_version 不一样的重算；成功后删掉不在这批条目里的。
// 嵌入不可用（ErrUnavailable）时保留未替换的旧行，返回 nil；一次最多算 batch 条。
// 算向量（网络）不持任何锁：先在私有的新表里攒，结束时（含出错退出）整张换上去，召回在这期间照用旧的
func (x *Index) SyncVectors(ctx context.Context, items []Item, e embed.Embedder, batch int) (computed int, err error) {
	x.syncMu.Lock()
	defer x.syncMu.Unlock()
	model := e.Model()
	want := map[string]Item{}
	for _, it := range items {
		want[it.Ref] = it
	}
	have := map[string]bool{}
	rows, err := x.db.Query(`SELECT ref, model, prompt_version, content_hash, vec FROM memory_vectors`)
	if err != nil {
		return 0, err
	}
	var stale []string
	next := map[string][]float32{}
	defer func() { x.publish(model, next) }()
	for rows.Next() {
		var ref, m, pv, ch string
		var blob []byte
		if err := rows.Scan(&ref, &m, &pv, &ch, &blob); err != nil {
			rows.Close()
			return 0, err
		}
		it, ok := want[ref]
		if !ok {
			stale = append(stale, ref)
			continue
		}
		if m != model || pv != VectorPrompt || ch != contentHash(it.Text) {
			continue
		}
		have[ref] = true
		next[ref] = decodeVec(blob)
	}
	rows.Close()
	var todo []Item
	for _, it := range items {
		// 正文全是嵌入服务眼里的空白（Python 的 strip 剥得干净，internal/pyspace）的条目算不出向量：送进去整批 400，
		// 一条这样的条目会让补向量永远栽在它所在的那一批上
		if !have[it.Ref] && !pyspace.IsBlank(it.Text) {
			todo = append(todo, it)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Ref < todo[j].Ref })
	for len(todo) > 0 {
		n := min(batch, len(todo))
		part := todo[:n]
		texts := make([]string, n)
		for i, it := range part {
			texts[i] = it.Text
		}
		vs, err := e.Embed(ctx, texts)
		if errors.Is(err, embed.ErrUnavailable) {
			return computed, nil
		}
		if err != nil {
			return computed, err
		}
		for i, it := range part {
			u := embed.Unit(vs[i])
			if _, err := x.db.Exec(`INSERT INTO memory_vectors (ref, model, prompt_version, content_hash, dim, vec) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(ref) DO UPDATE SET model = excluded.model, prompt_version = excluded.prompt_version, content_hash = excluded.content_hash, dim = excluded.dim, vec = excluded.vec`,
				it.Ref, model, VectorPrompt, contentHash(it.Text), len(u), encodeVec(u)); err != nil {
				return computed, err
			}
			next[it.Ref] = u
		}
		computed += n
		todo = todo[n:]
	}
	for _, ref := range stale {
		if _, err := x.db.Exec(`DELETE FROM memory_vectors WHERE ref = ?`, ref); err != nil {
			return computed, err
		}
	}
	return computed, nil
}

// Person 是一个能对得上号的人（ADR-038、064：昵称、各群名片、曾用名都挂在号上）
type Person struct {
	ID    string // 和事实的 subject 一样的写法（qq:<号>）
	Names []string
}

// SyncAliases 用人名表重建别名表。少于 2 个字的名字不收（太容易撞上日常的一两个字，比如有人叫"草"）
func (x *Index) SyncAliases(people []Person) error {
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM aliases WHERE source = 'people'`); err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT OR IGNORE INTO aliases (alias, person, source) VALUES (?, ?, 'people')`)
	if err != nil {
		return err
	}
	for _, p := range people {
		for _, n := range p.Names {
			n = strings.TrimSpace(n)
			if len([]rune(n)) < 2 {
				continue
			}
			if _, err := ins.Exec(strings.Map(fold, n), p.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
