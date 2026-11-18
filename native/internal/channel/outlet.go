package channel

// 出口（Outlet）是 types.Outbox 的实现，出去的路径的最后一站（规格-渠道中间件 第十节，第十五节第 2 步）：
// 插件和回合要让适配器发消息，只有这一个入口。收到要发的几段以后，它：
//   - 定账号（私聊是会话串里她的号，群里是这个角色绑的第一个号，ADR-007），定档位（说话 / 试运行 / 只记录）；
//   - 把她这句话的 events、发送计划、各段（待发）在一个事务里落库（10.1 第 7 步）——先落库后发送，崩了以后留下的是"发送中"，不是"没记录"；
//   - 排进这个会话的出站队列：同一会话的出站一条队列，段与段之间不插别的计划的段，不同会话互不等待（10.5）；
//   - 一段一段发：每段发之前停一会儿（从上一段到终态算起），回执分确认 / 明确失败 / 结果未知三种（10.4）。
//     明确失败最多发 3 次、隔 1 秒；结果未知不重发（协议端可能已经发了，重发就是两遍）；
//     一段没发出去（用完重试，或者结果未知），后面还没发的段都标已中止，不照发。
//   - 回声认领：协议端把我们发的消息推回来时，在入库一站存它之前认成那一段（ClaimEcho，Intake.Config.Echo 调它），
//     不然它会被存成"她的号上别人发的"。
//
// 本文件是入口、校验和关闭；一段怎么发在 outlet_run.go，回声认领在 outlet_echo.go。
//
// 关闭和重启（10.6）：Close 先不收新的、排着的段不发、等在发的那一段的回执最多 5 秒，之后封库——封库以后迟到的回执、回声、
// 计时器一律什么也不写（清单④）。NewOutlet 启动时清理上次没关干净的：发送中的转结果未知（可能已经发出去了，不重发），待发的转已中止（不续发）。
//
// 回合裁决负责普通文字的守卫、保险丝、拆条和改写；出口检查引用文字和转发节点，通道门接 OutletConfig.Check。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/kernel"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
	"sumika-native/internal/taskrun"
	"sumika-native/internal/turn"
)

// 档位：OutletConfig.Mode 返回的值。群的档位表（gate_modes）是第 5 步，这里只认这三个值
const (
	ModeSpeak  = "speak"   // 说话：真发
	ModeDryRun = "dry-run" // 试运行：写库（origin 记 automation:dry-run）、不发
	ModeRecord = "record"  // 只记录：她不说话，出口不收。不认识的值也按这个办，宁可不发
)

var (
	// ErrNotSpeaking：这个会话现在是只记录档，出口不收
	ErrNotSpeaking = errors.New("这个会话现在是只记录档，不发消息")
	// ErrNoAdapter：这个渠道没有适配器
	ErrNoAdapter = errors.New("这个渠道没有适配器")
	// ErrSendCheck：发送检查（通道门、风控间隔）没过，这一批没写库也没进队列；后面跟着钩子给的原因
	ErrSendCheck = errors.New("发送检查没过")
	// errSealed：出口已经封库，什么也不写
	errSealed = errors.New("出口已封库")
)

// PartResult 是 OutletConfig.OnPart 的参数
type PartResult struct {
	Conv      types.Conversation
	Character string
	Origin    string // 这条计划的来源（链接展开等插件的来源名，或空）
	PlanID    string
	Index     int
	Confirmed bool // 为假就是明确失败
	// Class 说明失败的原因（taskrun.ClassUnsupported、ClassRejected 或 ClassRetryExhausted）；确认的、回声认领的是空
	Class string
}

// MaxAttempts：一段最多发几次（第一次加两次重试，10.4）
const MaxAttempts = 3

// Pace 是发送的节奏（10.2）。零值的项用 Bun 的现值（qq.ts:750）
type Pace struct {
	PerChar time.Duration // 每个字停多久（给第二段起的文字段，发之前停）
	Max     time.Duration // 一次停顿的上限
	Retry   time.Duration // 明确失败以后隔多久重试
}

// OutletConfig 是出口的配置。时限的零值用括号里的默认
type OutletConfig struct {
	// Stores 是按渠道取库的那一组（ADR-199）：计划写进会话所在平台的那个库（出站计划和事件在同一个库，AppendPlan 的事务才成立）；
	// 启动和关闭时没发完的计划对每个平台库各扫一遍（Each）
	Stores store.Stores
	Roster Roster       // 谁是哪个角色的号；出口按它定账号
	Seen   SeenNotifier // nil 表示尚未接入内核
	// Adapters 是各渠道的适配器；同一个渠道名出现两次启动即报错（同一个出口不能绑两个适配器）
	Adapters []types.Adapter
	// Mode 说一个会话现在是什么档；nil 就是都说话。每次收到要发的、每一段发之前都问一次：
	// 停顿中途切档，后面的段就不发了（10.6）。返回的值不认识按只记录办
	Mode func(character, origin string, conv types.Conversation) string
	// Check 是发送检查（通道门、风控间隔，第 7 步挂）：返回 error 这一批就不进队列、不写库。试运行不查；
	// nil 就是不查。panic 当成没过
	Check func(characterID, origin string, conv types.Conversation, parts []types.Part) error
	// GuardFor 给引用文字和合并转发取同一份记忆守卫；未接入时拒绝这些动作。
	GuardFor func(character string) Guard
	// UnguardedOrigins 里的来源不过记忆守卫：链接展开发的是网页原文、不是她的话（ADR-312）。只放插件来源，不放 character。
	UnguardedOrigins map[string]bool
	// OnPart 在一段有了明确的结果时调：回包确认了，或明确失败且用完重试（适配器说不支持也算）。通道门靠它数连着几段发不出去（gate.go 的 PartEnded）。
	// 结果未知、已中止不调；回声确认也调，每段恰好一次。回调不要阻塞；panic 当没调
	OnPart func(PartResult)
	// Degrade 把每一段的结局记进 task_runs（成功记耗时，不支持、重试用完、结果未知记原因类别）；nil 就不记（taskrun.Degrader 为 nil 时空操作）
	Degrade *taskrun.Degrader
	Pace    Pace
	Clock   types.Clock // nil 用系统的钟
	// Selfcheck 是运行自检的计数器：一段按"结果未知"收场时往里数一次；nil 不数。只数，不改任何路径的行为
	Selfcheck *selfcheck.Counters
	// Logf 记日志；nil 用 log.Printf。日志里只放会话、计划编号、结果，不放消息原文（规格 第十七节）
	Logf func(format string, args ...any)
	// Errorf 记 ERROR（这一段不发、数据会丢）；nil 时用 Logf
	Errorf func(format string, args ...any)

	SendTimeout time.Duration // 一次 Send 最多等多久，到了按结果未知办（30 秒；适配器自己有超时，这是兜底）
	EchoWindow  time.Duration // 回声认领的窗口，从这一段第一次调 Send 起算（5 分钟，10.4）
	CloseWait   time.Duration // 关闭时等在发的那一段的回执（5 秒，10.6）
}

func (c *OutletConfig) defaults() {
	if c.Pace.PerChar == 0 {
		c.Pace.PerChar = 40 * time.Millisecond
	}
	if c.Pace.Max == 0 {
		c.Pace.Max = 2500 * time.Millisecond
	}
	if c.Pace.Retry == 0 {
		c.Pace.Retry = time.Second
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = 30 * time.Second
	}
	if c.EchoWindow == 0 {
		c.EchoWindow = 5 * time.Minute
	}
	if c.CloseWait == 0 {
		c.CloseWait = 5 * time.Second
	}
	if c.Clock == nil {
		c.Clock = SystemClock{}
	}
}

// SystemClock 是真的钟
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }
func (SystemClock) NewTimer(d time.Duration) types.Timer {
	return sysTimer{time.NewTimer(d)}
}

type sysTimer struct{ t *time.Timer }

func (s sysTimer) C() <-chan time.Time { return s.t.C }
func (s sysTimer) Stop() bool          { return s.t.Stop() }

// leakGrace：放弃等回执以后，再等多久让还卡着的发送 goroutine 退出。过了就不等了：封库以后它不会再写任何东西，
// 只是适配器没守"ctx 取消就返回"的规矩，这个 goroutine 漏着（契约测试会查这一条）。真时间，不走 Clock
var leakGrace = 2 * time.Second

// Outlet 是出口
type Outlet struct {
	cfg      OutletConfig
	clock    types.Clock
	adapters map[string]types.Adapter
	caps     map[string]types.Caps
	q        *lanes // 按（渠道、会话）排的出站队列：一条计划是一件活

	// gate：每次碰库拿读锁，封库拿写锁。封库以后任何迟到的东西（回执、回声、计时器）拿到读锁也只会看到 sealed，什么也不写
	gate   sync.RWMutex
	sealed bool

	ctx       context.Context // 所有 Send、Poke 的父 ctx；放弃等回执时取消
	cancel    context.CancelFunc
	closing   chan struct{} // Close 开始时关闭：不收新的，各处的等待立刻醒
	giveUp    chan struct{} // 等回执的时间到了关闭：还在等回执的发送不再等
	closeOnce sync.Once

	mu       sync.Mutex
	resultMu sync.Mutex          // 串行认领与结果通知，避免回包和回声各通知一次
	flights  map[string]*flight  // 正在发的段（段号 → 通知口），回声认领成功时叫醒它
	plans    map[string]*planRun // 包括未 Start 的 Held，关闭时也要交还资源。
	origins  map[string]*originPermit
	echoes   echoBook    // 认领到的回声（段号 → 回声的样子）、等某一段回声的人、按来源登记的认领回调
	sources  planSources // 计划号 → 它是为哪条源消息发的（只在内存里，撤回补发的宿主门槛用，outlet_source.go）
}

var _ types.Outbox = (*Outlet)(nil)

// NewOutlet 造出口。名册不合法、适配器重名都启动即报错；上次没关干净的计划在这里清理（10.6"重启"）
func NewOutlet(cfg OutletConfig) (*Outlet, error) {
	if cfg.Stores == nil {
		return nil, errors.New("没有库")
	}
	if err := cfg.Roster.validate(); err != nil {
		return nil, fmt.Errorf("名册不合法：%w", err)
	}
	cfg.defaults()
	o := &Outlet{
		cfg: cfg, clock: cfg.Clock, adapters: map[string]types.Adapter{}, caps: map[string]types.Caps{},
		q: newLanes(), closing: make(chan struct{}), giveUp: make(chan struct{}), flights: map[string]*flight{}, plans: map[string]*planRun{},
		origins: map[string]*originPermit{},
	}
	for _, a := range cfg.Adapters {
		if a == nil {
			return nil, errors.New("适配器是空的")
		}
		if _, dup := o.adapters[a.Name()]; dup {
			return nil, fmt.Errorf("渠道 %q 绑了两个适配器：同一个出口只能有一个", a.Name())
		}
		o.adapters[a.Name()] = a
		o.caps[a.Name()] = a.Caps() // 启动时读一次，之后不变
	}
	o.ctx, o.cancel = context.WithCancel(context.Background())

	// 一个库都拿不到（这组库已经关了）就不启动：不然上次没发完的计划没人清理，也没有一行日志
	each := cfg.Stores.Each()
	if len(each) == 0 {
		o.cancel()
		return nil, errors.New("没有能用的库（这组库已经关了？），出口不启动")
	}
	var sweepErrs []error
	for _, nd := range each {
		if nd.Store.ReadOnly() { // 别的进程写的平台库（local 交给 Bun 时）：这里只读，也没有该渠道的发送计划要清
			continue
		}
		sw, err := nd.Store.Sweep("上次没关干净：重启时这一段还在发送中，不知道发出去没有，没有重发",
			"上次没关干净：重启后不续发几分钟前的话", o.nowMS())
		if err != nil {
			sweepErrs = append(sweepErrs, fmt.Errorf("平台 %s：%w", nd.Platform, err))
			continue
		}
		if sw.Plans > 0 {
			o.logf("启动时清理了 %d 条上次没结束的发送计划（平台 %s）：%d 段转成结果未知，%d 段转成已中止（都没有重发）",
				sw.Plans, nd.Platform, sw.Unknown, sw.Aborted)
		}
	}
	if err := errors.Join(sweepErrs...); err != nil {
		o.cancel()
		return nil, fmt.Errorf("清理上次没发完的计划：%w", err)
	}
	return o, nil
}

func (o *Outlet) logf(format string, args ...any) {
	if o.cfg.Logf != nil {
		o.cfg.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (o *Outlet) errorf(format string, args ...any) {
	if o.cfg.Errorf != nil {
		o.cfg.Errorf(format, args...)
		return
	}
	o.logf(format, args...)
}

func (o *Outlet) nowMS() int64 { return o.clock.Now().UnixMilli() }

func (o *Outlet) isClosing() bool {
	select {
	case <-o.closing:
		return true
	default:
		return false
	}
}

// db 做一次库操作：封库以后返回 errSealed，什么也不做。fn 里不要再调 db（读锁不可重入，有人在等写锁时会死锁）
func (o *Outlet) db(fn func() error) error {
	o.gate.RLock()
	defer o.gate.RUnlock()
	if o.sealed {
		return errSealed
	}
	return fn()
}

// modeOf 问这个会话现在是什么档；钩子 panic 了按只记录办（出口宁可不发）
func (o *Outlet) modeOf(character, origin string, conv types.Conversation) (mode string) {
	if o.cfg.Mode == nil {
		return ModeSpeak
	}
	defer func() {
		if r := recover(); r != nil {
			o.logf("会话 %s 查档位时出了意外：%v，按只记录办", conv.String(), r)
			mode = ModeRecord
		}
	}()
	return o.cfg.Mode(character, origin, conv)
}

// route 是这一批用哪个适配器、哪个号发
type route struct {
	ad      types.Adapter
	caps    types.Caps
	account string
}

// route 定账号：私聊是会话串里她的号（得是绑给这个角色的，角色不能用别人的号发）；群里是这个角色在这个渠道绑的第一个号（Bun 的 accountOfTurn）
func (o *Outlet) route(characterID string, conv types.Conversation) (route, error) {
	if err := conv.Valid(); err != nil {
		return route{}, fmt.Errorf("会话不合法：%w", err)
	}
	ad, ok := o.adapters[conv.Channel]
	if !ok {
		return route{}, fmt.Errorf("%w：%s", ErrNoAdapter, conv.Channel)
	}
	r := route{ad: ad, caps: o.caps[conv.Channel]}
	if conv.Scene == types.ScenePrivate {
		a, ok := o.cfg.Roster.account(conv.Channel, conv.Self)
		if !ok || a.Character != characterID {
			return route{}, fmt.Errorf("号 %s:%s 没有绑给角色 %s，不能用它发私聊", conv.Channel, conv.Self, characterID)
		}
		r.account = a.ID
		return r, nil
	}
	for _, a := range o.cfg.Roster.Accounts {
		if a.Channel == conv.Channel && a.Character == characterID {
			r.account = a.ID
			return r, nil
		}
	}
	return route{}, fmt.Errorf("角色 %s 在渠道 %s 上没有绑号", characterID, conv.Channel)
}

// checkOrigin：调用方只能填 character 和 automation:<插件名>；automation:dry-run 由出口按档位决定，不是调用方填的（10.5"来源"）
func checkOrigin(origin string) error {
	switch {
	case origin == types.OriginCharacter:
		return nil
	case origin == types.OriginDryRun:
		return fmt.Errorf("来源 %s 由出口按会话的档位决定，调用方不能填", types.OriginDryRun)
	case strings.HasPrefix(origin, types.OriginAutomationPrefix) && len(origin) > len(types.OriginAutomationPrefix):
		return nil
	}
	return fmt.Errorf("来源 %q 不认识：只能是 character 或 automation:<插件名>", origin)
}

// checkParts 查要发的几段：种类认识，该有的字段有，适配器的 Caps 里没有的动作不收（Poke 只有 Caps.Poke 为真才会调，types.Adapter）
func checkParts(caps types.Caps, parts []types.Part) error {
	if len(parts) == 0 {
		return errors.New("没有要发的段")
	}
	for i, p := range parts {
		if p.Kind != types.PartPoke && !caps.Supports(p) {
			return fmt.Errorf("第 %d 段：%w", i, types.ErrUnsupported)
		}
		switch p.Kind {
		case types.PartMessage:
			if err := types.ValidateBody(p.Body); err != nil {
				return fmt.Errorf("第 %d 段：%w", i, err)
			}
		case types.PartForward:
			if len(p.Nodes) == 0 || len(p.Nodes) > 80 {
				return fmt.Errorf("第 %d 段转发节点数量不合法", i)
			}
			for j, n := range p.Nodes {
				if strings.TrimSpace(n.Name) == "" {
					return fmt.Errorf("第 %d 段转发第 %d 个节点没有显示名", i, j)
				}
				if err := types.ValidateBody(n.Body); err != nil {
					return fmt.Errorf("第 %d 段节点 %d：%w", i, j, err)
				}
			}
		case types.PartReact:
			if p.Ref == "" || p.EmojiID == "" {
				return fmt.Errorf("第 %d 段回应没有消息号或表情编号", i)
			}
		case types.PartPoke:
			if !caps.Poke {
				return fmt.Errorf("第 %d 段是戳一戳，这个渠道戳不了", i)
			}
			if p.Ref == "" {
				return fmt.Errorf("第 %d 段是戳一戳，没有说戳谁", i)
			}
		default:
			return fmt.Errorf("第 %d 段的种类 %q 不认识", i, p.Kind)
		}
	}
	return nil
}

// joiner 是几条文字段连成 events.text 的方式：空行。拆条的口径就是"空行一条"（Bun 按空行拆，qq.ts splitMessage），
// 库里的这句话和发出去的几条因此对得上（ADR-114）；界面想分气泡就按 segments_json 里的 parts
const joiner = "\n\n"

// eventText 是历史展示的语义投影，不包含资源内容或本机文件名。
func eventText(parts []types.Part) string {
	var out []string
	appendBody := func(body []types.BodySegment) {
		for _, s := range body {
			if s.Kind == types.BodyText {
				out = append(out, s.Text)
			} else if s.Semantic != "" {
				out = append(out, s.Semantic)
			} else {
				out = append(out, "["+s.Kind+"]")
			}
		}
	}
	for _, p := range parts {
		appendBody(p.Body)
		for _, n := range p.Nodes {
			appendBody(n.Body)
		}
	}
	return strings.Join(out, joiner)
}

type partJSON struct {
	Kind    string              `json:"kind"`
	Body    []types.BodySegment `json:"body,omitempty"`
	Ref     string              `json:"ref,omitempty"`
	Quote   string              `json:"quote,omitempty"`
	Nodes   []types.ForwardNode `json:"nodes,omitempty"`
	EmojiID string              `json:"emoji_id,omitempty"`
	Unset   bool                `json:"unset,omitempty"`
}

// pauseFor：第二段起的文字段，发之前停 min(上限, 字数 × 每字多久)（qq.ts:762）；第一段、别的种类不停（戳回去不占停顿，10.5）
func (o *Outlet) pauseFor(i int, p types.Part) time.Duration {
	if i == 0 || p.Kind != types.PartMessage {
		return 0
	}
	d := time.Duration(utf8.RuneCountInString(types.BodyTextOf(p.Body))) * o.cfg.Pace.PerChar
	return min(d, o.cfg.Pace.Max)
}

// partID：段号，由（计划，序号）派生，适配器的回执照抄它
func partID(planID string, idx int) string { return fmt.Sprintf("%s:%d", planID, idx) }

// planRun 是一条要发的计划在内存里的样子（库里的计划是真身，这里是发送用的）
type planRun struct {
	identity  kernel.Invocation
	id        string
	character string
	origin    string
	conv      types.Conversation
	rt        route
	db        *store.Store    // 这条计划在哪个平台的库（按 conv.Channel 取的一次）：计划和它的事件、段都在这个库里
	parts     []types.Part    // ID、Conv 已填
	pauses    []time.Duration // 每一段发之前的停顿，和库里存的 pause_ms 是同一个值（取整到毫秒）
	resources []*types.MediaResource
	calls     sync.WaitGroup
	finished  sync.Once
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool // o.mu 保护；Close 只直接释放未 Start 的 Held。
}

// Send 实现 types.Outbox：校验、定账号和档位，计划落库，说话档的排进队列。不等发完；
// 返回 error 是这一批没有进队列（也没写库）。origin 只能是 character 或 automation:<插件名>
func (o *Outlet) Send(ctx context.Context, characterID, origin string, conv types.Conversation, parts []types.Part) (string, error) {
	h, err := o.SendHeld(ctx, characterID, origin, conv, parts)
	if err != nil {
		return "", err
	}
	if err := h.Start(); err != nil {
		return "", err
	}
	return h.PlanID, nil
}

// Held 是一条已经落库、还没开始发的计划（SendHeld 的结果）
type Held struct {
	Identity kernel.Invocation
	PlanID   string
	// EventSeq 是她这句话写进 events 的那一行的 seq；没写事件（只有戳一戳）是 0
	EventSeq int64
	// DryRun：试运行档，写了库、不会发（events 那一行的 origin 是 automation:dry-run）
	DryRun bool
	// Done 在计划终态且持有的媒体资源释放后关闭，供生产者施加背压。
	Done  <-chan struct{}
	start func() error
}

// Start 让这条计划进发送队列。试运行的计划不用发，Start 什么也不做。每条只调一次；关闭了返回 ErrClosed（段已转成已中止）
func (h Held) Start() error {
	if h.start == nil {
		return nil
	}
	return h.start()
}

// SendHeld 同 Send，只是计划落库以后先不排进发送队列，等调用方调 Held.Start：回合定了以后的裁决要在写库和发出去之间，
// 把写进库的几行交回内核（Hub.Commit），交回了才开始发（规格-渠道中间件 第十节 10.1 第 7 步）。
// 同一个会话里先落库的先 Start 就先发；落库和 Start 之间出口关闭，Start 返回 ErrClosed，这一条的段由关闭时的清理转成已中止
func (o *Outlet) SendHeld(ctx context.Context, characterID, origin string, conv types.Conversation, parts []types.Part) (Held, error) {
	return o.sendHeld(ctx, characterID, origin, conv, parts, nil, nil)
}

// SendOwned 把资源持有权交给同一出口，失败也由出口释放。
func (o *Outlet) SendOwned(ctx context.Context, characterID, origin string, conv types.Conversation, parts []types.Part, resources []*types.MediaResource) (string, error) {
	h, err := o.sendHeld(ctx, characterID, origin, conv, parts, resources, nil)
	if err != nil {
		return "", err
	}
	if err := h.Start(); err != nil {
		return "", err
	}
	return h.PlanID, nil
}

func (o *Outlet) sendHeld(ctx context.Context, characterID, origin string, conv types.Conversation, parts []types.Part, resources []*types.MediaResource, permit *originPermit) (Held, error) {
	handed := false
	defer func() {
		if !handed {
			o.releaseResources(resources)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Held{}, err
	}
	if o.isClosing() {
		return Held{}, ErrClosed
	}
	if characterID == "" {
		return Held{}, errors.New("没有说话的角色")
	}
	if err := checkOrigin(origin); err != nil {
		return Held{}, err
	}
	o.mu.Lock()
	required, registered := o.origins[origin]
	o.mu.Unlock()
	if registered && permit != required {
		return Held{}, errors.New("必须使用宿主注册的自动化出口")
	}
	rt, err := o.route(characterID, conv)
	if err != nil {
		return Held{}, err
	}
	// 调用方交出后不能再改正在检查、排队的节点。
	parts = slices.Clone(parts)
	for i := range parts {
		parts[i].Conv = conv
		parts[i].Body = slices.Clone(parts[i].Body)
		parts[i].Nodes = slices.Clone(parts[i].Nodes)
		for j := range parts[i].Nodes {
			parts[i].Nodes[j].Body = slices.Clone(parts[i].Nodes[j].Body)
		}
	}
	if err := checkParts(rt.caps, parts); err != nil {
		return Held{}, err
	}
	digestImages(parts)
	owned := make(map[*types.MediaResource]bool, len(resources))
	for _, resource := range resources {
		owned[resource] = true
	}
	checkOwned := func(body []types.BodySegment) error {
		for _, s := range body {
			if s.Media != nil && !owned[s.Media] {
				return errors.New("媒体资源未交给出口持有")
			}
		}
		return nil
	}
	for _, p := range parts {
		if err := checkOwned(p.Body); err != nil {
			return Held{}, err
		}
		for _, n := range p.Nodes {
			if err := checkOwned(n.Body); err != nil {
				return Held{}, err
			}
		}
	}
	// 计划和它的事件必须进同一个库（AppendPlan 一个事务里写三样），所以按会话的渠道取，不按号取
	db, err := o.cfg.Stores.For(conv.Channel)
	if err != nil {
		return Held{}, fmt.Errorf("会话 %s 取库失败：%w", conv.String(), err)
	}

	kind := store.PlanKindSend
	switch m := o.modeOf(characterID, origin, conv); m {
	case ModeSpeak:
	case ModeDryRun:
		kind = store.PlanKindDryRun
	default:
		return Held{}, fmt.Errorf("%w（会话 %s，档位 %q）", ErrNotSpeaking, conv.String(), m)
	}
	if kind == store.PlanKindSend {
		if err := o.check(characterID, origin, conv, parts); err != nil {
			o.cfg.Degrade.Degrade(taskrun.ModuleSend, taskrun.ClassSendCheck, err, taskrun.Opts{Outcome: taskrun.OutcomeSkipped})
			return Held{}, fmt.Errorf("%w：%w", ErrSendCheck, err)
		}
	}
	if !o.cfg.UnguardedOrigins[origin] || origin == types.OriginCharacter {
		if err := o.guardActions(ctx, characterID, conv, parts); err != nil {
			return Held{}, err
		}
	}

	now := o.clock.Now()
	pr := &planRun{id: store.NewID(now), identity: kernel.InvocationFromContext(ctx), character: characterID, origin: origin, conv: conv, rt: rt, db: db, done: make(chan struct{})}
	pr.ctx, pr.cancel = context.WithCancel(o.ctx)
	pr.resources = resources
	defer func() {
		if !handed {
			pr.cancel()
		}
	}()
	np := store.NewPlan{
		ID: pr.id, CharacterID: characterID, Conversation: conv.String(), Account: rt.account, Origin: origin, Kind: kind,
		Observers: []string{characterID}, At: now.UnixMilli(),
	}
	js := make([]partJSON, len(parts))
	for i, p := range parts {
		p.ID, p.Conv = partID(pr.id, i), conv // 段号和会话由出口填
		pr.parts = append(pr.parts, p)
		pause := o.pauseFor(i, p).Truncate(time.Millisecond)
		pr.pauses = append(pr.pauses, pause)
		text, ref := "", p.Ref
		switch p.Kind {
		case types.PartMessage:
			body, _ := json.Marshal(p.Body)
			text, ref = string(body), p.Quote
		case types.PartForward:
			body, _ := json.Marshal(p.Nodes)
			text = string(body)
		case types.PartReact:
			body, _ := json.Marshal(struct {
				MessageID string `json:"message_id"`
				EmojiID   string `json:"emoji_id"`
				Unset     bool   `json:"unset,omitempty"`
			}{p.Ref, p.EmojiID, p.Unset})
			ref = string(body)
		}
		np.Parts = append(np.Parts, store.NewPart{Kind: p.Kind, Text: text, Ref: ref, PauseMS: int(pause / time.Millisecond)})
		js[i] = partJSON{Kind: p.Kind, Body: p.Body, Ref: p.Ref, Quote: p.Quote, Nodes: p.Nodes, EmojiID: p.EmojiID, Unset: p.Unset}
	}
	// 只有戳一戳或回应的计划不写 events：它们不产生新消息。
	if text := eventText(parts); text != "" {
		replyTo := o.replyTarget(db, conv, parts)
		evOrigin := origin
		if kind == store.PlanKindDryRun {
			evOrigin = types.OriginDryRun // 试运行的那一行记成没说出口的，历史读取会跳过它（history.notSaid）
		}
		np.Event = &store.NewEvent{
			Channel: conv.Channel, Conversation: conv.String(), ActorKind: ActorCharacter, ActorID: characterID, Origin: evOrigin,
			Kind: "message", Text: text, Segments: struct {
				Parts []partJSON `json:"parts"`
			}{js}, ReplyTo: replyTo, CreatedAt: now.UnixMilli(),
		}
	}
	var eventSeq int64
	err = o.db(func() (e error) { eventSeq, e = db.AppendPlan(np); return })
	switch {
	case errors.Is(err, errSealed):
		return Held{}, ErrClosed
	case err != nil:
		return Held{}, fmt.Errorf("计划落库失败：%w", err)
	}
	if kind == store.PlanKindSend {
		o.sources.put(pr.id, sourceSeqOf(parts), now)
	}
	if pr.identity.RunID != "" {
		if err := o.linkPlan(ctx, pr); err != nil {
			return Held{}, err
		}
	}
	// 试运行写进库的那一行（origin 是 automation:dry-run）她下一轮看不到，不通知
	if eventSeq > 0 && kind != store.PlanKindDryRun && o.cfg.Seen != nil && conv.Channel != "console" {
		o.cfg.Seen.NotifySeen(turn.Seen{CharacterID: characterID, Conversation: conv.String(), Seq: eventSeq, FromSelf: origin == types.OriginCharacter})
	}
	if kind == store.PlanKindDryRun {
		o.logf("会话 %s 试运行：计划 %s 写了库，没有发（%d 段）", conv.String(), pr.id, len(parts))
		close(pr.done)
		return Held{Identity: pr.identity, PlanID: pr.id, EventSeq: eventSeq, DryRun: true, Done: pr.done}, nil
	}

	o.mu.Lock()
	if o.isClosing() {
		o.mu.Unlock()
		_ = o.db(func() error { _, e := db.AbortPlanParts(pr.id, "渠道关闭，没发", o.nowMS()); return e })
		return Held{}, ErrClosed
	}
	o.plans[pr.id] = pr
	handed = true
	o.mu.Unlock()
	var startOnce sync.Once
	var startErr error
	return Held{Identity: pr.identity, PlanID: pr.id, EventSeq: eventSeq, Done: pr.done, start: func() error {
		startOnce.Do(func() {
			o.mu.Lock()
			closed := o.isClosing()
			if !closed {
				pr.started = true
			}
			o.mu.Unlock()
			if closed {
				o.finishPlan(pr)
				startErr = ErrClosed
				return
			}
			ok := o.q.submit(queueKey(conv), job{
				run:  func() { defer o.finishPlan(pr); o.runPlan(pr) },
				drop: func() { o.finishPlan(pr) },
			})
			if !ok {
				_ = o.db(func() error { _, e := db.AbortPlanParts(pr.id, "渠道关闭，没发", o.nowMS()); return e })
				o.finishPlan(pr)
				startErr = ErrClosed
			}
		})
		return startErr
	}}, nil
}

// check 调发送检查钩子，panic 当成没过
func (o *Outlet) check(characterID, origin string, conv types.Conversation, parts []types.Part) (err error) {
	if o.cfg.Check == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("检查时出了意外：%v", r)
		}
	}()
	return o.cfg.Check(characterID, origin, conv, parts)
}

// Record 记一条不发的计划：拦下（守卫、字数保险丝）、已打断、出错。没有段、不写 events、不存原文，reason 只写原因（10.1 第 7 步，10.3）。
// 不看档位：拦下、打断是发生过的事，哪个档都要记
func (o *Outlet) Record(ctx context.Context, characterID, origin string, conv types.Conversation, kind, reason string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if o.isClosing() {
		return "", ErrClosed
	}
	if err := checkOrigin(origin); err != nil {
		return "", err
	}
	switch kind {
	case store.PlanKindBlocked, store.PlanKindInterrupted, store.PlanKindError:
	default:
		return "", fmt.Errorf("Record 只记拦下、已打断、出错，不记 %q", kind)
	}
	rt, err := o.route(characterID, conv)
	if err != nil {
		return "", err
	}
	db, err := o.cfg.Stores.For(conv.Channel)
	if err != nil {
		return "", fmt.Errorf("会话 %s 取库失败：%w", conv.String(), err)
	}
	now := o.clock.Now()
	np := store.NewPlan{
		ID: store.NewID(now), CharacterID: characterID, Conversation: conv.String(), Account: rt.account, Origin: origin,
		Kind: kind, Reason: reason, At: now.UnixMilli(),
	}
	err = o.db(func() error { _, e := db.AppendPlan(np); return e })
	switch {
	case errors.Is(err, errSealed):
		return "", ErrClosed
	case err != nil:
		return "", fmt.Errorf("计划落库失败：%w", err)
	}
	return np.ID, nil
}

// Plan 读一条计划和它的段（界面、统计、测试用）。计划和它的事件在会话所在平台的那个库里，所以带上会话取库；
// 计划号只在那个库里找得到，别的库里没有这一行
func (o *Outlet) Plan(id string, conv types.Conversation) (store.Plan, error) {
	db, err := o.cfg.Stores.For(conv.Channel)
	if err != nil {
		return store.Plan{}, fmt.Errorf("会话 %s 取库失败：%w", conv.String(), err)
	}
	var p store.Plan
	err = o.db(func() (e error) { p, e = db.GetPlan(id); return })
	if errors.Is(err, errSealed) {
		return store.Plan{}, ErrClosed
	}
	return p, err
}

// Close 关闭出口（10.6"关闭渠道"）。顺序：
//  1. 不再收新的（Send 返回 ErrClosed），各处的等待（停顿、重试间隔）立刻醒，排着没开始的计划不再开始；
//  2. 正在发送的那一段等它的回执，最多 CloseWait；
//  3. 封库：还没发的段转已中止，等不到回执的转结果未知，之后迟到的回执、回声、计时器什么也不写。
//
// 返回以后可以关库。调用方先停适配器和入库一站（让回声不再进来），再 Close 出口，最后关库
func (o *Outlet) Close() {
	o.closeOnce.Do(func() {
		close(o.closing)
		done := make(chan struct{})
		go func() { o.q.close(); close(done) }()
		t := o.clock.NewTimer(o.cfg.CloseWait)
		select {
		case <-done:
			t.Stop()
		case <-t.C():
			// 回执等不到了：发送不再等，取消它的 ctx。这一段在封库时转成结果未知，回执以后再来也不写
			close(o.giveUp)
			o.cancel()
			select {
			case <-done:
			case <-time.After(leakGrace):
				o.logf("关闭出口时有发送卡着没退出：适配器没有在 ctx 取消后返回（已封库，它不会再写任何东西）")
			}
		}
		o.seal()
		o.cancel()
		o.mu.Lock()
		var held []*planRun
		for _, pr := range o.plans {
			if !pr.started {
				held = append(held, pr)
			}
		}
		o.mu.Unlock()
		for _, pr := range held {
			o.finishPlan(pr)
		}
	})
}

// seal 封库：先对每个平台的库做最后一次清理，再拦住以后的一切写
func (o *Outlet) seal() {
	o.gate.Lock()
	defer o.gate.Unlock()
	if o.sealed {
		return
	}
	each := o.cfg.Stores.Each()
	if len(each) == 0 {
		// 库组先关了（装配时该先关出口再关库）：这次没清理成，下次启动会再清理
		o.logf("关闭出口时没有能用的库（这组库已经先关了？），没发完的计划这次没清理（下次启动会再清理）")
	}
	for _, nd := range each {
		if nd.Store.ReadOnly() {
			continue
		}
		sw, err := nd.Store.Sweep("关闭时没等到回执，不知道发出去没有，没有重发", "渠道关闭，没发", o.nowMS())
		if err != nil {
			o.logf("关闭出口时清理平台 %s 没发完的计划失败：%v（下次启动会再清理）", nd.Platform, err)
		} else if sw.Plans > 0 {
			o.logf("关闭出口：平台 %s 有 %d 条计划没发完，%d 段转成结果未知，%d 段转成已中止", nd.Platform, sw.Plans, sw.Unknown, sw.Aborted)
		}
	}
	o.sealed = true
}

// replyTarget 找出这次发送回复的是库里哪一条事件，写进发出事件的 reply_to（界面的引用块只认它，不看段里的平台消息号）。
// 先认发起方给的源事件（SourceSeq，比如链接展开就是因为那条消息才发的），再按第一个带引用的消息段里的平台消息号反查；找不到就留空，不猜。
// 合并转发打头的计划（QQ 的合并转发带不了回复段，规格-链接发送改版 A5）线上没有引用，但它仍是对源事件的回应，入库照样按 SourceSeq 连上。
func (o *Outlet) replyTarget(db *store.Store, conv types.Conversation, parts []types.Part) *string {
	if len(parts) > 0 && parts[0].Kind == types.PartForward && parts[0].SourceSeq > 0 {
		if id, ok, err := db.EventIDBySeq(conv.String(), parts[0].SourceSeq); err == nil && ok {
			return &id
		}
		return nil
	}
	for _, p := range parts {
		if p.Kind != types.PartMessage || p.Quote == "" {
			continue
		}
		if p.SourceSeq > 0 {
			if id, ok, err := db.EventIDBySeq(conv.String(), p.SourceSeq); err == nil && ok {
				return &id
			}
		}
		if ref, ok, err := db.EventByPlatformID(conv.Channel, conv.String(), p.Quote); err == nil && ok {
			id := ref.ID
			return &id
		}
		return nil
	}
	return nil
}
