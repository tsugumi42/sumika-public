// Package approval 是工具调用执行前那一道关（daemon/plugins/tools.ts 的 gate、describe）：
// 命令先拆开看，按声明和设置判定（internal/policy），放行、拒绝、或者组一张审批卡片等主人点按钮。
// 判定本身是纯函数；这里管它要的那些"先查好的东西"（联接的真实路径、项目里声明过的工具、主人给过的授权）和卡片的内容
package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/edittext"
	"sumika-native/internal/egress"
	"sumika-native/internal/jsdiff"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/kernel"
	"sumika-native/internal/lineend"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// Preview 是写文件、改文件时的改动预览（统一 diff）或内容预览
type Preview struct {
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// Request 是发给主窗口的审批卡片（tools.ts ApprovalRequest）
type Request struct {
	Identity     kernel.Invocation `json:"identity"`
	ID           string            `json:"id"`
	Version      uint64            `json:"version,omitempty"` // 同一张卡片的状态版本，接收方只接受更大的版本
	CharacterID  string            `json:"characterId"`
	Conversation string            `json:"conversation,omitempty"`
	Workdir      string            `json:"workdir,omitempty"` // 这张卡是在哪个工作目录里弹的：按会话选目录后，不同会话的目录不一样，卡上要看得出
	ToolCallID   string            `json:"toolCallId"`
	Tool         string            `json:"tool"`
	Title        string            `json:"title"`
	Subject      string            `json:"subject"`
	Preview      *Preview          `json:"preview,omitempty"`
	Why          string            `json:"why,omitempty"`
	Purpose      string            `json:"purpose,omitempty"`
	Notes        []string          `json:"notes,omitempty"`
	Options      []string          `json:"options,omitempty"`
	KindLabel    string            `json:"kindLabel,omitempty"`
	Retry        int               `json:"retry,omitempty"` // 批准之后目标变了又问的第几次（0 是第一次问）
	CreatedAt    int64             `json:"createdAt"`
	ExpiresAt    int64             `json:"expiresAt"`

	// SubjectTruncated：Subject 被截短了（没声明的工具参数太长）。不在本机的入口不能只凭这样的 Subject 批。
	SubjectTruncated bool `json:"subjectTruncated,omitempty"`

	// Egress：这次调用参数里会把数据带出去的地址和危险写法（只有 Gate.Egress 接上、会话是悄悄话会话时才有）。
	// 只给主人看，不改放行；编号同时写进步骤记录，对话日志里同一个号（规格-群聊引用和路由 8.2）。
	Egress *egress.Report `json:"egress,omitempty"`
}

// Pending 是等着主人回答的这一类（主人点"这个对话""始终"时按它记授权）
type Pending struct {
	Key, Label, Tool, Conversation, Workdir string
	Guard                                   string   // 这条规则认的目标版本（Rule.Guard）
	Dirs                                    []string // 这条命令碰到的外部目录（"这个对话"记着）
}

// Decision 是主人的回答：approved / denied / expired / cancelled；Reason 是拒绝时写的原因
type Decision struct {
	Kind   string
	Reason string
	// Feedback 是自动审批方给模型的完整拒绝说明；只在 denied 时使用，主人的拒绝仍用 Reason。
	Feedback string
	// Via 是这个回答从哪来：空是本机主窗口（或到期、取消这类没人回答的），"qq" 是主人 QQ 私聊（DecideVia）。
	// 它只给订阅方和步骤记录看，不影响放行判断。
	Via string
	// By、Rule、Label：批准时按哪一档（policy.By*）、记成哪一条规则，写进步骤记录。别的审批方不填就当允许一次。
	By, Rule, Label string
}

// Clearance 是一次工具调用过门的结局。Blocked 时 Reason 是给模型看的原文；放行时 By 说是哪一档放的。
type Clearance struct {
	Blocked bool
	Reason  string
	By      string // policy.By*；只读、只碰记忆这种不留痕的也会有值，看 Traced
	Rule    string // 放行靠的规则（同一类的 key）
	Label   string // 规则给人看的名字
	Traced  bool   // 这一步值得写进步骤记录：会写、会跑命令、会联网，或者主人回答过
	// Recheck 在真正执行前再查一遍：批准之后目标变了、规则被撤销了就返回给模型看的原因，空是还作数。没有要查的时候为 nil。
	Recheck func() string
}

// Note 是放行之后追加给模型的一句状态；默认放行、模式放行不说。
func (c Clearance) Note() string {
	switch c.By {
	case policy.ByOnce:
		return "（所有者批准了这一步。）"
	case policy.ByConversation:
		return "（这个对话里所有者允许过这一类，这次没有再问。）"
	case policy.ByAlways:
		return "（所有者记住了放行这一类，这次没有再问。）"
	case policy.ByRule:
		return "（所有者写的放行规则「" + c.Label + "」放行了这一步，这次没有再问。）"
	}
	return ""
}

// RuleText 是步骤记录上给主人看的那一句：这次是哪条规则放行的。
func RuleText(by, label, reason string) string {
	switch by {
	case policy.ByOnce:
		return "你批准了这一次"
	case policy.ByConversation:
		return "这个对话里允许过" + label
	case policy.ByAlways:
		return "记住的规则：" + label
	case policy.ByRule:
		return "你写的放行规则：" + label
	case policy.ByDefault:
		return "默认放行，不用问"
	case policy.ByMode:
		return reason
	}
	return ""
}

// 模型看到的固定说法（规格-审批分级第七节的状态表）。
const (
	whyStale       = "批准之后目标变了，这次没有执行。先重新看一眼现在的样子再决定；重新发起会再问所有者。"
	whyStaleGaveUp = "目标一直在变，问了几次都对不上，这次没有执行。先重新看一眼现在的样子，等它稳定了再做。"
	whyRevoked     = "放行这一类的规则刚被所有者撤销了，这次没有执行；重新发起会再问所有者。"
	whyMCPStale    = "MCP 目标已经变了（服务重连了或参数定义变了），这次没有执行。重新 mcp_search 拿到新的 schemaDigest 再调用，会重新问所有者。"
	whyDenied      = "所有者拒绝了这个操作%s。不要换个方式重试同一件事，先问所有者想怎么做。"
	whyExpired     = "所有者一直没有处理审批，审批过期了，这个操作没有执行。要做的话先跟所有者说一声。"
	whyCancelled   = "操作被取消了（所有者发了新消息或对话被中止），没有执行。"
)

// staleAskLimit：批准之后目标又变了最多再问几轮。一直在变（日志文件之类）就不问了，告诉她等稳定了再做。
const staleAskLimit = 3

// Gate 是 tools.ts 的 Tools 里 gate 用到的那一套
type Gate struct {
	AutoApproveDirs []string
	ProtectedDirs   []string
	Mode            string // 默认审批档；运行中改用 SetMode，不直接写这个字段
	ApprovalMinutes float64
	SelfPorts       []int
	Analyzer        *policy.Analyzer
	AnalyzeBudget   time.Duration // 拆一条命令最多等多久；0 用 defaultAnalyzeBudget。评测、测试放得更长，不把机器忙算成看不清
	Grants          *Grants       // "记住"（data/config/tool-grants.yml）
	Conv            *ConvGrants   // "这个对话里都允许"
	Now             func() time.Time
	// OnAuto：没问主人就放行的（会写、会跑命令、会联网）留痕，写进这一步的 approval_json；c 说是哪一档放的
	OnAuto func(req Request, c Clearance)
	// Egress：给这次调用找外发地址（只在宿主认为需要的会话里返回非 nil，比如悄悄话那一轮）；nil 或返回 nil 就不标。
	// 找到的写进 Request.Egress：问主人的卡上和“没问就放行”的留痕里都有。只是信息，不影响放行。
	Egress func(conversation, toolCallID, tool string, args jsjson.Value) *egress.Report
	// Ask：等主人回答（NewApprovals 接上）；没接上时为空，当成取消
	Ask func(ctx context.Context, req Request, kind *Pending) Decision
	// ParentOf：子任务的内部会话找派它的会话（没有就空）。子任务沿用派它那个会话的审批档。
	ParentOf func(conversation string) string

	// SessionSaveErr：会话审批档、“这个对话”的放行写盘失败时调（放行照旧，只是重启后会丢）；没接就不报
	SessionSaveErr func(error)

	modeMu       sync.RWMutex
	sessionModes map[string]string // 会话 → 这个会话自己的审批档；接了 UseSessionFile 就同时写进文件，重启后保留
	sessions     *sessionState
}

// ValidMode：审批档的三个取值
func ValidMode(m string) bool { return m == "manual" || m == "auto" || m == "yolo" }

// SetMode 改运行中的默认审批档，下一次判定就用新的；不动已经被会话覆盖的。
func (g *Gate) SetMode(mode string) error {
	if !ValidMode(mode) {
		return fmt.Errorf("不认识的审批方式：%q", mode)
	}
	g.modeMu.Lock()
	g.Mode = mode
	g.modeMu.Unlock()
	return nil
}

// DefaultMode 是没有会话覆盖时用的审批档
func (g *Gate) DefaultMode() string {
	g.modeMu.RLock()
	defer g.modeMu.RUnlock()
	return g.Mode
}

// SetSessionMode 只改这个会话的审批档，当场生效；mode 为空是去掉覆盖、回到默认。本地私聊的会写进文件，重启后保留。
func (g *Gate) SetSessionMode(conversation, mode string) error {
	if conversation == "" {
		return errors.New("没有会话")
	}
	if mode != "" && !ValidMode(mode) {
		return fmt.Errorf("不认识的审批方式：%q", mode)
	}
	g.modeMu.Lock()
	if mode == "" {
		delete(g.sessionModes, conversation)
	} else {
		if g.sessionModes == nil {
			g.sessionModes = map[string]string{}
		}
		g.sessionModes[conversation] = mode
	}
	g.modeMu.Unlock()
	if g.sessions != nil && localPrivateKey(conversation) {
		if err := g.sessions.save(); err != nil {
			return fmt.Errorf("这次运行里已经生效，但没写进文件，重启后会回到原来的档：%w", err)
		}
	}
	return nil
}

// SessionMode 是这个会话自己覆盖的档（没覆盖返回 false）
func (g *Gate) SessionMode(conversation string) (string, bool) {
	g.modeMu.RLock()
	defer g.modeMu.RUnlock()
	m, ok := g.sessionModes[conversation]
	return m, ok
}

// ModeFor 是这个会话现在生效的审批档：会话自己的覆盖 > 派它的会话的覆盖（子任务）> 默认。
func (g *Gate) ModeFor(conversation string) string {
	if m, ok := g.SessionMode(conversation); ok {
		return m
	}
	if g.ParentOf != nil && strings.HasPrefix(conversation, "internal:") {
		if p := g.ParentOf(conversation); p != "" && p != conversation {
			if m, ok := g.SessionMode(p); ok {
				return m
			}
		}
	}
	return g.DefaultMode()
}

// ancestors 是子任务的内部会话一路往上派它的会话（最多四级，防环）；别的会话没有。给 ask 规则的会话范围用：
// 子任务替那个会话办事，那个会话设的"每次都问"不能靠派子任务躲开。放行规则不吃这个（子任务不继承上级会话的放行范围）。
func (g *Gate) ancestors(conversation string) []string {
	var out []string
	cur := conversation
	for i := 0; i < 4 && g.ParentOf != nil && strings.HasPrefix(cur, "internal:"); i++ {
		p := g.ParentOf(cur)
		if p == "" || p == cur || slices.Contains(out, p) {
			break
		}
		out = append(out, p)
		cur = p
	}
	return out
}

// analyzerWarmupDelay 是启动后多久预热命令解析器。
const analyzerWarmupDelay = 3 * time.Second

// defaultAnalyzeBudget 是拆一条命令最多等多久。常驻 pwsh 平时几十毫秒；冷启动（第一条、闲置 10 分钟被收掉以后）在机器忙时要好几秒，
// 原来的 4 秒在 CPU 80% 时反复撞上：明明开了 yolo 还弹审批卡，命令前也不拍检查点（2026-11-09 合并队列）。
// 这是"分析器真卡死了才等满"的上限：点停止由 ctx 马上返回，不用等满。
const defaultAnalyzeBudget = 20 * time.Second

// NewGate 设置受保护目录：数据目录 + 用户目录下放密钥的几个；工作目录每次调用显式传入。
// 在后台预热常驻 pwsh；没预热好或预热失败时，命令仍按 defaultAnalyzeBudget 往"问主人"收紧。
func NewGate(dataDir string) *Gate {
	analyzer := policy.NewAnalyzer()
	analyzer.WarmupLater(analyzerWarmupDelay)
	return NewGateWith(dataDir, analyzer)
}

// NewGateWith 用调用方给的解析器建门，不另起预热：测试夹具共用一个解析器，免得每建一道门就起一个 pwsh 再关掉
// （turn 的夹具一个包里建约 600 次，2026-11-01）。
func NewGateWith(dataDir string, analyzer *policy.Analyzer) *Gate {
	return &Gate{
		ProtectedDirs: withRealPaths(policy.DefaultProtectedDirs(dataDir)), Mode: "manual", ApprovalMinutes: 10, Analyzer: analyzer,
		Grants: LoadGrants(filepath.Join(dataDir, "config", "tool-grants.yml")), Conv: NewConvGrants(), Now: time.Now,
	}
}

// Protect 把更多目录并进受保护目录（每个目录连它的真实路径一起，去重；目录还不存在也行）。只在装配时调，门开始判定以后不要再改。
// 备份目录靠它进来：里面是数据目录的完整副本（库、明文配置、密钥文件），读它和读数据目录同样要问
func (g *Gate) Protect(dirs ...string) {
	seen := map[string]bool{}
	for _, d := range g.ProtectedDirs {
		seen[policy.LinkKey(d)] = true
	}
	for _, p := range withRealPaths(dirs) {
		if k := policy.LinkKey(p); !seen[k] {
			seen[k] = true
			g.ProtectedDirs = append(g.ProtectedDirs, p)
		}
	}
}

// withRealPaths：受保护目录本身可能是联接或符号链接（数据目录常放在别的盘再联过来），用它的真实路径去碰，字面比对认不出。
// 每个目录的真实路径也并进清单（去重；不存在的目录往上找最近存在的上级再接回去，和判目标用的 RealOf 一样）。
func withRealPaths(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		for _, p := range []string{d, RealOf(d)} {
			if k := policy.LinkKey(p); !seen[k] {
				seen[k] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// Call 是一次工具调用（参数是 pi 整理、校验以后的）
type Call struct {
	Identity kernel.Invocation
	ID       string
	Name     string
	Args     jsjson.Value
}

var selfTarget = regexp.MustCompile(`\b(mkdir|md)\b|\bnew-item\b[^|;\n]*\bdirectory\b|\bni\b[^|;\n]*\bdirectory\b`)

// AnalyzeShell 拆开一条命令；没有分析器、超时、被取消都返回 nil（调用方当"看不清"）。
func (g *Gate) AnalyzeShell(ctx context.Context, command string) *policy.Analysis {
	if g.Analyzer == nil {
		return nil
	}
	budget := g.AnalyzeBudget
	if budget <= 0 {
		budget = defaultAnalyzeBudget
	}
	return g.Analyzer.AnalyzeContext(ctx, command, budget)
}

// Check 照 gate：放行返回 (false, "")，拦下返回 (true, 给她看的原因)。要知道是哪一档放的、执行前还要复查的用 Clear。
func (g *Gate) Check(ctx context.Context, workdir, characterID string, aud policy.Audience, conversation string, c Call) (bool, string) {
	cl := g.Clear(ctx, workdir, characterID, aud, conversation, c)
	return cl.Blocked, cl.Reason
}

// Clear 判定一次工具调用。批准之后目标变了就从头再判一轮（规则、联接可能都变了），不拿旧批准执行。
func (g *Gate) Clear(ctx context.Context, workdir, characterID string, aud policy.Audience, conversation string, c Call) Clearance {
	for round := 0; round < staleAskLimit; round++ {
		if cl, stale := g.clearRound(ctx, workdir, characterID, aud, conversation, c, round); !stale {
			return cl
		}
	}
	return Clearance{Blocked: true, Reason: whyStaleGaveUp}
}

// NotOwner：这一轮不是主人发起的，或者在群的场合。身份按 ActorKind 认，不按名字；空身份的（内部调用、测试）只看会话，场合由 Decide 第一步挡。
func NotOwner(s kernel.Scope, conversation string) bool {
	return (s.ActorKind != "" && s.ActorKind != "owner") || s.Scene == "group" || strings.Contains(s.Conversation, ":group:") || strings.Contains(conversation, ":group:")
}

func (g *Gate) clearRound(ctx context.Context, workdir, characterID string, aud policy.Audience, conversation string, c Call, round int) (Clearance, bool) {
	spec, declared := policy.Specs[c.Name]
	if !declared {
		spec = policy.Undeclared
	}
	ownerConsole := aud.Channel == "console" && aud.PrivateOk
	// 命令先拆开看：拆不开（没装 pwsh、超时、没给分析器）就是"拆不开"，往"问主人"收紧；只在主人 × 主窗口才拆
	var shell *policy.Verdict
	shellFailed := false
	if v := c.Args.Obj["command"]; ownerConsole && c.Name == "powershell" && v.Kind == jsjson.String {
		if a := g.AnalyzeShell(ctx, v.Str); a != nil {
			j := policy.Judge(*a)
			shell = &j
		} else {
			shellFailed = true
		}
	}
	// 直接调方法改东西的（.Delete() 这类）不按"同一类"放行；拆不开的命令 KindOf 本来就给不出
	var kind *policy.Kind
	if ownerConsole && conversation != "" && (shell == nil || len(shell.MethodRisk) == 0) {
		kind = policy.KindOf(c.Name, c.Args, workdir, shell)
	}
	always := kind != nil && g.Grants.Match(kind.Key, "") && policy.CanAlways(c.Name, c.Args, declared, shell)
	var grantDirs []string
	hasConv := false
	if kind != nil {
		grantDirs, hasConv = g.Conv.Match(conversation, workdir, kind.Key, "")
	}
	granted := ""
	switch {
	case hasConv:
		granted = "conversation"
	case always:
		granted = "always"
	}
	env := policy.Env{Mode: g.ModeFor(conversation), Workdir: workdir, AutoApproveDirs: g.AutoApproveDirs, ProtectedDirs: g.ProtectedDirs, Audience: aud,
		Declared: declared, SelfPorts: g.SelfPorts, Shell: shell, ShellFailed: shellFailed, Granted: granted, GrantDirs: grantDirs,
		NotOwner: NotOwner(c.Identity.Scope, conversation), Rules: g.userRules(), Conversation: conversation, Ancestors: g.ancestors(conversation)}
	// 联接 / 符号链接：先解析成真实路径再交给判定
	env.Real = map[string]string{}
	for _, p := range append(policy.LinkPaths(spec, c.Args, env), policy.ExtraLinkPaths(spec, c.Args, env)...) {
		env.Real[strings.ToLower(p)] = RealOf(p)
	}
	// 下载来跑的包有没有在项目里声明过：只在命令里有 npx / bunx 这类时才读 package.json
	if shell != nil {
		for _, f := range shell.Fetches {
			if !f.Declarable {
				continue
			}
			d := policy.LinkKey(policy.Resolve(workdir, f.Dir))
			if env.ProjectTools == nil {
				env.ProjectTools = map[string][]string{}
			}
			if _, ok := env.ProjectTools[d]; !ok {
				env.ProjectTools[d] = ProjectToolsOf(d, workdir)
			}
		}
	}
	v := policy.Decide(c.Name, spec, c.Args, env)
	if v.Kind == "ask" && v.RuleKey != "" { // 被"每次都问"规则拦成问的：记下最近用过
		g.Grants.Touch(v.RuleKey, g.Now().UnixMilli())
	}
	var notes []string
	switch {
	case shellFailed:
		notes = []string{"拆不开这条命令，看不出它会做什么"}
	case shell != nil && !shell.ReadOnly:
		for _, x := range shell.Changes {
			notes = append(notes, "会改动："+x)
		}
		for _, x := range shell.Unclear {
			notes = append(notes, "看不清："+x)
		}
		if len(notes) > 12 {
			notes = notes[:12]
		}
	}
	switch v.Kind {
	case "deny":
		return Clearance{Blocked: true, Reason: v.Reason}, false
	case "allow":
		cl := g.allowed(v, kind, "", workdir, conversation)
		// 本来要批准的（会写、会跑命令、会联网）没问就放行了：留痕；只读的、只碰记忆的不算
		for _, e := range spec.Effects {
			if e == "write" || e == "exec" || e == "net" {
				cl.Traced = true
				if g.OnAuto != nil {
					req := g.Describe(workdir, characterID, c.ID, c.Name, c.Args)
					req.Identity = c.Identity
					req.Egress = g.egressOf(conversation, c)
					g.OnAuto(req, cl)
				}
				break
			}
		}
		return cl, false
	}
	// 要问：这条命令碰到的外面（点"这个对话"时记下这几个外部目录）；碰到外面的不给"始终"
	var outPaths []string
	outBlind := false
	if c.Name == "powershell" && shell != nil {
		outPaths, outBlind = policy.OutsideTargets(env)
	}
	command := ""
	if v := c.Args.Obj["command"]; v.Kind == jsjson.String {
		command = v.Str
	}
	selfOnly := policy.HasDeleteVerb(command) || selfTarget.MatchString(jsstr.LowerASCII(command))
	var outDirs []string
	for _, p := range outPaths {
		if d := GrantDirOf(p, selfOnly); !contains(outDirs, d) {
			outDirs = append(outDirs, d)
		}
	}
	var options []string
	if kind != nil && v.Limit != "once" {
		if v.Limit == "conversation" || len(outPaths) > 0 || outBlind || !policy.CanAlways(c.Name, c.Args, declared, shell) {
			options = []string{"conversation"}
		} else {
			options = []string{"conversation", "always"}
		}
	}
	// 先记指纹再读预览：两步之间变了只会多问一次，不会漏
	print := g.targetPrint(spec, c, env, workdir)
	before := print()
	req := g.Describe(workdir, characterID, c.ID, c.Name, c.Args)
	req.Identity = c.Identity
	req.Conversation = conversation
	req.Workdir = workdir
	req.Egress = g.egressOf(conversation, c)
	req.Why = v.Reason
	req.Notes = notes
	req.Retry = round
	if round > 0 {
		req.Notes = append([]string{"批准之后目标变了，这是现在的样子，再看一眼"}, req.Notes...)
	}
	var pending *Pending
	if kind != nil {
		label := kind.Label
		if len(outDirs) > 0 {
			label = kind.Label + "动到 " + strings.Join(outDirs, "、") + " 里的"
		}
		if len(options) > 0 {
			req.Options, req.KindLabel = options, label
		}
		pending = &Pending{Key: kind.Key, Label: label, Tool: c.Name, Conversation: conversation, Workdir: workdir, Dirs: outDirs}
	}
	askReal := "" // 建卡时写入目标的真实路径，批准后复查用
	if c.Name == "write" || c.Name == "edit" {
		askReal = RealOf(req.Subject)
	}
	d := Decision{Kind: "cancelled"}
	if g.Ask != nil {
		d = g.Ask(ctx, req, pending)
	}
	if d.Kind != "approved" {
		return blockedBy(d), false
	}
	// 批准后复查：主人在 QQ 里看的是建卡时的内容，批准到达时文件、命令、真实路径变了就不执行（本机点的看的是当前卡，不在此列）
	if d.Via == "qq" {
		if why := g.changedSince(workdir, characterID, c, req, askReal); why != "" {
			return Clearance{Blocked: true, Reason: "审批通过之后、执行之前，" + why + "，和所有者批准时看到的不一样，没有执行。需要的话请重新发起。"}, false
		}
	}
	if print() != before {
		return Clearance{}, true
	}
	cl := g.answered(d, kind, "", workdir, conversation)
	if cl.By == policy.ByOnce {
		cl.Recheck = func() string {
			if print() != before {
				return whyStale
			}
			return ""
		}
	}
	return cl, false
}

// blockedBy 把没批准的回答换成给模型看的那一句。
func blockedBy(d Decision) Clearance {
	switch d.Kind {
	case "denied":
		if d.Feedback != "" {
			return Clearance{Blocked: true, Reason: d.Feedback}
		}
		said := ""
		if d.Reason != "" {
			said = "，原因：" + d.Reason
		}
		return Clearance{Blocked: true, Reason: fmt.Sprintf(whyDenied, said)}
	case "expired":
		// 不写分钟数：期限跟着 approvalMinutes 变，以前照搬 Bun 写死"10 分钟"（Bun 残留盘点）
		return Clearance{Blocked: true, Reason: whyExpired}
	}
	return Clearance{Blocked: true, Reason: whyCancelled}
}

// allowed 是 Decide 放行的结局：靠规则放行的记下最近用过，执行前还要确认规则没被撤销。
func (g *Gate) allowed(v policy.Decision, kind *policy.Kind, guard, workdir, conversation string) Clearance {
	cl := Clearance{By: v.By, Reason: v.Reason}
	if kind != nil {
		cl.Rule, cl.Label = kind.Key, kind.Label
	}
	if v.By == policy.ByRule { // 主人自己写的放行规则：和"记住"同一份存储，留痕、撤销立刻生效
		cl.Rule, cl.Label, cl.Traced = v.RuleKey, v.RuleLabel, true
		cl.Recheck = g.ruleStill(policy.ByAlways, v.RuleKey, "", workdir, conversation)
		g.Grants.Touch(v.RuleKey, g.Now().UnixMilli())
		return cl
	}
	if kind != nil && (v.By == policy.ByConversation || v.By == policy.ByAlways) {
		cl.Traced = true
		cl.Recheck = g.ruleStill(v.By, kind.Key, guard, workdir, conversation)
		at := g.Now().UnixMilli()
		if v.By == policy.ByConversation {
			g.Conv.Touch(conversation, kind.Key, at)
		} else {
			g.Grants.Touch(kind.Key, at)
		}
	}
	return cl
}

// answered 是主人回答"批准"的结局。别的审批方（测试、自动答复）不填 By 就当允许一次。
func (g *Gate) answered(d Decision, kind *policy.Kind, guard, workdir, conversation string) Clearance {
	by := d.By
	if by == "" {
		by = policy.ByOnce
	}
	cl := Clearance{By: by, Traced: true}
	if kind != nil {
		cl.Rule, cl.Label = kind.Key, kind.Label
	}
	if kind != nil && by != policy.ByOnce {
		cl.Recheck = g.ruleStill(by, kind.Key, guard, workdir, conversation)
	}
	return cl
}

// ruleStill 是"这条规则还在"的复查：撤销立刻生效，放行之后还没开始执行的也算。
func (g *Gate) ruleStill(by, key, guard, workdir, conversation string) func() string {
	return func() string {
		ok := false
		if by == policy.ByConversation {
			_, ok = g.Conv.Match(conversation, workdir, key, guard)
		} else {
			ok = g.Grants.Match(key, guard)
		}
		if !ok {
			return whyRevoked
		}
		return ""
	}
}

// targetPrint 是主人批准时看到的目标的指纹：联接解析到哪里；写、改文件时还有文件内容。
// 命令和参数在这次调用里是冻结的，不用比；命令会碰的文件内容主人在卡片上没看到，也不比。
func (g *Gate) targetPrint(spec policy.Spec, c Call, env policy.Env, workdir string) func() string {
	links := policy.LinkPaths(spec, c.Args, env)
	content := ""
	unknown := false
	if spec.Paths != nil && spec.Classify == nil && slices.Contains(spec.Effects, "write") {
		raw := policy.PathArg(c.Args)
		abs := policy.ToolPath(raw, workdir)
		if opaqueTarget(raw, abs, workdir) {
			unknown = true // 网络共享、设备路径、驱动器相对路径（含工作目录本身在共享上）：卡片出来前不许真的连过去读它，指纹按“未知”算
		} else {
			content = abs
		}
	}
	return func() string {
		h := sha256.New()
		for _, p := range links {
			io.WriteString(h, p+"\x00"+RealOf(p)+"\x00")
		}
		if unknown {
			io.WriteString(h, "unknown-target")
		}
		if content != "" {
			hashFile(h, content)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
}

// 读目标文件用的几个入口；测试里换成会计数的桩，确认网络共享路径不会走到这里
var (
	fileStat     = os.Stat
	fileOpen     = func(p string) (io.ReadCloser, error) { return os.Open(p) }
	fileReadFile = os.ReadFile
)

// opaqueTarget：这次要写的目标落不到一个确定的本地位置上——原文是网络共享、设备路径、驱动器相对路径，
// 或者解出来的绝对路径是（工作目录本身在共享上时，相对路径解出来也是）。
func opaqueTarget(raw, abs, workdir string) bool {
	return policy.IsOpaquePath(raw) || policy.IsOpaquePath(abs) || policy.IsOpaquePath(workdir) || policy.IsOpaquePath(policy.ToolPath(raw, workdir))
}

// hashFile 把文件内容写进指纹；不存在、读不了、不是普通文件各算一种样子。
func hashFile(h io.Writer, path string) {
	if policy.IsOpaquePath(path) { // 兜底：调用方漏判时也不去连
		io.WriteString(h, "unknown-target")
		return
	}
	fi, err := fileStat(path)
	if err != nil {
		io.WriteString(h, "stat-failed")
		return
	}
	if !fi.Mode().IsRegular() {
		io.WriteString(h, "not-regular:"+fi.Mode().String())
		return
	}
	f, err := fileOpen(path)
	if err != nil {
		io.WriteString(h, "open-failed")
		return
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		io.WriteString(h, "read-failed")
	}
}

// changedSince 重新生成这次调用的卡片，和主人批准的那张比：标题、内容、预览、写入目标的真实路径任何一项不同都返回说明。
// 没变返回空串。只在回答不是本机点的时候用（见 Check）。
func (g *Gate) changedSince(workdir, characterID string, c Call, was Request, realAtAsk string) string {
	now := g.Describe(workdir, characterID, c.ID, c.Name, c.Args)
	if now.Title != was.Title || now.Subject != was.Subject || now.Purpose != was.Purpose || now.SubjectTruncated != was.SubjectTruncated {
		return "要做的事变了"
	}
	if (now.Preview == nil) != (was.Preview == nil) || now.Preview != nil && *now.Preview != *was.Preview {
		return "文件内容变了"
	}
	if (c.Name == "write" || c.Name == "edit") && was.Subject != "" && RealOf(was.Subject) != realAtAsk {
		return "写入位置指向了别处"
	}
	return ""
}

// egressOf 取这次调用的外发检查；没接上 Egress 钩子就没有
func (g *Gate) egressOf(conversation string, c Call) *egress.Report {
	if g.Egress == nil {
		return nil
	}
	return g.Egress(conversation, c.ID, c.Name, c.Args)
}

// Strings 把参数里所有的字符串（含嵌套的对象和数组）摊平，外发检查用；太多（超过 200 个）或嵌套太深（超过 8 层）的后面不取，
// truncated 为真告诉调用方“没取全”，卡上会说有一部分没检查。
func Strings(v jsjson.Value) (out []string, truncated bool) {
	var walk func(v jsjson.Value, depth int)
	walk = func(v jsjson.Value, depth int) {
		if depth > 8 || len(out) >= 200 {
			truncated = true
			return
		}
		switch v.Kind {
		case jsjson.String:
			out = append(out, v.Str)
		case jsjson.Array:
			for _, x := range v.Arr {
				walk(x, depth+1)
			}
		case jsjson.Object:
			for _, k := range v.Keys {
				walk(v.Obj[k], depth+1)
			}
		}
	}
	walk(v, 0)
	return out, truncated
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

var titles = map[string]string{"read": "读文件", "ls": "列目录", "grep": "搜文件内容", "find": "找文件"}

const previewMax = 6000

// Describe 生成卡片的标题、主体和预览；编辑计算与实际写入共用 edittext。
func (g *Gate) Describe(workdir, characterID, toolCallID, tool string, args jsjson.Value) Request {
	now := g.Now()
	r := Request{ID: store.NewID(now), CharacterID: characterID, ToolCallID: toolCallID, Tool: tool,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.UnixMilli() + int64(g.ApprovalMinutes*60_000)}
	if v := args.Obj["purpose"]; v.Kind == jsjson.String && jsstr.Trim(v.Str) != "" {
		r.Purpose = store.JSSlice(jsstr.Trim(v.Str), 120)
	}
	if tool == "powershell" {
		r.Title, r.Subject = "运行 PowerShell 命令", policy.JSString(args, "command", "")
		if v := args.Obj["background"]; v.Kind == jsjson.Bool && v.Bool {
			r.Title = "在后台运行 PowerShell 命令"
		}
		return r
	}
	path := policy.PathArg(args)
	abs := policy.ToolPath(path, workdir) // 卡片上写的就是工具实际操作的位置（~、@、file:///、/c/… 都按工具的办法换成路径）
	// 读、列、搜、找（碰到受保护路径才会到这里）：只写要碰哪里、搜什么
	if t, ok := titles[tool]; ok {
		r.Title, r.Subject = t, abs
		if p, ok := args.Obj["pattern"]; ok && truthy(p) {
			r.Subject = abs + "\n搜：" + policy.JSString(args, "pattern", "")
		}
		return r
	}
	if tool == "task" {
		r.Title, r.Subject = "派子任务", policy.JSString(args, "agent", "")+"："+policy.JSString(args, "description", "")
		r.Preview = cut("content", policy.JSString(args, "prompt", ""))
		return r
	}
	// 没声明的工具：参数原样给主人看
	if tool != "write" && tool != "edit" {
		full := jsjson.StringifyIndent(args, 2)
		r.Title, r.Subject = "运行工具 "+tool, store.JSSlice(full, previewMax)
		r.SubjectTruncated = store.JSLen(full) > previewMax
		return r
	}
	var old *string
	var oldBytes []byte
	if !opaqueTarget(path, abs, workdir) { // 网络共享等不读：预览里当“文件不存在”算，卡片出来前不去连
		if fi, err := fileStat(abs); err == nil && fi.Mode().IsRegular() {
			if b, err := fileReadFile(abs); err == nil {
				oldBytes = b
				s := string(b)
				if tool == "write" {
					s = jsstr.DecodeUTF8(b)
				}
				old = &s
			}
		}
	}
	var next *string
	if tool == "write" {
		// 卡上的字节必须就是 write 写进去的字节：同一个 lineend.Final，同样的路径、原文件开头和正文
		s := lineend.Final(abs, oldBytes, policy.JSString(args, "content", ""))
		next = &s
	}
	if tool == "edit" && old != nil {
		edits, err := edittext.Parse(args)
		if err != nil {
			r.Title, r.Subject, r.Preview = "修改文件", abs, cut("content", err.Error())
			return r
		}
		change, err := edittext.Apply(*old, edits, path)
		if err != nil {
			r.Title, r.Subject, r.Preview = "修改文件", abs, cut("content", err.Error())
			return r
		}
		next = &change.Content
	}
	switch {
	case tool == "write" && old == nil:
		r.Title = "新建文件"
	case tool == "write":
		r.Title = "覆盖文件"
	default:
		r.Title = "修改文件"
	}
	r.Subject = abs
	switch {
	case old != nil && next != nil:
		d, _, _ := jsdiff.GenerateDiffString(*old, *next, 4)
		r.Preview = cut("diff", d)
	case next != nil:
		r.Preview = cut("content", *next)
	case tool == "edit":
		r.Preview = cut("content", jsjson.StringifyIndent(args.Obj["edits"], 2))
	}
	return r
}

func truthy(v jsjson.Value) bool {
	switch v.Kind {
	case jsjson.Null:
		return false
	case jsjson.Bool:
		return v.Bool
	case jsjson.Number:
		return v.Num != 0 && v.Num == v.Num
	case jsjson.String:
		return v.Str != ""
	}
	return true
}

func cut(kind, text string) *Preview {
	n := store.JSLen(text)
	if n > previewMax {
		return &Preview{Kind: kind, Text: store.JSSlice(text, previewMax), Truncated: true}
	}
	return &Preview{Kind: kind, Text: text}
}

// RealOf 照 realOf：解析联接 / 符号链接以后的真实路径（只读元数据）；要写的新文件还不存在时往上找到最近一个存在的上级，解析它，再把剩下的接回去
func RealOf(abs string) string {
	if policy.IsOpaquePath(abs) { // 网络共享、设备路径、驱动器相对路径：不解析，解析会真的连过去
		return abs
	}
	var rest []string
	cur := abs
	for {
		if r, err := realPath(cur); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		up := filepath.Dir(cur)
		if up == cur {
			return abs
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = up
	}
}

// RealStrict 只解析已存在的目标；认不准真实路径就返回错误。
func RealStrict(abs string) (string, error) {
	return realPath(abs)
}

// RealForWrite 允许新建目标：只有明确不存在才往父目录退，其他错误原样返回。
func RealForWrite(abs string) (string, error) {
	return realForWrite(abs, realPath)
}

func realForWrite(abs string, resolve func(string) (string, error)) (string, error) {
	cur := filepath.Clean(abs)
	var rest []string
	for {
		r, err := resolve(cur)
		if err == nil {
			if len(rest) == 0 {
				return r, nil
			}
			rest = append(rest, r)
			slices.Reverse(rest)
			return filepath.Join(rest...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		up := filepath.Dir(cur)
		if up == cur {
			return "", err
		}
		part := filepath.Base(cur)
		if part == ".." || filepath.IsAbs(part) || filepath.VolumeName(part) != "" {
			return "", fmt.Errorf("新建目标的剩余路径不安全：%q", part)
		}
		rest = append(rest, part)
		cur = up
	}
}

// ProjectToolsOf 照 projectToolsOf：从 dir 往上到工作目录的每一级里，package.json 的依赖名和 node_modules/.bin 下的程序名（小写）
func ProjectToolsOf(dir, workdir string) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	root := policy.ResolveCwd(workdir)
	cur := policy.Resolve(workdir, dir)
	if !policy.IsInside(cur, []string{root}, root) {
		return out
	}
	binExt := regexp.MustCompile(`\.(cmd|ps1|exe|bat)$`)
	for {
		if b, err := os.ReadFile(filepath.Join(cur, "package.json")); err == nil {
			if pkg, err := jsjson.Parse(string(b)); err == nil && pkg.Kind == jsjson.Object {
				for _, k := range []string{"dependencies", "devDependencies"} {
					if deps := pkg.Obj[k]; deps.Kind == jsjson.Object {
						for _, n := range deps.Keys {
							add(strings.ToLower(n))
						}
					}
				}
			}
		}
		if es, err := os.ReadDir(filepath.Join(cur, "node_modules", ".bin")); err == nil {
			for _, e := range es {
				n := e.Name()
				if m := binExt.FindStringIndex(jsstr.LowerASCII(n)); m != nil {
					n = n[:m[0]]
				}
				add(strings.ToLower(n))
			}
		}
		if strings.EqualFold(cur, root) {
			break
		}
		up := filepath.Dir(cur)
		if up == cur {
			break
		}
		cur = up
	}
	return out
}

// GrantDirOf 照 grantDirOf："这个对话里都允许"记下的外部目录：已有的目录记它自己；不存在的记它所在的目录，
// 但上级是盘根、用户目录根或它们的直接子目录时、以及建目录、删除这类命令，只记目标路径本身
func GrantDirOf(p string, selfOnly bool) string {
	if policy.IsOpaquePath(p) { // 网络共享、设备路径、驱动器相对路径：不 Stat（会连过去），也不往上找
		return p
	}
	if fi, err := fileStat(p); err == nil && fi.IsDir() {
		return p
	}
	if selfOnly {
		return p
	}
	home, _ := os.UserHomeDir()
	key := func(d string) string { return strings.ToLower(policy.ResolveCwd(d)) }
	wide := map[string]bool{key(home): true, key(filepath.Dir(home)): true}
	isRoot := func(d string) bool { return filepath.Dir(d) == d }
	up := filepath.Dir(p)
	if isRoot(up) || wide[key(up)] || isRoot(filepath.Dir(up)) || wide[key(filepath.Dir(up))] {
		return p
	}
	return up
}
