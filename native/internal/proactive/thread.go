package proactive

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/config"
)

// 话题线的状态机（规格-主动联系 第四节"生命周期"）：纯函数，时间由调用方给，不碰库、不起定时器。
// 超时类的事（续醒、没回收线、聊完收线、想太久）全由"现在几点"推出，调度壳每个 Tick 扫一遍；
// 所以每个没关的状态都有截止时间（NextCheck 必有值），"卡住的状态"不可达（规格第十六节第 3 条）。

// ThreadState 是线的状态
type ThreadState string

const (
	ThreadThinking ThreadState = "thinking" // 导演醒了，模型在线里想；发话后有模型在跑时（续醒）也回到这里
	ThreadAwaiting ThreadState = "awaiting" // 发出去了，没有模型在跑，不花 token
	ThreadChatting ThreadState = "chatting" // 主人回了，线在回他
	ThreadClosed   ThreadState = "closed"   // 终态
)

// CloseReason 是线关闭的原因（proactive_threads.close_reason）
type CloseReason string

const (
	ClosedQuiet   CloseReason = "quiet"    // 想了但没开口
	ClosedDone    CloseReason = "done"     // 聊完（或她自己收线）
	ClosedNoReply CloseReason = "no-reply" // 等到时限没回
	ClosedSnoozed CloseReason = "snoozed"  // 主人说别烦
	ClosedCap     CloseReason = "cap"      // 达到每线发送上限
	ClosedError   CloseReason = "error"    // 出错（含重启时没发过话、想太久）
)

// 写死的几个时间（规格没列配置键）：想一轮最多多久、"热"算多快、结论最长
const (
	thinkMaxMinutes = 30 // 线里一轮想的时间上限，过了按出错关；模型自己有总时限，这里是兜底
	hotReplyMinutes = 10 // 主人 10 分钟内回且聊了至少 2 轮才算热（第七节输入表）
	hotReplies      = 2
	ConclusionMax   = 200 // 结论最长多少字
)

// Thread 是 proactive_threads 的一行
type Thread struct {
	ID, Character, Conversation string
	State                       ThreadState
	OpenedAt                    time.Time
	OpenedReason                string // 第一次开口的理由，限 60 字
	FirstSendAt, LastSendAt     time.Time
	LastWakeAt                  time.Time // 最近一次叫醒（新线或续醒）
	AwaitingSince               time.Time
	FollowUps, Sends            int
	OwnerReplies                int
	FirstReplyAt                time.Time
	OwnerRepliedAt              time.Time
	ChatLastAt                  time.Time // 聊着时主人最后一句被认领的时间
	BlockedConvs                []string  // hand_back 以后本线内不再认领的会话
	ClosedAt                    time.Time
	CloseReason                 CloseReason
	Conclusion                  string
}

// Open 线还没关
func (t Thread) Open() bool { return t.State != ThreadClosed && t.State != "" }

// Blocked 这个会话在本线内是不是已经被 hand_back 拉黑
func (t Thread) Blocked(conv string) bool {
	for _, b := range t.BlockedConvs {
		if b == conv {
			return true
		}
	}
	return false
}

// EventKind 是能落到线上的事
type EventKind string

const (
	EvSent      EventKind = "sent"       // thread_send 成功交出（写进目标会话；结果不明也算）
	EvTurnEnd   EventKind = "turn-end"   // 线里一轮模型调用正常结束
	EvTurnError EventKind = "turn-error" // 线里一轮出错或被停
	EvWake      EventKind = "wake"       // 导演续醒
	EvClaim     EventKind = "claim"      // 主人的回复被线认领
	EvOverheard EventKind = "overheard"  // 旁听到主人回过（不认领）
	EvTimeout   EventKind = "timeout"    // 到了截止时间
	EvEnd       EventKind = "end"        // end_thread
	EvSnooze    EventKind = "snooze"     // 主人说别烦
	EvCap       EventKind = "cap"        // 发送上限用完还想发
	EvRestart   EventKind = "restart"    // 进程重启后恢复
)

// AllEvents 给测试穷举状态 × 事件用
var AllEvents = []EventKind{EvSent, EvTurnEnd, EvTurnError, EvWake, EvClaim, EvOverheard, EvTimeout, EvEnd, EvSnooze, EvCap, EvRestart}

// Event 是一次落到线上的事
type Event struct {
	Kind       EventKind
	At         time.Time
	Conclusion string // end 用：结论
}

var (
	// ErrNoop 这个事件在这个状态下无事可做，线没变（不是错）
	ErrNoop = errors.New("proactive：这个状态下这件事无事可做")
	// ErrImpossible 这个事件在这个状态下不该出现；线没变，调用方记日志
	ErrImpossible = errors.New("proactive：这个状态下不该出现这件事")
	// ErrClosed 线已经关了，后到的事一律拒绝（工具侧回 thread_closed）
	ErrClosed = errors.New("proactive：线已经关了")
)

// CloseAt 是没关的线最迟什么时候要收：想太久、没回等太久、聊完闲太久。没关的线必有值，关了的返回零值
func CloseAt(p config.Proactive, t Thread) time.Time {
	switch t.State {
	case ThreadThinking:
		base := t.LastWakeAt
		if base.IsZero() {
			base = t.OpenedAt
		}
		return base.Add(thinkMaxMinutes * time.Minute)
	case ThreadAwaiting:
		if t.OwnerReplies > 0 {
			return t.LastSendAt.Add(minutes(p.ChatIdleMinutes)) // 聊过又没下文：她回完以后闲够了就收
		}
		base := t.FirstSendAt
		if base.IsZero() {
			base = t.OpenedAt
		}
		return base.Add(time.Duration(p.CloseAfterHours) * time.Hour)
	case ThreadChatting:
		return t.ChatLastAt.Add(minutes(p.ChatIdleMinutes))
	}
	return time.Time{}
}

// FollowUpAt 是续醒的时间：发话后 followUpMinutes 主人始终没回过（含旁听到的），最多 maxFollowUps 次。没有续醒返回 false
func FollowUpAt(p config.Proactive, t Thread) (time.Time, bool) {
	if t.State != ThreadAwaiting || t.OwnerReplies > 0 || t.FollowUps >= p.MaxFollowUps || t.Sends == 0 {
		return time.Time{}, false
	}
	base := t.LastSendAt
	if t.LastWakeAt.After(base) {
		base = t.LastWakeAt
	}
	return base.Add(minutes(p.FollowUpMinutes)), true
}

// FollowUpDue 现在是不是到了续醒时间
func FollowUpDue(p config.Proactive, t Thread, now time.Time) bool {
	at, ok := FollowUpAt(p, t)
	return ok && !now.Before(at)
}

// NextCheck 是这条线下一次需要被看一眼的时间：续醒和截止里较早的。没关的线必有值
func NextCheck(p config.Proactive, t Thread) (time.Time, bool) {
	if !t.Open() {
		return time.Time{}, false
	}
	c := CloseAt(p, t)
	if f, ok := FollowUpAt(p, t); ok && f.Before(c) {
		c = f
	}
	return c, true
}

func (t *Thread) noteReply(at time.Time) {
	t.OwnerReplies++
	t.OwnerRepliedAt = at
	if t.FirstReplyAt.IsZero() {
		t.FirstReplyAt = at
	}
}

func (t *Thread) closeWith(r CloseReason, at time.Time, conclusion string) {
	t.State, t.CloseReason, t.ClosedAt = ThreadClosed, r, at
	t.Conclusion = clip(conclusion, ConclusionMax)
	t.AwaitingSince = time.Time{}
}

// Apply 是状态机：线 × 事件 → 新的线。返回 ErrNoop、ErrImpossible、ErrClosed 时线原样不变。
// 每一格（4 个状态 × AllEvents）的预期在 thread_test.go 的表里逐格写明
func Apply(p config.Proactive, t Thread, ev Event) (Thread, error) {
	at := ev.At
	switch t.State {
	case ThreadClosed:
		return t, ErrClosed
	case ThreadThinking, ThreadAwaiting, ThreadChatting:
	default:
		return t, ErrImpossible
	}
	switch ev.Kind {
	case EvSent:
		// 发话：想的时候发的第一句、等回复时接着发、聊着时回他，都进"等回复"（规格：thread_send sent 含主人回复后她又回了一轮）
		t.Sends++
		if t.FirstSendAt.IsZero() {
			t.FirstSendAt = at
		}
		t.LastSendAt, t.AwaitingSince, t.State = at, at, ThreadAwaiting
		return t, nil
	case EvTurnEnd:
		switch {
		case t.State == ThreadThinking && t.Sends == 0:
			t.closeWith(ClosedQuiet, at, "")
			return t, nil
		case t.State == ThreadThinking: // 续醒这一轮没再发话：回到等回复，截止不变
			t.State = ThreadAwaiting
			return t, nil
		}
		return t, ErrNoop
	case EvTurnError:
		if t.State == ThreadThinking {
			t.closeWith(ClosedError, at, "")
			return t, nil
		}
		return t, ErrNoop // 已经发过话、她在等或聊着：话到了，出错的只是后面那一轮，由认领的交回处理
	case EvWake:
		if t.State != ThreadAwaiting {
			return t, ErrImpossible // 导演只在等回复时续醒（想着、聊着时不会放行续醒）
		}
		if t.FollowUps >= p.MaxFollowUps {
			return t, ErrNoop
		}
		t.FollowUps++
		t.LastWakeAt, t.State = at, ThreadThinking
		return t, nil
	case EvClaim:
		if t.Sends == 0 {
			return t, ErrImpossible // 没发过话就没有可以回的东西
		}
		t.noteReply(at)
		t.ChatLastAt, t.State = at, ThreadChatting
		return t, nil
	case EvOverheard:
		if t.Sends == 0 {
			return t, ErrImpossible
		}
		t.noteReply(at)
		return t, nil
	case EvTimeout:
		if due := CloseAt(p, t); at.Before(due) {
			return t, ErrNoop
		}
		switch t.State {
		case ThreadThinking:
			t.closeWith(ClosedError, at, "")
		case ThreadAwaiting:
			if t.OwnerReplies > 0 {
				t.closeWith(ClosedDone, at, "")
			} else {
				t.closeWith(ClosedNoReply, at, "")
			}
		default:
			t.closeWith(ClosedDone, at, "")
		}
		return t, nil
	case EvEnd:
		if t.Sends == 0 {
			t.closeWith(ClosedQuiet, at, ev.Conclusion)
		} else {
			t.closeWith(ClosedDone, at, ev.Conclusion)
		}
		return t, nil
	case EvSnooze:
		t.closeWith(ClosedSnoozed, at, ev.Conclusion)
		return t, nil
	case EvCap:
		if t.Sends == 0 {
			return t, ErrImpossible
		}
		t.closeWith(ClosedCap, at, ev.Conclusion)
		return t, nil
	case EvRestart:
		if t.State != ThreadThinking {
			return t, ErrNoop // 等回复、聊着照旧，计时用库里的时间戳重算
		}
		if t.Sends > 0 {
			t.State = ThreadAwaiting
			return t, nil
		}
		t.closeWith(ClosedError, at, "restart")
		return t, nil
	}
	return t, ErrImpossible
}

// HeatOf 线关闭时主人回得热不热 → 导演系数怎么变（第七节）：没开口过、想了没开口、出错、别烦都不动系数；
// 开过口的：没回是冷；10 分钟内回且至少聊了 2 轮是热；其余回过的是温
func HeatOf(t Thread) (CloseKind, Heat) {
	switch t.CloseReason {
	case ClosedSnoozed:
		return CloseSnoozed, HeatNone
	case ClosedQuiet, ClosedError:
		return CloseQuiet, HeatNone
	}
	if t.Sends == 0 {
		return CloseQuiet, HeatNone
	}
	if t.OwnerReplies == 0 {
		return CloseCold, HeatCold
	}
	if t.OwnerReplies >= hotReplies && !t.FirstSendAt.IsZero() && t.FirstReplyAt.Sub(t.FirstSendAt) <= hotReplyMinutes*time.Minute {
		return CloseHot, HeatHot
	}
	return CloseWarm, HeatWarm
}

// ClaimFacts 是判断"主人这句话归不归线"要的事实，由调用方从库里查好
type ClaimFacts struct {
	SentHere        bool // 线在这个会话发过话
	QuotedThreadMsg bool // 主人引用了线发出的那条消息
	ThreadMsgLatest bool // 线最后发的那条仍是这个会话最近一条角色发言（普通实例没有接过话）
}

// ClaimAllowed 是认领的代码判断（第五节第 2 条，本地私聊那一条；不调模型）。
// 线已在聊着，同一会话里主人接下来的话继续认领；否则要引用了线发的那条（不看时间），或在线最后一次发话后
// claimWindowMinutes 内、且线的那条话仍是该会话最近一条角色发言。被 hand_back 拉黑的会话本线内不再认领
func ClaimAllowed(p config.Proactive, t Thread, conv string, f ClaimFacts, now time.Time) bool {
	if !t.Open() || t.Sends == 0 || t.Blocked(conv) || !f.SentHere {
		return false
	}
	if t.State == ThreadChatting || f.QuotedThreadMsg {
		return true
	}
	return !now.Before(t.LastSendAt) && now.Sub(t.LastSendAt) <= minutes(p.ClaimWindowMinutes) && f.ThreadMsgLatest
}

// ThreadSnapshot 是日志和界面用的一句话状态（中文）
func ThreadSnapshot(t Thread, now time.Time) string {
	switch t.State {
	case ThreadThinking:
		return "正在想"
	case ThreadAwaiting:
		return "等回复 " + humanAgo(t.LastSendAt, now)
	case ThreadChatting:
		return "聊着"
	case ThreadClosed:
		return "已关：" + CloseReasonText(t.CloseReason)
	}
	return string(t.State)
}

// CloseReasonText 是关线原因的中文
func CloseReasonText(r CloseReason) string {
	switch r {
	case ClosedQuiet:
		return "想了没开口"
	case ClosedDone:
		return "聊完了"
	case ClosedNoReply:
		return "没等到回复"
	case ClosedSnoozed:
		return "主人说别烦"
	case ClosedCap:
		return "发满了上限"
	case ClosedError:
		return "出错"
	}
	return string(r)
}

func humanAgo(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "不到 1 分钟"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " 分钟"
	}
	return strconv.Itoa(int(d/time.Hour)) + " 小时 " + strconv.Itoa(int(d%time.Hour/time.Minute)) + " 分钟"
}

// ClipNote 截到 n 个字、去掉换行（理由、摘要、结论都过它）
func ClipNote(s string, n int) string { return clip(strings.TrimSpace(s), n) }
