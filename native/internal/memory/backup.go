package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 开写入前的备份和 memory_meta 里的"开写时间"（规格-记忆写入上线 W5 前提 4）。
// 备份做两次、各只做一次：首次开写前（before-write）、首次开后台整理前（before-summarize）。两次都只读打开源库，VACUUM INTO 到备份目录，
// 备份完 integrity_check 通过且 facts / episodes 行数和原库一致才算数。备份目录由调用方给（读 cfg.Backup.Dir，空才兜底），这里不猜、不自己删。

// 备份的两种、memory_meta 里记时间的两个键
const (
	BackupBeforeWrite     = "before-write"
	BackupBeforeSummarize = "before-summarize"

	MetaWriteEnabledAt     = "write_enabled_at"     // 毫秒：首次开写（备份成功之后）
	MetaSummarizeEnabledAt = "summarize_enabled_at" // 毫秒：首次开后台整理
	MetaCatchUpPlaces      = "catchup_places"       // 位图：catchUp=latest 已经处理过哪些场合取值（catchup.go）
)

// openBackupSource：只读打开（mode=ro，库文件写不了），但不带 query_only——query_only 连 VACUUM INTO 写另一个文件也拒绝
func openBackupSource(file string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	p := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(file))
	db, err := sql.Open("sqlite", "file:"+p+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 %s：%w", file, err)
	}
	return db, nil
}

// BackupResult：Existed 为真是这个库、这个指纹已经有备份，这次没再做
type BackupResult struct {
	File     string // 备份文件的路径
	Existed  bool
	Facts    int
	Episodes int
}

// 备份时探的数：每个表一两个整数，缺的表按 0 算（迁移前的库没有后几张）。指纹用全部，"行数一致"只比 count
type probe struct{ table, name, expr string }

var backupProbes = []probe{
	{"facts", "count", "count(*)"}, {"facts", "latest", "coalesce(max(created_at), 0)"}, {"facts", "states", "coalesce(sum(length(status) + length(coalesce(superseded_by, ''))), 0)"},
	{"episodes", "count", "count(*)"}, {"episodes", "latest", "coalesce(max(created_at), 0)"},
	{"fact_revisions", "count", "count(*)"}, {"fact_revisions", "latest", "coalesce(max(id), 0)"},
	{"fact_evidence", "count", "count(*)"}, {"fact_evidence", "latest", "coalesce(max(at), 0)"},
	{"memory_jobs", "count", "count(*)"}, {"memory_jobs", "latest", "coalesce(max(done_at), 0)"},
	{"summary_state", "count", "count(*)"}, {"summary_state", "cursors", "coalesce(sum(last_seq), 0)"}, {"summary_state", "latest", "coalesce(max(updated_at), 0)"},
	{"item_scopes", "count", "count(*)"},
}

// 备份和原库要行数一样的表
var backupCountTables = []string{"facts", "episodes", "fact_revisions", "fact_evidence", "memory_jobs", "summary_state", "item_scopes"}

// BackupMemoryDB 把 file（一个记忆库）备份到 dir 下。kind 是 BackupBeforeWrite 或 BackupBeforeSummarize。
// 文件名 memory-<角色>-<时间>-<kind>-<指纹>.db；指纹是库里几张表的行数和最大 rowid（我定的，起步值），同一个角色、同一个 kind、
// 同一个指纹已经有备份就跳过（备份成功、后面打开失败时库没改，下次启动不重做）。先写成 .partial 再改名，崩在半路不会留下一个像样的名字。
// 做不成（目录建不了、写不进、校验不过）返回错误，调用方据此不开对应那一档
func BackupMemoryDB(ctx context.Context, file, dir, char, kind string, now time.Time) (BackupResult, error) {
	if err := ValidCharacterID(char); err != nil {
		return BackupResult{}, err
	}
	if kind != BackupBeforeWrite && kind != BackupBeforeSummarize {
		return BackupResult{}, fmt.Errorf("不认识的备份种类 %q", kind)
	}
	if strings.TrimSpace(dir) == "" {
		return BackupResult{}, errors.New("备份目录是空的")
	}
	if err := mustBeFile(file); err != nil {
		return BackupResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BackupResult{}, fmt.Errorf("备份目录建不了：%w", err)
	}
	src, err := openBackupSource(file)
	if err != nil {
		return BackupResult{}, err
	}
	defer src.Close()
	before, err := takeProbes(ctx, src)
	if err != nil {
		return BackupResult{}, err
	}
	fp := fingerprint(before)
	if old, err := existingBackup(dir, char, kind, fp); err != nil {
		return BackupResult{}, err
	} else if old != "" {
		return BackupResult{File: old, Existed: true, Facts: int(before["facts.count"]), Episodes: int(before["episodes.count"])}, nil
	}
	name := fmt.Sprintf("memory-%s-%s-%s-%s.db", char, now.Format("20060102-150405"), kind, fp)
	final := filepath.Join(dir, name)
	part := final + ".partial"
	_ = os.Remove(part) // 上次崩在半路留下的
	if _, err := src.ExecContext(ctx, `VACUUM INTO ?`, part); err != nil {
		_ = os.Remove(part)
		return BackupResult{}, fmt.Errorf("备份写不进 %s：%w", name, err)
	}
	after, err := takeProbes(ctx, src)
	if err != nil || fingerprint(after) != fp {
		_ = os.Remove(part)
		return BackupResult{}, errors.New("备份期间原库变了，这一份作废（原因 source_changed）")
	}
	if err := verifyBackup(ctx, part, before); err != nil {
		_ = os.Remove(part)
		return BackupResult{}, err
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return BackupResult{}, fmt.Errorf("备份改名失败：%w", err)
	}
	return BackupResult{File: final, Facts: int(before["facts.count"]), Episodes: int(before["episodes.count"])}, nil
}

// probes：key 是 "表.名字"
type probes map[string]int64

func takeProbes(ctx context.Context, db *sql.DB) (probes, error) {
	out := probes{}
	have := map[string]bool{}
	for _, p := range backupProbes {
		if _, seen := have[p.table]; !seen {
			var n int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, p.table).Scan(&n); err != nil {
				return nil, err
			}
			have[p.table] = n > 0
		}
		if !have[p.table] {
			out[p.table+"."+p.name] = 0
			continue
		}
		var v int64
		// 表名和表达式都来自上面写死的 backupProbes，不是外面给的
		if err := db.QueryRowContext(ctx, `SELECT `+p.expr+` FROM `+p.table).Scan(&v); err != nil {
			return nil, err
		}
		out[p.table+"."+p.name] = v
	}
	return out, nil
}

func fingerprint(c probes) string {
	h := sha256.New()
	for _, p := range backupProbes {
		fmt.Fprintf(h, "%s.%s:%d;", p.table, p.name, c[p.table+"."+p.name])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// existingBackup：dir 里有没有这个角色、这个 kind、这个指纹的备份（不含 .partial）。有就返回第一个
func existingBackup(dir, char, kind, fp string) (string, error) {
	pattern := filepath.Join(dir, fmt.Sprintf("memory-%s-*-%s-%s.db", escapeGlob(char), kind, fp))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return m, nil
		}
	}
	return "", nil
}

func escapeGlob(s string) string {
	return strings.NewReplacer("[", `\[`, "*", `\*`, "?", `\?`).Replace(s)
}

// verifyBackup：备份能打开、integrity_check 是 ok、几张表的行数和原库一致
func verifyBackup(ctx context.Context, file string, want probes) error {
	db, err := openReadOnly(file)
	if err != nil {
		return fmt.Errorf("备份打不开（原因 backup_unreadable）：%w", err)
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
		return fmt.Errorf("备份没通过 integrity_check（原因 backup_corrupt）：%v %s", err, res)
	}
	got, err := takeProbes(ctx, db)
	if err != nil {
		return err
	}
	for _, t := range backupCountTables {
		if got[t+".count"] != want[t+".count"] {
			return fmt.Errorf("备份里 %s 的行数 %d 和原库 %d 对不上（原因 backup_mismatch）", t, got[t+".count"], want[t+".count"])
		}
	}
	return nil
}

// MetaValue 只读打开一个库，读 memory_meta 里的一个键；没有这个键（或表）ok 为假。开写之前、库还没以可写方式打开时用
func MetaValue(ctx context.Context, file, key string) (value int64, ok bool, err error) {
	db, err := openReadOnly(file)
	if err != nil {
		return 0, false, err
	}
	defer db.Close()
	var have int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'memory_meta'`).Scan(&have); err != nil || have == 0 {
		return 0, false, err
	}
	err = db.QueryRowContext(ctx, `SELECT value FROM memory_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return value, err == nil, err
}

// CheckSchema 只读打开一个库，查写入侧的表和列是不是都在（Open 以可写方式打开时会做的同一个检查）。没迁移返回 ErrNeedsMigration。
// 开写之前先问它，免得备份做了才发现库没迁移
func CheckSchema(ctx context.Context, file string) error {
	if err := mustBeFile(file); err != nil {
		return err
	}
	db, err := openReadOnly(file)
	if err != nil {
		return err
	}
	defer db.Close()
	return schemaReady(ctx, db)
}

// SetMetaIfAbsent 往 memory_meta 写一个键，已经有就不动（"首次"只记一次）。返回这次有没有写。要写入打开着，拿写锁
func (m *Memory) SetMetaIfAbsent(ctx context.Context, key string, value int64) (written bool, err error) {
	w, err := m.writer()
	if err != nil {
		return false, err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return false, err
	}
	defer unlock()
	r, err := w.db.ExecContext(ctx, `INSERT OR IGNORE INTO memory_meta (key, value) VALUES (?, ?)`, key, value)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// Meta 读 memory_meta 的一个键（写入打开着时）
func (m *Memory) Meta(ctx context.Context, key string) (value int64, ok bool, err error) {
	w, err := m.writer()
	if err != nil {
		return 0, false, err
	}
	err = w.db.QueryRowContext(ctx, `SELECT value FROM memory_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return value, err == nil, err
}
