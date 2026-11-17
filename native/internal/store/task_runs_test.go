package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/taskrun"
)

var fakeNum = strings.Repeat("8", 11) // 编的号码，运行时拼

func TestAddTaskRunAndSink(t *testing.T) {
	t.Parallel()
	db, err := OpenTraceFile(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{Trace: db}
	sink := &TaskSink{S: s}
	// 没有耗时 -> NULL；detail 漏了脱敏也要在这里被兜住
	if err := sink.AddTaskRun(taskrun.Row{At: time.UnixMilli(5), Module: "send", Outcome: "failed", Class: "unsupported", Ms: -1, Attempts: 0, Detail: "发给 " + fakeNum + " 失败 https://example.com/x"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.AddTaskRun(taskrun.Row{At: time.UnixMilli(6), Module: "link", Outcome: "ok", Ms: 120, Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	var at int64
	var module, outcome, class string
	var ms sql.NullInt64
	var attempts int
	var detail, root sql.NullString
	rows, err := db.Query(`SELECT at, module, outcome, class, ms, attempts, root_id, detail FROM task_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		if err := rows.Scan(&at, &module, &outcome, &class, &ms, &attempts, &root, &detail); err != nil {
			t.Fatal(err)
		}
		n++
		if n == 1 {
			if at != 5 || ms.Valid || attempts != 1 || class != "unsupported" || strings.Contains(detail.String, fakeNum) || strings.Contains(detail.String, "example.com") || root.Valid {
				t.Errorf("第一行：%d %v %d %q %q", at, ms, attempts, class, detail.String)
			}
		} else if !ms.Valid || ms.Int64 != 120 || attempts != 2 || class != "" || detail.Valid {
			t.Errorf("第二行：%v %d %q %v", ms, attempts, class, detail)
		}
	}
	if n != 2 {
		t.Fatalf("行数 %d", n)
	}
	// 直接写超长的 detail 被拒；缺 module 被拒
	if err := s.AddTaskRun(TaskRun{Module: "m", Outcome: "ok", Detail: strings.Repeat("x", 501)}); err == nil {
		t.Error("超长 detail 应被拒")
	}
	if err := s.AddTaskRun(TaskRun{Outcome: "ok"}); err == nil {
		t.Error("缺 module 应被拒")
	}
}
