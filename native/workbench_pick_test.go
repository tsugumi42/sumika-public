package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/turn"
)

// 多选上限和一次发送最多的块数是同一个数。
func TestPickLimitMatchesInputParts(t *testing.T) {
	t.Parallel()
	if chatui.MaxPick != turn.MaxInputParts {
		t.Fatalf("多选上限 %d，一次发送最多 %d 块", chatui.MaxPick, turn.MaxInputParts)
	}
}

// 多选：右键"多选"进入，输入区换成操作条；点别的消息切换；复制按"名字 时:分"加正文、条间空一行；
// 引用 ▸ 给每条可引用的加一张卡（按时间先后、对象相同），撤回的跳过并说跳过了几条，然后退出；Esc 退出；切会话退出。
func TestWhisperMultiSelect(t *testing.T) {
	h, w, g := whisperGroup(t)
	var others []*msg
	h.until(t, "两位都回了", func() bool {
		others = others[:0]
		for _, m := range h.a.msgs {
			if m.isChar && m.liveKind == liveNone && m.text != "" {
				others = append(others, m)
			}
		}
		return len(others) >= 2
	})
	h.rightClickText(t, "群里编的一句话")
	if err := h.tt.ChooseMenuItem("多选"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !h.a.picking() || !h.tt.HasText("已选 1 条") {
		t.Fatalf("应进入多选、输入区换成操作条：%v", h.tt.Texts())
	}
	if _, ok := h.tt.Find("发一条消息"); ok {
		t.Fatal("多选时不该有输入框")
	}
	for range 2 { // 第一个没选上的就是下一条
		if err := h.tt.Click("选择这条消息"); err != nil {
			t.Fatal(err)
		}
		h.tt.Frame()
	}
	whisperShot(t, h.tt, "5-多选和操作条", "聊天记录", "多选操作")
	if len(h.a.pick.IDs) != 3 || !h.tt.HasText("已选 3 条") {
		t.Fatalf("应选上 3 条：%v", h.a.pick.IDs)
	}
	if err := h.tt.Click("取消选择这条消息"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if len(h.a.pick.IDs) != 2 {
		t.Fatalf("点已选的应取消：%v", h.a.pick.IDs)
	}
	if err := h.tt.Click("选择这条消息"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.click(t, "复制")
	clip := h.tt.Clipboard()
	if !strings.HasPrefix(clip, "主人 ") || !strings.Contains(clip, "群里编的一句话") || strings.Count(clip, "\n\n") != 2 {
		t.Fatalf("复制格式不对：%q", clip)
	}
	if h.a.picking() {
		t.Fatal("复制后退出多选")
	}
	// 再选三条，其中一条标成撤回：引用时跳过它
	first := h.a.msgs[slices.IndexFunc(h.a.msgs, func(m *msg) bool { return m.text == "群里编的一句话" })].id
	h.a.pick.Start(g.Conversation, first)
	h.a.pick.IDs = append(h.a.pick.IDs, others[0].id, others[1].id)
	others[1].recalled = true
	h.tt.Frame()
	if !h.tt.HasText("其中 1 条没有文字或已撤回，引用时跳过") {
		t.Fatalf("操作条应提示跳过：%v", h.tt.Texts())
	}
	h.click(t, "引用")
	if err := h.tt.ChooseMenuItem("编造角色char00"); err != nil {
		t.Fatalf("%v %v", err, h.tt.Texts())
	}
	h.tt.Frame()
	p := h.a.composerOf(g.Conversation)
	if h.a.picking() || len(p.Cards) != 2 || p.Notice != "已跳过 1 条没有文字或已撤回的" {
		t.Fatalf("引用后：多选 %v，卡 %d，提示 %q", h.a.picking(), len(p.Cards), p.Notice)
	}
	if p.Cards[0].Quote.MessageID != first || !slices.Equal(p.Cards[1].To, []string{"char00"}) {
		t.Fatalf("卡按时间先后、对象相同：%+v %+v", p.Cards[0].Quote, p.Cards[1])
	}
	if len(w.seen()) != 0 {
		t.Fatalf("多选引用只是加卡，不发送：%q", w.seen())
	}
	// Esc 退出
	h.a.pick.Start(g.Conversation, others[0].id)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if h.a.picking() {
		t.Fatal("Esc 应退出多选")
	}
	// 切会话退出
	h.a.pick.Start(g.Conversation, others[0].id)
	h.a.open("console:char00")
	h.until(t, "切到私聊", func() bool { return h.a.firstDone && h.a.sel == "console:char00" })
	if h.a.picking() || h.a.pick.Conv != "" {
		t.Fatal("切会话应退出多选")
	}
}
