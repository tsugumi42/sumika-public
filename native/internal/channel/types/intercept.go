package types

import (
	"context"
	"time"
)

// 拦截插件用的类型（规格-渠道中间件 第七节，需求-拦截类插件接口）。
// 拦截链排在观察之后、闸门之前：每一批进来，按优先级（小的先）问各个拦截插件"这一批你接不接"；有一个说"接了"，链就停，
// 这一批不交给内核（闸门照常收到，只计数、更新退避）。复读、戳回去、以后的 #sl 命令都是拦截插件。
// 插件只引这个包，不引中间件：它能做的事只有读 Batch、返回 Verdict、通过构造时拿到的 Outbox 发东西——拿不到适配器（规格 第十节 10.5"挡法"）

// Occasion 是场合（渠道 × 形式，ADR-140）的一种，插件用它声明自己在哪些场合跑。Channel 为空表示任何渠道
// （复读跨平台，是"任何渠道的群"；戳回去是 QQ 独有，Channel 写 qq）。场合值不写死平台（ADR-144）
type Occasion struct {
	Channel string
	Scene   string // ScenePrivate / SceneGroup
}

// Matches 说一个会话是不是这个场合。只看渠道和形式，不看会话字符串（清单⑤）
func (o Occasion) Matches(c Conversation) bool {
	return (o.Channel == "" || o.Channel == c.Channel) && o.Scene == c.Scene
}

// TriggerMessage 是"一条收到的消息"这种批次；通知这种批次的触发用 Notice 的种类（NoticePoke……）。
// 插件在 Interceptor.Triggers 里写它关心的批次，不关心的批次链不会进它，也不记日志（不然每条消息、每个通知都记一行"不关我事"）
const TriggerMessage = "message"

// Msg 是拦截插件看到的一条消息。Who 是发言人，同一个人 Who 相同：events 的 actor_id（主人是 "owner"，群友是 "渠道:号"，角色是角色 id）。
// 数「几个不同的人」靠它，不靠昵称和名片（会变，平台的缓存）
type Msg struct {
	Seq    int64
	Time   time.Time
	Who    string
	Self   bool   // 是这个角色自己说的（她的号发的，或者她经出口发的）
	Origin string // events.origin：owner / peer / human / external / character / automation:<插件名>；复读靠它在重启后找到自己上次什么时候跟过
	Segs   []Seg  // 平台来的消息有；她经出口发的只有文字，没有段（段列表是准的，纯文本只是派生，所以她自己的消息不参与比较）
	// Name 是发言当时的名字（群名片 > 昵称 > 号），给模型看的那一行用；平台来的消息才有，名字是平台的缓存，不当身份。
	// Addressed 是这一条单独叫到了她（@、引用她的话或主人填写的别名），闸门拼批时靠它数被叫到的消息；名单者不算，OwnerAsked 仍按消息身份认。
	Name      string
	Addressed bool
}

// Batch 是交给拦截插件的一批。一批要么是消息（Trigger 为 TriggerMessage，Msgs 有新到的消息），要么是一条通知（Notice 有值）
type Batch struct {
	Conv      Conversation
	Character string    // 是哪个角色在看（收到它的号绑的）。同一条消息两个号都看到时，每个角色各过一遍链
	Account   string    // 收到它的号
	Now       time.Time // 链开始处理这一批的时刻（链的钟）；插件算冷却、限流用它，不自己读钟，测试才能拨时间
	Trigger   string
	Msgs      []Msg   // 这一批新到的消息，旧的在前；通知批为空
	Notice    *Notice // 通知批才有
	// Mine 是这个角色在这个渠道绑的全部号（包括收到它的这个），通知批才有。同一条通知，这个角色的几个号各收到一份，
	// 但入库一站按角色去重、只把先到的那份交给链——所以"这条通知是不是冲着她"不能看"收到它的号"，要看她所有的号
	Mine []string
	// Addressed 是叫到她了：@ 她的号、引用她的话或主人填写的别名。@ 全体不算；中间件在拦截链前算好，名单者不能把它置真，
	// 插件只能读，改不了（AstrBot 的 is_wake 谁都能改，我们不这样，规格 第四节）。通知批恒为假：戳她不等于叫她
	Addressed bool
	// GateDecisionIDs 是闸门放行判断的 trace.db 记录 ID；合批和已送达的插话合并保留，回合定了后逐条写回结局。
	GateDecisionIDs []int64
	// Recent 取这一批之前的最近 n 条消息（旧的在前，不含这一批的）：插件不用自己记状态，重启后也能从库里重建（需求 第一节）。
	// 撤回了的不在里面；通知不在里面；她自己的消息在（Self 为真），插件自己决定算不算
	Recent func(n int) ([]Msg, error)
}

// Verdict 是插件对一批的回答：接了（带理由，写日志）或没接（带原因，写日志）。
// 只有"接了"才会停下链；出错、超时、panic 都当没接（拦截插件坏了不能让她哑巴，规格 第七节）
type Verdict struct {
	Taken  bool
	Reason string // 不含消息原文（日志只记长度、会话号和结果，规格 第十七节）
}

// Took 造一个"接了"
func Took(reason string) Verdict { return Verdict{Taken: true, Reason: reason} }

// Passed 造一个"没接"
func Passed(reason string) Verdict { return Verdict{Reason: reason} }

// Interceptor 是一个拦截插件。链按 Priority 从小到大问（初值：戳回去 10，复读 20；#sl 0），同一个数字按注册的先后、一个接一个，不并发——
// 先到先赢才有意义（规格 第七节）
type Interceptor struct {
	Name      string // 日志里的名字，也是 automation:<Name> 里的名字，也是开关的键
	Priority  int
	Occasions []Occasion // 在哪些场合跑；不在里面的会话，链不问它
	// DefaultOn 是没人设过开关时默认开着的场合（要在 Occasions 里）；别的场合默认关。群里的插件默认全关（规格-群聊插件 第一节），
	// 所以多数插件不写；戳回去在她本来就接的私聊里默认开（ADR-189）。设过的开关永远压过默认：主人可以把某个私聊的戳回去单独关掉
	DefaultOn []Occasion
	Triggers  []string // 关心哪些批次：TriggerMessage 和/或通知的种类
	// Intercept 判断这一批接不接。ctx 到期（超时 3 秒、渠道关了）就该尽快返回；到期以后它再返回什么都被丢掉，
	// 也不该再通过 Outbox 发东西（Outbox.Send 看到 ctx 到期会拒绝）
	Intercept func(ctx context.Context, b Batch) Verdict
}
