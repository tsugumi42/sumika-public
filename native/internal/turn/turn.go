// Package turn 是本地主窗口私聊的一轮：存下主人这句话 → 拼请求 → 流式回 → 清句尾 → 写库。
// 照 daemon/plugins/console.ts consoleSend、router.ts reply、agent.ts（acquire / runTurn / buildContext / onAgentEvent / recordRun）
// 和 pi 的 openai-completions 拼请求的规则（规格-Go后台 四点六；出处清单 data/dev/process-study/result-step1-inventory.md）。
// 第 1 步只管实例冷启动后的第一轮：不留实例、不压缩；工具（第 2 步）、记忆和学说法往参考信息里加的行（第 3 步）还没有
package turn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/paths"
	"sumika-native/internal/persona"
	"sumika-native/internal/prompts"
	"sumika-native/internal/store"
	"sumika-native/internal/textclean"
	"sumika-native/internal/toolchain"
	"sumika-native/internal/tools"
)

// ContextHeader 是"参考信息"那条消息的开头（agent.ts CONTEXT_HEADER）
const ContextHeader = `（以下是给你的参考信息，不是对方说的话。只在相关时自然地用上，不要逐条复述，也不要提到"记忆""系统"这些词。）`

// 主窗口里对面一定是主人（router.ts reply 的 relation）
const ownerRelation = "对面是%s。这里只有你们两个。"

// 记进 model_runs 的那一段参考信息的来源说明（agent.ts recordRun）
const memorySource = "本轮追加的参考信息（对面是谁、记忆、报时；追加在这句话前面并留在历史里）"

// 思考过程最多存多少（agent.ts THINKING_MAX）
const thinkingMax = 32 * 1024

// 剥乱码 → 清"】【" 最多循环几轮（agent.ts CLEAN_ROUNDS）
const cleanRounds = 16

// Shell 是本机跑命令用的命令行（daemon/tools/shell-info.ts）：挑哪份说明、版本写几
type Shell struct {
	Template, Version string
}

// Config 是一轮要用到的配置
type Config struct {
	timeline *timeline // 这一轮的耗时；回合入口建，别处只追加
	// 内核补强共用端口；执行路径接入前不在这里提供空实现。
	Lifetime      kernel.Lifetime
	Lifecycle     LifecycleConfig
	Budget        kernel.Budget
	ModelCaller   ModelCaller
	Work          WorkPort
	Completion    CompletionPort
	Advisor       AdvisorPort
	Results       kernel.ResultStore
	Resolver      ToolResolver
	BindInput     func(context.Context, kernel.Invocation) (kernel.Invocation, error)
	Materials     func(context.Context, kernel.Invocation) ([]Msg, error)
	PrepareImages func(context.Context, Input, llm.Model) (string, []llm.Image)
	// RestoreImages 给重建后的历史里第 seq 条主人消息取回它当初带的图（没有或不能带就返回空）；宿主按事件从媒体库读，不存图片字节。
	RestoreImages     func(context.Context, Input, int64, llm.Model) []llm.Image
	ForgetContext     func(string)
	InternalAuthorize func(context.Context, kernel.Invocation, string) error
	// Notes 给某个角色在某个会话里的下一轮追加“参考信息”（悄悄话的处理记录，规格-群聊引用和路由 7.3）：
	// 只追加进 Relation 那一条，同一个实例里文字不变就不重给；返回空就什么也不加。只对前台回合问，内部子运行不问
	Notes func(characterID, conversation string) string
	// RefScope 算一批听众的可见档（memory.ScopeKey）：参考信息块按它分档存库、重建时只插回同一档的（refblock.go）。
	// nil = 不存、不插（失败关闭）；返回空串也当不存
	RefScope        func(audience.Audience) string
	internal        *internalLimits
	Root            string      // 项目根：出厂的提示词在 Root/prompts/
	Data            paths.Paths // 数据目录
	Thinking        string      // QQ 群的思考档位
	LocalThinking   string      // 本地私聊和本地群聊；空值沿用 Thinking
	PrivateThinking string      // QQ 私聊；空值沿用 Thinking
	// ModelSnapshot 在回合开头调用一次；返回映射和列表属于不可变的同一版本。
	ModelSnapshot func() (Config, []llm.Model)
	ModelVersion  string // 设置页用它区分已发布版本与仍在运行的旧轮
	// CatalogError 是上次列模型失败的原因（空 = 目录是好的）：模型不在列表时据此报"来源连不上"，不报"模型不存在"
	CatalogError   string
	RecentMessages int // agent.recentMessages
	// NoStartRebuild 只给测试和评测：关掉本地私聊和主人 QQ 私聊从上下文起点重建，照锚定窗口读最近这么多条。
	// 不进配置、不接 sumika.example.yml（评测要精确控制请求里带多少历史）
	NoStartRebuild bool
	// OwnerPrivate 认主人的平台私聊会话（开始新话题只允许本地私聊和这些会话）；由宿主按 qq.owner 设，nil = 不认
	OwnerPrivate func(conversation string) bool
	Workdir      string // 仅作本地会话的缺省 CWD；每轮冻结到 Input.Workspace，执行和审批不读此字段
	// ApprovalRuleOf 按会话给本地私聊里"哪些要先请主人批准"那句（句号结尾）；空 = 不说。
	// 由宿主按这个会话现在生效的审批档生成，每轮参考信息里变了才给；不进固定前缀，切档不断缓存。
	ApprovalRuleOf func(conversation string) string
	// WorkspaceFor 问这个本地私聊会话绑定了哪个工作目录；空串 = 没绑定，用 Workdir。出错 = 这一轮不跑，不回退。
	// 只对主人的本地私聊调；nil（评测、对拍）一律用 Workdir。
	WorkspaceFor          func(ctx context.Context, conversation string) (string, error)
	Models                map[string]string   // 角色 → 说话用的模型（配置 characters.<id>.model）
	FallbackModels        map[string][]string // 角色覆盖；nil 继承全局，显式空链禁用
	DefaultFallbackModels []string
	Failover              *llm.Failover    // 与后台模型调用共用，生命周期属于客户端
	modelCatalog          []llm.Model      // 本轮冻结的能力列表
	Shell                 *Shell           // nil = 没查到命令行，不拼跑命令说明
	Now                   func() time.Time // 时钟（测试里定住）
	Gate                  *approval.Gate   // 工具执行前的那一道关；nil = 工具一个都不跑（报"没有接上审批"）
	Context               ContextLimits    // agent.context（上下文的几道线）；零值用默认
	OnSteer               func(SteerEvent) // 插话走到了哪一步（agent.ts 的 turn/event steer）；可以是 nil
	// OnLocalStored 在主人消息（含已送达插话）入库后、拼上下文前调用；只许排任务，不等网络。
	OnLocalStored func(conversation string, seq int64, text string)
	BeforeTurn    func()                           // 一轮排到了、读卡之前（测试用来让几句话前后脚到）；可以是 nil
	OnDelta       func(text string)                // 每收到一段正文（agent.ts 的 turn/event delta）；可以是 nil
	OnProcess     func(ProcessStep)                // 思考和工具的进度，只有本地桌面的一轮才有
	OnTool        func(id, name string)            // 一个工具开始（tool_execution_start）；可以是 nil
	Hooks         HookTimeouts                     // 插件钩子的时限（零值用默认，测试里调短）
	Logf          func(format string, args ...any) // 插件出错、超时记在这里；nil 用 log.Printf
	Warnf         func(format string, args ...any) // WARN（出错但会自己接上）；nil 退回 Logf
	Errorf        func(format string, args ...any) // ERROR（有数据会丢、或要人处理）；nil 退回 Logf
	Debugf        func(format string, args ...any) // DEBUG（每步细节，默认不写）；nil 退回 Logf
	// ExecTool 真执行一个工具；nil 用 tools.Execute。测试换成会卡住、会记次数的假工具（8.2 A2、A3）
	ExecTool     func(ctx context.Context, workdir, name string, args jsjson.Value) (tools.Result, error)
	Instructions InstructionResolver
	// ToolTimeout 改某个工具的总超时（declared 是它声明的）；nil 照声明。测试里调短（规格第七节 T14）
	ToolTimeout func(name string, declared time.Duration) time.Duration
	Retry       RetryPolicy // 模型请求出错以后怎么重试（规格 8.2 A7）；零值用默认，测试里把等的时间调短
	// CompactTimeout 是一次压缩的摘要请求最多多久（agent.ts COMPACT_TIMEOUT_MS）；0 = 180 秒
	CompactTimeout time.Duration
	// OnCompacting 通知界面正在整理；开始 true、结束 false（失败也结束）。可能从后台调用；panic 只记日志
	OnCompacting func(characterID, conversation string, starting bool)
	// MaxLive、IdleAfter 是实例回收（agent.maxLive、agent.idleMinutes，规格第七节 T20）：最多留几个实例、闲多久放掉；0 = 8 个、45 分钟
	MaxLive   int
	IdleAfter time.Duration
	// IdleCompact 是本地私聊的空闲压缩（agent.idleCompact，ADR-388）：闲满 IdleCompactAfter、实例至少 IdleCompactMinTokens 才压；
	// IdleCompact 为假就关，After 0 = 50 分钟，MinTokens 0 = 200000（配置总给正数；零值是给不碰这项的调用方一个关着的缺省）
	IdleCompact          bool
	IdleCompactAfter     time.Duration
	IdleCompactMinTokens int
	// AfterFunc 是空闲压缩的计时器（测试用假时钟手动触发）；nil 用 time.AfterFunc，返回停止它的函数
	AfterFunc    func(time.Duration, func()) (stop func())
	WorkSettings config.Work
	invocation   kernel.Invocation
	Scheduler    *ToolScheduler
	BudgetConfig config.Budget
	// OnToolFinalized 在事实持久化后通知，不取代唯一结果入口。
	OnToolFinalized func(context.Context, kernel.Invocation, tools.Result) error
}

func (c Config) maxLive() int { return orInt(c.MaxLive, 8) }

func (c Config) idleAfter() time.Duration {
	if c.IdleAfter > 0 {
		return c.IdleAfter
	}
	return 45 * time.Minute
}

// idleCompactAfter 是空闲压缩的起步时长（agent.idleCompact.after，默认 50 分钟；Claude Code 约 54 分钟，赶在一小时缓存过期前）
func (c Config) idleCompactAfter() time.Duration {
	if c.IdleCompactAfter > 0 {
		return c.IdleCompactAfter
	}
	return 50 * time.Minute
}

// idleCompactMinTokens 是空闲压缩的最小实例（agent.idleCompact.minTokens，默认 200000，同 Claude Code 的起步值）
func (c Config) idleCompactMinTokens() int {
	if c.IdleCompactMinTokens > 0 {
		return c.IdleCompactMinTokens
	}
	return 200_000
}

// Built 是拼好的这一轮：固定前缀、实例里一开始的那几条消息，和每一步拼请求要用的东西（requestFor、contextFor）
type Built struct {
	CharacterID string
	// OwnerCall 是这个角色怎么称呼所有者（卡上的 ownerName，没写是"用户"）：压缩提示词里的 {{owner_name}} 用它。
	// 冻结在实例里，和固定前缀同源，卡改了重建后一起换
	OwnerCall    string
	Conversation string
	Local        bool              // 本地主窗口的：她说的由内核存进库；渠道的由中间件存（ADR-184 第 3 条）
	Observers    []string          // 本地群回复的观察者；空值只记说话的角色
	Sections     []prompts.Section // 固定前缀的各段（含空段：记 model_runs 时也记）
	System       string            // 拼起来的系统提示
	SystemRole   chat.SystemRole   // developer 或 system
	Context      string            // 这一轮的参考信息
	// ClockAt、Relation 是重建时从插回的参考块里恢复的「这个实例里已经给过什么」：上次报时的时刻、给过的对面是谁。
	// 不恢复的话重建出来的实例会当自己什么都没给过，下一轮又报一次时、又说一次对面是谁，和接着用的实例不一样（refblock.go）
	ClockAt  time.Time
	Relation string
	// Restored 是插回去的每个块引用的记忆（按块的先后），重建后交给插件恢复它们在新实例里的存储（RestoreIn）
	Restored    [][]MemoryRef
	Msgs        []Msg // 实例里一开始的消息：系统提示、（快照）、历史、参考信息、这句话
	Model       llm.Model
	Decls       []tools.Declaration
	CacheKey    string
	Effort      string // 发出去的 reasoning_effort（空不发）
	Window      int    // 上下文长度
	MaxOut      int    // 模型报的最长输出
	Hard        float64
	Soft        float64 // 软门槛：发出去的估算超了它，本轮之前的长工具结果换成短占位（只改这一次请求）
	Reserve     int     // 留给回复和工具往返的（记进 context_json）
	CompactKind string  // 满了怎么办：summary（压缩）或 trim（只带最近的话）
	Fixed       int     // 固定前缀的 Sumika 估算（系统提示 + 工具定义）
	EpochID     string  // 用了哪份压缩快照（没有是空）
	// EpochSeen 是建的时候库里最新的那份成功快照（没用上的也算：后面有"新话题"分隔时快照不用）；下一轮库里最新的不是它了才重建
	// （有了新快照），不拿 EpochID 比——不然"快照之后有分隔"的会话每轮都重建
	EpochSeen string
	Compacted bool // 因为有了新快照才重建的（model_runs.rebuild 记 compact）
	Reset     bool // 历史超了硬门槛，只带了最近的（model_runs.rebuild 记 reset）
	Kept      int  // 裁剪以后留了几条
	Dropped   int  // 超硬门槛裁掉了几条（只是裁剪那一步，不含窗口外的；窗口外的在 Trace）
	// Trace 是这次重建的留痕（rebuildtrace.go）：条数在 buildWith 里数，原因由调用的一方记；重建后第一步写进 context_json
	Trace RebuildTrace
	// 第一轮第一步的请求、context_json、参考信息（turn-check 拿第一步的请求原样再发一次看缓存）
	Request     chat.Request
	ContextJSON string
	Invocation  kernel.Invocation
}

// ownerLabel 是压缩提示词、关系行里给模型看的称呼：Built 里没填（内部子运行重建出来的）就用"所有者"
func (b *Built) ownerLabel() string {
	if b.OwnerCall != "" {
		return b.OwnerCall
	}
	return "所有者"
}

// Effort 是 pi 发请求前把思考档位换成模型能开的那档（models.js clampThinkingLevel）：先往上找、上面没有才往下；
// 模型不支持思考就不发（空串）；换完是 off 就返回 "off"，llm 发 none 真关（ADR 待办 2026-11-14）
func Effort(m llm.Model, level string) string {
	order := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	reasoning := m.Reasoning != nil && *m.Reasoning
	if m.Reasoning == nil && len(m.ReasoningLevels) > 0 {
		reasoning = true
	}
	listed := map[string]bool{}
	for _, l := range m.SupportedReasoningLevels {
		listed[l.Effort] = true
	}
	for _, level := range m.ReasoningLevels {
		listed[level] = true
	}
	hasMap := reasoning && len(listed) > 0 // 报过档位才映射；平面数组和旧对象数组共用同一能力表
	supported := func(l string) bool {
		if !reasoning {
			return l == "off"
		}
		mapped, explicit := false, false // explicit：映射里写了这一档（值是它自己或 null）
		if hasMap && l != "off" {
			explicit, mapped = true, listed[l]
		}
		if explicit && !mapped {
			return false
		}
		if l == "xhigh" || l == "max" {
			return explicit && mapped
		}
		return true
	}
	var avail []string
	for _, l := range order {
		if supported(l) {
			avail = append(avail, l)
		}
	}
	first := "off"
	if len(avail) > 0 {
		first = avail[0]
	}
	pick := first
	idx := -1
	for i, l := range order {
		if l == level {
			idx = i
		}
	}
	switch {
	case idx >= 0 && supported(level):
		pick = level
	case idx >= 0:
		pick = ""
		for i := idx; i < len(order) && pick == ""; i++ {
			if supported(order[i]) {
				pick = order[i]
			}
		}
		for i := idx - 1; i >= 0 && pick == ""; i-- {
			if supported(order[i]) {
				pick = order[i]
			}
		}
		if pick == "" {
			pick = first
		}
	}
	if !reasoning {
		return "" // 不支持思考的模型没有可关的，不发
	}
	return pick // off 原样交给 llm（发 none，真关）
}

// thinkingFor 在入口按场合选好，后续请求、恢复和压缩只读本轮配置。
func (c Config) thinkingFor(in Input) Config {
	if in.local() {
		if c.LocalThinking != "" {
			c.Thinking = c.LocalThinking
		}
	} else if in.Audience.Channel == "qq" && !in.Audience.Outsiders() && c.PrivateThinking != "" {
		c.Thinking = c.PrivateThinking
	}
	return c
}

// NowText 是给模型看的当前时间，和 Bun 版 nowText（toLocaleString('zh-CN')）一样："2026年10月25日星期日 00:05"（2026-10-25 实测，没有空格）
func NowText(t time.Time) string {
	week := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return fmt.Sprintf("%d年%d月%d日%s %02d:%02d", t.Year(), int(t.Month()), t.Day(), week[t.Weekday()], t.Hour(), t.Minute())
}

// CacheKey 是发给服务商的 prompt_cache_key（llm.ts cacheKeyOf）：哈希的是实例的键"角色:会话"，不往上游带号码
func CacheKey(characterID, conversation string) string {
	h := sha256.Sum256([]byte(characterID + ":" + conversation))
	return "sumika-" + hex.EncodeToString(h[:])[:24]
}

// Sumika 自己的 token 估算（util/context.ts estimateTokens）：CJK 这类宽字（码位 ≥ U+2E80）一个算一个，其余 3.5 个字一个。
// 和 pi 那边的估算不是一回事：这个管上下文的几道线，pi 那个只管 max_completion_tokens
func sumikaTokens(s string) int {
	wide := 0
	for _, r := range s {
		if r >= 0x2e80 {
			wide++
		}
	}
	return wide + int(math.Ceil(float64(store.JSLen(s)-wide)/3.5))
}

// 一组消息（estimateMessages）：每条加 4 的格式开销
func sumikaMessages(texts ...string) int {
	n := 0
	for _, t := range texts {
		n += 4 + sumikaTokens(t)
	}
	return n
}

// ContextLimits 是上下文的几道线（配置 agent.context，规格第七节 T21；agent.ts DEFAULT_CONTEXT、limits）：窗口可以封顶；
// 硬门槛是窗口的 hardRatio 倍和窗口减 reserve 里小的，超了本地闲聊压缩、别的只带最近 keepTokens 的原文；软门槛是窗口的
// softRatio 倍（不超过硬门槛）。window 写 0 跟模型，reserve 写 0 仍按默认 16384，设置页要分开显示配置值与实际值。
type ContextLimits struct {
	Window        int     // 封顶：0 = 跟着模型自己报的长度
	Reserve       int     // 留给回复和工具往返；默认 16384
	SoftRatio     float64 // 默认 0.7
	HardRatio     float64 // 默认 0.85
	KeepTokens    int     // 压完、只带最近的话时保留最近多少原文；默认 20000
	NoAutoCompact bool    // autoCompactChat 关掉：本地闲聊满了也不压缩、只带最近的话
}

func (c ContextLimits) reserve() int         { return orInt(c.Reserve, 16_384) }
func (c ContextLimits) softRatio() float64   { return orFloat(c.SoftRatio, 0.7) }
func (c ContextLimits) hardRatio() float64   { return orFloat(c.HardRatio, 0.85) }
func (c ContextLimits) keepTokens() int      { return orInt(c.KeepTokens, 20_000) }
func (c ContextLimits) window(model int) int { return min(model, orInt(c.Window, model)) }
func (c ContextLimits) hard(window int) float64 {
	return c.hardFor(window, false)
}

// hardFor 同 hard；窗口来源不可靠（保守默认、models.dev 各家说法不一致）时硬线比例最多取 0.75：
// 窗口信大了的代价是请求被拒，比早一点压缩坏得多（ADR-334）
func (c ContextLimits) hardFor(window int, uncertain bool) float64 {
	ratio := c.hardRatio()
	if uncertain {
		ratio = math.Min(ratio, 0.75)
	}
	return math.Min(float64(window)*ratio, float64(window-c.reserve()))
}

func orInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func orFloat(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

func textsOf(ms []history.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// 模型的上下文长度和最长输出：报了正数才算（用户覆盖、服务商、models.dev 补的都已经在 Model 里），
// 没有的窗口按 128K、单次输出按 32000（llm.FallbackMaxOutput，ADR-334；旧值是 8000）
func limits(m llm.Model) (window, maxOut int) {
	return m.Window(), m.OutputLimit()
}

// DefaultApprovalRule 是审批每次都问时的说法，也是没配时的出厂说法。
// 它不在固定前缀里：每轮参考信息里按当前审批档给（Live.buildContext），切档不会断缓存。
const DefaultApprovalRule = "写文件、改文件、运行命令，每次都会先请所有者在界面上批准。"

// prefixOf 拼固定前缀（规格-语言 3.3）：通用规则 → 身份 → 行为 → 表达 → 私下 → 场合 → 跑命令的说明。
// 每一轮都拼一遍，和实例里的比（agent.ts acquire），不一样才重建、读历史。
// 场合那一块：Input.Scenes 有就按它逐段拼（找不到那一段就报错，这一轮不说）；空的拼本地私聊的场合和风格预设。
// Scenes 里要不要放风格预设那段，等渠道中间件第 5 步定内容时一起定。"私下"那段有外人（平台群）时不拼，按听众算
func prefixOf(cfg Config, c persona.Character, in Input) ([]prompts.Section, string, error) {
	conversation := in.Conversation
	ps := prompts.Store{Root: cfg.Root, UserDir: cfg.Data.PromptsDir()}
	core, err := ps.Render("core", map[string]string{"name": c.Name, "owner_name": c.OwnerCall()})
	if err != nil {
		return nil, "", err
	}
	sections := append([]prompts.Section{core}, persona.Sections(c, in.Audience.Outsiders())...) // 平台群有外人，不拼"私下"
	vars := map[string]string{"workdir": in.Workspace.CWD, "owner_name": c.OwnerCall()}
	if len(in.Scenes) > 0 {
		for _, name := range in.Scenes {
			sec, err := ps.Render(name, vars)
			if err != nil {
				return nil, "", fmt.Errorf("场合段「%s」读不到：%w", name, err)
			}
			sections = append(sections, sec)
		}
	} else {
		scene, err := ps.ScenePrompts("local", "private", conversation, "scene-home", vars)
		if err != nil {
			return nil, "", err
		}
		sections = append(sections, scene...)
	}
	if cfg.Shell != nil {
		sh, err := ps.Render(cfg.Shell.Template, map[string]string{"version": cfg.Shell.Version})
		if err != nil {
			return nil, "", err
		}
		sections = append(sections, sh)
	}
	return sections, prompts.Join(sections), nil
}

// buildWith 建一个实例（agent.ts acquire）：拼好固定前缀以后，带上这个听众的工具（decls，Hub 的插件表给），从库里重建历史
// （快照 + 之后的原文，或者最近几条；超了硬门槛只留最近的）。这一批（in.Seqs）不进历史，这一轮单独追加在最后。
// done 是这个会话跑完的回合用过、说过的事件（Hub.done）：比这一批更晚、又不在 done 里的，是还排着队、没轮到的那几批，
// 不读进历史——轮到它们那一轮才进（不然会先出现在这一批前面、轮到时又出现一次；Bun 的 live-together 就是这样，ADR-183 第 6 条登记了）。
// trim：上下文快满了、压不了才重建的，重建出来的历史没超硬门槛也只留最近的（agent.ts acquire 的 trim）
func buildWith(plug *plugins, cfg Config, st *store.Store, c persona.Character, m llm.Model, in Input, sections []prompts.Section, system string, decls []tools.Declaration, done map[int64]bool, trim bool) (*Built, error) {
	conversation := in.Conversation
	// 先看库里最新的快照、再读历史：读的中间写进来的新快照，下一轮一定看得到（比这里记下的新）
	seen, err := history.LatestEpoch(st.Events, c.ID, conversation)
	if err != nil {
		return nil, err
	}
	fromStart := rebuildFromStart(cfg, in)
	var snapshot *history.Message
	var hist []history.Message
	var epochID string
	if fromStart {
		snapshot, hist, epochID, err = history.ForFromStart(st.Events, c.ID, conversation, in.Seqs)
	} else {
		snapshot, hist, epochID, err = history.For(st.Events, c.ID, conversation, cfg.RecentMessages, in.Seqs)
	}
	if err != nil {
		return nil, err
	}
	var epochSeen string
	if seen != nil {
		epochSeen = seen.ID
	}
	// 读出来以后再筛（不先查一遍"更晚的有哪些"再读：两次查询之间存进来的会漏过去，Linux 上 -race 跑出来过）
	if last := lastOf(in.Seqs); last > 0 {
		kept := hist[:0]
		for _, x := range hist {
			if x.Seq <= last || done[x.Seq] {
				kept = append(kept, x)
			}
		}
		hist = kept
	}
	model, _ := limits(m)
	window := cfg.Context.window(model)
	// 重建出来的历史超了硬门槛：从最新往前留，留到 keepTokens 为止，快照照样放在最前面（agent.ts acquire）。
	// 工具定义也在固定前缀里（agent.ts acquire 的 fixedTokens）
	fixed := sumikaTokens(system) + sumikaTokens(tools.FixedJSON(decls))
	snap := 0
	if snapshot != nil {
		snap = sumikaMessages(snapshot.Text)
	}
	hard := cfg.Context.hardFor(window, m.WindowUncertain())
	reset := false
	kept0 := len(hist) // 裁剪前有几条（窗口已经裁过，窗口外的在 Trace 里数）
	// 从起点重建的两种场合不在这里裁：超过硬门槛交给 fitHard 先用活实例压缩，压不了才带 trim 重建回来裁（规格 第三节）
	if trim || (!fromStart && float64(fixed+snap+sumikaMessages(textsOf(hist)...)) > hard) {
		kept, used := 0, 0
		for i := len(hist) - 1; i >= 0; i-- {
			n := sumikaMessages(hist[i].Text)
			if kept > 0 && used+n > cfg.Context.keepTokens() {
				break
			}
			used += n
			kept++
		}
		hist, reset = hist[len(hist)-kept:], true
	}
	trace := countRebuild(st, c.ID, conversation, in.Seqs, snapshot != nil, epochID, hist, fromStart)
	all := hist
	if snapshot != nil {
		all = append([]history.Message{*snapshot}, hist...)
	}
	// 渠道回合存过的参考信息块，按原位插回（refblock.go）：起点是重建后留下的第一条消息，更早的块不要
	var blocks []gatedBlock
	if len(hist) > 0 {
		var err error
		if blocks, err = refBlocksFor(plug, cfg, st, c, in, system, hist[0].Seq); err != nil {
			return nil, err
		}
	}
	msgs := []Msg{{Role: "system", Text: system}}
	bi := 0
	var inserted []gatedBlock
	for _, h := range all {
		// 排在第一条 seq 不小于它的消息前面（和插话那块参考信息照库重写时一样的规则）；一条消息都对不上的块不插
		for h.Seq > 0 && bi < len(blocks) && blocks[bi].BeforeSeq <= h.Seq {
			msgs = append(msgs, Msg{Role: "user", Text: blocks[bi].Text, Kind: KindContext, Scope: in.Scope})
			inserted = append(inserted, blocks[bi])
			bi++
		}
		kind := KindSaid
		if h.Seq == 0 {
			kind = KindSummary
		}
		msgs = append(msgs, Msg{Role: h.Role, Text: h.Text, HasText: h.Role == "assistant", Kind: kind, Seq: h.Seq})
	}
	b := &Built{
		CharacterID: c.ID, OwnerCall: c.OwnerCall(), Conversation: conversation, Local: in.local(), Sections: sections, System: system, Msgs: msgs, Decls: decls, CacheKey: CacheKey(c.ID, conversation),
		Observers: append([]string(nil), in.Observers...),
		Fixed:     fixed, EpochID: epochID, EpochSeen: epochSeen, Reset: reset, Kept: len(hist), Dropped: kept0 - len(hist), Trace: trace,
	}
	b.ClockAt, b.Relation = restoredState(inserted, in.Relation)
	for _, g := range inserted {
		b.Restored = append(b.Restored, g.Refs)
	}
	b.refresh(cfg, m)
	return b, nil
}

// lastOf 是这一批里最后一条（没有是 0）
func lastOf(seqs []int64) int64 {
	var m int64
	for _, s := range seqs {
		m = max(m, s)
	}
	return m
}

// refresh 是每一轮都按现在的模型、配置更新的（agent.ts runTurn 每轮设 state.model、thinkingLevel，limits 按当前模型的窗口算）：
// 复用实例时历史、固定前缀不动，这几样照样换成新的
func (b *Built) refresh(cfg Config, m llm.Model) {
	if b.Local && cfg.LocalThinking != "" {
		cfg.Thinking = cfg.LocalThinking
	}
	model, maxOut := limits(m)
	window := cfg.Context.window(model)
	// 系统提示的角色：模型支持思考时用 developer（pi 对这个地址算出来的 compat 支持 developer；和这一轮开不开思考无关）
	role := chat.SystemRoleSystem
	if m.Reasoning != nil && *m.Reasoning {
		role = chat.SystemRoleDeveloper
	}
	hard := cfg.Context.hardFor(window, m.WindowUncertain())
	b.Model, b.SystemRole, b.Effort, b.Window, b.MaxOut, b.Hard = m, role, Effort(m, cfg.Thinking), window, maxOut, hard
	b.Soft = math.Min(float64(window)*cfg.Context.softRatio(), hard)
	b.Reserve = cfg.Context.reserve()
	// 满了怎么办（agent.ts limits 的 compactKind）：本地闲聊压缩（summary），关掉自动压缩的、渠道的只带最近的话（trim）
	b.CompactKind = "trim"
	if b.Local && !cfg.Context.NoAutoCompact {
		b.CompactKind = "summary"
	}
}

// Clean 是回复的后处理（agent.ts message_end）：剥句尾乱码 → 清"】【"（清掉了东西再剥一次），循环到不再变，最多 16 轮。
// 主窗口不删外文碎片（只在 QQ 群删），也没有平台的 asSent
func Clean(raw string) string {
	text := raw
	for round := 1; round <= cleanRounds; round++ {
		before := text
		text = textclean.DropTailJunk(text, textclean.Options{}).Text
		if b := textclean.DropStrayBrackets(text); b.Dropped > 0 {
			text = textclean.DropTailJunk(b.Text, textclean.Options{}).Text
		} else {
			text = b.Text
		}
		if text == before {
			break
		}
	}
	return text
}

func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// clip 照 daemon/util/clip.ts（实现在 store.Clip，写库的几处共用）
func clip(text string, maxBytes int) string { return store.Clip(text, maxBytes) }

// Workdir 是 tools.workdir 解析后的路径（tools.ts：写了就 resolve，没写是用户目录）
func Workdir(configured string) (string, error) {
	if configured == "" {
		return os.UserHomeDir()
	}
	return filepath.Abs(configured)
}

var shellVersionRE = regexp.MustCompile(`^(\d+)\.(\d+)`)

// ShellOf 从 $PSVersionTable.PSVersion 的输出挑说明模板（shell-info.ts shellInfoOf）：6 及以上是 pwsh，5 及以下是 Windows PowerShell
func ShellOf(versionText string) *Shell {
	m := shellVersionRE.FindStringSubmatch(textclean.Trim(versionText))
	if m == nil {
		return nil
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	t := "shell-powershell5"
	if major >= 6 {
		t = "shell-pwsh"
	}
	return &Shell{Template: t, Version: fmt.Sprintf("%d.%d", major, minor)}
}

// DetectShell 在本机查一次（shell-info.ts detectShell）：PATH 上先找 pwsh.exe 再找 powershell.exe；没装、超时、读不出版本都是 nil
// 探到一次就记住：起一次 PowerShell 要一秒左右，这台机器的命令行在进程活着的时候不会变。探失败不记，下次再试。
func DetectShell(ctx context.Context) *Shell {
	detectedShell.Lock()
	defer detectedShell.Unlock()
	if detectedShell.shell != nil {
		return detectedShell.shell
	}
	detectedShell.shell = detectShell(ctx)
	return detectedShell.shell
}

var detectedShell struct {
	sync.Mutex
	shell *Shell
}

func detectShell(ctx context.Context) *Shell {
	r, ok := toolchain.Lookup("powershell")
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, r.Path, "-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()").Output()
	if err != nil {
		return nil
	}
	return ShellOf(string(out))
}
