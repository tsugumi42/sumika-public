package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 闸门的测试：真的入库一站、流水线、链（假插件）、闸门，临时库，假钟。号、群、话都是编的。
// 场合表（规格-渠道中间件 第十六节 B"闸门计数"和群聊闸门第九节）：闸门只管平台的群；本地群聊、QQ 私聊不过闸门，
// 本地私聊不进渠道中间件（console 的主窗口是内核自己的路）。每个场合一行测试在下面标出。
//
//	QQ 群    TestGate*（下面全部）
//	QQ 私聊  TestGateIgnoresPrivate
//	本地群聊  TestGateIgnoresConsole
//	本地私聊  不走这里（没有 Pipeline 的入库）

type gh struct {
	*ph
	g   *Gate
	clk *fake.Clock
	rmu sync.Mutex
	rel []types.Batch
	pid int
}

func newGH(t *testing.T, plugins []types.Interceptor, tweak func(*GateConfig)) *gh {
	t.Helper()
	h := &gh{clk: fake.NewClock(baseTime)}
	h.ph = newPH(t, plugins, func(_ *Config, p *PipelineConfig, _ *ChainConfig) { p.Gate = func(d Dispatch) { h.g.Handle(d) } })
	cfg := GateConfig{Stores: store.Single(h.st), Roster: testRoster(), Jitter: -1, Clock: h.clk, Loc: time.UTC, Logf: h.logf}
	cfg.Release = func(b types.Batch) { h.rmu.Lock(); h.rel = append(h.rel, b); h.rmu.Unlock() }
	if tweak != nil {
		tweak(&cfg)
	}
	g, err := NewGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.g = g
	t.Cleanup(g.Close) // 比 newPH 的收尾晚注册、先执行：闸门先关，库后关
	return h
}

func (h *gh) released() []types.Batch {
	h.rmu.Lock()
	defer h.rmu.Unlock()
	return append([]types.Batch(nil), h.rel...)
}

// waitReleased 等放行的批次到 n 个（合并一串 @ 的计时器在别的 goroutine 里放行）
func (h *gh) waitReleased(n int) []types.Batch {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r := h.released(); len(r) >= n {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("等了 10 秒，放行的批次只有 %d 个，想要 %d 个。日志：\n%s", len(h.released()), n, h.logText())
	return nil
}

// say 在 conv 里说一句：from 是发言人，at 是比 baseTime 晚多久。返回平台消息号
func (h *gh) say(conv types.Conversation, from, text string, at time.Duration, segs ...types.Seg) string {
	h.t.Helper()
	h.pid++
	id := fmt.Sprintf("%d", 9000+h.pid)
	in := msg(acctA, conv, id, from, text)
	in.Time = baseTime.Add(at)
	if len(segs) > 0 {
		in.Segs = append(segs, in.Segs...)
	}
	h.push(in)
	h.idle()
	return id
}

func atHer() types.Seg { return types.Seg{Kind: types.SegAt, Target: acctA} }

func (h *gh) verdicts(conv types.Conversation) []string {
	h.t.Helper()
	rows, err := h.g.Decisions(conv, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].Verdict)
	}
	return out
}

func (h *gh) lastDecision(conv types.Conversation) store.GateDecisionRow {
	h.t.Helper()
	rows, err := h.g.Decisions(conv, 1)
	if err != nil || len(rows) == 0 {
		h.t.Fatalf("没有判断记录：%v", err)
	}
	return rows[0]
}

func (h *gh) state(conv types.Conversation) (GateState, int64) {
	h.t.Helper()
	raw, since, ok, err := h.st.LoadGateState("ca|" + conv.String())
	if err != nil || !ok {
		h.t.Fatalf("没有窗口状态：%v", err)
	}
	var s GateState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		h.t.Fatal(err)
	}
	return s, since
}

func (h *gh) seed(conv types.Conversation, s GateState, since int64) {
	h.t.Helper()
	raw, _ := json.Marshal(s)
	if err := h.st.SaveGateState("ca|"+conv.String(), string(raw), since, 0); err != nil {
		h.t.Fatal(err)
	}
}

func texts(b types.Batch) []string {
	var out []string
	for _, m := range b.Msgs {
		var t string
		for _, s := range m.Segs {
			t += s.Text
		}
		out = append(out, t)
	}
	return out
}

func same(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

// 低可能性的轮次攒了不少：门槛很低，一条普通消息能过
func lowRounds() GateState {
	s := NewGateState()
	for i := 0; i < 12; i++ {
		t := baseTime.Add(-time.Duration(50-i*4) * time.Minute)
		s.Rounds = append(s.Rounds, [2]float64{float64(t.UnixMilli()), 0.2})
	}
	return s
}

// 高可能性的轮次攒了不少：门槛很高，一条普通消息过不了
func highRounds() GateState {
	s := lowRounds()
	for i := range s.Rounds {
		s.Rounds[i][1] = 0.9
	}
	return s
}

// ---------- 档位 ----------

// 只记录档：不算、不放行，主人叫她也不回；消息照存
func TestGateRecordModeDoesNothing(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, func(c *GateConfig) { c.Mode = func(types.Conversation) string { return ModeRecord } })
	g := group("5001")
	h.say(g, friend, "你们好", 0)
	h.say(g, owner, "来", time.Second, atHer())
	h.say(g, friend, "在吗", 2*time.Second, atHer())
	if got := h.released(); len(got) != 0 {
		t.Fatalf("只记录档不该放行：%d 个", len(got))
	}
	if v := h.verdicts(g); !same(v, []string{VerdictOff, VerdictOff, VerdictOff}) {
		t.Fatalf("判断记录：%v", v)
	}
	if len(h.events(g)) != 3 {
		t.Fatal("消息该照存")
	}
	// 窗口状态也没有动
	if _, _, ok, _ := h.st.LoadGateState("ca|" + g.String()); ok {
		t.Fatal("只记录档不该有窗口状态")
	}
}

// 试运行和说话档闸门都照算，放行的批次一样（试运行不发由出口分流）
func TestGateDryAndSpeakBothRelease(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{ModeDryRun, ModeSpeak} {
		h := newGH(t, nil, func(c *GateConfig) { c.Mode = func(types.Conversation) string { return mode } })
		g := group("5001")
		h.say(g, friend, "在吗", 0, atHer())
		if got := h.released(); len(got) != 1 {
			t.Fatalf("%s 档该放行：%d 个", mode, len(got))
		}
	}
}

// 不认识的档按只记录办（宁可不说）
func TestGateUnknownModeIsRecord(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, func(c *GateConfig) { c.Mode = func(types.Conversation) string { return "wat" } })
	h.say(group("5001"), friend, "在吗", 0, atHer())
	if len(h.released()) != 0 {
		t.Fatal("不认识的档该按只记录办")
	}
}

// 档位表 gate_modes：这个群的 > 所有群的 > 默认；私聊、本地会话是说话；读不出来按只记录
func TestGateModesFromStore(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	m := GateModes(store.Single(h.st), ModeDryRun, h.logf)
	g1, g2 := group("5001"), group("5002")
	if m(g1) != ModeDryRun {
		t.Fatalf("没设过用默认（试运行）：%s", m(g1))
	}
	if err := h.st.SetGateMode("*", "off", 1); err != nil {
		t.Fatal(err)
	}
	if m(g1) != ModeRecord {
		t.Fatalf("所有群设成只记录：%s", m(g1))
	}
	if err := h.st.SetGateMode(g2.String(), "speak", 1); err != nil {
		t.Fatal(err)
	}
	if m(g2) != ModeSpeak || m(g1) != ModeRecord {
		t.Fatalf("这个群自己的盖过所有群的：%s %s", m(g2), m(g1))
	}
	if err := h.st.SetGateMode(g2.String(), "bogus", 1); err != nil {
		t.Fatal(err)
	}
	if m(g2) != ModeRecord {
		t.Fatalf("不认识的值按只记录：%s", m(g2))
	}
	if err := h.st.SetGateMode(g2.String(), "dry", 1); err != nil {
		t.Fatal(err)
	}
	if m(g2) != ModeDryRun {
		t.Fatalf("dry 对应试运行：%s", m(g2))
	}
	if m(private(acctA, friend)) != ModeSpeak || m(types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "x"}) != ModeSpeak {
		t.Fatal("私聊、本地会话没有档位，是说话")
	}
	h.st.Trace.Close()
	if m(g1) != ModeRecord {
		t.Fatal("读不出来按只记录")
	}
}

// ---------- 叫到她 ----------

// 叫到她：放行；批里带上她上次看完以后攒下的、没叫到她的几条，每条的 Addressed 各算各的
func TestGateCalledReleasesPendingWithAddressed(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.seed(g, highRounds(), 0) // 门槛高，前两句不会放行
	h.say(g, friend, "第一句", 0)
	h.say(g, other, "第二句", time.Second)
	if len(h.released()) != 0 {
		t.Fatal("前两句不该放行")
	}
	h.say(g, friend, "第三句", 2*time.Second, atHer())
	got := h.waitReleased(1)
	b := got[0]
	if !same(texts(b), []string{"第一句", "第二句", "第三句"}) {
		t.Fatalf("批里的话：%v", texts(b))
	}
	if b.Msgs[0].Addressed || b.Msgs[1].Addressed || !b.Msgs[2].Addressed || !b.Addressed {
		t.Fatalf("Addressed 各算各的：%+v", b.Msgs)
	}
	if b.Conv != g || b.Character != "ca" || b.Account != acctA || b.Trigger != types.TriggerMessage {
		t.Fatalf("批的头：%+v", b)
	}
	if b.Recent == nil {
		t.Fatal("批要带 Recent")
	}
	if d := h.lastDecision(g); d.Verdict != VerdictForced || d.Batch != 3 {
		t.Fatalf("判断：%+v", d)
	}
	// 看完了：状态里"上次看完"推到第三句
	if _, since := h.state(g); since != b.Msgs[2].Seq {
		t.Fatalf("上次看完 = %d，想要 %d", since, b.Msgs[2].Seq)
	}
}

// 引用她说过的话也算叫到她；@ 全体不算
func TestGateQuoteCountsAtAllDoesNot(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.seed(g, highRounds(), 0)
	// 她自己说过一句（她的号发出去的，平台消息号 7001）
	h.push(types.Inbound{Kind: types.InboundSelf, Account: acctA, Conv: g, PlatformID: "7001", From: types.User{ID: acctA}, Time: baseTime, Segs: []types.Seg{{Kind: types.SegText, Text: "我说过的"}}})
	h.idle()
	h.say(g, friend, "全体通知", time.Second, types.Seg{Kind: types.SegAt, Target: types.AtAll})
	if len(h.released()) != 0 {
		t.Fatal("@ 全体不算叫到她")
	}
	h.say(g, friend, "接你的话", 2*time.Second, types.Seg{Kind: types.SegReply, Target: "7001"})
	if len(h.waitReleased(1)) != 1 {
		t.Fatal("引用她的话该放行")
	}
	// 引用别人的话不算
	h2 := newGH(t, nil, nil)
	h2.seed(g, highRounds(), 0)
	id := h2.say(g, friend, "别人的话", 0)
	h2.say(g, other, "接别人的", time.Second, types.Seg{Kind: types.SegReply, Target: id})
	if len(h2.released()) != 0 {
		t.Fatal("引用别人的话不算叫到她")
	}
}

// 别名：消息文字里出现表里的词（至少 2 个字）算叫到；没配别名表这一支不开
func TestGateAliases(t *testing.T) {
	t.Parallel()
	g := group("5001")
	for _, tc := range []struct {
		name    string
		aliases func(string) []string
		text    string
		want    bool
	}{
		{"表里的词", func(string) []string { return []string{"小测"} }, "小测在吗", true},
		{"没配别名表", nil, "小测在吗", false},
		{"一个字的词不算", func(string) []string { return []string{"测"} }, "测一下", false},
		{"别的角色的别名", func(c string) []string { return map[string][]string{"cb": {"小测"}}[c] }, "小测在吗", false},
	} {
		h := newGH(t, nil, func(c *GateConfig) { c.Aliases = tc.aliases })
		h.seed(g, highRounds(), 0)
		h.say(g, friend, tc.text, 0)
		got := len(h.released()) == 1
		if got != tc.want {
			t.Errorf("%s：放行 %v，想要 %v", tc.name, got, tc.want)
		}
		if tc.want && !h.released()[0].Msgs[0].Addressed {
			t.Errorf("%s：Msg.Addressed 该为真", tc.name)
		}
	}
}

// 频率 0：只有主人叫到她才回，别人叫她、主人没叫她都不回
func TestGateFrequencyZeroOnlyOwner(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	if err := h.st.SetGateRule(store.GateRule{Conversation: g.String(), Frequency: 0}, false); err != nil {
		t.Fatal(err)
	}
	h.say(g, friend, "随便说说", 0)
	h.say(g, friend, "在吗", time.Second, atHer())
	h.say(g, owner, "我随便说说", 2*time.Second)
	if len(h.released()) != 0 {
		t.Fatalf("频率 0 时只有主人叫到她才回：%d", len(h.released()))
	}
	h.say(g, owner, "回我", 3*time.Second, atHer())
	got := h.waitReleased(1)
	if len(got) != 1 || got[0].Msgs[len(got[0].Msgs)-1].Origin != OriginOwner {
		t.Fatalf("主人叫她该回：%+v", got)
	}
	if v := h.verdicts(g); !same(v, []string{VerdictMuted, VerdictMuted, VerdictMuted, VerdictForced}) {
		t.Fatalf("判断记录：%v", v)
	}
}

// ---------- 合并一串 @ ----------

// 1 分钟内的一串叫到她合成一轮：第一个马上看，之后的等这一分钟过完一起看一次（不是每个 @ 都回，也不是一个都不回）
func TestGateBurstMergesAtsIntoOneRound(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.say(g, friend, "第一个", 0, atHer())
	if len(h.released()) != 1 {
		t.Fatal("第一个叫到她的马上看")
	}
	for i := 1; i <= 8; i++ { // 1 分钟内又来 8 个
		h.say(g, other, fmt.Sprintf("逗她 %d", i), time.Duration(i)*5*time.Second, atHer())
	}
	if len(h.released()) != 1 {
		t.Fatalf("1 分钟内的一串不该每个都放行：%d", len(h.released()))
	}
	h.clk.WaitPending(t, 1)
	h.clk.Advance(61 * time.Second)
	got := h.waitReleased(2)
	if len(got) != 2 {
		t.Fatalf("这一分钟过完该再看一次，共两次：%d", len(got))
	}
	if len(got[1].Msgs) != 8 || texts(got[1])[0] != "逗她 1" || texts(got[1])[7] != "逗她 8" {
		t.Fatalf("合并的一批：%v", texts(got[1]))
	}
	v := h.verdicts(g)
	if v[0] != VerdictForced || v[len(v)-1] != VerdictForced {
		t.Fatalf("判断记录：%v", v)
	}
	merged := 0
	for _, x := range v {
		if x == VerdictMerged {
			merged++
		}
	}
	if merged != 8 {
		t.Fatalf("合并了 %d 个，想要 8：%v", merged, v)
	}
	// 之后过了一分钟，新的 @ 又能马上看
	h.say(g, friend, "又来了", 3*time.Minute, atHer())
	if len(h.waitReleased(3)) != 3 {
		t.Fatal("一分钟之后的 @ 该马上看")
	}
}

// ---------- 拦截链接了 ----------

// 拦截插件接了：闸门只计数，不放行（哪怕叫到她），也不更新"上次看完"
func TestGateTakenOnlyCounts(t *testing.T) {
	t.Parallel()
	s := &spy{verdict: func(types.Batch) types.Verdict { return types.Took("测试接了") }}
	h := newGH(t, []types.Interceptor{s.plugin("taker", 20)}, nil)
	g := group("5001")
	h.say(g, friend, "在吗", 0, atHer())
	if len(h.released()) != 0 {
		t.Fatal("接了的批次不该放行")
	}
	d := h.lastDecision(g)
	if d.Verdict != VerdictTaken || !strings.Contains(d.Reason, "taker") {
		t.Fatalf("判断：%+v", d)
	}
	st, since := h.state(g)
	if len(st.Rounds) != 1 || len(st.Forced) != 0 || since != 0 {
		t.Fatalf("只记了这一轮的可能性，没算被叫到、没动上次看完：%+v since=%d", st, since)
	}
}

// ---------- 门槛 ----------

func TestGateThresholdEnterAndSkip(t *testing.T) {
	t.Parallel()
	g := group("5001")
	// 攒的轮次可能性低：门槛低，过
	h := newGH(t, nil, nil)
	h.seed(g, lowRounds(), 0)
	h.say(g, friend, "今天吃什么", 0)
	if len(h.released()) != 1 {
		t.Fatalf("门槛低该放行：%v", h.verdicts(g))
	}
	d := h.lastDecision(g)
	if d.Verdict != VerdictEnter || d.Probability == nil || d.Threshold == nil || *d.Probability < *d.Threshold || d.Features == "" || !strings.Contains(d.Reason, "门槛") {
		t.Fatalf("放行的判断该带算式：%+v", d)
	}
	// 攒的轮次可能性高：门槛高，不过，并且"上次看完"不动（下一次这条还在攒着）
	h2 := newGH(t, nil, nil)
	h2.seed(g, highRounds(), 0)
	h2.say(g, friend, "今天吃什么", 0)
	if len(h2.released()) != 0 {
		t.Fatalf("门槛高不该放行：%v", h2.verdicts(g))
	}
	d = h2.lastDecision(g)
	if d.Verdict != VerdictSkip || *d.Probability >= *d.Threshold {
		t.Fatalf("没接的判断：%+v", d)
	}
	if _, since := h2.state(g); since != 0 {
		t.Fatalf("没接的不该动上次看完：%d", since)
	}
	// 之后叫她时，没接的那句跟着一起给她
	h2.say(g, friend, "在吗", time.Second, atHer())
	b := h2.waitReleased(1)[0]
	if !same(texts(b), []string{"今天吃什么", "在吗"}) {
		t.Fatalf("没接的那句跟着给：%v", texts(b))
	}
}

// 默认频率 1：不设门槛，每条都放行；频率改了（gate_rules）立刻生效
func TestGateFrequencyFromRules(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.seed(g, highRounds(), 0)
	if err := h.st.SetGateRule(store.GateRule{Conversation: "*", Frequency: 1}, false); err != nil {
		t.Fatal(err)
	}
	h.say(g, friend, "随便一句", 0)
	if len(h.released()) != 1 {
		t.Fatalf("频率 1 该放行：%v", h.verdicts(g))
	}
	fp, err := h.g.FrequencyOf(g, baseTime)
	if err != nil || fp.Value != 1 || fp.From != "default" {
		t.Fatalf("现在生效的频率：%+v %v", fp, err)
	}
}

// ---------- 保险丝 ----------

func TestGateFuse(t *testing.T) {
	t.Parallel()
	g := group("5001")
	// 频率 1：不设门槛、不压（说多了的反馈只在 0～1 之间才起作用），上限 round(4+16) = 20 条；被叫到的 40 条
	full := func(c *GateConfig) {}
	setup := func(n int, tweak func(*GateConfig)) *gh {
		h := newGH(t, nil, tweak)
		if err := h.st.SetGateRule(store.GateRule{Conversation: "*", Frequency: 1}, false); err != nil {
			t.Fatal(err)
		}
		s := NewGateState()
		for i := 0; i < n; i++ {
			s.Replies = append(s.Replies, baseTime.Add(-time.Duration(i+1)*time.Second).UnixMilli())
		}
		h.seed(g, s, 0)
		return h
	}
	// 说了 20 条：主动的先不接
	h := setup(20, full)
	h.say(g, friend, "今天吃什么", 0)
	if d := h.lastDecision(g); len(h.released()) != 0 || d.Verdict != VerdictFuse || !strings.Contains(d.Reason, "保险丝") {
		t.Fatalf("主动的该被保险丝拦下：%v %s", h.verdicts(g), d.Reason)
	}
	// 被叫到的照样回
	h.say(g, friend, "在吗", time.Second, atHer())
	if len(h.waitReleased(1)) != 1 {
		t.Fatalf("被叫到的到上限不拦：%v", h.verdicts(g))
	}
	// 差一条：主动的也进
	h1 := setup(19, full)
	h1.say(g, friend, "今天吃什么", 0)
	if len(h1.released()) != 1 {
		t.Fatalf("19 条还没到上限：%v", h1.verdicts(g))
	}
	// 说了 40 条（上限的 2 倍）：被叫到的也先不接；39 条还接
	h2 := setup(40, full)
	h2.say(g, friend, "在吗", 0, atHer())
	if len(h2.released()) != 0 || h2.lastDecision(g).Verdict != VerdictFuse {
		t.Fatalf("到 2 倍被叫到也该拦：%v", h2.verdicts(g))
	}
	h3 := setup(39, full)
	h3.say(g, friend, "在吗", 0, atHer())
	if len(h3.released()) != 1 {
		t.Fatalf("39 条没到 2 倍：%v", h3.verdicts(g))
	}
	// 一个小时以前说的不算
	h4 := newGH(t, nil, nil)
	if err := h4.st.SetGateRule(store.GateRule{Conversation: "*", Frequency: 1}, false); err != nil {
		t.Fatal(err)
	}
	old := NewGateState()
	for i := 0; i < 30; i++ {
		old.Replies = append(old.Replies, baseTime.Add(-61*time.Minute-time.Duration(i)*time.Minute).UnixMilli())
	}
	h4.seed(g, old, 0)
	h4.say(g, friend, "今天吃什么", 0)
	if len(h4.released()) != 1 {
		t.Fatalf("一小时以前说的不算：%v", h4.verdicts(g))
	}
	// 关掉保险丝（回放验收用）
	h5 := setup(20, func(c *GateConfig) { c.NoFuse = true })
	h5.say(g, friend, "今天吃什么", 0)
	if len(h5.released()) != 1 {
		t.Fatalf("关掉保险丝该放行：%v", h5.verdicts(g))
	}
	// 频率低的群上限也低：频率 0（只有主人叫才回）时上限 4，主人叫她，说了 8 条（4 的 2 倍）也拦
	h6 := newGH(t, nil, nil)
	if err := h6.st.SetGateRule(store.GateRule{Conversation: g.String(), Frequency: 0}, false); err != nil {
		t.Fatal(err)
	}
	s6 := NewGateState()
	for i := 0; i < 8; i++ {
		s6.Replies = append(s6.Replies, baseTime.Add(-time.Duration(i+1)*time.Second).UnixMilli())
	}
	h6.seed(g, s6, 0)
	h6.say(g, owner, "回我", 0, atHer())
	if len(h6.released()) != 0 || h6.lastDecision(g).Verdict != VerdictFuse {
		t.Fatalf("频率 0 时上限 4、2 倍是 8：%v", h6.verdicts(g))
	}
}

// ---------- 退避和回合定了 ----------

func TestGateBackoffAndTurnDone(t *testing.T) {
	t.Parallel()
	g := group("5001")
	h := newGH(t, nil, nil)
	h.seed(g, lowRounds(), 0)
	// 连着两次看了没说话：该等 15 秒
	h.g.TurnDone("ca", g, false, 0, baseTime)
	h.g.TurnDone("ca", g, false, 0, baseTime)
	h.say(g, friend, "今天吃什么", 5*time.Second)
	if len(h.released()) != 0 || h.lastDecision(g).Verdict != VerdictBackoff {
		t.Fatalf("退避中该等：%v", h.verdicts(g))
	}
	// 被叫到不等
	h.say(g, friend, "在吗", 6*time.Second, atHer())
	if len(h.waitReleased(1)) != 1 {
		t.Fatalf("被叫到不等：%v", h.verdicts(g))
	}
	// 说了话，退避清零
	h.g.TurnDone("ca", g, true, 1, baseTime.Add(7*time.Second))
	st, _ := h.state(g)
	if st.Idle != (GateIdle{}) || len(st.Replies) != 1 {
		t.Fatalf("说了话该清零、记一次：%+v", st)
	}
	// 说了话但不计数（比如这句被拦下没写进计划）：退避清零，次数不加
	h.g.TurnDone("ca", g, true, 0, baseTime.Add(8*time.Second))
	if st, _ := h.state(g); len(st.Replies) != 1 {
		t.Fatalf("不计数的不该加次数：%+v", st)
	}
	// 攒够 6 条不等
	h2 := newGH(t, nil, nil)
	h2.seed(g, highRounds(), 0) // 门槛高，前几句都不放行，只攒着
	h2.g.TurnDone("ca", g, false, 0, baseTime)
	h2.g.TurnDone("ca", g, false, 0, baseTime)
	for i := 0; i < 5; i++ {
		h2.say(g, friend, fmt.Sprintf("第 %d 句", i), time.Duration(i+1)*time.Second)
	}
	if v := h2.verdicts(g); v[len(v)-1] != VerdictBackoff {
		t.Fatalf("攒了 5 条还在退避：%v", v)
	}
	h2.say(g, friend, "第六句", 6*time.Second)
	if v := h2.verdicts(g); v[len(v)-1] == VerdictBackoff {
		t.Fatalf("攒够 6 条不再等：%v", v)
	}
}

// ---------- 通道门 ----------

func TestGateChannelGate(t *testing.T) {
	t.Parallel()
	g := group("5001")
	var mu sync.Mutex
	var events []string
	h := newGH(t, nil, func(c *GateConfig) {
		c.OnPause = func(ch string, paused bool, why string) {
			mu.Lock()
			events = append(events, fmt.Sprintf("%s %v", ch, paused))
			mu.Unlock()
		}
	})
	// 连着 2 段失败 + 1 段确认：清零，不暂停
	h.g.PartEnded(chQQ, false, true)
	h.g.PartEnded(chQQ, false, true)
	h.g.PartEnded(chQQ, true, false)
	h.g.PartEnded(chQQ, false, true)
	h.g.PartEnded(chQQ, false, true)
	if _, p := h.g.Paused(chQQ); p {
		t.Fatal("中间有确认，连续的失败数该清零")
	}
	// 既不是确认也不是失败的（结果未知、中止）调用方不该调；两个都给或都不给也不算
	h.g.PartEnded(chQQ, false, false)
	h.g.PartEnded(chQQ, true, true)
	h.g.PartEnded(chQQ, false, true) // 连着第 3 段：暂停
	if why, p := h.g.Paused(chQQ); !p || !strings.Contains(why, "3") {
		t.Fatalf("连着 3 段失败该暂停：%q %v", why, p)
	}
	// 别的渠道不受影响
	if _, p := h.g.Paused("wx"); p {
		t.Fatal("别的渠道不该暂停")
	}
	h.say(g, friend, "在吗", 0, atHer())
	if len(h.released()) != 0 || h.lastDecision(g).Verdict != VerdictPaused {
		t.Fatalf("暂停了不放行：%v", h.verdicts(g))
	}
	// 暂停期间攒下的话，恢复以后叫她时一起给
	h.g.Resume(chQQ)
	if _, p := h.g.Paused(chQQ); p {
		t.Fatal("恢复以后不该暂停")
	}
	h.say(g, friend, "还在吗", time.Second, atHer())
	b := h.waitReleased(1)[0]
	if !same(texts(b), []string{"在吗", "还在吗"}) {
		t.Fatalf("恢复以后给的批：%v", texts(b))
	}
	mu.Lock()
	defer mu.Unlock()
	if !same(events, []string{"qq true", "qq false"}) {
		t.Fatalf("暂停、恢复各提醒一次：%v", events)
	}
	// 恢复一个没暂停的渠道不提醒
	h.g.Resume(chQQ)
	if len(events) != 2 {
		t.Fatalf("没暂停过的恢复不该提醒：%v", events)
	}
}

// ---------- 攒着的话怎么取 ----------

// 撤回的、补录的、她自己号发的、插件自动发的都不在攒着的话里
func TestGatePendingExcludes(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.seed(g, highRounds(), 0)
	// 补录的
	old := msg(acctA, g, "8001", friend, "补录来的")
	old.Time = baseTime.Add(-time.Hour)
	tk, err := h.in.Submit(context.Background(), old, Options{Backfill: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitTicket(t, tk); err != nil {
		t.Fatal(err)
	}
	keep := h.say(g, friend, "留着的", 0)
	recalled := h.say(g, other, "撤回的", time.Second)
	_ = keep
	h.notice(recall(acctA, g, "qq:"+other, recalled))
	h.idle()
	// 她的号上真人手动发的
	h.push(types.Inbound{Kind: types.InboundSelf, Account: acctA, Conv: g, PlatformID: "8002", From: types.User{ID: acctA}, Time: baseTime.Add(2 * time.Second), Segs: []types.Seg{{Kind: types.SegText, Text: "手动发的"}}})
	h.idle()
	// 插件自动发的
	if _, err := h.st.AppendEvent(store.NewEvent{Channel: chQQ, Conversation: g.String(), ActorKind: ActorCharacter, ActorID: "ca", Origin: "automation:repeat", Kind: "message", Text: "草", CreatedAt: baseTime.Add(3 * time.Second).UnixMilli()}, []string{"ca"}); err != nil {
		t.Fatal(err)
	}
	h.say(g, friend, "在吗", 4*time.Second, atHer())
	b := h.waitReleased(1)[0]
	if !same(texts(b), []string{"留着的", "在吗"}) {
		t.Fatalf("攒着的话：%v", texts(b))
	}
}

// 一次最多看最近 50 条，更早的只数个数，写进判断理由
func TestGatePendingCap(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	for i := 0; i < 60; i++ {
		if _, err := h.st.AppendEvent(store.NewEvent{Channel: chQQ, Conversation: g.String(), ActorKind: ActorHuman, ActorID: "qq:" + friend, Origin: OriginPeer, Kind: "message",
			Text: fmt.Sprintf("第 %d 句", i), Segments: map[string]any{"segs": []types.Seg{{Kind: types.SegText, Text: fmt.Sprintf("第 %d 句", i)}}}, CreatedAt: baseTime.Add(-time.Hour + time.Duration(i)*time.Second).UnixMilli()}, []string{"ca"}); err != nil {
			t.Fatal(err)
		}
	}
	h.say(g, friend, "在吗", 0, atHer())
	b := h.waitReleased(1)[0]
	if len(b.Msgs) != MaxPending || texts(b)[0] != "第 11 句" || texts(b)[MaxPending-1] != "在吗" {
		t.Fatalf("该给最近 %d 条：共 %d，首 %q 尾 %q", MaxPending, len(b.Msgs), texts(b)[0], texts(b)[len(b.Msgs)-1])
	}
	if d := h.lastDecision(g); !strings.Contains(d.Reason, "更早的 11 条") {
		t.Fatalf("理由里该说更早的还有几条：%s", d.Reason)
	}
}

// 叫到她的那一条已经被撤回：不进回合，也不算被叫到一次（规格-群聊闸门 2.2：被撤回的不进回合、不被她回复）
func TestGateRecalledCallIsNotReleased(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: chQQ, Conversation: g.String(), ActorKind: ActorHuman, ActorID: "qq:" + friend, Origin: OriginPeer, Kind: "message",
		Text: "在吗", Segments: map[string]any{"segs": []types.Seg{atHer(), {Kind: types.SegText, Text: "在吗"}}}, CreatedAt: baseTime.UnixMilli()}, []string{"ca"})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := h.st.Events.QueryRow(`SELECT id FROM events WHERE seq = ?`, seq).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendEvent(store.NewEvent{Channel: chQQ, Conversation: g.String(), ActorKind: ActorHuman, ActorID: "qq:" + friend, Origin: OriginPeer, Kind: "notice",
		Segments: map[string]any{"notice": "recall"}, ReplyTo: &id, CreatedAt: baseTime.Add(time.Second).UnixMilli()}, []string{"ca"}); err != nil {
		t.Fatal(err)
	}
	h.g.Handle(Dispatch{Batch: types.Batch{Conv: g, Character: "ca", Account: acctA, Msgs: []types.Msg{{Seq: seq, Time: baseTime, Who: "qq:" + friend, Origin: OriginPeer, Addressed: true}}}})
	if len(h.released()) != 0 {
		t.Fatal("撤回了的不进回合")
	}
	if rows, _ := h.g.Decisions(g, 10); len(rows) != 0 {
		t.Fatalf("没什么可判断的：%+v", rows)
	}
	if _, _, ok, _ := h.st.LoadGateState("ca|" + g.String()); ok {
		t.Fatal("也不该算被叫到一次")
	}
}

// 特征的取法：最近 5 分钟的窗口含她自己说的；离她上次说话多久，上次说话可以在窗口之外
func TestGateFeaturesFromRecentWindow(t *testing.T) {
	t.Parallel()
	g := group("5001")
	add := func(h *gh, kind, who, origin, text string, at time.Duration) {
		if _, err := h.st.AppendEvent(store.NewEvent{Channel: chQQ, Conversation: g.String(), ActorKind: kind, ActorID: who, Origin: origin, Kind: "message", Text: text,
			Segments: map[string]any{"segs": []types.Seg{{Kind: types.SegText, Text: text}}}, CreatedAt: baseTime.Add(at).UnixMilli()}, []string{"ca"}); err != nil {
			t.Fatal(err)
		}
	}
	features := func(h *gh) GateFeatures {
		t.Helper()
		var f GateFeatures
		if err := json.Unmarshal([]byte(h.lastDecision(g).Features), &f); err != nil {
			t.Fatalf("判断里该带特征：%v", err)
		}
		return f
	}
	// 她上次说话在 10 分钟前（窗口之外）：窗口里只有别人的，占比 0，离上次说话 600 秒
	h := newGH(t, nil, nil)
	h.seed(g, lowRounds(), 0)
	add(h, ActorCharacter, "ca", "character", "我说过", -10*time.Minute)
	add(h, ActorHuman, "qq:"+other, OriginPeer, "旁人一句", -time.Minute)
	h.say(g, friend, "今天吃什么？", 0)
	f := features(h)
	if f.RecentCount != 2 || f.SelfRatio != 0 || f.SinceSelfSec < 599 || f.SinceSelfSec > 601 || !f.Question || f.Batch != 2 {
		t.Fatalf("特征：%+v", f)
	}
	// 她 1 分钟前说过话：占比 1/3；离上次说话 60 秒。真人用她的号手动发的、以前别的程序发的不算她说的
	h2 := newGH(t, nil, nil)
	h2.seed(g, lowRounds(), 0)
	add(h2, ActorCharacter, "ca", "character", "我说过", -time.Minute)
	add(h2, ActorCharacter, "ca", "human", "真人手动发的", -30*time.Second)
	add(h2, ActorCharacter, "ca", "external", "以前别的程序发的", -20*time.Second)
	h2.say(g, friend, "今天吃什么", 0)
	f = features(h2)
	if f.RecentCount != 4 || f.SelfRatio != 0.25 || f.SinceSelfSec < 59 || f.SinceSelfSec > 61 {
		t.Fatalf("特征：%+v", f)
	}
}

// ---------- 重启 ----------

// 窗口状态存了库：换一个闸门（重启）接着算，已经看过的不再给
func TestGateStateSurvivesRestart(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.say(g, friend, "第一次", 0, atHer())
	h.g.TurnDone("ca", g, true, 1, baseTime.Add(time.Second))
	first := h.waitReleased(1)[0]
	h.g.Close()
	cfg := h.g.cfg
	g2, err := NewGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g2.Close)
	h.g = g2
	h.say(g, friend, "第二次", 2*time.Minute, atHer())
	got := h.waitReleased(2)
	if !same(texts(got[1]), []string{"第二次"}) {
		t.Fatalf("重启以后不该再给已经看过的：%v（第一批 %v）", texts(got[1]), texts(first))
	}
	st, _ := h.state(g)
	if len(st.Forced) != 2 || len(st.Replies) != 1 {
		t.Fatalf("重启以后窗口里的该还在：%+v", st)
	}
}

// ---------- 出错 ----------

// 出错 = 不放行（和拦截插件出错放行相反）：存状态的库读不出来，叫她也不放行，记一行日志
func TestGateErrorMeansNoRelease(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := group("5001")
	h.st.Trace.Close()
	h.say(g, friend, "在吗", 0, atHer())
	if len(h.released()) != 0 {
		t.Fatal("读不出状态不该放行")
	}
	if !strings.Contains(h.logText(), "不放行") {
		t.Fatalf("该记一行日志：%s", h.logText())
	}
}

// 判断里 panic 了也不放行、不影响后面的
func TestGatePanicInReleaseHookIsNotFatalToGate(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, func(c *GateConfig) { c.Aliases = func(string) []string { panic("别名表坏了") } })
	g := group("5001")
	h.say(g, friend, "在吗", 0) // 没 @ 她：要问别名表，别名表 panic
	if len(h.released()) != 0 {
		t.Fatal("判断时出了意外，这一批不放行")
	}
	if !strings.Contains(h.logText(), "意外") {
		t.Fatalf("该记一行日志：%s", h.logText())
	}
}

// ---------- 别的场合 ----------

// QQ 私聊不过闸门（Gate 只管群）
func TestGateIgnoresPrivate(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	p := private(acctA, friend)
	h.say(p, friend, "在吗", 0, atHer())
	if len(h.released()) != 0 {
		t.Fatal("私聊不过闸门")
	}
	if rows, _ := h.g.Decisions(p, 10); len(rows) != 0 {
		t.Fatalf("私聊没有判断记录：%+v", rows)
	}
}

// 本地群聊（渠道 console）和 Bun 一样不过闸门
func TestGateIgnoresConsole(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, nil)
	g := types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "home"}
	h.g.Handle(Dispatch{Batch: types.Batch{Conv: g, Character: "ca", Msgs: []types.Msg{{Seq: 1, Time: baseTime, Addressed: true}}}})
	if len(h.released()) != 0 {
		t.Fatal("本地群聊不过闸门")
	}
	if rows, _ := h.g.Decisions(g, 10); len(rows) != 0 {
		t.Fatalf("本地群聊没有判断记录：%+v", rows)
	}
}

// 构造要报错：缺库、缺放行的去处
func TestNewGateValidates(t *testing.T) {
	t.Parallel()
	if _, err := NewGate(GateConfig{Release: func(types.Batch) {}}); err == nil {
		t.Error("没有库该报错")
	}
	h := newGH(t, nil, nil)
	if _, err := NewGate(GateConfig{Stores: store.Single(h.st)}); err == nil {
		t.Error("没有放行的去处该报错")
	}
	bad := testRoster()
	bad.Accounts = append(bad.Accounts, Account{chQQ, acctA, "cb"})
	if _, err := NewGate(GateConfig{Stores: store.Single(h.st), Release: func(types.Batch) {}, Roster: bad}); err == nil {
		t.Error("名册不合法该报错")
	}
}
