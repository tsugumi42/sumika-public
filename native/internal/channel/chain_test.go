package channel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 拦截链的测试：只测链自己（谁先问、谁停、出错超时怎么办、开关、场合、档位），用假插件。
// 链和入库一站、出口连起来的行为在 chain_integration_test.go

// recorder 记下链问了哪些插件、问的顺序
type recorder struct {
	mu    sync.Mutex
	order []string
	logs  []string
}

func (r *recorder) called(name string) {
	r.mu.Lock()
	r.order = append(r.order, name)
	r.mu.Unlock()
}

func (r *recorder) names() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.order, ",")
}

func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	r.mu.Unlock()
}

func (r *recorder) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// plug 造一个在任何群里关心消息的假插件
func plug(r *recorder, name string, prio int, v types.Verdict) types.Interceptor {
	return types.Interceptor{
		Name: name, Priority: prio,
		Occasions: []types.Occasion{{Scene: types.SceneGroup}},
		Triggers:  []string{types.TriggerMessage},
		Intercept: func(context.Context, types.Batch) types.Verdict { r.called(name); return v },
	}
}

func always(string, string, types.Conversation) (on, set bool) { return true, true }

func msgBatch(conv types.Conversation) types.Batch {
	return types.Batch{Conv: conv, Character: "ca", Account: acctA, Trigger: types.TriggerMessage,
		Msgs: []types.Msg{{Seq: 1, Who: "qq:" + friend, Segs: []types.Seg{{Kind: types.SegText, Text: "秘密内容"}}}}}
}

func newChain(t *testing.T, r *recorder, tweak func(*ChainConfig), plugins ...types.Interceptor) *Chain {
	t.Helper()
	cfg := ChainConfig{Plugins: plugins, Switch: always, Logf: r.logf}
	if tweak != nil {
		tweak(&cfg)
	}
	c, err := NewChain(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// 优先级小的先问；同一个数字按注册的先后，一个接一个
func TestChainRunsInPriorityOrder(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	c := newChain(t, r, nil,
		plug(r, "late", 20, types.Passed("")),
		plug(r, "first", 10, types.Passed("")),
		plug(r, "second", 10, types.Passed("")),
		plug(r, "zero", 0, types.Passed("")),
	)
	if res := c.Run(context.Background(), msgBatch(group("5001"))); res.Taken || !res.Ran {
		t.Fatalf("都没接，应该是跑了、没接：%+v", res)
	}
	if got := r.names(); got != "zero,first,second,late" {
		t.Fatalf("问的顺序：%s", got)
	}
}

// 有一个接了，链就停：后面的不再问，闸门拿到的结果里写着是谁接的；被抢先的记一行
func TestChainStopsAtFirstTaker(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	c := newChain(t, r, nil,
		plug(r, "poke", 10, types.Passed("冷却中")),
		plug(r, "repeat", 20, types.Took("跟了一遍")),
		plug(r, "keyword", 30, types.Took("不该被问到")),
	)
	res := c.Run(context.Background(), msgBatch(group("5001")))
	if !res.Taken || res.By != "repeat" || res.Reason != "跟了一遍" {
		t.Fatalf("结果：%+v", res)
	}
	if got := r.names(); got != "poke,repeat" {
		t.Fatalf("接了以后后面的不该再问：%s", got)
	}
	logs := r.logText()
	for _, want := range []string{"插件 poke 没接（冷却中）", "插件 repeat 接了（跟了一遍）", "插件 keyword 没跑，被 repeat 抢先", "qq:group:5001", "角色 ca"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("日志里没有 %q：\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "秘密内容") {
		t.Fatalf("日志里不该有消息原文：\n%s", logs)
	}
}

// 插件 panic、超时都当没接：交给下一个插件，最后交给闸门（拦截插件坏了不能让她哑巴）
func TestChainPanicAndTimeoutAreNotTaken(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	clk := fake.NewClock(baseTime)
	sawCancel := make(chan struct{})
	entered := make(chan struct{})
	blocked := types.Interceptor{
		Name: "stuck", Priority: 20, Occasions: []types.Occasion{{Scene: types.SceneGroup}}, Triggers: []string{types.TriggerMessage},
		Intercept: func(ctx context.Context, b types.Batch) types.Verdict {
			r.called("stuck")
			close(entered)
			<-ctx.Done() // 超时以后链会取消它的 ctx
			close(sawCancel)
			return types.Took("迟到的回答，该被丢掉")
		},
	}
	boom := types.Interceptor{
		Name: "boom", Priority: 10, Occasions: []types.Occasion{{Scene: types.SceneGroup}}, Triggers: []string{types.TriggerMessage},
		Intercept: func(context.Context, types.Batch) types.Verdict { r.called("boom"); panic("坏了") },
	}
	c := newChain(t, r, func(c *ChainConfig) { c.Clock = clk }, boom, blocked, plug(r, "after", 30, types.Passed("")))

	done := make(chan ChainResult, 1)
	go func() { done <- c.Run(context.Background(), msgBatch(group("5001"))) }()
	<-entered
	clk.WaitTimer(t, DefaultInterceptTimeout) // 卡住的那个在等 3 秒
	clk.Advance(DefaultInterceptTimeout)
	var res ChainResult
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("超时以后链还卡着")
	}
	select {
	case <-sawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("超时以后没有取消插件的 ctx")
	}
	if res.Taken {
		t.Fatalf("panic 和超时都该当没接，迟到的回答不算：%+v", res)
	}
	if got := r.names(); got != "boom,stuck,after" {
		t.Fatalf("坏的和卡住的之后还要接着问下一个：%s", got)
	}
	logs := r.logText()
	for _, want := range []string{"插件 boom 没接（出了意外，当没接：坏了）", "插件 stuck 没接（超时（等了 3s），当没接）"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("日志里没有 %q：\n%s", want, logs)
		}
	}
}

// 设过开关的听开关；没设过的看插件自己声明的默认（群里的插件默认全关，戳回去在 QQ 私聊里默认开，ADR-189）；
// 场合和触发对不上不问（也不记日志）
func TestChainSwitchOccasionAndTrigger(t *testing.T) {
	t.Parallel()
	repeater := func(r *recorder) types.Interceptor { return plug(r, "repeat", 20, types.Passed("")) }
	pokeBack := func(r *recorder) types.Interceptor {
		return types.Interceptor{
			Name: "poke", Priority: 10, Triggers: []string{types.NoticePoke},
			Occasions: []types.Occasion{{Channel: chQQ, Scene: types.ScenePrivate}, {Channel: chQQ, Scene: types.SceneGroup}},
			DefaultOn: []types.Occasion{{Channel: chQQ, Scene: types.ScenePrivate}},
			Intercept: func(context.Context, types.Batch) types.Verdict { r.called("poke"); return types.Passed("") },
		}
	}
	setOn := func(string, string, types.Conversation) (bool, bool) { return true, true }
	setOff := func(string, string, types.Conversation) (bool, bool) { return false, true }
	notSet := func(string, string, types.Conversation) (bool, bool) { return true, false } // 没设过：on 的值不该被看
	consoleGroup := types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "g"}
	pokeAtHer := func(conv types.Conversation) types.Batch {
		return types.Batch{Conv: conv, Character: "ca", Trigger: types.NoticePoke}
	}
	cases := []struct {
		name   string
		sw     func(plugin, character string, conv types.Conversation) (bool, bool)
		batch  types.Batch
		plugin func(r *recorder) types.Interceptor
		want   bool
	}{
		{"没给开关钩子、插件没有默认开的场合：关", nil, msgBatch(group("5001")), repeater, false},
		{"设成开", setOn, msgBatch(group("5001")), repeater, true},
		{"设成关", setOff, msgBatch(group("5001")), repeater, false},
		{"钩子说没设过：走默认（没有默认开的场合：关）", notSet, msgBatch(group("5001")), repeater, false},
		{"设成开的是别的群：这个群还是关", func(_, _ string, c types.Conversation) (bool, bool) { return c.ID == "5002", true }, msgBatch(group("5001")), repeater, false},
		{"设成开的是别的角色：这个角色还是关", func(_, ch string, _ types.Conversation) (bool, bool) { return ch == "cb", true }, msgBatch(group("5001")), repeater, false},
		{"开关钩子 panic：当设成了关", func(string, string, types.Conversation) (bool, bool) { panic("坏了") }, msgBatch(group("5001")), repeater, false},
		{"复读不在私聊跑（场合里没有）", setOn, msgBatch(private(acctA, friend)), repeater, false},
		{"空渠道的场合是任何渠道：本地群聊也跑", setOn, msgBatch(consoleGroup), repeater, true},
		{"戳一戳是 QQ 独有：本地群聊不跑", setOn, pokeAtHer(consoleGroup), pokeBack, false},
		{"消息批不问只关心戳一戳的", setOn, msgBatch(group("5001")), pokeBack, false},
		{"戳回去在 QQ 私聊里默认开（没给开关钩子）", nil, pokeAtHer(private(acctA, friend)), pokeBack, true},
		{"戳回去在 QQ 私聊里默认开（钩子说没设过）", notSet, pokeAtHer(private(acctA, friend)), pokeBack, true},
		{"这个私聊单独关掉：压过默认", func(_, _ string, c types.Conversation) (bool, bool) { return false, c.ID == friend }, pokeAtHer(private(acctA, friend)), pokeBack, false},
		{"别的私聊关掉，这个私聊还是默认开", func(_, _ string, c types.Conversation) (bool, bool) { return false, c.ID == other }, pokeAtHer(private(acctA, friend)), pokeBack, true},
		{"戳回去在 QQ 群里默认关（没给开关钩子）", nil, pokeAtHer(group("5001")), pokeBack, false},
		{"戳回去在 QQ 群里设成开才跑", setOn, pokeAtHer(group("5001")), pokeBack, true},
		{"开关钩子 panic：私聊的默认开也压不过（当设成了关）", func(string, string, types.Conversation) (bool, bool) { panic("坏了") }, pokeAtHer(private(acctA, friend)), pokeBack, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &recorder{}
			p := tc.plugin(r)
			c := newChain(t, r, func(c *ChainConfig) { c.Switch = tc.sw }, p)
			c.Run(context.Background(), tc.batch)
			if got := r.names() != ""; got != tc.want {
				t.Fatalf("问了没有 = %v，想要 %v；日志：%s", got, tc.want, r.logText())
			}
			for _, verb := range []string{"没接", "接了", "没跑"} {
				if !tc.want && strings.Contains(r.logText(), "插件 "+p.Name+" "+verb) {
					t.Fatalf("没进链的插件不该记结果（不然每条消息都记一行不关我事）：\n%s", r.logText())
				}
			}
		})
	}
}

// 开关钩子拿到的是（插件名、角色、会话）
func TestChainAsksSwitchWithPluginCharacterAndConversation(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var gotPlugin, gotChar string
	var gotConv types.Conversation
	c := newChain(t, r, func(c *ChainConfig) {
		c.Switch = func(p, ch string, conv types.Conversation) (bool, bool) {
			gotPlugin, gotChar, gotConv = p, ch, conv
			return true, true
		}
	}, plug(r, "repeat", 20, types.Passed("")))
	c.Run(context.Background(), msgBatch(group("5001")))
	if gotPlugin != "repeat" || gotChar != "ca" || gotConv != group("5001") {
		t.Fatalf("开关钩子的参数：%q %q %+v", gotPlugin, gotChar, gotConv)
	}
}

// 档位：只记录档（和不认识的档）链不跑；试运行和说话链照跑
func TestChainMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode   func(types.Conversation) string
		ran    bool
		called bool
	}{
		{nil, true, true},
		{func(types.Conversation) string { return ModeSpeak }, true, true},
		{func(types.Conversation) string { return ModeDryRun }, true, true},
		{func(types.Conversation) string { return ModeRecord }, false, false},
		{func(types.Conversation) string { return "不认识" }, false, false},
		{func(types.Conversation) string { panic("坏了") }, false, false},
	} {
		r := &recorder{}
		c := newChain(t, r, func(c *ChainConfig) { c.Mode = tc.mode }, plug(r, "repeat", 20, types.Took("跟了")))
		res := c.Run(context.Background(), msgBatch(group("5001")))
		if res.Ran != tc.ran || (r.names() != "") != tc.called || res.Taken != tc.called {
			t.Fatalf("结果 %+v，问了 %q；想要 ran=%v called=%v", res, r.names(), tc.ran, tc.called)
		}
	}
}

// 档位钩子拿到的是这一批的会话
func TestChainModeIsPerConversation(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	c := newChain(t, r, func(c *ChainConfig) {
		c.Mode = func(conv types.Conversation) string {
			if conv.ID == "5001" {
				return ModeSpeak
			}
			return ModeRecord
		}
	}, plug(r, "repeat", 20, types.Passed("")))
	c.Run(context.Background(), msgBatch(group("5002")))
	if r.names() != "" {
		t.Fatal("5002 是只记录档，不该问")
	}
	c.Run(context.Background(), msgBatch(group("5001")))
	if r.names() != "repeat" {
		t.Fatal("5001 是说话档，该问")
	}
}

// 渠道关了（ctx 到期）：不再问，结果是没接
func TestChainStopsWhenContextDone(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	c := newChain(t, r, nil, plug(r, "repeat", 20, types.Took("跟了")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := c.Run(ctx, msgBatch(group("5001"))); res.Taken || r.names() != "" {
		t.Fatalf("关了以后不该再问：%+v %q", res, r.names())
	}
	if r.logText() != "" {
		t.Fatalf("关了以后什么都不记：%s", r.logText())
	}
}

// Batch.Now 链给的时刻和链的钟一致；插件自己不读钟
func TestChainStampsBatchNow(t *testing.T) {
	t.Parallel()
	clk := fake.NewClock(baseTime)
	r := &recorder{}
	var got time.Time
	p := plug(r, "repeat", 20, types.Passed(""))
	p.Intercept = func(_ context.Context, b types.Batch) types.Verdict { got = b.Now; return types.Passed("") }
	c := newChain(t, r, func(c *ChainConfig) { c.Clock = clk }, p)
	c.Run(context.Background(), msgBatch(group("5001")))
	if !got.Equal(baseTime) {
		t.Fatalf("Batch.Now = %v，想要 %v", got, baseTime)
	}
}

// 写错了的插件启动即报错：悄悄不工作比报错更糟
func TestNewChainRejectsBadPlugins(t *testing.T) {
	t.Parallel()
	ok := plug(&recorder{}, "a", 1, types.Passed(""))
	mut := func(f func(*types.Interceptor)) types.Interceptor { p := ok; f(&p); return p }
	for name, plugins := range map[string][]types.Interceptor{
		"没名字":      {mut(func(p *types.Interceptor) { p.Name = "" })},
		"重名":       {ok, ok},
		"没有处理函数":   {mut(func(p *types.Interceptor) { p.Intercept = nil })},
		"没声明场合":    {mut(func(p *types.Interceptor) { p.Occasions = nil })},
		"没声明关心的批次": {mut(func(p *types.Interceptor) { p.Triggers = nil })},
	} {
		if _, err := NewChain(ChainConfig{Plugins: plugins}); err == nil {
			t.Fatalf("%s：该报错", name)
		}
	}
	if _, err := NewChain(ChainConfig{Plugins: []types.Interceptor{ok}}); err != nil {
		t.Fatal(err)
	}
}
