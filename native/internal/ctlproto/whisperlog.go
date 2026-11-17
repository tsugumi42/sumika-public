package ctlproto

import "strings"

// 对话日志（规格-群聊引用和路由 第十五节）：右侧栏和 sumika-ctl whisper log 读同一份投影，类型放在这里。
// 日志是现有记录的投影：悄悄话会话里的事件、trace 库里这一轮的 steps 和 kernel_tasks，加一张关联表 whisper_links。

// 条目种类（LogEntry.Kind）。
const (
	LogWhisper = "whisper" // 主人发出的悄悄话：引用卡、对象、主人的话
	LogArrange = "arrange" // 安排：某位被指的角色被安排去处理，带状态
	LogThink   = "think"   // 思考
	LogTool    = "tool"    // 工具调用
	LogMemory  = "memory"  // 记忆写入
	LogSpawn   = "spawn"   // 唤起：她叫起的子任务
	LogReturn  = "return"  // 返回：子任务把结果交回
	LogNote    = "note"    // 处理记录：进背景的那段话
)

// 一页日志的条数：默认 50、上限 200；批次整批给，不被页界切开，所以一页可能多于 Limit。
const (
	LogPageDefault = 50
	LogPageMax     = 200
)

// LogQuote 是悄悄话里的一张引用卡（最多三行摘要）。
type LogQuote struct {
	Name string `json:"name"`
	At   int64  `json:"at,omitempty"`
	Text string `json:"text"`
}

// LogEntry 是日志里的一行，只带摘要；全文用 ReadLogEntry。Batch 是引起它的悄悄话批号（cause），Parent 是上一级条目的 id。
type LogEntry struct {
	ID        string     `json:"id"`
	Batch     string     `json:"batch"`
	Kind      string     `json:"kind"`
	Character string     `json:"character,omitempty"` // 参与者：哪位角色（悄悄话条目是主人，留空）
	Parent    string     `json:"parent,omitempty"`
	At        int64      `json:"at"`
	State     string     `json:"state,omitempty"` // arrange 的状态，沿用 Whisper* 常量；tool 的状态 running / done / error / waiting
	Position  int        `json:"position,omitempty"`
	Summary   string     `json:"summary"`
	Runes     int        `json:"runes,omitempty"` // think：全文字数
	Targets   []string   `json:"targets,omitempty"`
	Quotes    []LogQuote `json:"quotes,omitempty"`
	Turn      string     `json:"turn,omitempty"` // 这一行属于哪一轮（回合 id），有就能到上下文里看那一轮
	Err       bool       `json:"err,omitempty"`
	Waiting   bool       `json:"waiting,omitempty"` // 工具在等批准
	Busy      bool       `json:"busy,omitempty"`    // arrange：悄悄话到的时候她在来源会话里有回合在跑
	// 工具调用的外发检查（审批卡上同一个编号）：只有编号和几处、几处可疑，不含地址和正文
	Egress       string `json:"egress,omitempty"`
	EgressN      int    `json:"egressN,omitempty"`
	EgressDanger int    `json:"egressDanger,omitempty"`
}

// LogPage 是一页日志，条目按时间从早到晚。Next 非空表示还有更早的，原样带回来取下一页；Running 说页里有没到终态的批。
type LogPage struct {
	Entries []LogEntry `json:"entries"`
	Next    string     `json:"next,omitempty"`
	Running bool       `json:"running,omitempty"`
}

// LogDetail 是一条的全文。Thinking 的原文默认不给（ctl 要 --with-thinking），界面里点开才读。
type LogDetail struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text"`
	Args   string `json:"args,omitempty"`
	Hidden bool   `json:"hidden,omitempty"` // 思考原文被收起了（没要求给）
}

// WhisperLogArgs 是 whisper log / list 的参数。Conversation 是自检来源会话；Batch 限一批；Entry 读一条的全文。
type WhisperLogArgs struct {
	Conversation string `json:"conversation,omitempty"`
	Batch        string `json:"batch,omitempty"`
	Entry        string `json:"entry,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	WithThinking bool   `json:"withThinking,omitempty"`
}

// WhisperListItem 是 whisper list 的一行：不带任何正文。
type WhisperListItem struct {
	Batch     string `json:"batch"`
	Character string `json:"character"`
	State     string `json:"state"`
	At        int64  `json:"at"`
}

// WhisperLogResult 是 whisper log / list 的 data：log 给 Page 或 Detail，list 给 Items。
type WhisperLogResult struct {
	Page   *LogPage          `json:"page,omitempty"`
	Detail *LogDetail        `json:"detail,omitempty"`
	Items  []WhisperListItem `json:"items,omitempty"`
}

// ValidLogConversation 自检来源会话的写法合不合：console:<角色>:topic:<话题>，不带空白和控制字符；是不是真的自检话题由后端按 audience 认。
func ValidLogConversation(s string) bool {
	if len(s) > 200 || !strings.HasPrefix(s, "console:") || !strings.Contains(s, ":topic:") {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return isControl(r) || r == ' ' || r == '\n' || r == '\t' })
}

// ValidLogEntry 条目 id 的写法合不合（say:/arr:/note:/step:/task:/ret: 加后面的标识）。
func ValidLogEntry(s string) bool {
	kind, rest, ok := strings.Cut(s, ":")
	if !ok || rest == "" || len(s) > 200 {
		return false
	}
	switch kind {
	case "say", "arr", "note", "step", "task", "ret":
	default:
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return isControl(r) || r == ' ' || r == '\n' || r == '\t' })
}

// 日志里外面来的文字（被引原文、模型的话、工具结果）出去前的字数上限。
const (
	MaxLogSummary = 300
	MaxLogDetail  = 32 * 1024
)

// CleanLogPage 把一页日志里外面来的文字过一遍控制字符，并截短。
func CleanLogPage(p LogPage) LogPage {
	es := make([]LogEntry, len(p.Entries)) // 空也给 [] 不给 null
	for i, e := range p.Entries {
		e.Summary = Truncate(Clean(e.Summary), MaxLogSummary)
		e.Character, e.State = Clean(e.Character), Clean(e.State)
		e.Egress = Truncate(Clean(e.Egress), 32)
		for j, q := range e.Quotes {
			e.Quotes[j] = LogQuote{Name: Truncate(Clean(q.Name), MaxQuoteName), At: q.At, Text: Truncate(CleanText(q.Text), MaxLogSummary)}
		}
		es[i] = e
	}
	p.Entries = es
	p.Next = Clean(p.Next)
	return p
}

// CleanLogDetail 同上，给一条的全文。
func CleanLogDetail(d LogDetail) LogDetail {
	d.Title = Truncate(Clean(d.Title), MaxLogSummary)
	d.Text = CleanText(Truncate(d.Text, MaxLogDetail))
	d.Args = CleanText(Truncate(d.Args, MaxLogDetail))
	return d
}
