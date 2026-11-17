package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"sumika-native/internal/kernel"
)

// error_class 要走真实的 upsert：modelcall 和 turn 用同一个 attempt_id 各写一次，后写的覆盖先写的。
// 只在 INSERT 里加列而不进 ON CONFLICT DO UPDATE，turn 侧算出的类别就永远写不进去。
func TestAddRunWritesAndUpsertsErrorClass(t *testing.T) {
	t.Parallel()
	db, err := OpenTraceFile(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{Trace: db}
	msg := "boom"
	id := kernel.Invocation{RootID: "r", RunID: "u", StepID: "s", AttemptID: "a1", Kind: "foreground"}
	base := Run{ID: "x1", CharacterID: "c", Conversation: "k", Stage: "reply", Model: "m", StartedAt: 1, Identity: id, Now: 1}

	class := func(attempt string) (c sql.NullString, e sql.NullString) {
		if err := db.QueryRow(`SELECT error_class, error FROM model_runs WHERE attempt_id = ?`, attempt).Scan(&c, &e); err != nil {
			t.Fatal(err)
		}
		return
	}

	first := base
	first.Error, first.ErrorClass = &msg, "network"
	if err := s.AddRun(first); err != nil {
		t.Fatal(err)
	}
	if c, _ := class("a1"); c.String != "network" {
		t.Fatalf("INSERT 没写 error_class：%+v", c)
	}
	// 第二次同一个 attempt：改写成别的类别，必须覆盖
	second := base
	second.ID = "x2"
	second.Error, second.ErrorClass = &msg, "timeout"
	if err := s.AddRun(second); err != nil {
		t.Fatal(err)
	}
	if c, _ := class("a1"); c.String != "timeout" {
		t.Fatalf("upsert 没覆盖 error_class：%+v", c)
	}
	// 第三次成功：error 和 error_class 都要变回 NULL，不能留着上一次的类别
	third := base
	third.ID = "x3"
	if err := s.AddRun(third); err != nil {
		t.Fatal(err)
	}
	if c, e := class("a1"); c.Valid || e.Valid {
		t.Fatalf("成功的一次还留着失败标记：%+v %+v", c, e)
	}
	// 没有 attempt_id 的行：不分类就是 NULL（读的时候现算），不是空串
	old := base
	old.ID, old.Identity = "x4", kernel.Invocation{}
	old.Error = &msg
	if err := s.AddRun(old); err != nil {
		t.Fatal(err)
	}
	var c sql.NullString
	if err := db.QueryRow(`SELECT error_class FROM model_runs WHERE id = 'x4'`).Scan(&c); err != nil || c.Valid {
		t.Fatalf("没分类的行应是 NULL：%+v %v", c, err)
	}
}
