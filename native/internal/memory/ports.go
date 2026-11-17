package memory

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/prompts"
)

// 记忆包碰外面只走这几个端口（规格-记忆-v2 第十节）：事件来源由 store 实现，后台模型走 internal/llm，嵌入是 internal/embed

// Event 是会话里的一条事件，给后台整理、recall_raw 读原文。
// 字段按消费者加、不照抄 history.Event（13.2.2，W0 核对后的清单）；store 实现时哪些列直接有、哪些要现算，见 13.2.12 的 W0 条。
// 过滤放在记忆包里（B8），store 不替它丢：补录、自检、陌生人的行都带标签送来
type Event struct {
	Seq          int64
	Conversation string
	At           int64 // 毫秒

	// 谁说的：B8 的素材过滤、B11 的归属降级、B12 的聊天记录身份标签用。身份标签只由 ActorKind 定，不由名字定
	ActorKind string // owner / character / human
	ActorID   string // 主人是 owner，群友是 qq:<号>，角色是角色 id；替代原来的 Sender
	ActorName string // 发言时的显示名：群名片，没有就昵称。取不到给空，不退回号码（边界 ㉑）
	Origin    string // owner / peer / human / external / character / automation:*

	Kind string // 不是 message 的一律不整理
	Text string // 图片只有 [图片] 占位，描述在 Images 里

	Quote  *Quote  // 回复别人时被引用的那条；库里 reply_to 存的是它的 id、不是 seq，所以由 store 带出来
	Images []Image // 按占位出现的先后

	Backfill bool // 补录的行（segments 里的 backfill）：不整理，也不当证据（ADR-092）
	Selftest bool // 自检的行
	Unsent   bool // 她的回复没发出去（试运行、被守卫拦下、断连、被打断）：不算她说过
	Revoked  bool // 撤回了（读的时候算：有撤回通知指向它）
	Seen     bool // 这个角色看见过（observations 里有）；陌生人私聊没有，不整理
}

// Quote 是被引用的那条：说话人和开头几个字（几个字由 store 定）
type Quote struct {
	ActorKind string
	ActorID   string
	ActorName string
	Text      string
}

// Image 是消息里的一张图：Ready 是这张图已经有了识图结果（看完或看失败都算），没好的批次在这条之前截断（13.2.13 ⑧）
type Image struct {
	Hash  string
	Brief string // 当前提示词版本的那一行；只写看得见的样子，feeling 不进来
	Ready bool
}

// EventSource 读会话事件（store 实现）。按角色取：Seen 来自这个角色的 observations，装配时给每个角色一个视图
type EventSource interface {
	// Events 取这个会话 afterSeq 之后最早的 limit 条，按 seq 升序。整理要的是游标之后的，不是最新的
	// （history.EventsSince 取的是最新的 limit 条，直接拿来会把中间的跳过去）
	Events(ctx context.Context, conv string, afterSeq int64, limit int) ([]Event, error)
	// Revoked 按 seq 问这几条现在撤回了没有：整理完以后才来的撤回靠它把对应的记忆降成待确认。
	// seq 只在一个库里唯一（聊天记录按平台分库，规格-聊天记录按平台分库 第四节），要带会话才知道去哪个库查
	Revoked(ctx context.Context, conv string, seqs []int64) (map[int64]bool, error)
	// LastSaid 取这个会话里她看到的最近一句人说的话（kind 是 message、origin 是 owner 或 peer），没有 ok 为假。
	// 通用最近消息查询；remember 不再使用它，改按宿主绑定 InputSeqs 精确读取。
	LastSaid(ctx context.Context, conv string) (ev Event, ok bool, err error)
	// Find 在她看到过的事件里按字找原话（recall_raw，13.2.3）。conv 空表示她看到过的所有会话。
	// like 里每一项是一个 SQL LIKE 模式（两头已经带 %），转义符是反斜杠：store 要写 `text LIKE ? ESCAPE '\'`，每一项都要匹配。
	// 记忆包已经把用户写的 % _ 和反斜杠转义好了，store 用了没有 ESCAPE 的 LIKE，用户写的 % 就又成了通配符。
	// 按时间最新的在前（同一毫秒按 seq），最多 limit 条。返回的事件 Seen 为真；撤回了的、没发出去的不返回（记忆包收到了也会再丢一遍）
	Find(ctx context.Context, conv string, like []string, limit int) ([]Event, error)
	// Conversations 列出这个角色的会话，各带现在最新的一条事件的 seq（Events 读得到的那些里最大的，不只是她看见的）。
	// 后台整理的启动扫描用（13.2.5）：没有游标的会话从这个 seq 立游标、不回填；有游标而游标比它小的，排一批。
	// 最近有动静的在前（启动扫描一次只排有限的几批，先排最近的）
	Conversations(ctx context.Context) ([]ConvHead, error)
}

// ConvHead 是一个会话、它现在最新的一条事件的 seq 和时间
type ConvHead struct {
	Conversation string
	LastSeq      int64
	// LastAt 是 LastSeq 那条事件的 created_at（毫秒）。跨库时 seq 不能比，按它排"最近有动静的在前"
	LastAt int64
}

// Reply 是后台模型的一次回复
type Reply struct {
	Text string
}

// Generator 调后台模型（宿主用 internal/llm 实现）
type Generator interface {
	Call(ctx context.Context, model, prompt, cacheKey string) (Reply, error)
}

// Options 是 Open 的可选部分，每个字段都有读取点：
//   - Writable：写入那一侧开不开，默认假。Bun 后台还在写 memory.db，库只能有一个写的人，所以假的时候库只读打开，写入口（apply 等）返回 ErrNotWritable（remember 工具是例外：它回一句只读的话，不当内部错误，W4），
//     不自己拷临时副本——要在副本上试写，调用方拷好副本、Migrate、再以 Writable 为真打开副本（测试、对拍、回放都这么做）。
//     真的要开，库必须已经跑过 Migrate（否则 Open 返回 ErrNeedsMigration），文件必须已经存在（不新建），Character 必须填且合法；
//     生产里由配置 memory.write.characters 和启动时的前提检查定（internal/memwrite，规格-记忆写入上线 W5），默认关，开之前要主人点头（规格-记忆-v2 13.2.1）
//   - Character：角色 id。拼进路径的 id 带路径分隔符、冒号或 ".." 的不开库（B17）；Writable 为真时必填。整理的缓存键、日志里的角色都用它
//   - Gen、Events：整理、工具要用，Gen 也供发送前守卫确认。为空时用到它们的写入口返回 ErrNoGenerator、ErrNoEvents
//   - Logf：日志只写计数、原因码和 ref，不写原文和模型输出，也不写会话 id（B16、边界 ㉑）；为空用 log.Printf
//   - Now：时钟，为空用 time.Now。写入的时间戳、证据的"哪一天"、工具文字里的"N 分钟前"都用它，测试和回放里固定住
//   - Prompts、Name、Model：整理（SummarizeBatch）要的三样。Prompts 是读 summarize-v2.md 的地方（出厂的在 Root/prompts/，用户改过的在 UserDir），
//     没配 Root 读不到提示词，整理返回错误；Name 是角色的显示名，填进提示词的 {{name}} 和聊天记录里她的那一行，空用 Character；
//     Model 返回后台模型 id，整理和守卫每次现取；返回空就是没选，整理跳过、游标不动、不算失败（B6），守卫能力不可用
//   - Reranker：可选的重排（规格-记忆-v2 第七节，2026-10-28 加）。nil 就是不重排，行为和以前一字不差；
//     给了就在合并排序、截出前 RerankK 条以后调一次，只改顺序，且只在明确回忆（ModeExplicit，memory_search）上调，
//     每轮自动想起和问以前都不调（W1a）。超时、出错、顺序不对退回原顺序，不让人这一轮失败
//   - RerankK：重排看多少条，为零用 RerankK（40）。只有配了 Reranker 才有意义
//   - SummarizeEnabled：后台整理开不开（规格-记忆写入上线 W5），默认假。remember 只看 Writable；SummarizeBatch 要 Writable 和它都为真，
//     否则返回 ErrSummarizeOff。排程也只对两者都为真的角色做整理
//   - WritePlaces：允许整理哪些场合（memory.write.places 的取值，AllWritePlaces）。nil 用 DefaultWritePlaces（只有 owner-private，主人自己的 QQ 私聊），
//     非 nil 的空切片什么场合都不整理；测试和影子整理工具要"什么场合都整理"时显式传 AllWritePlaces()。只管整理，remember 不看它（它自己有位置检查）
//   - QuarantineSince：止血用（毫秒时间，memory.write.quarantineSince）。非零时召回、memory_search、给整理模型看的已知事实和候选里，
//     不再出现创建时间不早于它的条目、以及线之后才被整理提升成有效的事实（线之前创建的旧候选也藏，看 fact_revisions 里 confirm 的时间），除了 confirmed_by = owner 的事实（主人亲口让她记的、亲口放行的）；片段只有整理会写，创建时间不早于它的不出现。
//     只是读侧的过滤，库一个字节不改；守卫的"禁看集合"不隔离（quarantine.go）。只读打开的角色也认它
//   - AutoConfirm、ConfirmOccasions、ConfirmKinds：待确认的事实攒够证据变有效（规格-记忆写入上线 W3b）。
//     AutoConfirm 为假（默认）只记账：条件满足时数 would_confirm，不改状态；为真才真提升（confirmed_by = ai，范围不动）。
//     ConfirmOccasions 是要几次算数的证据，零用 3，配成 1 也按 2 算（一次永远不够，新写的候选自带一次）。
//     ConfirmKinds 是哪几种事实可以自动提升：nil 用 [fact, preference]（promise、boundary 每轮都给、会塑造她的行为，要主人 remember 放行）；
//     非 nil 的空切片是什么都不自动提升；不认识的种类忽略。这三个字段的配置键是 `memory.write.autoConfirm`、`confirmOccasions`、`confirmKinds`，由 memwrite 的 `Settings.Apply` 设（装配在 app 的 `openMemories`）；`AutoConfirm` 只在这个角色的整理也开着时才为真（memwrite 的 `settings()`），只开 remember 的角色配了 autoConfirm 也不生效
//   - GuardThreshold：发送前句级余弦门槛；零用 0.70（ADR-202，待真实群负样本定）。只影响守卫
//   - GuardEmbedTimeout、GuardConfirmTimeout：守卫里嵌入、确认模型各自的时限，零或负用 2 秒、8 秒（ADR-186 第 4 条）。
//     负数不当真：嵌入时限一律到点会让守卫永远停在只比原文的退路上。整体仍受 10 秒管，设得更长也到 10 秒为止
type Options struct {
	Gen Generator
	// Background 在总结 / 守卫确认开始时冻结模型和生成器，防止混用两版模型能力和思考。
	Background       func() (string, Generator)
	Events           EventSource
	Shared           *Memory                                     // ADR-162 主人自身事实共用池，宿主统一打开和关闭。
	SourceEvents     func(character string) (EventSource, error) // 共用条目的原始来源，仅供已存引用核对。
	Writable         bool
	SummarizeEnabled bool
	WritePlaces      []string
	QuarantineSince  int64
	// SnapshotDir、SnapshotRel 是主人共用记忆库迁移前快照的目录和库相对数据目录的路径（规格-库版本与升级护栏 第 5 条）。
	// 空时放在库文件旁边的 upgrade-snapshots、用文件名；App 传 paths.UpgradeSnapshotDir() 和相对数据目录的路径。
	SnapshotDir string
	SnapshotRel string
	// OnChanged 在写入以后条目表换新时调：参数是删了、被取代、改了文字或范围的那些条目的 ref（新增的不报）。
	// 参考信息块引用记忆，宿主靠它把引用了这些条目的块删掉（规格-提示词前缀稳定 7.3）。在写锁里同步调，不许在里面再写记忆
	OnChanged func(refs []string)
	Character string
	Logf      func(format string, args ...any)
	Now       func() time.Time
	Prompts   prompts.Store
	Name      string
	// OwnerName 是这个角色卡上怎么称呼所有者（persona.OwnerCall）；空 = "用户"
	OwnerName           string
	Model               func() string
	Reranker            Reranker
	RerankK             int
	GuardThreshold      float64
	GuardEmbedTimeout   time.Duration
	GuardConfirmTimeout time.Duration
	AutoConfirm         bool
	ConfirmOccasions    int
	ConfirmKinds        []string
}

var (
	ErrNotWritable = errors.New("记忆库的写入没打开（Options.Writable 为假）")
	ErrNoGenerator = errors.New("没有配后台模型（Options.Gen 为空）")
	ErrNoEvents    = errors.New("没有配事件来源（Options.Events 为空）")
	// ErrSummarizeOff：后台整理没开（Options.SummarizeEnabled 为假）。直接调 SummarizeBatch 的工具遇到它是配置没给，不当成"没有可整理的"悄悄返回
	ErrSummarizeOff = errors.New("后台整理没开（Options.SummarizeEnabled 为假）")
)
