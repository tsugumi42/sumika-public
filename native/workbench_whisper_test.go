package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/llm/llmtest"
)

// observedWhisper 记下界面调了哪条写入口（悄悄话、群消息）；请求原样交给真后台。
type observedWhisper struct {
	api.Reader
	api.LocalGroupWriter
	wc    api.WhisperChat
	mu    sync.Mutex
	calls []string
	parts [][]api.LocalPart
}

func (w *observedWhisper) SendWhisper(ctx context.Context, k api.ConversationKey, parts []api.LocalPart) (api.WhisperReceipt, error) {
	w.mu.Lock()
	w.calls = append(w.calls, "whisper")
	w.parts = append(w.parts, parts)
	w.mu.Unlock()
	return w.wc.SendWhisper(ctx, k, parts)
}

func (w *observedWhisper) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	w.mu.Lock()
	w.calls = append(w.calls, "group:"+req.Text)
	w.mu.Unlock()
	return w.LocalGroupWriter.SendLocalGroup(ctx, req)
}

func (w *observedWhisper) seen() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func observeWhisper(h *localUIHarness) *observedWhisper {
	r := h.a.reader
	w := &observedWhisper{Reader: r, LocalGroupWriter: r.(api.LocalGroupWriter), wc: r.(api.WhisperChat)}
	h.a.reader = w
	return w
}

// whisperGroup 开一个编造的本地群（char00、char01），在群里发一句，等这句出现在列表里。
func whisperGroup(t *testing.T) (*localUIHarness, *observedWhisper, api.ConversationKey) {
	t.Helper()
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("编造的处理记录")}, nil)
	g := localUIGroup(t, h, "编造悄悄话群")
	w := observeWhisper(h)
	h.until(t, "群出现在会话表", func() bool { cv := h.a.convByID(g.Conversation); return cv != nil && len(cv.Observers) == 2 })
	h.a.open(g.Conversation)
	h.until(t, "群打开", func() bool { return h.a.firstDone && h.a.sel == g.Conversation })
	h.wbSend(t, "群里编的一句话")
	h.until(t, "群消息落库", func() bool {
		i := slices.IndexFunc(h.a.msgs, func(m *msg) bool { return m.text == "群里编的一句话" && m.liveKind == liveNone })
		return i >= 0 && !h.a.localRun(g.Conversation).busy
	})
	w.mu.Lock()
	w.calls, w.parts = nil, nil // 只看接下来界面调了什么
	w.mu.Unlock()
	return h, w, g
}

func (h *localUIHarness) rightClickText(t *testing.T, text string) {
	t.Helper()
	if _, ok := h.tt.Find(text); !ok {
		t.Fatalf("找不到 %q：%v", text, h.tt.Texts())
	}
	// 等菜单真的打开，不数帧：机器忙时后台还在往界面投活、排版没稳，固定两帧后点下去会点偏、菜单没开
	// （2026-11-17 合并队列里 TestWhisperRejectionMarksCard 报 no context menu is shown）。没开就按最新位置再点一次。
	h.until(t, "右键菜单打开", func() bool {
		if len(h.tt.Menu()) > 0 {
			return true
		}
		if r, ok := h.tt.Find(text); ok {
			h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
		}
		return false
	})
}

// 本地群聊：右键菜单是复制、多选、引用 ▸；选了对象加一张卡；只发卡时只走悄悄话，不发群消息；
// 卡和主输入框都有字时先悄悄话、再群消息；悄悄话不进聊天区。
func TestWhisperGroupSendsSeparately(t *testing.T) {
	h, w, g := whisperGroup(t)
	h.rightClickText(t, "群里编的一句话")
	if os.Getenv("WHISPER_SHOT") != "" { // 子菜单开着的样子：↓ 到“引用”、→ 打开
		for range 3 {
			h.tt.Key(0, ui.KeyDown)
		}
		h.tt.Key(0, ui.KeyRight)
		h.tt.Frame()
		whisperShot(t, h.tt, "1-右键菜单和引用子菜单", "群里编的一句话", "多选", "所有人")
		h.tt.CloseMenu()
		h.rightClickText(t, "群里编的一句话")
	}
	if got := h.tt.Menu(); len(got) < 3 || !slices.Equal(got[:3], []string{"复制", "多选", "引用"}) || slices.Contains(got, "回复") {
		t.Fatalf("本地群聊右键菜单 %q", got)
	}
	if err := h.tt.ChooseMenuItem("引用", "编造角色char01"); err != nil {
		t.Fatalf("%v；菜单 %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	p := h.a.composerOf(g.Conversation)
	if len(p.Cards) != 1 || !slices.Equal(p.Cards[0].To, []string{"char01"}) || !p.Whisper {
		t.Fatalf("应加一张指给 char01 的悄悄话卡：%+v", p.Cards)
	}
	if !h.tt.HasText("→ 编造角色char01") {
		t.Fatalf("卡头没有对象标签：%v", h.tt.Texts())
	}
	// 同一条再引一次：不重复加卡
	h.rightClickText(t, "群里编的一句话")
	if err := h.tt.ChooseMenuItem("引用", "编造角色char01"); err != nil {
		t.Fatal(err)
	}
	if len(p.Cards) != 1 {
		t.Fatalf("同一条重复引用加了第二张卡：%d", len(p.Cards))
	}
	h.click(t, "回复引用 1")
	h.tt.Type("编的悄悄话：帮我查一下")
	whisperShot(t, h.tt, "2-本地群聊引用卡和对象标签", "输入区")
	h.click(t, "发送")
	h.until(t, "悄悄话交出去", func() bool { return len(p.Cards) == 0 })
	if got := w.seen(); !slices.Equal(got, []string{"whisper"}) {
		t.Fatalf("只有卡时只该走悄悄话：%q", got)
	}
	parts := w.parts[0]
	if len(parts) != 1 || parts[0].Quote == nil || parts[0].Quote.Ref.Conversation != g || !slices.Equal(parts[0].Mentions, []string{"char01"}) || parts[0].Text != "编的悄悄话：帮我查一下" {
		t.Fatalf("交给后台的块不对：%+v", parts)
	}
	// 卡加主输入框：先悄悄话、再群消息
	h.rightClickText(t, "群里编的一句话")
	if err := h.tt.ChooseMenuItem("引用", "所有人"); err != nil {
		t.Fatal(err)
	}
	h.click(t, "发一条消息")
	h.tt.Type("群里另一句")
	h.click(t, "发送")
	h.until(t, "两样都发出", func() bool { return len(w.seen()) == 3 })
	if got := w.seen(); !slices.Equal(got, []string{"whisper", "whisper", "group:群里另一句"}) {
		t.Fatalf("顺序应是先悄悄话再群消息：%q", got)
	}
	if !w.parts[1][0].All || len(w.parts[1][0].Mentions) != 0 {
		t.Fatalf("所有人只给 All：%+v", w.parts[1][0])
	}
	h.until(t, "群这一轮收尾", func() bool { return !h.a.localRun(g.Conversation).busy })
	for _, m := range h.a.msgs {
		if strings.Contains(m.text, "帮我查一下") {
			t.Fatalf("悄悄话进了聊天区：%q", m.text)
		}
	}
}

// 后台拒收（对象不在群里）：原因按"第 N 块"标在那张卡上，卡留着，群消息不发；改了对象以后旧原因消失。
func TestWhisperRejectionMarksCard(t *testing.T) {
	h, w, g := whisperGroup(t)
	h.rightClickText(t, "群里编的一句话")
	if err := h.tt.ChooseMenuItem("引用", "编造角色char00"); err != nil {
		t.Fatal(err)
	}
	p := h.a.composerOf(g.Conversation)
	p.Cards[0].To = []string{"char05"} // 不在群里的角色（列表过时的情形）
	p.Main.Text = "不该发出的群消息"
	h.click(t, "发送")
	h.until(t, "拒收回来", func() bool { return p.Cards[0].Err != "" })
	whisperShot(t, h.tt, "3-拒收标在卡上", "输入区")
	if p.Cards[0].Err != "编造角色char05 不是这个会话里能被唤起的角色" {
		t.Fatalf("卡上的原因：%q", p.Cards[0].Err)
	}
	if !h.tt.HasText(p.Cards[0].Err) {
		t.Fatalf("原因没画在卡上：%v", h.tt.Texts())
	}
	if got := w.seen(); !slices.Equal(got, []string{"whisper"}) {
		t.Fatalf("被拒时群消息也不发：%q", got)
	}
	if p.Main.Text != "不该发出的群消息" || len(p.Cards) != 1 {
		t.Fatal("被拒时草稿要原样留着")
	}
	// 对象全取消：标签变红、发送前就拦下，不调后台
	p.Cards[0].To = nil
	h.tt.Frame()
	whisperShot(t, h.tt, "4-对象为空标签变红", "输入区")
	if !h.tt.HasText("→ 选对象") {
		t.Fatalf("没对象时标签应写选对象：%v", h.tt.Texts())
	}
	h.click(t, "发送")
	h.tt.Frame()
	if p.Cards[0].Err != "至少选一位" || len(w.seen()) != 1 {
		t.Fatalf("对象为空不能发：%q %q", p.Cards[0].Err, w.seen())
	}
}

// 本地私聊不是悄悄话：右键仍是复制、回复，没有多选和引用 ▸。
func TestWhisperNotInLocalPrivate(t *testing.T) {
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("编造回复")}, nil)
	h.wbSend(t, "私聊编的一句")
	h.until(t, "私聊这一轮收尾", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("编造回复") })
	h.rightClickText(t, "私聊编的一句")
	got := h.tt.Menu()
	if slices.Contains(got, "多选") || slices.Contains(got, "引用") || !slices.Contains(got, "回复") {
		t.Fatalf("本地私聊的菜单应保持复制、回复：%q", got)
	}
	if h.a.canWhisper() {
		t.Fatal("本地私聊不能走悄悄话")
	}
	_ = ui.KeyEnter
}

// 引用卡的出处带着被引消息的时间；找不到消息或没有时间就不编。
func TestQuotedOfCarriesTime(t *testing.T) {
	t.Parallel()
	a := &app{msgs: []*msg{{id: "m1", who: "小A", at: 1_700_000_000_000}, {id: "m2", who: "小B"}}}
	q := a.quotedOf(chatui.Selected{MessageID: "m1", Text: "字"})
	if q.Name != "小A" || q.At.UnixMilli() != 1_700_000_000_000 {
		t.Fatalf("出处 %+v", q)
	}
	for _, id := range []string{"m2", "不存在"} {
		if q := a.quotedOf(chatui.Selected{MessageID: id, Text: "字"}); !q.At.IsZero() {
			t.Fatalf("%s 没有时间，不能编：%v", id, q.At)
		}
	}
}
