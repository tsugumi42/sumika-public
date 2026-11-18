package desktopread

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// 重启后读聊天记录：中断回合的步骤挂在“程序退出时这一轮被中断”的系统行上，投影成状态为已停止、
// 展开处第一句写“程序退出时中断”，命令还在；旧版本收拾的行（没有标记、只有写下的预览）也认成同样的状态；
// 点停止被切断的步骤是已停止；正常出错的仍是出错。
func TestProcessOfInterruptedTurn(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	st, _ := set.Platform("local")
	const conv = "console:a"
	mustOK := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	say := func(kind, id, text string, at int64) {
		_, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: kind, ActorID: id, Origin: kind, Kind: "message", Text: text, CreatedAt: at}, []string{"a"})
		mustOK(err)
	}
	tool := func(id, turn, name, args string, at int64) {
		a := args
		mustOK(st.StepStart(store.Step{ID: id, CharacterID: "a", Conversation: conv, TurnID: turn, Kind: "tool", Name: name, Args: &a, Status: "running", StartedAt: at}))
	}
	mustOK(st.TurnStart("T", "a", conv, "console", 1000))
	say("owner", "owner", "跑个命令", 1001)
	tool("t-done", "T", "read", `{"path":"a.txt"}`, 1002)
	mustOK(st.StepEnd("t-done", "内容", false, 1003, nil))
	tool("t-ps", "T", "powershell", `{"command":"Start-Sleep 30"}`, 1004)
	tool("t-old", "T", "powershell", `{"command":"Get-Date"}`, 1005)
	// t-old 模拟旧版本收拾的行：出错、写了预览、没有标记
	if _, err := st.Trace.Exec(`UPDATE steps SET status = 'error', is_error = 1, ended_at = 1006, preview = ? WHERE id = 't-old'`, store.InterruptedPreview()); err != nil {
		t.Fatal(err)
	}
	tool("t-stop", "T", "powershell", `{"command":"Get-Process"}`, 1007)
	mustOK(st.StepEnd("t-stop", "Operation aborted", true, 1008, map[string]any{"interrupted": store.InterruptedByStop}))
	tool("t-bad", "T", "read", `{"path":"没有.txt"}`, 1009)
	mustOK(st.StepEnd("t-bad", "没有这个文件", true, 1010, nil))
	if _, err := st.Trace.Exec(`UPDATE steps SET details_json = '{"interrupted":"exit"}', status = 'error', is_error = 1, ended_at = 1011, preview = ? WHERE id = 't-ps'`, store.InterruptedPreview()); err != nil {
		t.Fatal(err)
	}
	mustOK(st.TurnEnd("T", store.TurnInterrupted, 1011))
	if seq, err := st.SurfaceInterrupted("T", "a", conv, 1012); err != nil || seq == 0 {
		t.Fatalf("留行：%d %v", seq, err)
	}
	say("owner", "owner", "回来了", 2000)

	res, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}, Character: "a", Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	var row *api.MessageView
	var order []string
	for i := range res.Value.Items {
		m := &res.Value.Items[i]
		order = append(order, m.Text)
		if m.Kind == "notice" {
			row = m
		}
	}
	if row == nil || len(row.Process) != 5 || !strings.Contains(row.Text, "2 个工具没有跑完") {
		t.Fatalf("系统行带着 5 步：%+v 顺序 %v", row, order)
	}
	if order[0] != "跑个命令" || order[len(order)-1] != "回来了" {
		t.Fatalf("系统行排在中断的那一轮之后、下一句之前：%v", order)
	}
	by := map[string]api.LocalProcessStep{}
	for _, p := range row.Process {
		by[p.ID] = p
	}
	if p := by["t-done"]; p.State != "done" || p.Output != "内容" || p.Target != "a.txt" {
		t.Fatalf("跑完的照旧：%+v", p)
	}
	for _, id := range []string{"t-ps", "t-old"} {
		p := by[id]
		if p.State != "stopped" || !strings.HasPrefix(p.Output, ExitInterruptedNote) || p.Tool != "powershell" || p.Target == "" {
			t.Fatalf("%s 该是已停止、写程序退出时中断、命令还在：%+v", id, p)
		}
		if strings.Contains(p.Output, store.InterruptedPreview()) {
			t.Fatalf("%s 旧预览不重复：%q", id, p.Output)
		}
	}
	if p := by["t-stop"]; p.State != "stopped" || strings.Contains(p.Output, "程序退出时中断") {
		t.Fatalf("点停止被切断的是已停止，不说程序退出：%+v", p)
	}
	if p := by["t-bad"]; p.State != "failed" {
		t.Fatalf("正常出错的仍是出错：%+v", p)
	}
}
