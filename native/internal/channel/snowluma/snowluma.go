// Package snowluma 是 SnowLuma（OneBot）适配器：翻译已经连上的连接上的帧，也在同一条正向 WS 上发 action。
package snowluma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"sumika-native/internal/channel/types"
)

// ReadFrame must return when ctx is cancelled. It reads only; it must not send actions.
type ReadFrame func(context.Context) ([]byte, error)

type Adapter struct {
	// 第一块：调用方给一个已建立连接的读帧函数（测试、别处接好的连接用）
	read ReadFrame
	// 第二块：自己拨 OneBot 正向 WS。和 read 二选一，NewWS 填这一组
	cfg          Config
	clock        types.Clock
	alerts       Alerter
	dial         dialFunc
	jitter       func(time.Duration) time.Duration
	backoffStart time.Duration
	backoffMax   time.Duration
	alertAfter   time.Duration

	started chan struct{}
	once    sync.Once

	// 发送：当前这条连接，和按 echo 等回包的调用。mu 保护 live、pend、seq
	mu   sync.Mutex
	live *live
	pend map[string]chan callResult
	seq  uint64

	// 登录号核验。mu 一并保护。session 每次开、关连接加一，旧的核验协程写不回去。
	session      uint64
	phase        int
	kick         chan struct{}
	saw          chan struct{} // 第一帧到了就关掉；核验在这之前不发查询
	loginID      string
	loginName    string // get_login_info 回包里的昵称，合并转发节点用它当显示名；取不到是空。重连不清（号核对过才写，昵称很少变），下次核验成功再覆盖
	lastSelf     string
	selfLogged   bool
	loginRaised  bool
	statusRaised bool
	recvRaised   bool // 运行中心跳报"收不到"的提醒开着没有（health.go）；跨重连保留，同一状态不重复提醒
	mismatchWait bool

	// 查询（query.go）：同一时刻只放一个在途，两次之间隔 queryGap。qsem 懒建，lastQuery 由持有 qsem 的一方读写
	qonce         sync.Once
	qsem          chan struct{}
	lastQuery     time.Time
	queryBudget   int // mu 保护；queryBudgetOn 为真时，历史和 get_msg 共用这一轮的次数
	queryBudgetOn bool
}

var _ types.Adapter = (*Adapter)(nil)

func New(read ReadFrame) *Adapter { return &Adapter{read: read, started: make(chan struct{})} }
func (*Adapter) Name() string     { return "qq" }
func (a *Adapter) Caps() types.Caps {
	// 引用、转发、群回应都按 2026-10-30 录的真帧（规格-QQ发送上线 3.4）；私聊回应协议端不支持。
	// 媒体（ADR-310）默认声明能发：不再靠真机验过才开的静态开关；某种媒体在某个场合明确失败后，
	// 由宿主记下这种组合（internal/app/media_block.go）、之后退成文字，不在这里挡。
	// 图片走 base64（call.go encodeBody，有端到端测试）；视频走本机 file URI。
	media := types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true, ForwardImage: true, ForwardVideo: true, ForwardGIF: true}
	return types.Caps{History: true, Poke: true, QuoteGroup: true, QuotePrivate: true, ForwardGroup: true, ForwardPrivate: true, ReactGroup: true, MediaGroup: media, MediaPrivate: media}
}
func (a *Adapter) Started() <-chan struct{} { return a.started }

func (a *Adapter) Run(ctx context.Context, sink types.Sink) error {
	if a.read == nil && a.dial == nil {
		return fmt.Errorf("缺少帧读取函数")
	}
	a.once.Do(func() { close(a.started) })
	if a.dial != nil {
		return a.runWS(ctx, sink)
	}
	return a.runReader(ctx, sink)
}

// runReader 是第一块的路径：读帧函数自己管连接，读到错就等 ctx；重连由调用方给的那个函数负责。
func (a *Adapter) runReader(ctx context.Context, sink types.Sink) error {
	for ctx.Err() == nil {
		frame, err := a.read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				a.warnf("读取帧失败: %s", a.safeError(err))
				<-ctx.Done()
			}
			break
		}
		if ctx.Err() != nil {
			break
		}
		a.dispatch(ctx, sink, frame)
	}
	return nil
}

type object map[string]json.RawMessage

func str(o object, key string) string {
	v := o[key]
	if len(v) == 0 || string(v) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}

// qqID 把 str 读出来的号码过一遍：0 一类的写法（0、-0、0.0、"00"、" 0"）和纯空白当成没有，返回空；别的原样返回，
// 不去空格、不去前导零（"0200"、" 200" 不能被认成 200，守卫的冒充测试钉着）。协议端换不出号码时填 0
// （SnowLuma、NapCat 都这样），0 不是谁的号。消息号、群号、at 段不走这里
func qqID(s string) string {
	if t := strings.TrimSpace(s); t == "" || zeroID.MatchString(t) {
		return ""
	}
	return s
}

var zeroID = regexp.MustCompile(`^[+-]?0+(?:\.0+)?(?:[eE][+-]?[0-9]+)?$`)

func obj(o object, key string) object { var x object; _ = json.Unmarshal(o[key], &x); return x }

// 商城表情的图片地址和文件名（照 Bun 的 daemon/qq/segments.ts）
var (
	marketURL  = regexp.MustCompile(`gxh\.vip\.qq\.com/club/item/parcel`)
	marketFile = regexp.MustCompile(`(?i)^[0-9a-f]{2}-[0-9a-f]{32}\.`)
)

// sticker 判断一个 image 段是不是表情包。照 Bun（那边照 MaiBot 统一 QQ 连接器的 message_codec.py）：
// sub_type 有值且不是 0、4、9（普通图片的几种），或者 summary 有字且不是"[图片]"（表情包是"[动画表情]"这类）；
// 商城表情按地址或文件名认。原始帧里没有 sticker、market 这种字段，那是 Bun 整理后自己加的
func sticker(d object) bool {
	if strings.TrimSpace(str(d, "sub_type")) != "" {
		if n, ok := number(d, "sub_type"); !ok || (n != 0 && n != 4 && n != 9) {
			return true
		}
	}
	if sum := strings.TrimSpace(str(d, "summary")); sum != "" && sum != "[图片]" {
		return true
	}
	return marketURL.MatchString(str(d, "url")) || marketFile.MatchString(str(d, "file"))
}

// number 是一个字段的数值，数字和数字字符串都认（照 Bun 的 Number()："4"、4、4.0 都是 4）；取不到时 ok 是 false
func number(o object, key string) (float64, bool) {
	s := strings.TrimSpace(str(o, key))
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > types.RawMax {
		return string(r[:types.RawMax])
	}
	return s
}
func segments(raw json.RawMessage) ([]types.Seg, error) {
	var input []json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	out := make([]types.Seg, 0, len(input))
	for _, el := range input {
		var x struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		// 一段本身不是对象（混进来的字符串、数字）：只这一段算认不出的，别的段照翻（原来整条消息丢掉）
		if json.Unmarshal(el, &x) != nil {
			out = append(out, types.Seg{Kind: types.SegOther, Raw: clip(string(el))})
			continue
		}
		// data 不是对象时当成空的（照 Bun 的 s.data ?? {}，取不到的字段都是空）
		var d object
		_ = json.Unmarshal(x.Data, &d)
		s := types.Seg{}
		switch x.Type {
		case "text":
			s.Kind = types.SegText
			s.Text = str(d, "text")
		case "at":
			s.Kind = types.SegAt
			s.Target = str(d, "qq")
		case "reply":
			s.Kind = types.SegReply
			s.Target = str(d, "id")
			s.Raw = replyPreview(d["message"])
		case "image":
			s.Kind = types.SegImage
			if sticker(d) {
				s.Kind = types.SegEmoji
			}
			s.URL, s.File = mediaURL(str(d, "url")), mediaFile(str(d, "file"))
		case "face":
			s.Kind = types.SegFace
			// 超级表情照 Bun（daemon/qq/segments.ts）：带 resultId，或者 sub_type 是 3
			if n, ok := number(d, "sub_type"); len(d["resultId"]) > 0 || len(d["result_id"]) > 0 || (ok && n == 3) {
				s.Kind = types.SegSuperface
			}
			s.Ref = str(d, "id")
		case "mface":
			s.Kind = types.SegEmoji
			s.Ref = str(d, "emojiId")
			if s.Ref == "" {
				s.Ref = str(d, "emoji_id")
			}
		case "record":
			s.Kind = types.SegVoice
			s.URL, s.File = mediaURL(str(d, "url")), mediaFile(str(d, "file"))
		case "video":
			s.Kind = types.SegVideo
			s.URL, s.File = mediaURL(onlyStr(d, "url")), mediaFile(onlyStr(d, "file"))
			s.FileID = fileID(onlyStr(d, "file_id"))
			if s.FileID == "" {
				s.FileID = fileID(onlyStr(d, "id"))
			}
			if s.Size = fileSize(d, "file_size"); s.Size == 0 {
				s.Size = fileSize(d, "size")
			}
		case "file":
			s.Kind = types.SegFile
			// 文件名在 name 和 file 里各给一份（都是 fileName），size 和 file_size、id 和 file_id 也是；哪个能用取哪个
			s.File = fileName(onlyStr(d, "name"))
			if s.File == "" {
				s.File = fileName(onlyStr(d, "file"))
			}
			s.URL = mediaURL(str(d, "url"))
			s.FileHash = fileID(onlyStr(d, "file_hash"))
			s.FileID = fileID(onlyStr(d, "file_id"))
			if s.FileID == "" {
				s.FileID = fileID(onlyStr(d, "id"))
			}
			if s.Size = fileSize(d, "file_size"); s.Size == 0 {
				s.Size = fileSize(d, "size")
			}
		case "forward":
			s.Kind = types.SegForward
			s.Ref = fileID(onlyStr(d, "id"))
		case "json", "xml":
			// 类型、预览与完整载荷分开；只解 data.data 的一层字符串，不展开卡片内部的 data。
			s.Kind = types.SegCard
			s.CardType = x.Type
			raw := str(d, "data")
			s.Raw = clip(raw)
			if x.Type == "json" {
				s.CardJSON, s.CardIssue = cardJSON(raw)
				if s.CardIssue == "" {
					types.FillCardDisplay(&s)
				}
			}
		default:
			s.Kind = types.SegOther
			b, _ := json.Marshal(x)
			s.Raw = clip(string(b))
		}
		out = append(out, s)
	}
	return out, nil
}

// mediaURL 只留 http(s) 的下载地址；别的协议（file:、本机路径、base64://）和超长的都不存，以后补下载时不会被引去读本地文件
func mediaURL(u string) string {
	l := strings.ToLower(u)
	if len(u) > types.MediaRefMax || !(strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")) || strings.ContainsFunc(u, unicode.IsControl) {
		return ""
	}
	return u
}

// mediaFile 只留像 "<哈希>.jpg" 这样的单个名字：带路径分隔符、控制字符、".."、超长的都不存
func mediaFile(f string) string {
	if len(f) > types.MediaRefMax || f == "." || f == ".." || strings.ContainsAny(f, `/\:`) || strings.ContainsFunc(f, unicode.IsControl) {
		return ""
	}
	return f
}

// fileName 是 file 段的文件名：只显示、不当路径，所以比 mediaFile 宽一点，允许英文冒号（"录音 12:30.m4a" 是合法的名字）；
// 路径分隔符、".."、控制字符、超长照旧不存
func fileName(f string) string {
	if len(f) > types.MediaRefMax || f == "." || f == ".." || strings.ContainsAny(f, `/\`) || strings.ContainsFunc(f, unicode.IsControl) {
		return ""
	}
	return f
}

// onlyStr 只认 JSON 字符串；数字、数组、对象、null 都当没有（str 会把它们的原文当字符串，拿去当文件名就是乱码）
func onlyStr(o object, key string) string {
	var s string
	if json.Unmarshal(o[key], &s) != nil {
		return ""
	}
	return s
}

// fileID 是 file 段的文件标识：不透明字符串（群文件的标识可以以 "/" 开头），只限长度和控制字符；不是路径，调用方不能拿它拼路径
func fileID(f string) string {
	if len(f) > types.MediaRefMax || strings.ContainsFunc(f, unicode.IsControl) {
		return ""
	}
	return f
}

// fileSize 是一个字段里的字节数：数字和数字字符串都认（SnowLuma 给数字，别的实现常给字符串）；
// 负数、小数、超过 2^53（再大 float64 也读不准）、NaN、无穷、不是数字的都当不知道，返回 0
func fileSize(o object, key string) int64 {
	f, ok := number(o, key)
	if !ok || f < 0 || f > 1<<53 || f != math.Trunc(f) {
		return 0
	}
	return int64(f)
}

// cardJSON 只接限长内的完整 JSON 对象；坏卡只留预览，不影响下一段（规格-链接解析 3.1）。
// 不按 app 筛：先存全收到的原文，避免以后无法补解析；是否识别交给消费者。
func cardJSON(raw string) (string, string) {
	if len(raw) > types.CardJSONMax {
		return "", "card_too_large"
	}
	var card map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &card) != nil || card == nil {
		return "", "card_malformed"
	}
	return raw, ""
}

// heartbeatInterval 从一帧里取出心跳间隔；不是心跳、或间隔不是正数时返回 0（还没收到过心跳，不计时）。
// 心跳已经在 translate 里翻成 Status，这里只给连接的看门狗用（不经过接收端）。
func heartbeatInterval(frame []byte) time.Duration {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return 0
	}
	if str(o, "post_type") != "meta_event" || str(o, "meta_event_type") != "heartbeat" {
		return 0
	}
	return intervalOf(o)
}

// 心跳间隔只认这个范围：太短看门狗会一直误判掉线，太长乘 3 会溢出成负数、计时器立刻到点（SnowLuma 是 30 秒）
const (
	minHeartbeat = time.Second
	maxHeartbeat = time.Hour
)

// intervalOf 是心跳帧里的间隔（毫秒）。没给、不是数、不在 minHeartbeat 到 maxHeartbeat 之间时是 0：
// 看门狗不拿它计时（还没有间隔就不计，有了就接着用上一个），心跳状态里也报 0（"没给"）
func intervalOf(o object) time.Duration {
	ms, ok := number(o, "interval")
	if !ok || ms < float64(minHeartbeat/time.Millisecond) || ms > float64(maxHeartbeat/time.Millisecond) {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// postType 是帧的 post_type。SnowLuma 偶尔推来没有 post_type、但带着消息的事件，照 Bun（daemon/onebot/client.ts，
// 那边照 MaiBot 的 runtime/router.py）当成消息
func postType(o object) string {
	p := str(o, "post_type")
	if p == "" && str(o, "message_type") != "" && len(o["message"]) > 0 && string(o["message"]) != "null" {
		return "message"
	}
	return p
}

// messageConv 是一条消息落在哪个会话。群聊还是私聊看 message_type（照 Bun）：临时会话的私聊帧可能带着来源群号，
// 按 group_id 判会把私聊并进群里；没有 message_type 才看 group_id。私聊是她和对方的会话，她发的按 target_id、peer_id、
// user_id 的顺序找对方，只认不是她自己的号、也不认 0 一类的写法（找不到就是空，中间件不收，不落进"她和自己"
// 或"她和 0"的会话）
func messageConv(o object, account, uid string, self bool) types.Conversation {
	group := str(o, "group_id")
	isGroup := group != "" && group != "0"
	switch str(o, "message_type") {
	case "group":
		isGroup = true
	case "private":
		isGroup = false
	}
	if isGroup {
		return types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: group}
	}
	peer := uid
	if self {
		peer = ""
		for _, k := range []string{"target_id", "peer_id", "user_id"} {
			if v := qqID(str(o, k)); v != "" && v != account {
				peer = v
				break
			}
		}
	}
	return types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: account, ID: peer}
}

// maxEventSeconds 以上的 time 不像秒数（多半是毫秒），按缺了算
const maxEventSeconds = 1e11

// eventTime 是帧里的 time（秒）。缺了、不是正数、不像秒数时返回零值，dispatch 交出去之前用收到的时间补
// （照 Bun：Number(e.time) > 0 才用帧里的，不然用当前时间；原来整帧丢掉）。整数、小数、数字字符串都认
func eventTime(o object) time.Time {
	var n json.Number
	if json.Unmarshal(o["time"], &n) != nil {
		return time.Time{}
	}
	f, err := n.Float64()
	if err != nil || !(f > 0) || f >= maxEventSeconds {
		return time.Time{}
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
}

// messageSegs 是消息的段。message 不是段数组（OneBot 的字符串格式、null、没有，或者单个对象、数字）时照 Bun 降成一段文字：
// 先取 raw_message，没有再取字符串的 message；都没有就是空的。原来整条消息丢掉
func messageSegs(o object) ([]types.Seg, error) {
	raw := bytes.TrimSpace(o["message"])
	if len(raw) == 0 || raw[0] != '[' {
		var s string
		_ = json.Unmarshal(raw, &s)
		text := str(o, "raw_message")
		if text == "" {
			text = s
		}
		if text == "" {
			return []types.Seg{}, nil
		}
		return []types.Seg{{Kind: types.SegText, Text: text}}, nil
	}
	return segments(raw)
}

// reaction 把贴表情的通知填成 NoticeReaction（规格第十三节"表情回应"）；填不了返回 false，照别的通知留原文。
// 贴的人是 user_id（真实帧里和 operator_id 一样），一帧一个表情。取消贴（sub_type remove、is_add false）和一帧几个表情的，
// types.Notice 里没有地方放，不填
func reaction(o object, n *types.Notice) bool {
	var likes []object
	if json.Unmarshal(o["likes"], &likes) != nil || len(likes) != 1 {
		return false
	}
	ref := str(likes[0], "emoji_id")
	if ref == "" || str(o, "sub_type") == "remove" || str(o, "is_add") == "false" {
		return false
	}
	n.Kind = types.NoticeReaction
	n.MsgID = str(o, "message_id")
	n.Ref = ref
	return true
}

// noticePeer 是私聊通知的对方：一般是 user_id；user_id 没有、是 0 或是她自己的号（她做的事的回声，比如她戳出去的戳一戳）
// 时看 target_id、peer_id，只认不是她自己的号（照 Bun 的私聊戳一戳）、不是 0 一类写法的号（qqID）。找不到就是空，
// 中间件不收，不落进"她和自己"的会话
func noticePeer(o object, account, uid string) string {
	for _, v := range []string{uid, str(o, "target_id"), str(o, "peer_id")} {
		if v = qqID(v); v != "" && v != account {
			return v
		}
	}
	return ""
}

func translate(frame []byte) (*types.Inbound, *types.Notice, *types.Status, error) {
	var o object
	if err := json.Unmarshal(frame, &o); err != nil || o == nil {
		return nil, nil, nil, fmt.Errorf("无效 JSON 帧")
	}
	account := str(o, "self_id")
	uid := str(o, "user_id")
	group := str(o, "group_id")
	at := eventTime(o)
	switch post := postType(o); post {
	case "message", "message_sent":
		sender := obj(o, "sender")
		if uid == "" {
			uid = str(sender, "user_id")
		}
		// 先回退再归一：外层是 0 不去读 sender。协议端的这两个号取自同一处（SnowLuma、NapCat 都是），外层 0、
		// sender 却有号的帧哪家都对不上，不认；号是空的消息中间件不收
		uid = qqID(uid)
		// 她自己发的：message_sent 一律算（规格第十三节"自己发的消息"），发言人是她的号也算（照 Bun）
		self := post == "message_sent" || (uid != "" && uid == account)
		kind, from := types.InboundMessage, uid
		if self {
			kind, from = types.InboundSelf, account
		}
		segs, err := messageSegs(o)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("无效消息段: %w", err)
		}
		return &types.Inbound{Kind: kind, Account: account, Conv: messageConv(o, account, uid, self), PlatformID: str(o, "message_id"), From: types.User{ID: from, Nick: str(sender, "nickname"), Card: str(sender, "card")}, Time: at, Segs: segs}, nil, nil, nil
	case "notice":
		conv := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: account, ID: noticePeer(o, account, uid)}
		if group != "" && group != "0" {
			conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: group}
		}
		actor := qqID(uid)
		if actor == "" {
			// 全员禁言这类通知 user_id 是 0，做事的是操作人（照 Bun 的 wholeBan）；操作人也是 0 就是没说是谁
			actor = qqID(str(o, "operator_id"))
		}
		n := &types.Notice{Account: account, Conv: conv, Time: at, Actor: actor}
		typ := str(o, "notice_type")
		switch {
		case typ == "bot_offline" || typ == "kicked_offline":
			// 被踢下线没有会话；只报告固定类别，协议端自由文本不交给日志出口。
			return nil, &types.Notice{Kind: types.NoticeKicked, Account: account, Time: at}, &types.Status{Kind: types.StatusKicked, Account: account, Time: at}, nil
		case typ == "group_msg_emoji_like" && reaction(o, n):
		case typ == "group_recall" || typ == "friend_recall":
			n.Kind = types.NoticeRecall
			n.MsgID = str(o, "message_id")
			if op := qqID(str(o, "operator_id")); op != "" {
				n.Actor = op
			}
		case typ == "notify" && str(o, "sub_type") == "poke":
			n.Kind = types.NoticePoke
			n.Target = qqID(str(o, "target_id"))
			// 私聊里认不出对方（她戳她自己这种）就不翻（照 Bun）
			if conv.Scene == types.ScenePrivate && conv.ID == "" {
				return nil, nil, nil, nil
			}
		case typ == "notify" && str(o, "sub_type") == "input_status":
			if conv.Scene != types.ScenePrivate || conv.ID == "" {
				return nil, nil, nil, nil
			}
			return nil, nil, &types.Status{Kind: types.StatusTyping, Account: account, Time: at, Conv: conv}, nil // “对方正在输入”是瞬时状态，不是事件，不进库
		default:
			n.Kind = types.NoticeOther
			n.Raw = clip(strings.TrimSpace(string(frame)))
		}
		return nil, n, nil, nil
	case "meta_event":
		switch str(o, "meta_event_type") {
		case "heartbeat":
			return nil, nil, &types.Status{Kind: types.StatusHeartbeat, Account: account, Time: at, Interval: intervalOf(o)}, nil
		case "lifecycle":
			// connect、enable 才算连上；disable 是协议端停用了 OneBot，当成连上会让通道门清零（规格第八节）
			if sub := str(o, "sub_type"); sub == "connect" || sub == "enable" {
				return nil, nil, &types.Status{Kind: types.StatusConnected, Account: account, Time: at}, nil
			}
		}
	}
	return nil, nil, nil, nil
}
