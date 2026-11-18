package api

import "context"

// 记忆页的只读接口（规格-记忆页 4.1）。和 Reader 分开：演示用的假实现不用跟着改，也只交给桌面窗口，
// 不交给 ctlserver、渠道、对话循环、工具（internal/archcheck 钉住 memview 不被它们引用）。
// 一个角色一个记忆库，页面一次看一个角色；所有场合的记忆都列出来，每条标清范围（页面是"库里有什么"，不是"她在某个场合想得起什么"）。
// 发布的值不可再改，和 Reader 一样。字符串都已经清过（控制字符、零宽字符换成占位，列表并成一行），界面当纯文本画。

// MemoryReader 不提供写入、SQL、记忆库路径。查询都能取消，默认时限 5 秒，一次一个。
type MemoryReader interface {
	// WatchMemory：概览订阅。先给一份快照，之后库里有变化（别的连接提交过）再给新的；订阅活着才轮询
	WatchMemory(context.Context) (Subscription[MemoryOverview], error)
	ListMemory(context.Context, MemoryListRequest) (Result[MemoryPage], error)
	ReadMemoryDetail(ctx context.Context, character, ref string) (Result[MemoryDetail], error)
	ReadMemoryEvidence(ctx context.Context, character, ref string, cursor Cursor) (Result[MemoryEvidencePage], error)
	ReadMemoryRevisions(ctx context.Context, character, ref string, cursor Cursor) (Result[MemoryRevisionPage], error)
	ReadMemoryRecent(ctx context.Context, character string) (Result[[]MemoryRecentChange], error)
	// ReadMemoryQuote 按（会话, seq）从聊天库取出处原话，取不到的照实说（MemoryQuote.Status）
	ReadMemoryQuote(ctx context.Context, character string, src MemorySource) (Result[MemoryQuote], error)
}

// MemoryWho 是一个人：身份类型只由数据里的类型和 id 的形状定，名字只用来显示（规格-记忆页 3.5）
type MemoryWho struct {
	Kind string // owner（你）/ self（她自己）/ person（qq:<号> 这种形状的 id）/ other（别的写法，原样显示）/ none（没写）
	Name string // 显示名；认人那一处查不到是空
	ID   string // 打码后的 id；person 才有
	Raw  string // other 的原文（已清过、截到 40 字）
}

// MemoryScope 是一个范围值
type MemoryScope struct {
	Kind  string // public / owner / conv / person / unknown
	Label string // 人话：所有场合 / 主人场合 / 只在〈会话〉 / 只在和〈人〉的私聊 / 范围不认识（原文前 40 字）
}

type MemoryChoice struct {
	Value string // 筛选用的原值
	Label string
	N     int
}

// MemoryCount 是一个（种类, 状态组）的条数；片段的种类写 episode
type MemoryCount struct {
	Kind  string
	Group string
	N     int
}

type MemoryQuarantine struct {
	Active                      bool
	Facts, Candidates, Episodes int
}

type MemoryCharacter struct {
	ID, Name string
	HasDB    bool   // false = 还没有记忆库（按钮灰着）
	Err      string // 有库但打不开 / 读不了的原因；空 = 正常
	Total    int
	Counts   []MemoryCount
	Migrated bool // false = 这个库还没迁移（页面顶上提示）
	// Rev 在同一次订阅里，这个角色的库每变一次加一；界面拿它和列表取到时的比，出"有 N 处变化"
	Rev           uint64
	Quarantine    MemoryQuarantine
	Conversations []MemoryChoice // 下拉：条数最多的 20 个会话
	Subjects      []MemoryChoice // 下拉：条数最多的 20 个"关于谁"
	// 确认三项配置（经 memory.ConfirmOccasionsFor 换算过），详情里的"已攒 N / M 次"用
	ConfirmNeed int
}

type MemoryOverview struct {
	Characters []MemoryCharacter
}

type MemoryListRequest struct {
	Character    string
	Kind         string // 空 = 全部事实；fact / promise / preference / boundary；episode = 片段
	Groups       []string
	ScopeKind    string // public / owner / conv / person
	Conversation string
	Subject      string
	Query        string
	Cursor       Cursor
	Limit        int // 0 = 50
}

type MemoryRow struct {
	Ref          string // f:<id> / e:<id>
	Kind         string
	Status       string
	ConfirmedBy  string
	Group        string // owner / ai / imported / candidate / revoked / superseded / active（片段）/ other
	Text         string
	TextCut      int
	Subject      MemoryWho
	AttributedTo MemoryWho
	Conversation string // 会话的显示名
	Scopes       []MemoryScope
	CreatedAt    int64 // 毫秒；0 = 不详
	LearnedAt    int64
	Temporal     string
	Expired      bool
	FromSeq      int64
	ToSeq        int64
}

type MemoryPage struct {
	List[MemoryRow]
	Truncated bool // 搜索结果多于上限，只给了前 200 条
}

// MemoryPlace 是一个场合里想不想得起：Place 是 local-private / local-group / platform-private / platform-group
type MemoryPlace struct {
	Place   string
	Visible string   // yes / no / when
	When    []string // when 时想得起的那几种听众（整词）
}

type MemoryRecall struct {
	Can    bool
	Reason string // ok / expired / quarantined / revoked / superseded / other
}

type MemoryOccasions struct {
	Have, Need, Self, Rows int
	Auto                   bool
}

// MemorySource 是一句出处：会话（原值，取原话用）加序号
type MemorySource struct {
	Conversation string
	Seq          int64
	Label        string // 会话的显示名；取原话时不用
}

type MemoryDetail struct {
	Row          MemoryRow
	Origin       string
	SpeechAct    string
	SupersededBy string
	Recall       MemoryRecall
	Places       []MemoryPlace
	Occasions    *MemoryOccasions
	Sources      []MemorySource
	SourcesTotal int
	Migrated     bool
	// Whisper 不空：这条是悄悄话处理那一轮里记下的，写明哪一批、来源会话、被引事件；记忆页据它写“来自悄悄话”
	Whisper string
}

type MemoryEvidenceRow struct {
	Day          string
	Conversation MemorySource // 证据行自带的会话和序号，不用事实的
	Speaker      MemoryWho
	BySubject    bool
	Verdict      string
	At           int64
}

type MemoryEvidencePage struct {
	List[MemoryEvidenceRow]
	Total   int
	Missing bool
}

type MemoryRevision struct {
	ID     int64
	At     int64
	Who    string // 整理 / 你让她记 / 你在界面上 / 导入 / 别的原样
	Did    string // 写下 / 取代 / 撤销 / 恢复 / 放行 / 改范围 / 导入 / 真删
	Change string // "待确认 → 有效，放行者：她自己"；没记前后状态是空
	Reason string
}

type MemoryRevisionPage struct {
	List[MemoryRevision]
}

type MemoryRecentChange struct {
	MemoryRevision
	Ref    string // f:<id>
	Text   string
	Exists bool
}

// MemoryQuote 的 Status：ok（正文在 Text）/ recalled（后来被撤回了，不给正文）/ missing（找不到：不在库里，或这个角色没看见过）/ unreadable（那个平台的聊天记录现在读不到）
type MemoryQuote struct {
	Status   string
	Platform string // unreadable 时写是哪个平台
	Who      MemoryWho
	At       int64
	Text     string
}
