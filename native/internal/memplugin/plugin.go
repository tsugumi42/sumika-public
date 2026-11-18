// Package memplugin 是记忆接进对话内核的适配器（规格-记忆-v2 13.2.1）：实现 turn.Plugin，是唯一同时认识 turn 和 memory 的地方
// （记忆包不引 turn，内核也不认识记忆）。
//
// 读的一侧：每个回合开头召回一次，把想起来的事追加进参考信息（插话送到时再来一次）。
// 写的一侧（工具、压缩前、发送前的守卫）一步一步加在这里；后台整理的排程在 schedule.go（W5）。整理不挂 OnSettled：闸门没放行的消息不进回合，
// Settled 不会发，只有"她看到的新事件"通知补得上它（13.2.5、13.2.14 的禁止清单）。
package memplugin

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/memory"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

const (
	// ID 是插件在内核里的名字
	ID = "memory"

	// stateKey 是 ContextIn.State 里放这个插件自己那份记录的键。State 是内核按实例给每个插件的一小块存储：
	// 实例重建就是新的，同一个会话不同的档（Input.Scope）各有各的，所以"给过哪几条"不会串到别的听众那里
	stateKey = "memory"

	// 第二个查询带上同会话最近几句人说的话（规格-记忆-v2 第七节第 2 步，4～8 条）。起步值：这里只能记自己见过的几句，
	// 每句只取开头，长了会把查询冲淡。考题（memory-eval）现在没有带上文的题，量过再调
	recentKeep = 4
	recentClip = 100

	// defaultRecallTimeout 是召回里等本机嵌入的最长时间。内核给拼参考信息的钩子 5 秒，超时整个插件这一轮就被跳过，
	// 固定要点（约定、边界）也跟着没了；嵌入慢的时候提前放弃向量那一路，只用全文，约定和边界照给
	defaultRecallTimeout = 3 * time.Second
)

// AfterFunc 是排程用的计时：d 以后在自己的协程里调 f，返回的函数取消它（time.AfterFunc 的样子）。测试里换成拨得动的假钟
type AfterFunc func(d time.Duration, f func()) (stop func() bool)

func realAfter(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// Memories 给出这个角色的记忆；这个角色没有记忆就返回 nil（她的回合不召回，别的角色不受影响）。
// 返回的必须是已经打开好的：打开要同步索引、算向量，不能在 5 秒的钩子里做。关库的人要在 StopPlugin 返回以后再关
type Memories func(characterID string) *memory.Memory

// Options 是装配处给的东西
type Options struct {
	Memories      Memories
	Now           func() time.Time                 // 时钟；空用 time.Now（测试里定住）
	Logf          func(format string, args ...any) // 日志只写计数和原因，不写原文、不写会话 id（B16、边界 ㉑）；空用 log.Printf
	RecallTimeout time.Duration                    // 召回里等嵌入的最长时间；零值 3 秒
	// WhisperOrigin 说悄悄话会话 conversation 里，本次请求接纳的这几条输入（inputSeqs）对应哪一批悄悄话、来源会话、被引事件。
	// remember 在悄悄话那一轮写的记忆据此带来源（规格-群聊引用和路由 8.3）；来源只由宿主给，不读模型的参数。nil 或找不到就不带。
	WhisperOrigin func(conversation string, inputSeqs []int64) (memory.WhisperSource, bool)

	// 后台整理的排程（schedule.go）。整理本身只对 Memory.Writable() 为真的角色做；下面的数都是起步值，零值用默认
	Characters     func() []string // 启动扫描要扫的角色；nil = 不扫（此时没有游标的会话一律不整理，免得误回填）。Setup 也靠它判断"每个有库的角色都只读"（allReadOnly）：nil 时判不了，remember 的描述不改
	Quiet          time.Duration   // 一个会话安静这么久就整理；默认 10 分钟
	Threshold      int             // 攒够这么多条立刻整理；默认 40
	Parallel       int             // 同时整理的会话数；默认 2
	StartupBatches int             // 每次启动最多排几批（含续跑）；默认 20
	ImageRetry     time.Duration   // 在没识完的图前面截断以后，隔多久再看；默认 1 分钟
	After          AfterFunc       // 计时；空用 time.AfterFunc
}

// readOnlyRememberNote 是全员只读时加在 remember 工具描述前面的一句（规格-记忆写入上线 W4）：
// 工具在、她又不知道存不下来，会顺口说"记住啦"；原描述最后一句"后台会自动整理"在只读期同样不成立，所以一起说清。
// 不带库路径、配置键名
const readOnlyForgetNote = "（现在记忆写入没打开：调用也忘不掉；对方让你忘时，如实说这次忘不了。）"

const readOnlyRememberNote = "（现在记忆写入没打开：调用也存不下来，后台也不会自动整理；对方让你记时，如实说这次记不了。）"

// remember 的原描述里有一句"后台会自动整理"。没有任何角色开着后台整理时（只开 remember 的第一档，或者全员只读）这句不成立：
// 她会照着它不去记非明说的闲聊、还可能对主人说"会自动整理"，那些话永远存不下来，所以换成这句
const (
	autoSummaryClaim   = "后台会自动整理"
	noAutoSummaryClaim = "后台现在没开自动整理"
)

// Plugin 是记忆插件。一个值可以先后 Setup 几次（停掉再启用），每次启用各有各的生命周期
type Plugin struct {
	opt Options

	mu  sync.Mutex
	cur *life // 现在启用着的那一次；没启用是 nil
}

// New 建一个；没填的用默认
func New(opt Options) *Plugin {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.RecallTimeout <= 0 {
		opt.RecallTimeout = defaultRecallTimeout
	}
	if opt.Quiet <= 0 {
		opt.Quiet = defaultQuiet
	}
	if opt.Threshold <= 0 {
		opt.Threshold = defaultThreshold
	}
	if opt.Parallel <= 0 {
		opt.Parallel = defaultParallel
	}
	if opt.StartupBatches <= 0 {
		opt.StartupBatches = defaultStartupBatches
	}
	if opt.ImageRetry <= 0 {
		opt.ImageRetry = defaultImageRetry
	}
	if opt.After == nil {
		opt.After = realAfter
	}
	return &Plugin{opt: opt}
}

// Notify 告诉记忆：这个角色在这个会话里看到了新事件（规格-记忆-v2 13.2.5）。整理的计时靠它：攒够 40 条或安静 10 分钟。
// 回调里只记一笔就返回，不调模型、不读库，内核或中间件的通知钩子里放心调。插件没启用、已停掉时什么都不做
func (p *Plugin) Notify(characterID, conversation string) {
	if l := p.current(); l != nil {
		l.notify(characterID, conversation)
	}
}

// Summarize 要求整理这个会话。force 不看退避（压缩前、整理按钮、归档，B4）。不等结果：和排着的整理合成一个，
// 1000 条通知加 10 次 force 也只多跑一两次（同键合并，13.2.5）。压缩前要等结果的走 OnBeforeCompact（compact.go）
func (p *Plugin) Summarize(characterID, conversation string, force bool) {
	if l := p.current(); l != nil {
		l.request(characterID, conversation, force)
	}
}

func (p *Plugin) current() *life {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur
}

// ID 实现 turn.Plugin
func (p *Plugin) ID() string { return ID }

// allReadOnly 说每个有记忆库的角色是不是都只读：是，remember 的描述就要说明存不下来。
//   - 没有记忆库的角色（Memories 返回 nil）不参与判断：它调 remember 走不到 Memory.Remember，在 life.tool 里就报"没有记忆库"
//   - 只要有一个角色可写就不改描述：描述按插件定、不按当前角色（工具清单只按听众过滤，Tool.For 没有角色），靠每个角色自己的结果文字兜底
//   - 没给 Options.Characters 枚举不了角色，判不出"都只读"，不改描述（和 Bun 的工具声明一字不差）
//   - 有枚举但一个有库的角色都没有：谁调都存不下来，算全员只读
func (p *Plugin) allReadOnly() bool {
	if p.opt.Characters == nil {
		return false
	}
	for _, id := range p.opt.Characters() {
		if m := p.opt.Memories(id); m != nil && m.Writable() {
			return false
		}
	}
	return true
}

// noSummary 说有没有后台整理在跑：每个有记忆库的角色都没开整理（Writable 且 SummarizeEnabled）就是没有。
// 没给 Options.Characters 枚举不了角色，判不出，当作有（描述保持和 Bun 的工具声明一字不差）。描述按插件定、不按当前角色，有一个角色在整理就不改
func (p *Plugin) noSummary() bool {
	if p.opt.Characters == nil {
		return false
	}
	for _, id := range p.opt.Characters() {
		if m := p.opt.Memories(id); m != nil && m.Writable() && m.SummarizeEnabled() {
			return false
		}
	}
	return true
}

// Setup 实现 turn.Plugin：注册拼参考信息的钩子。cleanup 先拒绝新来的调用、取消在途的召回、等它们退出，之后才能关库（13.2.5 的停掉顺序）
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	if p.opt.Memories == nil {
		return nil, errors.New("没给 Memories：不知道每个角色的记忆在哪")
	}
	lf := p.start()
	allReadOnly, noSummary := p.allReadOnly(), p.noSummary()
	for _, decl := range tools.MemoryDecls() {
		if decl.Name == "remember" {
			// decl 是 MemoryDecls 给的副本，改描述不碰 declarations_gen.go 里的原样；写入开不开启动时就定了，一次运行里描述不变（提示词缓存）
			if noSummary {
				decl.Description = strings.Replace(decl.Description, autoSummaryClaim, noAutoSummaryClaim, 1)
			}
			if allReadOnly {
				decl.Description = readOnlyRememberNote + decl.Description
			}
		}
		if decl.Name == "memory_forget" && allReadOnly {
			decl.Description = readOnlyForgetNote + decl.Description
		}
		t := turn.Tool{Decl: decl, Exec: lf.tool(decl.Name)}
		if decl.Name == "remember" || decl.Name == "memory_forget" { // 忘掉和记住同一道门槛（memory.Forget 里再核一遍）
			t.For = func(a audience.Audience) bool {
				place := memory.PlaceOf(a.Channel, a.Conversation, a.Scene)
				// 自检会话（sumika-ctl chat）不给 remember：工具不进声明表，自检说的话不可能进长期记忆
				return a.PrivateOK && !audience.IsSelftest(a.Conversation) && (place == memory.LocalPrivate || place == memory.PlatformPrivate)
			}
		}
		if _, err := r.OnTool(t); err != nil {
			lf.stop()
			return nil, err
		}
	}
	r.OnContext(lf.inputContext)
	r.OnContext(lf.context)
	r.OnRefCheck(lf.checkRefs)
	r.OnRestore(lf.restore)
	r.OnBeforeCompact(lf.beforeCompact)
	r.OnSeen(func(_ context.Context, e turn.Seen) {
		if !e.Backfill && !e.Selftest && !e.Stranger {
			p.Notify(e.CharacterID, e.Conversation)
		}
	})
	p.mu.Lock()
	p.cur = lf
	p.mu.Unlock()
	lf.startScan()
	return func() {
		p.mu.Lock()
		if p.cur == lf {
			p.cur = nil
		}
		p.mu.Unlock()
		lf.stop()
	}, nil
}

func (l *life) tool(name string) func(context.Context, turn.ToolCall) (tools.Result, error) {
	return func(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
		if !l.enter() {
			return tools.Result{}, errors.New("记忆插件已经停掉")
		}
		defer l.wg.Done()
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(l.ctx, cancel)
		defer func() { stop(); cancel() }()
		m := l.p.opt.Memories(c.CharacterID)
		if m == nil {
			return tools.Result{}, errors.New("这个角色没有记忆库")
		}
		if (name == "remember" || name == "memory_forget") && audience.IsSelftest(c.Conversation) { // 声明表里已经没有它；万一有别的路径调到，也不写
			text := "自检会话里不能记长期记忆。"
			if name == "memory_forget" {
				text = "自检会话里不能忘掉长期记忆。"
			}
			return tools.Result{Text: text, IsError: true}, nil
		}
		arg := func(key string) string { return c.Args.Obj[key].Str }
		var result memory.ToolResult
		var err error
		switch name {
		case "remember":
			for _, key := range []string{"temporal", "valid_until", "source_quote"} {
				if v, exists := c.Args.Obj[key]; exists && v.Kind != jsjson.String {
					return tools.Result{Text: "记忆参数类型不正确，这次没有记。", IsError: true}, nil
				}
			}
			var onlyHer *bool
			if v := c.Args.Obj["only_her"]; v.Kind == jsjson.Bool {
				onlyHer = &v.Bool
			}
			rem := memory.RememberArgs{InputSeqs: c.InputSeqs, Kind: arg("kind"), Text: arg("text"), Subject: arg("subject"), Replaces: arg("replaces"), OnlyHer: onlyHer, Temporal: arg("temporal"), ValidUntil: arg("valid_until"), SourceQuote: arg("source_quote")}
			if audience.IsWhisper(c.Conversation) && l.p.opt.WhisperOrigin != nil {
				if ws, ok := l.p.opt.WhisperOrigin(c.Conversation, c.InputSeqs); ok {
					rem.Whisper = &ws
				}
			}
			result, err = m.Remember(ctx, c.Audience, rem)
		case "memory_forget":
			for _, key := range []string{"id", "reason", "source_quote"} {
				if v, exists := c.Args.Obj[key]; exists && v.Kind != jsjson.String {
					return tools.Result{Text: "记忆参数类型不正确，这次没有忘。", IsError: true}, nil
				}
			}
			result, err = m.Forget(ctx, c.Audience, memory.ForgetArgs{InputSeqs: c.InputSeqs, ID: arg("id"), Reason: arg("reason"), SourceQuote: arg("source_quote")})
		case "memory_search":
			result, err = m.Search(ctx, c.Audience, arg("query"))
		case "recall_raw":
			result, err = m.RecallRaw(ctx, c.Audience, arg("keywords"))
		}
		if err != nil {
			// 内部错误（库打不开、锁等不到）只记原因代码，给她看的是一句固定的话：原文可能带库的路径，内核会把 error 原样转给她，
			// 在群里她可能照着说出去（规格-记忆-v2 13.2.3：error 由工具层转成 IsError 文字）
			l.p.opt.Logf("记忆插件：工具 %s 出错（角色 %s，原因 %s）", name, c.CharacterID, memory.FailureCode(err))
			return tools.Result{Text: toolErrorText(name), IsError: true}, nil
		}
		return tools.Result{Text: result.Text, IsError: result.IsError}, nil
	}
}

// toolErrorText 是工具内部出错时给她看的话：不带错误原文
func toolErrorText(name string) string {
	if name == "remember" {
		return "记忆这边出了点问题，这次没记上，过一会儿再说一次。"
	}
	if name == "memory_forget" {
		return "记忆这边出了点问题，这次没忘掉，过一会儿再说一次。"
	}
	return "记忆这边出了点问题，这次没查到，过一会儿再查一次。"
}

// life 是一次启用的生命周期
type life struct {
	p      *Plugin
	ctx    context.Context // 停掉时取消：在途的召回别再等嵌入，在途的整理别再等模型
	cancel context.CancelFunc
	s      *sched // 后台整理的排程

	mu     sync.Mutex // 管 closed、wg.Add 和 State 里取或建 *state
	closed bool
	wg     sync.WaitGroup // 在途的钩子调用
}

func (p *Plugin) start() *life {
	ctx, cancel := context.WithCancel(context.Background())
	l := &life{p: p, ctx: ctx, cancel: cancel}
	l.s = newSched(l)
	return l
}

// enter 登记一次调用；已经停了不收。关闭标记和 Add 在同一把锁里，stop 开始等以后不会再有 Add
func (l *life) enter() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.wg.Add(1)
	return true
}

// stop 的顺序是 13.2.5 的：先不收新的（closed），取消 ctx，停掉所有计时，再等在途的召回、整理、扫描协程全部退出。之后装配处才能关库
func (l *life) stop() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.cancel()
	l.s.stopTimers()
	l.wg.Wait()
}

// state 是 State 里这个插件的一份记录。钩子超时以后内核不等它，那次调用的协程还在跑，下一轮的调用可能和它同时碰这份记录，所以自带一把锁
type state struct {
	mu        sync.Mutex
	pinnedKey string          // 上一次给过的固定要点（ref 拼起来）；变了才重给（照 memory.ts 的 contextPart）
	given     map[string]bool // 这个实例的历史里已经追加过的记忆编号
	recent    []string        // 同会话最近几句人说的话（不含她的回复、轮次总结、工具结果），查询的第二路用
	warned    bool            // Scope 对不上的日志只记一次
}

func (l *life) stateOf(m map[string]any) *state {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := m[stateKey].(*state); ok {
		return s
	}
	s := &state{given: map[string]bool{}}
	m[stateKey] = s
	return s
}

func (s *state) snapshot() (given map[string]bool, recent []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	given = make(map[string]bool, len(s.given))
	for ref := range s.given {
		given[ref] = true
	}
	return given, append([]string(nil), s.recent...)
}

// noteText 把这一句记进"最近说的话"：空的、和上一句一样的不记
func (s *state) noteText(text string) {
	text = clip(text, recentClip)
	if text == "" || len(s.recent) > 0 && s.recent[len(s.recent)-1] == text {
		return
	}
	s.recent = append(s.recent, text)
	if len(s.recent) > recentKeep {
		s.recent = append([]string(nil), s.recent[len(s.recent)-recentKeep:]...)
	}
}

func clip(text string, n int) string {
	if r := []rune(text); len(r) > n {
		return string(r[:n])
	}
	return text
}

// scopeMatches：装配处放进 Input.Scope 的档要和听众算出的一样（memory.ScopeKey）；主窗口永远用 turn.ConsoleScope，
// 它对应本地主窗口里"全部可见"那一种听众。对不上说明装配处算错了档，同一个会话里不同听众会共用一个实例，
// 上一轮追加的主人私事就留在群友这一轮的历史里（外部审计 F02）——这时宁可什么都不给
func scopeMatches(in turn.ContextIn) bool {
	key := memory.ScopeKey(in.Audience)
	if in.Scope == key {
		return true
	}
	return in.Scope == turn.ConsoleScope && in.Audience.Channel == "console" && key == "all"
}

// inputContext 只记用户输入：不带资料，取消后这句话仍进实例时也由同一个接纳点提交。
func (l *life) inputContext(_ context.Context, in turn.ContextIn) (*turn.Contribution, error) {
	if !l.enter() {
		return nil, nil
	}
	defer l.wg.Done()
	if in.State == nil || !scopeMatches(in) || l.p.opt.Memories(in.CharacterID) == nil {
		return nil, nil
	}
	s := l.stateOf(in.State)
	return &turn.Contribution{OnAccepted: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.noteText(in.Text)
	}}, nil
}

// context 是拼参考信息的钩子（规格-记忆-v2 第七节、13.2.1）：召回 → 渲染 → 只追加这个实例里还没给过的。
// 插话送到时（in.Steer）内核再来一次，走的是同一份记录，所以只会多出新的
func (l *life) context(ctx context.Context, in turn.ContextIn) (*turn.Contribution, error) {
	if !l.enter() {
		return nil, nil
	}
	defer l.wg.Done()
	if in.State == nil {
		return nil, errors.New("ContextIn.State 为空，没地方记给过哪几条")
	}
	m := l.p.opt.Memories(in.CharacterID)
	if m == nil {
		return nil, nil
	}
	s := l.stateOf(in.State)
	if !scopeMatches(in) {
		s.mu.Lock()
		first := !s.warned
		s.warned = true
		s.mu.Unlock()
		if first {
			l.p.opt.Logf("记忆插件：Input.Scope 和听众算出的档对不上，这个实例不给记忆（角色 %s）", in.CharacterID)
		}
		return nil, nil
	}

	// 向量那一路最多等 RecallTimeout：超了 Recall 自己退回只用全文，固定要点照给。插件停掉时也一起取消
	rctx, cancel := context.WithTimeout(ctx, l.p.opt.RecallTimeout)
	defer cancel()
	defer context.AfterFunc(l.ctx, cancel)()

	now := l.p.opt.Now()
	given, recent := s.snapshot()
	recalled := m.Recall(rctx, in.Audience, memory.Query{
		Text: in.Text, Recent: recent, Addressed: in.Addressed, Given: given, Now: now,
	})
	b := memory.Render(recalled, now)

	s.mu.Lock()
	defer s.mu.Unlock()
	// 只生成候选，内核接纳后才提交；超时、取消、停用都不能提前记成给过。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.ctx.Err() != nil {
		return nil, nil
	}
	// 迟到的旧调用可能和新调用重叠；这里只按已经接纳的账本去重。
	// 界面看的召回明细从同一份 recalled 带出来，顺序和 Lines 一致（固定要点在前、命中在后），不解析拼好的字
	var hits, refs []string
	var shown []turn.RecallItem
	var shownRefs []turn.MemoryRef // 和 shown 同一顺序：给参考信息块存库用的引用（ID 和给她看的样子的指纹）
	for i, ref := range b.Refs {
		if !s.given[ref] {
			hits, refs = append(hits, b.Hits[i]), append(refs, ref)
			shown = append(shown, recallItem(recalled.Hits[i], false))
			shownRefs = append(shownRefs, memoryRef(recalled.Hits[i], false))
		}
	}
	b.Hits, b.Refs = hits, refs
	pinnedChanged := b.PinnedKey != s.pinnedKey
	lines := b.Lines(pinnedChanged)
	if len(lines) == 0 {
		return nil, nil
	}
	var items []turn.RecallItem
	var itemRefs []turn.MemoryRef
	if pinnedChanged {
		for _, p := range recalled.Pinned {
			items = append(items, recallItem(p, true))
			itemRefs = append(itemRefs, memoryRef(p, true))
		}
	}
	items = append(items, shown...)
	itemRefs = append(itemRefs, shownRefs...)
	// Storable：这几行可以按档存库；Refs 是它们引用的记忆，重建时由 checkRefs 核对（规格-提示词前缀稳定 7.2、7.3）
	return &turn.Contribution{Lines: lines, Recall: items, Storable: true, Refs: itemRefs, OnAccepted: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pinnedKey = b.PinnedKey
		for _, ref := range refs {
			s.given[ref] = true
		}
	}}, nil
}

// memoryRef 是一条命中给她看的样子的指纹（memory.HitDigest），参考信息块存库时引用它
func memoryRef(h memory.Hit, pinned bool) turn.MemoryRef {
	d := memory.HitDigest(h)
	return turn.MemoryRef{ID: d.Ref, Hash: d.Hash, Pinned: pinned}
}

// restore 是重建以后恢复这个实例里「已经给过哪几条」：插回去的块里引用的记忆已经在历史里了，别再给一遍
// （不恢复的话说话人每换一次、实例重建一次，历史里就多一份固定要点和命中）。固定要点按最后一个带它的块的那一组恢复
func (l *life) restore(_ context.Context, in turn.RestoreIn) error {
	if !l.enter() {
		return nil
	}
	defer l.wg.Done()
	if in.State == nil {
		return nil
	}
	s := l.stateOf(in.State)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, block := range in.Blocks {
		var pinned []string
		for _, r := range block {
			if r.Pinned {
				pinned = append(pinned, r.ID)
			} else {
				s.given[r.ID] = true
			}
		}
		if len(pinned) > 0 {
			s.pinnedKey = strings.Join(pinned, ",")
		}
	}
	return nil
}

// checkRefs 是重建时核对参考信息块里的记忆引用：这些记忆现在还在、没过期、没被隔离、给她看的样子没变，才返回真。
// 这个角色没有记忆库、读不出来都返回假（失败关闭）
func (l *life) checkRefs(ctx context.Context, in turn.RefCheckIn) (bool, error) {
	if !l.enter() {
		return false, nil
	}
	defer l.wg.Done()
	m := l.p.opt.Memories(in.CharacterID)
	if m == nil {
		return false, nil
	}
	refs := make([]memory.RefDigest, len(in.Refs))
	for i, r := range in.Refs {
		refs[i] = memory.RefDigest{Ref: r.ID, Hash: r.Hash}
	}
	return m.VerifyRefs(ctx, in.Audience, in.Now, refs), nil
}

// recallItem 把一条命中的结构化原样交给界面：分数只带真有的（没重排 Rerank 是 0，向量没中 Vector、Sim 是 0）。
func recallItem(h memory.Hit, pinned bool) turn.RecallItem {
	var prev []turn.RecallPrior // 注入的行里拼着“取代了旧记录”，明细带同一份
	for _, p := range h.Previous {
		prev = append(prev, turn.RecallPrior{Text: p.Text, CreatedAt: p.CreatedAt})
	}
	if pinned { // 固定要点不是按这句话召回的，Score 是占位的 1，不是相关度
		return turn.RecallItem{Text: h.Text, Kind: h.Kind, Source: h.Source, CreatedAt: h.CreatedAt, Hearsay: h.Hearsay, Pinned: true}
	}
	return turn.RecallItem{Text: h.Text, Kind: h.Kind, Source: h.Source, CreatedAt: h.CreatedAt, Hearsay: h.Hearsay, Pinned: pinned,
		Score: h.Score, Rerank: h.Rerank, Sim: h.Sim, Lexical: h.Lexical, Vector: h.Vector, Previous: prev}
}
