package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 纯 Go 驱动（modernc）里 FTS5 的 trigram 分词器能不能用（规格-记忆-v2 第十节：现在的全文索引 memory_fts 是 tokenize='trigram'）。
// 不能用的话，全文那一路要换做法，所以先证明它能：建表、写中文、按三字片段查得到，不足三个字的查不到（和 SQLite 官方说的一样）
func TestFTS5Trigram(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE f USING fts5(text, ref UNINDEXED, tokenize='trigram')`); err != nil {
		t.Fatalf("建不了 trigram 的 FTS5 表：%v", err)
	}
	for ref, text := range map[string]string{"f:1": "主人最近在学吉他，每天练半小时", "f:2": "主人下周三考试"} {
		if _, err := db.Exec(`INSERT INTO f (text, ref) VALUES (?, ?)`, text, ref); err != nil {
			t.Fatal(err)
		}
	}
	var ref string
	if err := db.QueryRow(`SELECT ref FROM f WHERE f MATCH '"学吉他"' ORDER BY bm25(f)`).Scan(&ref); err != nil || ref != "f:1" {
		t.Fatalf("三字片段没查到：ref=%q err=%v", ref, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM f WHERE f MATCH '"吉他"'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("两个字的查询照理查不到（trigram 至少要 3 个字），结果 n=%d err=%v", n, err)
	}
}

// 能读现在 Bun 版写的记忆库：SUMIKA_MEMORY_DB 指向一份副本（不要指真实的库）时跑，没设就跳过
func TestReadsBunMemoryDb(t *testing.T) {
	t.Parallel()
	file := os.Getenv("SUMIKA_MEMORY_DB")
	if file == "" {
		t.Skip("没设 SUMIKA_MEMORY_DB（记忆库副本的路径）")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var facts, fts int
	if err := db.QueryRow(`SELECT count(*) FROM facts WHERE status = 'active'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	// 拿一条有效事实开头的三个字去查（trigram 至少三个字），它自己得在结果里
	var id, text string
	if err := db.QueryRow(`SELECT id, text FROM facts WHERE status = 'active' AND length(text) >= 3 LIMIT 1`).Scan(&id, &text); err != nil {
		t.Fatal(err)
	}
	q := `"` + string([]rune(text)[:3]) + `"`
	if err := db.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ? AND ref = ?`, q, "f:"+id).Scan(&fts); err != nil || fts != 1 {
		t.Fatalf("读不了 Bun 版的全文索引：查 %s 得到 %d 条，err=%v", q, fts, err)
	}
	// vec0 是 sqlite-vec 的 C 扩展建的虚表，纯 Go 驱动打不开（规格-记忆-v2 第十节冲突 1）：确认一下是这样
	_, vecErr := db.Exec(`SELECT count(*) FROM memory_vec`)
	t.Logf("有效事实 %d 条，全文按开头三个字查到自己 %d 条；读 memory_vec：%v", facts, fts, vecErr)
}
