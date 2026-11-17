package turn

// 活着的实例（第 2 步 2g，agent.ts AgentHub）：同一个会话的下一轮接着用上一轮的实例——历史里留着之前的参考信息、
// 工具调用和结果、思考、被停下的半句；新的一轮只追加这一轮的参考信息（有才加）和这句话。
// 同一个会话的回合一个接一个跑（turnTail）；来了新的一句先打断正在说的（interrupt）；主人按停止，下一轮提醒她一次

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/persona"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/policy"
	"sumika-native/internal/runstate"
	"sumika-native/internal/store"
	"sumika-native/internal/textclean"
	"sumika-native/internal/tools"
	"sumika-native/internal/turnbudget"
)

// 隔多久再告诉她一次"现在几点"（agent.ts CLOCK_EVERY_MS）
const clockEvery = 15 * time.Minute

// Live 是一个会话的实例（agent.ts Live）
type Live struct {
	b              *Built
	cfg            Config // 本轮冻结；提前压缩先复制，不能重新读取发布指针
	msgs           []Msg  // 实例里的消息（pi 的 state.messages）：系统提示、重建的历史，之后每一轮追加的
	system         string // 建实例时拼的系统提示、用的模型：下一轮拼出来不一样就重建（agent.ts acquire 比的是卡的版本和拼好的各段）
	model          string
	reasoningModel string              // 当前历史里模型绑定思考的来源；切换时清除
	staleGen       uint64              // 建这个实例时该会话的 MarkStale 代次；落后于 Hub 的就不复用，下一轮重建
	toolRegs       map[string]*toolReg // acquire 按当轮清单绑定；硬门槛原地重建也保留，回合中不换成同名新注册
	// relation：这个实例里已经给过的"对面是谁"；lastClockAt：上一次报时（零值 = 还没报过，一定到点）
	relation       string
	approvalRule   string // 这个实例里最近给过的审批规则说法；变了才再给一次，放在每轮参考信息里（不进固定前缀，切档不断缓存）
	lastClockAt    time.Time
	lastTotalInput int64 // 上一步服务商报的输入（input + cacheRead + cacheWrite），下一轮看硬门槛用
	first          bool  // 下一步是建好以后的第一步：model_runs 记为什么重建、裁剪留了几条
	todoChecked    bool  // 这个实例看过计划清单了（重建后只提醒一次）
	workState      kernel.WorkState
	workRecord     workRecord // 重建时从旧 root 算出的累计工作记录（有些步骤可能没放进 msgs），压缩时合进摘要
	midRun         bool       // 回合中途重建：这一轮的消息由调用方接回
	noWorkFold     bool       // 溢出恢复时给压缩用的那份：不折叠当前 root 的工作记录，尾部还要原样接回
	steer          *Steering
	// in：建这个实例的那一档（会话、听众、档）；seen：这个实例里已经有的事件（她这几轮说的、主人说的、插话，也算没留在实例里的乱码那句），
	// baseSeq：建实例时那一批的最后一条。复用时把 baseSeq 之后、没见过、跑完的回合用过的（别的档那几轮说的）追加进来（规格-Go后台 8.1）
	in       Input
	seen     map[int64]bool
	baseSeq  int64
	turnSeqs []int64 // 这一轮用过、说过的事件（回合结束时汇进 Hub.done）
	// pending：上一个渠道回合在实例里的那一段，等中间件写完库用 Commit 交回来（ADR-184 第 3 条）；
	// 这个会话下一轮开始时还没交回来，就照库重写那一段（她说的没进库，自然不留）。turnFrom 是这一轮那一批在 msgs 里的位置，
	// turnCtx 是这一轮插话送到时插件追加的参考信息各在哪、跟着哪条事件
	pending  *pendingTurn
	turnFrom int
	turnCtx  []steerCtx
	// refBlocks：这一轮拼出的、可以存库的参考信息块（refblock.go），交回时写进事件库；每轮开头清空
	refBlocks []refBlock
	state     map[string]map[string]any // 每个插件在这个实例里的存储（ContextIn.State）
	contextID string                    // fill 每次换新，软门槛精简不换
	// lastUsed：上一次取用、上一轮结束的时刻（回收按它挑最久没用的、闲了多久）。只在这个会话的回合队列里写；
	// 回收只在这个会话的队列空着时读
	lastUsed time.Time

	// mu 管着下面这些：哪一轮在跑、这一步说出口的、上一轮被停了没有、这一步从什么时候算起。
	// 打断、停止、插话都先在锁里认准"现在在跑的是哪一轮"，只对那一轮下手（不会停错轮、插话不会落到收完尾的那一轮上）
	mu          sync.Mutex
	run         *turnRun  // 正在跑的这一轮；nil = 没在跑
	compacting  bool      // 正在压缩（手动压缩，或者回复前压）：这时按停止不留"上一轮被停止"的提醒
	stepText    string    // 这一步说出口的（agent.ts turn.text，按停止时记下的半句）
	stopped     *string   // 上一轮被主人停了：她被停住前这一步说出口的（去掉两头空白）
	stepStarted time.Time // 这一步从什么时候算（首字、总耗时）；主人批完审批时重新算（agent.ts 听 tools/resolved 那一段）
}

// turnRun 是正在跑的一轮：取消它、等它跑完
type turnRun struct {
	id     string // 回合 id（RunningTurnIDs 给运行自检用）
	cancel context.CancelFunc
	model  ModelUse
	done   chan struct{}
}

// Outcome 是一轮的结果
type Outcome struct {
	Result *Result
	TurnID string // 这一轮的回合 id，提前出错（没跑起来）的也有，和"回合定了"里的一样
	Err    error
}

// Hub 管着各个会话的实例（agent.ts AgentHub）。同一个会话的回合一个接一个跑；实例按"角色 + 会话 + 档 + 听众"分（规格-Go后台 8.1）
type Hub struct {
	failoverOnce sync.Once
	failover     *llm.Failover // 宿主没给 Config.Failover 时的冷却表（failoverSelector）
	Cfg          Config
	late         sync.Map // 这一轮的运行 id → 晚到的图（*lateQueue），见 late.go
	// Stores 按会话取库（规格-聊天记录按平台分库 第五节第 3 步）：聊天记录在会话所在平台的库里，运行记录（trace）共用一份。
	// 内核只经 storeOf、trace 取，不自己拆会话串
	Stores    store.Stores
	Client    *llm.Client
	Models    []llm.Model
	lifecycle *hubLifecycle

	base       context.Context
	cancelBase context.CancelFunc
	stop       *stagedStop
	plugins    *plugins
	mu         sync.Mutex
	live       map[string]*Live
	active     map[string]*Live         // 会话 → 正在跑回合的那个实例（插话、停止、打断找它）
	turnTail   map[string]chan struct{} // 同一个会话的回合一个接一个跑
	// done：会话 → 跑完的回合用过、说过的事件（那一批、插话、她的回复）。复用实例追上别的档时只追这些：
	// 还排着队、没轮到的那几句已经存进库，但不能提前塞进这一轮
	done map[string]map[int64]bool
	// queued：会话 → 排上队、还没开始跑的那几批各自的最后一条。清 done 时要留着比它们晚的：
	// 它们新建实例时靠 done 认"比这一批晚、跑完的回合用过的"（排在后面跑的那批可能比先跑的那批早存进库）
	queued map[string][]int64
	// turnGen：会话 → 跑完的回合数。空闲计时排上以后又跑过回合（新消息在计时到点前后到了）就不压这一次
	turnGen map[string]uint64
	// compactJobs：会话 → 后台正在提前压或空闲压的那一次（同一个会话同时只压一个，agent.ts compact 的去重）；
	// compactFailures：会话 → 上一次后台压缩没成时的上下文大小和时刻（提前压再长 10% 才再试，空闲压 6 小时内不再试）；
	// bg 等后台的活（Idle）；idleTimers：会话 → 排着的空闲压缩计时（ADR-388，键是 convKey）
	compactJobs     map[string]*compactJob
	compactFailures map[string]compactFailure
	idleTimers      map[string]*idleCompactTimer
	bg              *bgTasks
	// stoppedLeft：实例被回收时主人按过停止、还没提醒的那句（liveKey → 停住前说出口的），这一档下次建实例时带上（7.1 A42）
	stoppedLeft map[string]*string
	// testHook 是测试卡住交错用的（8.2 A4、A11）：在几个普通回调抓不到的时刻调，point 是哪一刻。
	// "settling"：最后一次取完插话、没有插话也不再调工具，马上收尾之前；"waitCompact"：开始等后台正在提前压的那一次；
	// "newTopicBarrier"：开始新话题已经挡住新输入、取消了这个会话正在跑和排着的回合，等它们收尾之前
	testHook func(point string)
	// newTopicTimeout 是“开始新话题”等这个会话停下的上限；零是 30 秒。只在测试里设短一些。
	newTopicTimeout time.Duration
	workErr         error
	staleGen        map[string]uint64 // 会话 → MarkStale 的代次（thread.go）
	workToolOnce    sync.Once
	workToolErr     error

	// gone：这一档（liveKey）的实例为什么被放掉（闲置、挤掉、RestartIn），下次建实例时取走记进重建留痕；
	// seenKeys、seenConvs：本进程建过实例的档和会话（rebuildtrace.go）
	gone                map[string]string
	seenKeys, seenConvs map[string]bool
}

// compactJob 是后台正在提前压的一次：done 关上以后 res 是结局；cancel 停掉这一次（开新话题时结果已经用不上了）
type compactJob struct {
	done   chan struct{}
	cancel context.CancelFunc
	res    Compacted
}

// idleCompactTimer 是一次排着的空闲压缩：live 是排计时时的实例（被换掉、回收了就不压）；gen 是排上时的回合数（又跑过回合就不压）
type idleCompactTimer struct {
	live *Live
	gen  uint64
	stop func()
}

// compactFailure 是上一次后台压缩没成：size 是那时的实例大小，at 是失败的时刻（h.Cfg.Now 计时）
type compactFailure struct {
	size int
	at   time.Time
}

// bgTasks 数着后台的活（提前压、空闲压）。Add 和 Wait 可能同时发生：关机正好撞上计时到点，
// 这时用 sync.WaitGroup 是文档里说的误用；计数和等待用同一个条件变量，Add 在等待中也安全
type bgTasks struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int
}

func newBgTasks() *bgTasks {
	b := &bgTasks{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *bgTasks) Add(n int) {
	b.mu.Lock()
	b.n += n
	b.mu.Unlock()
}

func (b *bgTasks) Done() {
	b.mu.Lock()
	b.n--
	if b.n == 0 {
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

func (b *bgTasks) Wait() {
	b.mu.Lock()
	for b.n > 0 {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

// Input 是"该她回这一批了"（规格-Go后台 8.1 内核对外的入口）：主窗口的 Send 拼好一份；渠道中间件自己拼。
// 能不能不说、试运行、平台怎么改写发出去的字不在这里：渠道中间件在回合定了以后做（ADR-184 第 1 条）
type Input struct {
	Identity                  kernel.Invocation // 宿主绑定；不从正文或听众权限推导调用者
	CharacterID, Conversation string
	Seqs                      []int64 // 这一批里已经存进库的事件：重建历史时不进历史，Text 这一轮单独追加
	Text                      string
	// Audience 是谁在听（含 OwnerAsked）：原样交给插件；工具清单、执行前的关也按它（policyAudience）
	Audience  audience.Audience
	Addressed bool // 中间件判定这一批叫到了她（@、引用她的话、人工别名）；记忆的门控据此决定是否查
	// Scope 是权限档：当轮可见集合的指纹，由调用的一方从 Audience 算好（内核不认识记忆）。同一个会话不同档各一个实例，
	// 互相不共用参考信息和工具结果（外部审计 F02）
	Scope string
	// Relation 是"对面是谁"那一句（中间件按对方是谁写好，规格-渠道中间件第九节第 7 条）：放进只追加的参考信息，
	// 同一个实例里只给一次、变了才再给（ADR-022）。空的：主窗口照旧是"对面是主人"那句，渠道（平台群）就不给
	Relation string
	// Scenes 是场合那几段提示词的名字（prompts/<名字>.md，按先后拼，例如 scene-group、platform-qq；ADR-175 写死在程序里），
	// 渠道中间件按场合和平台选（规格-渠道中间件第九节第 3 条）。空的照旧拼本地私聊那一套（场合 + 风格预设），主窗口不填
	Scenes []string
	// Parts 是主人这条消息的有序部分（引用回复块 / 普通文字块，规格-引用）：只有本地私聊主人的输入能带，由桌面入口核对后绑定；
	// Text 是各块文字用换行连起来的样子。存档在主人这条事件上（reply_to 是第一个引用块的出处，segments.local_parts 是全部）；
	// 给模型的结构化文字只用在这一条新消息上，不改历史。omitempty：没分块时输入摘要和存档的字节与加这个字段之前一样。
	Parts     []history.Part `json:",omitempty"`
	Observers []string       // 本地群的可信成员；本地回复的观察记录与新事件通知都给这些角色
	Workspace Workspace      // 由本地宿主绑定；零值使用 Config.Workdir，QQ 强制无工作区
	// ReplyOrigin 非空时，她这一轮说的话存进库用这个来源名（默认 character）。宿主自动发的轮次（定期任务）
	// 先用不显示的来源存，等分了类再改回来；普通回合不填
	ReplyOrigin string `json:"replyOrigin,omitempty"`
	// Auto 是宿主自动发起的一轮（目标续轮、定期任务）：没有主人在场，额度用顶层那组，不用本地的宽额度。随持久载荷保存
	Auto bool `json:"auto,omitempty"`
	// 本地桌面发送的寿命与草稿接收方只属于这次请求，不放进共享 Config。
	requestCtx context.Context
	onDraft    func(LocalDraft)
	onProcess  func(ProcessStep)
	onSteps    func([]ProcessStep) // 这一轮到现在的全部思考和工具，给 WithProgress 的调用方
	onSettled  func(Settled)
}

// WithProgress 让这一轮把流式正文和思考、工具进度交给调用方（本地群聊：每位成员一轮，没有 WatchLocalRun 可订）。
// 回调在回合的协程里调，不能阻塞；只属于这次请求，不进持久载荷。
func (in Input) WithProgress(draft func(LocalDraft), steps func([]ProcessStep)) Input {
	in.onDraft, in.onSteps = draft, steps
	return in
}

// ConsoleScope 是本地主窗口的档：主人一个人在，永远只有这一档
const ConsoleScope = "owner"

// consoleInput 是主人在主窗口对 characterID 说的一句
func consoleInput(characterID string, seq int64, text string) Input {
	conv := consoleConv(characterID)
	var seqs []int64
	if seq > 0 {
		seqs = []int64{seq}
	}
	return Input{CharacterID: characterID, Conversation: conv, Seqs: seqs, Text: text,
		Audience: audience.Console(conv), Addressed: true, Scope: ConsoleScope}
}

// local 是本地主窗口的回合：她说的、主人插的话由内核存进库；别的渠道由中间件存（ADR-184 第 3 条）
func (in Input) local() bool {
	return in.Identity.Kind != kernel.KindChild && in.Audience.Channel == "console"
}

// ownerConsoleAudience 不把本地群的主人在场误当成私聊执行许可；任一群标识都收紧。
func ownerConsoleAudience(a audience.Audience) bool {
	return a.Channel == "console" && a.PrivateOK && (a.Scene == "private" || a.Scene == "") && !strings.Contains(a.Conversation, ":group:")
}

// policyAudience 只转换工具权限，不改传给记忆插件的原始听众。
func policyAudience(a audience.Audience) policy.Audience {
	return policy.Audience{Channel: a.Channel, PrivateOk: a.PrivateOK && (a.Channel != "console" || ownerConsoleAudience(a))}
}

// NewHub 建一个空的；ctx 取消时所有在跑的回合都停下。一个进程只建一个 Hub（几个平台的库共用它，规格-聊天记录按平台分库第六节）：
// 建的时候收拾上一个进程没收尾的（表一 A9：没有结局的回合记成被打断、停在执行中的工具记成不知道结果、等审批的记成取消，
// store.SweepInterrupted）。这些都在共用的运行记录里，只扫这一次：再扫一次会把正在跑的回合当成上一个进程没收尾。
// stores 为 nil 的只能用不碰库的那几样（插件、通知）
func NewHub(ctx context.Context, cfg Config, stores store.Stores, client *llm.Client, models []llm.Model) *Hub {
	ctx, staged := newStagedStop(ctx, cfg.Lifecycle.defaults().StopGrace, cfg.Logf, nil)
	h := &Hub{Cfg: cfg, Stores: stores, Client: client, Models: models, base: ctx, cancelBase: staged.stop, stop: staged, plugins: newPlugins(ctx, cfg.Hooks, cfg.Logf),
		live: map[string]*Live{}, active: map[string]*Live{}, turnTail: map[string]chan struct{}{}, done: map[string]map[int64]bool{},
		queued: map[string][]int64{}, turnGen: map[string]uint64{}, compactJobs: map[string]*compactJob{}, compactFailures: map[string]compactFailure{}, idleTimers: map[string]*idleCompactTimer{},
		bg: newBgTasks(), stoppedLeft: map[string]*string{}}
	h.plugins.warnf, h.plugins.errorf, h.plugins.debugf = cfg.Warnf, cfg.Errorf, cfg.Debugf
	h.initLifecycle()
	h.plugins.toolBase = staged.hard
	staged.mu.Lock()
	staged.escalate = h.lifecycle.manager.CancelAll
	staged.mu.Unlock()
	h.plugins.lifetime = h.Cfg.Lifetime
	// 插件日志按数据目录写；测试和没配数据目录时 Root 是空的，什么也不写
	h.plugins.trace = plugintrace.New(plugintrace.Options{DataDir: cfg.Data.Root, Logf: h.plugins.logf, Now: cfg.Now})
	h.initWork()
	h.initializeBudget()
	if st := h.trace(); st != nil {
		now := time.Now
		if cfg.Now != nil {
			now = cfg.Now
		}
		if n, err := st.PruneSpans(now().AddDate(0, 0, -store.SpanRetentionDays).UnixMilli()); err != nil {
			h.plugins.logf("清旧的回合时间线出错：%v", err)
		} else if n > 0 {
			h.plugins.logf("清掉 %d 行超过 %d 天的回合时间线", n, store.SpanRetentionDays)
		}
		at := now().UnixMilli()
		if err := h.sweepInterrupted(st, at); err != nil {
			// 整库的清扫没做成：库里还留着上个进程没收尾的记录，所有输入先拒，等重试（fault.go）
			h.lifecycle.raiseLocked(time.Now(), FaultScopeKernel, "", "", FaultStartupSweep, err, repair{kind: repairSweep, at: at})
		}
	}
	context.AfterFunc(ctx, h.Seal)
	return h
}

// surfaceSwept 给上一个进程被杀时留下的、有工具步骤没挂上回复的本地回合各留一行系统行（store.SurfaceInterrupted），
// 重启以后聊天区看得到那次工具调用。出错只记日志，不拦启动。
func (h *Hub) surfaceSwept(tr *store.Store, at int64) {
	turns, err := tr.SweptWithOrphans(at)
	if err != nil {
		h.plugins.warn("查被打断的回合里没挂上回复的步骤出错：%v", err)
		return
	}
	for _, t := range turns {
		h.surfaceInterrupted(t.ID, t.CharacterID, t.Conversation, at)
	}
}

// surfaceInterrupted 本地会话才留行：别的渠道的聊天记录不是这个窗口的，也不往里写系统行。
func (h *Hub) surfaceInterrupted(turnID, characterID, conversation string, at int64) {
	if !strings.HasPrefix(conversation, "console:") {
		return
	}
	st, err := h.storeOf(conversation)
	if err != nil {
		h.plugins.logf("被打断的回合 %s 找不到会话的库：%v", turnID, err)
		return
	}
	if _, err := st.SurfaceInterrupted(turnID, characterID, conversation, at); err != nil {
		h.plugins.logf("给被打断的回合 %s 留系统行出错：%v", turnID, err)
	}
}

// storeOf 是这个会话所在平台的那份库：Events 是那个平台的聊天记录，Trace 是共用的运行记录。
// 会话 → 平台只认 store 里登记的那一处（PlatformOfConversation），没登记的报错、不落进别的库
func (h *Hub) storeOf(conversation string) (*store.Store, error) {
	if h.Stores == nil {
		return nil, errors.New("内核没有接库")
	}
	return h.Stores.ForConversation(conversation)
}

// trace 是只调运行记录那些方法的一份（回合的开始和结局、收拾上一个进程没收尾的），不借某个平台的库；没接库时是 nil
func (h *Hub) trace() *store.Store {
	if h.Stores == nil {
		return nil
	}
	return h.Stores.TraceStore()
}

// Idle 等后台的活（提前压缩）做完。关库之前先取消 NewHub 的 ctx、等回合都停了再调：在路上的压缩不再等模型，也不写库
func (h *Hub) Idle() { h.bg.Wait() }

// Use 启用一个插件（规格-Go后台 8.1）；StopPlugin 停掉它：注册撤掉，还在路上的结果丢掉
func (h *Hub) Use(p Plugin) error   { return h.plugins.use(p) }
func (h *Hub) StopPlugin(id string) { h.plugins.stop(id) }

// NotifySeen 是渠道中间件入库以后告诉订阅的插件"她看到了新事件"（每条一次，带标签照发、不替订阅者过滤）。当场返回，不等它们处理。
// 本地主窗口的事件由内核自己发，中间件不用管
func (h *Hub) NotifySeen(e Seen) { h.plugins.notifySeen(e) }

func convKey(characterID, conversation string) string { return characterID + ":" + conversation }

// liveKey：听众也算进去，换了听众绝不复用旧实例（工具清单在实例里）
func liveKey(in Input) string {
	return fmt.Sprintf("%s|%s|%s|%v", convKey(in.CharacterID, in.Conversation), in.Scope, in.Audience.Channel, in.Audience.PrivateOK)
}

// liveOf 是主窗口那一档的实例（测试看实例里的消息用）
func (h *Hub) liveOf(characterID string) *Live {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[liveKey(consoleInput(characterID, 0, ""))]
}

// activeOf 是这个会话正在跑回合的实例（没有是 nil）
func (h *Hub) activeOf(characterID, conversation string) *Live {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active[convKey(characterID, conversation)]
}

// running 是这个会话正在跑的那一轮（没有在跑的是 nil）
func (h *Hub) running(characterID, conversation string) *turnRun {
	l := h.activeOf(characterID, conversation)
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.run
}

// RunningTurns 是现在正在跑的回合数（每个会话最多一个，只数还在跑的实例）。换 exe 之前看一眼，不切断它们。
func (h *Hub) RunningTurns() int {
	h.mu.Lock()
	lives := make([]*Live, 0, len(h.active))
	for _, l := range h.active {
		lives = append(lives, l)
	}
	h.mu.Unlock()
	n := 0
	for _, l := range lives {
		l.mu.Lock()
		if l.run != nil {
			n++
		}
		l.mu.Unlock()
	}
	return n
}

// RunningTurnIDs 是现在正在跑的回合 id：每个会话正在跑的那一轮，加上还在跑的内部运行（子运行、后台任务的循环）。
// 运行自检（S4 turn.stuck）用它排除"进程里还在跑"的回合；只读，不改任何状态。
func (h *Hub) RunningTurnIDs() []string {
	h.mu.Lock()
	lives := make([]*Live, 0, len(h.active))
	for _, l := range h.active {
		lives = append(lives, l)
	}
	h.mu.Unlock()
	var ids []string
	for _, l := range lives {
		l.mu.Lock()
		if l.run != nil && l.run.id != "" {
			ids = append(ids, l.run.id)
		}
		l.mu.Unlock()
	}
	if lc := h.lifecycle; lc != nil {
		lc.mu.Lock()
		for id := range lc.internal {
			ids = append(ids, id)
		}
		lc.mu.Unlock()
	}
	return ids
}

// Respond 是"该她回这一批了"（规格-Go后台 8.1）：这一批已经由调用的一方存进库，排在这个会话上一轮后面回。
// 要不要先打断正在说的由调用的一方定（主窗口的 Send 会打断）
func (h *Hub) Respond(in Input) <-chan Outcome {
	return h.RespondContext(h.base, in)
}

// RespondContext 让调用方取消整个已排队的回合；取消覆盖尚未登记为 active 的时间段。
// Outcome 仍要取走，收到后才能关闭调用方持有的存储。
func (h *Hub) RespondContext(ctx context.Context, in Input) <-chan Outcome {
	h.cancelIdleCompact(in.CharacterID, in.Conversation)
	in.Audience.Conversation = in.Conversation
	if in.requestCtx == nil {
		in.requestCtx = ctx
	}
	return h.submitOutcome(ctx, in, kernel.InputQueue)
}

// Send 是主人在主窗口说了一句（console.ts consoleSend + agent.ts respond）：先请正在说的停下（打断），等它停好了再把这句存成主人说的，
// 排在上一轮后面回。不在跑就当场存、当场排上队（几句话前后脚到时按调用的先后存、按先后回）。返回的通道在这一轮回完时给结果
func (h *Hub) Send(characterID, text string) <-chan Outcome {
	return h.send(consoleInput(characterID, 0, text))
}

// LocalDraft 是当前步骤的完整正文；落库后发空正文，最终消息由存储订阅交付。
type LocalDraft struct {
	TurnID string
	Step   int
	Text   string
	Reset  bool // 撤回失败草稿；不封存这一步，替代模型仍在同一步输出
}

// SendLocal 由可信桌面入口绑定会话，所有权由入口在边界校验。
func (h *Hub) SendLocal(ctx context.Context, characterID, conversation, text string, draft func(LocalDraft)) <-chan Outcome {
	return h.SendLocalParts(ctx, characterID, conversation, nil, text, draft)
}

// SendLocalParts 同 SendLocal，主人这句由已核对过的几块组成（parts 为空就是一句普通的话 text；否则 text 是各块文字连起来的样子）。
func (h *Hub) SendLocalParts(ctx context.Context, characterID, conversation string, parts []history.Part, text string, draft func(LocalDraft)) <-chan Outcome {
	in := consoleInput(characterID, 0, text)
	in.Parts = parts
	in.Conversation, in.Audience.Conversation = conversation, conversation
	in.requestCtx, in.onDraft = ctx, draft
	return h.send(in)
}

func (h *Hub) send(in Input) <-chan Outcome {
	h.cancelIdleCompact(in.CharacterID, in.Conversation)
	if r := h.running(in.CharacterID, in.Conversation); r != nil {
		r.cancel()
	}
	ctx := h.base
	if in.requestCtx != nil {
		ctx = in.requestCtx
	}
	return h.submitOutcome(ctx, in, kernel.InputQueue)
}

// enqueue 把一轮接在这个会话上一轮的尾巴后面（agent.ts turnTail）；登记是当场做完的，上一轮出错不影响这一轮
func (h *Hub) enqueue(key string, fn func()) {
	h.mu.Lock()
	prev := h.turnTail[key]
	mine := make(chan struct{})
	h.turnTail[key] = mine
	h.mu.Unlock()
	go func() {
		if prev != nil {
			<-prev
		}
		defer close(mine)
		fn()
	}()
}

// consoleConv 是主人和 characterID 在本地主窗口的那个会话
func consoleConv(characterID string) string { return "console:" + characterID }

// Steer、Stop、Interrupt、Restart 是本地主窗口的；渠道中间件按会话调后面带 In 的那几个（规格-渠道中间件第九节第 4 条）。
// 都按（角色，会话）找正在跑的那个实例，不管它是哪一档

// Steer 是主人中途插一句（agent.ts steer）：不在跑返回 false（调用方当普通的一句发）
func (h *Hub) Steer(characterID, text string) (string, bool) {
	return h.SteerIn(characterID, consoleConv(characterID), text, nil)
}

// SteerIn 是往这个会话正在跑的回合里插一句。主窗口的 seqs 给 nil：送到时内核存成主人说的。
// 渠道的回合不由内核写库（ADR-184 第 3 条）：中间件先把这几条存进库，seqs 是它们；送到时内核只登记，前缀也不写"主人亲口说的"。
// 渠道的不带 seqs 不收（返回 false）。
// 认"在跑"和收下这句在同一把锁里：这一轮收尾（取走没送到的退回去）也在这把锁里，收下的插话一定有人送到或者退回
func (h *Hub) SteerIn(characterID, conversation, text string, seqs []int64) (string, bool) {
	lc := h.lifecycle
	lc.mu.Lock()
	r := lc.active[convKey(characterID, conversation)]
	if r == nil {
		lc.mu.Unlock()
		return "", false
	}
	in := r.input
	lc.mu.Unlock()
	in.Text, in.Seqs, in.Parts = text, seqs, nil // 在跑那一轮自己的分块不能带到这句插话上
	if !in.local() && len(seqs) == 0 {
		return "", false
	}
	in.Identity = kernel.Invocation{}
	in, err := h.trustedInput(in)
	if err != nil {
		return "", false
	}
	rec, err := h.Submit(h.base, Submission{Input: in, Mode: kernel.InputSteer, ClientKey: h.inputClientKey(in), ExpectedRunID: r.input.Identity.RunID})
	if err != nil {
		return "", false
	}
	if rec.RunID != r.input.Identity.RunID {
		return "", false
	}
	if h.Cfg.OnSteer != nil {
		h.Cfg.OnSteer(SteerEvent{Phase: "accepted", ID: rec.InputID, Text: text})
	}
	return rec.InputID, true
}

// Stop 是主人按了停止（agent.ts stop）
func (h *Hub) Stop(characterID string) bool { return h.StopIn(characterID, consoleConv(characterID)) }

// StopIn 停止本轮并有界等待真实退出；超过 Drain 期限仍由生命周期追踪执行。
func (h *Hub) StopIn(characterID, conversation string) bool {
	wait := time.NewTimer(h.Cfg.Lifecycle.defaults().DrainTimeout)
	defer wait.Stop()
	lc := h.lifecycle
	lc.mu.Lock()
	r := lc.active[convKey(characterID, conversation)]
	if r == nil {
		lc.mu.Unlock()
		if current := h.running(characterID, conversation); current != nil {
			current.cancel()
			select {
			case <-current.done:
			case <-wait.C:
			case <-h.base.Done():
			}
			return true
		}
		return false
	}
	through := r.record.Sequence
	for _, p := range lc.pending[r.record.Domain] {
		through = max(through, p.record.Sequence)
	}
	for _, p := range lc.steers[r.input.Identity.RunID] {
		through = max(through, p.Sequence)
	}
	lc.mu.Unlock()
	if l := h.activeOf(characterID, conversation); l != nil {
		l.mu.Lock()
		if l.in.local() && !l.compacting {
			p := textclean.Trim(l.stepText)
			l.stopped = &p
		}
		l.mu.Unlock()
	}
	_, err := h.StopRun(h.base, kernel.StopRequest{Scope: r.input.Identity.Scope, RunID: r.input.Identity.RunID, ThroughSequence: through})
	if err != nil {
		return false
	}
	sub, err := h.WatchRun(r.input.Identity.Scope, r.input.Identity.RunID)
	if err != nil {
		return false
	}
	defer sub.Close()
	for !sub.Snapshot().Drained {
		select {
		case <-sub.Wake():
		case <-wait.C:
			return true
		case <-h.base.Done():
			return true
		}
	}
	return true
}

// Interrupt 请主窗口正在说的停下（agent.ts interrupt），等它停好
func (h *Hub) Interrupt(characterID string) { h.InterruptIn(characterID, consoleConv(characterID)) }

// InterruptIn 请这个会话正在说的停下，等它停好（没送到的插话在那一轮收尾时退回去，不当成被停止）
func (h *Hub) InterruptIn(characterID, conversation string) {
	if r := h.running(characterID, conversation); r != nil {
		r.cancel()
		<-r.done
	}
}

// Restart 丢掉主窗口的实例（agent.ts restart）：只按 RestartIn 处理，不写新话题分隔；要开始新话题用 StartNewTopic
func (h *Hub) Restart(characterID string) { h.RestartIn(characterID, consoleConv(characterID)) }

// newTopicSource 是“开始新话题”那条系统事件的来源名，也是 actor_id（写法照 app 的 systemNotice）
const newTopicSource = "new-topic"

// ErrNewTopicTimeout 是“开始新话题”等这个会话停下超过上限：不写分隔，放掉 maintenance。
var ErrNewTopicTimeout = errors.New("开始新话题：等这个会话停下超时")

// defaultNewTopicTimeout 是“开始新话题”等这个会话停下的上限；测试用 Hub.newTopicTimeout 换短。
const defaultNewTopicTimeout = 30 * time.Second

// StartNewTopic 是“开始新话题”的唯一入口：先让这个会话停下，写一条 context-break 通知事件（历史读到这里为止），
// 再丢掉它每一档的实例。检查点回退、渠道交回只丢实例，不写这条分隔。
// 只收两种场合：主人的本地私聊（会话名正好是 console:<角色>）和主人的平台私聊（Cfg.OwnerPrivate 认）。
// 别的会话一律报错、不写：分隔按整个会话读，群聊写下去会把所有角色的历史一起截断，可实例只丢了一个角色的。
// 停下是取消：正在跑的和排着的都取消（排着的属于旧话题，新话题开始以后没有意义），照停止那条路标成已取消、
// 按被停下收尾，它们不会再 acquire。等它们收完尾用回合订阅的事件唤醒；写分隔照旧排进会话队列，不和新输入、
// Compact 这类同队列的活同时改实例。
func (h *Hub) StartNewTopic(characterID, conversation string) error {
	owner := characterID != "" && conversation == consoleConv(characterID)
	if !owner {
		owner = h.Cfg.OwnerPrivate != nil && h.Cfg.OwnerPrivate(conversation)
	}
	if !owner {
		return fmt.Errorf("开始新话题只支持主人的本地私聊和主人的平台私聊，%q 不是", conversation)
	}
	// 切话题是一道屏障：先把这个会话的新输入挡在外面（maintenance），再把正在跑和排着的都取消。
	// 不挡新输入的话排队的会一直补上来；不排队写分隔的话，那一批（或同队列的 Compact）会在丢实例之后
	// 把装着旧历史的实例放回 h.live
	lc := h.lifecycle
	domain := convKey(characterID, conversation)
	lc.mu.Lock()
	if lc.sealed.Load() {
		lc.mu.Unlock()
		return runstate.ErrSealed
	}
	if lc.maintenance[domain] {
		lc.mu.Unlock()
		return ErrInputBusy
	}
	lc.maintenance[domain] = true
	var subs []*RunSubscription
	var pending []*inputRun
	cancel := func(r *inputRun) {
		subs = append(subs, h.subscribeRunLocked(r))
		r.cancel()
	}
	if r := lc.active[domain]; r != nil {
		cancel(r)
	}
	for _, r := range lc.pending[domain] {
		pending = append(pending, r)
		cancel(r)
	}
	lc.mu.Unlock()
	// 正在后台压的这一份结果也会作废（新话题换了起点）：现在就停掉请求，不等它回来白花钱
	h.cancelCompact(characterID, conversation)
	defer func() {
		for _, s := range subs {
			s.Close()
		}
		lc.mu.Lock()
		delete(lc.maintenance, domain)
		lc.mu.Unlock()
	}()
	// 排着的输入在库里标成已取消（和停止走同一条路）；内存里已经取消，收尾时按 stopped 结算。
	for _, r := range pending {
		if tr := h.trace(); tr != nil {
			if err := tr.CancelKernelInput(context.Background(), r.input.Identity.InputID); err != nil && !errors.Is(err, store.ErrInputConflict) {
				h.plugins.logf("开始新话题：把排着的输入 %s 标成已取消失败：%v", r.input.Identity.InputID, err)
			}
		}
	}
	if h.testHook != nil {
		h.testHook("newTopicBarrier")
	}
	timeout := h.newTopicTimeout
	if timeout <= 0 {
		timeout = defaultNewTopicTimeout
	}
	limit := time.NewTimer(timeout)
	defer limit.Stop()
	// 被取消的回合收完尾（事件唤醒），不靠轮询；关机或超时就有出口，不写分隔。
	for _, sub := range subs {
		for !sub.Snapshot().Drained {
			select {
			case <-sub.Wake():
			case <-limit.C:
				return ErrNewTopicTimeout
			case <-h.base.Done():
				return runstate.ErrSealed
			}
		}
	}
	// 写分隔排进这个会话的回合队列：Compact 也走同一条队列，写的时候不能和它同时改实例。
	// 超时或关机时还没动笔就放弃；已经动笔的写完照实返回，不能报了超时却把分隔留在库里。
	var mu sync.Mutex
	aborted, writing := false, false
	done := make(chan error, 1)
	h.enqueue(domain, func() {
		mu.Lock()
		if aborted {
			mu.Unlock()
			done <- ErrNewTopicTimeout
			return
		}
		writing = true
		mu.Unlock()
		done <- h.startNewTopic(characterID, conversation)
	})
	var stopErr error
	select {
	case err := <-done:
		return err
	case <-limit.C:
		stopErr = ErrNewTopicTimeout
	case <-h.base.Done():
		stopErr = runstate.ErrSealed
	}
	mu.Lock()
	if writing {
		mu.Unlock()
		return <-done
	}
	aborted = true
	mu.Unlock()
	return stopErr
}

func (h *Hub) startNewTopic(characterID, conversation string) error {
	st, err := h.storeOf(conversation)
	if err != nil {
		return err
	}
	channel, _, _ := strings.Cut(conversation, ":")
	_, err = st.AppendEvent(store.NewEvent{Channel: channel, Conversation: conversation, ActorKind: "system", ActorID: newTopicSource,
		Origin: "system:" + newTopicSource, Kind: "notice", Text: "从这里开始新话题",
		Segments:  map[string]any{"notice": history.ContextBreak, "source": newTopicSource},
		CreatedAt: h.Cfg.Now().UnixMilli()}, []string{characterID})
	if err != nil {
		return err
	}
	h.dropIn(characterID, conversation)
	return nil
}

// RestartIn 先让这个会话停下，再丢掉它每一档的实例：下一批重新读历史建实例。
// 实例被回收时留下的停止提醒也丢掉（新话题不提上一轮被停止，和丢实例时一样）
func (h *Hub) RestartIn(characterID, conversation string) {
	h.InterruptIn(characterID, conversation)
	h.dropIn(characterID, conversation)
}

// dropIn 丢掉这个会话每一档的实例，不写分隔（调用方先停下）
func (h *Hub) dropIn(characterID, conversation string) {
	prefix := convKey(characterID, conversation) + "|"
	h.mu.Lock()
	ck := convKey(characterID, conversation)
	stop := h.cancelIdleLocked(ck)
	job := h.compactJobs[ck]
	if h.staleGen == nil {
		h.staleGen = map[string]uint64{}
	}
	// 会话代次也往前走：后台压缩开始时记的代次对不上就不写快照（检查点回退、新话题都走这里）
	h.staleGen[convKey(characterID, conversation)]++
	for k := range h.live {
		if strings.HasPrefix(k, prefix) {
			h.noteGone(k, RebuildRestartIn)
			delete(h.live, k)
		}
	}
	for k := range h.stoppedLeft {
		if strings.HasPrefix(k, prefix) {
			delete(h.stoppedLeft, k)
		}
	}
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
	// 正在后台压的这一份也作废了（起点或代次已经变了）：停掉请求，不等它回来
	if job != nil && job.cancel != nil {
		job.cancel()
	}
}

// ApprovalResolved 是主人批完了一张审批（tools/resolved）：这个角色在主窗口正在跑的这一步重新计时，等审批的时间不算进模型的首字和总耗时。
// 只管主窗口：工具和审批只有主人在本地主窗口时有（toolsFor），渠道的回合没有审批
func (h *Hub) ApprovalResolved(characterID string) {
	if l := h.activeOf(characterID, consoleConv(characterID)); l != nil {
		l.mu.Lock()
		if l.run != nil {
			l.stepStarted = h.Cfg.Now()
		}
		l.mu.Unlock()
	}
}

func (h *Hub) modelSnapshot() (Config, []llm.Model) {
	if h.Cfg.ModelSnapshot != nil {
		cfg, models := h.Cfg.ModelSnapshot()
		// 静态运行依赖仍来自 Hub；发布者只提供模型页字段。
		base := h.Cfg
		base.Models, base.Thinking, base.LocalThinking, base.PrivateThinking, base.Context = cfg.Models, cfg.Thinking, cfg.LocalThinking, cfg.PrivateThinking, cfg.Context
		base.FallbackModels, base.DefaultFallbackModels = cfg.FallbackModels, cfg.DefaultFallbackModels
		base.modelCatalog = models
		base.ModelVersion = cfg.ModelVersion
		base.CatalogError = cfg.CatalogError
		return base, models
	}
	cfg := h.Cfg
	cfg.modelCatalog = h.Models
	return cfg, h.Models
}

// Compact 压缩这个会话（主人点的；以后记忆整理完触发的也走它，maxSeq 传记忆整理到的下一条，规格-记忆-v2 13.2.7、ADR-062）。
// 排进这个会话的回合队列：前一轮跑完才做，做完下一轮才开始，不和回合同时改实例。材料是这个会话本地那一档的实例里她看到的那份
// （还没有实例的先照重建的样子建一个；已经存进库、还排着队的那几句当成"这一批"，不读进历史、不算进切点）。
// 压的时候和一轮一样算"在跑"：停止、新的一句都能打断它（不留"上一轮被停止"的提醒），这时来的插话压完退回去当普通的一句。
// 后台正在提前压这个会话的，等它、用它的结局（不再压一次）。
// 成功的下一轮换上新快照（acquire 看到更新的快照就重建）。只压本地主窗口的会话：渠道的会话满了只带最近的话（agent.ts limits 的 trim）
func (h *Hub) Compact(characterID, conversation, trigger string, maxSeq int64) (Compacted, error) {
	if !strings.HasPrefix(conversation, "console:") {
		return Compacted{}, fmt.Errorf("会话 %s 不是本地主窗口的：渠道的会话满了只带最近的话，不压缩", conversation)
	}
	h.cancelIdleCompact(characterID, conversation)
	input := consoleInput(characterID, 0, "")
	input.Conversation, input.Audience.Conversation = conversation, conversation
	input, err := h.trustedInput(input)
	if err != nil {
		return Compacted{}, err
	}
	identity := input.Identity
	identity.RunID = store.NewID(h.Cfg.Now())
	identity.RootID, identity.Kind = identity.RunID, kernel.KindCompact
	if err := AdmitBudgetRoot(h.base, h.Cfg.Budget, identity, h.Cfg.BudgetConfig.LimitsFor(identity.Scope, time.Now())); err != nil {
		return Compacted{}, err
	}
	activityCtx, activity, err := h.Cfg.Lifetime.Begin(h.base, identity)
	if err != nil {
		return Compacted{}, err
	}
	defer activity.End(nil)
	activityCtx = kernel.WithInvocation(activityCtx, identity)
	type out struct {
		c   Compacted
		err error
	}
	done := make(chan out, 1)
	h.enqueue(convKey(characterID, conversation), func() {
		cfg, models := h.modelSnapshot()
		c, model, err := h.cardAndModel(cfg, models, characterID)
		if err != nil {
			done <- out{err: err}
			return
		}
		ck := convKey(characterID, conversation)
		h.mu.Lock()
		queued := append([]int64(nil), h.queued[ck]...)
		h.mu.Unlock()
		in := Input{Identity: input.Identity, CharacterID: characterID, Conversation: conversation, Seqs: queued, Audience: audience.Console(conversation), Addressed: true, Scope: ConsoleScope}
		h.mu.Lock()
		if current := h.live[liveKey(in)]; current != nil {
			in.Identity = current.in.Identity
			in.Workspace = current.in.Workspace
		}
		h.mu.Unlock()
		in, err = in.freezeWorkspace(activityCtx, cfg)
		if err != nil {
			done <- out{err: err}
			return
		}
		st, err := h.storeOf(conversation)
		if err != nil {
			done <- out{err: err}
			return
		}
		l, err := h.acquire(cfg.thinkingFor(in), st, c, *model, in)
		if err != nil {
			done <- out{err: err}
			return
		}
		ctx, cancel := context.WithCancel(activityCtx)
		defer cancel()
		run := &turnRun{cancel: cancel, done: make(chan struct{}), model: modelUse(l)}
		l.mu.Lock()
		l.run, l.compacting = run, true
		l.mu.Unlock()
		h.mu.Lock()
		h.active[ck] = l
		h.mu.Unlock()
		res := h.compactOrWait(ctx, l, trigger, maxSeq)
		l.mu.Lock()
		l.run, l.compacting = nil, false
		left := l.steer.take()
		l.mu.Unlock()
		h.mu.Lock()
		if h.active[ck] == l {
			delete(h.active, ck)
		}
		h.mu.Unlock()
		l.steer.emitDropped(left, true)
		close(run.done)
		done <- out{c: res}
	})
	o := <-done
	return o.c, o.err
}

// cardAndModel 读角色卡、在模型列表里找她用的那个（respond、Compact 共用）
func (h *Hub) cardAndModel(cfg Config, models []llm.Model, characterID string) (persona.Character, *llm.Model, error) {
	c, err := persona.Load(cfg.Data.CharactersDir(), characterID, cfg.Models[characterID])
	if err != nil {
		return c, nil, err
	}
	for i := range models {
		if models[i].ID == c.Model {
			return c, &models[i], nil
		}
	}
	if len(cfg.fallbacks(characterID)) > 0 {
		// 列表可能暂时缺失；按真实请求错误判断是否换模型。
		return c, &llm.Model{ID: c.Model}, nil
	}
	// 目录本身没取到：说的是"来源连不上"（设置里修地址），不是"模型不存在"
	if cfg.CatalogError != "" {
		return c, nil, fmt.Errorf("模型来源连不上：%s", cfg.CatalogError)
	}
	return c, nil, &llm.UnknownModelError{ID: c.Model}
}

// respond 是 agent.ts respondNow：读卡、找模型、取或建实例、看硬门槛，然后跑一轮，跑完看要不要提前压
func (h *Hub) respond(caller context.Context, in Input) (res *Result, turnID string, err error) {
	h.unmarkQueued(in)
	ctx, cancel := context.WithCancel(caller)
	ctx = kernel.WithInvocation(ctx, in.Identity)
	defer cancel()
	cfg, models := h.modelSnapshot()
	cfg = cfg.thinkingFor(in)
	if cfg.BeforeTurn != nil {
		cfg.BeforeTurn()
	}
	turnID = in.Identity.RunID
	if turnID == "" {
		turnID = store.NewID(cfg.Now())
	}
	tr := h.trace()
	if tr != nil {
		if e := tr.TurnStart(turnID, in.CharacterID, in.Conversation, in.Audience.Channel, cfg.Now().UnixMilli()); e != nil {
			return nil, turnID, e
		}
	}
	// 回合定了（8.1）：每个 Respond 恰好一次，提前返回的（读卡、找模型、建实例出错、超了硬门槛）也发，结局是 error。
	// 挂在最前面：跑起来的那一轮先收尾（插话退回、放开这个会话），最后才发（规格-渠道中间件第九节第 6 条）；
	// 结局先写进库（只写一次）再发
	defer func() {
		s := Settled{Identity: in.Identity, TurnID: turnID, CharacterID: in.CharacterID, Conversation: in.Conversation, Scope: in.Scope, Outcome: OutcomeDone, Err: err,
			Audience: in.Audience, Scenes: in.Scenes, Auto: in.Auto}
		if res != nil {
			s.Said = res.Said
			s.Answer, s.Tools = res.Reply, res.Tools
			for _, step := range res.Steps {
				s.Tokens += step.Usage.Input + step.Usage.Output
			}
			s.Truncated = len(res.Steps) > 0 && res.Steps[len(res.Steps)-1].Stop == "length"
		}
		if err != nil {
			s.Outcome = OutcomeError
		}
		// 被停下、被打断的算 stopped，哪怕停在请求之前、照 7.1 A36 带着那个错误（渠道中间件按 stopped 记"已打断"）
		if res != nil && res.Stopped {
			s.Outcome = OutcomeStopped
		}
		if res != nil && res.BudgetExhausted {
			s.Outcome = string(kernel.OutcomeBudgetExhausted)
		}
		if tr != nil {
			if e := tr.TurnEnd(turnID, s.Outcome, h.Cfg.Now().UnixMilli()); e != nil {
				err = errors.Join(err, e)
				s.Err = err
				if s.Outcome != OutcomeStopped {
					s.Outcome = OutcomeError
				}
			}
		}
		// 正常退出（封口）时被停下：被切断的工具记成“程序退出时中断”，下一个进程第一轮提醒她，聊天区也留得住（和被杀的同一条路）
		if tr != nil && s.Outcome == OutcomeStopped && h.lifecycle.sealed.Load() {
			if _, e := tr.MarkExitInterrupted(turnID); e != nil {
				h.plugins.logf("记退出时被切断的工具出错：%v", e)
			}
			h.surfaceInterrupted(turnID, in.CharacterID, in.Conversation, h.Cfg.Now().UnixMilli()) // 没有没挂上回复的工具步骤就什么都不写
		}
		if in.onSettled != nil {
			in.onSettled(s)
		} else {
			h.plugins.settle(s)
		}
	}()
	characterID := in.CharacterID
	ck := convKey(characterID, in.Conversation)
	// 这一批没交给她就结束了（读卡、找模型、建实例出错，停在压缩上、压完重建出错）：记成跑完的回合用过的（不记成见过），
	// 下一轮接着用实例时追进来，不从她的历史里消失（以库为准：重建时本来就读得到它；Bun 要等重建，7.1 A45）
	unseen := func() {
		h.mu.Lock()
		h.markDone(ck, in.Seqs)
		h.mu.Unlock()
	}
	// 排队时就被停了（本地私聊点停止、本地群停群）：算 stopped 不算 error，回合记录和主人看到的一致
	if caller.Err() != nil {
		unseen()
		return &Result{TurnID: turnID, Stopped: true}, turnID, nil
	}
	// 这个会话所在平台的库（没登记的渠道在这里就报错，这一轮不跑）
	st, err := h.storeOf(in.Conversation)
	if err != nil {
		unseen()
		return nil, turnID, err
	}
	in, err = in.freezeWorkspace(caller, cfg)
	if err != nil {
		unseen()
		return nil, turnID, err
	}
	c, model, err := h.cardAndModel(cfg, models, characterID)
	if err != nil {
		unseen()
		return nil, turnID, err
	}
	l, err := h.acquire(cfg, st, c, *model, in)
	if err != nil {
		unseen()
		return nil, turnID, err
	}
	// 先登记成在跑，再看硬门槛：回复前的压缩也能被停止、新的一句打断，这时来的插话收下
	if ctx.Err() != nil {
		unseen()
		return &Result{Built: l.b, TurnID: turnID, Stopped: true}, turnID, nil
	}
	run := &turnRun{id: turnID, cancel: cancel, done: make(chan struct{}), model: modelUse(l)}
	if staged := stopOf(ctx); staged != nil {
		run.cancel = staged.stop
	}
	l.mu.Lock()
	l.run = run
	l.mu.Unlock()
	h.mu.Lock()
	h.active[ck] = l
	h.mu.Unlock()
	defer func() {
		// 收尾：回合结束了还有没送到的插话（刚好在取插话之后结束、或者出错了），退回给网页当普通的一句再发（agent.ts respondNow 的 finally）。
		// 和"不在跑"一起在锁里做完，之后来的插话就当普通的一句
		l.lastUsed = h.Cfg.Now()
		l.mu.Lock()
		l.run = nil
		left := l.steer.take()
		l.mu.Unlock()
		h.mu.Lock()
		if h.active[ck] == l {
			delete(h.active, ck)
		}
		h.markDone(ck, l.turnSeqs)
		h.turnGen[ck]++
		h.mu.Unlock()
		l.steer.emitDropped(left, true)
		close(run.done)
		h.armIdleCompact(l)
	}()
	if stopped, err := h.fitHard(ctx, l, c, *model, in); err != nil || stopped {
		unseen()
		if err != nil {
			return nil, turnID, err
		}
		return &Result{Built: l.b, TurnID: turnID, Stopped: true}, turnID, nil
	}
	res, err = h.runTurn(ctx, l, c, in, turnID)
	if !in.local() && res != nil {
		// 回合定了发出去之前记下：中间件收到就可能来 Commit
		l.pending = &pendingTurn{id: turnID, from: l.turnFrom, to: len(l.msgs), seqs: append([]int64(nil), l.turnSeqs...), batchSeqs: append([]int64(nil), in.Seqs...), ctx: l.turnCtx}
		if err != nil || res.Stopped || len(res.Said) == 0 {
			// 结局不是 done（中间件整轮丢掉、不写库，规格-渠道中间件 10.1），或者一句都没说：不会来 Commit，这一段当场照库重写
			if e := l.settle(st, nil); e != nil {
				h.plugins.logf("回合 %s 照库重写没成，她说的那几句直接拿掉：%v", turnID, e)
				l.removePending()
			}
			// 这一轮没说话（不会有 Commit）也要存参考信息块：群里她多半不开口，这样的回合占大多数。出错只记日志，只是下次重建少这一块
			if err == nil && !res.Stopped {
				if e := h.putRefBlocks(st, l, 0); e != nil {
					h.plugins.logf("回合 %s 存参考信息块没成（下次重建这一块会断）：%v", turnID, e)
				}
			}
		}
	}
	if err == nil {
		h.compactEarly(l) // 说到一半被停下的也算（和 Bun 一样：只有出错的不压）
	}
	return res, turnID, err
}

// promptTokens 是一次请求真实的输入总量：llm.Usage.Input 已减掉缓存读和缓存写，三项加回来才是服务商看到的整段输入。
// 漏了缓存写，首次写缓存的那一步会少算一截，硬门槛就晚压
func promptTokens(u llm.Usage) int64 { return u.Input + u.CacheRead + u.CacheWrite }

// fitHard 是回复前看硬门槛（agent.ts respondNow）：上一次请求的真实输入和按现在的历史估的取大的，加上这句话还是超了，
// 本地闲聊先压缩（后台正在提前压的等它），压成了原地换上新快照；关了自动压缩的、渠道的、没压成的原地重建、只带最近的话。
// 都只做这一次、不循环。压的时候这一轮已经登记成在跑：停止、新的一句打断它，返回 stopped（不发回复的请求、实例照旧，
// 不留"上一轮被停止"的提醒：什么命令都没执行，ADR-190 第 5 条）；这时来的插话收下，压完送进这一轮
func (h *Hub) fitHard(ctx context.Context, l *Live, c persona.Character, m llm.Model, in Input) (stopped bool, err error) {
	size := max(int(l.lastTotalInput), l.b.Fixed+conversationTokens(l.msgs))
	if float64(size+sumikaTokens(in.Text)+600) <= l.b.Hard {
		return false, nil
	}
	ok := false
	if l.b.CompactKind == "summary" {
		l.mu.Lock()
		l.compacting = true
		l.mu.Unlock()
		r := h.compactOrWait(ctx, l, "rule", 0)
		l.mu.Lock()
		l.compacting = false
		l.mu.Unlock()
		if ctx.Err() != nil {
			return true, nil
		}
		if ok = r.OK; !ok {
			h.plugins.warn("会话 %s 压缩没成（%s）：只带最近的话重建", in.Conversation, r.Err)
		}
	} else {
		h.plugins.warn("会话 %s 的上下文快满了（约 %d token，上限 %.0f）：只带最近的话重建", in.Conversation, size, l.b.Hard)
	}
	reason := RebuildOverLimitSnap
	if !ok {
		reason = RebuildOverLimitTrim
	}
	if err := h.rebuildIn(l, c, m, in, !ok, reason); err != nil {
		return false, err
	}
	// 聊天里只提两种：回复前必须同步等压缩，裁剪兜底真的丢了内容（ADR-388 第 5 节第 4 条）。
	// 后台提前压、空闲压不在聊天里说，运行页的 context_epochs / model_runs 有留痕
	if ok {
		h.noteCompact(in, "之前的聊天有点长，回复前先整理了一下")
	} else if lost := l.b.Trace.Dropped - l.b.Trace.Covered; lost > 0 {
		// Dropped 是带上的第一条之前的全部条数，其中被旧摘要覆盖的不算这次丢掉：那些内容还在快照里
		h.noteCompact(in, fmt.Sprintf("更早的 %d 条聊天这次放不下了，只带了最近的一部分", lost))
	}
	return false, nil
}

// compactNoticeSource 是聊天里“整理过了”那行的来源名（界面画成居中通知条，和“从这里开始新话题”同一套）
const compactNoticeSource = "compact"

// noteCompact 在本地私聊里留一行系统通知；别的场合、别的会话不写。写不进去只记日志，不影响这一轮
func (h *Hub) noteCompact(in Input, text string) {
	if !in.local() || in.Conversation != consoleConv(in.CharacterID) || !ownerConsoleAudience(in.Audience) {
		return
	}
	st, err := h.storeOf(in.Conversation)
	if err != nil {
		return
	}
	channel, _, _ := strings.Cut(in.Conversation, ":")
	if _, err := st.AppendEvent(store.NewEvent{Channel: channel, Conversation: in.Conversation, ActorKind: "system", ActorID: compactNoticeSource,
		Origin: "system:" + compactNoticeSource, Kind: "notice", Text: text,
		Segments:  map[string]any{"notice": "system", "source": compactNoticeSource},
		CreatedAt: h.now().UnixMilli()}, []string{in.CharacterID}); err != nil {
		h.plugins.logf("会话 %s 整理过以后的提示没写成：%v", in.Conversation, err)
	}
}

// compactOrWait 压这个会话一次：后台正在提前压的就等它、用它的结局（同一个会话同时只压一个），不然当场压。
// 等的时候被停下就不等了，后台那一次照常做完。提前压不带切点上限（只按压缩前钩子交回的收紧，钩子超时就不收紧）：
// 这次带了 maxSeq、后台那一次切过了它，就不算这次压成（那一份照样是最新的快照，下一轮照样换上）
func (h *Hub) compactOrWait(ctx context.Context, l *Live, trigger string, maxSeq int64) Compacted {
	h.mu.Lock()
	job := h.compactJobs[convKey(l.b.CharacterID, l.b.Conversation)]
	h.mu.Unlock()
	if job == nil {
		return h.compactNow(ctx, l.compactSource(l.cfg), trigger, maxSeq, nil)
	}
	h.compactingNotice(l.cfg, l.b, true)
	defer h.compactingNotice(l.cfg, l.b, false)
	if h.testHook != nil {
		h.testHook("waitCompact")
	}
	select {
	case <-job.done:
		if r := job.res; maxSeq > 0 && r.OK && r.FromSeq > maxSeq {
			return Compacted{Err: fmt.Sprintf("后台提前压已经切到第 %d 条，晚于这次的上限（第 %d 条），这次不再压", r.FromSeq, maxSeq)}
		}
		return job.res
	case <-ctx.Done():
		return Compacted{Stopped: true, Err: "等后台压缩的时候被停下了"}
	}
}

// compactEarly 是回合没出错地结束以后（说到一半被停下的也算）看要不要提前压（agent.ts respondNow 末尾，PREPARE_RATIO）：过了硬门槛的八成，趁主人还没说下一句
// 在后台压好，她说下一句时换上，不用等。只压本地闲聊（summary）；后台已经在压的、上一次没成而上下文还没长 10% 的不压（每次都要
// 调一次模型、强制整理一次记忆，Bun 2026-10-19 体检）；库里已经有比实例新的快照也不压（上一次提前压在这一轮跑的时候写好了，
// 下一轮才换上：Bun 这时会再压一次）。材料连同配置在回合队列里取好，后台只碰材料、库、模型和插件，不占回合队列
func (h *Hub) compactEarly(l *Live) {
	b := l.b
	after := max(int(l.lastTotalInput), b.Fixed+conversationTokens(l.msgs))
	if b.CompactKind != "summary" || float64(after) <= b.Hard*0.8 {
		return
	}
	conv := b.Conversation
	ck := convKey(b.CharacterID, conv)
	h.mu.Lock()
	failed, hasFailed := h.compactFailures[ck]
	busy := h.compactJobs[ck] != nil
	h.mu.Unlock()
	if busy || (hasFailed && float64(after) < float64(failed.size)*1.1) {
		return
	}
	st, err := h.storeOf(conv)
	if err != nil {
		return
	}
	start, latestID, err := compactBaseline(st, b.CharacterID, conv)
	if err != nil || latestID != b.EpochSeen {
		return
	}
	src := l.compactSource(l.cfg)
	identity := l.in.Identity
	// 后台压缩独占 Run、沿用原 Root 余额；前台不等它，宿主 Drain 仍等待真实退出。
	identity.RunID = "compact:" + store.NewID(h.Cfg.Now())
	identity.Kind = kernel.KindCompact
	activityCtx, activity, err := h.Cfg.Lifetime.Begin(h.base, identity)
	if err != nil {
		return
	}
	activityCtx = kernel.WithInvocation(activityCtx, identity)
	ctx, cancel := context.WithCancel(activityCtx)
	job := &compactJob{done: make(chan struct{}), cancel: cancel}
	// 登记和"有没有人在压"在同一把锁里：空闲压缩可能刚好排上，两边只有一个能登记
	h.mu.Lock()
	if h.compactJobs[ck] != nil {
		h.mu.Unlock()
		cancel()
		activity.End(nil)
		return
	}
	h.compactJobs[ck] = job
	guard := &compactGuard{start: start, latestID: latestID, model: src.b.Model.ID, modelVersion: src.cfg.ModelVersion, gen: h.staleGen[ck], job: job}
	h.mu.Unlock()
	h.bg.Add(1)
	go func() {
		defer h.bg.Done()
		defer activity.End(nil)
		defer cancel()
		job.res = h.compactNow(ctx, src, "early", 0, guard)
		h.mu.Lock()
		delete(h.compactJobs, ck)
		if job.res.OK {
			delete(h.compactFailures, ck)
		} else if !job.res.Stopped && !job.res.Void {
			// 取消、作废都不算失败：开新话题、检查点回退以后不替一次没写库的压缩退避
			h.compactFailures[ck] = compactFailure{size: after, at: h.now()}
		}
		h.mu.Unlock()
		close(job.done)
		if !job.res.OK && job.res.EpochID != "" {
			h.plugins.warn("会话 %s 后台提前压缩没用上：%s", conv, job.res.Err)
		}
	}()
}

// 空闲压缩失败以后，同一个会话这么久之内不再试（ADR-388 第 3 条）
const idleCompactRetry = 6 * time.Hour

// idleFailedRecently 是空闲压缩的失败退避：内存里的记录先看（快），重启后内存空了再看库里这个角色、这个会话
// 最近一行 trigger='idle' 的失败（context_epochs.created_at 毫秒）。查询出错不挡压缩：库真坏了，模型那一步也会报
func (h *Hub) idleFailedRecently(characterID, conversation string) bool {
	ck := convKey(characterID, conversation)
	h.mu.Lock()
	failed, hasFailed := h.compactFailures[ck]
	h.mu.Unlock()
	if hasFailed && h.now().Sub(failed.at) < idleCompactRetry {
		return true
	}
	st, err := h.storeOf(conversation)
	if err != nil {
		return false
	}
	var at int64
	err = st.Events.QueryRow(`SELECT created_at FROM context_epochs WHERE conversation = ? AND character_id = ? AND trigger = 'idle' AND status = 'failed' ORDER BY created_at DESC, id DESC LIMIT 1`,
		conversation, characterID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		h.plugins.logf("会话 %s 的空闲压缩失败记录没读出来：%v", conversation, err)
		return false
	}
	return h.now().Sub(time.UnixMilli(at)) < idleCompactRetry
}

// now 是这台机器的钟（测试注入假时钟）；Config 里没给就用 time.Now
func (h *Hub) now() time.Time {
	if h.Cfg.Now != nil {
		return h.Cfg.Now()
	}
	return time.Now()
}

// defaultAfterFunc 是空闲压缩的计时器（测试换成假时钟手动触发）
func defaultAfterFunc(d time.Duration, f func()) (stop func()) {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// armIdleCompact 在本地私聊的一轮结束以后排一次空闲压缩（ADR-388 第 3 条）：独立计时，不挂在“下一次取实例时顺手回收”上。
// 实例现在就不够 agent.idleCompact.minTokens 的不排（没有新消息，闲满也不会变大）；闲满以后还会再核一遍有没有更新的快照、
// 有没有人在压、失败退避。计时器可注入（Config.AfterFunc），新消息、新话题、检查点回退、别的线写了话都会取消它
func (h *Hub) armIdleCompact(l *Live) {
	b, cfg := l.b, l.cfg
	if !cfg.IdleCompact || b == nil || b.CompactKind != "summary" || !idleCompactLocal(cfg, l.in) {
		return
	}
	size := max(int(l.lastTotalInput), b.Fixed+conversationTokens(l.msgs))
	if size < cfg.idleCompactMinTokens() {
		return
	}
	// 失败退避：内存里有最近的失败就不排；重启以后内存空了，再看库里最近一行 idle 失败（别的 trigger 不挡）。
	// 不排的话下一条消息结束时会再看一次，过了 6 小时自然恢复
	if h.idleFailedRecently(l.in.CharacterID, l.in.Conversation) {
		return
	}
	ck := convKey(l.in.CharacterID, l.in.Conversation)
	state := &idleCompactTimer{live: l}
	h.mu.Lock()
	// 还有排着队的一批：它马上会跑，这一轮不是这个会话的最后一轮
	if h.base.Err() != nil || h.lifecycle.sealed.Load() || len(h.queued[ck]) > 0 {
		h.mu.Unlock()
		return
	}
	state.gen = h.turnGen[ck]
	if old := h.idleTimers[ck]; old != nil {
		if old.stop != nil {
			old.stop()
			old.stop = nil
		}
	}
	h.idleTimers[ck] = state
	h.mu.Unlock()
	factory := h.Cfg.AfterFunc
	if factory == nil {
		factory = defaultAfterFunc
	}
	stop := factory(cfg.idleCompactAfter(), func() { h.idleTimerFired(ck, state) })
	h.mu.Lock()
	if h.idleTimers[ck] != state {
		// 这中间被取消了（新消息到了）：停掉计时器，别再触发
		h.mu.Unlock()
		if stop != nil {
			stop()
		}
		return
	}
	state.stop = stop
	h.mu.Unlock()
}

// cancelIdleLocked 取消这个会话排着的空闲压缩。调用方拿着 h.mu；返回的停止函数在锁外调
func (h *Hub) cancelIdleLocked(ck string) (stop func()) {
	state := h.idleTimers[ck]
	if state == nil {
		return nil
	}
	delete(h.idleTimers, ck)
	stop, state.stop = state.stop, nil
	return stop
}

// cancelIdleCompact 取消这个会话排着的空闲压缩（新消息、开始新话题、检查点回退、手动压缩、别的线写了话）
func (h *Hub) cancelIdleCompact(characterID, conversation string) {
	h.mu.Lock()
	stop := h.cancelIdleLocked(convKey(characterID, conversation))
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// cancelCompact 停掉这个会话正在后台压的那一次（开新话题时结果已经用不上了）。没在压就什么都不做；
// 正好压完的，cancel 一个已经结束的 ctx 也无害
func (h *Hub) cancelCompact(characterID, conversation string) {
	h.mu.Lock()
	job := h.compactJobs[convKey(characterID, conversation)]
	h.mu.Unlock()
	if job != nil && job.cancel != nil {
		job.cancel()
	}
}

// cancelAllIdleTimers 是关机时用的：不再新开压缩，排着的计时器都停掉
func (h *Hub) cancelAllIdleTimers() {
	h.mu.Lock()
	stops := make([]func(), 0, len(h.idleTimers))
	for ck := range h.idleTimers {
		if stop := h.cancelIdleLocked(ck); stop != nil {
			stops = append(stops, stop)
		}
	}
	h.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
}

// idleTimerFired 是空闲计时到点：把核对和取材料排进这个会话的队列（不和回合同时改实例），模型调用再放后台。
// 关机的路上不再新开请求
func (h *Hub) idleTimerFired(ck string, state *idleCompactTimer) {
	h.mu.Lock()
	if h.idleTimers[ck] != state {
		h.mu.Unlock() // 已经被取消
		return
	}
	delete(h.idleTimers, ck)
	h.mu.Unlock()
	if h.base.Err() != nil || h.lifecycle.sealed.Load() {
		return
	}
	// 只有真的排上活才开始等它：排着没到点的计时器不该让 Idle 等到下次对话
	h.bg.Add(1)
	h.enqueue(ck, func() { h.startIdleCompact(ck, state) })
}

// idleCompactRoot 给空闲压缩开一个独立的后台预算根（结果的身份也换成这个根，模型调用照它记账）。
// 不能沿用最后一轮的根：自动回合的根只有 15 分钟，等 50 分钟再压时早过期了。
// 令牌上限跟这一份实例的大小走：顶层那组按无人值守的常态取 524288，四十万以上的实例重试一次就撞顶，
// 窗口更大的模型一次都过不了。取默认值和实例三倍里大的（中文按字节估请求，三倍≈一份摘要请求的量）；请求次数和时限不变
func (h *Hub) idleCompactRoot(identity kernel.Invocation, size int) (kernel.Invocation, error) {
	identity.RunID = "compact:" + store.NewID(h.now())
	identity.RootID = identity.RunID
	identity.Kind = kernel.KindCompact
	limits := h.Cfg.BudgetConfig.Limits(time.Now())
	if floor := int64(size) * 3; floor > limits.MaxTokens {
		limits.MaxTokens = floor
	}
	// 预算时限是 context 的绝对时刻，和手工压缩一样用墙钟；别的时间戳（失败退避）仍走 h.now()
	return identity, AdmitBudgetRoot(h.base, h.Cfg.Budget, identity, limits)
}

// startIdleCompact 在会话队列里核对空闲压缩的条件、取材料、登记任务；模型调用交给后台协程，不挡下一轮
func (h *Hub) startIdleCompact(ck string, state *idleCompactTimer) {
	defer h.bg.Done()
	if h.base.Err() != nil || h.lifecycle.sealed.Load() {
		return
	}
	l := state.live
	key := liveKey(l.in)
	h.mu.Lock()
	if h.live[key] != l || h.compactJobs[ck] != nil || h.turnGen[ck] != state.gen || h.active[ck] != nil || len(h.queued[ck]) > 0 {
		h.mu.Unlock()
		return
	}
	gen := h.staleGen[ck]
	failed, hasFailed := h.compactFailures[ck]
	h.mu.Unlock()
	cfg := l.cfg
	if !cfg.IdleCompact || l.b.CompactKind != "summary" || !idleCompactLocal(cfg, l.in) {
		return
	}
	size := max(int(l.lastTotalInput), l.b.Fixed+conversationTokens(l.msgs))
	if size < cfg.idleCompactMinTokens() {
		return
	}
	if hasFailed && h.now().Sub(failed.at) < idleCompactRetry {
		return
	}
	st, err := h.storeOf(l.b.Conversation)
	if err != nil {
		return
	}
	start, latestID, err := compactBaseline(st, l.b.CharacterID, l.b.Conversation)
	if err != nil {
		return
	}
	if latestID != l.b.EpochSeen {
		// 已经有更新的快照（提前压写好了、还没换上）：同一份内容不各跑一次
		return
	}
	src := l.compactSource(cfg)
	identity, err := h.idleCompactRoot(l.in.Identity, size)
	if err != nil {
		h.plugins.logf("会话 %s 的空闲压缩没开成预算根：%v", l.b.Conversation, err)
		return
	}
	activityCtx, activity, err := h.Cfg.Lifetime.Begin(h.base, identity)
	if err != nil {
		return
	}
	activityCtx = kernel.WithInvocation(activityCtx, identity)
	ctx, cancel := context.WithCancel(activityCtx)
	job := &compactJob{done: make(chan struct{}), cancel: cancel}
	h.mu.Lock()
	if h.compactJobs[ck] != nil || h.staleGen[ck] != gen {
		h.mu.Unlock()
		cancel()
		activity.End(nil)
		return
	}
	h.compactJobs[ck] = job
	guard := &compactGuard{start: start, latestID: latestID, model: src.b.Model.ID, modelVersion: src.cfg.ModelVersion, gen: gen, job: job}
	h.mu.Unlock()
	h.bg.Add(1)
	go func() {
		defer h.bg.Done()
		defer activity.End(nil)
		defer cancel()
		job.res = h.compactNow(ctx, src, "idle", 0, guard)
		h.mu.Lock()
		delete(h.compactJobs, ck)
		if job.res.OK {
			delete(h.compactFailures, ck)
		} else if !job.res.Stopped && !job.res.Void {
			// 取消、作废都不算失败：开新话题、检查点回退以后不替一次没写库的压缩退避 6 小时
			h.compactFailures[ck] = compactFailure{size: src.before, at: h.now()}
		}
		h.mu.Unlock()
		close(job.done)
		if !job.res.OK && job.res.EpochID != "" {
			h.plugins.warn("会话 %s 空闲压缩没用上：%s", l.b.Conversation, job.res.Err)
		}
	}()
}

// pendingTurn 是一个渠道回合在实例里的那一段：from、to 是在 l.msgs 里的范围（那一批那条起、回合收尾时的末尾），
// seqs 是这一回合用过的事件（那一批、插话），ctx 是插话前插件追加的参考信息
type pendingTurn struct {
	id        string
	from, to  int
	seqs      []int64
	batchSeqs []int64
	ctx       []steerCtx
}

// steerCtx：插话送到时插件追加的那块参考信息在 l.msgs 的 at，照库重写时排在 before（这几句插话里最早的事件）前面
type steerCtx struct {
	at     int
	before int64
}

// Committed 是中间件写进库的一句：这一回合第几步、库里那一行的 seq、改写以后的字
type Committed struct {
	Step int
	Seq  int64
	Text string
}

// Commit 是渠道中间件裁决、改写、写完库以后，把这一回合她说的那几句交回来（ADR-184 第 3 条，规格-渠道中间件第九节第 1 条）。
// 以库为准：实例里这一回合那一段照库里的样子重写——这一回合用过的事件（那一批、插话）加上交回的这几行，照 seq 排、照从库里
// 重建的写法；没交回的步（拦下、试运行、打断的）不在库里，不留。这几行记成跑完的回合用过的（别的档追得到）、挂上步骤记录。
// 交回的每一行都要在库里、在这个会话、是她说出口的那一行（试运行的不算）、字和交回的一样、是这一回合定了以后新写的
// （比这一回合用过的事件都晚），一步一行、两步不共用一行，不然报错、什么都不改。
// 要在这个会话下一批 Respond 之前调（中间件的屏障）；排进这个会话的回合队列里做、做完才返回，不会和下一轮同时改实例
func (h *Hub) Commit(characterID, conversation, turnID string, lines []Committed) error {
	done := make(chan error, 1)
	h.enqueue(convKey(characterID, conversation), func() { done <- h.commit(characterID, conversation, turnID, lines) })
	return <-done
}

func (h *Hub) commit(characterID, conversation, turnID string, lines []Committed) error {
	ck := convKey(characterID, conversation)
	h.mu.Lock()
	var l *Live
	for k, x := range h.live {
		if strings.HasPrefix(k, ck+"|") && x.pending != nil && x.pending.id == turnID {
			l = x
		}
	}
	h.mu.Unlock()
	if l == nil {
		return fmt.Errorf("回合 %s 没有等着写库的句子（不是渠道的回合、没说完或一句都没说、已经交回来过，或者下一轮开始时已经照库重写了）", turnID)
	}
	steps := map[int]bool{}
	for _, m := range l.msgs[l.pending.from:l.pending.to] {
		if m.Turn == turnID {
			steps[m.Step] = true
		}
	}
	// 她的句子是回合定了以后才写库的：一定比这一回合用过的事件（那一批、插话）都晚（以前交回过的同字句也就冒充不了）
	var used int64
	for _, s := range l.pending.seqs {
		used = max(used, s)
	}
	byStep := map[int]Committed{}
	bySeq := map[int64]bool{}
	var seqs []int64
	for _, c := range lines {
		if !steps[c.Step] {
			return fmt.Errorf("回合 %s 没有第 %d 步说的话", turnID, c.Step)
		}
		if c.Seq == 0 {
			return fmt.Errorf("回合 %s 第 %d 步交回来的没有 seq", turnID, c.Step)
		}
		if _, dup := byStep[c.Step]; dup {
			return fmt.Errorf("回合 %s 第 %d 步交回了两次（一句只写一行 events）", turnID, c.Step)
		}
		if bySeq[c.Seq] {
			return fmt.Errorf("回合 %s 有两步交回了同一行 seq %d（一句只写一行 events）", turnID, c.Seq)
		}
		if c.Seq <= used {
			return fmt.Errorf("回合 %s 第 %d 步交回的 seq %d 不是这一回合定了以后新写的那一行", turnID, c.Step, c.Seq)
		}
		byStep[c.Step], bySeq[c.Seq] = c, true
		seqs = append(seqs, c.Seq)
	}
	st, err := h.storeOf(conversation)
	if err != nil {
		return err
	}
	got, err := history.Of(st.Events, characterID, conversation, seqs)
	if err != nil {
		return err
	}
	inDB := map[int64]history.Message{}
	for _, m := range got {
		inDB[m.Seq] = m
	}
	for _, c := range lines {
		if m, ok := inDB[c.Seq]; !ok || m.Role != "assistant" || m.Text != c.Text {
			return fmt.Errorf("回合 %s 第 %d 步交回的 seq %d 在库里不是她说出口的那一行，或者字不一样（试运行、拦下的不交回）", turnID, c.Step, c.Seq)
		}
	}
	// 这一轮拼的参考信息块写进事件库（同一个事务里清窗口起点之前的）：写不进去就报错、实例什么都不改，
	// 中间件按约定让下一批从库里重建（库里没有的块，实例里也不留）；同一块再交一次是覆盖，重做幂等（refblock.go）
	if err := h.putRefBlocks(st, l, minSeq(seqs)); err != nil {
		return fmt.Errorf("回合 %s 写参考信息块出错：%w", turnID, err)
	}
	// 步骤记录先挂（同一句 UPDATE，重做一遍也一样）：挂不上就报错、实例什么都不改，中间件可以再交一次
	if len(seqs) > 0 {
		if err := st.AttachSteps(turnID, lastOf(seqs)); err != nil {
			return err
		}
	}
	if err := l.settle(st, lines); err != nil {
		return err
	}
	h.mu.Lock()
	h.markDone(ck, seqs)
	h.mu.Unlock()
	return nil
}

// settle 把等着写库的那一段照库里的样子重写（规格-渠道中间件第九节第 1 条：以库为准，和从库里重建、别的档追上来的逐条一样）：
// 这一回合用过的事件加上交回的那几行，照 seq 排、照重建的写法（history.Of）；插话前插件追加的那块参考信息跟着那句插话
// （不进库，重建时没有，接着用的实例照旧留着）；没交回的、工具调用和结果不在库里，不留。
// 她每一步的思考不带回来（规格-提示词前缀稳定第五节）：重建出来的也没有，带了的话接着用的实例和重建的在她每句话上都差一截；
// 最后一步的思考本来就没当输入发过，所以不带回来不会让下一轮多断一次。用量照旧留着（只算输出上限，不进前缀）
func (l *Live) settle(st *store.Store, lines []Committed) error {
	p := l.pending
	seqOfStep := map[int]int64{}
	seqs := append([]int64(nil), p.seqs...)
	for _, c := range lines {
		seqOfStep[c.Step] = c.Seq
		seqs = append(seqs, c.Seq)
	}
	rendered, err := history.Of(st.Events, l.in.CharacterID, l.in.Conversation, seqs)
	if err != nil {
		return err
	}
	said := map[int64]Msg{} // 交回的 seq → 那一步原来的消息（带思考）
	var imageBatch *Msg
	batchSeqs := map[int64]bool{}
	if p.from < p.to && len(l.msgs[p.from].Images) > 0 {
		m := l.msgs[p.from]
		imageBatch = &m
		for _, seq := range p.batchSeqs {
			batchSeqs[seq] = true
		}
	}
	for _, m := range l.msgs[p.from:p.to] {
		if m.Turn == p.id {
			if seq, ok := seqOfStep[m.Step]; ok {
				said[seq] = m
			}
		}
	}
	var ctx []steerCtx
	for _, c := range p.ctx {
		if c.at >= p.from && c.at < p.to {
			ctx = append(ctx, c)
		}
	}
	sort.SliceStable(ctx, func(i, j int) bool { return ctx[i].before < ctx[j].before })
	out := make([]Msg, 0, len(rendered)+len(ctx))
	batchEmitted := false
	for _, r := range rendered {
		if imageBatch != nil && batchSeqs[r.Seq] {
			l.seen[r.Seq] = true
			if !batchEmitted {
				out = append(out, *imageBatch)
				batchEmitted = true
			}
			continue
		}
		// 排在那几句插话最早的那条前面（那条不给她看就排在之后第一条给她看的前面；之后一条都没有就不留）
		for len(ctx) > 0 && ctx[0].before <= r.Seq {
			out = append(out, l.msgs[ctx[0].at])
			ctx = ctx[1:]
		}
		m := Msg{Role: r.Role, Text: r.Text, HasText: r.Role == "assistant", Kind: KindSaid, Seq: r.Seq}
		if o, ok := said[r.Seq]; ok {
			m.Usage = o.Usage
		}
		l.seen[r.Seq] = true
		out = append(out, m)
	}
	rest := append([]Msg(nil), l.msgs[p.to:]...)
	l.msgs = append(append(l.msgs[:p.from:p.from], out...), rest...)
	l.pending = nil
	return nil
}

// removePending：照库重写不成（读库出错）时退一步，只把这一回合她说的那几句拿掉
func (l *Live) removePending() {
	kept := l.msgs[:0]
	for _, m := range l.msgs {
		if m.Turn != l.pending.id {
			kept = append(kept, m)
		}
	}
	l.msgs = kept
	l.pending = nil
}

// dropPending：上一个渠道回合说完了却没交回来（中间件没调 Commit，或者交回报错了）：照库重写那一段，她这一回合说的不算进来
// （没进库的自然不留；已经写进库、交回却报错的，按约定中间件要调 RestartIn，下一批从库里重建才读得到），记一行日志
func (l *Live) dropPending(st *store.Store, warn, fail func(string, ...any)) {
	if l.pending == nil {
		return
	}
	id := l.pending.id
	if err := l.settle(st, nil); err != nil {
		fail("回合 %s 照库重写没成，她说的那几句直接拿掉：%v", id, err)
		l.removePending()
		return
	}
	warn("回合 %s 定了以后没交回写库的结果（Commit），照库重写了这一回合，她这一回合说的不算进来", id)
}

// acquire 取或建这个会话这一档的实例（agent.ts acquire）：每轮都重新拼一遍固定前缀，和实例里的一样（模型也没换）就接着用，
// 只把这一轮的模型、思考档位、几道线换成新的，再追上别的档在这期间说过的话；不一样（改了提示词、换了模型）才重建、从库里读历史。
// st 是这个会话所在平台的库
func (h *Hub) acquire(cfg Config, st *store.Store, c persona.Character, m llm.Model, in Input) (*Live, error) {
	if h.workErr != nil {
		return nil, h.workErr
	}
	key := liveKey(in)
	sections, system, err := prefixOf(cfg, c, in)
	if err != nil {
		return nil, err
	}
	// 同一个会话的回合一个接一个跑：这时候别的回合都跑完了，done 只有跑完时才改，读它不用再锁
	h.mu.Lock()
	l := h.live[key]
	done := h.done[convKey(in.CharacterID, in.Conversation)]
	stale := h.staleGen[convKey(in.CharacterID, in.Conversation)]
	var missing string // 这一档没有实例时的原因（留痕用）；空 = 本进程头一回见，读完库再分
	if l == nil {
		missing = h.missingReason(key, convKey(in.CharacterID, in.Conversation))
	}
	h.mu.Unlock()
	// 有了新的压缩快照（这个会话压过了）也重建：新快照下一轮换上，不在回合中间换（agent.ts respond 开头比 epoch）
	latest, err := history.LatestEpoch(st.Events, in.CharacterID, in.Conversation)
	if err != nil {
		return nil, err
	}
	var latestID string
	if latest != nil {
		latestID = latest.ID
	}
	// 闲了 IdleAfter 以上的也不接着用：Bun 的定时器这时已经把它放掉了，回来的这一句从库里重建（rebuild 记 fresh）。
	// 别的会话的实例由回收放掉；这个会话自己的，回收时它的队列正开着，放不到，在这里认
	now := cfg.Now()
	// 工具清单也在固定前缀里：插件启停、注册的工具变了，下一轮重建
	decls, regs := h.plugins.toolsFor(in.Audience)
	working := workEnabled(cfg, in.Identity)
	if working {
		h.workToolOnce.Do(func() { h.workToolErr = h.Use(resultPlugin{h}) })
		if h.workToolErr != nil {
			return nil, h.workToolErr
		}
		decls, regs = h.plugins.toolsFor(in.Audience)
	} else {
		decls = withoutResultRead(decls)
		delete(regs, "result_read")
	}
	if l != nil && l.in.Identity.Scope == in.Identity.Scope && l.in.Workspace == in.Workspace && l.system == system && l.model == m.ID && tools.FixedJSON(l.b.Decls) == tools.FixedJSON(decls) && l.b.EpochSeen == latestID &&
		l.staleGen == stale && now.Sub(l.lastUsed) < cfg.idleAfter() {
		l.b.refresh(cfg, m)
		if err := l.catchUp(st, in, done); err != nil {
			return nil, err
		}
		if working && toolPairProblems(l.msgs) {
			l.cfg = cfg
			if err := h.rebuildIn(l, c, m, in, false, RebuildToolPair); err != nil {
				return nil, err
			}
		} else if working && l.in.Identity.RootID != in.Identity.RootID {
			// 新输入有新的 RootID：消息原样留着（缓存前缀不断，ADR-022），只换属于 root 的工作状态
			l.cfg = cfg
			if err := h.switchWorkRoot(l, c, m, in); err != nil {
				return nil, err
			}
		}
	} else {
		b, err := buildWith(h.plugins, cfg, st, c, m, in, sections, system, decls, done, false)
		if err != nil {
			return nil, err
		}
		b.Compacted = l != nil && l.b.EpochSeen != latestID
		if l != nil {
			b.Trace.carry(l)
			b.Trace.add(reuseBlockers(l, in, m.ID, sections, system, decls, latestID, stale, now, cfg.idleAfter()))
		} else {
			if missing == "" {
				missing = settleFirst(&b.Trace)
			}
			b.Trace.add([]string{missing})
		}
		base, err := baseOf(st, in)
		if err != nil {
			return nil, err
		}
		old := l
		l = &Live{steer: &Steering{On: cfg.OnSteer}, in: in, cfg: cfg, staleGen: stale}
		l.fill(b, base)
		h.plugins.restoreState(l, b.Restored) // 插回去的块里已经给过的记忆，别让插件再给一遍
		if err := h.restoreWork(h.base, l, in); err != nil {
			return nil, err
		}
		l.restoreImages(h.base, cfg, in, m)
		// 重建前主人按过停止、还没提醒的，带到新实例上（"命令可能已经执行了一部分"不能因为换了快照、改了提示词、
		// 实例被回收了就丢；Bun 会丢，7.1 A42）
		if old != nil {
			old.mu.Lock()
			l.stopped = old.stopped
			old.mu.Unlock()
		}
		h.mu.Lock()
		if old == nil {
			l.stopped = h.stoppedLeft[key]
		}
		delete(h.stoppedLeft, key)
		h.live[key] = l
		delete(h.gone, key)
		h.markSeen(key, convKey(in.CharacterID, in.Conversation))
		h.mu.Unlock()
	}
	l.in = in
	l.cfg = cfg
	l.toolRegs = regs
	l.lastUsed = now
	h.mu.Lock()
	h.recycleLocked(now)
	h.mu.Unlock()
	return l, nil
}

// recycleLocked 回收实例（agent.ts evictOverflow、sweep，规格第七节 T20）：闲了 IdleAfter 以上的放掉；还多于 MaxLive 个，
// 从最久没用的放起，没有能放的就先多着（Bun 也是）。Bun 用定时器每分钟扫一遍，这里在每次取实例时顺手做，不另开协程。
// 只放这个会话回合队列空着的：没有在跑、排着的回合（压缩、交回写库也排在队列里），这时它的字段不会有人在改，读得安全
// （队列尾巴关上先于这里看到它关上）；正在取实例的这个会话自己的队列在跑，所以不会放掉它自己。等着交回写库的渠道回合
// （pending）也不放：交回来时要找它。放掉前主人按过停止、还没提醒的，留在 Hub 上（stoppedLeft）。调用方拿着 h.mu
func (h *Hub) recycleLocked(now time.Time) {
	idle := func(l *Live) bool {
		if tail := h.turnTail[convKey(l.in.CharacterID, l.in.Conversation)]; tail != nil {
			select {
			case <-tail:
			default:
				return false
			}
		}
		return l.pending == nil
	}
	drop := func(key string, l *Live, reason string) {
		stop := h.cancelIdleLocked(convKey(l.in.CharacterID, l.in.Conversation))
		h.noteGone(key, reason)
		l.mu.Lock()
		if l.stopped != nil {
			h.stoppedLeft[key] = l.stopped
		}
		l.mu.Unlock()
		if h.Cfg.ForgetContext != nil {
			h.Cfg.ForgetContext(l.contextID)
		}
		delete(h.live, key)
		if stop != nil {
			stop()
		}
	}
	cutoff := now.Add(-h.Cfg.idleAfter())
	for key, l := range h.live {
		if idle(l) && l.lastUsed.Before(cutoff) {
			// 等着空闲压缩的先留着：计时器到点还要用这个实例（ADR-388）。挤掉（下面 MaxLive）照旧
			if h.idleTimers[convKey(l.in.CharacterID, l.in.Conversation)] != nil {
				continue
			}
			drop(key, l, RebuildIdle)
		}
	}
	for len(h.live) > h.Cfg.maxLive() {
		var oldKey string
		var oldest *Live
		for key, l := range h.live {
			if idle(l) && (oldest == nil || l.lastUsed.Before(oldest.lastUsed)) {
				oldKey, oldest = key, l
			}
		}
		if oldest == nil {
			break
		}
		drop(oldKey, oldest, RebuildEvicted)
	}
}

// fill 把建好的 b 装进实例（新建、回复前压完原地重建都走它）：历史、模型，和"这个实例里给过什么"（对面是谁、报时、计划提醒、
// 插件的存储）、见过哪些、从哪条往后追、上一个渠道回合那一段都从头算。正在跑的这一轮（run、compacting、这一步说出口的和计时）、
// 工具注册绑定、插话、还没提醒的停止、建它的那一档不动：原地重建时这一轮照常跑下去。
// 从库里读进来的历史登记成见过：其中可能有比这一批更晚、跑完的回合用过的（别的档交回的那几句），复用时 catchUp 不能再追一次
func (l *Live) fill(b *Built, base int64) {
	if l.contextID != "" && l.cfg.ForgetContext != nil {
		l.cfg.ForgetContext(l.contextID)
	}
	l.b, l.msgs, l.system, l.model = b, b.Msgs, b.System, b.Model.ID
	l.reasoningModel = b.Model.ID
	l.relation, l.approvalRule, l.lastClockAt, l.lastTotalInput, l.first, l.todoChecked = b.Relation, "", b.ClockAt, 0, true, false
	l.workState, l.workRecord = kernel.WorkState{}, workRecord{}
	l.seen, l.baseSeq, l.turnSeqs = map[int64]bool{}, base, nil
	l.pending, l.turnFrom, l.turnCtx, l.state = nil, 0, nil, map[string]map[string]any{}
	l.contextID = store.NewID(time.Now())
	for _, m := range b.Msgs {
		if m.Seq != 0 {
			l.seen[m.Seq] = true
		}
	}
}

// baseOf 是新实例从哪条往后追：这一批最后一条（建实例时跳过的、还排着队的以后被别的档的回合用过了，还追得到）；
// 没有这一批时从这个会话所在的库里最新的算
func baseOf(st *store.Store, in Input) (int64, error) {
	if base := lastOf(in.Seqs); base > 0 {
		return base, nil
	}
	return history.MaxSeq(st.Events, in.Conversation)
}

// rebuildIn 原地重建这个实例（回复前过了硬门槛）：压成了换上新快照；trim（压不了、没压成）只带最近 keepTokens 的话。
// 不换对象：这一轮已经登记成在跑，停止、插话认的是这个实例。固定前缀照旧（这一轮 acquire 刚拼过）
func (h *Hub) rebuildIn(l *Live, c persona.Character, m llm.Model, in Input, trim bool, reason string) error {
	st, err := h.storeOf(in.Conversation)
	if err != nil {
		return err
	}
	h.mu.Lock()
	done := h.done[convKey(in.CharacterID, in.Conversation)]
	h.mu.Unlock()
	b, err := buildWith(h.plugins, l.cfg, st, c, m, in, l.b.Sections, l.system, l.b.Decls, done, trim)
	if err != nil {
		return err
	}
	b.Compacted = b.EpochSeen != l.b.EpochSeen
	b.Trace.carry(l)
	b.Trace.add([]string{reason})
	base, err := baseOf(st, in)
	if err != nil {
		return err
	}
	l.fill(b, base)
	h.plugins.restoreState(l, b.Restored)
	if err := h.restoreWork(h.base, l, in); err != nil {
		return err
	}
	l.restoreImages(h.base, l.cfg, in, m)
	return nil
}

// restoreImages 重建实例后把历史里主人发过的图按事件从媒体库重新取回，放回原来那条消息上（不存 base64，也不改文字，前缀和第一次发时一样）。
// 哪些事件有图、能不能带由宿主判断（和第一次直给同一套条件）；取不到的留文字占位，宿主自己写日志。
func (l *Live) restoreImages(ctx context.Context, cfg Config, in Input, m llm.Model) {
	if cfg.RestoreImages == nil {
		return
	}
	for i := range l.msgs {
		if l.msgs[i].Role != "user" || l.msgs[i].Kind != KindSaid || l.msgs[i].Seq == 0 || len(l.msgs[i].Images) > 0 {
			continue
		}
		if images := cfg.RestoreImages(ctx, in, l.msgs[i].Seq, m); len(images) > 0 {
			l.msgs[i].Images = images
			l.msgs[i].imageSeqs = []int64{l.msgs[i].Seq}
		}
	}
}

// catchUp 把建实例以后、这个实例没见过的消息（别的档那几轮主人、群友说的和她回的）按先后追加在末尾：
// 只追加不插中间，前缀照旧走缓存；别的档的参考信息和工具结果不进库，追不过来（外部审计 F02）。
// 只追跑完的回合用过、说过的（done）：还排着队的那几句已经在库里，轮到它们那一轮才进；这一批本身也不追（这一轮单独追加）
func (l *Live) catchUp(st *store.Store, in Input, done map[int64]bool) error {
	ms, err := history.After(st.Events, in.CharacterID, in.Conversation, l.baseSeq)
	if err != nil {
		return err
	}
	batch := map[int64]bool{}
	for _, s := range in.Seqs {
		batch[s] = true
	}
	for _, m := range ms {
		groupHistory := in.local() && in.Audience.Scene == "group" && m.Seq <= lastOf(in.Seqs)
		if l.seen[m.Seq] || batch[m.Seq] || (!done[m.Seq] && !groupHistory) {
			continue
		}
		l.seen[m.Seq] = true
		l.msgs = append(l.msgs, Msg{Role: m.Role, Text: m.Text, HasText: m.Role == "assistant", Kind: KindSaid, Seq: m.Seq})
	}
	return nil
}

// markDone 把跑完的这一轮用过、说过的事件记进这个会话的 done（调用方拿着 h.mu）；
// 比这个会话所有活着的实例的 baseSeq、所有排着队的那几批都旧的再也用不到，清掉（常开的群不越攒越多）
func (h *Hub) markDone(ck string, seqs []int64) {
	d := h.done[ck]
	if d == nil {
		d = map[int64]bool{}
		h.done[ck] = d
	}
	for _, s := range seqs {
		d[s] = true
	}
	floor, found := int64(0), false
	for k, l := range h.live {
		if strings.HasPrefix(k, ck+"|") && (!found || l.baseSeq < floor) {
			floor, found = l.baseSeq, true
		}
	}
	for _, q := range h.queued[ck] {
		if !found || q < floor {
			floor, found = q, true
		}
	}
	for s := range d {
		if s <= floor {
			delete(d, s)
		}
	}
}

// markQueued / unmarkQueued：这一批排上队 / 开始跑（Hub.queued）
func (h *Hub) markQueued(in Input) {
	ck := convKey(in.CharacterID, in.Conversation)
	h.mu.Lock()
	h.queued[ck] = append(h.queued[ck], lastOf(in.Seqs))
	h.mu.Unlock()
}

func (h *Hub) unmarkQueued(in Input) {
	ck, last := convKey(in.CharacterID, in.Conversation), lastOf(in.Seqs)
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.queued[ck]
	for i, x := range q {
		if x == last {
			h.queued[ck] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	if len(h.queued[ck]) == 0 {
		delete(h.queued, ck)
	}
}

// stateOf 是这个实例里给某个插件的存储
func (l *Live) stateOf(plugin string) map[string]any {
	s := l.state[plugin]
	if s == nil {
		s = map[string]any{}
		l.state[plugin] = s
	}
	return s
}

func conversationTexts(msgs []Msg) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != "system" {
			out = append(out, m.sumikaText())
		}
	}
	return out
}

func imageTokens(m Msg) int { return ceilDiv35(3000 * len(m.Images)) }

func conversationTokens(msgs []Msg) int {
	n := sumikaMessages(conversationTexts(msgs)...)
	for _, m := range msgs {
		if m.Role != "system" {
			n += imageTokens(m)
		}
	}
	return n
}

// channelBatch 是渠道回合这一批照库里的写法逐条（history.Of，每个 seq 一条），和回合定了以后照库重写（settle）、
// 从库里重建出来的一个样：下一轮的请求在这里不断开（cache-audit-20261113 第 2.2 节；以前拼成一条多行，交回后变成 N 条，
// 每个多条批次的下一轮都在这批的位置丢缓存）。in.Text 照旧给插件、闸门和 kernel_inbox。
// 返回空就照旧拼成一条：本地的回合（主人那一句由内核存，Bun 录像对的是它）、子运行、带图的批次（整批一条带图，交回时也整批留着）、
// 识图兜底改过文字的、有分块的；以及库里读出来的和开口时排好的不一样的：条数和 seq 对不上，或者拼起来和 in.Text 不是逐字节相同
// （入库时排的那一行可能比开口时少东西，比如被引那条的链接摘要后来才有；宁可照旧这一处断一次，也不让她这一轮少看到）。
// 后两种退回各记一行日志，线上看得出有多少批次没走逐条
func (h *Hub) channelBatch(st *store.Store, in Input, text string, images []llm.Image) []Msg {
	if in.local() || in.Identity.Kind == kernel.KindChild || len(in.Seqs) == 0 || len(images) > 0 || len(in.Parts) > 0 || text != in.Text {
		return nil
	}
	rows, err := history.Of(st.Events, in.CharacterID, in.Conversation, in.Seqs)
	if err != nil {
		h.plugins.logf("会话 %s 这一批照库逐条读出错，照旧拼成一条：%v", in.Conversation, err)
		return nil
	}
	if len(rows) != len(in.Seqs) {
		h.plugins.warn("会话 %s 这一批库里读出 %d 条、批次有 %d 条，照旧拼成一条", in.Conversation, len(rows), len(in.Seqs))
		return nil
	}
	out := make([]Msg, len(rows))
	texts := make([]string, len(rows))
	for i, r := range rows {
		out[i] = Msg{Role: r.Role, Text: r.Text, HasText: r.Role == "assistant", Kind: KindSaid, Seq: r.Seq}
		texts[i] = r.Text
	}
	if strings.Join(texts, "\n") != in.Text {
		h.plugins.logf("会话 %s 这一批库里的写法和开口时排的不一样（%d 条），照旧拼成一条", in.Conversation, len(rows))
		return nil
	}
	return out
}

// buildContext 是这一轮的参考信息（agent.ts buildContext）：对面是谁（这个实例里第一次或者变了才给）、插件给的几行（记忆这类）、
// 上一轮被主人停了（说一次）、上一轮被进程退出打断时还在跑的工具（说一次，表一 A9）、报时（隔 15 分钟才再报）。
// 什么新东西都没有就不加这一段，返回空
func (l *Live) buildContext(now time.Time, st *store.Store, conversation, relation string, stopped *string, interrupted []string, extra []string) string {
	var parts []string
	if relation != "" && l.relation != relation {
		l.relation = relation
		parts = append(parts, relation)
	}
	// 审批规则：本地私聊里第一次给、之后审批档变了才再给（输入框切档当场生效，说法跟着变，但不动固定前缀）
	if f := l.cfg.ApprovalRuleOf; f != nil && strings.HasPrefix(conversation, "console:") && !strings.Contains(conversation, ":group:") {
		if r := f(conversation); r != "" && r != l.approvalRule {
			l.approvalRule = r
			parts = append(parts, "审批规则（以这一条为准，所有者可能随时切档）："+r)
		}
	}
	// 插件的几行排在对面是谁之后、计划提醒和停止提醒之前（规格-Go后台 四点八 3c）
	parts = append(parts, extra...)
	// 计划清单的重建后提醒（todo.ts 的 turn/context）：每个实例只看一次（活着的实例历史里本来就有清单）
	if !l.todoChecked {
		l.todoChecked = true
		if line := todoReminder(l.workState); line != "" {
			parts = append(parts, line)
		}
	}
	if stopped != nil {
		parts = append(parts, "上一轮被所有者手动停止了。没做完的命令可能已经执行了一部分，先确认现在的状态再继续，不要直接重做。")
		if *stopped != "" {
			parts = append(parts, "你被停住之前已经说出口的是：「"+clip(*stopped, 300)+"」")
		}
	}
	if len(interrupted) > 0 {
		parts = append(parts, "上一轮还没做完，后台就退出了："+strings.Join(interrupted, "、")+"跑到一半，不知道结果，可能已经执行了一部分。先确认现在的状态再继续，不要直接重做。")
	}
	clockDue := l.lastClockAt.IsZero() || now.Sub(l.lastClockAt) >= clockEvery
	if len(parts) == 0 && !clockDue {
		return ""
	}
	lines := []string{ContextHeader}
	if clockDue {
		lines = append(lines, "现在是"+NowText(now)+"。")
		l.lastClockAt = now
	}
	return strings.Join(append(lines, parts...), "\n")
}

// runTurn 跑一轮（agent.ts runTurn + pi 的 agent-loop）：追加参考信息和这句话，一步一步请求，直到她不再调工具、也没有插话
func (h *Hub) runTurn(ctx context.Context, l *Live, c persona.Character, in Input, turnID string) (*Result, error) {
	cfg, b := l.cfg, l.b
	cfg.invocation = in.Identity
	cfg.invocation.RunID = turnID
	characterID, conversation, text := in.CharacterID, in.Conversation, in.Text
	late := h.openLate(in.Identity.RunID)
	defer h.closeLate(in.Identity.RunID)
	var st *store.Store
	var err error
	if in.Identity.Kind == kernel.KindChild {
		st = h.trace()
	} else {
		st, err = h.storeOf(conversation)
	}
	if err != nil {
		return nil, err
	}
	var identity kernel.Invocation
	var cancel context.CancelFunc
	ctx, identity, cancel, err = h.beginBudget(ctx, cfg, in, turnID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	b.Invocation = identity
	cfg.invocation = identity
	l.dropPending(st, h.plugins.warn, h.plugins.fail)
	cfg.timeline = newTimeline(cfg.Now)
	endTurn := cfg.timeline.begin("turn.total", "")
	defer func() {
		endTurn(ctx.Err() != nil)
		cfg.timeline.flush(st, turnID, h.plugins.logf)
	}()
	pluginIn := func(text string, steer bool) ContextIn {
		return ContextIn{timeline: cfg.timeline, Identity: in.Identity, CharacterID: characterID, Conversation: conversation, Scope: in.Scope, Audience: in.Audience, Addressed: in.Addressed, Text: text, Steer: steer, ContextID: l.contextID, Workspace: in.Workspace}
	}
	// 这一轮从这里开始计时：Bun 版在拼参考信息之前就记下开始的时刻（runTurn），首字、总耗时都从这里算；之后每一步结束重新计
	startedAt := cfg.Now()
	l.mu.Lock()
	l.stepStarted = startedAt
	l.mu.Unlock()
	guard := &repeatGuard{} // 新的一轮从头数（agent.ts：respond 开头 live.guard.reset()）
	var lastSeq int64
	recalls := &recallSeq{} // 这一轮自动召回记下的步骤编号，和它排在第几步之前
	l.turnSeqs, l.turnCtx, l.refBlocks = nil, nil, nil
	inputSeqs := append([]int64(nil), in.Seqs...)
	mark := func(s int64) {
		l.seen[s] = true
		l.turnSeqs = append(l.turnSeqs, s)
	}
	for _, s := range in.Seqs {
		mark(s)
		lastSeq = max(lastSeq, s)
	}
	// 上一轮被停下的提醒在这一轮一开始就取走：拼参考信息（插件钩子）期间再按停止，记下的留给下一轮，不被这一轮用掉
	l.mu.Lock()
	stopped := l.stopped
	l.stopped = nil
	l.mu.Unlock()
	relation := in.Relation
	if relation == "" && in.local() {
		relation = fmt.Sprintf(ownerRelation, b.ownerLabel())
	}
	hasNotes := false // 悄悄话的处理记录并进了关系行：它不随档、卡、记忆变，也没法核对，带它的块不存（refblock.go）
	if cfg.Notes != nil && in.Identity.Kind != kernel.KindChild {
		if n := cfg.Notes(characterID, conversation); n != "" {
			relation = strings.TrimSpace(relation + "\n" + n)
			hasNotes = true
		}
	}
	// 之前被进程退出打断、有工具不知道结果、还没提醒到她的（表一 A9）：从库里查，再被杀一次也不丢；拼过参考信息、正常结束过一轮就不再提
	// （store.InterruptedTools）。只给本地的回合，和停止提醒一样（渠道的回合由中间件管）
	var interrupted []string
	if in.local() {
		names, err := st.InterruptedTools(characterID, conversation, turnID)
		if err != nil {
			h.plugins.warn("查之前有没有被打断出错：%v", err)
		}
		interrupted = names
	}
	historyEnd := len(l.msgs) // 溢出只压前面的轮次；本轮参考信息、工具调用和结果原样接回，不重跑工具
	var images []llm.Image
	if in.Identity.Kind != kernel.KindChild && cfg.PrepareImages != nil {
		endImages := cfg.timeline.begin("context.images", "")
		text, images = cfg.PrepareImages(ctx, in, b.Model)
		endImages(false)
	}
	var pending []contextContribution
	if in.Identity.Kind != kernel.KindChild {
		pending = h.plugins.contextContributions(ctx, pluginIn(text, false), l.stateOf)
	}
	if h.testHook != nil {
		h.testHook("contextReady")
	}
	batch := h.channelBatch(st, in, text, images)
	var block string
	h.plugins.acceptContext(ctx, pending, func(got acceptedContext) {
		h.noteRecall(st, in, turnID, startedAt, recalls, got.Recall)
		block = l.buildContext(startedAt, st, conversation, relation, stopped, interrupted, got.Lines)
		if block != "" {
			l.msgs = append(l.msgs, Msg{Role: "user", Text: block, Kind: KindContext, Scope: in.Scope})
			l.noteRefBlock(cfg, in, block, got, hasNotes, 0)
		}
		l.turnFrom = len(l.msgs) // 这一批那条的位置：渠道回合照库重写从这里起
		if len(batch) > 0 {
			l.msgs = append(l.msgs, batch...)
			return
		}
		m := Msg{Role: "user", Text: history.Said(in.Parts, text), Images: images, Kind: KindSaid, Seq: lastSeq}
		if len(images) > 0 {
			m.imageSeqs = append([]int64(nil), in.Seqs...)
		}
		l.msgs = append(l.msgs, m)
	})
	for _, m := range l.msgs[l.turnFrom:] {
		if err := recordWork(ctx, cfg, m); err != nil {
			return nil, err
		}
	}
	if in.local() {
		if err := st.TurnRan(turnID); err != nil {
			h.plugins.logf("记回合拼过参考信息出错：%v", err)
		}
	}
	out := &Result{Built: b, TurnID: turnID}
	defer func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			snapshot, err := cfg.Budget.Snapshot(context.WithoutCancel(ctx), identity.RootID)
			if err == nil && !time.Now().Before(snapshot.Limits.Deadline) {
				out.BudgetExhausted = true
			}
		}
	}()
	if w := reviewFor(in); w != nil {
		defer func() {
			if w.verdict.Status != "done" {
				out.Said = nil
			}
		}()
		if err := h.observeWork(ctx, cfg, in, turnID, "goal_changed"); err != nil {
			return out, err
		}
	}
	// 最后一步整段清完什么都不剩（全是乱码）：这条回复不留在她的实例里（agent.ts held + forgetHeld）。
	// 和 Bun 不一样（规格第七节 A39）：按最后一步算（后面又说了真话就不算）、只拿掉末尾那一条；Bun 一轮里打上就不撤，
	// 还往回截到最后一条 user，会把前面说过、已经存进库的话和工具结果一起拿掉（agent.ts 853 行自己记的已知问题）
	held := false
	defer func() {
		if held {
			l.forgetHeld()
		}
	}()
	// 插话送到（pi 每一步开头取一次插话，agent.ts message_end）：主窗口的存成主人说的原话，渠道的中间件已经存了、这里只登记；
	// 防卡死从头数，她看到的是带前缀的那句
	// 插话送到时插件再拼一次参考信息（问"上次说的那个"也召回得到，8.1）：只追加，排在这几句插话前面
	deliver := func() (bool, error) {
		var q []steer
		if in.Identity.Kind == kernel.KindChild {
			q = l.steer.drain() // 子运行：上级用 task_message 追加的指示，在内存队列里
		} else {
			var err error
			if q, err = h.takeInputSteers(turnID); err != nil {
				return false, err
			}
		}
		if len(q) == 0 {
			return false, nil
		}
		said := make([]Msg, 0, len(q))
		texts := make([]string, 0, len(q))
		var steerSeqs []int64
		var first int64 // 这几句插话里最早的事件
		markSteer := func(s int64) {
			steerSeqs = append(steerSeqs, s)
			mark(s)
			if first == 0 || s < first {
				first = s
			}
		}
		for i, s := range q {
			prefix, seq := steerPrefixChannel, lastOf(s.seqs)
			if in.Identity.Kind == kernel.KindChild {
				prefix = steerPrefixTask
			} else if in.local() && len(s.seqs) == 0 {
				var err error
				if seq, err = st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conversation, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: s.text, Segments: selftestSegments(conversation, nil), CreatedAt: cfg.Now().UnixMilli()}, []string{characterID}); err != nil {
					// 存不进库：前面已经送到（存进库、记成见过）的几句照样进她的历史，这一句和后面的退回网页当普通的一句再发
					l.msgs = append(l.msgs, said...)
					l.steer.emitDropped(q[i:], true)
					return false, err
				}
				if cfg.OnLocalStored != nil {
					cfg.OnLocalStored(conversation, seq, s.text)
				}
				h.plugins.notifySeen(Seen{CharacterID: characterID, Conversation: conversation, Seq: seq})
				prefix = steerPrefix
				markSteer(seq)
			} else {
				if in.local() && in.Identity.Scope.ActorKind == "owner" {
					prefix = steerPrefix
				}
				for _, x := range s.seqs {
					markSteer(x)
				}
			}
			*guard = repeatGuard{}
			l.steer.emit(SteerEvent{Phase: "delivered", ID: s.id, Text: s.text})
			said = append(said, Msg{Role: "user", Text: prefix + history.Said(s.parts, s.text), Kind: KindSaid, Seq: seq})
			texts = append(texts, s.text)
		}
		var pending []contextContribution
		if in.Identity.Kind != kernel.KindChild { // 子运行不拼记忆等参考信息（和 runTurn 开头一致）
			pending = h.plugins.contextContributions(ctx, pluginIn(strings.Join(texts, "\n"), true), l.stateOf)
		}
		if h.testHook != nil {
			h.testHook("contextReady")
		}
		h.plugins.acceptContext(ctx, pending, func(got acceptedContext) {
			h.noteRecall(st, in, turnID, cfg.Now(), recalls, got.Recall)
			if len(got.Lines) > 0 {
				l.turnCtx = append(l.turnCtx, steerCtx{at: len(l.msgs), before: first})
				text := strings.Join(append([]string{ContextHeader}, got.Lines...), "\n")
				l.msgs = append(l.msgs, Msg{Role: "user", Text: text, Kind: KindContext, Scope: in.Scope})
				l.noteRefBlock(cfg, in, text, got, false, first)
			}
			l.msgs = append(l.msgs, said...)
			inputSeqs = append(inputSeqs, steerSeqs...)
		})
		// 插话也记进工作记录：不记的话，恢复时这之后的工具步骤会排到插话前面（说的字仍以事件库为准）
		if workHistoryEnabled(cfg, in) {
			for _, m := range said {
				if err := recordWork(context.WithoutCancel(ctx), cfg, m); err != nil {
					return true, err
				}
			}
		}
		return true, nil
	}
	if _, err := deliver(); err != nil {
		return out, err
	}
	steered := false
	overflowTries := 0       // 这一轮恢复过几次溢出；本地私聊有进展就接着恢复，其他场合只一次
	var toolFacts []ToolFact // 这一轮调过的工具，给结束前钩子看
	stopContinues := 0       // 这一轮因为结束前钩子续跑了几次
	for step := 0; ; step++ {
		recalls.step = step
		if ctx.Err() != nil {
			out.Stopped = true
			return out, nil
		}
		stepID := "step:" + storeID(cfg)
		b.Invocation = identity
		b.Invocation.StepID = stepID
		b.Invocation.AttemptID = ""
		if err := h.takeMaterials(ctx, cfg, l, b.Invocation); err != nil {
			return out, err
		}
		// 接着调工具的下一步开头再取一次（上一步做完那次没取到的话；agent-loop 在 prepareNextTurn 之后）
		if step > 0 && !steered {
			if _, err := deliver(); err != nil {
				return out, err
			}
		}
		// 等图超时后才下完的图：这一步请求之前补给她（只追加在末尾）
		for _, batch := range late.take() {
			l.msgs = append(l.msgs, Msg{Role: "user", Text: batch.note, Images: batch.images, Kind: KindContext, Scope: in.Scope})
			if cfg.Debugf != nil {
				cfg.Debugf("补图送到 conversation=%s run=%s images=%d", conversation, in.Identity.RunID, len(batch.images))
			}
		}
		l.mu.Lock()
		l.stepText = ""
		l.mu.Unlock()
		var failover *llm.FailoverPlan
		if chain := cfg.fallbacks(characterID); len(chain) > 0 {
			if cfg.Failover == nil {
				cfg.Failover = h.failoverSelector(cfg)
			}
			failover = cfg.Failover.Plan(c.Model, chain)
			if failover.Model == "" {
				return out, failover.Err(nil)
			}
			if e := checkUnlistedImages(cfg, l, failover.Model); e != nil {
				return out, e
			}
			if failover.Model != b.Model.ID {
				if e := l.useFailoverModel(cfg, failover.Model); e != nil {
					return out, failover.Exhausted(e)
				}
			}
			failoverNotice(in, turnID, step, failover)
		}
		l.clearModelReasoning(b.Model.ID)
		// 这一次发出去的（agent.ts transformContext）：超了软门槛就把本轮之前的长工具结果换成短占位，只改这一次，不改实例里的
		requestSeqs := append([]int64(nil), inputSeqs...)
		endRequest := cfg.timeline.begin("context.request", "")
		sent, slimmed := b.slim(l.msgs)
		req, ctxJSON := b.requestFor(sent), b.contextFor(l.msgs, sent, block, slimmed, l.first)
		endRequest(false)
		if step == 0 && b.Request.Model == "" {
			b.Request, b.ContextJSON, b.Context = req, ctxJSON, block
		}
		var firstText, firstThinking time.Time
		var res llm.Result
		var err error
		// 同模型重试仍遵守“本地已经出字不重来”；故障切换另行撤回当前草稿。
		requestNumber := 0
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				// 重来的这一次从头算：说出口的、首字、这一步的开始时刻（等着重来时被停下的，那一行也从停下时算、没有首字）
				l.mu.Lock()
				l.stepText, l.stepStarted = "", cfg.Now()
				l.mu.Unlock()
				firstText, firstThinking = time.Time{}, time.Time{}
			}
			if ctx.Err() != nil {
				// 工具跑着、等审批、等着重试时被停下：pi 照样去请求，请求发不出去，记成出错（不是 aborted）、网页多一条模型错误（规格第七节 A36）。
				// 这一轮的结局照样算被停下（回合定了是 stopped）；等重试时被停下的，出错那次的半句不要了
				// 这次没有派发，不能沿用上一次 AttemptID 覆盖其真实失败记录。
				b.Invocation.AttemptID = ""
				res, err = llm.Result{}, errAbortedBeforeRequest
				out.Stopped = true
				break
			}
			callStart := cfg.Now()
			thought, thoughtDone := "", false
			thoughtID := thinkID(turnID, step)
			if failover != nil {
				thoughtID += fmt.Sprintf(":%d", requestNumber)
			}
			requestNumber++
			callCtx := ctx
			if failover != nil {
				callCtx = llm.WithFailover(ctx, failover.Info)
			}
			res, err = modelStream(callCtx, cfg, b, req, func(e llm.Event) {
				if e.Text != "" && firstText.IsZero() {
					firstText = cfg.Now() // 首字只算正文（agent.ts：thinking_delta 不算）
				}
				if e.Reasoning != "" && firstThinking.IsZero() {
					firstThinking = cfg.Now()
				}
				if in.onProcess != nil {
					if e.Reasoning != "" {
						thought += e.Reasoning
						in.onProcess(ProcessStep{ID: thoughtID, Step: step, Kind: "think", Thought: thought, State: "running", Started: firstThinking.UnixMilli()})
					}
					if e.Text != "" && thought != "" && !thoughtDone {
						thoughtDone = true
						in.onProcess(ProcessStep{ID: thoughtID, Step: step, Kind: "think", State: "done", Seconds: max(int(cfg.Now().Sub(firstThinking).Seconds()), 1)})
					}
				}
				if e.Text != "" {
					l.mu.Lock()
					l.stepText += e.Text
					text := l.stepText
					l.mu.Unlock()
					if cfg.OnDelta != nil {
						cfg.OnDelta(e.Text)
					}
					if in.onDraft != nil {
						in.onDraft(LocalDraft{TurnID: turnID, Step: step, Text: text})
					}
				}
			})
			cfg.timeline.model(req.Model, callStart, &res, err)
			if in.onProcess != nil && thought != "" && (!thoughtDone || (failover != nil && err != nil)) {
				state := "done"
				if err != nil {
					state = "stopped"
				}
				in.onProcess(ProcessStep{ID: thoughtID, Step: step, Kind: "think", State: state, Seconds: max(int(cfg.Now().Sub(firstThinking).Seconds()), 1)})
			}
			if errors.Is(err, turnbudget.ErrExhausted) {
				out.BudgetExhausted = true
				break
			}
			overflow := llm.IsContextOverflow(err)
			if err == nil && resultOverflow(&res, b.Window) {
				err = fmt.Errorf("上下文超了：结束原因 %s，输入 %d，缓存读 %d，输出 %d，窗口 %d", res.Stop, res.Usage.Input, res.Usage.CacheRead, res.Usage.Output, b.Window)
				overflow = true
			}
			if err == nil || ctx.Err() != nil {
				break
			}
			l.mu.Lock()
			shown, startedAt := l.stepText != "", l.stepStarted
			l.mu.Unlock()
			if shown && in.local() && (failover == nil || overflow) {
				break
			}
			if overflow {
				if overflowTries >= maxOverflowTries(cfg, in) || b.CompactKind != "summary" || !in.local() {
					break
				}
				overflowTries++
				msg := errorText(err) + "（上下文超了，压缩后重来）"
				if e := b.failedAttempt(cfg, st, &res, turnID, startedAt, firstText, ctxJSON, msg, llm.ClassContextOverflow, block); e != nil {
					return out, e
				}
				h.plugins.debug("回合 %s 第 %d 步：%s", turnID, step, msg)
				ok, end, e := h.recoverOverflow(ctx, l, c, in, historyEnd, overflowTries)
				if e != nil {
					return out, e
				}
				historyEnd = end
				if ctx.Err() != nil {
					continue // 照 A7 等待时停止：清掉首字和半截结果，不再发请求
				}
				if !ok {
					break // 没压成照常报原错误，不裁剪、不重发
				}
				b, out.Built = l.b, l.b
				b.Invocation = identity
				b.Invocation.StepID = stepID
				b.Invocation.AttemptID = ""
				if _, e := deliver(); e != nil {
					return out, e
				}
				requestSeqs = append([]int64(nil), inputSeqs...)
				sent, slimmed = b.slim(l.msgs)
				req, ctxJSON = b.requestFor(sent), b.contextFor(l.msgs, sent, block, slimmed, l.first)
				b.Request, b.ContextJSON, b.Context = req, ctxJSON, block
				continue
			}
			wait, ok := cfg.Retry.wait(err, attempt)
			if shown && in.local() {
				ok = false
			}
			if !ok {
				if failover == nil || !llm.FailoverEligible(ctx, err) {
					// 最后一次请求仍失败、不再重试：ERROR（规格-日志分级 5.1）；用同一句模板，打码才认得出它是模型错误
					h.plugins.fail("回合 %s 第 %d 步的模型请求出错：%s；不再重试", turnID, step, errorText(err))
					break
				}
				previous := err
				next := failover.Next(ctx, err)
				if !next {
					err = failover.Exhausted(err)
					h.plugins.fail("回合 %s 第 %d 步的模型请求出错：%s；备用模型也用完了，不再重试", turnID, step, errorText(err))
					discardModelDraft(l, in, turnID, step)
					res.Text, res.Reasoning, res.Replay, res.ToolCalls = "", "", chat.Replay{}, nil
					break
				}
				msg := fmt.Sprintf("%s（%s，换成 %s）", errorText(previous), modelcall.ErrorClass(previous), failover.Model)
				if e := b.failedAttempt(cfg, st, &res, turnID, startedAt, firstText, ctxJSON, msg, modelcall.ErrorClass(previous), block); e != nil {
					return out, e
				}
				discardModelDraft(l, in, turnID, step)
				if e := l.useFailoverModel(cfg, failover.Model); e != nil {
					return out, failover.Err(e)
				}
				failoverNotice(in, turnID, step, failover)
				sent, slimmed = b.slim(l.msgs)
				req, ctxJSON = b.requestFor(sent), b.contextFor(l.msgs, sent, block, slimmed, l.first)
				firstText, firstThinking = time.Time{}, time.Time{}
				l.mu.Lock()
				l.stepStarted = cfg.Now()
				l.mu.Unlock()
				attempt = -1
				continue
			}
			msg := errorText(err)
			if e := b.failedAttempt(cfg, st, &res, turnID, startedAt, firstText, ctxJSON, fmt.Sprintf("%s（第 %d 次请求出错，%.1f 秒以后再请求）", msg, attempt+1, wait.Seconds()), modelcall.ErrorClass(err), block); e != nil {
				return out, e
			}
			h.plugins.warn("回合 %s 第 %d 步的模型请求出错：%s；%.1f 秒以后再请求（第 %d 次重试，最多 %d 次）", turnID, step, msg, wait.Seconds(), attempt+1, cfg.Retry.orDefault().Max)
			endRetry := cfg.timeline.begin("model.retry", req.Model)
			cfg.Retry.pause(ctx, wait)
			endRetry(false)
		}
		// 说到一半被停下（主人按停止、来了新的一句）：pi 把收到的半句放进一条 aborted 的回复，agent.ts 照常存（segments 带 aborted），
		// 这一步记 "Request was aborted"，这一轮到此为止、不算出错
		aborted := errors.Is(err, context.Canceled)
		out.Steps = append(out.Steps, res)
		if len(out.Steps) == 1 {
			out.LLM = res
		}
		// 中途出错（被内容过滤、断了、没给结束原因）：已经收到的思考和半句话照样记下，模型调用那一条带上错误，这一轮到此为止
		// （pi 把它们放进一条出错的回复，agent.ts message_end 照常存；agent-loop 不再往下跑）
		var errMsg *string
		var errClass string
		a := Msg{Role: "assistant", Text: Clean(res.Text), HasText: res.Text != "", Thinking: res.Reasoning, Replay: res.Replay,
			HasThinking: res.Reasoning != "" || res.Replay.Codec != "" || len(res.Replay.Data) > 0, Usage: &res.Usage}
		if err != nil {
			msg := errorText(err)
			a.Stop = "error"
			errClass = modelcall.ErrorClass(err)
			if aborted {
				msg, a.Stop, errClass = "Request was aborted", "aborted", llm.ClassCancelled
			}
			errMsg = &msg
		}
		for _, tc := range res.ToolCalls {
			args, argsErr := parseArgs(tc.Args)
			a.Calls = append(a.Calls, Call{ID: tc.ID, Name: tc.Name, Args: args, ArgsErr: argsErr})
		}
		cfg.invocation = b.Invocation
		a.WorkStepID = b.Invocation.StepID
		for i := range a.Calls {
			a.Calls[i].ToolID = "tool:" + storeID(cfg)
			a.Calls[i].GroupEnd = i == len(a.Calls)-1
		}
		// 带工具调用的那一步不算：工具循环还没完，拿掉会连这一轮的工具结果一起丢（agent.ts 853 行）
		held = jsstr.Trim(res.Text) != "" && jsstr.Trim(a.Text) == "" && len(a.Calls) == 0
		l.mu.Lock()
		stepStarted := l.stepStarted
		l.mu.Unlock()
		endSave := cfg.timeline.begin("turn.save", "")
		replySeq, e := b.endStep(cfg, st, a, &res, turnID, stepStarted, firstText, firstThinking, ctxJSON, errMsg, errClass, aborted, l.first, block, in.ReplyOrigin, out)
		endSave(e != nil)
		if e != nil {
			return out, e
		}
		if in.onDraft != nil {
			in.onDraft(LocalDraft{TurnID: turnID, Step: step})
		}
		a.Kind, a.Seq = KindSaid, replySeq
		if textclean.Trim(a.Text) != "" {
			out.Said = append(out.Said, Said{Step: step, Text: a.Text})
			if !b.Local {
				a.Turn, a.Step = turnID, step
			}
		}
		if replySeq != 0 {
			mark(replySeq)
			// 存进库的才有 seq：只有主窗口的回合由内核写库；渠道回合由中间件存、存了以后它发
			observers := in.Observers
			if len(observers) == 0 {
				observers = []string{characterID}
			}
			for _, id := range observers {
				h.plugins.notifySeen(Seen{CharacterID: id, Conversation: conversation, Seq: replySeq})
			}
		}
		// agent.ts recordRun：重建原因只记一次；下一步的首字从这里重新计时；上一步的真实输入（停下、出错的那步是 0）；
		// 这一步说的话记完就清掉（工具跑着时按停止，提醒里不带这一步的话：那不是被停住时说的）
		l.first = false
		l.mu.Lock()
		l.stepText = ""
		l.mu.Unlock()
		l.lastTotalInput = promptTokens(res.Usage)
		l.mu.Lock()
		l.stepStarted = cfg.Now()
		l.mu.Unlock()
		// 出错、被停下的回复也留在实例里（pi 照样放进历史，发请求时跳过）
		l.msgs = append(l.msgs, a)
		if e := recordWork(context.WithoutCancel(ctx), cfg, a); e != nil {
			return out, e
		}
		if aborted {
			out.Stopped = true
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if len(a.Calls) > 0 {
			if in.onProcess != nil {
				current := step
				cfg.OnProcess = func(ev ProcessStep) {
					ev.Step = current
					in.onProcess(ev)
				}
			}
			results, e := runTools(kernel.WithInvocation(ctx, b.Invocation), cfg, in.Workspace, l.contextID, st, h.plugins, a, res.Stop, characterID, conversation, in.Audience, b.Decls, l.toolRegs, turnID, guard, requestSeqs)
			// 钩子可能跑很久：先跑完，再按这时的状态判是否接纳（停止、来源失效都要算进去）
			var notes [][]string
			if e == nil {
				notes = h.toolResultHooks(ctx, cfg, in, a, results, turnID, step, &toolFacts)
			}
			for i := range results {
				m := &results[i]
				controlled := m.Disposition != nil || m.OnAccepted != nil || m.Acceptable != nil || m.Controlled || m.ControlledText != ""
				reject := controlled && (ctx.Err() != nil || e != nil) // 受控资料是只读的，回合停了就不接纳（规格-停止三段）
				if valid := m.Acceptable; valid != nil && !valid() {
					reject = true
				}
				if reject {
					if m.Disposition != nil {
						m.Disposition.Discard()
					}
					m.Text, m.IsError, m.Ref = "资料未接纳：回合停止、保存失败或来源失效。", true, nil
					m.OnAccepted, m.Disposition, m.Controlled, m.ControlledText = nil, nil, false, ""
				}
				m.Acceptable = nil
				m.Kind, m.Scope = KindTool, in.Scope
				out.Tools = append(out.Tools, ToolSummary{Name: m.ToolName, OK: !m.IsError, Result: clip(m.Text, toolSummaryRunes)})
				if i < len(notes) && !m.IsError && ctx.Err() == nil {
					appendToolNotes(m, notes[i])
				}
			}
			l.msgs = append(l.msgs, results...)
			for i := range results {
				m := &l.msgs[len(l.msgs)-len(results)+i]
				if disposition := m.Disposition; disposition != nil {
					m.Disposition = nil
					disposition.Accept()
				}
				if accept := m.OnAccepted; accept != nil {
					m.OnAccepted = nil
					accept()
				}
			}
			if e != nil {
				out.BudgetExhausted = errors.Is(e, turnbudget.ErrExhausted)
				return out, e
			}
			if ctx.Err() != nil {
				out.Stopped = true
				return out, nil
			}
			if err := h.observeWork(ctx, cfg, in, turnID, "plan_changed"); err != nil {
				return out, err
			}
			if err := h.takeAdvice(ctx, cfg, l, in); err != nil {
				return out, err
			}
		}
		// 这一步做完再取一次插话：有插话、或者还要接着调工具，就再请求一次（agent-loop：while 有工具调用或有插话）
		recalls.step = step + 1 // 这一步做完再取的插话，它召回的记忆排在下一步之前
		if steered, e = deliver(); e != nil {
			return out, e
		}
		if len(a.Calls) == 0 && !steered {
			switch h.stopHooks(ctx, cfg, in, l, turnID, step, a, toolFacts, &stopContinues) {
			case stopContinue:
				continue
			case stopHalted:
				out.Stopped = true
				return out, nil
			}
			if err := h.observeWork(ctx, cfg, in, turnID, "candidate"); err != nil {
				return out, err
			}
			if err := h.completeWork(ctx, cfg, in, turnID, out); err != nil {
				return out, err
			}
			// 这之后到的插话由收尾那一段在锁里取走、退回去（不会丢，A4）
			if h.testHook != nil {
				h.testHook("settling")
			}
			return out, nil
		}
	}
}

// forgetHeld 拿掉末尾那条全是乱码的回复（没字、没工具调用；发请求时本来就跳过它，这里是不让它留在实例里）
func (l *Live) forgetHeld() {
	if n := len(l.msgs); n > 0 {
		if m := l.msgs[n-1]; m.Role == "assistant" && jsstr.Trim(m.Text) == "" && len(m.Calls) == 0 {
			l.msgs = l.msgs[:n-1]
		}
	}
}
