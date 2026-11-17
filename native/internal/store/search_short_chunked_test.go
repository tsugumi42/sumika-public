package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// 0016 把一两个字的索引改成切块写法：产出必须和 0015 的旧写法逐条相同（所以已有的索引行不用重建），长文本快几十倍（仍是平方级，常数小 64 倍）。
func TestSearchShortChunkedMatchesOld(t *testing.T) {
	t.Parallel()
	old, cur := chatAt(t, 15), chatAt(t, 16)
	samples := []string{
		"", "a", "ab", "AB", "你", "你好", "😀", "a😀b", "ÄÖ é́", "  x\n\ty  ", "混合 Mixed 文本 😀👍🏽 ok\r\n第二行",
		"\x00a\x00", "末尾\x00后面", "ẞ İ Σ", strings.Repeat("x", 500),
		strings.Repeat("a", 63), strings.Repeat("b", 64), strings.Repeat("c", 65), cjk(128), cjk(129), strings.Repeat("😀", 130), cjk(3000), cjk(192), cjk(193),
		cjk(63) + "\x00" + cjk(10), cjk(64) + "\x00" + cjk(10), "a\xffb\xc3", cjk(64) + "\xe4\xb8" + cjk(5),
	}
	for i, s := range samples {
		id := fmt.Sprint("s", i)
		insertSearchEvent(t, old, id, s)
		insertSearchEvent(t, cur, id, s)
		if a, b := shortTokens(t, old, id), shortTokens(t, cur, id); a != b {
			t.Fatalf("样本 %d（%q…）插入后的索引不一样：\n旧 %.300s\n新 %.300s", i, firstRunes(s, 12), a, b)
		}
	}
	// 改文本走更新触发器：换成另一条样本，同样要一样
	for i := range samples {
		id, next := fmt.Sprint("s", i), samples[(i+7)%len(samples)]
		for _, db := range []*sql.DB{old, cur} {
			if _, err := db.Exec(`UPDATE events SET text = ? WHERE id = ?`, next, id); err != nil {
				t.Fatal(err)
			}
		}
		if a, b := shortTokens(t, old, id), shortTokens(t, cur, id); a != b {
			t.Fatalf("样本 %d 改文本后的索引不一样", i)
		}
	}
}

// 长文本写入不再平方级：1.2 万字旧写法约 0.6 秒，新写法几十毫秒。按倍数比，不写死毫秒（机器忙时两边一起慢）
func TestSearchShortChunkedLongText(t *testing.T) {
	t.Parallel()
	old, cur := chatAt(t, 15), chatAt(t, 16)
	text := cjk(12000)
	took := func(db *sql.DB) time.Duration {
		start := time.Now()
		insertSearchEvent(t, db, "long", text)
		return time.Since(start)
	}
	o, n := took(old), took(cur)
	if n*5 > o {
		t.Fatalf("1.2 万字：旧 %v、新 %v，新写法没有快五倍以上", o, n)
	}
	if a, b := shortTokens(t, old, "long"), shortTokens(t, cur, "long"); a != b {
		t.Fatal("长文本的索引不一样")
	}
}

// 有数据的旧库跑 0016：已有的索引一行不动；之后新写入的和一直用旧触发器的库逐条相同；中途失败整段回滚、修好能重跑；能退回 0015 再升上来
func TestSearchShortChunkedOnPopulatedDB(t *testing.T) {
	t.Parallel()
	db, twin := chatAt(t, 15), chatAt(t, 15)
	// 三千条短消息（编的）+ 一条 8000 字的；迁移只换触发器、不碰数据行，耗时和条数无关（上万条的真实副本实测见规格第十节）
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ins, err := tx.Prepare(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at) VALUES(?, 'x', 'x', 'human', 'x', 'message', ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3000 {
		if _, err := ins.Exec(fmt.Sprint("m", i), fmt.Sprintf("第%d条 Msg%d %s😀", i, i%97, cjk(i%40)), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	insertSearchEvent(t, db, "long", cjk(8000))
	before := dumpSearch(t, db)

	// 中途失败：先摘掉第二个触发器，0016 的第二句 DROP 会报错；第一句已经删掉的触发器必须跟着回滚
	if _, err := db.Exec(`DROP TRIGGER events_search_update`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "migrations/chat", 16); err == nil {
		t.Fatal("少了触发器还迁移成功了")
	}
	if !strings.Contains(triggerSQL(t, db, "events_search_insert"), "positions(n)") {
		t.Fatal("迁移失败后插入触发器没有回滚成旧的")
	}
	// 修好（照 0015 补回更新触发器）再跑一次
	if _, err := db.Exec(triggerSQL(t, twin, "events_search_update")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := migrate(db, "migrations/chat", 16); err != nil {
		t.Fatal(err)
	}
	t.Logf("三千条加一条长消息的库跑 0016：%v", time.Since(start))
	if !strings.Contains(triggerSQL(t, db, "events_search_insert"), "MATERIALIZED") {
		t.Fatal("没换成新触发器")
	}
	if after := dumpSearch(t, db); after != before {
		t.Fatal("迁移改动了已有的索引")
	}
	// 迁移之后两边写一样的新数据（插入、改文本、删除），索引逐条相同
	same := func(stage string) {
		t.Helper()
		if a, b := dumpSearchFrom(t, db, 0), dumpSearchFrom(t, twin, 0); a != b {
			t.Fatalf("%s后新数据的索引不一样", stage)
		}
	}
	for i, s := range []string{"新的一条", cjk(5000), "ab😀", ""} {
		for _, d := range []*sql.DB{db, twin} {
			insertSearchEvent(t, d, fmt.Sprint("n", i), s)
		}
	}
	same("插入")
	for _, d := range []*sql.DB{db, twin} {
		if _, err := d.Exec(`UPDATE events SET text = text || '尾巴' WHERE id LIKE 'n%'`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`DELETE FROM events WHERE id = 'n0'`); err != nil {
			t.Fatal(err)
		}
	}
	same("改文本、删除")
	// 退回 0015 再升上来：触发器换回旧的、已有索引不动
	pv := chatProvider(t, db)
	if _, err := pv.DownTo(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(triggerSQL(t, db, "events_search_insert"), "positions(n)") {
		t.Fatal("退回后不是旧触发器")
	}
	mid := dumpSearch(t, db)
	if err := migrate(db, "migrations/chat", 16); err != nil {
		t.Fatal(err)
	}
	if dumpSearch(t, db) != mid {
		t.Fatal("退回再升改动了索引")
	}
}

func chatProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	sub, err := fs.Sub(migrations, "migrations/chat")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatal(err)
	}
	return pv
}

func triggerSQL(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&s); err != nil {
		t.Fatalf("读触发器 %s：%v", name, err)
	}
	return s
}

// dumpSearch 把两张搜索索引整个写成一串：短词表每一行，trigram 用 FTS5 自带的完整性检查 + 每个词的出现数
func dumpSearch(t *testing.T, db *sql.DB) string { return dumpSearchFrom(t, db, -1) }

// dumpSearchFrom 只看 seq 在 minSeq 之后的行（按 id 认，两库 seq 可能不同）；minSeq < 0 是全部，按 seq 认
func dumpSearchFrom(t *testing.T, db *sql.DB, skipFirst int) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO events_trigram(events_trigram, rank) VALUES('integrity-check', 1)`); err != nil {
		t.Fatalf("trigram 索引不完整：%v", err)
	}
	q := `SELECT events.id, hex(token), events_short.created_at FROM events_short JOIN events ON events.seq = events_short.event_seq ORDER BY 1, 2, 3`
	if skipFirst < 0 {
		q = `SELECT event_seq, hex(token), created_at FROM events_short ORDER BY 1, 2, 3`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, tok string
		var at int64
		if err := rows.Scan(&id, &tok, &at); err != nil {
			t.Fatal(err)
		}
		if skipFirst >= 0 && (strings.HasPrefix(id, "m") || id == "long") {
			continue
		}
		fmt.Fprintf(&b, "%s %s %d\n", id, tok, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if skipFirst < 0 {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM events_trigram WHERE events_trigram MATCH '"第1条"'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "trigram 第1条 %d\n", n)
	}
	return b.String()
}

// 隐藏来源（定期任务没说出口的话）和可见之间互转，新旧更新触发器对两张索引做的事一样
func TestSearchShortChunkedOriginToggle(t *testing.T) {
	t.Parallel()
	old, cur := chatAt(t, 15), chatAt(t, 16)
	origins := []any{nil, "", "character", "automation:loop-pending", "automation:loop-quiet", "Automation:Loop-Quiet"}
	for i := range 12 {
		for _, db := range []*sql.DB{old, cur} {
			if _, err := db.Exec(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at,origin) VALUES(?, 'x', 'x', 'character', 'x', 'message', ?, ?, ?)`, fmt.Sprint("o", i), cjk(60+i*7), i, origins[i%len(origins)]); err != nil {
				t.Fatal(err)
			}
		}
	}
	for step := range 60 {
		id, origin := fmt.Sprint("o", step%12), origins[(step*5+1)%len(origins)]
		for _, db := range []*sql.DB{old, cur} {
			q := `UPDATE events SET origin = ? WHERE id = ?`
			if step%3 == 0 {
				q = `UPDATE events SET origin = ?, text = text || '改' WHERE id = ?`
			}
			if _, err := db.Exec(q, origin, id); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 12 {
			if a, b := shortTokens(t, old, fmt.Sprint("o", i)), shortTokens(t, cur, fmt.Sprint("o", i)); a != b {
				t.Fatalf("第 %d 步后 o%d 的短词索引不一样", step, i)
			}
		}
		if a, b := trigramRows(t, old), trigramRows(t, cur); a != b {
			t.Fatalf("第 %d 步后 trigram 索引不一样：旧 %s 新 %s", step, a, b)
		}
	}
}

// trigramRows 列出 trigram 索引里有哪些行（按 id）：用每行原文的头三个字去搜。
// 不用 FTS5 的 integrity-check：外部内容表会把“故意没进索引的隐藏行”当成损坏（新旧写法都一样）
func trigramRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM events e WHERE EXISTS (SELECT 1 FROM events_trigram WHERE events_trigram MATCH '"' || substr(e.text, 1, 3) || '"' AND rowid = e.seq) ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		b.WriteString(id + " ")
	}
	return b.String()
}

// chatAt 建一个跑到第 upTo 个 chat 迁移的临时库
func chatAt(t *testing.T, upTo int64) *sql.DB {
	t.Helper()
	db, err := open(filepath.Join(t.TempDir(), "chat.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate(db, "migrations/chat", upTo); err != nil {
		t.Fatal(err)
	}
	return db
}

func shortTokens(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	rows, err := db.Query(`SELECT hex(token), events_short.created_at FROM events_short JOIN events ON events.seq = events_short.event_seq WHERE events.id = ? ORDER BY 1, 2`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var tok string
		var at int64
		if err := rows.Scan(&tok, &at); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s@%d ", tok, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// cjk 造 n 个不重复为主的汉字（编的，不是真实内容）
func cjk(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteRune(rune(0x4e00 + (i*7919)%3000))
	}
	return b.String()
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
