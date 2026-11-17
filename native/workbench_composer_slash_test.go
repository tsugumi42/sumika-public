package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// 斜杠命令补全和大段粘贴折叠接进新界面（workbench_composer.go、workbench_paste.go）。
// 命令名、文字都是编的；后台用 fakeInputs 记提交，不碰真实数据。

// slashInputs 在 fakeInputs 上加命令名单（后台的命令登记表由 internal/app 的测试覆盖）。
type slashInputs struct{ *fakeInputs }

func (slashInputs) SlashCommands() []api.SlashCommand {
	return []api.SlashCommand{{Name: "goal", Summary: "设定目标"}, {Name: "loop", Summary: "定期任务"}}
}

func newSlashRig(t *testing.T) (*wbRig, *fakeInputs) {
	t.Helper()
	f := &fakeInputs{}
	r := newWbRig(t, f)
	r.a.reader = slashInputs{f}
	r.settle()
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	return r, f
}

func TestWorkbenchSlashListUsesBackendCommandsAndEnterDoesNotSend(t *testing.T) {
	r, f := newSlashRig(t)
	r.tt.Type("/")
	r.settle()
	if !r.tt.HasText("设定目标") || !r.tt.HasText("定期任务") {
		t.Fatalf("输入 / 该列出后台登记的命令：%v", r.tt.Texts())
	}
	r.tt.Key(0, ui.KeyEnter)
	r.settle()
	if len(f.submissions()) != 0 || r.p.Main.Text != "/goal " {
		t.Fatalf("列表开着 Enter 只采纳不发送：%q 提交 %d", r.p.Main.Text, len(f.submissions()))
	}
	r.tt.Type("做点事")
	r.tt.Key(0, ui.KeyEnter)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || got[0].Text != "/goal 做点事" {
		t.Fatalf("采纳后接着打参数、再按 Enter 才发送：%+v", got)
	}
}

func TestWorkbenchSlashListHiddenWithoutBackendList(t *testing.T) {
	f := &fakeInputs{} // 没有 SlashLister：旧后台、只看历史
	r := newWbRig(t, f)
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.Type("/")
	r.settle()
	if r.tt.HasText("设定目标") {
		t.Fatalf("后台没给命令名单就不该弹列表：%v", r.tt.Texts())
	}
}

func TestWorkbenchFoldedPasteIsSentInTheTextAndNotAsAttachment(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	long := strings.Repeat("编造的长文。", 600) // 3600 字
	r.p.Main.Text = "帮我总结"
	if !r.p.PasteText(nil, long) {
		t.Fatal("超过 3000 字符该折叠")
	}
	r.a.submitComposer(f, r.key, r.p)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || got[0].Text != "" || len(got[0].Parts) != 1 {
		t.Fatalf("带粘贴附件走分块：%+v", got)
	}
	part := got[0].Parts[0]
	if len(part.Attachments) != 0 {
		t.Fatalf("折叠的粘贴不是后台的附件：%+v", part.Attachments)
	}
	want := "帮我总结\n\n【粘贴的内容，3600 字】\n" + strings.TrimSpace(long)
	if part.Text != want {
		t.Fatalf("正文该是打的字加粘贴全文，保留顺序：长度 %d，想要 %d", len(part.Text), len(want))
	}
	if r.p.Main.Text != "" || len(r.p.Main.Atts) != 0 {
		t.Fatal("收下后输入框和折叠的粘贴都该清空")
	}
}

func TestWorkbenchFoldedPasteOnlySendsWithoutTyping(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	long := strings.Repeat("x", chatui.FoldPasteRunes+1)
	r.p.PasteText(nil, long)
	if !r.p.Sendable() {
		t.Fatal("只有折叠的粘贴也该能发")
	}
	r.a.submitComposer(f, r.key, r.p)
	r.submitted(t)
	got := f.submissions()
	if len(got) != 1 || len(got[0].Parts) != 1 || !strings.HasSuffix(got[0].Parts[0].Text, long) {
		t.Fatalf("提交不对：%+v", got)
	}
}

// groupSpy 在 fakeInputs 上加本地群聊的写接口，只记下发出的文字。
type groupSpy struct {
	*fakeInputs
	sent chan string
}

func (g groupSpy) CreateLocalGroup(context.Context, api.LocalGroupCreate) (api.ConversationView, error) {
	return api.ConversationView{}, nil
}
func (g groupSpy) SendLocalGroup(_ context.Context, req api.LocalGroupSend) error {
	g.sent <- req.Text
	return nil
}
func (g groupSpy) StopLocalGroup(context.Context, api.ConversationKey) error { return nil }

func TestWorkbenchFoldedPasteInGroupIsInTheText(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	spy := groupSpy{f, make(chan string, 1)}
	k := r.key
	k.Scene = "group"
	long := strings.Repeat("y", chatui.FoldPasteRunes+1)
	r.p.Main.Text = "群里看看"
	r.p.PasteText(nil, long)
	r.a.submitComposer(spy, k, r.p)
	sent := localRecvString(t, spy.sent)
	if !strings.HasPrefix(sent, "群里看看\n\n【粘贴的内容，3001 字】\n") || !strings.HasSuffix(sent, long) {
		t.Fatalf("群聊也该带上粘贴全文：%d", len(sent))
	}
	if r.p.Main.Text != "" || len(r.p.Main.Atts) != 0 {
		t.Fatal("发出后该清空")
	}
}

func localRecvString(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(uiWaitLimit):
		t.Fatal("没有发出去")
		return ""
	}
}

func TestWorkbenchPasteOutsideInputFoldsToo(t *testing.T) {
	r := newKeysRig(t, false)
	long := strings.Repeat("z", chatui.FoldPasteRunes+1)
	r.tt.SetClipboard(long)
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if len(r.p.Main.Atts) != 1 || r.p.Main.Atts[0].Kind != chatui.AttText || r.p.Main.Text != "" {
		t.Fatalf("输入框之外粘贴长文字也该折叠：文字 %q 附件 %d", r.p.Main.Text, len(r.p.Main.Atts))
	}
	r.tt.SetClipboard("一句话")
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if r.p.Main.Text != "一句话" {
		t.Fatalf("短的照旧接在输入框里：%q", r.p.Main.Text)
	}
}

func TestWorkbenchPasteViewShowsFullTextInDialog(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	r.tt = ui.NewTester(func(c *ui.Context) {
		r.a.workbenchChat(c)
		r.a.wbPasteDialog(c, r.a.wbKit(r.a.look.dark, c.Theme().Font))
	}, 900, 700)
	r.tt.SetScale(1.25)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	long := "开头标记" + strings.Repeat("甲", chatui.FoldPasteRunes) + "结尾标记"
	r.p.PasteText(nil, long)
	r.settle()
	if err := r.tt.Click("查看粘贴的内容，3008 字"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if !r.a.pastePane.open || !r.tt.HasText("粘贴的内容 · 3008 字") {
		t.Fatalf("点药丸该弹出查看框：%v", r.tt.Texts())
	}
	if err := r.tt.Click("关闭粘贴的内容"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.a.pastePane.open || r.a.pastePane.text != "" {
		t.Fatal("关掉后状态该清掉")
	}
	if len(r.p.Main.Atts) != 1 {
		t.Fatal("看全文不该动药丸")
	}
}

// 本地群聊折叠的药丸也要能点开看全文（回归：OnOpenText 曾只在私聊里接上）。
func TestWorkbenchFoldedPasteInGroupCanBeOpened(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	spy := groupSpy{f, make(chan string, 1)}
	k := r.key
	k.Scene, k.Conversation = "group", "console:group00"
	r.a.keys[k.Conversation] = k
	r.a.sel, r.a.reader = k.Conversation, spy
	r.p = r.a.composerOf(k.Conversation)
	long := "开头" + strings.Repeat("乙", chatui.FoldPasteRunes) + "结尾"
	r.p.PasteText(nil, long)
	r.settle()
	if err := r.tt.Click("查看粘贴的内容，3004 字"); err != nil {
		t.Fatal(err)
	}
	if !r.a.pastePane.open || r.a.pastePane.text != long {
		t.Fatal("群聊里点药丸也该打开全文")
	}
}

// 待发气泡和 ↑ 回忆历史只带占位，不带粘贴全文（回归：曾把全文塞回输入框，绕过折叠）。
func TestWorkbenchFoldedPasteStaysOutOfPendingBubbleAndRecall(t *testing.T) {
	f := &fakeInputs{}
	r := newWbRig(t, f)
	long := strings.Repeat("丙", chatui.FoldPasteRunes+50)
	r.p.Main.Text = "帮我总结"
	r.p.PasteText(nil, long)
	r.a.submitComposer(f, r.key, r.p)
	want := "帮我总结\n\n【粘贴的内容，3050 字】"
	st := r.a.localRun(r.key.Conversation)
	if len(st.pending) != 1 || st.pending[0].text != want {
		t.Fatalf("待发气泡只该有占位：%+v", st.pending)
	}
	r.settle()
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.Key(0, ui.KeyUp)
	r.settle()
	if r.p.Main.Text != want {
		t.Fatalf("↑ 回忆出来的只该有占位，不超过折叠门槛：长度 %d", len([]rune(r.p.Main.Text)))
	}
}
