package snowluma

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
)

// callTimeout 从调用开始计时，包含核验等待、写入和回包。出口外面还有 30 秒兜底，
// 这里先取消 Write，不能让背压把适配器自己的上限拖过去（规格-QQ发送上线 3.2）。
const callTimeout = 10 * time.Second

// jsonIntRe 认 JSON 整数：0，或者没有前导零的十进制（可带一个负号）。0.0、"0"、true 都不是。
var jsonIntRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

var (
	errNotConnected = errors.New("没连上")
	errConnLost     = errors.New("写出以后连接断了")
	errCallTimeout  = errors.New("写出以后没有回包")
)

const (
	kindText = "text"
	kindPoke = "poke"
)

// live 是当前这条已连上的连接。dead 在拆掉时关闭，等回包的调用立刻醒来。
type live struct {
	c     Conn
	dead  chan struct{}
	ready chan struct{} // 当前连接核验通过；只在 mu 里关闭一次。
	once  sync.Once
}

func (l *live) kill() {
	if l == nil {
		return
	}
	l.once.Do(func() { close(l.dead) })
}

// callResult 是一次 action 等到的东西：frame 是对上 echo 的回包；lost 是写出以后连接没了。
type callResult struct {
	frame []byte
	lost  bool
}

type actionRequest struct {
	Action string `json:"action"`
	Params any    `json:"params"`
	Echo   string `json:"echo"`
}

// attach 把这条连接换成当前可以写 action 的那条。同一时刻只有一条。
func (a *Adapter) attach(c Conn) *live {
	l := &live{c: c, dead: make(chan struct{}), ready: make(chan struct{})}
	a.mu.Lock()
	a.live = l
	a.mu.Unlock()
	return l
}

// detach 拆掉这条连接。是当前这条的话，还在等的调用一律当「写出以后断了」（通道缓冲 1，不会堵在这里）。
func (a *Adapter) detach(l *live) {
	if l == nil {
		return
	}
	a.mu.Lock()
	if a.live == l {
		a.live = nil
		for echo, ch := range a.pend {
			delete(a.pend, echo)
			select {
			case ch <- callResult{lost: true}:
			default:
			}
		}
	}
	a.mu.Unlock()
	l.kill()
}

// call 固定使用传入的连接；l 为空时，在同一把锁里取得发送许可和对应连接。
// wrote 表示已经进入 Write，不能证明零字节写出的错误也算可能已发，不能重试。
// echo 用自己的序号：重试是新调用，迟到的上一包不能对上这一次。
func (a *Adapter) call(ctx context.Context, l *live, action string, params any) (frame []byte, wrote bool, err error) {
	limit := callTimeout
	if a.cfg.CallTimeout > 0 {
		limit = a.cfg.CallTimeout
	}
	return a.callFor(ctx, l, action, params, limit)
}

// callFor 同 call，只是超时可以换：发送一律用 callTimeout，查询（query.go）一次动作在协议端可能有好几次往返，单独给更长的时限，不去改发送的。
func (a *Adapter) callFor(ctx context.Context, l *live, action string, params any, limit time.Duration) (frame []byte, wrote bool, err error) {
	// 计时器可以换一次：取得许可和编好帧之前按 limit 计，知道帧有多大以后换成 limit 加放宽量（大图上传慢，types.UploadAllowance），
	// 从写入开始重新计。换的时候同步造好新的、停掉旧的，goroutine 看到被换了就接着等新的
	var tmu sync.Mutex
	timer := a.timer(limit)
	poke := make(chan struct{}, 1)
	extend := func(d time.Duration) {
		tmu.Lock()
		old := timer
		timer = a.timer(d)
		tmu.Unlock()
		old.Stop()
		select {
		case poke <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			tmu.Lock()
			t := timer
			tmu.Unlock()
			select {
			case <-t.C():
				tmu.Lock()
				current := t == timer
				tmu.Unlock()
				if current {
					cancel(errCallTimeout)
					return
				}
			case <-poke:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		cancel(nil)
		tmu.Lock()
		timer.Stop()
		tmu.Unlock()
		<-done
	}()
	if l == nil {
		l, err = a.allowSend(ctx)
		if err != nil {
			return nil, false, err
		}
	}
	a.mu.Lock()
	if a.live != l || ctx.Err() != nil {
		a.mu.Unlock()
		if ctx.Err() != nil {
			return nil, false, context.Cause(ctx)
		}
		return nil, false, errNotConnected
	}
	select {
	case <-l.dead:
		a.mu.Unlock()
		return nil, false, errNotConnected
	default:
	}
	a.seq++
	echo := "s" + strconv.FormatUint(a.seq, 10)
	ch := make(chan callResult, 1)
	if a.pend == nil {
		a.pend = map[string]chan callResult{}
	}
	a.pend[echo] = ch
	a.mu.Unlock()

	body, err := json.Marshal(actionRequest{Action: action, Params: params, Echo: echo})
	if err != nil {
		a.forget(echo)
		return nil, false, err
	}
	if ctx.Err() != nil {
		a.forget(echo)
		return nil, false, context.Cause(ctx)
	}
	select {
	case <-l.dead:
		a.forget(echo)
		return nil, false, errNotConnected
	default:
	}
	if extra := types.UploadAllowance(int64(len(body))); extra > 0 {
		// 大帧（十几 MB 的图）写到协议端、等它传到 QQ 要比 10 秒久：写入不能被 10 秒的计时器掐断（coder/websocket 写入被取消会关掉整条连接）
		extend(limit + extra)
	}
	// 诊断（2026-11-20，待办第八节“CI 偶发红的遗留”①）：CI 上撤回补发那批红时，适配器自己先关了连接、前面没有一行说是谁关的。
	// coder/websocket 写帧途中 ctx 结束会顺手关掉整条连接（write.go 的 setupWriteTimeout），这里把写失败和写得慢都记下来；查清后再定改法
	start := time.Now()
	if err := l.c.Write(ctx, body); err != nil {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
			a.warnf("写 %s 帧（%d 字节）用了 %v 被打断，库会顺手关掉这条连接: %s", action, len(body), time.Since(start).Round(time.Millisecond), a.safeError(err))
		} else {
			a.warnf("写 %s 帧（%d 字节）失败，用了 %v: %s", action, len(body), time.Since(start).Round(time.Millisecond), a.safeError(err))
		}
		return a.complete(echo, ch, true, err)
	}
	if d := time.Since(start); d > time.Second {
		a.logf("写 %s 帧（%d 字节）用了 %v", action, len(body), d.Round(time.Millisecond))
	}
	select {
	case res := <-ch:
		if res.lost {
			return nil, true, errConnLost
		}
		return res.frame, true, nil
	case <-ctx.Done():
		return a.complete(echo, ch, true, context.Cause(ctx))
	case <-l.dead:
		return a.complete(echo, ch, true, errConnLost)
	}
}

// complete 和 takeReply 共用完成点：退出先删等待项，已匹配的回包则已经投递，优先认它。
func (a *Adapter) complete(echo string, ch chan callResult, wrote bool, fallback error) ([]byte, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pend, echo)
	select {
	case res := <-ch:
		if res.lost {
			return nil, wrote, errConnLost
		}
		return res.frame, wrote, nil
	default:
		return nil, wrote, fallback
	}
}

func (a *Adapter) forget(echo string) {
	a.mu.Lock()
	delete(a.pend, echo)
	a.mu.Unlock()
}

func (a *Adapter) timer(d time.Duration) types.Timer {
	if a.clock == nil {
		return realClock{}.NewTimer(d)
	}
	return a.clock.NewTimer(d)
}

// takeReply 认出 action 回包。对上了正在等的 echo 就交给那次调用；
// 有 echo、又不是事件（没有 post_type）、却对不上的，丢掉并记一行，不交给接收端。
// 返回 true 表示这帧已经处理完，调用方不要再翻译。
func (a *Adapter) takeReply(frame []byte) bool {
	echo, ok := frameEcho(frame)
	if !ok {
		return false
	}
	a.mu.Lock()
	ch := a.pend[echo]
	if ch == nil {
		a.mu.Unlock()
		// 不含令牌、不含原文：对不上的回包整帧不进日志，也不交接收端翻译。
		a.warnf("对不上的 action 回包丢掉了")
		return true
	}
	delete(a.pend, echo)
	ch <- callResult{frame: append([]byte(nil), frame...)}
	a.mu.Unlock()
	return true
}

// frameEcho 取出回包的 echo。事件帧（有 post_type）不是回包，哪怕带着 echo 也不抢。
func frameEcho(frame []byte) (string, bool) {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return "", false
	}
	if _, isEvent := o["post_type"]; isEvent {
		return "", false
	}
	raw := o["echo"]
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return "", false
	}
	return s, true
}

func (a *Adapter) Send(ctx context.Context, account string, p types.Part) (types.Receipt, error) {
	if !a.Caps().Supports(p) {
		return types.Receipt{}, types.ErrUnsupported
	}
	var action, reason string
	var params any
	var ok bool
	switch p.Kind {
	case types.PartMessage:
		action, params, reason, ok = messageAction(p, a.Caps().Media(p.Conv.Scene).ImageBase64)
	case types.PartForward:
		a.mu.Lock()
		self := a.loginName
		a.mu.Unlock()
		action, params, reason, ok = forwardAction(p, a.cfg.Account, self, a.Caps().Media(p.Conv.Scene).ImageBase64)
	case types.PartReact:
		action, params, reason, ok = reactAction(p)
	}
	if !ok {
		return failedReceipt(p.ID, reason, a.now()), nil
	}
	if reason, ok := a.accountOK(account); !ok {
		return unknownReceipt(p.ID, reason, a.now()), nil
	}
	frame, wrote, err := a.call(ctx, nil, action, params)
	return a.finish(p.ID, p.Kind, frame, wrote, err), nil
}

func (a *Adapter) Poke(ctx context.Context, account string, c types.Conversation, target string) (types.Receipt, error) {
	action, params, reason, ok := pokeAction(c, target)
	if !ok {
		return failedReceipt("", reason, a.now()), nil
	}
	if reason, ok := a.accountOK(account); !ok {
		return unknownReceipt("", reason, a.now()), nil
	}
	frame, wrote, err := a.call(ctx, nil, action, params)
	return a.finish("", kindPoke, frame, wrote, err), nil
}

// accountOK：这条连接配了号，调用方却拿别的号来发，就别写出去。没配号（New 造的、只读帧）不在这里拦。
func (a *Adapter) accountOK(account string) (string, bool) {
	if a.cfg.Account != "" && account != a.cfg.Account {
		return "不是这条连接的号", false
	}
	return "", true
}

func messageAction(p types.Part, imageBase64 bool) (string, any, string, bool) {
	id, ok := positiveID(p.Conv.ID)
	if !ok {
		return "", nil, "会话号不是数字", false
	}
	msg := make([]any, 0, 2)
	if p.Quote != "" {
		if _, ok := decimalID(p.Quote); !ok {
			return "", nil, "引用消息号不是合法十进制整数", false
		}
		msg = append(msg, map[string]any{"type": "reply", "data": map[string]any{"id": p.Quote}})
	}
	body, err := encodeBody(p.Body, imageBase64)
	if err != nil {
		return "", nil, "消息媒体不可读", false
	}
	msg = append(msg, body...)
	switch p.Conv.Scene {
	case types.SceneGroup:
		return "send_group_msg", map[string]any{"group_id": id, "message": msg}, "", true
	case types.ScenePrivate:
		return "send_private_msg", map[string]any{"user_id": id, "message": msg}, "", true
	default:
		return "", nil, "会话不是群也不是私聊", false
	}
}

// selfName 是连接自己的 QQ 昵称（取不到传空）：节点要求用它时代替 Name；号和头像永远是连接自己的。
func forwardAction(p types.Part, account, selfName string, imageBase64 bool) (string, any, string, bool) {
	id, ok := positiveID(p.Conv.ID)
	if !ok {
		return "", nil, "会话号不是数字", false
	}
	if _, ok := positiveID(account); !ok {
		return "", nil, "连接没有自己的号", false
	}
	if len(p.Nodes) == 0 {
		return "", nil, "转发没有节点", false
	}
	nodes := make([]any, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.ReplyTo != 0 {
			// 规格-链接发送改版 A7：SnowLuma 的 reply 段要的是一条真消息的正整数 replySeq（index.mjs 的 reply 校验），
			// 转发里的节点没有消息号，“指向同一转发里另一节点”的写法根本没有；回能力缺失，链接插件去掉引用按旧排法重发
			return "", nil, types.RejectedErr + "：不支持转发节点里的引用", false
		}
		if n.Name == "" {
			return "", nil, "转发节点没有显示名", false
		}
		body, err := encodeBody(n.Body, imageBase64)
		if err != nil {
			return "", nil, "转发节点媒体不可读", false
		}
		name := n.Name
		if n.Self && selfName != "" {
			name = selfName
		}
		nodes = append(nodes, map[string]any{"type": "node", "data": map[string]any{
			"name": name, "uin": account,
			"content": body,
		}})
	}
	params := func(key string) map[string]any {
		m := map[string]any{key: id, "messages": nodes}
		if c := p.Card; !c.Empty() {
			// 空项不传，让 QQ 用默认；SnowLuma 的 readForwardPreviewMeta 把没传的当没有
			if c.Source != "" {
				m["source"] = c.Source
			}
			if len(c.News) > 0 {
				news := make([]any, len(c.News))
				for i, t := range c.News {
					news[i] = map[string]any{"text": t}
				}
				m["news"] = news
			}
			if c.Summary != "" {
				m["summary"] = c.Summary
			}
			if c.Prompt != "" {
				m["prompt"] = c.Prompt
			}
		}
		return m
	}
	switch p.Conv.Scene {
	case types.SceneGroup:
		return "send_group_forward_msg", params("group_id"), "", true
	case types.ScenePrivate:
		return "send_private_forward_msg", params("user_id"), "", true
	default:
		return "", nil, "会话不是群也不是私聊", false
	}
}

func encodeBody(body []types.BodySegment, imageBase64 bool) ([]any, error) {
	if err := types.ValidateBody(body); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(body))
	for _, s := range body {
		if s.Kind == types.BodyText {
			out = append(out, map[string]any{"type": "text", "data": map[string]any{"text": s.Text}})
			continue
		}
		kind, file := "image", ""
		if s.Kind == types.BodyVideo || !imageBase64 {
			name, err := s.Media.FileName()
			if err != nil {
				return nil, err
			}
			name, err = filepath.Abs(name)
			if err != nil {
				return nil, err
			}
			path := filepath.ToSlash(name)
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			file = (&url.URL{Scheme: "file", Path: path}).String()
			if s.Kind == types.BodyVideo {
				kind = "video"
			}
		} else {
			var b bytes.Buffer
			b.Grow(9 + base64.StdEncoding.EncodedLen(int(s.Media.Size)))
			b.WriteString("base64://")
			err := s.Media.Read(func(r io.Reader) error {
				e := base64.NewEncoder(base64.StdEncoding, &b)
				_, err := io.Copy(e, r)
				return errors.Join(err, e.Close())
			})
			if err != nil {
				return nil, err
			}
			file = b.String()
		}
		out = append(out, map[string]any{"type": kind, "data": map[string]any{"file": file}})
	}
	return out, nil
}

// reactAction 只在群里发：私聊协议端回 failed/100"不支持"（3.4 真帧）。
// message_id 照录到的成功帧写成 JSON 数字（群里是负数），emoji_id 是字符串；成功回包 data 为 null，没有新消息号。
func reactAction(p types.Part) (string, any, string, bool) {
	if p.Conv.Scene != types.SceneGroup {
		return "", nil, "私聊不支持回应", false
	}
	id, ok := decimalID(p.Ref)
	if !ok || p.EmojiID == "" {
		return "", nil, "回应没有合法消息号或表情编号", false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", nil, "回应没有合法消息号或表情编号", false
	}
	params := map[string]any{"message_id": n, "emoji_id": p.EmojiID}
	if p.Unset {
		params["set"] = false // 取消自己贴过的这个表情；省略时协议端默认 true（贴）
	}
	return "set_msg_emoji_like", params, "", true
}

func pokeAction(c types.Conversation, target string) (string, any, string, bool) {
	who, ok := positiveID(target)
	if !ok {
		return "", nil, "没有要戳的人", false
	}
	switch c.Scene {
	case types.SceneGroup:
		gid, ok := positiveID(c.ID)
		if !ok {
			return "", nil, "会话号不是数字", false
		}
		return "group_poke", map[string]any{"group_id": gid, "user_id": who}, "", true
	case types.ScenePrivate:
		return "friend_poke", map[string]any{"user_id": who}, "", true
	default:
		return "", nil, "会话不是群也不是私聊", false
	}
}

func positiveID(s string) (int64, bool) {
	if !jsonIntRe.MatchString(s) || stringsHasSignZero(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func stringsHasSignZero(s string) bool {
	return s == "0" || s == "-0"
}

// finish 把一次 call 收成回执。核验拒绝没有发送结果，不重试、不计连续失败；写出后没对上也按未知办。
func (a *Adapter) finish(partID, kind string, frame []byte, wrote bool, err error) types.Receipt {
	at := a.now()
	if !wrote {
		if errors.Is(err, errLoginMismatch) || errors.Is(err, errUnhealthy) || errors.Is(err, errSelfMismatch) {
			return unknownReceipt(partID, err.Error(), at)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return failedReceipt(partID, "取消了，还没发出去", at)
		}
		if errors.Is(err, errNotConnected) {
			return failedReceipt(partID, "没连上", at)
		}
		return failedReceipt(partID, "没写出去", at)
	}
	if err != nil {
		switch {
		case errors.Is(err, errConnLost):
			return unknownReceipt(partID, types.ConnLostErr, at)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return unknownReceipt(partID, "取消了等待", at)
		default:
			return unknownReceipt(partID, types.NoReplyErr, at)
		}
	}
	return classifyReply(partID, kind, frame, at)
}

// classifyReply 按规格 3.2 认回包。retcode 必须是 JSON 整数 0；status 为 failed 一律明确失败。
// 文字和转发要有非 0 的 message_id（负数合法，按十进制原样存）。戳一戳和回应不产生新消息，平台消息号留空。
func classifyReply(partID, kind string, frame []byte, at time.Time) types.Receipt {
	var o object
	if json.Unmarshal(frame, &o) != nil || o == nil {
		return unknownReceipt(partID, "回包不能确认", at)
	}
	if str(o, "status") == "failed" {
		if unconfirmedReply(o) {
			// 协议端自己说没拿到回执（SnowLuma 的 delivery was not confirmed，QQ 往往已经收了）：不是拒绝，不能重试，也不能换形式补发
			return unknownReceipt(partID, types.UnconfirmedErr, at)
		}
		return failedReceipt(partID, rejectReason(o), at)
	}
	if string(o["retcode"]) != "0" {
		return unknownReceipt(partID, "回包不能确认", at)
	}
	if kind == kindPoke || kind == types.PartReact {
		// 即使带了 message_id 也不映射：这两种动作不产生新消息。
		return confirmedReceipt(partID, "", at)
	}
	id, ok := messageID(o["data"])
	if !ok {
		return unknownReceipt(partID, types.NoIDErr, at)
	}
	return confirmedReceipt(partID, id, at)
}

// capabilityWords 是 SnowLuma 说“这个场合发不了这种段”时的报错文字（源码里的 UNSENDABLE_TYPE 一类）。
// 其余 failed（内容审核、单张太大、链接过期……）协议端没给可分辨的文字，一律当作“这一条被拒”，分辨不了不往能力缺失上猜。
var capabilityWords = []string{"cannot be sent", "unsendable", "only accepts text", "is not supported in", "only valid inside", "no executable send semantics", "不支持"}

// unconfirmedWords 是 SnowLuma 说“请求发出去了，但没拿到送达回执”时的报错文字：群消息 result=0 却没有 sequence
// （“group message delivery was not confirmed: QQ returned no message receipt; check the conversation before retrying”）、
// 以及等不到协议端回包（“send ... failed: no response”）。这两种消息可能已经到了，按结果未知办。
var unconfirmedWords = []string{"delivery was not confirmed", "not confirmed", "no message receipt", "before retrying", "failed: no response", "timed out", "timeout"}

// unconfirmedReply 说一个 failed 回包其实是“结果不明”：先于能力缺失、拒绝判断。
func unconfirmedReply(o object) bool {
	words := strings.ToLower(str(o, "wording") + " " + str(o, "message") + " " + str(o, "msg"))
	for _, w := range unconfirmedWords {
		if strings.Contains(words, w) {
			return true
		}
	}
	return false
}

// rejectionWords 是协议端说“这份内容被拒了”的措辞（审核、风控、格式、体积一类）。failed 回包里没有这些词，也没有能力缺失的词，
// 就不是可确认的内容拒绝（可能是 NT 忙、限流、临时 send failed）：不带“协议端拒绝”前缀，出口照旧重试，链接插件不换形式、不记按图的被拒结论。
var rejectionWords = []string{"reject", "block", "risk", "censor", "audit", "illegal", "forbidden", "sensitive", "violat", "too large", "too big", "size limit", "exceed", "invalid image", "image format", "decode", "审核", "拒绝", "违规", "风控", "敏感", "过大", "不支持"}

// transientErr 是 failed 回包里没有可识别的拒绝措辞时的原因：和“协议端拒绝”区分开，让出口照旧重试
const transientErr = "协议端报发送失败"

func hasAny(words string, list []string) bool {
	for _, w := range list {
		if strings.Contains(words, w) {
			return true
		}
	}
	return false
}

// rejectReason 只返回固定文案，不带协议端原文（原文可能带内容或地址）：能力缺失写“协议端拒绝：不支持这类消息”，有审核、风控、格式、体积一类措辞的写“协议端拒绝”，其余（没有可识别措辞的 failed）写 transientErr，不当内容拒绝。
func rejectReason(o object) string {
	words := strings.ToLower(str(o, "wording") + " " + str(o, "message") + " " + str(o, "msg"))
	if hasAny(words, capabilityWords) {
		return types.RejectedErr + "：不支持这类消息"
	}
	if hasAny(words, rejectionWords) {
		return types.RejectedErr
	}
	return transientErr
}

// messageID 是 data.message_id 的十进制原文。JSON 整数和十进制字符串都认；0、空、缺、小数都不是。
func messageID(data json.RawMessage) (string, bool) {
	if len(data) == 0 || string(data) == "null" {
		return "", false
	}
	var o object
	if json.Unmarshal(data, &o) != nil || o == nil {
		return "", false
	}
	raw := o["message_id"]
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	if id, ok := decimalID(string(raw)); ok {
		return id, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return decimalID(s)
}

// decimalID 收下非 0 的十进制整数原文。JSON 数字的原文就是它自己；字符串要整段都是这个整数。
func decimalID(s string) (string, bool) {
	if !jsonIntRe.MatchString(s) {
		return "", false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return "", false
	}
	return s, true
}

func failedReceipt(partID, reason string, at time.Time) types.Receipt {
	return types.Receipt{PartID: partID, Status: types.ReceiptFailed, Via: types.ViaReply, Err: reason, At: at}
}

func unknownReceipt(partID, reason string, at time.Time) types.Receipt {
	return types.Receipt{PartID: partID, Status: types.ReceiptUnknown, Via: types.ViaReply, Err: reason, At: at}
}

func confirmedReceipt(partID, platformID string, at time.Time) types.Receipt {
	return types.Receipt{PartID: partID, Status: types.ReceiptConfirmed, PlatformID: platformID, Via: types.ViaReply, At: at}
}
