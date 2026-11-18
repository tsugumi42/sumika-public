package selfcheck_test

import (
	"context"
	"path/filepath"
	"testing"

	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// B1：app 传给自检的读连接（store.OpenReadOnlyFile）写不了库：INSERT、UPDATE、DELETE、CREATE TABLE 都报错。
// 测试用临时目录里新建的 trace 库，不碰真实 data/。
func TestReadOnlyHandleRejectsWrites(t *testing.T) {
	file := filepath.Join(t.TempDir(), "trace.db")
	rw, err := store.OpenTraceFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`INSERT INTO turns (id, character_id, conversation, started_at) VALUES ('t1', 'c', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	rw.Close()
	ro, err := store.OpenReadOnlyFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var q selfcheck.Querier = ro // 自检只拿得到这个窄接口
	for _, sql := range []string{
		`INSERT INTO turns (id, character_id, conversation, started_at) VALUES ('t2', 'c', 'x', 1)`,
		`UPDATE turns SET outcome = 'done'`,
		`DELETE FROM turns`,
		`CREATE TABLE selfcheck_x (a INTEGER)`,
		`DROP TABLE turns`,
	} {
		rows, err := q.QueryContext(context.Background(), sql)
		if err == nil {
			// 有的驱动在 Next / Close 时才报错
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		if err == nil {
			t.Errorf("只读连接不该能执行：%s", sql)
		}
	}
	var n int
	rows, err := q.QueryContext(context.Background(), `SELECT COUNT(*) FROM turns`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		rows.Scan(&n)
	}
	rows.Close()
	if n != 1 {
		t.Fatalf("写入都被拒，库里应仍是 1 行：%d", n)
	}
}
