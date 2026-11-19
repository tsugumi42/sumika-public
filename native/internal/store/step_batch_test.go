package store

import (
	"path/filepath"
	"testing"

	"sumika-native/internal/kernel"
)

// 批次是第几次模型回复：同一次回复里前面的工具已经结束了，后面的也还算这一批；下一次回复的第一个工具才开新批。
// 审批身份的占位行（batch 默认 0、没开始过）不算上一批。旧写法按“写入那一刻还有没有工具在跑”算，
// 并发执行里快的工具先结束，第三个就被算成下一批（CI 上 TestSameAsBun/tool-more 时红时绿）。
func TestStepBatchFollowsModelReply(t *testing.T) {
	t.Parallel()
	db, err := OpenTraceFile(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Store{Trace: db}
	const turn = "T1"
	start := func(id string, ordinal int, approval bool) {
		t.Helper()
		in := kernel.Invocation{ToolID: id, RunID: turn}
		in.Scope.CharacterID, in.Scope.Conversation = "suzu", "console:suzu"
		if approval { // 要审批的工具：开始前先有一行身份占位
			if err := s.RecordStepIdentity(id, in, -1); err != nil {
				t.Fatal(err)
			}
		}
		args := "{}"
		if err := s.StepStart(Step{ID: id, CharacterID: "suzu", Conversation: "console:suzu", TurnID: turn, Kind: "tool", Name: "ls", Args: &args, Status: "running", StartedAt: 1000, Identity: in, Ordinal: &ordinal}); err != nil {
			t.Fatal(err)
		}
	}
	end := func(id string) {
		t.Helper()
		if err := s.StepEnd(id, "好", false, 1000, nil); err != nil {
			t.Fatal(err)
		}
	}
	// 第一次回复三个调用：前两个都结束了第三个才开始
	start("a", 0, false)
	end("a")
	start("b", 1, false)
	end("b")
	start("c", 2, false)
	end("c")
	// 第二次回复：开新批；第三次回复：再开一批（started_at 全一样也不能取到旧批号）
	start("d", 0, true)
	end("d")
	start("e", 0, true)
	if err := s.StepStart(Step{ID: "think", CharacterID: "suzu", Conversation: "console:suzu", TurnID: turn, Kind: "thinking", Status: "done", StartedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"a": 0, "b": 0, "c": 0, "d": 1, "e": 2, "think": 2}
	for id, w := range want {
		var got int64
		if err := db.QueryRow(`SELECT batch FROM steps WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("%s 的批次是 %d，该是 %d", id, got, w)
		}
	}
}
