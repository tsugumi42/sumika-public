package snowluma

// 向协议端查询历史页或按消息号取单条；查询共用串行、间隔和本轮预算。
// 查询是只读的：不发消息、不改协议端任何东西，所以不受发送钳制管；但仍要等连接核验通过（allowSend），
// 不然登录号对不上时会向别人的号查东西。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"sumika-native/internal/channel/types"
)

const (
	// queryTimeout：一次历史类动作在协议端可能有十几次往返（规格 5.3），callTimeout 的 10 秒会误判超时，所以查询单独给时限
	queryTimeout = 45 * time.Second
	// queryGap：两次查询之间最少隔多久。照 SnowLuma 自己登录时补历史用的间隔，给 QQ 的"不可疑"节奏
	queryGap = 2 * time.Second
)

var _ types.MessageReader = (*Adapter)(nil)
var _ types.HistoryReader = (*Adapter)(nil)

// BeginQueryRound / EndQueryRound 由唯一的补录协调器调用；媒体刷新也在 query 里扣同一份预算。
func (a *Adapter) BeginQueryRound(limit int) {
	a.mu.Lock()
	a.queryBudget, a.queryBudgetOn = limit, true
	a.mu.Unlock()
}

func (a *Adapter) EndQueryRound() {
	a.mu.Lock()
	a.queryBudgetOn = false
	a.mu.Unlock()
}

// RemainingQueryBudget 由补录协调器在断线收尾时保存；重连不能重置同一预算窗口。
func (a *Adapter) RemainingQueryBudget() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.queryBudget
}

// ErrNoReply：查询没有从协议端拿到回包（没连上、超时、连接断了、核验没通过、被取消）。
// 调用方靠它分清"暂时问不到"和"协议端答了、但不行"（后者是别的错误），前者可以过一阵再问
var ErrNoReply = errors.New("没有拿到回包")

// ErrPartialPage 表示历史页里有消息翻译不了；返回的页仍带着翻译得了的那部分，只证明“有”，不证明“没有”。
var ErrPartialPage = errors.New("历史页含无效消息，覆盖范围未知")

// queryGap 取配置的查询间隔，没配用默认。
func (a *Adapter) queryGap() time.Duration {
	if a.cfg.QueryGap > 0 {
		return a.cfg.QueryGap
	}
	return queryGap
}

// query 发一个只读动作，等回包。同一时刻只放一个在途，和上一次结束隔至少 queryGap；ctx 取消随时放弃等待
func (a *Adapter) query(ctx context.Context, action string, params any) ([]byte, error) {
	a.qonce.Do(func() { a.qsem = make(chan struct{}, 1) })
	select {
	case a.qsem <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	defer func() { <-a.qsem }()
	if !a.lastQuery.IsZero() {
		if wait := a.lastQuery.Add(a.queryGap()).Sub(a.now()); wait > 0 {
			t := a.timer(wait)
			select {
			case <-t.C():
			case <-ctx.Done():
				t.Stop()
				return nil, context.Cause(ctx)
			}
		}
	}
	a.mu.Lock()
	if a.queryBudgetOn {
		if a.queryBudget == 0 {
			a.mu.Unlock()
			return nil, types.ErrQueryBudget
		}
		a.queryBudget--
	}
	a.mu.Unlock()
	frame, sent, err := a.callFor(ctx, nil, action, params, queryTimeout)
	if sent {
		// 间隔是给"发出去的请求"留的；根本没发出去的（没连上、核验没过）不占间隔，连上以后的第一次查询不用白等
		a.lastQuery = a.now()
	}
	return frame, err
}

// Message 按平台消息号取回一条已经收过的消息（OneBot get_msg）。协议端取消息时会刷新图片、语音、视频的地址，
// 所以拿它给过期的图片换新链接。回包里没有 self_id、post_type（SnowLuma 删掉了），这里按配置的号补上再走 translate，
// 翻译规则只有一份。翻出来的会话和消息号必须就是请求的那一个，对不上整条不要
func (a *Adapter) Message(ctx context.Context, account string, conv types.Conversation, platformID string) (types.Inbound, error) {
	if reason, ok := a.accountOK(account); !ok || a.cfg.Account == "" {
		if ok {
			reason = "这条连接没配号"
		}
		return types.Inbound{}, errors.New(reason)
	}
	if err := conv.Valid(); err != nil || conv.Channel != a.Name() {
		return types.Inbound{}, errors.New("要查的会话不合法")
	}
	id, ok := decimalID(platformID)
	if !ok {
		return types.Inbound{}, errors.New("平台消息号不是合法十进制整数")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return types.Inbound{}, errors.New("平台消息号不是合法十进制整数")
	}
	frame, err := a.query(ctx, "get_msg", map[string]any{"message_id": n})
	if err != nil {
		if errors.Is(err, types.ErrQueryBudget) {
			return types.Inbound{}, err
		}
		return types.Inbound{}, fmt.Errorf("get_msg %w：%s", ErrNoReply, errQuery(err))
	}
	var reply object
	if json.Unmarshal(frame, &reply) != nil || reply == nil {
		return types.Inbound{}, errors.New("get_msg 的回包不是对象")
	}
	if str(reply, "status") == "failed" || string(reply["retcode"]) != "0" {
		return types.Inbound{}, fmt.Errorf("get_msg 被协议端拒绝（retcode %s）", clipRetcode(reply["retcode"]))
	}
	in, err := historyInbound(reply["data"], a.cfg.Account)
	if err != nil {
		return types.Inbound{}, err
	}
	if in.Conv != conv {
		return types.Inbound{}, errors.New("get_msg 回来的消息不在请求的会话里，丢弃")
	}
	if got, ok := decimalID(in.PlatformID); !ok || got != id {
		return types.Inbound{}, errors.New("get_msg 回来的消息号和请求的不一样，丢弃")
	}
	return in, nil
}

// errQuery 把底层错误压成不带内容的短话：net 和 context 的错误文字里没有消息原文，但也没必要原样往上带
func errQuery(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("取消了")
	case errors.Is(err, errCallTimeout), errors.Is(err, context.DeadlineExceeded):
		return errors.New("超时")
	case errors.Is(err, errNotConnected):
		return errors.New("没连上")
	case errors.Is(err, errConnLost):
		return errors.New("连接断了")
	case errors.Is(err, errLoginMismatch), errors.Is(err, errUnhealthy), errors.Is(err, errSelfMismatch):
		return errors.New("连接还没核验通过")
	}
	return errors.New("失败")
}

func clipRetcode(raw json.RawMessage) string {
	if len(raw) == 0 || len(raw) > 12 {
		return "?"
	}
	return string(raw)
}

// historyInbound 把 get_msg / 历史动作回包里的一条消息翻成 Inbound。SnowLuma 的这类回包没有 self_id 和 post_type：
// 补上配置的号；发言人是她自己的号就按"自己发的"（message_sent）。回包自带的 self_id 和配置的号对不上，整条不要
func historyInbound(data json.RawMessage, account string) (types.Inbound, error) {
	var o object
	if json.Unmarshal(data, &o) != nil || o == nil {
		return types.Inbound{}, errors.New("回包里没有消息")
	}
	if _, has := o["self_id"]; has && str(o, "self_id") != account {
		return types.Inbound{}, errors.New("回包里的号和配置的号不一致，丢弃")
	}
	uid := str(o, "user_id")
	if uid == "" {
		uid = str(obj(o, "sender"), "user_id")
	}
	uid = qqID(uid)
	post := "message"
	if uid != "" && uid == account {
		post = "message_sent"
	}
	selfID, _ := json.Marshal(account)
	postType, _ := json.Marshal(post)
	o["self_id"], o["post_type"] = selfID, postType
	frame, err := json.Marshal(o)
	if err != nil {
		return types.Inbound{}, err
	}
	in, _, _, err := translate(frame)
	if err != nil {
		return types.Inbound{}, err
	}
	if in == nil {
		return types.Inbound{}, errors.New("回包翻不成消息")
	}
	return *in, nil
}

// 群首页锚在协议端本地最新序号，不能证明停机后的尾段；私聊无锚点走服务端漫游。
func (a *Adapter) HistoryPage(ctx context.Context, account string, conv types.Conversation, anchor string, count int) (types.HistoryPage, error) {
	if reason, ok := a.accountOK(account); !ok || a.cfg.Account == "" {
		return types.HistoryPage{}, fmt.Errorf("历史查询账号不匹配：%s", reason)
	}
	if err := conv.Valid(); err != nil || conv.Channel != a.Name() || (conv.Scene == types.ScenePrivate && conv.Self != account) {
		return types.HistoryPage{}, errors.New("历史查询会话不合法")
	}
	if count < 1 || count > 200 {
		return types.HistoryPage{}, errors.New("历史查询条数须在 1 到 200 之间")
	}
	params := map[string]any{"count": count, "reverse_order": true}
	action, key := "get_group_msg_history", "group_id"
	if conv.Scene == types.ScenePrivate {
		action, key = "get_friend_msg_history", "user_id"
		if anchor != "" {
			return types.HistoryPage{}, errors.New("私聊历史不支持锚点翻页")
		}
	}
	id, err := strconv.ParseInt(conv.ID, 10, 64)
	if err != nil || id <= 0 {
		return types.HistoryPage{}, errors.New("历史查询会话号不合法")
	}
	params[key] = id
	if anchor != "" {
		n, err := strconv.ParseInt(anchor, 10, 64)
		if _, ok := decimalID(anchor); !ok || err != nil {
			return types.HistoryPage{}, errors.New("历史锚点不是非零整数")
		}
		params["message_id"] = n
	}
	frame, err := a.query(ctx, action, params)
	if err != nil {
		if errors.Is(err, types.ErrQueryBudget) {
			return types.HistoryPage{}, err
		}
		return types.HistoryPage{}, fmt.Errorf("历史查询 %w：%s", ErrNoReply, errQuery(err))
	}
	return parseHistoryPage(a, frame, account, conv)
}

// afterPageCount 是 HistoryAfter 每页向协议端要的条数；afterMaxPages 是最多翻几页（页间隔 queryGap，总共不超过十来秒）
const (
	afterPageCount = 50
	afterMaxPages  = 4
)

var _ types.AfterReader = (*Adapter)(nil)

// HistoryAfter 取 anchor 之后的消息直到最新（ADR-333）。
// 群：get_group_msg_history 带锚点、reverse_order=false，SnowLuma 向服务器取“锚点及更新的消息”（SsoGetGroupMsg 往新的方向走），
// 所以能拿到 SnowLuma 没来得及实时收到的；一页 50 条，满了就以最后一条为新锚点接着翻，最多 4 页。
// 私聊：没有锚点翻页，取服务端漫游的最新一页（“无锚点时从 QQ 服务器获取最新双向记录”）。
// 查不了（没连上、超时、锚点协议端不认、回包无效）返回 error；返回的页只证明“有”，ReachedLatest 才证明“没有”。
// 页里个别消息翻译不了时不丢整页：返回能翻译的部分，ReachedLatest 恒为假（“没有”不可信，“有”可信）。
func (a *Adapter) HistoryAfter(ctx context.Context, account string, conv types.Conversation, anchor string) (types.AfterPage, error) {
	if conv.Scene == types.ScenePrivate {
		page, err := a.HistoryPage(ctx, account, conv, "", afterPageCount)
		partial := errors.Is(err, ErrPartialPage)
		if err != nil && !partial {
			return types.AfterPage{}, err
		}
		// 页里该有源消息（它才发了几十秒）：没有就说明这页不是我们要的那一段（中间隔了五十条以上、漫游缓存落后……），什么都证明不了
		if !hasMessage(page.Messages, anchor) {
			return types.AfterPage{}, errors.New("最新一页里没有源消息，翻不到发送之后的消息")
		}
		// 有翻译不了的消息时页是残缺的：找得到算有，找不到不能算没有
		return types.AfterPage{Messages: page.Messages, ReachedLatest: page.Latest && len(page.Messages) < afterPageCount && !partial}, nil
	}
	if reason, ok := a.accountOK(account); !ok || a.cfg.Account == "" {
		return types.AfterPage{}, fmt.Errorf("历史查询账号不匹配：%s", reason)
	}
	if err := conv.Valid(); err != nil || conv.Channel != a.Name() {
		return types.AfterPage{}, errors.New("历史查询会话不合法")
	}
	gid, err := strconv.ParseInt(conv.ID, 10, 64)
	if err != nil || gid <= 0 {
		return types.AfterPage{}, errors.New("历史查询会话号不合法")
	}
	if _, ok := decimalID(anchor); !ok {
		return types.AfterPage{}, errors.New("历史锚点不是非零整数")
	}
	var out types.AfterPage
	seen := map[string]bool{}
	cur := anchor
	for i := 0; i < afterMaxPages; i++ {
		n, _ := strconv.ParseInt(cur, 10, 64)
		frame, err := a.query(ctx, "get_group_msg_history", map[string]any{"group_id": gid, "message_id": n, "count": afterPageCount, "reverse_order": false})
		if err != nil {
			if errors.Is(err, types.ErrQueryBudget) {
				return types.AfterPage{}, err
			}
			return types.AfterPage{}, fmt.Errorf("历史查询 %w：%s", ErrNoReply, errQuery(err))
		}
		page, err := parseHistoryPage(a, frame, account, conv)
		partial := errors.Is(err, ErrPartialPage)
		if err != nil && !partial {
			return types.AfterPage{}, err
		}
		fresh := 0
		for _, m := range page.Messages {
			if !seen[m.PlatformID] {
				seen[m.PlatformID] = true
				out.Messages = append(out.Messages, m)
				fresh++
			}
		}
		if len(page.Messages) == 0 || (i == 0 && !hasMessage(page.Messages, anchor)) {
			// 锚点协议端不认（没有权威序号）时 SnowLuma 回空或回别的：什么都证明不了
			return types.AfterPage{}, errors.New("历史查询没有返回锚点（锚点可能不被协议端认得）")
		}
		if partial {
			// 页里有翻译不了的消息：翻译得了的部分照常交回（找到就是到了），但不能证明翻到了尽头，也不再往后翻
			out.ReachedLatest = false
			return out, nil
		}
		if len(page.Messages) < afterPageCount || fresh == 0 {
			out.ReachedLatest = len(page.Messages) < afterPageCount
			return out, nil
		}
		newest := page.Messages[0]
		for _, m := range page.Messages {
			if m.MessageSeq > newest.MessageSeq || (m.MessageSeq == newest.MessageSeq && !m.Time.Before(newest.Time)) {
				newest = m
			}
		}
		cur = newest.PlatformID
	}
	return out, nil
}

func hasMessage(msgs []types.Inbound, id string) bool {
	for _, m := range msgs {
		if m.PlatformID == id {
			return true
		}
	}
	return false
}

// parseHistoryPage 把历史动作的回包翻成页；HistoryPage 和 HistoryAfter 共用
func parseHistoryPage(a *Adapter, frame []byte, account string, conv types.Conversation) (types.HistoryPage, error) {
	var reply struct {
		Status  string `json:"status"`
		Retcode *int   `json:"retcode"`
		Data    struct {
			Messages json.RawMessage `json:"messages"`
		} `json:"data"`
	}
	if json.Unmarshal(frame, &reply) != nil || reply.Retcode == nil || *reply.Retcode != 0 || reply.Status != "ok" {
		return types.HistoryPage{}, errors.New("历史查询被拒绝或回包无效")
	}
	var messages []json.RawMessage
	if len(reply.Data.Messages) == 0 || string(reply.Data.Messages) == "null" || json.Unmarshal(reply.Data.Messages, &messages) != nil {
		return types.HistoryPage{}, errors.New("历史回包的 messages 不是数组")
	}
	// 历史动作可能回退本地缓存，协议没有页内无遗漏保证，Continuous 保持 false。
	out := types.HistoryPage{Messages: make([]types.Inbound, 0, len(messages)), Latest: conv.Scene == types.ScenePrivate}
	for _, raw := range messages {
		in, err := historyPageInbound(raw, account, conv)
		if err != nil {
			a.logf("历史消息丢弃：%v", err)
			continue
		}
		out.Messages = append(out.Messages, in)
	}
	if len(out.Messages) != len(messages) {
		return out, ErrPartialPage
	}
	return out, nil
}

func historyPageInbound(raw json.RawMessage, account string, conv types.Conversation) (types.Inbound, error) {
	var o object
	if json.Unmarshal(raw, &o) != nil || o == nil {
		return types.Inbound{}, errors.New("消息不是对象")
	}
	id, ok := decimalID(str(o, "message_id"))
	if _, err := strconv.ParseInt(id, 10, 64); !ok || err != nil {
		return types.Inbound{}, errors.New("消息号无效")
	}
	sec, err := strconv.ParseInt(str(o, "time"), 10, 64)
	if err != nil || sec <= 0 || sec >= 100_000_000_000 {
		return types.Inbound{}, errors.New("消息时间不是有效的秒时间戳")
	}
	in, err := historyInbound(raw, account)
	if err != nil {
		return types.Inbound{}, err
	}
	uid, uidErr := strconv.ParseUint(in.From.ID, 10, 64)
	if in.Conv != conv || uidErr != nil || uid == 0 {
		return types.Inbound{}, errors.New("消息会话或发送者无效")
	}
	in.PlatformID, in.Time = id, time.Unix(sec, 0)
	if conv.Scene == types.SceneGroup {
		seq, err := strconv.ParseInt(str(o, "message_seq"), 10, 64)
		if err == nil && seq > 0 {
			in.MessageSeq = seq
		}
	}
	return in, nil
}
