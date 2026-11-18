package channel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/history"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 回合定了以后的裁决测试（规格-渠道中间件 第十节 10.1，第十六节 B"回合的裁决""替换和 catchUp"）：
// 真的出口、临时库、假适配器、假内核（Commit 照真内核的规矩查库）、假守卫。号、名字、话都是编的。

// ro 是一套：出口那一套（oh）加假内核和回合
type ro struct {
	*oh
	k     *fakeKernel
	r     *Responder
	log   *callLog
	guard *fakeGuard

	mu     sync.Mutex
	routes [][2]string

	mode  atomic.Value // string：出口现在的档，默认说话
	check atomic.Value // checkBox：发送检查返回的错
}

type checkBox struct{ err error }

func (x *ro) setMode(m string)   { x.mode.Store(m) }
func (x *ro) setCheck(err error) { x.check.Store(checkBox{err}) }

func newRO(t *testing.T, tweak ...func(*ResponderConfig)) *ro {
	t.Helper()
	return newROWith(t, nil, tweak...)
}

// newROWith 同 newRO，只是造出口时多一道 outlet 的调整（比如多接一个渠道的适配器）
func newROWith(t *testing.T, outlet func(*OutletConfig), tweak ...func(*ResponderConfig)) *ro {
	t.Helper()
	log := &callLog{}
	x := &ro{}
	h := newOH(t, types.Caps{}, func(c *OutletConfig) {
		if outlet != nil {
			outlet(c)
		}
		c.Clock = SystemClock{}
		c.Pace = Pace{PerChar: time.Nanosecond, Max: time.Millisecond, Retry: time.Millisecond}
		c.Mode = func(string, string, types.Conversation) string { return x.mode.Load().(string) }
		c.Check = func(string, string, types.Conversation, []types.Part) error { return x.check.Load().(checkBox).err }
	})
	h.run(nil)
	x.oh, x.log, x.guard = h, log, &fakeGuard{route: GuardRouteNormal}
	x.setMode(ModeSpeak)
	x.setCheck(nil)
	x.k = newFakeKernel(t, log, h.st)
	h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
		log.add("send:%s", types.BodyTextOf(c.Part.Body))
		return fake.Confirmed(c.Part.ID, fmt.Sprintf("%d", 5000+c.N), c.At), nil
	})
	cfg := ResponderConfig{Kernel: x.k, Writer: h.out, Stores: store.Single(h.st), Roster: testRoster(), Logf: h.logf, Guard: x.guard,
		Line: LineEnv{Location: tz8}, OnGuardRoute: func(prev, cur string) { x.mu.Lock(); x.routes = append(x.routes, [2]string{prev, cur}); x.mu.Unlock() }}
	for _, f := range tweak {
		f(&cfg)
	}
	var err error
	if x.r, err = NewResponder(cfg); err != nil {
		t.Fatal(err)
	}
	x.k.r = x.r
	t.Cleanup(x.r.Close)
	return x
}

func (x *ro) routeChanges() [][2]string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([][2]string(nil), x.routes...)
}

// say 往库里放一条别人说的话（扮演入库一站），返回它的批里那一条
func (x *ro) say(conv types.Conversation, who, text string, called bool) types.Msg {
	x.t.Helper()
	kind, origin := ActorHuman, OriginPeer
	if who == whoOwner {
		kind, origin = ActorOwner, OriginOwner
	}
	seq, err := x.st.AppendEvent(store.NewEvent{Channel: conv.Channel, Conversation: conv.String(), ActorKind: kind, ActorID: who, Origin: origin,
		Kind: "message", Text: text, CreatedAt: baseTime.UnixMilli()}, []string{"ca"})
	if err != nil {
		x.t.Fatal(err)
	}
	return mk(seq, who, text, called)
}

// start 放一批、等内核收到
func (x *ro) start(conv types.Conversation, ms ...types.Msg) {
	x.t.Helper()
	n := x.log.count("respond:")
	x.r.Release(batchFor(conv, ms...))
	eventually(x.t, "Respond", func() bool { return x.log.count("respond:") == n+1 }, func() string { return fmt.Sprint(x.log.all()) })
}

// settleAndWait 让回合定了，等处理完（裁决、写库、Commit、该发的发完）
func (x *ro) settleAndWait(conv types.Conversation, outcome string, said ...turn.Said) {
	x.t.Helper()
	x.k.finish("ca", conv.String(), outcome, said...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := x.r.Idle(ctx); err != nil {
		x.t.Fatalf("等了 10 秒裁决还没做完：%v\n调用：%v\n日志：%s", err, x.log.all(), x.logText())
	}
}

// hers 是库里她在这个会话说的（出口来的：发的和试运行的）：(seq, origin, text)
type herRow struct {
	Seq          int64
	Origin, Text string
}

func (x *ro) hers(conv types.Conversation) []herRow {
	x.t.Helper()
	rows, err := x.st.Events.Query(`SELECT seq, COALESCE(origin, ''), text FROM events WHERE conversation = ? AND actor_kind = 'character' ORDER BY seq`, conv.String())
	if err != nil {
		x.t.Fatal(err)
	}
	defer rows.Close()
	var out []herRow
	for rows.Next() {
		var r herRow
		if err := rows.Scan(&r.Seq, &r.Origin, &r.Text); err != nil {
			x.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type planRow struct{ ID, Kind, State, Reason string }

func (x *ro) plans(conv types.Conversation) []planRow {
	x.t.Helper()
	rows, err := x.st.Events.Query(`SELECT id, kind, state, COALESCE(reason, '') FROM outbound_plans WHERE conversation = ? ORDER BY created_at, rowid`, conv.String())
	if err != nil {
		x.t.Fatal(err)
	}
	defer rows.Close()
	var out []planRow
	for rows.Next() {
		var p planRow
		if err := rows.Scan(&p.ID, &p.Kind, &p.State, &p.Reason); err != nil {
			x.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// nextContext 是下一轮重建实例时给她看的历史（去掉这一轮单独追加的）里，她说的（assistant）那几条的字
func (x *ro) nextContext(conv types.Conversation) []string {
	x.t.Helper()
	_, msgs, _, err := history.For(x.st.Events, "ca", conv.String(), 100, nil)
	if err != nil {
		x.t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		if m.Role == "assistant" {
			out = append(out, m.Text)
		}
	}
	return out
}

func (x *ro) waitSent(n int) {
	x.t.Helper()
	eventually(x.t, fmt.Sprintf("适配器收到 %d 段", n), func() bool { return len(x.ad.Sent()) >= n }, func() string { return fmt.Sprint(x.log.all()) + "\n" + x.logText() })
}

// splitting 是一句改写以后真的会被拆成几条的话（拆条的随机数由这句话定，同一句每次一样）
func splitting(t *testing.T) string {
	t.Helper()
	for _, c := range []string{
		"我刚才看到一个很有意思的东西，是一只猫坐在键盘上面打字，打出来的居然是一串有意义的单词，太神奇了，你说是不是巧合呢",
		"今天的天气真的很不错，我们要不要一起出去走走，顺便买点吃的回来，路上还可以拍几张照片发给他们看看",
		"我今天去了超市，买了苹果、香蕉、橙子和葡萄，还有一些零食，回来的路上下雨了，淋得全身都湿了，真倒霉",
	} {
		if len(splitMessage(asSentQQ(dropTrailingPeriods(c)))) >= 2 {
			return c
		}
	}
	t.Fatal("三句备选都没有被拆开，测试没法验拆条")
	return ""
}

// ---------- 正常的一回合 ----------

// 她说了两句（第 0 步、第 2 步）：改写、拆条、写库、交回内核、发出去。
// 库里的话、交回的话、发出去的话、下一轮的历史是同一个字，各出现一次；Commit 在第一次发送之前；seq 按步号的顺序
func TestSettleDoneWritesCommitsThenSends(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.k.commitDone = func(string, string, string, []turn.Committed) { time.Sleep(40 * time.Millisecond) } // Commit 慢一点，发得太早就露馅
	long := splitting(t)
	m := x.say(convGroup, whoFriend, "在吗", true)
	x.start(convGroup, m)
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 2, Text: long + "。"}, turn.Said{Step: 0, Text: "在的。"})

	wantLong := asSentQQ(dropTrailingPeriods(long + "。"))
	wantTexts := []string{"在的", strings.Join(splitMessage(wantLong), "\n\n")}
	hers := x.hers(convGroup)
	if len(hers) != 2 || hers[0].Text != wantTexts[0] || hers[1].Text != wantTexts[1] || hers[0].Origin != "character" || hers[1].Origin != "character" {
		t.Fatalf("库里她的话：%+v，应为 %q", hers, wantTexts)
	}
	if hers[0].Seq >= hers[1].Seq || hers[0].Seq <= m.Seq {
		t.Fatalf("seq 该按步号排，并且比这一批晚：%d、%d、批 %d", hers[0].Seq, hers[1].Seq, m.Seq)
	}
	cs := x.k.commitList()
	if len(cs) != 1 || cs[0].TurnID != "t1" || len(cs[0].Lines) != 2 {
		t.Fatalf("该交回一次、两句：%+v", cs)
	}
	for i, l := range cs[0].Lines {
		if l.Seq != hers[i].Seq || l.Text != hers[i].Text || l.Step != []int{0, 2}[i] {
			t.Fatalf("交回的第 %d 句和库里不一样：%+v ↔ %+v", i, l, hers[i])
		}
	}
	// 发出去的：先 Commit 再发；每一段都是拆出来的那一段
	parts := splitMessage(wantLong)
	x.waitSent(1 + len(parts))
	var sent []string
	for _, s := range x.ad.Sent() {
		sent = append(sent, types.BodyTextOf(s.Part.Body))
	}
	if want := append([]string{"在的"}, parts...); !slices.Equal(sent, want) {
		t.Fatalf("发出去的：%q，应为 %q", sent, want)
	}
	if c, s := x.log.index("commit-ok:"), x.log.index("send:"); c < 0 || s < c {
		t.Fatalf("该先 Commit 再发：%v", x.log.all())
	}
	// 下一轮的历史里：她这回合说的各出现一次，是改写以后的字，不是原文
	if got := x.nextContext(convGroup); !slices.Equal(got, wantTexts) {
		t.Fatalf("下一轮历史里她说的：%q，应为 %q", got, wantTexts)
	}
	// 计划：两条都是发，段数对得上
	ps := x.plans(convGroup)
	if len(ps) != 2 || ps[0].Kind != "send" || ps[1].Kind != "send" {
		t.Fatalf("计划：%+v", ps)
	}
	if p := x.plan(ps[1].ID); len(p.Parts) != len(parts) {
		t.Fatalf("第二句的计划该有 %d 段：%+v", len(parts), p.Parts)
	}
}

// 守卫拦下：库里没有这句的事件行，计划 kind 为拦下、里面没有原文，下一轮历史不含，不发；别的句照常；交回的只有放行的
func TestSettleGuardBlocks(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.guard.set(func(g *fakeGuard) { g.blockOn = []string{"考试在周五"} })
	m := x.say(convGroup, whoFriend, "他考试是哪天", true)
	x.start(convGroup, m)
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "他的考试在周五。"}, turn.Said{Step: 1, Text: "这个我不太清楚"})

	hers := x.hers(convGroup)
	if len(hers) != 1 || hers[0].Text != "这个我不太清楚" {
		t.Fatalf("只有放行的那句该进库：%+v", hers)
	}
	ps := x.plans(convGroup)
	if len(ps) != 2 || ps[0].Kind != "blocked" || ps[0].State != "blocked" || ps[1].Kind != "send" {
		t.Fatalf("计划：%+v", ps)
	}
	if strings.Contains(ps[0].Reason, "周五") || !strings.Contains(ps[0].Reason, "m1") {
		t.Fatalf("拦下的计划只记命中的记忆编号，不存原文：%q", ps[0].Reason)
	}
	if p := x.plan(ps[0].ID); len(p.Parts) != 0 || p.EventSeq != 0 {
		t.Fatalf("拦下的计划不该有段和事件：%+v", p)
	}
	cs := x.k.commitList()
	if len(cs) != 1 || len(cs[0].Lines) != 1 || cs[0].Lines[0].Step != 1 {
		t.Fatalf("交回的只该有放行的那句（第 1 步）：%+v", cs)
	}
	x.waitSent(1)
	for _, s := range x.ad.Sent() {
		if strings.Contains(types.BodyTextOf(s.Part.Body), "周五") {
			t.Fatalf("拦下的话发出去了：%q", types.BodyTextOf(s.Part.Body))
		}
	}
	if got := x.nextContext(convGroup); !slices.Equal(got, []string{"这个我不太清楚"}) {
		t.Fatalf("下一轮历史：%q", got)
	}
	if strings.Contains(x.logText(), "周五") {
		t.Fatalf("日志里不该有原文：%s", x.logText())
	}
}

// 守卫看到的是改写以后的字，并且把这一回合前面放行的句子接在前面（一条私事拆成两句，单看第二句不拦）
func TestGuardSeesRewrittenAndAccumulated(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.guard.set(func(g *fakeGuard) { g.blockOn = []string{"周五\n考数学"} }) // 两句接起来（换行隔开）才命中
	m := x.say(convGroup, whoFriend, "他什么时候考试", true)
	x.start(convGroup, m)
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "他周五。"}, turn.Said{Step: 1, Text: "考数学。"})
	seen := x.guard.texts()
	if len(seen) != 2 || seen[0] != "他周五" || seen[1] != "他周五\n考数学" {
		t.Fatalf("守卫看到的：%q（该是改写以后的字，第二句带着前面放行的）", seen)
	}
	// 第一句放行了、第二句合起来命中被拦下
	if hers := x.hers(convGroup); len(hers) != 1 || hers[0].Text != "他周五" {
		t.Fatalf("库里：%+v", hers)
	}
	if ps := x.plans(convGroup); len(ps) != 2 || ps[1].Kind != "blocked" {
		t.Fatalf("计划：%+v", ps)
	}
}

// 守卫出错、超时、panic：拦下，reason 记「守卫没判成」，不放行；日志不含原文
func TestGuardFailureBlocksWithReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*fakeGuard)
		cfg  func(*ResponderConfig)
	}{
		{"出错", func(g *fakeGuard) { g.err = errBoom }, nil},
		{"panic", func(g *fakeGuard) { g.panics = true }, nil},
		{"超时", func(g *fakeGuard) { g.slow = 5 * time.Second }, func(c *ResponderConfig) { c.GuardTimeout = 30 * time.Millisecond }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var tweak []func(*ResponderConfig)
			if tc.cfg != nil {
				tweak = append(tweak, tc.cfg)
			}
			x := newRO(t, tweak...)
			x.guard.set(tc.set)
			x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
			x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "绝密内容"})
			if hers := x.hers(convGroup); len(hers) != 0 {
				t.Fatalf("守卫没判成不该放行：%+v", hers)
			}
			ps := x.plans(convGroup)
			if len(ps) != 1 || ps[0].Kind != "blocked" || ps[0].Reason != "守卫没判成" {
				t.Fatalf("计划：%+v", ps)
			}
			if strings.Contains(x.logText(), "绝密") || strings.Contains(x.logText(), "炸了") {
				t.Fatalf("日志里不该有原文和守卫的报错：%s", x.logText())
			}
			if len(x.ad.Sent()) != 0 {
				t.Fatal("不该发")
			}
		})
	}
}

// ADR-186 第 4 条的两种退路：嵌入挂了只比原文（没比中的照常发、比中的仍拦），小模型挂了按相似度判（超过门槛的拦）。
// 退路由守卫自己实现，中间件认它报的路：每句日志记走的是哪条路；路变了才叫 OnGuardRoute（切过去一次、恢复一次，连发多句不重复）
func TestGuardFallbackRoutes(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.guard.set(func(g *fakeGuard) { g.blockOn = []string{"原文命中"}; g.route = GuardRouteEmbedDown })
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone,
		turn.Said{Step: 0, Text: "换了说法的私事"}, turn.Said{Step: 1, Text: "再来一句"}, turn.Said{Step: 2, Text: "这句原文命中"})
	// 嵌入挂了：前两句照常发（比不出相似度），原文命中的仍拦下
	if hers := x.hers(convGroup); len(hers) != 2 || hers[0].Text != "换了说法的私事" || hers[1].Text != "再来一句" {
		t.Fatalf("嵌入挂了时只比原文：%+v", hers)
	}
	if ps := x.plans(convGroup); len(ps) != 3 || ps[2].Kind != "blocked" {
		t.Fatalf("原文命中的该拦下：%+v", ps)
	}
	if got := x.routeChanges(); !slices.Equal(got, [][2]string{{GuardRouteNormal, GuardRouteEmbedDown}}) {
		t.Fatalf("连发三句只该在切到退路时通知一次：%v", got)
	}
	if n := strings.Count(x.logText(), "守卫走的是 "+GuardRouteEmbedDown); n != 3 {
		t.Fatalf("每句日志都该记走的路，记了 %d 句：%s", n, x.logText())
	}

	// 恢复正常：再通知一次；小模型挂了：又是一次切换
	x.guard.set(func(g *fakeGuard) { g.route = GuardRouteNormal })
	x.start(convGroup, x.say(convGroup, whoFriend, "第二轮", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "好的"}, turn.Said{Step: 1, Text: "知道了"})
	x.guard.set(func(g *fakeGuard) { g.route = GuardRouteModelDown; g.blockOn = []string{"相似度超过门槛"} })
	x.start(convGroup, x.say(convGroup, whoFriend, "第三轮", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "相似度超过门槛的一句"}, turn.Said{Step: 1, Text: "无关的一句"})
	want := [][2]string{{GuardRouteNormal, GuardRouteEmbedDown}, {GuardRouteEmbedDown, GuardRouteNormal}, {GuardRouteNormal, GuardRouteModelDown}}
	if got := x.routeChanges(); !slices.Equal(got, want) {
		t.Fatalf("路的变化：%v，应为 %v", got, want)
	}
	ps := x.plans(convGroup)
	if last := ps[len(ps)-2:]; last[0].Kind != "blocked" || last[1].Kind != "send" {
		t.Fatalf("小模型挂了按相似度判：超过门槛的拦、其余发：%+v", last)
	}
}

// 字数保险丝：改写以后超过 200 字拦下，按整句、在拆条之前算
func TestSettleFuse(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.start(convGroup, x.say(convGroup, whoFriend, "讲个长故事", true))
	long := strings.Repeat("字", 201)
	ok := strings.Repeat("字", 200)
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: long}, turn.Said{Step: 1, Text: ok})
	ps := x.plans(convGroup)
	if len(ps) != 2 || ps[0].Kind != "blocked" || !strings.Contains(ps[0].Reason, "保险丝") || ps[1].Kind != "send" {
		t.Fatalf("201 字拦下、200 字放行：%+v", ps)
	}
	if hers := x.hers(convGroup); len(hers) != 1 || hers[0].Text != ok {
		t.Fatalf("库里只该有 200 字那句")
	}
}

// 试运行档：写库（origin = automation:dry-run）、计划 kind 为试运行、不发；交回的是空的（试运行的不算她说出口的）；下一轮历史不含
func TestSettleDryRun(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.setMode(ModeDryRun)
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "在的。"})
	hers := x.hers(convGroup)
	if len(hers) != 1 || hers[0].Origin != "automation:dry-run" || hers[0].Text != "在的" {
		t.Fatalf("试运行该写一行 dry-run：%+v", hers)
	}
	if ps := x.plans(convGroup); len(ps) != 1 || ps[0].Kind != "dry-run" {
		t.Fatalf("计划：%+v", ps)
	}
	if cs := x.k.commitList(); len(cs) != 1 || len(cs[0].Lines) != 0 {
		t.Fatalf("试运行的不交回（交一次空的，让实例照库重写）：%+v", cs)
	}
	if got := x.nextContext(convGroup); len(got) != 0 {
		t.Fatalf("下一轮历史不含试运行的话：%q", got)
	}
	if len(x.ad.Sent()) != 0 {
		t.Fatal("试运行不发")
	}
}

// 只记录档（回合跑的时候切过去了）：不写库、不发，记一条拦下；发送检查没过（通道门、风控）同样
func TestSettleRecordModeAndSendCheck(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.setMode(ModeRecord)
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	if ps := x.plans(convGroup); len(ps) != 1 || ps[0].Kind != "blocked" || !strings.Contains(ps[0].Reason, "只记录") {
		t.Fatalf("计划：%+v", ps)
	}
	x.setMode(ModeSpeak)
	x.setCheck(fmt.Errorf("通道暂停中"))
	x.start(convGroup, x.say(convGroup, whoFriend, "再问", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	ps := x.plans(convGroup)
	if len(ps) != 2 || ps[1].Kind != "blocked" || !strings.Contains(ps[1].Reason, "发送检查没过") {
		t.Fatalf("计划：%+v", ps)
	}
	if hers := x.hers(convGroup); len(hers) != 0 || len(x.ad.Sent()) != 0 {
		t.Fatalf("两次都不该写库、不该发：%+v", hers)
	}
}

// 回合被打断（stopped）：缓冲的句子丢掉，不写库、不发、不交回；只记一条"已打断"的计划，里面没有原文；下一轮历史不含
func TestSettleStopped(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeStopped, turn.Said{Step: 0, Text: "说到一半的话"})
	if hers := x.hers(convGroup); len(hers) != 0 {
		t.Fatalf("不写库：%+v", hers)
	}
	ps := x.plans(convGroup)
	if len(ps) != 1 || ps[0].Kind != "interrupted" || strings.Contains(ps[0].Reason, "说到一半") {
		t.Fatalf("计划：%+v", ps)
	}
	if len(x.k.commitList()) != 0 || len(x.ad.Sent()) != 0 || len(x.nextContext(convGroup)) != 0 {
		t.Fatal("不交回、不发、历史里也没有")
	}
}

// 结局 error：同样不写库，计划是"出错"，带原因
func TestSettleError(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.k.finishErr("ca", convGroup.String(), turn.OutcomeError, fmt.Errorf("上下文超限"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := x.r.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	ps := x.plans(convGroup)
	if len(ps) != 1 || ps[0].Kind != "error" || !strings.Contains(ps[0].Reason, "上下文超限") || len(x.hers(convGroup)) != 0 {
		t.Fatalf("计划：%+v", ps)
	}
}

// Commit 报错：记一行日志，让下一批从库里重建实例（RestartIn）；已经写进库的话照发
func TestCommitErrorRestartsInstanceAndStillSends(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.k.commitErr = fmt.Errorf("交回失败")
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "在的"})
	if x.log.count("restart") != 1 {
		t.Fatalf("Commit 报错该 RestartIn：%v", x.log.all())
	}
	x.waitSent(1)
	if !strings.Contains(x.logText(), "交回写库的句子（Commit）报错") {
		t.Fatalf("该记一行日志：%s", x.logText())
	}
	if hers := x.hers(convGroup); len(hers) != 1 {
		t.Fatalf("话已经在库里了：%+v", hers)
	}
}

// 回合没说话（Said 为空）：不写库、不调 Commit（真内核那一头没有等着的）
func TestSettleDoneWithNothingSaid(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone)
	if len(x.k.commitList()) != 0 || len(x.plans(convGroup)) != 0 {
		t.Fatalf("没说话就什么都不记：%v", x.log.all())
	}
}

// 改写出错用原文（三处仍然一致）；拆条出错整句当一段
func TestRewriteAndSplitFailuresFallBack(t *testing.T) {
	t.Parallel()
	x := newRO(t, func(c *ResponderConfig) {
		c.Shapers = map[string]Shaper{"qq": {
			Rewrite: func(s string) string { panic("改写炸了") },
			Split:   func(s string) []string { panic("拆条炸了") },
		}}
	})
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "原样的话。"})
	if hers := x.hers(convGroup); len(hers) != 1 || hers[0].Text != "原样的话。" {
		t.Fatalf("改写出错该用原文：%+v", hers)
	}
	x.waitSent(1)
	if got := types.BodyTextOf(x.ad.Sent()[0].Part.Body); got != "原样的话。" {
		t.Fatalf("发出去的：%q", got)
	}
}

// 出站记数：命中的记在写进库的那一条事件上，试运行的分开记；拦下的不记
func TestAuditHitsAreCounted(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []AuditRecord
	x := newRO(t, func(c *ResponderConfig) {
		c.Audit = func(text string) []AuditHit {
			if strings.Contains(text, "作为") {
				return []AuditHit{{Kind: "self-ai", Hit: "作为"}}
			}
			return nil
		}
		c.OnAudit = func(r AuditRecord) { mu.Lock(); got = append(got, r); mu.Unlock() }
	})
	x.start(convGroup, x.say(convGroup, whoFriend, "在吗", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "作为一个助手我在的"}, turn.Said{Step: 1, Text: "正常的话"})
	mu.Lock()
	defer mu.Unlock()
	hers := x.hers(convGroup)
	if len(got) != 1 || got[0].Hit.Kind != "self-ai" || got[0].EventSeq != hers[0].Seq || got[0].Dry || got[0].Character != "ca" {
		t.Fatalf("记数：%+v", got)
	}
}

// ---------- 同一个群里两个角色 ----------

// 同群两个角色（清单⑤）：各自的回合各自的屏障，写库用各自的号，互不等待
func TestTwoCharactersInOneGroupAreIndependent(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	m := x.say(convGroup, whoFriend, "你们俩在吗", true)
	ba, bb := batchFor(convGroup, m), batchFor(convGroup, m)
	bb.Character, bb.Account = "cb", acctB
	x.r.Release(ba)
	x.r.Release(bb)
	eventually(t, "两个角色各一个 Respond", func() bool { return x.log.count("respond:") == 2 })
	ins := x.k.inputs()
	if ins[0].CharacterID == ins[1].CharacterID {
		t.Fatalf("两个回合该是两个角色的：%q %q", ins[0].CharacterID, ins[1].CharacterID)
	}
	x.k.finish("cb", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "乙在"})
	x.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "甲在"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := x.r.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := x.st.Events.Query(`SELECT actor_id, text FROM events WHERE conversation = ? AND actor_kind = 'character' ORDER BY seq`, convGroup.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var who, text string
		rows.Scan(&who, &text)
		got[who] = text
	}
	if got["ca"] != "甲在" || got["cb"] != "乙在" || len(got) != 2 {
		t.Fatalf("各说各的：%v", got)
	}
	x.waitSent(2)
	accounts := map[string]string{}
	for _, s := range x.ad.Sent() {
		accounts[types.BodyTextOf(s.Part.Body)] = s.Account
	}
	if accounts["甲在"] != acctA || accounts["乙在"] != acctB {
		t.Fatalf("各用各的号发：%v", accounts)
	}
	if cs := x.k.commitList(); len(cs) != 2 {
		t.Fatalf("各交回各的：%+v", cs)
	}
}

// ---------- 插件自己发的话：下一批带上 ----------

// 复读等 automation:* 发的话不是回合说的，不调 Commit；下一批 Respond 把它们的 seq 放进 Seqs、字按重建历史的写法拼进 Text
func TestAutomationLinesJoinNextBatch(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	m1 := x.say(convGroup, whoFriend, "在吗", true)
	x.start(convGroup, m1)
	// 回合还跑着，复读插件发了一句（写库）
	held, err := x.out.SendHeld(context.Background(), "ca", "automation:repeater", convGroup, []types.Part{types.Message("哈哈哈")})
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	x.settleAndWait(convGroup, turn.OutcomeDone)
	m2 := x.say(convGroup, whoOther, "第二批", true)
	x.start(convGroup, m2)
	in := x.k.inputs()[1]
	if len(in.Seqs) != 2 || in.Seqs[1] != m2.Seq || in.Seqs[0] <= m1.Seq || in.Seqs[0] >= m2.Seq {
		t.Fatalf("Seqs 该含复读那一条和新的一批，按 seq 排：%v（批 %d、%d）", in.Seqs, m1.Seq, m2.Seq)
	}
	if !strings.HasPrefix(in.Text, "（这条是系统替你自动发的（repeater），不是你说的）哈哈哈\n[") || headerLines(in.Text) != 1 {
		t.Fatalf("Text：%q", in.Text)
	}
	// 试运行的、self-skip 的不算；之前的不重复带
	if lines := x.r.automationLines(batchFor(convGroup, m2), in.Seqs[0]); len(lines) != 0 {
		t.Fatalf("已经带过的不再带：%+v", lines)
	}
}

// 内核的 Clean 再跑一遍结果不变（规格 10.1 第 1 步：标记过滤不重做，只写成幂等）
func TestKernelCleanIsIdempotentOnSamples(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"好的", "好的】【", "在的\n\n", "哈哈​", "  前后有空白  ", "正常的一句话。", "带 emoji 😀 的话", "【系统】"} {
		once := turn.Clean(s)
		if twice := turn.Clean(once); twice != once {
			t.Errorf("Clean 不幂等：%q → %q → %q", s, once, twice)
		}
	}
}
