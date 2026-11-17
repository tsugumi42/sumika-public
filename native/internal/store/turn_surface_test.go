package store

// 被中断的回合：步骤标记、退出时的收尾、给没回复的步骤一个落脚的系统行（规格-本地回合显示、规格-停止三段）。
// 旧库、空库、异常行：见每个用例的注释。

import (
	"database/sql"
	"testing"
)

func detailsOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var d sql.NullString
	if err := s.Trace.QueryRow(`SELECT details_json FROM steps WHERE id = ?`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d.String
}

func toolStep(t *testing.T, s *Store, id, turn, name string, at int64) {
	t.Helper()
	mustOK(t, s.StepStart(Step{ID: id, CharacterID: "suzu", Conversation: "console:suzu", TurnID: turn, Kind: "tool", Name: name, Status: "running", StartedAt: at}))
}

func noticeCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Events.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'notice' AND origin = 'system:turn'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 收拾时给停在执行中的工具写“程序退出时中断”的标记；SweptWithOrphans 只认这一次收拾的、有没挂上回复的工具步骤的回合。
func TestSweepMarksExitAndFindsOrphans(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	mustOK(t, s.TurnStart("OLD", "suzu", "console:suzu", "console", 1)) // 更早的被打断回合（旧库里留着的）：这一次不替它补行
	toolStep(t, s, "old-ps", "OLD", "powershell", 2)
	if _, err := s.SweepInterrupted(50); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.TurnStart("A", "suzu", "console:suzu", "console", 60))
	toolStep(t, s, "a-ps", "A", "powershell", 61)
	toolStep(t, s, "a-read", "A", "read", 62)
	mustOK(t, s.StepEnd("a-read", "好", false, 63, nil))
	mustOK(t, s.TurnStart("B", "suzu", "console:suzu", "console", 64)) // 没有工具步骤
	if _, err := s.SweepInterrupted(100); err != nil {
		t.Fatal(err)
	}
	if got := detailsOf(t, s, "a-ps"); got != `{"interrupted":"exit"}` {
		t.Fatalf("停在执行中的写上标记：%q", got)
	}
	if got := detailsOf(t, s, "a-read"); got != "" {
		t.Fatalf("跑完的不动：%q", got)
	}
	got, err := s.SweptWithOrphans(100)
	if err != nil || len(got) != 1 || got[0] != (SweptTurn{ID: "A", CharacterID: "suzu", Conversation: "console:suzu"}) {
		t.Fatalf("只有 A：%+v %v", got, err)
	}
	if got, _ := s.SweptWithOrphans(50); len(got) != 1 || got[0].ID != "OLD" {
		t.Fatalf("按收拾的时刻分：%+v", got)
	}
	if got, _ := s.SweptWithOrphans(999); len(got) != 0 {
		t.Fatalf("别的时刻没有：%+v", got)
	}
}

// 正常退出收尾：只改标成 stop 的步骤，正常出错的不动；名字记进 unknown_tools；重复调用什么都不改；没有被切断的不动回合行（空库、没有步骤）。
func TestMarkExitInterrupted(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	mustOK(t, s.TurnStart("T", "suzu", "console:suzu", "console", 1))
	if n, err := s.MarkExitInterrupted("T"); err != nil || n != 0 || turnOf(t, s, "T").unknown.Valid {
		t.Fatalf("没有步骤：%d %v %+v", n, err, turnOf(t, s, "T"))
	}
	if n, err := s.MarkExitInterrupted("没有这个回合"); err != nil || n != 0 {
		t.Fatalf("回合不存在也不报错：%d %v", n, err)
	}
	toolStep(t, s, "t-ps", "T", "powershell", 2)
	mustOK(t, s.StepEnd("t-ps", "Operation aborted", true, 3, map[string]any{"interrupted": InterruptedByStop}))
	toolStep(t, s, "t-bad", "T", "read", 4)
	mustOK(t, s.StepEnd("t-bad", "没有这个文件", true, 5, nil)) // 正常出错：不是被切断的
	toolStep(t, s, "t-edit", "T", "edit", 6)
	mustOK(t, s.StepEnd("t-edit", "改好了", false, 7, map[string]any{"diff": "-a", "patch": "p"})) // 带自己的详情：不动
	n, err := s.MarkExitInterrupted("T")
	if err != nil || n != 1 {
		t.Fatalf("只有 powershell：%d %v", n, err)
	}
	if got := detailsOf(t, s, "t-ps"); got != `{"interrupted":"exit"}` {
		t.Fatalf("改成程序退出时中断：%q", got)
	}
	if got := detailsOf(t, s, "t-bad"); got != "" {
		t.Fatalf("正常出错的不动：%q", got)
	}
	if got := detailsOf(t, s, "t-edit"); got == "" || got == `{"interrupted":"exit"}` {
		t.Fatalf("自己带详情的不动：%q", got)
	}
	if r := turnOf(t, s, "T"); r.unknown.String != `["powershell"]` {
		t.Fatalf("名字记下：%+v", r)
	}
	if n, err := s.MarkExitInterrupted("T"); err != nil || n != 0 {
		t.Fatalf("重复调用什么都不改：%d %v", n, err)
	}
	if r := turnOf(t, s, "T"); r.unknown.String != `["powershell"]` {
		t.Fatalf("重复调用不覆盖：%+v", r)
	}
}

// 提醒：被停下的回合带着 unknown_tools 也算（正常退出的）；坏掉的 unknown_tools 跳过、不拦着别的（异常行）。
func TestInterruptedToolsFromQuitAndBadRow(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	conv := "console:suzu"
	mustOK(t, s.TurnStart("T1", "suzu", conv, "console", 1))
	mustOK(t, s.TurnRan("T1"))
	toolStep(t, s, "t1-ps", "T1", "powershell", 2)
	mustOK(t, s.StepEnd("t1-ps", "Operation aborted", true, 3, map[string]any{"interrupted": InterruptedByStop}))
	mustOK(t, s.TurnEnd("T1", "stopped", 4))
	if _, err := s.MarkExitInterrupted("T1"); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.TurnStart("T2", "suzu", conv, "console", 5)) // 异常行：unknown_tools 不是 JSON
	mustOK(t, s.TurnRan("T2"))
	mustOK(t, s.TurnEnd("T2", "stopped", 6))
	if _, err := s.Trace.Exec(`UPDATE turns SET unknown_tools = '不是json' WHERE id = 'T2'`); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.TurnStart("T3", "suzu", conv, "console", 7))
	got, err := s.InterruptedTools("suzu", conv, "T3")
	if err != nil || len(got) != 1 || got[0] != "powershell" {
		t.Fatalf("坏行跳过、别的照常：%v %v", got, err)
	}
	mustOK(t, s.TurnRan("T3"))
	mustOK(t, s.TurnEnd("T3", "done", 8))
	mustOK(t, s.TurnStart("T4", "suzu", conv, "console", 9))
	if got, _ := s.InterruptedTools("suzu", conv, "T4"); len(got) != 0 {
		t.Fatalf("拼过参考信息、正常结束的一轮之后不再提：%v", got)
	}
}

// 给没回复的步骤一个落脚的系统行：挂上、只出一行、没步骤不写、已经挂在回复上的不算、被切断的数进文字。
func TestSurfaceInterrupted(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	conv := "console:suzu"
	mustOK(t, s.TurnStart("E", "suzu", conv, "console", 1)) // 空回合：没有任何步骤
	if seq, err := s.SurfaceInterrupted("E", "suzu", conv, 10); err != nil || seq != 0 || noticeCount(t, s) != 0 {
		t.Fatalf("没步骤不写行：%d %v", seq, err)
	}
	// 已经挂在回复上的步骤不算：这一轮有回复，步骤跟着回复显示
	mustOK(t, s.TurnStart("R", "suzu", conv, "console", 2))
	toolStep(t, s, "r-read", "R", "read", 3)
	mustOK(t, s.StepEnd("r-read", "好", false, 4, nil))
	mustOK(t, s.AttachSteps("R", 777))
	if seq, err := s.SurfaceInterrupted("R", "suzu", conv, 10); err != nil || seq != 0 || noticeCount(t, s) != 0 {
		t.Fatalf("已挂在回复上的不重复挂：%d %v", seq, err)
	}
	// 中断的回合：一个跑完的 read、一个被切断的 write
	mustOK(t, s.TurnStart("A", "suzu", conv, "console", 20))
	toolStep(t, s, "a-read", "A", "read", 21)
	mustOK(t, s.StepEnd("a-read", "内容", false, 22, nil))
	toolStep(t, s, "a-write", "A", "write", 23)
	if _, err := s.SweepInterrupted(100); err != nil {
		t.Fatal(err)
	}
	seq, err := s.SurfaceInterrupted("A", "suzu", conv, 100)
	if err != nil || seq == 0 || noticeCount(t, s) != 1 {
		t.Fatalf("留一行：%d %v", seq, err)
	}
	var text, actor, kind string
	if err := s.Events.QueryRow(`SELECT text, actor_id, kind FROM events WHERE seq = ?`, seq).Scan(&text, &actor, &kind); err != nil || kind != "notice" || actor != "suzu" || text != "程序退出时，这一轮被中断了：1 个工具没有跑完，结果不知道" {
		t.Fatalf("系统行：%q %q %q %v", text, actor, kind, err)
	}
	steps, err := s.ReadSteps(conv, seq)
	if err != nil || len(steps) != 2 || steps[0].Name != "read" || steps[0].Interrupted != "" || steps[1].Name != "write" || steps[1].Interrupted != InterruptedByExit {
		t.Fatalf("两步都挂上，read 不是中断的：%+v %v", steps, err)
	}
	if again, err := s.SurfaceInterrupted("A", "suzu", conv, 200); err != nil || again != 0 || noticeCount(t, s) != 1 {
		t.Fatalf("重复调用：步骤已经挂上了，什么都不再写：%d %v", again, err)
	}
	// 挂步骤那一步没做成（崩在中间）：行已有、步骤没挂，再调一次接着挂在同一行上，不出第二行
	mustOK(t, s.TurnStart("B", "suzu", conv, "console", 30))
	toolStep(t, s, "b-ps", "B", "powershell", 31)
	first, err := s.AppendEvent(NewEvent{Channel: "console", Conversation: conv, ActorKind: "system", ActorID: "suzu", Origin: "system:" + SurfaceSource, Kind: "notice", Text: "x",
		Segments: map[string]any{"notice": "system", "source": SurfaceSource, "turn": "B"}, CreatedAt: 40}, []string{"suzu"})
	mustOK(t, err)
	if got, err := s.SurfaceInterrupted("B", "suzu", conv, 50); err != nil || got != first || noticeCount(t, s) != 2 {
		t.Fatalf("接着用已有的行：%d %d %v", got, first, err)
	}
	if steps, _ := s.ReadSteps(conv, first); len(steps) != 1 || steps[0].Name != "powershell" {
		t.Fatalf("挂上了：%+v", steps)
	}
}
