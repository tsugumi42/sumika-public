package approvalrelay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/channel/types"
)

// 号、角色、名字都是编的。
const (
	tOwner   = "200"
	tAccount = "100"
	tChar    = "example"
)

var tConv = types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: tOwner, Self: tAccount}

type sent struct {
	conv  types.Conversation
	texts []string
}

type fixture struct {
	t         *testing.T
	ap        *approval.Approvals
	r         *Relay
	mu        sync.Mutex
	clock     time.Time
	sends     []sent
	nextMsg   int
	bindOK    bool
	binding   Binding
	sendErr   error
	confirm   bool
	attempted bool
	away      bool
	decided   map[string]chan approval.Decision
	elig      bool
	started   time.Time
	sendHook  func()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, clock: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), bindOK: true, confirm: true, away: true, elig: true, decided: map[string]chan approval.Decision{}}
	f.started = f.clock
	f.binding = Binding{Away: 60 * time.Second, Conv: tConv, Character: tChar, Owner: tOwner, Account: tAccount}
	g := &approval.Gate{Now: f.now, Grants: approval.LoadGrants(filepath.Join(t.TempDir(), "grants.yml")), Conv: approval.NewConvGrants(), ApprovalMinutes: 10}
	f.ap = approval.NewApprovals(g)
	r, err := New(Deps{
		Approver: f.ap,
		Binding: func(character string) (Binding, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.binding, f.bindOK && character == tChar
		},
		Eligible: func(approval.Request) bool { return f.elig },
		Away:     func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.away },
		Send: func(ctx context.Context, b Binding, texts []string) (Delivery, error) {
			// 按调用顺序记：出口按会话排队，同一会话后调用的一定排在前一条发完之后。
			// 不能按返回先后记——“发卡途中”的回应会异步触发一句提示，它可能先返回、被记在卡前面，lastSent 就成了卡本身。
			f.mu.Lock()
			f.sends = append(f.sends, sent{b.Conv, append([]string(nil), texts...)})
			f.mu.Unlock()
			if f.sendHook != nil {
				f.sendHook()
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.sendErr != nil {
				return Delivery{}, f.sendErr
			}
			var ids []string
			for range texts {
				f.nextMsg++
				ids = append(ids, fmt.Sprintf("9%03d", f.nextMsg))
			}
			return Delivery{Confirmed: f.confirm, Attempted: f.attempted || f.confirm, MessageIDs: ids}, nil
		},
		Now:  f.now,
		Logf: func(format string, args ...any) { t.Logf(format, args...) },
		Tick: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.r = r
	t.Cleanup(r.Close)
	return f
}

func (f *fixture) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }
func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func (f *fixture) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sends {
		out = append(out, strings.Join(s.texts, "\n"))
	}
	return out
}

func (f *fixture) lastSent() string {
	s := f.sentTexts()
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// ask 发起一张本地审批卡，返回它的结局通道；等关系人订阅收到。
func (f *fixture) ask(id, command string) <-chan approval.Decision {
	f.t.Helper()
	now := f.now()
	req := approval.Request{ID: id, CharacterID: tChar, Conversation: "console:" + tChar, ToolCallID: "call-" + id, Tool: "powershell", Title: "运行 PowerShell 命令",
		Subject: command, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(10 * time.Minute).UnixMilli()}
	ch := make(chan approval.Decision, 1)
	f.decided[id] = ch
	go func() { ch <- f.ap.Ask(context.Background(), req, nil) }()
	waitUntil(f.t, "审批进入 relay", func() bool {
		f.r.mu.Lock()
		defer f.r.mu.Unlock()
		return f.r.tickets[id] != nil
	})
	return ch
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等了 10 秒仍没有：%s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// open 让主人离开后发卡，返回单号。
func (f *fixture) open(id, command string) (string, <-chan approval.Decision) {
	f.t.Helper()
	ch := f.ask(id, command)
	f.advance(61 * time.Second)
	f.r.Step(context.Background())
	waitUntil(f.t, "票开了", func() bool {
		f.r.mu.Lock()
		defer f.r.mu.Unlock()
		t := f.r.tickets[id]
		return t != nil && t.state == stOpen
	})
	f.r.mu.Lock()
	n := f.r.tickets[id].id
	f.r.mu.Unlock()
	return n, ch
}

func inbound(text string) Inbound {
	return Inbound{Character: tChar, Conv: tConv, Account: tAccount, FromID: tOwner, IsOwner: true, Message: true, PlatformID: fmt.Sprintf("m%d", time.Now().UnixNano()),
		Segs: []types.Seg{{Kind: types.SegText, Text: text}}}
}

func (f *fixture) settleQuiet() { f.r.Idle() }

// decision 等一个结局，最多等 wait：期待有结局时给足时间（机器忙时 300 毫秒会误红），期待没有结局时用短等。
func (f *fixture) decisionWithin(ch <-chan approval.Decision, wait time.Duration) (approval.Decision, bool) {
	select {
	case d := <-ch:
		return d, true
	case <-time.After(wait):
		return approval.Decision{}, false
	}
}

func (f *fixture) decision(ch <-chan approval.Decision) (approval.Decision, bool) {
	return f.decisionWithin(ch, 300*time.Millisecond)
}

func mustDecide(t *testing.T, f *fixture, ch <-chan approval.Decision, kind string) {
	t.Helper()
	d, ok := f.decisionWithin(ch, 10*time.Second)
	if !ok || d.Kind != kind {
		t.Fatalf("应该是 %s，实际 %v %v", kind, d, ok)
	}
}

func mustPending(t *testing.T, f *fixture, ch <-chan approval.Decision) {
	t.Helper()
	if d, ok := f.decision(ch); ok {
		t.Fatalf("不该有结局，却收到 %v", d)
	}
}

// 过了启动后的 10 分钟窗口，裸“同意”才可能直接结算
func (f *fixture) pastStartup() { f.advance(11 * time.Minute) }

func TestLocalApprovedFromQQ(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo 编的命令 > a.txt")
	card := f.sentTexts()[0]
	if !strings.Contains(card, "echo 编的命令 > a.txt") || !strings.Contains(card, id) {
		t.Fatalf("卡里要有完整命令和编号：%s", card)
	}
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("同意")) {
		t.Fatal("同意应该被截走")
	}
	mustDecide(t, f, ch, "approved")
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "已批准") {
		t.Fatalf("要回一句确认：%q", f.lastSent())
	}
	if f.r.OpenCount() != 0 {
		t.Fatal("票要收掉")
	}
}

func TestDeniedFromQQ(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch := f.open("req1", "rm 编的")
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("拒绝")) {
		t.Fatal("拒绝应该被截走")
	}
	d, ok := f.decisionWithin(ch, 10*time.Second)
	if !ok || d.Kind != "denied" || d.Via != "qq" {
		t.Fatalf("应该是 qq 拒绝：%v %v", d, ok)
	}
}

// 本地先答：QQ 立刻收到结局，之后的同意不再结算，也不转到别的单。
func TestLocalFirstUpdatesQQ(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo 一")
	if ok, err := f.ap.Decide("req1", "once", ""); !ok || err != nil {
		t.Fatal(ok, err)
	}
	mustDecide(t, f, ch, "approved")
	waitUntil(t, "结局帧", func() bool { f.settleQuiet(); return strings.Contains(f.lastSent(), "本机已批准") })
	if !strings.Contains(f.lastSent(), id) {
		t.Fatalf("结局要带编号：%q", f.lastSent())
	}
	// 再开一张新单，迟到的裸同意不能批新单
	f.advance(30 * time.Second)
	id2, ch2 := f.open("req2", "echo 二")
	f.advance(5 * time.Second)
	f.r.Control(inbound("同意"))
	mustPending(t, f, ch2)
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), id2) {
		t.Fatalf("要提示带编号回：%q", f.lastSent())
	}
}

func TestDefaultNothingSentWhenNotAway(t *testing.T) {
	f := newFixture(t)
	f.away = false
	f.ask("req1", "echo")
	f.advance(61 * time.Second)
	f.r.Step(context.Background())
	if len(f.sentTexts()) != 0 {
		t.Fatalf("主人在场时不发：%v", f.sentTexts())
	}
	f.mu.Lock()
	f.away = true
	f.mu.Unlock()
	f.r.Step(context.Background())
	if len(f.sentTexts()) != 1 {
		t.Fatalf("离开后发一次：%v", f.sentTexts())
	}
}

func TestAwayZeroSendsAtOnce(t *testing.T) {
	f := newFixture(t)
	f.binding.Away = 0
	f.away = false // 0 表示一出单就发，不看在场
	f.ask("req1", "echo")
	f.r.Step(context.Background())
	if len(f.sentTexts()) != 1 {
		t.Fatalf("0 秒应该立刻发：%v", f.sentTexts())
	}
}

func TestNotEligibleNotSent(t *testing.T) {
	f := newFixture(t)
	f.elig = false
	f.mu.Lock()
	f.mu.Unlock()
	req := approval.Request{ID: "x", CharacterID: tChar, Conversation: "qq:private:100:200", Title: "t", CreatedAt: f.now().UnixMilli(), ExpiresAt: f.now().Add(time.Hour).UnixMilli()}
	go f.ap.Ask(context.Background(), req, nil)
	waitUntil(t, "进审批", func() bool { return len(f.ap.Pending()) == 1 })
	f.advance(2 * time.Minute)
	f.r.Step(context.Background())
	if len(f.sentTexts()) != 0 {
		t.Fatal("不是本地私聊的卡不发 QQ")
	}
}

// 身份形状：每一种都不能结算，也不能被吞掉（要么放行成普通消息，要么在明确的提示场合被截）
func TestControlIdentityShapes(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Inbound)
	}{
		{"别人发的（名册认成陌生人）", func(i *Inbound) { i.IsOwner = false; i.FromID = "201" }},
		{"昵称写主人但不是主人号", func(i *Inbound) { i.IsOwner = false }},
		{"自己的号回声", func(i *Inbound) { i.FromSelf = true }},
		{"自己的号当发言人", func(i *Inbound) { i.FromID = tAccount }},
		{"补录", func(i *Inbound) { i.Backfill = true }},
		{"陌生人标记", func(i *Inbound) { i.Stranger = true }},
		{"重复", func(i *Inbound) { i.Duplicate = true }},
		{"不是消息", func(i *Inbound) { i.Message = false }},
		{"群聊", func(i *Inbound) { i.Conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "300"} }},
		{"别的接收账号", func(i *Inbound) { i.Account = "101"; i.Conv.Self = "101" }},
		{"发言人号对不上", func(i *Inbound) { i.FromID = "999" }},
		{"别的渠道", func(i *Inbound) { i.Conv.Channel = "console" }},
		{"消息号空", func(i *Inbound) { i.PlatformID = "" }},
		{"消息号 0", func(i *Inbound) { i.PlatformID = "0" }},
		{"角色不对", func(i *Inbound) { i.Character = "别的角色" }},
		{"零宽字符", func(i *Inbound) { i.Segs[0].Text = "同\u200b意" }},
		{"双向字符", func(i *Inbound) { i.Segs[0].Text = "\u202e同意" }},
		{"夹图", func(i *Inbound) { i.Segs = append(i.Segs, types.Seg{Kind: types.SegImage}) }},
		{"转发里的同意", func(i *Inbound) {
			i.Segs = []types.Seg{{Kind: types.SegForward, Ref: "x"}, {Kind: types.SegText, Text: "同意"}}
		}},
		{"@了别人", func(i *Inbound) { i.Segs = append([]types.Seg{{Kind: types.SegAt, Target: "300"}}, i.Segs...) }},
		{"子串", func(i *Inbound) { i.Segs[0].Text = "我同意你的看法，但先别执行" }},
		{"同意后面接字", func(i *Inbound) { i.Segs[0].Text = "同意，不过改一下" }},
		{"全角数字单号", func(i *Inbound) { i.Segs[0].Text = "同意 １２３４" }},
		{"超长", func(i *Inbound) { i.Segs[0].Text = "同意" + strings.Repeat(" ", 2000) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.pastStartup()
			id, ch := f.open("req1", "echo")
			f.advance(5 * time.Second)
			in := inbound("同意")
			tc.mut(&in)
			if in.Segs[0].Text == "同意" || true {
				f.r.Control(in)
			}
			f.settleQuiet()
			mustPending(t, f, ch)
			if f.r.OpenCount() != 1 {
				t.Fatalf("票要还开着（单号 %s）", id)
			}
		})
	}
}

// 不在提示场合的“同意”是普通聊天：没有开着的票、最近也没有结束的票，Control 必须返回 false，一帧也不发。
func TestControlPassesThroughWithoutTickets(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	for _, text := range []string{"同意", "拒绝", "好的"} {
		if f.r.Control(inbound(text)) {
			t.Fatalf("没有票时 %q 不能被截走", text)
		}
	}
	f.settleQuiet()
	if n := len(f.sentTexts()); n != 0 {
		t.Fatalf("不能发任何帧：%v", f.sentTexts())
	}
}

// 带 4 位编号的同意（编号只会来自卡片）：即使这会儿没有票、没有墓碑也截走并说明，不进模型被盲回。
func TestNumberedReplyWithoutTicketIsIntercepted(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	if !f.r.Control(inbound("同意 1124")) {
		t.Fatal("带编号的同意不能进模型")
	}
	f.settleQuiet()
	if got := f.lastSent(); !strings.Contains(got, "1124") || !strings.Contains(got, "没有处理") {
		t.Fatalf("要说明没有这一单：%q", got)
	}
}

func TestInvisibleOnlyHints(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch := f.open("req1", "echo")
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("同\u200b意")) {
		t.Fatal("看不见的字符属于提示场合，应该被截走")
	}
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "看不见的字符") {
		t.Fatalf("要提示：%q", f.lastSent())
	}
	mustPending(t, f, ch)
}

func TestWeakAnswerHintsAndWaits(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch := f.open("req1", "echo")
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("好的")) {
		t.Fatal("短答要被截走并提示")
	}
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "明确") {
		t.Fatalf("要提示明确回：%q", f.lastSent())
	}
	mustPending(t, f, ch)
	if f.r.OpenCount() != 1 {
		t.Fatal("票继续等")
	}
}

func TestQuoteOfOtherMessage(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch := f.open("req1", "echo")
	f.advance(5 * time.Second)
	in := inbound("同意")
	in.Segs = append([]types.Seg{{Kind: types.SegReply, Target: "88888"}}, in.Segs...)
	if !f.r.Control(in) {
		t.Fatal("引用别的消息回同意要提示")
	}
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "不是审批") {
		t.Fatalf("要说明引用的不是审批：%q", f.lastSent())
	}
	mustPending(t, f, ch)
}

func TestQuoteOfCardSelectsThatTicket(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch1 := f.open("req1", "echo 一")
	_, ch2 := f.open("req2", "echo 二")
	f.advance(5 * time.Second)
	f.r.mu.Lock()
	quote := f.r.tickets["req2"].msgIDs[0]
	f.r.mu.Unlock()
	in := inbound("同意")
	in.Segs = append([]types.Seg{{Kind: types.SegReply, Target: quote}}, in.Segs...)
	if !f.r.Control(in) {
		t.Fatal("应截走")
	}
	mustDecide(t, f, ch2, "approved")
	mustPending(t, f, ch1)
}

func TestParallelTicketsAskFirst(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id1, ch1 := f.open("req1", "echo 一")
	id2, ch2 := f.open("req2", "echo 二")
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("同意")) {
		t.Fatal("多单时裸同意要被截走并追问")
	}
	f.settleQuiet()
	if txt := f.lastSent(); !strings.Contains(txt, id1) || !strings.Contains(txt, id2) {
		t.Fatalf("要列出两单：%q", txt)
	}
	mustPending(t, f, ch1)
	mustPending(t, f, ch2)
	if !f.r.Control(inbound("同意 " + id2)) {
		t.Fatal("带单号应结算")
	}
	mustDecide(t, f, ch2, "approved")
	mustPending(t, f, ch1)
}

func TestWrongIDDoesNothing(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo")
	f.advance(5 * time.Second)
	wrong := "0000"
	if id == wrong {
		wrong = "0001"
	}
	if !f.r.Control(inbound("同意 " + wrong)) {
		t.Fatal("错单号要提示")
	}
	mustPending(t, f, ch)
}

func TestLateReplyDoesNotMoveToNewTicket(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	idA, chA := f.open("reqA", "echo 甲")
	f.advance(RemoteTTL + time.Second)
	f.r.Step(context.Background()) // A 在 QQ 这边过期
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "过期") {
		t.Fatalf("要告诉主人过期：%q", f.lastSent())
	}
	idB, chB := f.open("reqB", "echo 乙")
	f.advance(5 * time.Second)
	// 迟到的裸同意：不能批 B
	f.r.Control(inbound("同意"))
	mustPending(t, f, chB)
	mustPending(t, f, chA)
	// 迟到的带 A 号的同意：不能批 A，更不能批 B
	if !f.r.Control(inbound("同意 " + idA)) {
		t.Fatal("A 的墓碑要回话")
	}
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "已经结束") {
		t.Fatalf("要说明已结束：%q", f.lastSent())
	}
	mustPending(t, f, chB)
	mustPending(t, f, chA)
	// B 用自己的号才行
	if !f.r.Control(inbound("同意 " + idB)) {
		t.Fatal("B 的号应结算")
	}
	mustDecide(t, f, chB, "approved")
}

func TestDeadlineBoundary(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo")
	f.advance(RemoteTTL - time.Second)
	if !f.r.Control(inbound("同意 " + id)) {
		t.Fatal("299 秒应该还行")
	}
	mustDecide(t, f, ch, "approved")

	f2 := newFixture(t)
	f2.pastStartup()
	id2, ch2 := f2.open("req1", "echo")
	f2.advance(RemoteTTL)
	if !f2.r.Control(inbound("同意 " + id2)) {
		t.Fatal("正好 300 秒要回话")
	}
	mustPending(t, f2, ch2)
	f2.settleQuiet()
	if !strings.Contains(f2.lastSent(), "过期") {
		t.Fatalf("正好 300 秒算过期：%q", f2.lastSent())
	}
}

func TestStartupWindowNeedsID(t *testing.T) {
	f := newFixture(t) // 不 pastStartup：刚启动
	id, ch := f.open("req1", "echo")
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("同意")) {
		t.Fatal("要提示带号")
	}
	mustPending(t, f, ch)
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), id) {
		t.Fatalf("提示里要有编号：%q", f.lastSent())
	}
	if !f.r.Control(inbound("同意 " + id)) {
		t.Fatal("带号可以")
	}
	mustDecide(t, f, ch, "approved")
}

func TestTooFreshNeedsID(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo")
	f.advance(time.Second) // 送达才 1 秒
	f.r.Control(inbound("同意"))
	mustPending(t, f, ch)
	f.r.Control(inbound("同意 " + id))
	mustDecide(t, f, ch, "approved")
}

func TestSameMessageTwiceDecidesOnce(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch := f.open("req1", "echo")
	_, ch2 := f.open("req2", "echo 二")
	f.advance(5 * time.Second)
	f.r.mu.Lock()
	id1 := f.r.tickets["req1"].id
	f.r.mu.Unlock()
	in := inbound("同意 " + id1)
	if !f.r.Control(in) {
		t.Fatal()
	}
	mustDecide(t, f, ch, "approved")
	f.settleQuiet()
	before := len(f.sentTexts())
	if !f.r.Control(in) {
		t.Fatal("同一条消息重推要被吞掉，不进模型")
	}
	f.settleQuiet()
	if n := len(f.sentTexts()); n != before {
		t.Fatalf("重推的同一条不能再回话：%d → %d", before, n)
	}
	mustPending(t, f, ch2)
}

func TestDeliveryFailureStaysLocal(t *testing.T) {
	for name, set := range map[string]func(*fixture){
		"发送报错":   func(f *fixture) { f.sendErr = errors.New("断线") },
		"结果未知":   func(f *fixture) { f.confirm = false },
		"试运行未发出": func(f *fixture) { f.confirm = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.pastStartup()
			set(f)
			ch := f.ask("req1", "echo")
			f.advance(61 * time.Second)
			f.r.Step(context.Background())
			f.settleQuiet()
			if f.r.OpenCount() != 0 {
				t.Fatal("没送达不能开票")
			}
			if f.r.Control(inbound("同意")) {
				t.Fatal("没有票，同意是普通聊天")
			}
			mustPending(t, f, ch)
			if len(f.ap.Pending()) != 1 {
				t.Fatal("本机的卡仍在等")
			}
			// 之后巡检不会再重复发卡
			f.advance(time.Minute)
			f.r.Step(context.Background())
			if n := len(f.sentTexts()); n != 1 {
				t.Fatalf("只试一次，实际发了 %d 次", n)
			}
		})
	}
}

func TestLocalDecisionWhileSending(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	ch := f.ask("req1", "echo")
	f.advance(61 * time.Second)
	f.sendHook = func() {
		f.ap.Decide("req1", "once", "")
		time.Sleep(50 * time.Millisecond)
	}
	f.r.Step(context.Background())
	f.settleQuiet()
	mustDecide(t, f, ch, "approved")
	if f.r.OpenCount() != 0 {
		t.Fatal("发卡期间有了结局，不能开票")
	}
	waitUntil(t, "补一句结局", func() bool { f.settleQuiet(); return strings.Contains(f.lastSent(), "本机已批准") })
}

func TestBindingClosedWhileWaiting(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	f.ask("req1", "echo")
	f.mu.Lock()
	f.bindOK = false
	f.mu.Unlock()
	f.advance(61 * time.Second)
	f.r.Step(context.Background())
	if len(f.sentTexts()) != 0 {
		t.Fatal("开关关了不能发")
	}
}

func TestDisableClosesTickets(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo")
	f.mu.Lock()
	f.bindOK = false
	f.mu.Unlock()
	f.advance(5 * time.Second)
	if f.r.Control(inbound("同意 " + id)) {
		t.Fatal("关了就放行成普通消息")
	}
	mustPending(t, f, ch)
	if f.r.OpenCount() != 0 {
		t.Fatal("关了以后票要收掉")
	}
	// 再打开也不能复活旧票
	f.mu.Lock()
	f.bindOK = true
	f.mu.Unlock()
	if f.r.Control(inbound("同意 " + id)) {
		// 墓碑在最近窗口内：提示“已结束”，但绝不结算
		mustPending(t, f, ch)
	}
}

func TestOwnerChangedCannotUseOldTicket(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo")
	f.mu.Lock()
	f.binding.Owner = "300"
	f.binding.Conv = types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: "300", Self: tAccount}
	f.mu.Unlock()
	f.advance(5 * time.Second)
	// 旧主人号
	f.r.Control(inbound("同意 " + id))
	// 新主人号：看不到旧会话的票
	in := inbound("同意 " + id)
	in.Conv, in.FromID = f.binding.Conv, "300"
	f.r.Control(in)
	mustPending(t, f, ch)
}

func TestUnsendableCards(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	now := f.now()
	req := approval.Request{ID: "req1", CharacterID: tChar, Conversation: "console:" + tChar, Tool: "write", Title: "写文件", Subject: "a.txt",
		Preview: &approval.Preview{Kind: "content", Text: "很长", Truncated: true}, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	ch := make(chan approval.Decision, 1)
	go func() { ch <- f.ap.Ask(context.Background(), req, nil) }()
	waitUntil(t, "进 relay", func() bool { f.r.mu.Lock(); defer f.r.mu.Unlock(); return f.r.tickets["req1"] != nil })
	f.advance(61 * time.Second)
	f.r.Step(context.Background())
	f.settleQuiet()
	if f.r.OpenCount() != 0 {
		t.Fatal("预览被截短不能开票")
	}
	if txt := f.lastSent(); !strings.Contains(txt, "没法在这里批") || strings.Contains(txt, "a.txt\n") && strings.Contains(txt, "同意") {
		t.Fatalf("只告诉主人回本机：%q", txt)
	}
	mustPending(t, f, ch)
}

func TestRenderCardEscapesInvisible(t *testing.T) {
	req := approval.Request{CharacterID: tChar, Tool: "powershell", Title: "运行 PowerShell 命令", Subject: "echo a\u200bb \u202egnp.exe"}
	texts, why := RenderCard(req, "1234")
	if why != "" {
		t.Fatal(why)
	}
	all := strings.Join(texts, "\n")
	if strings.ContainsAny(all, "\u200b\u202e") {
		t.Fatalf("卡里不能有看不见的字符：%q", all)
	}
	if !strings.Contains(all, `\u200b`) || !strings.Contains(all, `\u202e`) {
		t.Fatalf("要显示成可见转义：%q", all)
	}
}

func TestRenderCardSegments(t *testing.T) {
	req := approval.Request{CharacterID: tChar, Tool: "powershell", Title: "t", Subject: strings.Repeat("字", 3200)}
	texts, why := RenderCard(req, "1234")
	if why != "" || len(texts) < 3 {
		t.Fatalf("应该分段：%d %q", len(texts), why)
	}
	for _, s := range texts {
		if n := len([]rune(s)); n > segmentRunes+10 {
			t.Fatalf("单段 %d 字超限", n)
		}
	}
	if !regexp.MustCompile(`同意 1234`).MatchString(texts[len(texts)-1]) {
		t.Fatal("最后一段要有编号说明")
	}
	huge := approval.Request{CharacterID: tChar, Tool: "powershell", Title: "t", Subject: strings.Repeat("字", segmentRunes*maxSegments)}
	if _, why := RenderCard(huge, "1234"); why == "" {
		t.Fatal("放不下就不能在 QQ 批")
	}
}

// 本机和 QQ 同时答：恰好一个结局，另一边被告知输了。
func TestLocalAndQQRace(t *testing.T) {
	for i := range 40 {
		f := newFixture(t)
		f.pastStartup()
		id, ch := f.open("req1", "echo")
		f.advance(5 * time.Second)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var localOK bool
		wg.Add(2)
		go func() { defer wg.Done(); <-start; localOK, _ = f.ap.Decide("req1", "deny", "") }()
		go func() { defer wg.Done(); <-start; f.r.Control(inbound("同意 " + id)) }()
		close(start)
		wg.Wait()
		d, ok := f.decisionWithin(ch, 10*time.Second)
		if !ok {
			t.Fatal("必须有一个结局")
		}
		f.settleQuiet()
		if localOK && d.Kind != "denied" || !localOK && d.Kind != "approved" {
			t.Fatalf("第 %d 轮结局和输赢对不上：local=%v %v", i, localOK, d)
		}
		if !localOK {
			if d.Via != "qq" {
				t.Fatal("QQ 赢了 Via 要是 qq")
			}
		}
		waitUntil(t, "票收掉", func() bool { return f.r.OpenCount() == 0 })
	}
}

func TestOpenLimit(t *testing.T) {
	f := newFixture(t)
	for i := range maxOpen + 2 {
		now := f.now()
		req := approval.Request{ID: fmt.Sprintf("r%d", i), CharacterID: tChar, Conversation: "console:" + tChar, Tool: "x", Title: "t", CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
		go f.ap.Ask(context.Background(), req, nil)
	}
	waitUntil(t, "全进审批", func() bool { return len(f.ap.Pending()) == maxOpen+2 })
	time.Sleep(100 * time.Millisecond)
	f.r.mu.Lock()
	n := len(f.r.tickets)
	f.r.mu.Unlock()
	if n != maxOpen {
		t.Fatalf("票上限 %d，实际 %d", maxOpen, n)
	}
}

// 回复里写的单号和引用的卡不是同一单：都不结算。
func TestIDQuoteMismatch(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id1, ch1 := f.open("req1", "echo 一")
	_, ch2 := f.open("req2", "echo 二")
	f.advance(5 * time.Second)
	f.r.mu.Lock()
	quote2 := f.r.tickets["req2"].msgIDs[0]
	f.r.mu.Unlock()
	in := inbound("同意 " + id1)
	in.Segs = append([]types.Seg{{Kind: types.SegReply, Target: quote2}}, in.Segs...)
	if !f.r.Control(in) {
		t.Fatal("对不上要提示")
	}
	mustPending(t, f, ch1)
	mustPending(t, f, ch2)
}
