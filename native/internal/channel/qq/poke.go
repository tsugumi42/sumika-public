// Package qq 是 QQ 平台插件（规格-渠道中间件 第一节）：QQ 独有的、不属于协议翻译也不属于通用中间件的规矩。
// 这一步只有戳回去；#sl 协议命令、收发自检、掉线和被踢的提醒文字是第 9 步。
// 它只引 types：能做的事只有读 Batch、返回 Verdict、通过构造时拿到的 Outbox 发东西，拿不到适配器（规格 第十节 10.5"挡法"）。
package qq

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
)

const (
	// PokeName 是戳回去的插件名，也是 automation:<名字> 里的名字和开关的键
	PokeName = "poke"
	// PokePriority 是优先级初值（需求-拦截类插件接口 第四节）：戳一戳是通知，和复读不冲突，排在复读前面
	PokePriority = 10

	// 下面几个数都是我定的初值（规格-群聊插件 二点五，规格-渠道中间件 第十九节 B12），量过再调
	DefaultPokeCooldown  = 60 * time.Second // 同一个人戳她，这么久之内只戳回一次
	DefaultPokePerMinute = 3                // 同一个会话每分钟最多戳回几次
	pokeWindow           = time.Minute
)

// PokeConfig 是戳回去的配置
type PokeConfig struct {
	Out types.Outbox // 必填
	// IsBot 说这个号是不是机器人（名单加按行为认出的模板号）：对方也是机器人就不戳回，免得互戳停不下来；nil 就是谁都不是
	IsBot func(channel, userID string) bool
	// Cooldown、PerMinute 不大于 0 就用默认值
	Cooldown  time.Duration
	PerMinute int
}

// Poke 是戳回去插件：有人戳她，她戳回去，不过模型、不过守卫（没有字）；经出口发 poke 段，所以发送检查（风控间隔、渠道断开）照样过。
// 不计开口、不计退避（规格-群聊闸门 10.1：戳回去不算开口）——出口对只有戳一戳的计划不写 events，闸门数她说过几句时看不到它。
// 用哪个号戳由出口定（群里是这个角色绑的第一个号，私聊是会话里她的号），不一定是被戳的那个号
type Poke struct {
	cfg PokeConfig

	mu       sync.Mutex
	byPerson map[string]time.Time   // 角色 + 会话 + 人 → 上次戳回去的时刻
	byConv   map[string][]time.Time // 角色 + 会话 → 最近一分钟内戳回去的时刻
}

// NewPoke 造戳回去插件
func NewPoke(cfg PokeConfig) (*Poke, error) {
	if cfg.Out == nil {
		return nil, errors.New("戳回去没有出口")
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultPokeCooldown
	}
	if cfg.PerMinute <= 0 {
		cfg.PerMinute = DefaultPokePerMinute
	}
	return &Poke{cfg: cfg, byPerson: map[string]time.Time{}, byConv: map[string][]time.Time{}}, nil
}

// Interceptor 交给拦截链：QQ 的私聊和群，只关心戳一戳。
// 开关（ADR-189）：群里默认全关，主人按群打开；私聊里默认开，每个私聊能单独关。"只限她本来就接的私聊"不在这里判——
// 不接的人（陌生人）的通知，宿主的准入检查（Config.AdmitNotice）标成不记看到了，流水线不会把它交给链
func (p *Poke) Interceptor() types.Interceptor {
	private := types.Occasion{Channel: "qq", Scene: types.ScenePrivate}
	return types.Interceptor{
		Name: PokeName, Priority: PokePriority,
		Occasions: []types.Occasion{private, {Channel: "qq", Scene: types.SceneGroup}},
		DefaultOn: []types.Occasion{private},
		Triggers:  []string{types.NoticePoke},
		Intercept: p.intercept,
	}
}

func (p *Poke) intercept(ctx context.Context, b types.Batch) types.Verdict {
	n := b.Notice
	if n == nil || n.Kind != types.NoticePoke {
		return types.Passed("不是戳一戳")
	}
	// 只回戳她的人：被戳的是她的某一个号（Mine）。不看"收到通知的号"：她在同一个群里有两个号时，两个号各收到一份同样的通知，
	// 入库一站只把先到的那份交给链，先到的恰好不是被戳的那个号，就会漏掉。同群另一个角色的号收到的同一条通知，被戳的不是它的号，不会回；
	// 她自己戳出去的回声（戳的是别人）和她自己的号戳她（包括自己戳自己）也都不回
	mine := func(id string) bool { return id == n.Account || slices.Contains(b.Mine, id) }
	if !mine(n.Target) {
		return types.Passed("戳的不是她")
	}
	if mine(n.Actor) {
		return types.Passed("是她自己戳的")
	}
	if p.cfg.IsBot != nil && p.cfg.IsBot(b.Conv.Channel, n.Actor) {
		return types.Passed("戳她的 " + n.Actor + " 是机器人，不戳回")
	}

	personKey := b.Character + "\x00" + b.Conv.String() + "\x00" + n.Actor
	convKey := b.Character + "\x00" + b.Conv.String()
	if why := p.limited(personKey, convKey, b.Now); why != "" {
		return types.Passed("戳她的 " + n.Actor + "：" + why)
	}

	planID, err := p.cfg.Out.Send(ctx, b.Character, types.OriginAutomationPrefix+PokeName, b.Conv,
		[]types.Part{{Kind: types.PartPoke, Ref: n.Actor}})
	if err != nil {
		// 没发成（渠道断开、发送检查没过、档位变了）：不记冷却，也不算接了。戳一戳本来就不交给闸门，这里没有"往下走"的后果
		return types.Passed("戳她的 " + n.Actor + "：想戳回去，没发出去：" + err.Error())
	}
	p.record(personKey, convKey, b.Now)
	return types.Took(fmt.Sprintf("戳她的 %s，戳回去了，计划 %s", n.Actor, planID))
}

// limited：被哪一道限流挡住了；没有就是空串
func (p *Poke) limited(personKey, convKey string, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.byPerson[personKey]; ok {
		if since := now.Sub(last); since >= 0 && since < p.cfg.Cooldown {
			left := p.cfg.Cooldown - since
			return fmt.Sprintf("同一个人 %d 秒内只戳回一次（还要等 %d 秒）", int(p.cfg.Cooldown/time.Second), int((left+time.Second-1)/time.Second))
		}
	}
	recent := 0
	for _, t := range p.byConv[convKey] {
		if since := now.Sub(t); since >= 0 && since < pokeWindow {
			recent++
		}
	}
	if recent >= p.cfg.PerMinute {
		return fmt.Sprintf("这个会话一分钟内已经戳回 %d 次了", recent)
	}
	return ""
}

// record 记下一次戳回去，顺手把过了期的清掉（状态不会随着戳过她的人一直长）
func (p *Poke) record(personKey, convKey string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byPerson[personKey] = now
	times := p.byConv[convKey]
	kept := times[:0]
	for _, t := range times {
		if since := now.Sub(t); since < 0 || since < pokeWindow {
			kept = append(kept, t)
		}
	}
	p.byConv[convKey] = append(kept, now)
	if len(p.byPerson) > 256 {
		for k, t := range p.byPerson {
			if now.Sub(t) >= p.cfg.Cooldown {
				delete(p.byPerson, k)
			}
		}
	}
}
