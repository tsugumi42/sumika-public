// Package approvalrelay 是“主人 QQ 私聊审批”的投影和认单（规格-主人QQ私聊审批）：
// 本地发起的审批卡在主人离开时发到主人的 QQ 私聊，主人回“同意”“拒绝”就结算。
//
// 它不是第二个状态机：结算只走 approval.Approvals 的 DecideVia，先答先算由那里的认领保证；
// 这里只有“票”——这一单发出去没有、编号是几、什么时候作废。模型没有批准工具，回复在入库后、进模型之前被截走。
// 每个入口（发卡、认回复、结算、迟到回复）都现读配置绑定（Deps.Binding），读不到、关了、绑定变了都按关处理。
package approvalrelay

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/channel/types"
)

// 参数（规格第三节）。
const (
	// RemoteTTL 是一张票在 QQ 这边有效的时间：从送达确认起算。
	RemoteTTL = 5 * time.Minute
	// ambiguousWindow：有单刚被本机处理、过期、取消，或者刚启动后这段时间里，裸“同意”必须带单号，防迟到回复转到新单。
	ambiguousWindow = 10 * time.Minute
	// minRead：送达后这么短的时间内的裸回复要带单号（来不及看）。
	minRead      = 3 * time.Second
	maxOpen      = 16
	maxTombs     = 256
	maxSeenMsgs  = 256
	segmentRunes = 1500
	maxSegments  = 8
	defaultTick  = 5 * time.Second
	// decideLimit：回复已被接受但迟迟没有结局回调，超过这个时间就告诉主人“不知道有没有生效”。
	decideLimit = 30 * time.Second
)

// Approver 是审批的唯一状态机（*approval.Approvals 满足）。
type Approver interface {
	Subscribe(onRequest func(approval.Request), onResolved func(approval.Request, approval.Decision))
	DecideVia(id, choice, via string) (bool, error)
}

// Binding 是这一刻有效的绑定：主人私聊会话、负责发的角色。Deps.Binding 每次现读配置，关着或读不到就返回 false。
type Binding struct {
	Away      time.Duration
	Conv      types.Conversation // QQ 私聊：ID 是主人号，Self 是接收账号
	Character string
	Owner     string
	Account   string
}

// Delivery 是发卡的结果。只有 Confirmed 才开票；部分发送、结果未知、失败、试运行都是 false。
type Delivery struct {
	Confirmed  bool
	MessageIDs []string // 每一段的平台消息号，引用选单用
	// Attempted：至少有一段可能已经发到主人手机（已确认、结果未知、超时前已发出）。没确认送达但 Attempted 的卡，
	// 编号收成墓碑，主人若回这个编号会被明确告知“没有这一单”，裸“同意”也不会转到别的单。
	Attempted bool
}

// Deps 是宿主给的依赖。
type Deps struct {
	Approver Approver
	Binding  func(character string) (Binding, bool)
	// Eligible：这张卡在主窗口显示、能被本机点的那一类（本地私聊）。别的来源的卡不发 QQ。
	Eligible func(approval.Request) bool
	// Away：主人现在不在电脑前。宿主没有在场信号时返回 true（等够 awayAfterSeconds 就算离开）。
	Away func() bool
	// Send 把几段文字发到主人 QQ 私聊，等计划终态再返回。必须走宿主的出口和发送门。
	Send func(ctx context.Context, b Binding, texts []string) (Delivery, error)
	Now  func() time.Time
	Logf func(format string, args ...any)
	// Tick 是巡检间隔；0 用默认值，负数不起后台循环（测试手动 Step）。
	Tick time.Duration
}

// Inbound 是入库后的一条私聊消息，宿主从 channel.Stored 转来；身份字段都是宿主认证过的，不是昵称。
type Inbound struct {
	Character  string
	Conv       types.Conversation
	Account    string
	FromID     string // 平台号
	IsOwner    bool   // 宿主名册认出的主人（按号，不按昵称）
	Message    bool   // 是一条消息（不是通知、自己的号回报）
	PlatformID string
	Segs       []types.Seg
	Seq        int64
	Backfill   bool
	FromSelf   bool
	Stranger   bool
	Duplicate  bool
	// Time 是平台时间（整秒）。零值表示宿主给不出：这时只能靠到达时刻判断。
	Time time.Time
}

const (
	stPending  = iota // 等主人离开
	stSending         // 卡在发
	stOpen            // 卡发到了，等回复
	stDeciding        // 正在结算
)

type ticket struct {
	req       approval.Request
	id        string
	state     int
	b         Binding
	delivered time.Time
	deadline  time.Time
	msgIDs    []string
	// 发卡期间已经有了结局：发完以后补一句结局，不开票
	endedWhileSending *approval.Decision
	sentAt            time.Time // 开始发第一段的时刻：比它早的平台时间的裸回复不可能是在回这张卡
	choice            string    // QQ 回复要的结局（once、deny），结算后据实际结局说话
	decidingAt        time.Time
}

type tomb struct {
	id, reqID, title, reason string
	at                       time.Time
	conv                     types.Conversation
	msgIDs                   []string
}

// Relay 是投影和认单。
type Relay struct {
	d         Deps
	startedAt time.Time

	mu       sync.Mutex
	tickets  map[string]*ticket // 请求号 → 票
	byID     map[string]*ticket // 单号 → 开着的票（open、deciding）
	tombs    map[string]*tomb   // 单号 → 墓碑
	tombSeq  []string
	seenMsgs map[string]bool
	seenSeq  []string
	lastEnd  time.Time // 最近一次不是 QQ 结算的结局
	// reconnectAt：协议端最近一次重连。断线期间说的话可能在重连后才被推来，这段时间里的裸回复同样要带单号。
	reconnectAt time.Time
	closed      bool

	wg   sync.WaitGroup
	stop context.CancelFunc
	ctx  context.Context
}

// New 建 Relay 并订阅审批；Start 起巡检循环。
func New(d Deps) (*Relay, error) {
	if d.Approver == nil || d.Binding == nil || d.Send == nil {
		return nil, fmt.Errorf("approvalrelay 缺依赖：Approver、Binding、Send 都要给")
	}
	if d.Eligible == nil {
		d.Eligible = func(approval.Request) bool { return false }
	}
	if d.Away == nil {
		d.Away = func() bool { return true }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	r := &Relay{d: d, startedAt: d.Now(), tickets: map[string]*ticket{}, byID: map[string]*ticket{}, tombs: map[string]*tomb{}, seenMsgs: map[string]bool{}}
	r.ctx, r.stop = context.WithCancel(context.Background())
	d.Approver.Subscribe(r.onRequest, r.onResolved)
	return r, nil
}

// Start 起后台巡检；Tick 为负时不起，由调用方手动 Step。
func (r *Relay) Start() {
	tick := r.d.Tick
	if tick < 0 {
		return
	}
	if tick == 0 {
		tick = defaultTick
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-t.C:
				r.Step(r.ctx)
			}
		}
	}()
}

// Close 停下，等在飞的发送收尾。之后所有入口都不再动作。
func (r *Relay) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	r.wg.Wait()
}

// Idle 等在飞的异步回复发完（测试用）。
func (r *Relay) Idle() { r.wg.Wait() }

// OpenCount 是占着编号的票数（在发的和发到了等回复的都算）。
func (r *Relay) OpenCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// DeliveredCount 是确认发到、开始计时等回复的票数（测试用：等到过期时间定下来再拨钟）。
func (r *Relay) DeliveredCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.byID {
		if t.state == stOpen {
			n++
		}
	}
	return n
}

func (r *Relay) onRequest(req approval.Request) {
	if !r.d.Eligible(req) {
		return
	}
	if _, ok := r.d.Binding(req.CharacterID); !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.tickets[req.ID] != nil || len(r.tickets) >= maxOpen {
		return
	}
	r.tickets[req.ID] = &ticket{req: req, state: stPending}
}

func (r *Relay) onResolved(req approval.Request, d approval.Decision) {
	r.mu.Lock()
	t := r.tickets[req.ID]
	if t == nil || r.closed {
		r.mu.Unlock()
		return
	}
	switch t.state {
	case stPending:
		delete(r.tickets, req.ID)
		r.mu.Unlock()
	case stSending:
		t.endedWhileSending = &d
		r.mu.Unlock()
	default: // open、deciding
		if d.Via != "qq" {
			r.lastEnd = r.d.Now()
		}
		reason := endReason(d)
		r.closeLocked(t, reason)
		// 说什么按真实结局来：QQ 的回复被接受不等于生效（写库失败会按拒绝处理），所以确认话在这里说，不在 Control 里抢先说
		b, text := t.b, localEnded(t, d)
		if d.Via == "qq" {
			text = qqEnded(t, d)
		}
		r.mu.Unlock()
		r.say(b, text)
	}
}

func endReason(d approval.Decision) string {
	via := "本机"
	if d.Via == "qq" {
		via = "QQ"
	}
	switch d.Kind {
	case "approved":
		return via + "已批准"
	case "denied":
		if d.Via == "qq" && d.Reason != "" {
			return "QQ没生效" // 写库失败等：不是主人拒绝的，之后的裸回复仍要带单号
		}
		return via + "已拒绝"
	case "expired":
		return "已过期"
	default:
		return "已取消"
	}
}

func localEnded(t *ticket, d approval.Decision) string {
	head := fmt.Sprintf("编号 %s：", t.id)
	switch d.Kind {
	case "approved":
		return head + "本机已批准这一单，不用再回了。"
	case "denied":
		return head + "本机已拒绝这一单，不用再回了。"
	case "expired":
		return head + "这一单已过期，操作没有执行。"
	default:
		return head + "这一单已取消（对话被中止或有了新消息），操作没有执行。"
	}
}

// qqEnded 是 QQ 回复结算后的确认话，按实际结局说：不是主人拒绝的拒绝（写库失败）要说没执行。
func qqEnded(t *ticket, d approval.Decision) string {
	head := fmt.Sprintf("编号 %s：", t.id)
	switch {
	case d.Kind == "approved":
		return head + "已批准。"
	case d.Kind == "denied" && d.Reason == "":
		return head + "已拒绝。"
	case d.Kind == "denied":
		return head + "没能生效：审批结果没写进记录，已按拒绝处理，操作没有执行。需要的话请回本机重新处理。"
	case d.Kind == "expired":
		return head + "这一单已过期，操作没有执行。"
	default:
		return head + "这一单已取消（对话被中止或有了新消息），操作没有执行。"
	}
}

// NoteReconnect 告诉 Relay 协议端刚重连过：之后一段时间里的裸“同意”要带单号。
func (r *Relay) NoteReconnect() {
	r.mu.Lock()
	r.reconnectAt = r.d.Now()
	r.mu.Unlock()
}

// closeLocked 把票收成墓碑；调用时持有 r.mu。
func (r *Relay) closeLocked(t *ticket, reason string) {
	delete(r.tickets, t.req.ID)
	if t.id == "" {
		return
	}
	delete(r.byID, t.id)
	r.tombs[t.id] = &tomb{id: t.id, reqID: t.req.ID, title: t.req.Title, reason: reason, at: r.d.Now(), conv: t.b.Conv, msgIDs: t.msgIDs}
	r.tombSeq = append(r.tombSeq, t.id)
	if len(r.tombSeq) > maxTombs {
		delete(r.tombs, r.tombSeq[0])
		r.tombSeq = r.tombSeq[1:]
	}
}

// newID 取 4 位数字单号，和开着的、墓碑里的都不重复；持有 r.mu。
func (r *Relay) newID() (string, bool) {
	for range 64 {
		n, err := rand.Int(rand.Reader, big.NewInt(10000))
		if err != nil {
			return "", false
		}
		id := fmt.Sprintf("%04d", n.Int64())
		if r.byID[id] == nil && r.tombs[id] == nil {
			return id, true
		}
	}
	return "", false
}

// Step 巡检一遍：该发的发、过期的收、关了的收。能重复调。
func (r *Relay) Step(ctx context.Context) {
	now := r.d.Now()
	type job struct {
		t     *ticket
		b     Binding
		texts []string
	}
	var jobs []job
	var says []func()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	reqIDs := make([]string, 0, len(r.tickets))
	for id := range r.tickets {
		reqIDs = append(reqIDs, id)
	}
	sort.Strings(reqIDs)
	for _, rid := range reqIDs {
		t := r.tickets[rid]
		b, ok := r.d.Binding(t.req.CharacterID)
		switch t.state {
		case stPending:
			if !ok || now.UnixMilli() >= t.req.ExpiresAt {
				delete(r.tickets, rid)
				continue
			}
			if now.Sub(time.UnixMilli(t.req.CreatedAt)) < b.Away || (b.Away > 0 && !r.d.Away()) {
				continue
			}
			id, ok := r.newID()
			if !ok {
				delete(r.tickets, rid)
				continue
			}
			texts, why := RenderCard(t.req, id)
			if why != "" {
				// 放不下或不完整：不开票，只告诉主人回本机处理，一次
				delete(r.tickets, rid)
				bb := b
				msg := fmt.Sprintf("有一项操作等你批准（%s），但%s，没法在这里批，请回电脑上处理。", clipTitle(t.req), why)
				says = append(says, func() { r.say(bb, msg) })
				continue
			}
			t.id, t.b, t.state, t.sentAt = id, b, stSending, now
			r.byID[id] = t // 占住编号；发到了才算开着，发不到会收回
			jobs = append(jobs, job{t, b, texts})
		case stDeciding:
			if now.Sub(t.decidingAt) > decideLimit {
				bb, text := t.b, fmt.Sprintf("编号 %s：回复已收到，但一直没有结算结果，不知道有没有生效，请到本机确认。", t.id)
				r.closeLocked(t, "结算无回音")
				says = append(says, func() { r.say(bb, text) })
			}
		case stOpen:
			if !ok {
				r.closeLocked(t, "已关闭")
				continue
			}
			if !now.Before(t.deadline) {
				r.lastEnd = now
				bb, text := t.b, fmt.Sprintf("编号 %s：QQ 这边已经过期了，操作没有执行；本机上还能批的话，请在本机处理。", t.id)
				if now.UnixMilli() >= t.req.ExpiresAt {
					text = fmt.Sprintf("编号 %s：这一单已过期，操作没有执行。", t.id)
				}
				r.closeLocked(t, "已过期")
				says = append(says, func() { r.say(bb, text) })
			}
		}
	}
	r.mu.Unlock()
	for _, s := range says {
		s()
	}
	for _, j := range jobs {
		r.deliver(ctx, j.t, j.b, j.texts)
	}
}

func (r *Relay) deliver(ctx context.Context, t *ticket, b Binding, texts []string) {
	del, err := r.d.Send(ctx, b, texts)
	now := r.d.Now()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if t.endedWhileSending != nil {
		d := *t.endedWhileSending
		if d.Via != "qq" {
			r.lastEnd = now
		}
		t.state, t.msgIDs = stOpen, del.MessageIDs
		r.closeLocked(t, endReason(d))
		text := ""
		if err == nil && del.Confirmed {
			text = localEnded(t, d)
		}
		r.mu.Unlock()
		if text != "" {
			r.say(b, text)
		}
		return
	}
	if err != nil || !del.Confirmed {
		if del.Attempted {
			// 可能有几段已到主人手机：编号留墓碑、记一次结局，主人回这个编号得到“没有这一单”，裸“同意”不会转到别的单
			t.msgIDs = del.MessageIDs
			r.lastEnd = now
			r.closeLocked(t, "没确认送达")
		} else {
			delete(r.byID, t.id)
			delete(r.tickets, t.req.ID)
		}
		r.mu.Unlock()
		r.d.Logf("审批 %s 没能确认发到主人 QQ 私聊（%v），留在本机", t.req.ID, err)
		return
	}
	t.state, t.delivered, t.msgIDs = stOpen, now, del.MessageIDs
	t.deadline = now.Add(RemoteTTL)
	if exp := time.UnixMilli(t.req.ExpiresAt); exp.Before(t.deadline) {
		t.deadline = exp
	}
	r.mu.Unlock()
}

// say 异步发一句话给主人；失败只记日志，不影响任何一张票的状态。
func (r *Relay) say(b Binding, text string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		if _, err := r.d.Send(r.ctx, b, []string{text}); err != nil {
			r.d.Logf("给主人 QQ 私聊回话没发出去：%v", err)
		}
	}()
}

// Control 在入库后、进拦截链和闸门之前被调：返回 true 表示这条是对审批的回答（或明确的提示场合），到此为止，不进模型。
// 返回 false 时这条消息照普通消息往下走，和没有这个功能时完全一样。
func (r *Relay) Control(in Inbound) bool {
	// 身份的每一道门：消息本身、自己的号、补录、陌生人、重复、主人（按号）。任何一道不过都不当决定。
	if !in.Message || in.Duplicate || in.Backfill || in.FromSelf || in.Stranger || !in.IsOwner {
		return false
	}
	if in.PlatformID == "" || in.PlatformID == "0" {
		return false
	}
	// 先解析（纯函数、不读配置）：绝大多数聊天不像决定，直接放行，不为它们读文件
	rep := ParseReply(in.Segs)
	if rep.Verdict == VerdictNone || rep.Verdict == VerdictMixed {
		return false
	}
	b, ok := r.d.Binding(in.Character)
	if !ok {
		r.closeFor(in.Character)
		return false
	}
	if in.Conv.Channel != "qq" || in.Conv.Scene != types.ScenePrivate || in.Conv != b.Conv || in.Account != b.Account || in.FromID != b.Owner || in.Conv.ID != b.Owner {
		return false
	}
	now := r.d.Now()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	open := r.openFor(b.Conv)
	recent := r.recentTombFor(b.Conv, now)
	// 带 4 位编号的同意/拒绝几乎不会是闲聊：没有单、没有墓碑也截走并说明，不让它进模型被盲回（编号来自卡片，卡片不在模型历史里）
	if len(open) == 0 && !recent && rep.ID == "" {
		r.mu.Unlock()
		return false // 没有单、最近也没有单：“同意”只是一句普通的话
	}
	key := in.Conv.String() + "|" + in.PlatformID
	if r.seenMsgs[key] {
		r.mu.Unlock()
		return true // 同一条消息重推：已经处理过，不再结算也不再往下走
	}
	r.seenMsgs[key] = true
	r.seenSeq = append(r.seenSeq, key)
	if len(r.seenSeq) > maxSeenMsgs {
		delete(r.seenMsgs, r.seenSeq[0])
		r.seenSeq = r.seenSeq[1:]
	}
	switch rep.Verdict {
	case VerdictWeak:
		if len(open) == 0 {
			r.mu.Unlock()
			return false
		}
		r.mu.Unlock()
		r.say(b, "请明确回复“同意”或“拒绝”（有几单在等时带上编号），单子继续等着。")
		return true
	case VerdictInvisible:
		r.mu.Unlock()
		r.say(b, "你的回复里有看不见的字符，没有算数。请重新发一遍纯文字的“同意”或“拒绝”。")
		return true
	}
	t, reply := r.selectLocked(rep, open, b.Conv, now, in.Time)
	if t == nil {
		r.mu.Unlock()
		r.say(b, reply)
		return true
	}
	if !now.Before(t.deadline) {
		r.lastEnd = now
		id := t.id
		r.closeLocked(t, "已过期")
		r.mu.Unlock()
		r.say(b, fmt.Sprintf("编号 %s：QQ 这边已经过期了，没有处理。", id))
		return true
	}
	choice := "once"
	if rep.Verdict == VerdictDeny {
		choice = "deny"
	}
	t.state, t.choice, t.decidingAt = stDeciding, choice, now
	r.mu.Unlock()

	done, err := r.d.Approver.DecideVia(t.req.ID, choice, "qq")
	r.mu.Lock()
	if !done && t.state == stDeciding {
		t.state = stOpen
	}
	switch {
	case err != nil:
		r.mu.Unlock()
		r.d.Logf("QQ 回复结算失败：%v", err)
		r.say(b, fmt.Sprintf("编号 %s：没能结算（%s），请回本机处理。", t.id, "内部出错"))
	case !done:
		// 本机抢先了、到期了或被取消了：认领没成功，说明原因，票收成墓碑（已被结局回调收掉的不重复收）
		r.lastEnd = now
		id := t.id
		if r.tickets[t.req.ID] == t {
			r.closeLocked(t, "本机已处理")
		}
		r.mu.Unlock()
		r.say(b, fmt.Sprintf("编号 %s：这一单已经有结果了（本机先处理了，或已过期、取消），你的回复没有再执行。", id))
	default:
		// 认领成功只说明这一单归 QQ 了，不等于生效；确认话由结局回调（onResolved）按真实结局说，票也在那里收
		r.mu.Unlock()
	}
	return true
}

func (r *Relay) openFor(conv types.Conversation) []*ticket {
	var out []*ticket
	for _, t := range r.byID {
		if t.state == stOpen && t.b.Conv == conv {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].delivered.Before(out[j].delivered) || out[i].delivered.Equal(out[j].delivered) && out[i].id < out[j].id
	})
	return out
}

func (r *Relay) recentTombFor(conv types.Conversation, now time.Time) bool {
	for _, tb := range r.tombs {
		if tb.conv == conv && now.Sub(tb.at) < ambiguousWindow {
			return true
		}
	}
	return false
}

// selectLocked 决定这条回复结算哪一单。选不出来就返回 nil 和要回给主人的话；不猜、不取最新、不取最老。
func (r *Relay) selectLocked(rep Reply, open []*ticket, conv types.Conversation, now time.Time, at time.Time) (*ticket, string) {
	var byID, byQuote *ticket
	var tombID, tombQuote *tomb
	if rep.ID != "" {
		byID = r.byID[rep.ID]
		if byID != nil && byID.b.Conv == conv && rep.Quote == "" {
			switch byID.state {
			case stSending:
				return nil, fmt.Sprintf("编号 %s：这张卡还在发，发完请再回一次，这次没有处理。", byID.id)
			case stDeciding:
				return nil, fmt.Sprintf("编号 %s：已经收到回复，正在结算，不用再回了。", byID.id)
			}
		}
		if byID != nil && (byID.state != stOpen || byID.b.Conv != conv) {
			byID = nil
		}
		if tb := r.tombs[rep.ID]; byID == nil && tb != nil && tb.conv == conv {
			tombID = tb
		}
	}
	if rep.Quote != "" {
		for _, t := range open {
			if slices.Contains(t.msgIDs, rep.Quote) {
				byQuote = t
			}
		}
		if byQuote == nil {
			for _, tb := range r.tombs {
				if tb.conv == conv && slices.Contains(tb.msgIDs, rep.Quote) {
					tombQuote = tb
				}
			}
		}
	}
	switch {
	case rep.ID != "" && rep.Quote != "":
		if byID != nil && byID == byQuote {
			return byID, ""
		}
		return nil, "编号和你引用的不是同一单，没有处理。请只写一个：回复“同意 编号”，或直接引用那条审批消息回复“同意”。"
	case rep.ID != "":
		if byID != nil {
			return byID, ""
		}
		if tombID != nil {
			return nil, tombText(tombID)
		}
		return nil, "没有编号 " + rep.ID + " 的待批操作，没有处理。"
	case rep.Quote != "":
		if byQuote != nil {
			return byQuote, ""
		}
		if tombQuote != nil {
			return nil, tombText(tombQuote)
		}
		return nil, "你引用的不是审批消息，没有处理。"
	}
	switch {
	case len(open) == 0:
		// 只有最近结束的单：不把迟到的“同意”转到别的单上
		return nil, "刚才那一单已经结束了，你的回复没有处理。"
	case len(open) > 1:
		return nil, listOpen(open)
	}
	t := open[0]
	if now.Sub(r.startedAt) < ambiguousWindow || now.Sub(r.lastEnd) < ambiguousWindow && !r.lastEnd.IsZero() ||
		now.Sub(r.reconnectAt) < ambiguousWindow && !r.reconnectAt.IsZero() || r.recentTombLocked(conv, now) {
		return nil, fmt.Sprintf("为了防止批错单，请带上编号回复：“同意 %s”或“拒绝 %s”。", t.id, t.id)
	}
	// 平台时间比这张卡开始发送还早：是断线期间说的话在重连后才推来，不可能是在回这张卡
	if !at.IsZero() && at.Before(t.sentAt.Truncate(time.Second)) {
		return nil, fmt.Sprintf("这条回复比这张卡发出的时间还早，没有处理。请带上编号回复：“同意 %s”或“拒绝 %s”。", t.id, t.id)
	}
	if now.Sub(t.delivered) < minRead {
		return nil, fmt.Sprintf("才刚发出，请带上编号回复：“同意 %s”或“拒绝 %s”。", t.id, t.id)
	}
	return t, ""
}

// recentTombLocked：最近 10 分钟内是否有（不是这位主人刚刚用 QQ 结算的）单结束，防迟到回复转到新单。
func (r *Relay) recentTombLocked(conv types.Conversation, now time.Time) bool {
	for _, tb := range r.tombs {
		if tb.conv == conv && now.Sub(tb.at) < ambiguousWindow && !strings.HasPrefix(tb.reason, "QQ已") {
			return true
		}
	}
	return false
}

func tombText(tb *tomb) string {
	return fmt.Sprintf("编号 %s：这一单已经结束（%s），你的回复没有再处理。", tb.id, tb.reason)
}

func listOpen(open []*ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "现在有 %d 单在等，没法猜是哪一单。请回复“同意 编号”或“拒绝 编号”：", len(open))
	for _, t := range open {
		fmt.Fprintf(&b, "\n%s  %s", t.id, clipTitle(t.req))
	}
	return b.String()
}

func clipTitle(req approval.Request) string {
	s := escapeInvisible(strings.TrimSpace(req.Title))
	if s == "" {
		s = req.Tool
	}
	if rs := []rune(s); len(rs) > 40 {
		s = string(rs[:40]) + "…"
	}
	return s
}

// closeFor 把这个角色的票全收掉（配置关了、绑定变了、读不到）：不发任何帧，本机的卡不受影响。
func (r *Relay) closeFor(character string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tickets {
		if t.req.CharacterID == character && t.state != stSending {
			r.closeLocked(t, "已关闭")
		}
	}
}

// RenderCard 把审批卡写成给主人 QQ 私聊看的几段文字，编号和说明在最后一段也有。
// 第二个返回值非空表示不能在 QQ 批（预览被截短、放不下），文字不给。
func RenderCard(req approval.Request, id string) ([]string, string) {
	if req.Preview != nil && req.Preview.Truncated || req.SubjectTruncated {
		return nil, "内容太长、被截短了，看不全"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【待批准 %s】%s\n角色：%s  工具：%s\n", id, escapeInvisible(req.Title), escapeInvisible(req.CharacterID), escapeInvisible(req.Tool))
	if req.Purpose != "" {
		fmt.Fprintf(&b, "目的：%s\n", escapeInvisible(req.Purpose))
	}
	if req.Why != "" {
		fmt.Fprintf(&b, "为什么要问：%s\n", escapeInvisible(req.Why))
	}
	for _, n := range req.Notes {
		fmt.Fprintf(&b, "注意：%s\n", escapeInvisible(n))
	}
	b.WriteString("内容：\n")
	b.WriteString(escapeInvisible(req.Subject))
	if req.Preview != nil && req.Preview.Text != "" {
		b.WriteString("\n预览：\n")
		b.WriteString(escapeInvisible(req.Preview.Text))
	}
	body := b.String()
	var segs []string
	rs := []rune(body)
	for len(rs) > 0 {
		n := min(len(rs), segmentRunes)
		segs = append(segs, string(rs[:n]))
		rs = rs[n:]
	}
	segs = append(segs, fmt.Sprintf("编号 %s：回复“同意 %s”批准，回复“拒绝 %s”不执行。只对这一单有效，5 分钟内有效。", id, id, id))
	if len(segs) > maxSegments {
		return nil, "内容太长，一条消息放不下"
	}
	return segs, ""
}
