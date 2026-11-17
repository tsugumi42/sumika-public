package turn

// 插件接口（规格-Go后台 8.1、8.3 第 1 项）：插件是编进程序的 Go 代码，Setup 时在 Registry 上注册钩子，每个注册都返回撤销函数，
// 插件停掉时它的注册一起撤掉、还在路上的结果丢掉。现在有两种钩子：
//   - 拼参考信息（改写）：每个回合开头调一次，插话送到时再调一次；返回的几行只追加在末尾（缓存规则，ADR-022），按注册的先后排。
//     出错、超时只跳过这个插件这一次，别的照常。
//   - 回合定了（通知）：一个回合的所有步骤、插话都走完以后发一次。每个订阅者一条不限长的队列，按先后送、不丢；出错、超时记日志。
//   - 她看到的新事件（通知，同上的送法）：一条事件存进库、这个角色看得到。渠道中间件入库以后发（Hub.NotifySeen），
//     本地主窗口由内核在存主人说的、插话、她的回复时发。记忆的整理计时靠它（规格-记忆-v2 13.2.5：不挂回合定了）。
//   - 注册工具：声明、哪些听众能用、执行。审批的那一类只认 policy.Specs 里集中登记的，执行照样过内核的审批的关和总时限；
//     插件停掉时它的工具从清单里撤掉；当轮绑定原注册，同名重注册不能接替执行，还在跑的那次结果不要。
//   - 压缩前（等它做完一件事）：真要调模型压缩一个会话之前，同时调每个钩子，最多等 90 秒（记忆强制整理一次）；它可以说
//     "我只整理到第几条"，切点不越过那里。出错、超时记日志、照常压缩，不开放替换压缩结果（8.1"压缩前"、规格-记忆-v2 13.2.7）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// Plugin 是接进内核的一样东西
type Plugin interface {
	ID() string
	// Setup 注册钩子；返回的 cleanup 在插件停掉时调（可以是 nil）。出错时这个插件不启用，已经注册的撤掉
	Setup(r *Registry) (cleanup func(), err error)
}

// ContextIn 是拼参考信息时给插件看的：这一回合是谁、在哪、谁在听，这一批话，和这个实例里给这个插件的一小块存储
type ContextIn struct {
	timeline                         *timeline // 记每个插件花了多久；插件自己用不到
	Identity                         kernel.Invocation
	CharacterID, Conversation, Scope string
	Audience                         audience.Audience // Input 带来的听众，原样（含 OwnerAsked）
	Addressed                        bool              // 这一批是不是在叫她
	Text                             string            // 这一批话（插话时是插的那句）
	Steer                            bool              // 插话送到时的那一次
	ContextID                        string            // 宿主生成的上下文代次，重建或压缩换新
	Workspace                        Workspace         // 本回合冻结的工作区，QQ 为零值
	// State 是这个实例里这个插件自己的存储（记"给过哪几条"这类）：实例重建就是新的
	State map[string]any
}

// Contribution 是待接纳的参考信息；只有消息已经进实例，才同步调用 OnAccepted，此时不持内核锁。
// 无 Lines 的贡献记录用户输入的接纳；即使回合取消，只要输入仍追加进实例，就照常提交。
type Contribution struct {
	Lines []string
	// Recall 是 Lines 里这些记忆的结构化原样（和 Lines 同一次召回、同一顺序），给界面看召回相关程度；没有就不显示，不另算一遍
	Recall []RecallItem
	// Storable 声明这几行可以按档存库、重建时插回（规格-提示词前缀稳定第七节）；不声明的（默认）整块参考信息都不存。
	// 只有内容不随授权、信任、插件开关变、并且变了也能核对出来的才能声明：目前只有记忆召回，它的 Refs 重建时由 OnRefCheck 核对
	Storable bool
	// Refs 是 Lines 引用的记忆（ID 和给她看的样子的指纹），Storable 的贡献带；块存库时一起存，重建时核对，核对不过整块不插
	Refs       []MemoryRef
	OnAccepted func()
}

// MemoryRef 是参考信息块引用的一条记忆：ID 和它当时给她看的样子的指纹；Pinned 是它在块里是「要一直遵守的约定」那一段的（不是按这句话召回的）
type MemoryRef struct {
	ID, Hash string
	Pinned   bool
}

// RestoreIn 是重建以后实例里的存储被清空了，内核把插回去的块引用的记忆交给插件，让它恢复「这个实例里已经给过什么」：
// 不恢复的话插件会把已经在历史里的记忆再给一遍，每重建一次历史里就多一份。State 是这个插件在新实例里的存储，Blocks 是每个插回的块引用的记忆，按块的先后
type RestoreIn struct {
	CharacterID, Conversation string
	State                     map[string]any
	Blocks                    [][]MemoryRef
}

// RestoreFn 恢复插件在重建出来的实例里的存储。没有这个钩子的插件什么都不做（照旧当没给过）；出错只记日志
type RestoreFn func(ctx context.Context, in RestoreIn) error

// RefCheckIn 是重建时问「这些记忆现在还是原样吗」：Audience 是重建这一轮的听众，Now 是重建的这一刻（过期随时间变）
type RefCheckIn struct {
	CharacterID, Conversation string
	Audience                  audience.Audience
	Now                       time.Time
	Refs                      []MemoryRef
}

// RefCheckFn 回答 RefCheckIn：全部还是原样才返回真；出错、读不出来、不确定都当假（失败关闭，块整块不插）
type RefCheckFn func(ctx context.Context, in RefCheckIn) (bool, error)

// ContextFn 没有变化时返回 nil；出错、超时或停用的贡献不接纳，取消时只接纳无 Lines 的输入贡献。
type ContextFn func(ctx context.Context, in ContextIn) (*Contribution, error)

// Settled 是一个回合定了：Outcome 是 done（说完）、stopped（被停下、被打断）、error（出错）
type Settled struct {
	Identity                                 kernel.Invocation
	TurnID, CharacterID, Conversation, Scope string
	Outcome                                  string
	Err                                      error
	// Said 是这一回合每一步说出口的那句（清理以后的字，没字的步不算），按先后；提前出错的是空的。
	// 渠道中间件拿它裁决、改写、写库（ADR-184 第 2 条），连同这一轮的听众、场合段
	Said     []Said
	Audience audience.Audience
	Scenes   []string
	// Answer 是这一轮最后存进库的那句话，Tools 是调过的工具摘要，Tokens 是这一轮各步的输入加输出（不含缓存命中的部分）；
	// 回合提前出错的是空的。宿主的 /goal 用它们判断目标
	Answer string
	Tools  []ToolSummary
	Tokens int64
	// Truncated 是这一轮最后一步被单次输出上限截断了（模型报 length）：话没说完，宿主要告诉主人
	Truncated bool
	// Auto 是宿主自动发起的一轮（目标续轮、定期任务）
	Auto bool
}

// Said 是一步说出口的那句：Step 是这一回合的第几步（从 0 数，和第几次请求对得上）
type Said struct {
	Step int
	Text string
}

// 一个回合的结局（Settled.Outcome）
const (
	OutcomeDone    = "done"
	OutcomeStopped = "stopped"
	OutcomeError   = "error"
)

// Seen 是"她看到了新事件"（8.1"一轮结束的通知"那一行，规格-渠道中间件第七节第 8 条）：一条事件存进库了，这个角色看得到。
// 标签照 Bun 的 message/stored(event, info)（ADR-141）：补录的、陌生人的、自己号发的、自检的也照发，用的一方各自过滤
// （ADR-187 第 1 条：记忆不吃补录、自检、陌生人，ADR-092；学说法吃补录的，ADR-101）。FromSelf 是渠道从她自己的号上收到的
// （发出去的回声、有人在她的号上手动发的），不等于她这一轮的回复
type Seen struct {
	CharacterID, Conversation              string
	Seq                                    int64
	Backfill, Stranger, FromSelf, Selftest bool
}

// Tool 是插件贡献的一个工具（8.1"插件贡献工具"）。审批的那一类（会改什么、给哪些听众、能不能并行、总时限）只认 policy.Specs 里
// 登记的，插件自己带不进来：注册时名字没登记就报错。给不给某个听众：policy 那一类先卡（owner-console 的只给主人 × 主窗口），
// 过了再看 For
type Tool struct {
	Decl tools.Declaration
	For  func(a audience.Audience) bool // 这个听众能不能用；nil = policy 放行的都能用
	// Exec 执行一次。ctx 在这一轮被停下、到了总时限、插件停掉时取消。返回的 error 当成一条出错的结果给她看（pi 的工具抛错也是这样），
	// 不让这一轮出错
	Exec func(ctx context.Context, c ToolCall) (tools.Result, error)
}

// ToolCall 是一次调用：这一回合是谁、在哪、谁在听（当轮的听众，外部审计 F02）、参数（内核整理过的）
type ToolCall struct {
	InputSeqs                         []int64 // 本次模型请求实际接纳的输入事件；宿主绑定，不从 Args 读取。
	Identity                          kernel.Invocation
	CharacterID, Conversation, TurnID string
	Name                              string // 宿主调用的工具名，不从 Args 中猜目标
	Audience                          audience.Audience
	Args                              jsjson.Value
	ContextID                         string
	Workspace                         Workspace
}

// BeforeCompact 是要压缩这个会话了（8.1"压缩前"，规格-记忆-v2 13.2.7，和记忆线对过的签名）
type BeforeCompact struct {
	CharacterID, Conversation string
	Trigger                   string    // rule：回复前到了硬门槛；early：回合后提前压；manual：主人点的（以后 memory：整理完触发）
	WaitUntil                 time.Time // 内核最多等到这时候：到点前交回推到哪儿了就行
}

// BeforeCompactFn 是压缩前要等它做完的一件事。ctx 跟插件的生命周期（插件停掉才取消，不随内核的等待时限取消）：内核到点就不等、
// 照常压缩，晚到的返回值丢掉，它自己的活接着做完。upTo：它已经处理到哪一条 seq；limited 为真时切点不越过 upTo+1。
// 出错、超时、limited 为假：不限制切点。提前压（early）在后台跑、不占回合队列：可能和同一个会话下一轮的拼参考信息、通知同时被调
// （同一个会话同时只压一个，压缩前钩子自己不会叠着来）
type BeforeCompactFn func(ctx context.Context, e BeforeCompact) (upTo int64, limited bool, err error)

// HookTimeouts 是钩子的时限；零值用默认（拼参考信息 5 秒、通知 30 秒、压缩前 90 秒、工具后和结束前 15 分钟）。测试里调短。
// 工具后和结束前是兜底：用户钩子每条命令有自己的时限（最多 10 分钟），这里防的是插件不看取消
type HookTimeouts struct {
	Context, Notify, Compact, Stage time.Duration
}

func (t HookTimeouts) context() time.Duration { return orDefault(t.Context, 5*time.Second) }
func (t HookTimeouts) notify() time.Duration  { return orDefault(t.Notify, 30*time.Second) }
func (t HookTimeouts) compact() time.Duration { return orDefault(t.Compact, 90*time.Second) }
func (t HookTimeouts) stage() time.Duration   { return orDefault(t.Stage, 15*time.Minute) }

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Registry 是一个插件拿来注册钩子的地方
type Registry struct {
	p   *plugins
	id  string
	gen int // 插件这次启动的代号（插件停了再起，代号变，旧注册晚到就撤）
}

type contextHook struct {
	plugin string
	fn     ContextFn
	ctx    context.Context
	alive  bool
}

type refCheckHook struct {
	plugin string
	fn     RefCheckFn
	ctx    context.Context
	alive  bool
}

type restoreHook struct {
	plugin string
	fn     RestoreFn
	alive  bool
}

type compactHook struct {
	plugin string
	fn     BeforeCompactFn
	ctx    context.Context // 插件停掉才取消
	alive  bool
}

type toolReg struct {
	plugin string
	gen    int // 注册时插件的代号，写插件日志用
	t      Tool
	spec   policy.Spec
	ctx    context.Context // 插件停掉才取消
	alive  bool
}

type notification[T any] struct {
	value    T
	ctx      context.Context
	activity kernel.Activity
}

// notifySub 是一种通知的一个订阅者：一条不限长的队列，按先后送、不丢；出错、超时记日志、接着送下一个（8.1"事件会不会丢"）。
// what 是日志里怎么叫这种通知
type notifySub[T any] struct {
	plugin string
	what   string
	fn     func(ctx context.Context, e T)
	mu     sync.Mutex
	queue  []notification[T]
	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

// plugins 是 Hub 上的插件表
type plugins struct {
	mu        sync.Mutex
	timeouts  HookTimeouts
	logf      func(format string, args ...any)
	warnf     func(format string, args ...any) // 级别出口（规格-日志分级）；nil 时退回 logf
	errorf    func(format string, args ...any)
	debugf    func(format string, args ...any)
	base      context.Context
	toolBase  context.Context // 已注册工具跟着升级取消；钩子仍用 base，封口就停
	lifetime  kernel.Lifetime
	context   []*contextHook
	refChecks []*refCheckHook
	restores  []*restoreHook
	settled   []*notifySub[Settled]
	seen      []*notifySub[Seen]
	compact   []*compactHook
	tools     []*toolReg
	// 回合里的两个同步挂点（stage_hooks.go）
	toolResult []*toolResultHook
	stopHooks  []*stopHook
	entries    map[string]*pluginEntry
	order      []*pluginEntry // 目录，按注册顺序
	services   map[string]*serviceEntry
	seqNext    int
	reconMu    sync.Mutex // 启停（Reconcile）只能一个跑
	// trace 是各插件自己的日志（规格-插件两层 第二点五节）；没给数据目录时什么也不写
	trace *plugintrace.Log
}

// warn、fail（ERROR）、debug 按级别写；没注入时退回 logf（级别在 applog 里就是 INFO）
func (p *plugins) warn(format string, args ...any)  { p.levelOr(p.warnf, format, args...) }
func (p *plugins) fail(format string, args ...any)  { p.levelOr(p.errorf, format, args...) }
func (p *plugins) debug(format string, args ...any) { p.levelOr(p.debugf, format, args...) }

func (p *plugins) levelOr(f func(string, ...any), format string, args ...any) {
	if f == nil {
		f = p.logf
	}
	f(format, args...)
}

func newPlugins(base context.Context, t HookTimeouts, logf func(string, ...any)) *plugins {
	if logf == nil {
		logf = log.Printf
	}
	return &plugins{base: base, timeouts: t, logf: logf}
}

// Trace 交一条这个插件的运行记录，宿主写进它自己的日志文件（规格-插件两层 第二点五节、规格-Bun后台第5批 9.7）。
// 插件 id、代号、时间、event 由宿主填；不进模型；写不进去就丢，不报错
func (r *Registry) Trace(e plugintrace.Event) {
	r.p.trace.Write(r.id, r.gen, plugintrace.EventPlugin, e, "")
}

// OnContext 注册一个拼参考信息的钩子
func (r *Registry) OnContext(fn ContextFn) (undo func()) {
	p := r.p
	ctx, cancel := context.WithCancel(p.base)
	h := &contextHook{plugin: r.id, fn: fn, alive: true, ctx: ctx}
	p.mu.Lock()
	p.context = append(p.context, h)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		defer func() { p.mu.Unlock(); cancel() }()
		h.alive = false
		for i, x := range p.context {
			if x == h {
				p.context = append(p.context[:i:i], p.context[i+1:]...)
				break
			}
		}
	}
	p.onCleanupFor(r, undo)
	return undo
}

// OnRefCheck 注册重建时核对参考信息块里记忆引用的钩子（Contribution.Refs）。没有活着的核对者，带引用的块一律不插
func (r *Registry) OnRefCheck(fn RefCheckFn) (undo func()) {
	p := r.p
	ctx, cancel := context.WithCancel(p.base)
	h := &refCheckHook{plugin: r.id, fn: fn, alive: true, ctx: ctx}
	p.mu.Lock()
	p.refChecks = append(p.refChecks, h)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		defer func() { p.mu.Unlock(); cancel() }()
		h.alive = false
		for i, x := range p.refChecks {
			if x == h {
				p.refChecks = append(p.refChecks[:i:i], p.refChecks[i+1:]...)
				break
			}
		}
	}
	p.onCleanupFor(r, undo)
	return undo
}

// OnRestore 注册重建后恢复插件存储的钩子（RestoreIn）
func (r *Registry) OnRestore(fn RestoreFn) (undo func()) {
	p := r.p
	h := &restoreHook{plugin: r.id, fn: fn, alive: true}
	p.mu.Lock()
	p.restores = append(p.restores, h)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		h.alive = false
		for i, x := range p.restores {
			if x == h {
				p.restores = append(p.restores[:i:i], p.restores[i+1:]...)
				break
			}
		}
	}
	p.onCleanupFor(r, undo)
	return undo
}

// restoreState 把插回的块引用的记忆交给各插件，恢复它们在新实例里的存储。调用方拿着这个会话的回合队列（实例只有这一处在改）
func (p *plugins) restoreState(l *Live, blocks [][]MemoryRef) {
	if len(blocks) == 0 {
		return
	}
	p.mu.Lock()
	hooks := append([]*restoreHook(nil), p.restores...)
	p.mu.Unlock()
	for _, h := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.logf("插件 %s 恢复重建后的存储出错：%v", h.plugin, r)
				}
			}()
			if err := h.fn(context.Background(), RestoreIn{CharacterID: l.in.CharacterID, Conversation: l.in.Conversation, State: l.stateOf(h.plugin), Blocks: blocks}); err != nil {
				p.logf("插件 %s 恢复重建后的存储没成：%v", h.plugin, err)
			}
		}()
	}
}

// refsStillValid 问所有活着的核对者：引用的记忆都还是原样吗。没有核对者、有一个说不、出错、超时，都是假
func (p *plugins) refsStillValid(ctx context.Context, in RefCheckIn) bool {
	if len(in.Refs) == 0 {
		return true
	}
	p.mu.Lock()
	hooks := append([]*refCheckHook(nil), p.refChecks...)
	p.mu.Unlock()
	if len(hooks) == 0 {
		return false
	}
	for _, h := range hooks {
		cctx, cancel := context.WithTimeout(ctx, p.timeouts.context())
		stop := context.AfterFunc(h.ctx, cancel)
		ok, err := func() (ok bool, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("出错了：%v", r)
				}
			}()
			return h.fn(cctx, in)
		}()
		stop()
		cancel()
		if err != nil {
			p.logf("插件 %s 核对参考信息块的记忆引用没成，这块不插回：%v", h.plugin, err)
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

// OnSettled 订阅"回合定了"
func (r *Registry) OnSettled(fn func(ctx context.Context, s Settled)) (undo func()) {
	return subscribe(r, &r.p.settled, "回合定了", fn)
}

// OnSeen 订阅"她看到了新事件"
func (r *Registry) OnSeen(fn func(ctx context.Context, e Seen)) (undo func()) {
	return subscribe(r, &r.p.seen, "新事件", fn)
}

// subscribe 给这个插件在 list 上挂一个订阅者（list 由 p.mu 管着），起它送通知的协程
func subscribe[T any](r *Registry, list *[]*notifySub[T], what string, fn func(context.Context, T)) (undo func()) {
	p := r.p
	ctx, cancel := context.WithCancel(p.base)
	s := &notifySub[T]{plugin: r.id, what: what, fn: fn, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel}
	p.mu.Lock()
	*list = append(*list, s)
	p.mu.Unlock()
	go s.loop(p)
	undo = func() {
		p.mu.Lock()
		for i, x := range *list {
			if x == s {
				*list = append((*list)[:i:i], (*list)[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		cancel()
	}
	p.onCleanupFor(r, undo)
	return undo
}

// OnTool 注册一个工具。名字要在 policy.Specs 里登记过（审批的那一类集中登记），不能和内置的、别的插件的重名，要有执行；
// 不合格返回错误，Setup 照样把它返回（这个插件不启用）
func (r *Registry) OnTool(t Tool) (undo func(), err error) {
	p := r.p
	name := t.Decl.Name
	spec, ok := policy.Specs[name]
	switch {
	case name == "" || t.Exec == nil:
		return nil, fmt.Errorf("工具 %q 没有名字或者没有执行", name)
	case !ok:
		return nil, fmt.Errorf("工具 %s 没有在 policy.Specs 里登记审批的那一类", name)
	}
	for _, d := range tools.OwnerConsole() {
		if d.Name == name {
			return nil, fmt.Errorf("工具 %s 和内置的重名", name)
		}
	}
	parent := p.base
	if p.toolBase != nil {
		parent = p.toolBase
	}
	ctx, cancel := context.WithCancel(parent)
	reg := &toolReg{plugin: r.id, gen: r.gen, t: t, spec: spec, ctx: ctx, alive: true}
	p.mu.Lock()
	for _, x := range p.tools {
		if x.t.Decl.Name == name {
			p.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("工具 %s 已经由插件 %s 注册了", name, x.plugin)
		}
	}
	p.tools = append(p.tools, reg)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		reg.alive = false
		for i, x := range p.tools {
			if x == reg {
				p.tools = append(p.tools[:i:i], p.tools[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		cancel()
	}
	p.onCleanupFor(r, undo)
	return undo, nil
}

// toolsFor 是这个听众能用的工具：插件注册的在前（注册的先后），内置的在后（只有主人 × 本地主窗口有，QQ 渠道里不出现本地执行能力），
// 和 Bun 的 forAudience 一样记忆的排在最前。工具定义在固定前缀里，所以实例按听众分（liveKey），插件启停时实例重建（acquire 比清单）
func (p *plugins) toolsFor(a audience.Audience) ([]tools.Declaration, map[string]*toolReg) {
	ownerConsole := ownerConsoleAudience(a)
	p.mu.Lock()
	regs := append([]*toolReg(nil), p.tools...)
	p.mu.Unlock()
	var out []tools.Declaration
	var bound map[string]*toolReg
	for _, r := range regs {
		if r.spec.Audience == "owner-console" && !ownerConsole || r.t.For != nil && !r.t.For(a) {
			continue
		}
		out = append(out, r.t.Decl)
		if bound == nil {
			bound = make(map[string]*toolReg)
		}
		bound[r.t.Decl.Name] = r
	}
	if ownerConsole {
		out = append(out, tools.OwnerConsole()...)
	}
	return out, bound
}

// tool 是插件注册的这个工具（没有是 nil）
func (p *plugins) tool(name string) *toolReg {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.tools {
		if r.t.Decl.Name == name {
			return r
		}
	}
	return nil
}

// runTool 执行插件的一个工具：ctx 跟着这一轮（停下、总时限）也跟着插件（停掉）；出错、慌了都变成一条出错的结果；
// 跑完插件已经停掉的，结果不要（8.1"插件停掉以后还在路上的活"）
func (p *plugins) runTool(ctx context.Context, r *toolReg, c ToolCall) (res tools.Result) {
	// 每次调用在插件自己的日志里记一行 host.tool：结果、耗时、失败分类（gone 插件停了或换了、denied 听众不能用、timeout、error）
	turnCtx, began, kind := ctx, time.Now(), ""
	defer func() { p.traceTool(turnCtx, r, began, kind, res) }()
	// 只核身份，不把同名新注册当执行体；即使新插件也允许这个听众，旧回合也不能调它。
	current := p.tool(r.t.Decl.Name)
	if current != r {
		why := "停掉了"
		if current != nil {
			why = "变了"
		}
		kind = "gone"
		return tools.Result{Text: fmt.Sprintf("工具 %s 没有执行：提供它的插件%s。", r.t.Decl.Name, why), IsError: true}
	}
	ownerConsole := ownerConsoleAudience(c.Audience)
	if r.spec.Audience == "owner-console" && !ownerConsole || r.t.For != nil && !r.t.For(c.Audience) {
		kind = "denied"
		return tools.Result{Text: fmt.Sprintf("工具 %s 没有执行：当前听众不能用。", r.t.Decl.Name), IsError: true}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(r.ctx, cancel)()
	defer func() {
		if x := recover(); x != nil {
			p.logf("插件 %s 的工具 %s 出错：%v", r.plugin, r.t.Decl.Name, x)
			res = tools.Result{Text: fmt.Sprintf("工具 %s 出错了，没有执行完。", r.t.Decl.Name), IsError: true}
			kind = "error"
		}
		p.mu.Lock()
		alive := r.alive
		p.mu.Unlock()
		if !alive {
			if res.Disposition != nil {
				res.Disposition.Discard()
			}
			res = tools.Result{Text: fmt.Sprintf("工具 %s 没有执行完：提供它的插件停掉了。", r.t.Decl.Name), IsError: true}
			kind = "gone"
		}
	}()
	out, err := r.t.Exec(ctx, c)
	if err != nil {
		if out.Disposition != nil {
			out.Disposition.Discard()
		}
		return tools.Result{Text: err.Error(), IsError: true}
	}
	return out
}

// traceTool 记一次工具调用（host.tool）。这一轮被停下记 skip（canceled），到了总时限记 timeout，工具回的是出错结果记 error
func (p *plugins) traceTool(turnCtx context.Context, r *toolReg, began time.Time, kind string, res tools.Result) {
	ev := plugintrace.Event{Took: time.Since(began), Timed: true, Result: plugintrace.OK}
	switch {
	case kind != "":
	case errors.Is(turnCtx.Err(), context.DeadlineExceeded):
		kind = "timeout"
	case turnCtx.Err() != nil:
		ev.Result, ev.Kind = plugintrace.Skip, "canceled"
	case res.IsError:
		kind = "error"
	}
	if kind != "" {
		ev.Result, ev.Kind, ev.Error = plugintrace.Fail, kind, res.Text
	}
	p.trace.Write(r.plugin, r.gen, plugintrace.EventTool, ev, r.t.Decl.Name)
}

// OnBeforeCompact 注册一个压缩前要等的钩子
func (r *Registry) OnBeforeCompact(fn BeforeCompactFn) (undo func()) {
	p := r.p
	ctx, cancel := context.WithCancel(p.base)
	h := &compactHook{plugin: r.id, fn: fn, ctx: ctx, alive: true}
	p.mu.Lock()
	p.compact = append(p.compact, h)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		h.alive = false
		for i, x := range p.compact {
			if x == h {
				p.compact = append(p.compact[:i:i], p.compact[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		cancel()
	}
	p.onCleanupFor(r, undo)
	return undo
}

// beforeCompact 压缩前同时调每个钩子，等到都做完、到了时限（WaitUntil）或者 turnCtx 取消（这一轮被停下）为止。
// 返回切点的上限 maxSeq（0 = 不限制）：limited 的取最小的 upTo+1；出错、超时、等的时候插件停掉的不算，记一行日志
func (p *plugins) beforeCompact(turnCtx context.Context, e BeforeCompact) (maxSeq int64) {
	p.mu.Lock()
	hooks := append([]*compactHook(nil), p.compact...)
	p.mu.Unlock()
	if len(hooks) == 0 {
		return 0
	}
	limit := p.timeouts.compact()
	e.WaitUntil = time.Now().Add(limit)
	type res struct {
		h       *compactHook
		upTo    int64
		limited bool
		err     error
	}
	ch := make(chan res, len(hooks))
	for _, h := range hooks {
		inv := kernel.InvocationFromContext(turnCtx)
		if inv.Kind == "" {
			inv = kernel.Invocation{Kind: kernel.KindCompact, RunID: "compact-hook:" + store.NewID(time.Now()), Scope: kernel.Scope{CharacterID: e.CharacterID, Conversation: e.Conversation}}
		}
		ctx, activity, err := p.lifetime.Begin(kernel.WithInvocation(h.ctx, inv), inv)
		if err != nil {
			p.logf("插件 %s 压缩前回调未受理：%v", h.plugin, err)
			ch <- res{h: h, err: err}
			continue
		}
		go func() {
			var hookErr error
			defer func() { activity.End(hookErr) }()
			defer func() {
				if r := recover(); r != nil {
					hookErr = fmt.Errorf("插件 %s 压缩前回调出错：%v", h.plugin, r)
					ch <- res{h: h, err: fmt.Errorf("出错了：%v", r)}
				}
			}()
			upTo, limited, err := h.fn(ctx, e)
			hookErr = err
			ch <- res{h, upTo, limited, err}
		}()
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	waiting := map[*compactHook]bool{}
	for _, h := range hooks {
		waiting[h] = true
	}
	for len(waiting) > 0 {
		select {
		case r := <-ch:
			delete(waiting, r.h)
			p.mu.Lock()
			alive := r.h.alive
			p.mu.Unlock()
			switch {
			case !alive:
				// 等的时候插件停掉了：它的结果不要（边界清单第 ④ 类）
			case r.err != nil:
				p.logf("插件 %s 压缩前没做完，照常压缩、不限制切点：%v", r.h.plugin, r.err)
			case r.limited && (maxSeq == 0 || r.upTo+1 < maxSeq):
				maxSeq = r.upTo + 1
			}
		case <-timer.C:
			for h := range waiting {
				p.logf("插件 %s 压缩前 %v 没做完，不等了，照常压缩、不限制切点", h.plugin, limit)
			}
			return maxSeq
		case <-turnCtx.Done():
			return maxSeq
		}
	}
	return maxSeq
}

type contextContribution struct {
	hook  *contextHook
	value *Contribution
}

// contextContributions 只收集候选；最后追加前仍要重新核对插件和取消状态。
func (p *plugins) contextContributions(ctx context.Context, in ContextIn, state func(plugin string) map[string]any) []contextContribution {
	p.mu.Lock()
	hooks := append([]*contextHook(nil), p.context...)
	p.mu.Unlock()
	var out []contextContribution
	for _, h := range hooks {
		if ctx.Err() != nil {
			break
		}
		in.State = state(h.plugin)
		end := in.timeline.begin("context.plugin", h.plugin)
		value, err := p.callContext(ctx, h, in)
		end(err != nil)
		if err != nil {
			var detail interface{ LogDetail() string } // 只写本机日志的细节，比如授权为什么被拒
			if errors.As(err, &detail) {
				p.logf("插件 %s 拼参考信息没成，这一轮跳过它：%v（%s）", h.plugin, err, detail.LogDetail())
			} else {
				p.logf("插件 %s 拼参考信息没成，这一轮跳过它：%v", h.plugin, err)
			}
		} else if value != nil {
			out = append(out, contextContribution{h, value})
		}
	}
	return out
}

// acceptedContext 是内核定下接纳的参考信息：各插件的行并成一份，Storable 是「这些行全部可以存库」
type acceptedContext struct {
	Lines    []string
	Recall   []RecallItem
	Refs     []MemoryRef
	Storable bool
}

// acceptContext 在锁内定下仍有效的贡献，锁外追加消息和提交账本；之后停用不撤回已经定下的接纳。
func (p *plugins) acceptContext(ctx context.Context, pending []contextContribution, appendMessage func(acceptedContext)) {
	p.mu.Lock()
	cancelled := ctx.Err() != nil
	got := acceptedContext{Storable: true} // 只有真注入了字的贡献才算（取消后只接纳无 Lines 的输入贡献，它没有召回）
	accepted := pending[:0]
	for _, c := range pending {
		if c.hook.alive && (!cancelled || len(c.value.Lines) == 0) {
			got.Lines = append(got.Lines, c.value.Lines...)
			if len(c.value.Lines) > 0 {
				got.Recall = append(got.Recall, c.value.Recall...)
				got.Refs = append(got.Refs, c.value.Refs...)
				// 白名单：有字的贡献没声明可存，整块都不存（不认识的插件、用户扩展默认不存）
				got.Storable = got.Storable && c.value.Storable
			}
			accepted = append(accepted, c)
		}
	}
	p.mu.Unlock()
	appendMessage(got)
	for _, item := range accepted {
		c := item.value
		if c.OnAccepted != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						p.logf("插件提交参考信息账本出错：%v", r)
					}
				}()
				c.OnAccepted()
			}()
		}
	}
}

var errHookTimeout = errors.New("超时了")

func (p *plugins) callContext(ctx context.Context, h *contextHook, in ContextIn) (*Contribution, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeouts.context())
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	inv := in.Identity
	if inv.Kind == "" {
		inv = kernel.Invocation{Kind: kernel.KindMemory, RunID: "context-hook:" + store.NewID(time.Now()), Scope: kernel.Scope{CharacterID: in.CharacterID, Conversation: in.Conversation, ScopeID: in.Scope}}
	}
	// Activity.End 会取消活动自己的 context；等返回值只看调用方寿命，不能把正常退出当取消。
	hookCtx, activity, err := p.lifetime.Begin(kernel.WithInvocation(ctx, inv), inv)
	if err != nil {
		stop()
		return nil, err
	}
	type res struct {
		value *Contribution
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		defer stop()
		var hookErr error
		defer func() { activity.End(hookErr) }()
		defer func() {
			if r := recover(); r != nil {
				hookErr = fmt.Errorf("插件 %s 参考回调出错：%v", h.plugin, r)
				ch <- res{err: fmt.Errorf("出错了：%v", r)}
			}
		}()
		l, err := h.fn(hookCtx, in)
		hookErr = err
		ch <- res{l, err}
	}()
	select {
	case r := <-ch:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return r.value, r.err
	case <-ctx.Done():
		// 超时或者这一轮被停下：不等它，它晚到的结果丢掉
		return nil, fmt.Errorf("%w（%v）", errHookTimeout, ctx.Err())
	}
}

// settle 把"回合定了"放进每个订阅者的队列，当场返回（不等它们处理）
func (p *plugins) settle(s Settled) {
	p.mu.Lock()
	subs := append([]*notifySub[Settled](nil), p.settled...)
	p.mu.Unlock()
	inv := kernel.Invocation{Kind: kernel.KindMemory, RunID: "settled-hook:" + store.NewID(time.Now()), Scope: kernel.Scope{CharacterID: s.CharacterID, Conversation: s.Conversation, ScopeID: s.Scope}}
	publish(p, subs, s, inv)
}

// notifySeen 把"她看到了新事件"放进每个订阅者的队列，当场返回
func (p *plugins) notifySeen(e Seen) {
	e.Selftest = e.Selftest || audience.NoMemory(e.Conversation) // 自检会话、提醒专用会话里的事件一律带标，不靠各个调用点记得填
	p.mu.Lock()
	subs := append([]*notifySub[Seen](nil), p.seen...)
	p.mu.Unlock()
	inv := kernel.Invocation{Kind: kernel.KindMemory, RunID: "seen-hook:" + store.NewID(time.Now()), Scope: kernel.Scope{CharacterID: e.CharacterID, Conversation: e.Conversation}}
	publish(p, subs, e, inv)
}

func publish[T any](p *plugins, subs []*notifySub[T], e T, inv kernel.Invocation) {
	for _, sub := range subs {
		sub.mu.Lock()
		if sub.ctx.Err() != nil {
			sub.mu.Unlock()
			continue
		}
		ctx, activity, err := p.lifetime.Begin(kernel.WithInvocation(sub.ctx, inv), inv)
		if err != nil {
			sub.mu.Unlock()
			p.logf("插件 %s 的%s未受理：%v", sub.plugin, sub.what, err)
			continue
		}
		sub.queue = append(sub.queue, notification[T]{value: e, ctx: ctx, activity: activity})
		sub.mu.Unlock()
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
}

func (s *notifySub[T]) loop(p *plugins) {
	defer func() {
		s.mu.Lock()
		pending := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, event := range pending {
			event.activity.End(s.ctx.Err())
		}
	}()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		for {
			s.mu.Lock()
			if len(s.queue) == 0 || s.ctx.Err() != nil {
				s.mu.Unlock()
				break
			}
			e := s.queue[0]
			s.queue[0] = notification[T]{}
			s.queue = s.queue[1:]
			s.mu.Unlock()
			s.call(p, e)
		}
	}
}

func (s *notifySub[T]) call(p *plugins, e notification[T]) {
	ctx, cancel := context.WithTimeout(e.ctx, p.timeouts.notify())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var callErr error
		defer func() { e.activity.End(callErr) }()
		defer func() {
			if r := recover(); r != nil {
				p.logf("插件 %s 处理%s出错：%v", s.plugin, s.what, r)
				callErr = fmt.Errorf("插件 %s 处理%s出错：%v", s.plugin, s.what, r)
			}
		}()
		s.fn(ctx, e.value)
		callErr = ctx.Err()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && s.ctx.Err() == nil {
			p.logf("插件 %s 处理%s超时（%v），接着送下一个", s.plugin, s.what, p.timeouts.notify())
		}
	}
}
