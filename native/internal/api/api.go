// Package api 定义桌面端的读接口、可选写接口和展示 DTO（规格-界面接Go后台 4.1–4.6）。
// 发布的值不能再改，里面嵌套的切片和映射也一样。调用方会保留快照，
// 所以提供方发布后不能再改这些值。
package api

import (
	"context"
	"errors"
)

// ConversationKey 保留数据库里的身份。QQ 私聊同时带账号和对方的身份；
// 群聊的观察者另记，不放进这个键里。
// 本地日常和话题的 ID 仍用 console:<character>[:topic:<id>]。认不出的本地身份
// 必须报不支持，不能从参与者反推。
type ConversationKey struct {
	Platform     string
	Channel      string
	Scene        string
	Conversation string
}

type EventKey struct {
	Platform string
	EventID  string
}

// EventRef 把引用限定在一个会话里；单凭 seq 不能认定事件身份。
type EventRef struct {
	Conversation ConversationKey
	Event        EventKey
}

// Cursor 由调用方原样传回，不解析内容；它只属于对应的平台和会话。
// 空值取最新一页，拿别处的游标来用要报参数错误。
type Cursor string

type State string

const (
	Loading     State = "loading"
	Ready       State = "ready"
	Failed      State = "failed"
	Unsupported State = "unsupported"
)

// Result 区分成功但没数据、失败和能力不支持这三种情况。
// 刷新失败可以保留上一次的值，同时设 Stale=true 并填上 Err。
type Result[T any] struct {
	State State
	Value T
	Err   error
	Stale bool
}

type List[T any] struct {
	Items      []T
	NextCursor Cursor
	HasMore    bool
}

// Snapshot.Version 只在同一次订阅里递增，不能跨平台或跨订阅比较；
// 它不是数据库的 seq，也不代表跨文件事务。
type Snapshot[T any] struct {
	Version uint64
	Result[T]
}

// Subscription 先登记订阅再读初始快照，初始快照必须先于后续快照发布。
// 通知可以合并，已经提交的事实不能丢。上下文取消后停止后台任务。
// Close 可以重复调用，要等到不会再查询或发布才返回；
// 不能靠界面回调才能完成关闭。
type Subscription[T any] interface {
	Updates() <-chan Snapshot[T]
	Close()
}

type MessageView struct {
	Ref EventRef
	// Version 在同一个读取服务中递增；旧分页不能覆盖较新的行状态。
	Version     uint64
	Seq         int64 // ordering/high-water only, scoped to Ref.Event.Platform
	ActorKind   string
	ActorID     string
	ActorUID    string // platform account number of the speaker when known (owner included); for avatars only, never an authority signal
	DisplayName string // presentation only, never an authority signal
	Kind        string
	Process     []LocalProcessStep // 本地回复：这一句之前的思考和工具
	Service     string             // 不空时这行是通知条（进群、退群、禁言、改名片），内容就是这句话
	Origin      string
	CreatedAt   int64 // Unix milliseconds
	Text        string
	Segments    []DisplaySegment
	Unsent      bool
	Partial     bool
	Aborted     bool
	Recalled    bool
	ReplyTo     *EventRef
	Reply       Result[QuoteView]
	// Reactions 是贴在这条消息上的表情，按贴的时间排。后台只存了"贴"，没存"取消"（协议端的取消回应现在被当成别的通知丢掉），
	// 所以只增不减；角色自己的号贴的也没存。
	Reactions []ReactionView
	// Parts 是主人用分块输入（引用回复块、普通文字块）发的那条消息的结构化内容，按发送顺序，
	// 可还原每块的出处、片段、回复文字和附件；普通消息为空。Text 仍是各块文字用换行连起来的样子；
	// Reply 对应第一个引用块（兼容只看一个引用的界面）。
	Parts []MessagePart
	// Proactive 不为空表示这条她说的话是主动联系发来的（规格-主动联系 第十节第 1 条）：界面在气泡时间旁标"主动联系"，
	// 悬停看她为什么找你。只有本地私聊里她主动发的话有；来自事件 segments 里宿主写的标记。
	Proactive *ProactiveMark
}

// ProactiveMark 是一条主动联系消息的标记；Reason 是她开口的理由（线里第一次 thread_send 写的，限 60 字，只本机显示）
type ProactiveMark struct {
	Thread string
	Reason string
}

// ReactionView 是一次表情回应。Ref 是平台给的表情编号原样：小黄脸编号，或 Unicode 码位的十进制（界面按大小分，规格-原生界面第二版第三节）。
// 认人按 ActorID；DisplayName 只用来显示（主人固定叫"主人"，别人是群名片或昵称，查不到退回号码）。
type ReactionView struct {
	ID          string // 通知事件的 id，同一次回应只算一次
	Ref         string
	ActorKind   string
	ActorID     string
	DisplayName string
	At          int64 // Unix milliseconds
}

type QuoteView struct {
	Ref         EventRef
	ActorKind   string // owner：界面给主人的名字上强调色
	DisplayName string
	Text        string
	Recalled    bool
	Segments    []DisplaySegment // 原消息的展示段；引用与正文复用同一套名字和占位。
	// 主人框选引用（规格-引用）才有：Snippet 是被选中的那段，整条引用时等于原文；
	// SnippetStart、SnippetEnd 是它在 Text 里的字符（rune）偏移 [起, 止)，都为 0 表示没有定位（整条引用，或选中的文字没对上、退回了开头）。
	Snippet          string
	SnippetStart     int
	SnippetEnd       int
	SnippetWhole     bool // 引用的是整条
	SnippetFallback  bool // 选中的文字不在原文里，Snippet 是原文开头
	SnippetTruncated bool // Snippet 超长被截断
}

// DisplaySegment 由后台整理好，界面不解析 Bun JSON。
// 未知或不支持的种类保留文字占位。媒体凭据必须来自可见事件，
// 或该事件在同一会话内获准引用的事件。
type DisplaySegment struct {
	Kind       string
	Text       string
	Target     string
	Ref        string
	Media      *MediaRef
	Cover      *MediaRef // video：同一视频派生的封面，不接受任意媒体 hash
	DurationMS int64
	FetchState string            // video/file：下载状态（收图规格第十三节）
	FileKind   string            // file：已下载原件的实际种类；未知留空
	Download   *VideoDownloadRef // video/file：UI 聚合前保留的原始位置
	Width      int
	Height     int
	Animated   bool
	Sticker    bool
	Summary    string
	Name       string
	// Size 是 file/video 的字节数；0 表示不知道。
	Size       int64
	Card       *CardView
	Links      []string
	Forward    *ForwardBrief
	MediaState string // 合并转发里的媒体为什么没取（规格-链接和合并转发）
	// Issue 非空表示旧导入元数据对不上（版本、下标、身份），界面保留中立段并显示提示，不猜着配图。
	Issue string
}

type CardView struct {
	Title, Description, Source, URL string
}

type MediaRef struct {
	Source  EventRef
	Hash    string // 64 lowercase hexadecimal characters; never a file path
	Forward []string
}

// MediaTier 为 0 时取原始字节。静态缩略图只支持
// 128、256、384、512、768、1024 和 1536；其他值都报参数错误。
type MediaTier int

type MediaView struct {
	Bytes []byte
	MIME  string
	Size  int64
}

type FaceView struct {
	ID   string
	Name string
	Kind string
	Char string
}

type CharacterView struct {
	ID     string
	Name   string
	Color  string
	Model  string
	Status string
}

type ConversationView struct {
	Key       ConversationKey
	Title     string
	Observers []string
	CreatedAt int64 // 本地群的真实创建时间；空群没有伪造的 Last
	Last      *MessageView
	Unread    int  // 读到 read_at 之后的真实条数，不封顶（主人 2026-11-02）；界面照实显示
	HasUnread bool // false for local conversations without read tracking
	// WorkspaceCWD、WorkspaceRoot 只有绑定了工作目录的本地私聊才有；QQ、本地群永远是空的，不带路径
	WorkspaceCWD, WorkspaceRoot string
}

type Capability struct {
	Name   string
	State  State
	Reason string
}

type OverviewRequest struct {
	Cursor    Cursor
	Limit     int
	Character string // optional observation filter; not a visibility override
}

type Overview struct {
	Characters    List[Result[CharacterView]]
	Conversations List[ConversationView]
	Capabilities  []Capability
	Runtime       RuntimeView
}

// MessagePage 带上平台 seq 的上界，和最新一页在同一个读事务里取得。
// 历史按 (created_at,seq) 排序，正序展示；回放新增 seq 时，
// 也必须处理补录进来、时间戳比已有消息更早的事件。
type MessagePage struct {
	List[MessageView]
	HighWater int64
	// Before 是这一页最老一条再往前的那条消息，没有更早的消息时为空。界面只用它的发送人和时间，
	// 判断最老那一行是不是一组的第一条、是不是新的一天：不然前面的页加载进来以后，那一行的日期分隔、名字、上边距会消失，下面的行跟着跳。
	Before *MessageView
}

type ConversationRequest struct {
	Key       ConversationKey
	Character string
	Limit     int // default supplied by caller: 200; valid range 1..1000
}

// ConversationSnapshot 带上以后台为准的刷新页，以及已加载行里的变化
// （包括没有新增 seq 的撤回、观察记录和状态变化）。
// 调用方按 EventKey 合并，旧页不能把行的新状态改回去。
type ConversationSnapshot struct {
	Conversation ConversationView
	Page         MessagePage
	Changed      []MessageView
}

// Setting 只给出明确允许展示的安全值，不能暴露原始 YAML、
// 令牌、认证 URL、角色卡正文或授权文件。某一项有问题就单独返回失败的 Result，
// 不能编一个默认值当成实际生效的值。
type Setting struct {
	Key            string
	Value          any
	Source         string
	Configured     bool
	Effective      bool
	EffectiveValue any // 运行中的安全值；未启用时为 nil，不把配置值冒充生效值。
	Supported      bool
	// 下面四项是给人看的中文名、一行说明、单位和取值的叫法（desktopread 的 settingLabels 一张表出，界面不自己拼）。
	// Choices 把原始取值（"low"、"manual"）换成中文；没登记的取值原样显示。
	Name    string
	Help    string
	Unit    string
	Choices map[string]string
}

type Settings struct {
	Items List[Result[Setting]]
}

type RuntimeAlert struct {
	Key   string
	Title string
	Body  string
}

type RuntimeView struct {
	Mode       string
	Phase      string
	Account    string // masked display identity
	Connection string // transport connected does not imply QQ online or saving
	ReasonCode string
	Reason     string
	Alerts     List[RuntimeAlert]
}

// ErrNoAvatar 是没有这个头像：没登记过、上次没取到，或图已经不在媒体库里。正常情况，界面退回首字母，不重试。
var ErrNoAvatar = errors.New("没有这个头像")

// AvatarReader 是可选的头像入口，没放进 Reader：Reader 的实现很多（演示、测试、别的分支在加的方法），多一个方法大家都要跟着改。
// 窗口拿到 Reader 后问一下它是不是 AvatarReader，不是就全部用首字母色块。
// key 是 user:<QQ 号> 或 group:<群号>；没有这个头像回 ErrNoAvatar（State 为 Failed）。
type AvatarReader interface {
	ReadAvatar(ctx context.Context, key string) (Result[MediaView], error)
}

// VideoReader 只为本机桌面准备已下载视频的带后缀副本；不下载、不启动播放器。
type VideoReader interface {
	PrepareVideo(context.Context, MediaRef) (string, error)
}

// Reader 不提供写入、发送、审批、SQL 或应用句柄。调用都在后台任务里执行，
// 上下文必须能取消，默认查询时限 5 秒。
// ReadMessages 接受 1..1000 条事件；后台校验游标和引用的范围，
// 使用和概览、媒体、未读相同的观察过滤条件。
type Reader interface {
	WatchOverview(context.Context, OverviewRequest) (Subscription[Overview], error)
	ReadMessages(context.Context, ConversationRequest, Cursor) (Result[MessagePage], error)
	WatchConversation(context.Context, ConversationRequest) (Subscription[ConversationSnapshot], error)
	ReadMedia(context.Context, MediaRef, MediaTier) (Result[MediaView], error)
	ReadFaceIndex(context.Context) (Result[List[FaceView]], error)
	ReadFacePNG(context.Context, string) (Result[MediaView], error)
	ReadSettings(context.Context) (Result[Settings], error)
	WatchRuntime(context.Context) (Subscription[RuntimeView], error)
}
