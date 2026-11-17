package main

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// 输入框之外的 Esc 停止和 Ctrl+V 粘贴（workbench_composer_keys.go）。

type stopCounting struct {
	*fakeInputs
	stops chan struct{}
}

func (f *stopCounting) StopLocalRun(context.Context, api.ConversationKey, string, uint64) (api.StopReceipt, error) {
	f.stops <- struct{}{}
	return api.StopReceipt{RunID: "r1"}, nil
}

// keysRig 是新界面聊天区加一个会开的对话框（新建群聊，里面有别的输入框），回复正在进行。
type keysRig struct {
	*wbRig
	stops chan struct{}
}

func newKeysRig(t *testing.T, busy bool) *keysRig {
	t.Helper()
	f := &stopCounting{fakeInputs: &fakeInputs{}, stops: make(chan struct{}, 4)}
	r := newWbRig(t, f.fakeInputs)
	r.a.reader = f
	r.tt = ui.NewTester(func(c *ui.Context) {
		r.a.workbenchChat(c)
		r.a.wbGroupDialog(c, r.a.wbKit(r.a.look.dark, c.Theme().Font))
	}, 900, 700)
	r.tt.SetScale(1.25)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	if busy {
		st := r.a.localRun(r.key.Conversation)
		st.busy, st.runID, st.through = true, "r1", 1
		st.startRun()
	}
	r.settle()
	return &keysRig{wbRig: r, stops: f.stops}
}

// stopped 等后台收到停止请求（调用在协程里发出，机器忙时晚很久）；wantStop 为真时等到 uiWaitLimit，
// 为假是确认“不会发”：只看一小段时间，到点没来就当没发（负向断言没有等不完的办法）。
func (r *keysRig) stopped(t *testing.T, wantStop bool) bool {
	t.Helper()
	wait := 300 * time.Millisecond
	if wantStop {
		wait = uiWaitLimit
	}
	select {
	case <-r.stops:
		return true
	case <-time.After(wait):
		return false
	}
}

func TestEscStopsRunWhenFocusIsOutsideInput(t *testing.T) {
	r := newKeysRig(t, true)
	r.tt.Key(0, ui.KeyEscape)
	r.settle()
	if !r.stopped(t, true) {
		t.Fatal("回复进行中、焦点不在输入框，按 Esc 应当停止这一轮")
	}
	if !r.a.localRun(r.key.Conversation).stopping {
		t.Fatal("停止没有走 stopComposer")
	}
}

func TestEscDoesNothingWhenIdle(t *testing.T) {
	r := newKeysRig(t, false)
	r.tt.Key(0, ui.KeyEscape)
	r.settle()
	if r.stopped(t, false) {
		t.Fatal("没有在回复时 Esc 不该调用停止")
	}
}

func TestEscYieldsToOpenDialog(t *testing.T) {
	r := newKeysRig(t, true)
	r.a.gc.open = true
	r.settle()
	r.tt.Key(0, ui.KeyEscape)
	r.settle()
	if r.stopped(t, false) || r.a.localRun(r.key.Conversation).stopping {
		t.Fatal("对话框开着时 Esc 只该关对话框，不能停止回复")
	}
	if r.a.gc.open {
		t.Fatal("Esc 没有关掉对话框")
	}
	r.tt.Key(0, ui.KeyEscape) // 对话框关了以后才轮到停止
	r.settle()
	if !r.stopped(t, true) {
		t.Fatal("对话框关掉后 Esc 应当停止")
	}
}

func TestEscYieldsToApprovalCard(t *testing.T) {
	r := newKeysRig(t, true)
	r.a.approvals = []api.LocalApproval{{ID: "ap1", Key: r.key, Title: "编造的审批", Tool: "read"}}
	r.settle()
	r.tt.Key(0, ui.KeyEscape)
	r.settle()
	if r.stopped(t, false) {
		t.Fatal("审批卡开着时 Esc 归审批卡，不能停止回复")
	}
}

func TestEscInsideInputKeepsOldBehaviour(t *testing.T) {
	r := newKeysRig(t, true)
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.Type("还没发的话")
	r.settle()
	r.tt.Key(0, ui.KeyEscape)
	r.settle()
	if r.stopped(t, false) || r.p.Main.Text != "还没发的话" {
		t.Fatalf("输入框里按 Esc 不该停止回复也不该动文字：%q", r.p.Main.Text)
	}
}

func TestPasteTextOutsideInputGoesToComposerAndFocuses(t *testing.T) {
	r := newKeysRig(t, false)
	r.tt.SetClipboard("别处复制的话")
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if r.p.Main.Text != "别处复制的话" {
		t.Fatalf("文字没有进输入框：%q", r.p.Main.Text)
	}
	if !r.tt.Focused("发一条消息") {
		t.Fatal("粘贴后输入框应当拿到焦点")
	}
}

func TestPasteImageOutsideInputBecomesAttachment(t *testing.T) {
	r := newKeysRig(t, false)
	_, data := tinyPNGFile(t, t.TempDir(), "图.png")
	r.tt.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, data))))
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if n := len(r.p.Main.Atts); n != 1 || r.p.Main.Atts[0].Kind != 0 {
		t.Fatalf("图片应当变成一个附件，实际 %+v", r.p.Main.Atts)
	}
	if !r.tt.Focused("发一条消息") {
		t.Fatal("粘贴图片后输入框应当拿到焦点")
	}
}

func TestPasteIntoOtherInputIsNotStolen(t *testing.T) {
	r := newKeysRig(t, false)
	r.a.gc.open = true
	r.settle()
	if err := r.tt.Click("群聊名称"); err != nil {
		t.Fatal(err)
	}
	r.tt.SetClipboard("群名")
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if r.a.gc.name != "群名" {
		t.Fatalf("别的输入框里粘贴应当进它自己：%q", r.a.gc.name)
	}
	if r.p.Main.Text != "" {
		t.Fatalf("输入区不该抢到粘贴：%q", r.p.Main.Text)
	}
}

func TestPasteWithoutComposerIsIgnored(t *testing.T) {
	r := newKeysRig(t, false)
	qq := api.ConversationKey{Platform: "qq", Channel: "napcat", Scene: "private", Conversation: "qq:private:编造"}
	r.a.keys[qq.Conversation] = qq
	r.a.sel = qq.Conversation
	r.settle()
	r.tt.SetClipboard("不该进任何输入框")
	r.tt.Key(ui.Cmd, ui.KeyV)
	r.settle()
	if p := r.a.composers[qq.Conversation]; p != nil && p.Main.Text != "" {
		t.Fatalf("只读会话没有输入框，不该处理粘贴：%q", p.Main.Text)
	}
	if r.p.Main.Text != "" {
		t.Fatalf("别的会话的输入框被写入了：%q", r.p.Main.Text)
	}
}

func TestUpRecallsSentAndListedOwnMessages(t *testing.T) {
	r := newKeysRig(t, false)
	r.a.msgs = append(r.a.msgs, &msg{id: "m2", mine: true, text: "列表里我说过的话", at: 2000})
	r.p.Remember("刚发出去的话")
	r.settle()
	if err := r.tt.Click("发一条消息"); err != nil {
		t.Fatal(err)
	}
	r.tt.Key(0, ui.KeyUp)
	r.settle()
	if r.p.Main.Text != "刚发出去的话" {
		t.Fatalf("↑ 应先取本次发出的话：%q", r.p.Main.Text)
	}
	r.tt.Key(0, ui.KeyUp)
	r.settle()
	if r.p.Main.Text != "列表里我说过的话" {
		t.Fatalf("再 ↑ 应取列表里自己的消息（角色的话不算）：%q", r.p.Main.Text)
	}
	r.tt.Key(0, ui.KeyEscape)
	r.settle()
	if r.p.Main.Text != "" {
		t.Fatalf("Esc 应还回空草稿：%q", r.p.Main.Text)
	}
}
