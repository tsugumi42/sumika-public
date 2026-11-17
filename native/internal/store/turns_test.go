package store

// 回合的结局和收拾上一个进程没收尾的（表一 A9）：对着写明的预期测

import (
	"database/sql"
	"fmt"
	"testing"
)

type stepRow struct {
	status, preview, approval string
	isError                   int
	ended                     sql.NullInt64
}

func stepOf(t *testing.T, s *Store, id string) stepRow {
	t.Helper()
	var r stepRow
	var preview, approval sql.NullString
	if err := s.Trace.QueryRow(`SELECT status, preview, approval_json, is_error, ended_at FROM steps WHERE id = ?`, id).Scan(&r.status, &preview, &approval, &r.isError, &r.ended); err != nil {
		t.Fatal(err)
	}
	r.preview, r.approval = preview.String, approval.String
	return r
}

type turnRow struct {
	outcome, unknown sql.NullString
	ended            sql.NullInt64
}

func turnOf(t *testing.T, s *Store, id string) turnRow {
	t.Helper()
	var r turnRow
	if err := s.Trace.QueryRow(`SELECT outcome, unknown_tools, ended_at FROM turns WHERE id = ?`, id).Scan(&r.outcome, &r.unknown, &r.ended); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// 结局只写一次：先写的算，后写的不生效
func TestTurnOutcomeOnce(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	mustOK(t, s.TurnStart("T1", "suzu", "console:suzu", "console", 10))
	if r := turnOf(t, s, "T1"); r.outcome.Valid || r.ended.Valid {
		t.Fatalf("刚开始没有结局：%+v", r)
	}
	mustOK(t, s.TurnEnd("T1", "done", 20))
	mustOK(t, s.TurnEnd("T1", "error", 30))
	if r := turnOf(t, s, "T1"); r.outcome.String != "done" || r.ended.Int64 != 20 {
		t.Fatalf("先写的算：%+v", r)
	}
}

// 收拾：没结局的回合记成 interrupted、带上那时停在执行中的工具；执行中的工具记成出错（不知道结果），等审批的记成取消；
// 有结局的回合、结束了的步骤都不动；再收拾一次什么都不改；收拾过的回合再写结局也不生效
func TestSweepInterrupted(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	tool := func(id, turn, name string, at int64) {
		mustOK(t, s.StepStart(Step{ID: id, CharacterID: "suzu", Conversation: "console:suzu", TurnID: turn, Kind: "tool", Name: name, Status: "running", StartedAt: at}))
	}
	// 被杀的那一轮：两个工具在跑（先 write 后 powershell）、一个跑完了、一个在等审批
	mustOK(t, s.TurnStart("A", "suzu", "console:suzu", "console", 1))
	tool("a-write", "A", "write", 2)
	tool("a-read", "A", "read", 3)
	mustOK(t, s.StepEnd("a-read", "读到了", false, 4, nil))
	tool("a-ps", "A", "powershell", 5)
	tool("a-wait", "A", "write", 6)
	mustOK(t, s.StepApproval("a-wait", "suzu", "waiting", `{"title":"写文件"}`))
	// 正常结束的一轮、被主人停下的一轮（审批取消了）
	mustOK(t, s.TurnStart("B", "suzu", "console:suzu", "console", 7))
	tool("b-read", "B", "read", 8)
	mustOK(t, s.StepEnd("b-read", "好", false, 9, nil))
	mustOK(t, s.TurnEnd("B", "done", 10))
	mustOK(t, s.TurnStart("C", "suzu", "console:suzu", "console", 11))
	tool("c-write", "C", "write", 12)
	mustOK(t, s.StepApproval("c-write", "suzu", "cancelled", `{"title":"写文件","decision":"cancelled"}`))
	mustOK(t, s.StepEnd("c-write", "Operation aborted", true, 13, nil))
	mustOK(t, s.TurnEnd("C", "stopped", 14))

	sw, err := s.SweepInterrupted(100)
	if err != nil || sw != (TurnsSwept{Turns: 1, Unknown: 2, Waiting: 1}) {
		t.Fatalf("改了几样：%+v %v", sw, err)
	}
	if r := turnOf(t, s, "A"); r.outcome.String != TurnInterrupted || r.ended.Int64 != 100 || r.unknown.String != `["write","powershell"]` {
		t.Fatalf("被杀的那一轮：%+v", r)
	}
	for _, id := range []string{"a-write", "a-ps"} {
		if r := stepOf(t, s, id); r.status != "error" || r.isError != 1 || r.preview != unknownPreview || r.ended.Int64 != 100 {
			t.Fatalf("%s 停在执行中：%+v", id, r)
		}
	}
	if r := stepOf(t, s, "a-wait"); r.status != "cancelled" || r.isError != 1 || r.preview != abandonedPreview || r.approval != `{"title":"写文件","decision":"cancelled"}` {
		t.Fatalf("等审批的：%+v", r)
	}
	if r := stepOf(t, s, "a-read"); r.status != "done" || r.preview != "读到了" || r.ended.Int64 != 4 {
		t.Fatalf("跑完了的不动：%+v", r)
	}
	if b, c := turnOf(t, s, "B"), turnOf(t, s, "C"); b.outcome.String != "done" || c.outcome.String != "stopped" || b.unknown.Valid || c.ended.Int64 != 14 {
		t.Fatalf("有结局的不动：%+v %+v", b, c)
	}
	if r := stepOf(t, s, "c-write"); r.status != "cancelled" || r.preview != "Operation aborted" || r.ended.Int64 != 13 {
		t.Fatalf("主人取消过的不动：%+v", r)
	}
	if sw, err := s.SweepInterrupted(200); err != nil || sw != (TurnsSwept{}) {
		t.Fatalf("再收拾一次什么都不改：%+v %v", sw, err)
	}
	mustOK(t, s.TurnEnd("A", "done", 300))
	if r := turnOf(t, s, "A"); r.outcome.String != TurnInterrupted || r.ended.Int64 != 100 {
		t.Fatalf("收拾过的回合再写结局不生效：%+v", r)
	}
}

// 还没提醒到的被打断回合：只看同一个会话、不算这一轮；越过被打断的、没拼过参考信息的（一开始就出错、回复前压缩时被停下），
// 连着被杀两次也还在；拼过参考信息、没被打断的那一轮之后不再提（拼过以后出错的也算提醒过了）；几轮被打断的工具按先后合起来
func TestInterruptedTools(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	conv := "console:suzu"
	at := int64(0)
	start := func(id, c string) {
		at++
		mustOK(t, s.TurnStart(id, "suzu", c, "console", at))
	}
	running := func(turn string, names ...string) {
		for _, n := range names {
			at++
			mustOK(t, s.StepStart(Step{ID: fmt.Sprint(turn, "-", at), CharacterID: "suzu", Conversation: conv, TurnID: turn, Kind: "tool", Name: n, Status: "running", StartedAt: at}))
		}
	}
	end := func(id, outcome string, ran bool) {
		if ran {
			mustOK(t, s.TurnRan(id))
		}
		at++
		mustOK(t, s.TurnEnd(id, outcome, at))
	}
	killed := func() { // 进程被杀、再起来
		at++
		if _, err := s.SweepInterrupted(at); err != nil {
			t.Fatal(err)
		}
	}
	check := func(except string, want []string) {
		t.Helper()
		got, err := s.InterruptedTools("suzu", conv, except)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("不算 %s：%v %v，应该是 %v", except, got, err, want)
		}
	}
	start("T0", conv)
	end("T0", "done", true) // 被打断之前正常的一轮
	start("T1", conv)
	// 并行的一批里两个 write、一个 powershell 都没跑完：同名的只提一次，按先后
	running("T1", "write", "powershell", "write")
	start("Q1", "qq:group:1") // 别的会话、没有不知道结果的工具
	killed()
	both := []string{"write", "powershell"}
	start("T2", conv)
	check("T2", both)
	end("T2", "error", false) // 一开始就出错（读卡、找模型），没拼参考信息：下一轮还要提
	start("T3", conv)
	check("T3", both)
	end("T3", "stopped", false) // 回复前压缩时被停下，没拼参考信息：还要提
	start("T4", conv)
	check("T4", both)
	mustOK(t, s.TurnRan("T4")) // 拼了参考信息（提醒交给她了），还没回完又被杀：实例跟着进程没了，还要提
	killed()
	start("T5", conv)
	check("T5", both)
	end("T5", "error", true) // 拼过参考信息以后出错：提醒在实例里了，不再提
	start("T6", conv)
	check("T6", nil)
	end("T6", "done", true)
	// 又被打断两次、各有不知道结果的工具：按先后合起来
	start("T7", conv)
	running("T7", "edit")
	killed()
	start("T8", conv)
	mustOK(t, s.TurnRan("T8"))
	running("T8", "write")
	killed()
	start("T9", conv)
	check("T9", []string{"edit", "write"})
	if got, _ := s.InterruptedTools("suzu", "qq:group:1", "X"); len(got) != 0 {
		t.Fatalf("被打断、但没有不知道结果的工具：不提：%v", got)
	}
	if got, _ := s.InterruptedTools("rin", conv, "X"); len(got) != 0 {
		t.Fatalf("别的角色：%v", got)
	}
}
