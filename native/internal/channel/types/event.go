package types

import "time"

// 适配器交给中间件的事实有三类（Sink 的三个方法）：入站的消息、通知、连接状态。
// 适配器只报"协议端说了什么"，不判断是谁（主人、角色、群友是中间件按账号配置认的）、不判闸门、不写库（规格 第五节"适配器的边界"）

// User 是一个发言人在平台上的样子。名片和昵称是平台的缓存，会变，只当线索；身份认 ID（规格-QQ渠道 第四节）
// json 标签是它存进 events.segments_json 的样子（发言当时的名字，以后重建给模型看的那一行用它，不会因为后来改了名片而变）
type User struct {
	ID   string `json:"id"`             // 平台上的号
	Nick string `json:"nick,omitempty"` // 昵称；平台没给就空
	Card string `json:"card,omitempty"` // 群名片；私聊或没设就空
}

// Inbound 的 Kind
const (
	InboundMessage = "message" // 别人（包括主人）发来的
	InboundSelf    = "self"    // 她自己的号发出去的消息被协议端报回来：Sumika 发的回声，或者真人在别的设备上发的（谁发的由中间件认）
)

// Inbound 是收到的一条消息，或自己的号的消息回报。补录的消息也走这个（标签由中间件打，适配器不知道自己交的是不是补录）
type Inbound struct {
	Kind    string
	Account string       // 这条是哪个号收到的（配置里的账号）：同一个群被两个号看到时各交一份，中间件去重后各记一条"看到了"
	Conv    Conversation // 会话；Kind 为 self 的私聊，ID 是收信的对方
	// PlatformID 是平台消息号，原始数字串。空串或 "0" 表示协议端没给：这样的消息不去重，按单条入库（规格 第六节第 3 步）
	PlatformID string
	MessageSeq int64     // 群历史的协议序号；0 表示无有效证据，与消息 ID 和本地 seq 无关。
	From       User      // 谁发的；Kind 为 self 时是她自己的号
	Time       time.Time // 平台时间，精度是整秒；同一秒的多条靠平台消息号定先后，到达的先后是中间件的 seq，两者不同
	Segs       []Seg
}

// Notice 的 Kind。通知不是发言：不计闸门、不计提及（规格 第三节"戳一戳"一行）
const (
	NoticePoke     = "poke"     // 戳一戳。Target 是她的号才是"戳她"
	NoticeRecall   = "recall"   // 撤回
	NoticeReaction = "reaction" // 表情回应
	NoticeKicked   = "kicked"   // 被踢下线（同时发一条 StatusKicked）
	NoticeOther    = "other"    // 认不出的通知：留着 Raw，不丢
)

// Notice 是收到的一条通知
type Notice struct {
	Kind    string
	Account string       // 哪个号收到的
	Conv    Conversation // 发生在哪个会话；NoticeKicked 是整个账号的事，没有会话，这里是零值
	Time    time.Time
	Actor   string // 谁做的：戳的人、撤回的人、贴表情的人
	Target  string // NoticePoke：被戳的人
	MsgID   string // NoticeRecall、NoticeReaction：作用在哪条平台消息上
	Ref     string // NoticeReaction：表情编号
	Raw     string // NoticeOther：截短到 RawMax 个字的原始数据
}

// Status 的 Kind。元事件（心跳、生命周期）归这里，不进 events：心跳整天周期性到达，存进去只是噪声（规格 第六节第 1 步）
const (
	StatusConnected    = "connected"    // 连上了（重连成功也是这个）；通道门据此清零（规格 第八节）
	StatusDisconnected = "disconnected" // 断了，或半开超过 3 个心跳间隔判为掉线（规格 第十三节）
	StatusReconnecting = "reconnecting" // 正在重连
	StatusHeartbeat    = "heartbeat"
	StatusTyping       = "typing"   // 对方正在输入（瞬时的，只给界面显示几秒，不进库）；Conv 是哪个会话
	StatusKicked       = "kicked"   // 被踢下线（风控），和普通掉线分开提醒（规格-QQ渠道 第六节）
	StatusMismatch     = "mismatch" // 连上后核对的号和配置对不上：适配器已断开，中间件提醒一次
)

// Status 是一个账号的连接状态变化
type Status struct {
	Kind     string
	Account  string
	Time     time.Time
	Interval time.Duration // StatusHeartbeat：协议端给的心跳间隔，没给是 0（协议端是否带这个字段没核实，规格 第二十节第 5 条）
	Reason   string        // 断开、被踢、对不上的原因；不含令牌
	Conv     Conversation  // StatusTyping：哪个会话
}
