package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// 旧 trace 没有 details_json：升级后旧步骤还在，详情完整保存，普通结果仍是 NULL。
func TestStepDetailsUpgradeAndPersist(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "trace.db")
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "migrations/trace", 4); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var columns int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('steps') WHERE name = 'details_json'`).Scan(&columns); err != nil || columns != 0 {
		db.Close()
		t.Fatalf("起点应是没有详情列的旧库：%d，%v", columns, err)
	}
	if _, err := db.Exec(`INSERT INTO steps (id, kind, preview, status) VALUES ('old', 'tool', '旧输出', 'done')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenTraceFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Trace: db}
	details := struct {
		Diff             string `json:"diff"`
		Patch            string `json:"patch"`
		FirstChangedLine int    `json:"firstChangedLine"`
	}{strings.Repeat("原文 <>& 🙂\u2028\n", 2000), "--- a\n+++ a\n", 2}
	want, err := JSString(details)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"edit", "plain", "denied"} {
		mustOK(t, s.StepStart(Step{ID: id, Kind: "tool"}))
	}
	mustOK(t, s.StepApproval("denied", "suzu", "denied", `{"decision":"denied"}`))
	mustOK(t, s.StepEnd("edit", "模型只看这句话", false, 12, details))
	mustOK(t, s.StepEnd("plain", "普通输出", false, 13, nil))
	mustOK(t, s.StepEnd("denied", "拒绝", true, 14, nil))
	s.Close()
	db, err = OpenTraceFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range []struct{ id, preview, status, details string }{
		{"old", "旧输出", "done", ""},
		{"edit", "模型只看这句话", "done", want},
		{"plain", "普通输出", "done", ""},
		{"denied", "拒绝", "denied", ""},
	} {
		var preview, status string
		var got sql.NullString
		if err := db.QueryRow(`SELECT preview, status, details_json FROM steps WHERE id = ?`, c.id).Scan(&preview, &status, &got); err != nil {
			t.Fatal(err)
		}
		if preview != c.preview || status != c.status || got.String != c.details || got.Valid != (c.details != "") {
			t.Errorf("%s：预览 %q、状态 %q、详情 %.160q（有效 %v，%d 字节）；应该 %q、%q、%.160q（%d 字节）", c.id, preview, status, got.String, got.Valid, len(got.String), c.preview, c.status, c.details, len(c.details))
		}
	}
	if !t.Failed() {
		t.Logf("升级旧 trace 并重开验证 4 条步骤，详情 %d 字节未截断", len(want))
	}
}

func TestStepEndRejectsInvalidDetails(t *testing.T) {
	t.Parallel()
	db, err := OpenTraceFile(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{Trace: db}
	large := strings.Repeat("详情", stepDetailsMax)
	for _, details := range []any{large, map[string]any{"diff": large}, map[string]any{"diff": large, "patch": 123}, map[string]any{"diff": "a", "patch": "b", "extra": large}} {
		mustOK(t, s.StepStart(Step{ID: "made-invalid", Kind: "tool"}))
		if err := s.StepEnd("made-invalid", "已经执行，保存失败", false, 42, details); err == nil {
			t.Fatal("坏详情被当作保存成功")
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM steps WHERE id='made-invalid'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "done" {
			t.Fatal("保存失败仍发布完成状态")
		}
	}
}
