package main

import (
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/segview"
)

// 右栏“联系”标签和消息角标（规格-主动联系 第十节，第十六节第 9 条）：看不见时不读库、四种场合标签对、
// 状态和决定行的文字、按钮、读不到写原因；气泡时间旁的“主动联系”。名字、话都是编的。

type contactReader struct {
	demoReader
	mu                     sync.Mutex
	status                 ctlproto.ProactiveStatus
	err, snoozeErr         error
	reads, snoozes, closes int
	chars                  []string
}

func (f *contactReader) ProactiveStatus(_ context.Context, character string) (ctlproto.ProactiveStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.chars = append(f.chars, character)
	return f.status, f.err
}
func (f *contactReader) ProactiveSnooze(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snoozes++
	return f.snoozeErr
}
func (f *contactReader) ProactiveCloseThread(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}
func (f *contactReader) counts() (reads, snoozes, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, f.snoozes, f.closes
}

var (
	_ api.ProactiveReader  = (*contactReader)(nil)
	_ api.ProactiveControl = (*contactReader)(nil)
)

func contactStatus() ctlproto.ProactiveStatus {
	inputs, _ := json.Marshal(map[string]any{"sinceContactMin": 192, "factor": 0.7, "coldStreak": 1, "threadsToday": 1, "openThread": false, "inSleep": false,
		"ownerLastActiveAgoMin": 45, "localLastInputAgoMin": -1, "windowForeground": true})
	return ctlproto.ProactiveStatus{
		Enabled: true, Send: "live", Factor: 0.7,
		NextWake: time.Date(2026, 11, 9, 18, 30, 0, 0, time.Local).UnixMilli(),
		Thread: &ctlproto.ProactiveThreadView{ID: "th1", Open: true, State: "awaiting", StateText: "等回复 12 分钟", OpenedReason: "想接着问昨天的事",
			OpenedAt: 1000, LastSendAt: 2000, Sends: 1},
		Visits: []ctlproto.ProactiveVisit{{At: time.Date(2026, 11, 9, 16, 2, 0, 0, time.Local).UnixMilli(), Label: "本地私聊", State: "sent", Summary: "在忙吗？昨天那件事"}},
		Decisions: []ctlproto.ProactiveRow{
			{ID: 3, At: time.Date(2026, 11, 9, 16, 0, 0, 0, time.Local).UnixMilli(), Kind: "wake", Reason: "model-spoke", Thread: "th1", Inputs: inputs},
			{ID: 2, At: time.Date(2026, 11, 9, 14, 0, 0, 0, time.Local).UnixMilli(), Kind: "skip", Reason: "owner-chatting"},
			{ID: 1, At: time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local).UnixMilli(), Kind: "wake", Reason: "model-quiet", Note: "他刚忙完，不想打扰"},
		},
	}
}

type contactHarness struct {
	t  *testing.T
	a  *app
	f  *contactReader
	tt *ui.Tester
	q  *uiQueue
}

func newContactHarness(t *testing.T, sel string, f *contactReader) *contactHarness {
	t.Helper()
	q := &uiQueue{}
	a := shellApp()
	a.postFn = q.post
	a.reader = f
	a.sel = sel
	tt := shellTester(a, 2048, 1030)
	h := &contactHarness{t: t, a: a, f: f, tt: tt, q: q}
	a.workbench.shell.st.Panes.SideOpen = true
	h.frames()
	return h
}

func (h *contactHarness) frames() {
	for range 3 {
		settleMem(h.q)
		h.tt.Frame()
	}
}

func (h *contactHarness) click(text string) {
	h.t.Helper()
	if err := h.tt.Click(text); err != nil {
		h.t.Fatalf("点 %q：%v；%q", text, err, h.tt.Texts())
	}
	h.frames()
}

// 四种场合：只有本地私聊有“联系”；标签看不见时不读
func TestSideContactTabOnlyForLocalPrivate(t *testing.T) {
	for _, c := range []struct {
		name string
		k    api.ConversationKey
		want []string
	}{
		{"本地私聊", api.ConversationKey{Platform: "local", Scene: "private", Conversation: "console:r1"}, []string{"资料", "上下文", "联系"}},
		{"本地群聊", api.ConversationKey{Platform: "local", Scene: "group", Conversation: "console:group:g1"}, []string{"资料", "上下文", "对话日志"}},
		{"QQ 私聊", api.ConversationKey{Platform: "qq", Scene: "private", Conversation: "qq:private:1:2"}, []string{"资料", "活动", "对话日志"}},
		{"QQ 群", api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:3"}, []string{"资料", "活动", "对话日志"}},
	} {
		got := sideTabsFor(c.k)
		if len(got) != len(c.want) {
			t.Errorf("%s：%v，想要 %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s：%v，想要 %v", c.name, got, c.want)
			}
		}
	}
	f := &contactReader{status: contactStatus()}
	for _, sel := range []string{"home:g1", "qq:group:90002", "qq:private:80001:90001"} {
		h := newContactHarness(t, sel, f)
		if h.tt.HasText("联系") {
			t.Errorf("%s：不该有“联系”标签：%q", sel, h.tt.Texts())
		}
	}
	if reads, _, _ := f.counts(); reads != 0 {
		t.Fatalf("没有联系标签的场合不该读：%d", reads)
	}
}

func TestSideContactReadsOnlyWhenVisibleAndShowsStatus(t *testing.T) {
	f := &contactReader{status: contactStatus()}
	h := newContactHarness(t, "local:r1", f)
	if reads, _, _ := f.counts(); reads != 0 {
		t.Fatalf("没切到联系不该读：%d 次", reads)
	}
	h.click("联系")
	reads, _, _ := f.counts()
	if reads != 1 {
		t.Fatalf("切到联系读一次：%d 次", reads)
	}
	if f.chars[0] != "r1" {
		t.Fatalf("按这个会话的角色读：%v", f.chars)
	}
	for _, want := range []string{"等回复 12 分钟", "想接着问昨天的事", "18:30", "今天别找我", "现在别再找了", "去过哪", "最近的决定"} {
		if !hasSubstr(h.tt.Texts(), want) {
			t.Errorf("联系标签里缺 %q：%q", want, h.tt.Texts())
		}
	}
	// 去过哪一行、三条决定各一行：时间 · 结果 · 一句为什么
	for _, want := range []string{"· 本地私聊 · 在忙吗？昨天那件事", "· 开口了", "· 你正在聊，先不打扰", "· 想了没开口 · 他刚忙完，不想打扰"} {
		if !hasSubstr(h.tt.Texts(), want) {
			t.Errorf("缺 %q：%q", want, h.tt.Texts())
		}
	}
	// 快照折叠，点开才看
	if hasSubstr(h.tt.Texts(), "距上次找你 192 分钟") {
		t.Fatal("快照应当折叠")
	}
	h.clickRowContaining("开口了")
	if !hasSubstr(h.tt.Texts(), "距上次找你 192 分钟") || !hasSubstr(h.tt.Texts(), "你最后活跃距今 45 分钟") || !hasSubstr(h.tt.Texts(), "你本地最后输入距今 不知道") {
		t.Fatalf("展开后看当时的快照：%q", h.tt.Texts())
	}
	// 切回资料再停一会儿：不再读
	h.click("资料")
	before, _, _ := f.counts()
	h.a.workbench.shell.sideContact.due = time.Time{}
	h.frames()
	if after, _, _ := f.counts(); after != before {
		t.Fatalf("切到资料以后不读：%d → %d", before, after)
	}
}

// clickRowContaining 点第一个含这段字的文字
func (h *contactHarness) clickRowContaining(sub string) {
	h.t.Helper()
	for _, s := range h.tt.Texts() {
		if hasSubstr([]string{s}, sub) && len(s) > len(sub) {
			h.click(s)
			return
		}
	}
	h.t.Fatalf("没有含 %q 的行：%q", sub, h.tt.Texts())
}

func TestSideContactButtonsAndErrors(t *testing.T) {
	f := &contactReader{status: contactStatus()}
	h := newContactHarness(t, "local:r1", f)
	h.click("联系")
	h.click("今天别找我")
	waitUntil(t, "别烦调到后台", func() bool { _, s, _ := f.counts(); h.frames(); return s == 1 })
	if r, _, _ := f.counts(); r < 2 {
		t.Fatalf("点完按钮马上重读：%d 次", r)
	}
	h.click("现在别再找了")
	waitUntil(t, "关线调到后台", func() bool { _, _, c := f.counts(); h.frames(); return c == 1 })

	// 没有开着的线：关线按钮灰着，点了不调
	f2 := &contactReader{status: contactStatus()}
	f2.status.Thread = &ctlproto.ProactiveThreadView{ID: "th0", Open: false, State: "closed", StateText: "已关：没等到回复", CloseReason: "no-reply", Conclusion: "他没回"}
	h2 := newContactHarness(t, "local:r1", f2)
	h2.click("联系")
	if !hasSubstr(h2.tt.Texts(), "已关：没等到回复") || !hasSubstr(h2.tt.Texts(), "他没回") {
		t.Fatalf("最近一条线关了：%q", h2.tt.Texts())
	}
	_ = h2.tt.Click("现在别再找了")
	h2.frames()
	if _, _, c := f2.counts(); c != 0 {
		t.Fatal("没有开着的线时关线按钮是灰的，点了不该调后台")
	}

	// 别烦失败：写原因
	f3 := &contactReader{status: contactStatus(), snoozeErr: errors.New("主动联系没有打开")}
	h3 := newContactHarness(t, "local:r1", f3)
	h3.click("联系")
	h3.click("今天别找我")
	waitUntil(t, "别烦失败写原因", func() bool { h3.frames(); return hasSubstr(h3.tt.Texts(), "没做成：主动联系没有打开") })
}

func TestSideContactStatesOffErrorAndNoBackend(t *testing.T) {
	// 没打开
	f := &contactReader{status: ctlproto.ProactiveStatus{Enabled: false}}
	h := newContactHarness(t, "local:r1", f)
	h.click("联系")
	if !hasSubstr(h.tt.Texts(), "这个角色没有打开主动联系") || h.tt.HasText("今天别找我") {
		t.Fatalf("没打开：%q", h.tt.Texts())
	}
	// 读失败：写原因，不影响别的
	f2 := &contactReader{err: errors.New("读运行记录库出错")}
	h2 := newContactHarness(t, "local:r1", f2)
	h2.click("联系")
	if !hasSubstr(h2.tt.Texts(), "没读到主动联系的情况：读运行记录库出错") {
		t.Fatalf("读失败：%q", h2.tt.Texts())
	}
	// 没有决定记录
	f3 := &contactReader{status: ctlproto.ProactiveStatus{Enabled: true, Send: "think"}}
	h3 := newContactHarness(t, "local:r1", f3)
	h3.click("联系")
	if !hasSubstr(h3.tt.Texts(), "还没有决定记录") || !hasSubstr(h3.tt.Texts(), "没有开着的线") || !hasSubstr(h3.tt.Texts(), "只想不说") {
		t.Fatalf("空状态：%q", h3.tt.Texts())
	}
	// 只看历史的后台（读服务不是 ProactiveReader）
	q := &uiQueue{}
	a := shellApp()
	a.postFn = q.post
	a.reader = demoReader{}
	tt := shellTester(a, 2048, 1030)
	a.workbench.shell.st.Panes.SideOpen = true
	settleMem(q)
	tt.Frame()
	if err := tt.Click("联系"); err != nil {
		t.Fatal(err)
	}
	settleMem(q)
	tt.Frame()
	if !hasSubstr(tt.Texts(), "后台还没起来") {
		t.Fatalf("没有读服务：%q", tt.Texts())
	}
}

// 角标：她主动发的话气泡时间旁多一个“主动联系”，悬停补一行她为什么找你；别的消息没有
func TestProactiveMessageBadge(t *testing.T) {
	v := api.MessageView{Ref: api.EventRef{Event: api.EventKey{EventID: "e1"}}, ActorKind: "character", ActorID: "r1", Origin: "character", Text: "在忙吗？",
		CreatedAt: time.Date(2026, 11, 9, 16, 2, 0, 0, time.Local).UnixMilli(), Proactive: &api.ProactiveMark{Thread: "th1", Reason: "想接着问昨天的事"}}
	charOf := func(id string) (character, bool) { return character{ID: id, Name: "白羽"}, true }
	k := api.ConversationKey{Platform: "local", Scene: "private", Conversation: "console:r1"}
	m := msgFromView(k, v, charOf)
	if m.proactive == nil || m.proactive.Reason != "想接着问昨天的事" {
		t.Fatalf("消息要带主动联系标记：%+v", m.proactive)
	}
	prepareChatView(m, "local", nil)
	if len(m.chatNotes) != 1 || m.chatNotes[0] != "主动联系" {
		t.Fatalf("时间旁的小标记：%v", m.chatNotes)
	}
	cm := chatMessage(m)
	if cm.Tip != "她为什么找你：想接着问昨天的事" {
		t.Fatalf("悬停补一行：%q", cm.Tip)
	}
	v2 := v
	v2.Proactive = &api.ProactiveMark{Thread: "th1"}
	m2 := msgFromView(k, v2, charOf)
	if tip := proactiveTip(m2); tip != "她主动来找你的" {
		t.Fatalf("没有理由时的补充：%q", tip)
	}
	// 普通消息没有标记
	v3 := v
	v3.Proactive = nil
	m3 := msgFromView(k, v3, charOf)
	prepareChatView(m3, "local", nil)
	if len(m3.chatNotes) != 0 || chatMessage(m3).Tip != "" {
		t.Fatalf("普通消息不带标记：%v %q", m3.chatNotes, chatMessage(m3).Tip)
	}
	// 画出来：气泡右下角是“主动联系 16:02”
	q := &uiQueue{}
	a := shellApp()
	a.postFn = q.post
	m.blocks = segview.TextBlocks("在忙吗？")
	a.msgs = []*msg{m}
	tt := shellTester(a, 2048, 1030)
	for range 3 {
		settleMem(q)
		tt.Frame()
	}
	if !hasSubstr(tt.Texts(), "主动联系 16:02") {
		t.Fatalf("气泡时间旁要有“主动联系”：%q", tt.Texts())
	}
}

// 联系标签和角标的样子：只出图（LOOK_SHOT），不判。
func TestSideContactLook(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("只在看样子时跑")
	}
	f := &contactReader{status: contactStatus()}
	h := newContactHarness(t, "local:r1", f)
	h.a.msgs[0].proactive = &api.ProactiveMark{Thread: "th1", Reason: "想接着问昨天的事"}
	h.click("联系")
	h.clickRowContaining("开口了")
	out, err := os.Create(filepath.Join(dir, "联系标签.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// 内容压力：10 条决定、3 个去处、很长的理由和摘要时，标签的元素数和帧耗时（设计自查第 10 条，只记数不判快慢）；
// 长文字要换行不撑开右栏
func TestSideContactStressNumbers(t *testing.T) {
	st := contactStatus()
	long := ""
	for i := 0; i < 40; i++ {
		long += "很长很长的理由"
	}
	st.Thread.OpenedReason = long
	st.Visits = nil
	for i := 0; i < 3; i++ {
		st.Visits = append(st.Visits, ctlproto.ProactiveVisit{At: time.Date(2026, 11, 9, 16, i, 0, 0, time.Local).UnixMilli(), Label: "本地私聊", State: "sent", Summary: long})
	}
	st.Decisions = nil
	for i := 0; i < 10; i++ {
		st.Decisions = append(st.Decisions, ctlproto.ProactiveRow{ID: int64(10 - i), At: time.Date(2026, 11, 9, 12, i, 0, 0, time.Local).UnixMilli(), Kind: "wake", Reason: "model-quiet", Note: long[:60]})
	}
	f := &contactReader{status: st}
	h := newContactHarness(t, "local:r1", f)
	h.click("联系")
	start := time.Now()
	const n = 30
	for range n {
		h.tt.Frame()
	}
	per := time.Since(start) / n
	t.Logf("联系标签：10 条决定 + 3 个去处 + 超长文字：%d 个文字元素，每帧约 %v", len(h.tt.Texts()), per)
	if per > 200*time.Millisecond {
		t.Fatalf("一帧 %v，太慢了", per)
	}
}
