package types

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrUnsupported 表示启动时声明的能力不支持本次动作。
// 它是明确的失败，不是成功，也不是"结果未知"：没发出去，重试也没用（盘点 4.4 第 2 条：没有认领的动作不能当成功）
var ErrUnsupported = errors.New("适配器不支持这个动作")

// 每个 Part 对应一次平台动作；引用只能附在 Message 上。
const (
	PartMessage = "message"
	PartPoke    = "poke"
	PartForward = "forward"
	PartReact   = "react"
)

type Part struct {
	ID    string
	Conv  Conversation
	Kind  string
	Body  []BodySegment
	Ref   string // 戳一戳的目标或回应的源消息号
	Quote string
	Nodes []ForwardNode
	// Card 只对 PartForward 有效：合并转发卡片上的标题、预览、底部摘要和外显文字；nil 或某项为空用 QQ 默认
	Card    *ForwardCard
	EmojiID string
	// Unset 只对 PartReact 有效：取消自己之前贴过的这个表情（SnowLuma 的 set_msg_emoji_like 带 set:false）
	Unset bool
	// SourceSeq 仅供注册自动化的最新发送检查，不进入协议段。
	SourceSeq int64
}

// 节点身份由适配器固定为连接自己的号，外部数据只能提供显示名和内容。
type ForwardNode struct {
	Name string `json:"name"`
	// Self 为真时显示名用连接自己的 QQ 昵称，取不到才用 Name（Name 此时是退路）；头像和号永远是连接自己的
	Self bool          `json:"self,omitempty"`
	Body []BodySegment `json:"body"`
	// ReplyTo 是这个节点用 reply 段指向的同一转发里另一节点的下标加 1（规格-链接发送改版 A7）；0 表示没有，零值即旧行为
	ReplyTo int `json:"reply_to,omitempty"`
}

// ForwardCard 是合并转发卡片外面能看到的文字（SnowLuma 的 source、news、summary、prompt）。
// 文字已经按显示长度截好；空项由适配器省略，让 QQ 用默认值。
type ForwardCard struct {
	Source  string   `json:"source,omitempty"`
	News    []string `json:"news,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
}

// Empty 说这张卡片一项都没有。
func (c *ForwardCard) Empty() bool {
	return c == nil || (c.Source == "" && len(c.News) == 0 && c.Summary == "" && c.Prompt == "")
}

// Receipt 的 Status：只有这三种，没有别的（盘点 4.4 第 1 条：回执不能是 string | void，void 当成功就是静默成功）
const (
	ReceiptConfirmed = "confirmed" // 协议端确认发出去了
	ReceiptFailed    = "failed"    // 明确没发出去：没连上、没写出去的本地原因最多重试；协议端明确拒绝（IsRejection）原样重发只会再被拒，不重试
	ReceiptUnknown   = "unknown"   // 不知道发没发出去（超时、半路断线、没拿到回执、协议端回了个不能判定的码）：不重试，等回声认领；链接展开没拿到确认时等回声（AwaitsEcho，规格-链接发送改版 A1）
)

// Receipt 的 Via：怎么确认的
const (
	ViaReply = "reply" // 协议端的回包
	ViaEcho  = "echo"  // 自己的消息被报回来，中间件认领的
)

// Receipt 是发一段的结果
type Receipt struct {
	PartID     string // 说的是哪一段：Send 的回执照抄 Part.ID；Poke 没有段，空着
	Status     string // 上面 Receipt* 之一
	PlatformID string // 平台消息号，只有 confirmed 才有
	Via        string // ViaReply / ViaEcho；空着等于没说
	Err        string // failed、unknown 的原因，截短，不含令牌
	At         time.Time
}

// Validate 查回执是不是合法的三种之一。零值回执最要紧：适配器忘了给结果，不能被当成成功（规格 第十六节 B"回执缺失"）
func (r Receipt) Validate() error {
	switch r.Status {
	case ReceiptConfirmed, ReceiptFailed, ReceiptUnknown:
	case "":
		return errors.New("回执是零值：适配器没给结果，不能当成功")
	default:
		return fmt.Errorf("回执状态 %q 不是 confirmed / failed / unknown 三种之一", r.Status)
	}
	if r.Via != "" && r.Via != ViaReply && r.Via != ViaEcho {
		return fmt.Errorf("回执的 Via %q 不是 reply 或 echo", r.Via)
	}
	if r.Status != ReceiptConfirmed && r.PlatformID != "" {
		return fmt.Errorf("没确认的回执（%s）不该带平台消息号", r.Status)
	}
	return nil
}

// Caps 是适配器启动时声明一次的能力，之后不变。引用和转发分别声明群、私聊；回应仅有群能力。
type Caps struct {
	History        bool // 能按会话查询历史页；私聊是否能向前翻由协议端实际能力决定
	MediaGroup     MediaCaps
	MediaPrivate   MediaCaps
	Poke           bool // 能戳人：戳回去
	Echo           bool // 自己发的消息会被协议端报回来（SnowLuma 的 reportSelfMessage）；否则只能靠补录发现
	MaxText        int  // 一段文字最多多少字（rune），拆条用；0 表示不限
	QuoteGroup     bool
	QuotePrivate   bool
	ForwardGroup   bool
	ForwardPrivate bool
	ReactGroup     bool
}

// MediaCaps 分开声明普通消息和转发内的媒体能力。
type MediaCaps struct {
	Image, ImageBase64, Video, GIF         bool
	ForwardImage, ForwardVideo, ForwardGIF bool
}

func (c Caps) Media(scene string) MediaCaps {
	if scene == SceneGroup {
		return c.MediaGroup
	}
	if scene == ScenePrivate {
		return c.MediaPrivate
	}
	return MediaCaps{}
}

func (c Caps) Supports(p Part) bool {
	if p.Quote != "" && p.Kind != PartMessage {
		return false
	}
	group, private := p.Conv.Scene == SceneGroup, p.Conv.Scene == ScenePrivate
	media := c.Media(p.Conv.Scene)
	allowed := func(body []BodySegment, forward bool) bool {
		for _, s := range body {
			if s.Kind != BodyText && !s.Media.Available() {
				return false
			}
			switch s.Kind {
			case BodyText:
			case BodyImage:
				if (!forward && !media.Image) || (forward && !media.ForwardImage) {
					return false
				}
				if !media.ImageBase64 && !s.Media.FileAvailable() {
					return false
				}
			case BodyVideo:
				if (!forward && !media.Video) || (forward && !media.ForwardVideo) {
					return false
				}
				if !s.Media.FileAvailable() {
					return false
				}
			case BodyGIF:
				if (!forward && !media.GIF) || (forward && !media.ForwardGIF) {
					return false
				}
				if !media.ImageBase64 && !s.Media.FileAvailable() {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	switch p.Kind {
	case PartMessage:
		return (p.Quote == "" || (group && c.QuoteGroup) || (private && c.QuotePrivate)) && allowed(p.Body, false)
	case PartForward:
		if !((group && c.ForwardGroup) || (private && c.ForwardPrivate)) {
			return false
		}
		for _, n := range p.Nodes {
			if !allowed(n.Body, true) {
				return false
			}
		}
		return true
	case PartReact:
		return group && c.ReactGroup
	}
	return false
}

// Adapter 是一个平台的适配器（规格 第五节）：只翻译、只执行动作、只报告事实。
// 实现是否守规矩，由 internal/channel/contract 的契约测试查
type Adapter interface {
	// Name 是渠道名，也是 Conversation.Channel 和 audience.Audience.Channel
	Name() string
	// Caps 启动时读一次，之后不变
	Caps() Caps
	// Run 连上并收，一直阻塞到 ctx 取消。连不上、掉线都自己重连，用 sink.Status 报告，不当成错误返回；
	// 返回 error 只给"根本没法运行"（比如配置不对）。ctx 取消的正常返回是 nil。
	// Run 返回以后不再碰 sink：关掉的渠道还在回来的事件，不能再进库（清单④）。
	// Run 起来要让人知道：往 sink 报出第一个 Status，或者提供 Started() <-chan struct{}（接好 sink 后关闭，可选）；
	// 契约测试等这个信号才去查运行中的行为
	Run(ctx context.Context, sink Sink) error
	// Send 发一段，account 是用哪个号发。结果一律写在回执里（确认、明确失败、结果未知，连没连上、ctx 到期也是）：
	// 返回的 error 只给 ErrUnsupported，此时回执作废；出口只由它调用，插件拿不到适配器
	Send(ctx context.Context, account string, p Part) (Receipt, error)
	// Poke 在 c 里戳 target。只有 Caps.Poke 为真才会被调用，否则返回 ErrUnsupported；规矩同 Send
	Poke(ctx context.Context, account string, c Conversation, target string) (Receipt, error)
}

// MessageReader 是适配器的可选能力：按平台消息号向协议端取回已经收过的一条消息，翻成统一的 Inbound。
// 目前用来给过期的图片段换新地址（协议端取消息时会刷新媒体地址）。不属于 Adapter：没有这个能力的适配器照样是适配器。
// 取不到、对不上（会话、消息号和请求的不一样）、没连上都返回 error，不返回半条。
type MessageReader interface {
	Message(ctx context.Context, account string, conv Conversation, platformID string) (Inbound, error)
}

// Recaller 是适配器的可选能力：撤回自己号发的一条（SnowLuma 的 delete_msg，按平台消息号）。不属于 Adapter。
// 只给链接展开在图被吞时撤回只剩文字的那条用（规格-链接发送改版 A4）：装配处包一层门槛（自己号、本次刚认领、2 分钟内），
// 模型、工具、别的插件都拿不到。撤回失败（回包失败、超时、没连上）返回 error
type Recaller interface {
	Recall(ctx context.Context, account string, conv Conversation, platformID string) error
}

// HistoryPage 把消息、首页来源和协议完整性分开；QQ 群另以 Inbound.MessageSeq 核对目标区间。
type HistoryPage struct {
	Messages   []Inbound
	Latest     bool // 仅无锚点首页使用：来源保证取的是服务端最新历史。
	Continuous bool // 来源保证页内无遗漏且未回退缓存；只有协议明确提供时才能置真。
}

// HistoryReader 是可选的只读历史能力；anchor 非空取含锚点的更旧消息。
type HistoryReader interface {
	HistoryPage(ctx context.Context, account string, conv Conversation, anchor string, count int) (HistoryPage, error)
}

// AfterPage 是“某条消息之后的消息”：Messages 按时间从旧到新，含锚点自己（私聊没有锚点，取服务端最新一页）。
// ReachedLatest 为真表示翻到了尽头（最后一页没满）：这时页里没有的消息才算“没有”；为假（预算用完、翻页上限）只有“有”可信。
type AfterPage struct {
	Messages      []Inbound
	ReachedLatest bool
}

// AfterReader 是可选的只读能力：取锚点（一条已入库的消息）之后的消息，直到最新。
// 链接展开发图结果不明时靠它核实“这一条到底进没进群”（ADR-333）。
type AfterReader interface {
	HistoryAfter(ctx context.Context, account string, conv Conversation, anchor string) (AfterPage, error)
}

// ErrQueryBudget 表示这一轮的历史及媒体刷新共用查询预算已用完。
var ErrQueryBudget = errors.New("本轮查询预算已用完")

// Sink 是适配器把事实交给谁（中间件实现它）。返回的 error 只表示中间件没收下，适配器记日志，连接不因此关
type Sink interface {
	Inbound(ctx context.Context, in Inbound) error // 消息、自己的消息回报
	Notice(ctx context.Context, n Notice) error    // 戳一戳、撤回、表情回应、被踢、其他通知
	Status(ctx context.Context, s Status) error    // 连上、断开、重连、被踢、心跳
}

// 出站的来源（Outbox.Send 的 origin，规格 第十节 10.5）：调用方只填前两种，试运行由出口按会话当前的档位决定，不是调用方填的
const (
	OriginCharacter        = "character"          // 回合说的
	OriginAutomationPrefix = "automation:"        // 插件发的，后面接插件名，例如 automation:repeater
	OriginDryRun           = "automation:dry-run" // 试运行档：写库、不发
)

// Outbox 是唯一能让适配器发消息的入口（规格 第十节 10.5）：守卫、保险丝、拆条、写库、发送计划、回执都在它后面。
// 插件的构造函数只收它，不收适配器——架构测试查 channel 下面的包没有引 onebot
type Outbox interface {
	// Send 把 parts 排进这个会话的出站队列，计划落库以后返回计划号，不等发完。账号由出口按 characterID 查配置里的绑定。
	// 返回 error 表示这一批没有进队列（渠道断开、发送检查没过……），原因写在 error 里，调用方记日志
	Send(ctx context.Context, characterID, origin string, conv Conversation, parts []Part) (planID string, err error)
}
