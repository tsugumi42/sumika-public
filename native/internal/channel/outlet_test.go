package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/history"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 出口的测试：假适配器、假钟、临时库，不碰真实数据。号、群、名字都是编的（LESSONS）。
// 出站里全是时间（停顿、重试、超时、认领窗口、关闭等回执），时间都走假钟：测试自己拨，不 sleep 去碰运气。

// oh 是一套：临时库、假适配器（已 Run）、假钟、出口。需要入库一站的测试再 withIntake
type oh struct {
	t   *testing.T
	st  *store.Store
	ad  *fake.Adapter
	clk *fake.Clock
	out *Outlet
	in  *Intake

	mu   sync.Mutex
	logs []string

	stopAd context.CancelFunc
	adDone chan struct{}
}

type ohOpt func(*OutletConfig)

func (h *oh) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, fmt.Sprintf(format, args...))
}

func (h *oh) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

// newOH 造一套。tweak 可以改出口的配置（挂 Mode、Check、换时限……）
func newOH(t *testing.T, caps types.Caps, tweak ...ohOpt) *oh {
	t.Helper()
	return newOHWith(t, caps, nil, tweak...)
}

// newOHWith 同 newOH，只是造出口之前先让 pre 往库里写点东西（模拟上次没关干净留下的计划）
func newOHWith(t *testing.T, caps types.Caps, pre func(*store.Store), tweak ...ohOpt) *oh {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	if pre != nil {
		pre(st)
	}
	h := &oh{t: t, st: st, ad: fake.New(chQQ, caps), clk: fake.NewClock(baseTime)}
	h.ad.Now = h.clk.Now
	cfg := OutletConfig{Stores: store.Single(st), Roster: testRoster(), Adapters: []types.Adapter{h.ad}, Clock: h.clk, Logf: h.logf}
	for _, f := range tweak {
		f(&cfg)
	}
	h.out, err = NewOutlet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.shutdown)
	return h
}

// run 把假适配器跑起来，事件交给 sink（nil 就是丢给 Recorder）
func (h *oh) run(sink types.Sink) {
	h.t.Helper()
	if sink == nil {
		sink = &fake.Recorder{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stopAd = cancel
	h.adDone = make(chan struct{})
	go func() {
		defer close(h.adDone)
		h.ad.Run(ctx, sink)
	}()
	select {
	case <-h.ad.Started():
	case <-time.After(5 * time.Second):
		h.t.Fatal("假适配器没起来")
	}
}

// withIntake 再造一个入库一站，把出口接成它的回声认领，适配器的事件交给它
func (h *oh) withIntake(tweak ...func(*Config)) {
	h.t.Helper()
	var err error
	cfg := Config{Stores: store.Single(h.st), Roster: testRoster(), Echo: h.out, Logf: h.logf, Now: h.clk.Now}
	for _, f := range tweak {
		f(&cfg)
	}
	h.in, err = NewIntake(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.run(intakeSink{h.in})
}

// intakeSink 把入库一站接成完整的 Sink：它只管 Inbound，通知和连接状态这一步还没有接收方（第 4、9 步才有），这里丢掉
type intakeSink struct{ *Intake }

func (intakeSink) Notice(context.Context, types.Notice) error { return nil }
func (intakeSink) Status(context.Context, types.Status) error { return nil }

// shutdown 收尾的顺序：先停适配器和入库一站（回声不再进来），再关出口（时钟没人拨的话它会等 5 秒，所以边拨边等），最后关库
func (h *oh) shutdown() {
	if h.stopAd != nil {
		h.stopAd()
		<-h.adDone
	}
	if h.in != nil {
		h.in.Close()
	}
	done := make(chan struct{})
	go func() { h.out.Close(); close(done) }()
	for {
		select {
		case <-done:
			h.st.Close()
			return
		case <-time.After(time.Millisecond):
			h.clk.Advance(10 * time.Second)
		}
	}
}

// eventually 等条件成立，最多真等 5 秒
func eventually(t *testing.T, what string, cond func() bool, detail ...func() string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			more := ""
			for _, d := range detail {
				more += "\n" + d()
			}
			t.Fatalf("等了 5 秒还没有：%s%s", what, more)
		}
		time.Sleep(time.Millisecond)
	}
}

// plan 直接读库（不经出口：出口封库以后 Plan 就不能用了，而关闭的测试正是要看库里留下了什么）
func (h *oh) plan(id string) store.Plan {
	h.t.Helper()
	p, err := h.st.GetPlan(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

// waitPart 等一段到某个状态
func (h *oh) waitPart(id string, idx int, state string) {
	h.t.Helper()
	eventually(h.t, fmt.Sprintf("计划 %s 第 %d 段变成 %s", id, idx, state), func() bool {
		p, err := h.st.GetPlan(id)
		return err == nil && len(p.Parts) > idx && p.Parts[idx].State == state
	})
}

// waitPlan 等计划到某个状态
func (h *oh) waitPlan(id, state string) {
	h.t.Helper()
	eventually(h.t, fmt.Sprintf("计划 %s 变成 %s", id, state), func() bool {
		p, err := h.st.GetPlan(id)
		return err == nil && p.State == state
	}, func() string {
		p, err := h.st.GetPlan(id)
		var parts []string
		for _, pt := range p.Parts {
			parts = append(parts, pt.State)
		}
		return fmt.Sprintf("现在 %q 段 %v（%v）\n日志：%s", p.State, parts, err, h.logText())
	})
}

func (h *oh) waitSent(n int) {
	h.t.Helper()
	eventually(h.t, fmt.Sprintf("适配器收到 %d 次 Send", n), func() bool { return len(h.ad.Sent()) >= n })
}

func (h *oh) partStates(id string) []string {
	h.t.Helper()
	var out []string
	for _, p := range h.plan(id).Parts {
		out = append(out, p.State)
	}
	return out
}

func txt(s string) types.Part { return types.Message(s) }

// say 让角色 ca 在群 g1 里按顺序发几段文字，返回计划号
func (h *oh) say(texts ...string) string {
	h.t.Helper()
	return h.sayIn(group("g1"), texts...)
}

func (h *oh) sayIn(conv types.Conversation, texts ...string) string {
	h.t.Helper()
	var parts []types.Part
	for _, s := range texts {
		parts = append(parts, txt(s))
	}
	id, err := h.out.Send(context.Background(), "ca", types.OriginCharacter, conv, parts)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// hold 让之后的每次 Send 都停住，直到测试放行：started 收到每次调用，release 给它一个回执
type hold struct {
	started chan fake.SendCall
	rel     chan func(fake.SendCall) types.Receipt
}

func (h *oh) hold() *hold {
	d := &hold{started: make(chan fake.SendCall, 32), rel: make(chan func(fake.SendCall) types.Receipt, 32)}
	h.ad.OnSend(func(ctx context.Context, c fake.SendCall) (types.Receipt, error) {
		d.started <- c
		select {
		case f := <-d.rel:
			return f(c), nil
		case <-ctx.Done():
			return fake.Unknown(c.Part.ID, "ctx 取消", c.At), nil
		}
	})
	return d
}

func (d *hold) release(f func(fake.SendCall) types.Receipt) { d.rel <- f }

// next 等下一次被停住的 Send；等 5 秒没有就让测试失败（别无限等：被测的代码写坏了，常常就是这一次永远不来）
func (d *hold) next(t *testing.T) fake.SendCall {
	t.Helper()
	select {
	case c := <-d.started:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("等了 5 秒，适配器还没有收到下一次 Send")
		return fake.SendCall{}
	}
}

func confirmedWith(pid string) func(fake.SendCall) types.Receipt {
	return func(c fake.SendCall) types.Receipt { return fake.Confirmed(c.Part.ID, pid, c.At) }
}

// 回声：协议端把她自己发的消息推回来。account 是发它的号（回声里的发言人）
func echoOf(account string, conv types.Conversation, platformID, text string) types.Inbound {
	return types.Inbound{
		Kind: types.InboundSelf, Account: account, Conv: conv, PlatformID: platformID,
		From: types.User{ID: account}, Time: baseTime, Segs: []types.Seg{{Kind: types.SegText, Text: text}},
	}
}

func sentTexts(h *oh) []string {
	var out []string
	for _, c := range h.ad.Sent() {
		out = append(out, types.BodyTextOf(c.Part.Body))
	}
	return out
}

func countRows(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Events.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ───────────────────────── 发一条计划 ─────────────────────────

// 三段文字：计划、events 那一行、各段一起落库；第一段马上发，后面每段发之前停 min(2500ms, 字数×40ms)，
// 从上一段确认以后算起；每段的回执照抄段号；全部确认后计划 done；回执带的平台消息号映射到这次回复的事件
func TestSendThreeParts(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{MediaGroup: types.MediaCaps{Image: true}, Poke: true, Echo: true})
	h.run(nil)
	conv := group("g1")
	id := h.say("第一条", "第二条是比较长的一句话", "第三条")

	p := h.plan(id)
	if p.Kind != store.PlanKindSend || p.Account != acctA || p.Origin != types.OriginCharacter || p.Conversation != conv.String() || p.CharacterID != "ca" {
		t.Fatalf("计划的样子：%+v", p)
	}
	var pauses []int
	for _, pt := range p.Parts {
		pauses = append(pauses, pt.PauseMS)
	}
	if want := []int{0, 440, 120}; fmt.Sprint(pauses) != fmt.Sprint(want) {
		t.Fatalf("停顿定死在计划里：%v，应该是 %v（第二段 11 个字 × 40ms，第三段 3 个字）", pauses, want)
	}

	h.waitSent(1)
	h.waitPart(id, 0, store.PartConfirmed)
	h.clk.WaitTimer(t, 440*time.Millisecond) // 第二段的停顿
	h.clk.Advance(439 * time.Millisecond)
	if n := len(h.ad.Sent()); n != 1 {
		t.Fatalf("停顿还差 1 毫秒，第二段不该发：已发 %d 次", n)
	}
	h.clk.Advance(time.Millisecond)
	h.waitSent(2)
	h.waitPart(id, 1, store.PartConfirmed)
	h.clk.WaitTimer(t, 120*time.Millisecond)
	h.clk.Advance(120 * time.Millisecond)
	h.waitPlan(id, store.PlanDone)

	sent := h.ad.Sent()
	for i, c := range sent {
		if c.Account != acctA || c.Part.ID != fmt.Sprintf("%s:%d", id, i) || c.Part.Conv != conv || c.Part.Kind != types.PartMessage {
			t.Errorf("第 %d 次 Send：%+v", i, c)
		}
	}
	if got := sentTexts(h); fmt.Sprint(got) != fmt.Sprint([]string{"第一条", "第二条是比较长的一句话", "第三条"}) {
		t.Fatalf("发出去的文字：%v", got)
	}

	p = h.plan(id)
	for i, pt := range p.Parts {
		if pt.State != store.PartConfirmed || pt.Via != types.ViaReply || pt.PlatformID != fmt.Sprint(1001+i) || pt.Attempts != 1 || pt.SentAt == 0 {
			t.Errorf("第 %d 段：%+v", i, pt)
		}
		var mapped int64
		if err := h.st.Events.QueryRow(`SELECT event_seq FROM channel_msgs WHERE channel = 'qq' AND conversation = ? AND platform_id = ?`, conv.String(), pt.PlatformID).Scan(&mapped); err != nil || mapped != p.EventSeq {
			t.Errorf("平台消息号 %s 该映射到这次回复的事件 %d：%d %v", pt.PlatformID, p.EventSeq, mapped, err)
		}
	}
	if p.DoneAt == 0 {
		t.Error("done_at 该有")
	}
	if viaOutlet, err := h.out.Plan(id, conv); err != nil || viaOutlet.EventSeq != p.EventSeq || len(viaOutlet.Parts) != 3 {
		t.Errorf("出口的 Plan 读到的该和库里一样（第 3 步要靠它拿到事件号）：%+v %v", viaOutlet, err)
	}

	// 她这句话在 events 里只有一行：文字段用空行连起来，origin = character，她自己看到了；segments_json 带着各段
	var text, origin, actorKind, actorID, seg string
	if err := h.st.Events.QueryRow(`SELECT text, origin, actor_kind, actor_id, segments_json FROM events WHERE seq = ?`, p.EventSeq).Scan(&text, &origin, &actorKind, &actorID, &seg); err != nil {
		t.Fatal(err)
	}
	if text != "第一条\n\n第二条是比较长的一句话\n\n第三条" || origin != "character" || actorKind != "character" || actorID != "ca" {
		t.Errorf("events 那一行：%q %s %s %s", text, origin, actorKind, actorID)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE conversation = ?`, conv.String()); n != 1 {
		t.Errorf("一次回复只该写一行 events：%d", n)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM observations WHERE event_seq = ? AND character_id = 'ca'`, p.EventSeq); n != 1 {
		t.Errorf("她自己该看到这一行：%d", n)
	}
	if strings.Contains(h.logText(), "第一条") {
		t.Errorf("日志里不放消息原文：%s", h.logText())
	}
}

// 回执三态各走一遍（10.4）；适配器不守规矩的回执一律按结果未知，不当成功也不重发（盘点 4.4 第 1 条）
func TestReceiptStatuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		send      fake.SendFunc
		wantPart  string
		wantPlan  string
		wantSends int
		errHas    string
	}{
		{"确认", nil, store.PartConfirmed, store.PlanDone, 1, ""},
		{"结果未知（超时、回了异步码都是这个）", func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Unknown(c.Part.ID, "retcode=1401 已接受，异步执行", c.At), nil
		}, store.PartUnknown, store.PlanUnknown, 1, "retcode=1401"},
		{"零值回执不是成功", func(context.Context, fake.SendCall) (types.Receipt, error) {
			return types.Receipt{}, nil
		}, store.PartUnknown, store.PlanUnknown, 1, "不合规"},
		{"回执状态不认识", func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return types.Receipt{PartID: c.Part.ID, Status: "ok"}, nil
		}, store.PartUnknown, store.PlanUnknown, 1, "不合规"},
		{"回执说的是别的段", func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Confirmed("别的段:9", "5555", c.At), nil
		}, store.PartUnknown, store.PlanUnknown, 1, "不是这一段"},
		{"没带平台消息号的确认也只能算确认", func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Confirmed(c.Part.ID, "", c.At), nil
		}, store.PartConfirmed, store.PlanDone, 1, ""},
		{"没确认的回执带了平台消息号", func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			r := fake.Failed(c.Part.ID, "拒绝", c.At)
			r.PlatformID = "5555"
			return r, nil
		}, store.PartUnknown, store.PlanUnknown, 1, "不合规"},
		{"ErrUnsupported 是明确失败，不重试", func(context.Context, fake.SendCall) (types.Receipt, error) {
			return types.Receipt{}, types.ErrUnsupported
		}, store.PartFailed, store.PlanFailed, 1, "不支持"},
		{"别的 error 不是适配器该返回的：按未知办", func(context.Context, fake.SendCall) (types.Receipt, error) {
			return types.Receipt{}, errors.New("连接 reset")
		}, store.PartUnknown, store.PlanUnknown, 1, "ErrUnsupported 以外"},
		{"适配器 panic 也按未知办，队列不停", func(context.Context, fake.SendCall) (types.Receipt, error) {
			panic("适配器炸了")
		}, store.PartUnknown, store.PlanUnknown, 1, "panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newOH(t, types.Caps{})
			h.run(nil)
			if tc.send != nil {
				h.ad.OnSend(tc.send)
			}
			id := h.say("一条")
			h.waitPlan(id, tc.wantPlan)
			p := h.plan(id)
			if p.Parts[0].State != tc.wantPart || !strings.Contains(p.Parts[0].Err, tc.errHas) {
				t.Fatalf("段：%+v，应该 %s 且错误含 %q", p.Parts[0], tc.wantPart, tc.errHas)
			}
			// 钟拨过很久也不会有第二次 Send（未知、不支持都不重试）
			h.clk.Advance(10 * time.Minute)
			if n := len(h.ad.Sent()); n != tc.wantSends {
				t.Fatalf("Send 被调了 %d 次，应该 %d 次", n, tc.wantSends)
			}
		})
	}
}

// 明确失败最多发 3 次、每次隔 1 秒；用完重试，段失败，计划 failed。第 2 次成功就不再发
func TestRetryOnExplicitFailure(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		return fake.Failed(c.Part.ID, "没写出去", c.At), nil
	})
	id := h.say("一条")
	h.waitSent(1)
	h.waitPart(id, 0, store.PartRetrying)
	h.clk.WaitTimer(t, time.Second)
	h.clk.Advance(999 * time.Millisecond)
	if n := len(h.ad.Sent()); n != 1 {
		t.Fatalf("还差 1 毫秒，不该重试：%d", n)
	}
	h.clk.Advance(time.Millisecond)
	h.waitSent(2)
	h.waitPart(id, 0, store.PartRetrying)
	h.clk.WaitTimer(t, time.Second)
	h.clk.Advance(time.Second)
	h.waitPlan(id, store.PlanFailed)

	h.clk.Advance(time.Hour)
	sent := h.ad.Sent()
	if len(sent) != 3 {
		t.Fatalf("最多发 3 次：%d", len(sent))
	}
	for i, c := range sent {
		if got := c.At.Sub(baseTime); got != time.Duration(i)*time.Second {
			t.Errorf("第 %d 次发送在 +%v，应该每次隔 1 秒", i+1, got)
		}
	}
	if pt := h.plan(id).Parts[0]; pt.State != store.PartFailed || pt.Attempts != 3 || pt.Err != "没写出去" {
		t.Fatalf("段：%+v", pt)
	}

	// 第 2 次成功：不再发第 3 次
	h2 := newOH(t, types.Caps{})
	h2.run(nil)
	h2.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.N == 1 {
			return fake.Failed(c.Part.ID, "断了一下", c.At), nil
		}
		return fake.Confirmed(c.Part.ID, "7001", c.At), nil
	})
	id2 := h2.say("一条")
	h2.waitPart(id2, 0, store.PartRetrying)
	h2.clk.WaitTimer(t, time.Second)
	h2.clk.Advance(time.Second)
	h2.waitPlan(id2, store.PlanDone)
	if pt := h2.plan(id2).Parts[0]; pt.Attempts != 2 || pt.PlatformID != "7001" || len(h2.ad.Sent()) != 2 {
		t.Fatalf("重试成功：%+v，Send %d 次", pt, len(h2.ad.Sent()))
	}
}

// 一段没发出去（用完重试，或者结果未知），后面还没发的段都标已中止，Send 不再被调（不照发，规格 第十七节）
func TestFailedPartAbortsTheRest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		failAt   int
		receipt  func(fake.SendCall) types.Receipt
		wantPlan string
		wantSent int
		want     []string
	}{
		{"第一段用完重试", 0, func(c fake.SendCall) types.Receipt { return fake.Failed(c.Part.ID, "拒绝", c.At) },
			store.PlanFailed, 3, []string{store.PartFailed, store.PartAborted, store.PartAborted}},
		{"第二段用完重试，第一段已发出去", 1, func(c fake.SendCall) types.Receipt { return fake.Failed(c.Part.ID, "拒绝", c.At) },
			store.PlanPartial, 4, []string{store.PartConfirmed, store.PartFailed, store.PartAborted}},
		{"第二段结果未知，不重试", 1, func(c fake.SendCall) types.Receipt { return fake.Unknown(c.Part.ID, "超时", c.At) },
			store.PlanUnknown, 2, []string{store.PartConfirmed, store.PartUnknown, store.PartAborted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newOH(t, types.Caps{})
			h.run(nil)
			h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
				if strings.HasSuffix(c.Part.ID, fmt.Sprintf(":%d", tc.failAt)) {
					return tc.receipt(c), nil
				}
				return fake.Confirmed(c.Part.ID, fmt.Sprint(5000+c.N), c.At), nil
			})
			id := h.say("甲", "乙", "丙")
			// 一路拨钟直到计划结束：每次等有计时器在等（停顿或重试间隔）或者计划已经结束再拨
			eventually(t, "计划结束", func() bool {
				if h.plan(id).State != store.PlanSending {
					return true
				}
				if h.clk.Pending() > 0 {
					h.clk.Advance(time.Second)
				}
				return false
			})
			if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint(tc.want) || h.plan(id).State != tc.wantPlan {
				t.Fatalf("段 %v 计划 %s，应该 %v %s", got, h.plan(id).State, tc.want, tc.wantPlan)
			}
			h.clk.Advance(time.Hour)
			if n := len(h.ad.Sent()); n != tc.wantSent {
				t.Fatalf("Send 调了 %d 次，应该 %d 次（后面的段不能照发）", n, tc.wantSent)
			}
			if pt := h.plan(id).Parts[2]; pt.State != store.PartAborted || pt.Err == "" || pt.SentAt != 0 {
				t.Fatalf("没发的段该带着原因、没有 sent_at：%+v", pt)
			}
		})
	}
}

// 停顿从上一段的终态算起，不是从上一段开始发算起：第一段的 Send 拖了 3 秒，第二段的 440ms 从它确认以后才开始数
func TestPauseCountsFromPreviousTerminalState(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	d := h.hold()
	id := h.say("第一条", "第二条是比较长的一句话")
	d.next(t)
	h.clk.Advance(3 * time.Second) // 第一段还没回执，钟走了 3 秒，已经超过第二段的停顿
	if n := len(h.ad.Sent()); n != 1 {
		t.Fatalf("上一段没到终态，下一段不能开始：%d", n)
	}
	d.release(confirmedWith("6001"))
	h.waitPart(id, 0, store.PartConfirmed)
	h.clk.WaitTimer(t, 440*time.Millisecond) // 停顿现在才开始数
	h.clk.Advance(439 * time.Millisecond)
	if n := len(h.ad.Sent()); n != 1 {
		t.Fatalf("停顿从终态算起，才过了 439ms：%d", n)
	}
	h.clk.Advance(time.Millisecond)
	d.next(t)
	d.release(confirmedWith("6002"))
	h.waitPlan(id, store.PlanDone)
}

// 同一个会话的出站一条队列：前一条计划的所有段发完，后一条才开始，段与段之间不插别的计划的段；
// 不同会话互不等待
func TestSameConversationSerialDifferentConversationsParallel(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	d := h.hold()
	a := h.sayIn(group("g1"), "甲一", "甲二")
	b := h.sayIn(group("g1"), "乙一")
	other := h.sayIn(group("g2"), "别的群")

	got := map[string]bool{}
	for i := 0; i < 2; i++ { // g1 的第一段、g2 的第一段都已经在发（并行）；g1 的乙一不在
		c := d.next(t)
		got[types.BodyTextOf(c.Part.Body)] = true
	}
	if !got["甲一"] || !got["别的群"] {
		t.Fatalf("g1 的第一段和 g2 的该同时在发：%v", got)
	}
	d.release(confirmedWith("1")) // 随便一个先放行
	d.release(confirmedWith("2"))
	h.waitPlan(other, store.PlanDone)
	h.waitPart(a, 0, store.PartConfirmed)
	h.clk.WaitTimer(t, 80*time.Millisecond)
	if n := len(h.ad.Sent()); n != 2 {
		t.Fatalf("g1 的乙一不能插在甲一和甲二之间：已发 %d", n)
	}
	h.clk.Advance(time.Second)
	c := d.next(t)
	if types.BodyTextOf(c.Part.Body) != "甲二" {
		t.Fatalf("甲一之后该是甲二：%s", types.BodyTextOf(c.Part.Body))
	}
	d.release(confirmedWith("3"))
	h.waitPlan(a, store.PlanDone)
	c = d.next(t)
	if types.BodyTextOf(c.Part.Body) != "乙一" {
		t.Fatalf("甲整个发完才轮到乙：%s", types.BodyTextOf(c.Part.Body))
	}
	d.release(confirmedWith("4"))
	h.waitPlan(b, store.PlanDone)
}

// 等 SendTimeout 没有回执：按结果未知办，不重试；适配器的 ctx 被取消
func TestSendTimeoutIsUnknown(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	cancelled := make(chan struct{})
	h.ad.OnSend(func(ctx context.Context, c fake.SendCall) (types.Receipt, error) {
		<-ctx.Done()
		close(cancelled)
		return types.Receipt{}, nil
	})
	id := h.say("一条", "二条")
	h.waitSent(1)
	h.clk.WaitTimer(t, 30*time.Second)
	h.clk.Advance(30*time.Second - time.Millisecond)
	if st := h.plan(id).Parts[0].State; st != store.PartSending {
		t.Fatalf("还差 1 毫秒到超时：%s", st)
	}
	h.clk.Advance(time.Millisecond)
	h.waitPlan(id, store.PlanUnknown)
	<-cancelled
	if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartUnknown, store.PartAborted}) {
		t.Fatalf("%v", got)
	}
	if pt := h.plan(id).Parts[0]; !strings.Contains(pt.Err, "超时") {
		t.Fatalf("%+v", pt)
	}
}

// 适配器不守"ctx 取消就返回"的规矩，也不会把队列卡死：超时照样走下去
func TestStuckAdapterDoesNotBlockTheLane(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	forever := make(chan struct{})
	defer close(forever)
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.N == 1 {
			<-forever // 不看 ctx，卡着
		}
		return fake.Confirmed(c.Part.ID, "9", c.At), nil
	})
	first := h.say("会卡住的")
	second := h.say("后面的")
	h.waitSent(1)
	h.clk.WaitTimer(t, 30*time.Second)
	h.clk.Advance(30 * time.Second)
	h.waitPlan(first, store.PlanUnknown)
	h.waitPlan(second, store.PlanDone)
}

// 一条计划发送时出了意外（这里用日志钩子 panic 模拟），只清理它自己：别的会话在发的那一段不能被一起转成结果未知
func TestPanicInOnePlanDoesNotTouchOthers(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		orig := c.Logf
		c.Logf = func(format string, args ...any) {
			if msg := fmt.Sprintf(format, args...); strings.Contains(msg, "qq:group:boom") && strings.Contains(msg, "不重发") {
				panic("日志钩子炸了")
			}
			orig(format, args...)
		}
	})
	h.run(nil)
	calmSending, calmRel := make(chan struct{}, 4), make(chan struct{}, 4)
	h.ad.OnSend(func(ctx context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.Part.Conv.ID == "calm" {
			calmSending <- struct{}{}
			select {
			case <-calmRel:
			case <-ctx.Done():
			}
			return fake.Confirmed(c.Part.ID, "5001", c.At), nil
		}
		if types.BodyTextOf(c.Part.Body) == "甲" {
			return fake.Unknown(c.Part.ID, "超时", c.At), nil
		}
		return fake.Confirmed(c.Part.ID, fmt.Sprint(6000+c.N), c.At), nil
	})
	calm := h.sayIn(group("calm"), "别的群正在发")
	<-calmSending // 别的群的这一段发送中，回执还没回

	boom := h.sayIn(group("boom"), "甲", "乙")
	h.waitPlan(boom, store.PlanUnknown) // 出意外的这条被清理：甲未知，乙中止
	if got := h.partStates(boom); fmt.Sprint(got) != fmt.Sprint([]string{store.PartUnknown, store.PartAborted}) {
		t.Fatalf("%v", got)
	}
	if st := h.plan(calm).Parts[0].State; st != store.PartSending {
		t.Fatalf("别的会话在发的那一段不能被动：%s", st)
	}
	calmRel <- struct{}{}
	h.waitPlan(calm, store.PlanDone)
	if !strings.Contains(h.logText(), "发送时出了意外") {
		t.Fatal("出意外要记日志")
	}
	// 队列没停：同一个会话接着发新的
	next := h.sayIn(group("boom"), "还能说话")
	h.waitPlan(next, store.PlanDone)
}

// ───────────────────────── 校验和路由 ─────────────────────────

// 出口收下之前的检查：每一条被拒的，都不写库、不排队（B 组：来源只能是 character 或 automation:<插件名>，试运行由出口决定）
func TestSendRejections(t *testing.T) {
	t.Parallel()
	caps := types.Caps{Poke: false}
	var checked []string
	h := newOH(t, caps, func(c *OutletConfig) {
		c.Check = func(character, origin string, conv types.Conversation, parts []types.Part) error {
			checked = append(checked, character+"/"+origin)
			if strings.HasPrefix(origin, "automation:blocked") {
				return errors.New("风控间隔没到")
			}
			return nil
		}
	})
	h.run(nil)
	ctx := context.Background()
	g1 := group("g1")
	for _, tc := range []struct {
		name   string
		char   string
		origin string
		conv   types.Conversation
		parts  []types.Part
		want   string
	}{
		{"没有角色", "", "character", g1, []types.Part{txt("x")}, "没有说话的角色"},
		{"来源不认识", "ca", "someone", g1, []types.Part{txt("x")}, "不认识"},
		{"调用方不能填试运行", "ca", types.OriginDryRun, g1, []types.Part{txt("x")}, "由出口按会话的档位决定"},
		{"插件名是空的", "ca", "automation:", g1, []types.Part{txt("x")}, "不认识"},
		{"没有段", "ca", "character", g1, nil, "没有要发的段"},
		{"空文字", "ca", "character", g1, []types.Part{txt("  \n")}, "空文字"},
		{"渠道发不了图", "ca", "character", g1, []types.Part{{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage}}}}, "不支持"},
		{"渠道戳不了人", "ca", "automation:poke", g1, []types.Part{{Kind: types.PartPoke, Ref: "30001"}}, "戳不了"},
		{"种类不认识", "ca", "character", g1, []types.Part{{Kind: "voice", Ref: "x"}}, "不认识"},
		{"没有适配器的渠道", "ca", "character", types.Conversation{Channel: "wx", Scene: types.SceneGroup, ID: "g1"}, []types.Part{txt("x")}, "没有适配器"},
		{"会话不合法", "ca", "character", types.Conversation{Channel: chQQ, Scene: types.SceneGroup}, []types.Part{txt("x")}, "会话不合法"},
		{"角色在这个渠道没有号", "cz", "character", g1, []types.Part{txt("x")}, "没有绑号"},
		{"私聊用了别的角色的号", "ca", "character", private(acctB, friend), []types.Part{txt("x")}, "没有绑给角色 ca"},
		{"私聊用了没配的号", "ca", "character", private("99999", friend), []types.Part{txt("x")}, "没有绑给角色 ca"},
		{"发送检查没过", "ca", "automation:blocked-by-check", g1, []types.Part{txt("x")}, "发送检查没过"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := h.out.Send(ctx, tc.char, tc.origin, tc.conv, tc.parts)
			if err == nil || id != "" {
				t.Fatalf("id=%q err=%v，应该整批拒绝（%s）", id, err, tc.name)
			}
		})
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM outbound_plans`) + countRows(t, h.st, `SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("被拒的不该写库：%d 行", n)
	}
	if n := len(h.ad.Sent()) + len(h.ad.Pokes()); n != 0 {
		t.Fatalf("被拒的不该发：%d", n)
	}
	if len(checked) != 1 || checked[0] != "ca/automation:blocked-by-check" {
		t.Fatalf("发送检查只对前面都过了的那一条问过：%v", checked)
	}
}

// 账号怎么定（ADR-007）：私聊是会话串里她的号；群里是这个角色绑的第一个号；两个角色各用各的号
func TestAccountRouting(t *testing.T) {
	t.Parallel()
	roster := Roster{Owners: map[string]string{chQQ: owner}, Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, "20003", "ca"}, {chQQ, acctB, "cb"}}}
	h := newOH(t, types.Caps{}, func(c *OutletConfig) { c.Roster = roster })
	h.run(nil)
	ctx := context.Background()
	say := func(char string, conv types.Conversation) {
		t.Helper()
		if _, err := h.out.Send(ctx, char, "character", conv, []types.Part{txt("嗯")}); err != nil {
			t.Fatal(err)
		}
	}
	say("ca", group("g1"))
	say("cb", group("g1"))
	say("ca", private("20003", friend))
	say("cb", private(acctB, friend))
	eventually(t, "四次 Send", func() bool { return len(h.ad.Sent()) == 4 })
	var got []string
	for _, c := range h.ad.Sent() {
		got = append(got, c.Account+"@"+c.Part.Conv.String())
	}
	want := map[string]bool{
		acctA + "@qq:group:g1": true, acctB + "@qq:group:g1": true,
		"20003@qq:private:20003:" + friend: true, acctB + "@qq:private:" + acctB + ":" + friend: true,
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("不该有的 Send：%s（全部 %v）", g, got)
		}
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("该有没有的：%v", want)
	}
	// 两个角色在同一个群各自说话，计划各记各的号
	if n := countRows(t, h.st, `SELECT count(DISTINCT account) FROM outbound_plans WHERE conversation = 'qq:group:g1'`); n != 2 {
		t.Errorf("同群两个角色该是两个号：%d", n)
	}
}

// 启动即报错：同一个渠道绑了两个适配器；名册里同一个号绑给两个角色；没有库
func TestNewOutletRejectsBadSetup(t *testing.T) {
	t.Parallel()
	p, _ := paths.Init(t.TempDir())
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a1, a2 := fake.New(chQQ, types.Caps{}), fake.New(chQQ, types.Caps{})
	if _, err := NewOutlet(OutletConfig{Stores: store.Single(st), Roster: testRoster(), Adapters: []types.Adapter{a1, a2}}); err == nil || !strings.Contains(err.Error(), "两个适配器") {
		t.Errorf("同一个渠道两个适配器该报错：%v", err)
	}
	bad := Roster{Accounts: []Account{{chQQ, acctA, "ca"}, {chQQ, acctA, "cb"}}}
	if _, err := NewOutlet(OutletConfig{Stores: store.Single(st), Roster: bad, Adapters: []types.Adapter{a1}}); err == nil || !strings.Contains(err.Error(), "只能绑一个角色") {
		t.Errorf("一个号绑两个角色该报错：%v", err)
	}
	if _, err := NewOutlet(OutletConfig{Roster: testRoster()}); err == nil {
		t.Error("没有库该报错")
	}
	if _, err := NewOutlet(OutletConfig{Stores: store.Single(st), Roster: testRoster(), Adapters: []types.Adapter{nil}}); err == nil {
		t.Error("空适配器该报错")
	}
}

// ───────────────────────── 档位：试运行、只记录、切档 ─────────────────────────

// 试运行档：写库（events 的 origin 记 automation:dry-run、计划 dry-run、段 dry-run），不调 Send；
// 她下一轮的历史里没有这一句（history 跳过 automation:dry-run）；说话档的邻居不变
func TestDryRunWritesButNeverSends(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	modes := map[string]string{"qq:group:g1": ModeDryRun, "qq:group:g2": ModeSpeak}
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		c.Mode = func(_, _ string, conv types.Conversation) string {
			mu.Lock()
			defer mu.Unlock()
			return modes[conv.String()]
		}
		c.Check = func(_, _ string, conv types.Conversation, _ []types.Part) error {
			if conv.ID == "g1" { // 试运行的群：发送检查一问就报错，证明试运行不问
				return errors.New("试运行不该过发送检查")
			}
			return nil
		}
	})
	h.run(nil)
	dry := h.sayIn(group("g1"), "试运行想说的")
	h.clk.Advance(time.Hour)
	if n := len(h.ad.Sent()); n != 0 {
		t.Fatalf("试运行不调 Send：%d", n)
	}
	p := h.plan(dry)
	if p.Kind != store.PlanKindDryRun || p.State != store.PlanKindDryRun || p.Parts[0].State != store.PartDryRun || p.Origin != types.OriginCharacter || p.EventSeq == 0 {
		t.Fatalf("试运行的计划：%+v", p)
	}
	var origin, actor string
	if err := h.st.Events.QueryRow(`SELECT origin, actor_id FROM events WHERE seq = ?`, p.EventSeq).Scan(&origin, &actor); err != nil || origin != types.OriginDryRun || actor != "ca" {
		t.Fatalf("试运行的事件：%s %s %v", origin, actor, err)
	}

	// 下一轮的历史：群 g1 里试运行的这句不在
	snap, msgs, _, err := history.For(h.st.Events, "ca", "qq:group:g1", 50, nil)
	if err != nil || snap != nil {
		t.Fatal(err, snap)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "试运行想说的") {
			t.Fatalf("试运行的话进了她的下一轮历史：%+v", msgs)
		}
	}

	// 邻居：说话档的群 g2 照常发、照常进历史
	live := h.sayIn(group("g2"), "真说的")
	h.waitPlan(live, store.PlanDone)
	_, msgs, _, err = history.For(h.st.Events, "ca", "qq:group:g2", 50, nil)
	if err != nil || len(msgs) != 1 || msgs[0].Role != "assistant" || msgs[0].Text != "真说的" {
		t.Fatalf("说话档的历史：%+v %v", msgs, err)
	}
}

// 只记录档（和不认识的档位、钩子 panic）不收：返回 ErrNotSpeaking，不写库
func TestRecordOnlyModeRefuses(t *testing.T) {
	t.Parallel()
	mode := ModeRecord
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		c.Mode = func(_, _ string, _ types.Conversation) string {
			if mode == "panic" {
				panic("查档位炸了")
			}
			return mode
		}
	})
	h.run(nil)
	for _, m := range []string{ModeRecord, "没听说过的档", "panic"} {
		mode = m
		_, err := h.out.Send(context.Background(), "ca", "character", group("g1"), []types.Part{txt("x")})
		if !errors.Is(err, ErrNotSpeaking) {
			t.Errorf("档位 %q：%v", m, err)
		}
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM outbound_plans`) + countRows(t, h.st, `SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("只记录档不该写库：%d", n)
	}
}

// 停顿中途切档：后面的段不调 Send，标已中止（原因写切档）；发送中的那一段让它完成并记结果；
// 排在后面的另一条计划开始时发现已经不是说话档，整条中止；别的会话（邻居）不受影响
func TestSwitchModeDuringPause(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	silent := map[string]bool{}
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		c.Mode = func(_, _ string, conv types.Conversation) string {
			mu.Lock()
			defer mu.Unlock()
			if silent[conv.String()] {
				return ModeRecord
			}
			return ModeSpeak
		}
	})
	h.run(nil)
	first := h.sayIn(group("g1"), "甲", "乙是比较长的一句", "丙")
	queued := h.sayIn(group("g1"), "排在后面的")
	h.waitPart(first, 0, store.PartConfirmed)
	h.clk.WaitTimer(t, 320*time.Millisecond) // 第二段的停顿

	mu.Lock()
	silent["qq:group:g1"] = true
	mu.Unlock()
	h.clk.Advance(time.Second)
	h.waitPlan(first, store.PlanPartial)
	h.waitPlan(queued, store.PlanAborted)

	if n := len(h.ad.Sent()); n != 1 {
		t.Fatalf("切档以后不该再调 Send：%d 次", n)
	}
	if got := h.partStates(first); fmt.Sprint(got) != fmt.Sprint([]string{store.PartConfirmed, store.PartAborted, store.PartAborted}) {
		t.Fatalf("%v", got)
	}
	if e := h.plan(first).Parts[1].Err; !strings.Contains(e, "切档") {
		t.Fatalf("原因该写切档：%q", e)
	}

	// 邻居：另一个群不受影响
	other := h.sayIn(group("g2"), "照常说")
	h.waitPlan(other, store.PlanDone)
}

// 切档时发送中的那一段让它完成：切档那一刻 Send 还没回执，回执回来记确认，后面的才中止
func TestSwitchModeLetsInFlightFinish(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	silent := false
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		c.Mode = func(_, _ string, _ types.Conversation) string {
			mu.Lock()
			defer mu.Unlock()
			if silent {
				return ModeDryRun
			}
			return ModeSpeak
		}
	})
	h.run(nil)
	d := h.hold()
	id := h.say("甲", "乙")
	d.next(t)
	mu.Lock()
	silent = true
	mu.Unlock()
	d.release(confirmedWith("1"))
	h.waitPlan(id, store.PlanPartial)
	if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartConfirmed, store.PartAborted}) {
		t.Fatalf("发送中的完成，后面的中止：%v", got)
	}
}

// ───────────────────────── 戳一戳、不发的计划 ─────────────────────────

// 戳一戳走 Adapter.Poke，不是 Send；没有段号，回执的 PartID 空着；不占停顿；纯戳一戳的计划没有 events 那一行
func TestPokePlan(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{Poke: true})
	h.run(nil)
	id, err := h.out.Send(context.Background(), "ca", "automation:poke", group("g1"), []types.Part{{Kind: types.PartPoke, Ref: friend}})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(id, store.PlanDone)
	pokes := h.ad.Pokes()
	if len(pokes) != 1 || pokes[0].Account != acctA || pokes[0].Target != friend || pokes[0].Conv != group("g1") || len(h.ad.Sent()) != 0 {
		t.Fatalf("Poke 调用：%+v，Send %d 次", pokes, len(h.ad.Sent()))
	}
	p := h.plan(id)
	if p.EventSeq != 0 || p.Origin != "automation:poke" || p.Parts[0].PauseMS != 0 || p.Parts[0].PlatformID != "2001" {
		t.Fatalf("%+v", p)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("纯戳一戳不写 events：%d", n)
	}
	// 戳一戳的回执也要合规：给个零值回执不是成功
	h.ad.OnPoke(func(context.Context, fake.PokeCall) (types.Receipt, error) { return types.Receipt{}, nil })
	id2, _ := h.out.Send(context.Background(), "ca", "automation:poke", group("g1"), []types.Part{{Kind: types.PartPoke, Ref: friend}})
	h.waitPlan(id2, store.PlanUnknown)
	h.ad.OnPoke(func(_ context.Context, c fake.PokeCall) (types.Receipt, error) {
		return fake.Failed("", "不让戳", c.At), nil
	})
	id3, _ := h.out.Send(context.Background(), "ca", "automation:poke", group("g1"), []types.Part{{Kind: types.PartPoke, Ref: friend}})
	h.waitPart(id3, 0, store.PartRetrying)
	h.clk.WaitTimer(t, time.Second)
	h.clk.Advance(time.Second)
	h.waitPart(id3, 0, store.PartRetrying)
}

// 拦下、已打断、出错：只记一条计划，没有段、没有 events、不存原文，状态就是种类；不看档位
func TestRecordNonSendingPlans(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{}, func(c *OutletConfig) { c.Mode = func(_, _ string, _ types.Conversation) string { return ModeRecord } })
	h.run(nil)
	for _, k := range []string{store.PlanKindBlocked, store.PlanKindInterrupted, store.PlanKindError} {
		id, err := h.out.Record(context.Background(), "ca", "character", group("g1"), k, "原因："+k)
		if err != nil {
			t.Fatal(k, err)
		}
		p := h.plan(id)
		if p.Kind != k || p.State != k || p.Reason != "原因："+k || len(p.Parts) != 0 || p.EventSeq != 0 || p.Account != acctA {
			t.Fatalf("%s：%+v", k, p)
		}
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("不存原文、不写 events：%d", n)
	}
	for _, bad := range []string{store.PlanKindSend, store.PlanKindDryRun, "mystery"} {
		if _, err := h.out.Record(context.Background(), "ca", "character", group("g1"), bad, ""); err == nil {
			t.Errorf("Record 不记 %s", bad)
		}
	}
	if len(h.ad.Sent()) != 0 {
		t.Fatal("这些不发")
	}
}

// 文字以外的段在 events.text 里写成方括号（和入站的 PlainText 一样），引用和戳一戳没有字
func TestEventTextOfParts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		parts []types.Part
		want  string
	}{
		{"两条文字用空行连", []types.Part{txt("甲"), txt("乙")}, "甲\n\n乙"},
		{"文字加图片", []types.Part{txt("看"), {Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage}}}}, "看\n\n[image]"},
		{"动图", []types.Part{{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyGIF}}}}, "[gif]"},
		{"引用正文", []types.Part{{Kind: types.PartMessage, Body: types.TextBody("回你"), Quote: "-90004"}}, "回你"},
		{"戳一戳没有字", []types.Part{{Kind: types.PartPoke, Ref: "u"}}, ""},
	} {
		if got := eventText(tc.parts); got != tc.want {
			t.Errorf("%s：%q，应该 %q", tc.name, got, tc.want)
		}
	}
}

// ---------- SendHeld：落库以后先不发，调 Start 才进发送队列（回合定了以后交回内核用，第十节 10.1 第 7 步） ----------

func TestSendHeldWritesFirstAndSendsOnlyAfterStart(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	held, err := h.out.SendHeld(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("第一段"), txt("第二段")})
	if err != nil {
		t.Fatal(err)
	}
	p := h.plan(held.PlanID)
	if p.Kind != store.PlanKindSend || held.EventSeq == 0 || p.EventSeq != held.EventSeq || held.DryRun || len(p.Parts) != 2 || p.Parts[0].State != store.PartPending {
		t.Fatalf("落了库、段是待发：%+v %+v", held, p)
	}
	time.Sleep(30 * time.Millisecond)
	if n := len(h.ad.Sent()); n != 0 {
		t.Fatalf("Start 之前不该发，发了 %d 段", n)
	}
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "两段都发完", func() bool {
		h.clk.Advance(time.Second) // 第二段发之前的停顿，边拨边等
		return h.plan(held.PlanID).State == store.PlanDone
	})
	if sent := h.ad.Sent(); len(sent) != 2 || types.BodyTextOf(sent[0].Part.Body) != "第一段" || types.BodyTextOf(sent[1].Part.Body) != "第二段" {
		t.Fatalf("发出去的：%+v", sent)
	}
}

// 同一个会话里先落库的先 Start 就先发：两条计划按 Start 的先后排队
func TestSendHeldQueuesInStartOrder(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	a, err := h.out.SendHeld(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("甲")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.out.SendHeld(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("乙")})
	if err != nil {
		t.Fatal(err)
	}
	if a.EventSeq >= b.EventSeq {
		t.Fatalf("落库的先后就是 seq 的先后：%d %d", a.EventSeq, b.EventSeq)
	}
	for _, x := range []Held{a, b} {
		if err := x.Start(); err != nil {
			t.Fatal(err)
		}
	}
	h.waitSent(2)
	if sent := h.ad.Sent(); types.BodyTextOf(sent[0].Part.Body) != "甲" || types.BodyTextOf(sent[1].Part.Body) != "乙" {
		t.Fatalf("发送顺序：%+v", sent)
	}
}

// 试运行档：落了库（origin 是 dry-run）、Held.DryRun 为真、Start 什么也不发
func TestSendHeldDryRun(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{}, func(c *OutletConfig) { c.Mode = func(_, _ string, _ types.Conversation) string { return ModeDryRun } })
	h.run(nil)
	held, err := h.out.SendHeld(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("试试")})
	if err != nil || !held.DryRun || held.EventSeq == 0 {
		t.Fatalf("%+v %v", held, err)
	}
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if len(h.ad.Sent()) != 0 || h.plan(held.PlanID).Kind != store.PlanKindDryRun {
		t.Fatal("试运行不发")
	}
}

// 落库以后、Start 之前出口关了：Start 返回 ErrClosed，不发；这一条的段由关闭时的清理转成已中止
func TestSendHeldStartAfterCloseIsAborted(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	held, err := h.out.SendHeld(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("来不及了")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { h.out.Close(); close(done) }()
	for stop := false; !stop; {
		select {
		case <-done:
			stop = true
		case <-time.After(time.Millisecond):
			h.clk.Advance(10 * time.Second)
		}
	}
	if err := held.Start(); err != ErrClosed {
		t.Fatalf("Start 应返回 ErrClosed：%v", err)
	}
	if len(h.ad.Sent()) != 0 {
		t.Fatal("关闭以后不该发")
	}
}

// 发送检查没过的错能用 errors.Is 认出来（回合定了以后据此记「拦下」）
func TestSendHeldCheckFailureIsRecognizable(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		c.Check = func(string, string, types.Conversation, []types.Part) error { return fmt.Errorf("通道暂停中") }
	})
	h.run(nil)
	_, err := h.out.SendHeld(context.Background(), "ca", types.OriginCharacter, group("g1"), []types.Part{txt("x")})
	if !errors.Is(err, ErrSendCheck) || !strings.Contains(err.Error(), "通道暂停中") {
		t.Fatalf("%v", err)
	}
}

// 协议端明确拒绝这份内容：原样重发只会再被拒，所以只发一次、段失败、类别 rejected；
// 结果不明（没有回执）同样只发一次，由上层决定。旧代码对拒绝也试 3 次，群里的图被反复提交。
func TestRejectionAndUnknownAreNeverRetried(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		receipt   func(c fake.SendCall) types.Receipt
		wantState string
		wantPlan  string
	}{
		{"协议端拒绝", func(c fake.SendCall) types.Receipt { return fake.Failed(c.Part.ID, "协议端拒绝", c.At) }, store.PartFailed, store.PlanFailed},
		{"拒绝带细分", func(c fake.SendCall) types.Receipt {
			return fake.Failed(c.Part.ID, "协议端拒绝：不支持这类消息", c.At)
		}, store.PartFailed, store.PlanFailed},
		{"没有回执", func(c fake.SendCall) types.Receipt {
			return fake.Unknown(c.Part.ID, "协议端没有确认送达", c.At)
		}, store.PartUnknown, store.PlanUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newOH(t, types.Caps{})
			h.run(nil)
			h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) { return tc.receipt(c), nil })
			id := h.say("一条")
			h.waitPlan(id, tc.wantPlan)
			h.clk.Advance(time.Hour)
			if n := len(h.ad.Sent()); n != 1 {
				t.Fatalf("只该发 1 次，发了 %d 次", n)
			}
			if pt := h.plan(id).Parts[0]; pt.State != tc.wantState || pt.Attempts != 1 {
				t.Fatalf("段：%+v", pt)
			}
		})
	}
}
