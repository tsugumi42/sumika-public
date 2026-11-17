package channel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 回声认领的测试（规格 10.4"回声认领（总规则）"，第十六节 B"回声认领"一条）。号、群、文字都是编的。

func (h *oh) claim(in types.Inbound) bool {
	h.t.Helper()
	ok, err := h.out.ClaimEcho(in)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok
}

// unknownReceipt 让每次 Send 都回"结果未知"
func (h *oh) unknownReceipts() {
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		return fake.Unknown(c.Part.ID, "超时", c.At), nil
	})
}

// 回声先于回包到达：这一段还在发送中，回声认领成确认（via=echo，用回声的平台消息号），不等回包就接着发后面的；
// 之后回包迟到，不再改它，回包里的号也不映射
func TestEchoBeforeReceiptClaimsInFlightPart(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	late, lateDone := make(chan struct{}), make(chan struct{})
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.N == 1 {
			defer close(lateDone)
			<-late // 第一次 Send 的回包迟迟不来（也不看 ctx）
			return fake.Confirmed(c.Part.ID, "9999", c.At), nil
		}
		return fake.Confirmed(c.Part.ID, fmt.Sprint(8000+c.N), c.At), nil
	})
	id := h.say("第一条", "第二条")
	h.waitSent(1)
	if !h.claim(echoOf(acctA, group("g1"), "7001", "第一条")) {
		t.Fatal("回声先到，该认领发送中的段")
	}
	h.waitPart(id, 0, store.PartConfirmed)
	pt := h.plan(id).Parts[0]
	if pt.Via != types.ViaEcho || pt.PlatformID != "7001" {
		t.Fatalf("认领的样子：%+v", pt)
	}

	h.clk.WaitTimer(t, 120*time.Millisecond) // 不等回包，直接进入第二段的停顿
	h.clk.Advance(120 * time.Millisecond)
	h.waitPlan(id, store.PlanDone)

	close(late) // 回包终于来了
	<-lateDone
	time.Sleep(20 * time.Millisecond) // 给 goroutine 机会（错误的实现会在这里把它改回去）
	pt = h.plan(id).Parts[0]
	if pt.Via != types.ViaEcho || pt.PlatformID != "7001" {
		t.Fatalf("迟到的回包不该改已认领的段：%+v", pt)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM channel_msgs WHERE platform_id = '9999'`); n != 0 {
		t.Fatalf("迟到回包里的号不该映射：%d", n)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM channel_msgs WHERE platform_id = '7001'`); n != 1 {
		t.Fatalf("回声的号该映射到这次回复的事件：%d", n)
	}
}

// 结果未知的段，在窗口（从第一次调 Send 起 5 分钟，含）内被回声认领成确认，计划状态跟着变；每个回声只认一段，每段只认一次
func TestEchoClaimsUnknownPartWithinWindow(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	id := h.say("好的")
	h.waitPlan(id, store.PlanUnknown)

	h.clk.Advance(5 * time.Minute) // 正好 5 分钟：窗口含边界
	if !h.claim(echoOf(acctA, group("g1"), "7001", "好的")) {
		t.Fatal("5 分钟内该认领")
	}
	h.waitPlan(id, store.PlanDone)
	if pt := h.plan(id).Parts[0]; pt.State != store.PartConfirmed || pt.Via != types.ViaEcho || pt.PlatformID != "7001" {
		t.Fatalf("%+v", pt)
	}
	if h.claim(echoOf(acctA, group("g1"), "7002", "好的")) {
		t.Fatal("每段只认一次")
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM channel_msgs WHERE platform_id = '7002'`); n != 0 {
		t.Fatalf("没认领就不映射：%d", n)
	}
}

// 超过窗口不认：差 1 毫秒到点还认，过 1 毫秒就不认了，段保持结果未知
func TestEchoAfterWindowIsNotClaimed(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	id := h.say("好的")
	h.waitPlan(id, store.PlanUnknown)
	h.clk.Advance(5*time.Minute + time.Millisecond)
	if h.claim(echoOf(acctA, group("g1"), "7001", "好的")) {
		t.Fatal("窗口过了不该认领")
	}
	if st := h.plan(id); st.State != store.PlanUnknown || st.Parts[0].State != store.PartUnknown {
		t.Fatalf("没认领，段保持结果未知：%+v", st)
	}
}

// 回声落在重试等待期：认领成确认，重试作废（钟拨过重试间隔也没有第二次 Send），后面的段接着发
func TestEchoDuringRetryWaitCancelsTheRetry(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.N == 1 {
			return fake.Failed(c.Part.ID, "协议端说没发出去", c.At), nil
		}
		return fake.Confirmed(c.Part.ID, fmt.Sprint(6000+c.N), c.At), nil
	})
	id := h.say("第一条", "第二条")
	h.waitPart(id, 0, store.PartRetrying)
	h.clk.WaitTimer(t, time.Second) // 重试间隔
	if !h.claim(echoOf(acctA, group("g1"), "7001", "第一条")) {
		t.Fatal("明确失败、等待重试的段也能被认领")
	}
	h.waitPart(id, 0, store.PartConfirmed)
	h.clk.WaitTimer(t, 120*time.Millisecond) // 重试间隔的计时器取消了，剩第二段的停顿
	h.clk.Advance(time.Second)
	h.waitPlan(id, store.PlanDone)
	if got := sentTexts(h); fmt.Sprint(got) != fmt.Sprint([]string{"第一条", "第二条"}) {
		t.Fatalf("重试作废，第一段只发过一次：%v", got)
	}
	if pt := h.plan(id).Parts[0]; pt.Attempts != 1 || pt.Via != types.ViaEcho {
		t.Fatalf("%+v", pt)
	}
}

// 已中止、已确认、段失败的段收到同样文字的回声：不认领，状态不变（只记观察——这条回声由入库一站照常处理）
func TestEchoDoesNotTouchFinishedParts(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if types.BodyTextOf(c.Part.Body) == "甲" {
			return fake.Failed(c.Part.ID, "拒绝", c.At), nil
		}
		return fake.Confirmed(c.Part.ID, "5001", c.At), nil
	})
	id := h.say("甲", "乙")
	for h.plan(id).State == store.PlanSending {
		if h.clk.Pending() > 0 {
			h.clk.Advance(time.Second)
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartFailed, store.PartAborted}) {
		t.Fatalf("准备的状态：%v", got)
	}
	for _, text := range []string{"甲", "乙"} {
		if h.claim(echoOf(acctA, group("g1"), "7001", text)) {
			t.Errorf("%q：段失败、已中止的不认领", text)
		}
	}
	if got := h.partStates(id); fmt.Sprint(got) != fmt.Sprint([]string{store.PartFailed, store.PartAborted}) {
		t.Fatalf("状态不该变：%v", got)
	}

	// 已确认的：先发一条确认的，同文字的回声不认领（它的回执本来就带着号）
	ok := h.say("丙")
	h.waitPlan(ok, store.PlanDone)
	if h.claim(echoOf(acctA, group("g1"), "5002", "丙")) {
		t.Fatal("已确认的段不认领")
	}
}

// 键里有账号：同一个群里两个号各发一段同样的字、都是结果未知，回声来自 B 号的就只认 B 号那一段，不串给更早的 A 号那段
func TestEchoKeyIncludesAccount(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	conv := group("g1")
	a, err := h.out.Send(context.Background(), "ca", "character", conv, []types.Part{txt("哈哈")})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(a, store.PlanUnknown)
	h.clk.Advance(time.Second)
	b, err := h.out.Send(context.Background(), "cb", "character", conv, []types.Part{txt("哈哈")})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(b, store.PlanUnknown)

	if !h.claim(echoOf(acctB, conv, "7002", "哈哈")) {
		t.Fatal("B 号的回声该认领")
	}
	if pa, pb := h.plan(a), h.plan(b); pa.State != store.PlanUnknown || pb.State != store.PlanDone {
		t.Fatalf("B 号的回声只该认 B 号那段：A %s，B %s", pa.State, pb.State)
	}
	if !h.claim(echoOf(acctA, conv, "7001", "哈哈")) {
		t.Fatal("A 号的回声该认领")
	}
	h.waitPlan(a, store.PlanDone)
	var ma, mb int64
	h.st.Events.QueryRow(`SELECT event_seq FROM channel_msgs WHERE platform_id = '7001'`).Scan(&ma)
	h.st.Events.QueryRow(`SELECT event_seq FROM channel_msgs WHERE platform_id = '7002'`).Scan(&mb)
	if ma != h.plan(a).EventSeq || mb != h.plan(b).EventSeq || ma == mb {
		t.Fatalf("各映射到各自的事件：%d %d（A 的事件 %d，B 的 %d）", ma, mb, h.plan(a).EventSeq, h.plan(b).EventSeq)
	}
}

// 同一个号两段同样的字：先到先认——回声按发得先后依次认，第三个回声没有可认的
func TestEchoSameAccountSameTextFirstComeFirstServed(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	first := h.say("嗯")
	h.waitPlan(first, store.PlanUnknown)
	h.clk.Advance(time.Second)
	second := h.say("嗯")
	h.waitPlan(second, store.PlanUnknown)

	if !h.claim(echoOf(acctA, group("g1"), "7001", "嗯")) {
		t.Fatal()
	}
	if h.plan(first).State != store.PlanDone || h.plan(second).State != store.PlanUnknown {
		t.Fatalf("先发的先认：第一条 %s，第二条 %s", h.plan(first).State, h.plan(second).State)
	}
	if !h.claim(echoOf(acctA, group("g1"), "7002", "嗯")) || h.plan(second).State != store.PlanDone {
		t.Fatal("第二个回声认第二条")
	}
	if h.claim(echoOf(acctA, group("g1"), "7003", "嗯")) {
		t.Fatal("两段都认过了，第三个回声是主人手发的或别的程序发的，不认（残余风险：主人手发同样的字且恰在窗口里，只靠文字区分不开）")
	}
}

// 文字比的时候去掉首尾空白、统一换行（协议端可能裁掉末尾换行）；别的会话、图片之类的回声不认
func TestEchoTextMatchingDetails(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	id := h.say("第一行\n第二行\n")
	h.waitPlan(id, store.PlanUnknown)

	if h.claim(echoOf(acctA, group("别的群"), "7001", "第一行\n第二行")) {
		t.Fatal("会话对不上不认")
	}
	img := echoOf(acctA, group("g1"), "7002", "")
	img.Segs = []types.Seg{{Kind: types.SegImage, Hash: "abc"}}
	if h.claim(img) {
		t.Fatal("图片的回声（文字是 [图片]）不认文字段")
	}
	if h.claim(echoOf(acctA, group("g1"), "0", "第一行\n第二行")) {
		t.Fatal("没有平台消息号，映射写不了，不认")
	}
	if !h.claim(echoOf(acctA, group("g1"), "7003", "第一行\r\n第二行")) {
		t.Fatal("换行写法不同、末尾空白被裁掉，仍该认")
	}
}

// ───────────────────────── 和入库一站接起来 ─────────────────────────

// 回声在入库一站存它之前被认领：她的号上不会多出一条 origin = human 的重复事件；
// 对任何账号交来的角色号消息都问：B 号的回复被 A 号先看到，A 号交来的那份也认领，之后 B 号自己的回声是重复，不再存。
// 另一个角色（A 号绑的 ca）因此也"看到"了这条，被通知一次
func TestEchoClaimedBeforeIntakeStoresIt(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.withIntake()
	h.unknownReceipts() // 回包没有号，只能靠回声拿到号
	conv := group("g1")
	ctx := context.Background()

	// 角色 cb（B 号）说话；A 号先看到了它，From 是 B 号，Account 是 A 号
	b, err := h.out.Send(ctx, "cb", "character", conv, []types.Part{txt("我是乙")})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(b, store.PlanUnknown)
	eventsBefore := countRows(t, h.st, `SELECT count(*) FROM events`)

	seenByA := types.Inbound{Kind: types.InboundMessage, Account: acctA, Conv: conv, PlatformID: "7001", From: types.User{ID: acctB}, Time: baseTime,
		Segs: []types.Seg{{Kind: types.SegText, Text: "我是乙"}}}
	if err := h.ad.PushInbound(ctx, seenByA); err != nil {
		t.Fatal(err)
	}
	if err := h.in.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitPlan(b, store.PlanDone)
	if pt := h.plan(b).Parts[0]; pt.Via != types.ViaEcho || pt.PlatformID != "7001" {
		t.Fatalf("A 号交来的 B 号的回复该认领：%+v", pt)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != eventsBefore {
		t.Fatalf("认领了就不该多存一条事件：%d → %d", eventsBefore, n)
	}
	evSeq := h.plan(b).EventSeq
	if n := countRows(t, h.st, `SELECT count(*) FROM observations WHERE event_seq = ? AND character_id = 'ca'`, evSeq); n != 1 {
		t.Fatalf("A 号看到了，ca 该有一条看到了：%d", n)
	}

	// B 号自己的回声晚到：是重复，不新增事件，cb 早就看到过
	own := echoOf(acctB, conv, "7001", "我是乙")
	if err := h.ad.PushInbound(ctx, own); err != nil {
		t.Fatal(err)
	}
	h.in.Idle(ctx)
	if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != eventsBefore {
		t.Fatalf("重复的回声不新增事件：%d", n)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human'`); n != 0 {
		t.Fatalf("她的回声被存成了 origin = human：%d 条", n)
	}

	// 邻居不变：她的号上别人手发的话（没有对应的段）照旧存成 origin = human；群友说的同样文字不被当成回声
	manual := echoOf(acctA, conv, "7100", "手发的")
	manual.Kind = types.InboundMessage
	h.ad.PushInbound(ctx, manual)
	friendSays := msg(acctA, conv, "7101", friend, "我是乙")
	h.ad.PushInbound(ctx, friendSays)
	h.in.Idle(ctx)
	var origin string
	if err := h.st.Events.QueryRow(`SELECT origin FROM events WHERE text = '手发的'`).Scan(&origin); err != nil || origin != OriginHuman {
		t.Fatalf("没对应的段，照旧存成 human：%q %v", origin, err)
	}
	if err := h.st.Events.QueryRow(`SELECT origin FROM events WHERE actor_id = ?`, "qq:"+friend).Scan(&origin); err != nil || origin != OriginPeer {
		t.Fatalf("群友说的同样文字是 peer：%q %v", origin, err)
	}
	if strings.Contains(h.logText(), "我是乙") {
		t.Fatal("日志里不放消息原文")
	}
}

// 不接认领的入库一站（旧写法）把回声存成 origin = human 的重复事件——认领要拦的正是这个
func TestWithoutEchoClaimTheEchoIsStoredAsHuman(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.unknownReceipts()
	id := h.say("嗯嗯")
	h.waitPlan(id, store.PlanUnknown)

	in, err := NewIntake(Config{Stores: store.Single(h.st), Roster: testRoster(), Logf: h.logf, Now: h.clk.Now}) // 没有 Echo
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := in.Inbound(context.Background(), echoOf(acctA, group("g1"), "7001", "嗯嗯")); err != nil {
		t.Fatal(err)
	}
	in.Idle(context.Background())
	if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human' AND text = '嗯嗯'`); n != 1 {
		t.Fatalf("没有认领，回声就是一条 human 事件：%d", n)
	}
	if h.plan(id).State != store.PlanUnknown {
		t.Fatal("没认领，段还是结果未知")
	}
}

// ───────────────────────── 同一个平台消息号只认领一次 ─────────────────────────

// push 把一条消息交给入库一站，等它处理完
func (h *oh) push(in types.Inbound) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.ad.PushInbound(ctx, in); err != nil {
		h.t.Fatal(err)
	}
	if err := h.in.Idle(ctx); err != nil {
		h.t.Fatal(err)
	}
}

// mappedSeq 是（群 g1 里）这个平台消息号映射到的事件，没有映射返回 0
func (h *oh) mappedSeq(platformID string) int64 {
	h.t.Helper()
	var seq int64
	err := h.st.Events.QueryRow(`SELECT event_seq FROM channel_msgs WHERE conversation = ? AND platform_id = ?`, group("g1").String(), platformID).Scan(&seq)
	if err != nil {
		return 0
	}
	return seq
}

// 同一个回声（同一个平台消息号）到了不止一次——同群双号时 A、B 两个号各交一次，重连后协议端又推一遍，补录和实时重叠——
// 只认领一段：后到的是重复，不能拿去认领另一段同文字、结果未知的段。
// 否则那一段被假报成确认，它真正的回声来的时候已经没有段可认，被存成一条 origin = human 的重复事件（清单⑤×⑬）
func TestEchoSamePlatformIDClaimsOnlyOnePart(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.withIntake()
	h.unknownReceipts()
	conv := group("g1")
	say := func() string {
		id, err := h.out.Send(context.Background(), "cb", types.OriginCharacter, conv, []types.Part{txt("哈哈")})
		if err != nil {
			t.Fatal(err)
		}
		h.waitPlan(id, store.PlanUnknown)
		return id
	}
	p1 := say()
	h.clk.Advance(time.Second)
	p2 := say() // 同一个角色连着两次说了同样的字，回包都没有号

	seenByA := types.Inbound{Kind: types.InboundMessage, Account: acctA, Conv: conv, PlatformID: "7001", From: types.User{ID: acctB}, Time: baseTime,
		Segs: []types.Seg{{Kind: types.SegText, Text: "哈哈"}}}
	h.push(seenByA)                           // A 号先看到 B 号发的 7001
	h.push(echoOf(acctB, conv, "7001", "哈哈")) // B 号自己的回声随后也到
	h.push(seenByA)                           // 重连后协议端又推了一遍
	h.waitPlan(p1, store.PlanDone)
	if pt := h.plan(p1).Parts[0]; pt.Via != types.ViaEcho || pt.PlatformID != "7001" {
		t.Fatalf("7001 该认领第一条：%+v", pt)
	}
	if st := h.plan(p2); st.State != store.PlanUnknown || st.Parts[0].PlatformID != "" {
		t.Fatalf("7001 重复到达，不该认领第二条：%s %+v", st.State, st.Parts[0])
	}

	h.push(echoOf(acctB, conv, "7002", "哈哈")) // 第二条真正的回声
	h.waitPlan(p2, store.PlanDone)
	if pt := h.plan(p2).Parts[0]; pt.Via != types.ViaEcho || pt.PlatformID != "7002" {
		t.Fatalf("7002 该认领第二条：%+v", pt)
	}
	if a, b := h.mappedSeq("7001"), h.mappedSeq("7002"); a != h.plan(p1).EventSeq || b != h.plan(p2).EventSeq || a == b {
		t.Fatalf("两个号各映射到各自的事件：%d %d（第一条 %d，第二条 %d）", a, b, h.plan(p1).EventSeq, h.plan(p2).EventSeq)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human'`); n != 0 {
		t.Fatalf("她的回声被存成了 origin = human：%d 条", n)
	}
}

// 回包已经确认、映射早写了的那一条，它的回声来时不能去认领更早一段同文字、结果未知的段：
// 那个号是别人的，认了就把没确认的段假报成确认，号也记错了
func TestEchoOfConfirmedReplyDoesNotClaimOlderUnknownPart(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.withIntake()
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.N == 1 {
			return fake.Unknown(c.Part.ID, "超时", c.At), nil
		}
		return fake.Confirmed(c.Part.ID, "7002", c.At), nil
	})
	conv := group("g1")
	p1 := h.say("好")
	h.waitPlan(p1, store.PlanUnknown)
	h.clk.Advance(time.Minute)
	p2 := h.say("好")
	h.waitPlan(p2, store.PlanDone)
	eventsBefore := countRows(t, h.st, `SELECT count(*) FROM events`)

	h.push(echoOf(acctA, conv, "7002", "好")) // 第二条的回声
	if st := h.plan(p1); st.State != store.PlanUnknown || st.Parts[0].PlatformID != "" {
		t.Fatalf("7002 是第二条的回声，不该把第一条认成确认：%s %+v", st.State, st.Parts[0])
	}
	if pt := h.plan(p2).Parts[0]; pt.Via != types.ViaReply || pt.PlatformID != "7002" || h.mappedSeq("7002") != h.plan(p2).EventSeq {
		t.Fatalf("第二条的号和映射不动：%+v 映射 %d", pt, h.mappedSeq("7002"))
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != eventsBefore {
		t.Fatalf("已知的号只补看到了，不新增事件：%d → %d", eventsBefore, n)
	}

	h.push(echoOf(acctA, conv, "7003", "好")) // 第一条自己的回声来了，还认得出来
	h.waitPlan(p1, store.PlanDone)
	if pt := h.plan(p1).Parts[0]; pt.Via != types.ViaEcho || pt.PlatformID != "7003" {
		t.Fatalf("第一条该被自己的回声认领：%+v", pt)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human'`); n != 0 {
		t.Fatalf("她的回声被存成了 origin = human：%d 条", n)
	}
}

// 邻居：平台消息号只在一个会话里唯一。别的群里恰好有同一个号（已经映射），这个群里同号的回声照常认领
func TestEchoPlatformIDMappedInAnotherConversationDoesNotBlock(t *testing.T) {
	t.Parallel()
	h := newOH(t, types.Caps{})
	h.run(nil)
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		if c.Part.Conv == group("g2") {
			return fake.Confirmed(c.Part.ID, "7001", c.At), nil
		}
		return fake.Unknown(c.Part.ID, "超时", c.At), nil
	})
	other := h.sayIn(group("g2"), "好")
	h.waitPlan(other, store.PlanDone)
	mine := h.say("好")
	h.waitPlan(mine, store.PlanUnknown)

	if !h.claim(echoOf(acctA, group("g1"), "7001", "好")) {
		t.Fatal("别的群里有同一个号不影响这个群认领")
	}
	h.waitPlan(mine, store.PlanDone)
	if n := countRows(t, h.st, `SELECT count(*) FROM channel_msgs WHERE platform_id = '7001'`); n != 2 {
		t.Fatalf("两个群各有一条映射：%d", n)
	}
}
