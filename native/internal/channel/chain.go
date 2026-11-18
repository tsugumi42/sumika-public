package channel

// 拦截链（规格-渠道中间件 第七节，第十五节第 4 步）：每一批进来，按优先级（小的先）一个一个问拦截插件"这一批你接不接"。
//   - 有一个明确说"接了"，链就停：后面的插件不再问，这一批不交给内核；闸门照常收到，只计数、更新退避（流水线 pipeline.go 把结果交给它）。
//   - 出错、超时、panic 都当"没接"，记日志，交给下一个插件，最后交给闸门——拦截插件坏了不能让她哑巴。
//     AstrBot 的做法（插件异常停掉整个事件，还把错误文字发到聊天）、NoneBot 的"运行过就停"都不照：错误只进日志。
//   - 什么时候问哪个插件：这批的触发（消息 / 某种通知）是它关心的、会话在它声明的场合里、开着它。
//     开关链自己不存，问宿主的 Switch（主人在设置里设的）；没设过的看插件自己声明的默认（DefaultOn）：群里的插件默认全关
//     （规格-群聊插件 第一节），戳回去在她本来就接的私聊里默认开（ADR-189）。设过的永远压过默认。
//   - 档位：说话和试运行档链照跑（试运行的输出由出口写成 origin = automation:dry-run、不发）；只记录档链不跑——她不进来看，插件也不动。
//   - 同一个优先级的按注册先后、一个接一个（不并发）：先到先赢才有意义，并发会让谁赢取决于调度。
//   - 每个进了链的插件记一行：插件名、会话、结果（接了 / 没接 + 原因）；排在接了的后面、本来也要问的，记"被 X 抢先"。日志不放消息原文。

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"sumika-native/internal/channel/types"
)

// DefaultInterceptTimeout 是问一个插件最多等多久（规格 第十九节 B12 的初值，量过再调）：拦截在模型之前，慢了就放行，不能卡住整批
const DefaultInterceptTimeout = 3 * time.Second

// ChainConfig 是链的配置
type ChainConfig struct {
	Plugins []types.Interceptor
	// Switch 说某个角色在某个会话里有没有设过某个插件的开关：返回（开着吗，设过吗）。设过的照它办，没设过的用插件声明的默认（Interceptor.DefaultOn）；
	// nil 就是谁都没设过。panic 当设成了关（宁可不动）
	Switch func(plugin, character string, conv types.Conversation) (on, set bool)
	// Mode 说一个会话现在是什么档（和出口用同一个钩子）；nil 就是都说话。只记录档和不认识的值，链不跑
	Mode    func(types.Conversation) string
	Clock   types.Clock // nil 用系统的钟；超时、Batch.Now 都从它取
	Timeout time.Duration
	Logf    func(format string, args ...any) // nil 用 log.Printf
}

// ChainResult 是一批过完链的结果
type ChainResult struct {
	Ran    bool   // 链跑了（档位允许）；只记录档是 false。跑了但没有插件要问，也是 true
	Taken  bool   // 有插件接了
	By     string // 接了的插件
	Reason string // 它给的理由
}

// Chain 是拦截链
type Chain struct {
	cfg     ChainConfig
	plugins []types.Interceptor // 按 Priority 排好，同一个数字保持注册的先后
	clock   types.Clock
}

// NewChain 造链。插件没名字、重名、没有处理函数、没声明场合或触发，启动即报错（这些写错了插件就永远不会被问到，悄悄不工作比报错更糟）
func NewChain(cfg ChainConfig) (*Chain, error) {
	seen := map[string]bool{}
	for i, p := range cfg.Plugins {
		switch {
		case p.Name == "":
			return nil, fmt.Errorf("第 %d 个拦截插件没有名字", i+1)
		case seen[p.Name]:
			return nil, fmt.Errorf("拦截插件 %q 注册了两次", p.Name)
		case p.Intercept == nil:
			return nil, fmt.Errorf("拦截插件 %q 没有处理函数", p.Name)
		case len(p.Occasions) == 0:
			return nil, fmt.Errorf("拦截插件 %q 没有声明在哪些场合跑", p.Name)
		case len(p.Triggers) == 0:
			return nil, fmt.Errorf("拦截插件 %q 没有声明关心哪些批次", p.Name)
		}
		seen[p.Name] = true
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultInterceptTimeout
	}
	c := &Chain{cfg: cfg, clock: cfg.Clock, plugins: append([]types.Interceptor(nil), cfg.Plugins...)}
	if c.clock == nil {
		c.clock = SystemClock{}
	}
	sort.SliceStable(c.plugins, func(i, j int) bool { return c.plugins[i].Priority < c.plugins[j].Priority })
	return c, nil
}

func (c *Chain) logf(format string, args ...any) {
	if c.cfg.Logf != nil {
		c.cfg.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Now 是链的钟；Pipeline 造 Batch 时用它，插件看到的 Batch.Now 和超时是同一个钟
func (c *Chain) Now() time.Time { return c.clock.Now() }

// modeOf 问档位；钩子 panic 或不认识的值都按只记录办
func (c *Chain) modeOf(conv types.Conversation) (mode string) {
	if c.cfg.Mode == nil {
		return ModeSpeak
	}
	defer func() {
		if r := recover(); r != nil {
			c.logf("拦截链 会话 %s 查档位时出了意外：%v，按只记录办（链不跑）", conv.String(), r)
			mode = ModeRecord
		}
	}()
	return c.cfg.Mode(conv)
}

// on 说这个插件在这一批的会话里开着没有：设过开关的听开关，没设过的看插件的默认
func (c *Chain) on(p types.Interceptor, b types.Batch) bool {
	if on, set := c.setting(p.Name, b.Character, b.Conv); set {
		return on
	}
	for _, o := range p.DefaultOn {
		if o.Matches(b.Conv) {
			return true
		}
	}
	return false
}

func (c *Chain) setting(plugin, character string, conv types.Conversation) (on, set bool) {
	if c.cfg.Switch == nil {
		return false, false
	}
	defer func() {
		if r := recover(); r != nil {
			c.logf("拦截链 会话 %s 查插件 %s 的开关时出了意外：%v，按关掉办", conv.String(), plugin, r)
			on, set = false, true
		}
	}()
	return c.cfg.Switch(plugin, character, conv)
}

// wants 说这个插件要不要问：批次是它关心的、会话在它声明的场合里、开着
func (c *Chain) wants(p types.Interceptor, b types.Batch) bool {
	trigger := false
	for _, t := range p.Triggers {
		if t == b.Trigger {
			trigger = true
			break
		}
	}
	if !trigger {
		return false
	}
	in := false
	for _, o := range p.Occasions {
		if o.Matches(b.Conv) {
			in = true
			break
		}
	}
	return in && c.on(p, b)
}

// Run 把一批过一遍链。b.Now 为零时用链的钟。ctx 到期（渠道关了）时不再问后面的插件，结果是没接
func (c *Chain) Run(ctx context.Context, b types.Batch) ChainResult {
	if m := c.modeOf(b.Conv); m != ModeSpeak && m != ModeDryRun {
		return ChainResult{}
	}
	if b.Now.IsZero() {
		b.Now = c.clock.Now()
	}
	res := ChainResult{Ran: true}
	for _, p := range c.plugins {
		if !c.wants(p, b) {
			continue
		}
		if res.Taken {
			c.logf("拦截链 会话 %s 角色 %s：插件 %s 没跑，被 %s 抢先", b.Conv.String(), b.Character, p.Name, res.By)
			continue
		}
		if ctx.Err() != nil {
			return res // 渠道关了：不再问了，也不记日志（关了以后什么都不再做，清单④）
		}
		v := c.ask(ctx, p, b)
		if v.Taken {
			res.Taken, res.By, res.Reason = true, p.Name, v.Reason
			c.logf("拦截链 会话 %s 角色 %s：插件 %s 接了（%s）", b.Conv.String(), b.Character, p.Name, clipRunes(v.Reason, 200))
		} else {
			c.logf("拦截链 会话 %s 角色 %s：插件 %s 没接（%s）", b.Conv.String(), b.Character, p.Name, clipRunes(v.Reason, 200))
		}
	}
	return res
}

// ask 问一个插件。插件在另一个 goroutine 里跑：它不返回（没守"ctx 到期就返回"）也不会把这里卡死，超时就放行；
// 超时之后它再返回什么都没人读（通道缓冲 1，不会堵）。到期的 ctx 传给它，它之后再通过 Outbox 发东西会被出口拒绝
func (c *Chain) ask(ctx context.Context, p types.Interceptor, b types.Batch) types.Verdict {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan types.Verdict, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- types.Passed(fmt.Sprintf("出了意外，当没接：%v", r))
			}
		}()
		ch <- p.Intercept(pctx, b)
	}()
	t := c.clock.NewTimer(c.cfg.Timeout)
	defer t.Stop()
	select {
	case v := <-ch:
		return v
	case <-t.C():
		return types.Passed(fmt.Sprintf("超时（等了 %v），当没接", c.cfg.Timeout))
	case <-ctx.Done():
		return types.Passed("渠道关了，没等它")
	}
}
