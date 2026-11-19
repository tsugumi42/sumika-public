// Package proactive 是主动联系的"导演"（规格-主动联系 第七节）：只决定什么时候醒，不调模型。
// 输入全是代码能算的信号（上次接触、主人活跃、睡觉时段、今天次数、别烦），放行才叫模型，不开口也留日志。
//
// 分两层：director.go 是纯函数（Decide、Interval、OnClose、睡觉时段），随机数和时间都由调用方给；
// scheduler.go 是轮询到点、存状态、写日志的壳，模型怎么叫留给第 2 步（Waker 接口）。
// 写法照 internal/loop：Options 注入，不 import 回合内核。
package proactive

import (
	"time"

	"sumika-native/internal/config"
)

// Kind 是日志行的种类
type Kind string

const (
	KindWake     Kind = "wake"     // 放行，叫（或本该叫）模型想一想
	KindFollowUp Kind = "followup" // 线开着、发话后主人始终没回，续醒
	KindSkip     Kind = "skip"     // 到点被拦下，或没醒到
	KindClose    Kind = "close"    // 收线
)

// 日志里的原因码（规格第七节"日志"）。规格原列表以外新增两个：due-asleep-active、missed-offline，规格同一节已补。
// 第 2 步起模型和线再往里加：model-quiet、model-spoke、send-*、think-only 等。
const (
	ReasonDue             = "due"                // 放行（第 1 步没有模型：只写"本该醒"）
	ReasonDueAsleepActive = "due-asleep-active"  // 放行，而且当时在睡觉时段内但主人在活跃（软信号让路）
	ReasonSnoozed         = "snoozed"            // 主人说过别烦，quiet_until 没到
	ReasonDailyCap        = "daily-cap"          // 今天已找满
	ReasonAlreadyOpen     = "already-open"       // 已有开着的线，没到续醒时间
	ReasonOwnerChatting   = "owner-chatting"     // 主人正在聊，先不打扰
	ReasonAsleepNoActive  = "asleep-no-activity" // 睡觉时段内且主人没有活跃迹象
	ReasonColdStreak      = "cold-streak"        // 连续冷了几次，当天不再找
	ReasonMissedOffline   = "missed-offline"     // 到点时进程没开，启动时补记，不补跑
	ReasonError           = "error"              // 导演自己出错（取信号失败等）
)

// Heat 是一条线关闭时主人回得热不热（第七节输入表）
type Heat string

const (
	HeatNone Heat = ""     // 没有线可比较
	HeatHot  Heat = "hot"  // 主人 10 分钟内回且聊了至少 2 轮
	HeatWarm Heat = "warm" // 3 小时内回
	HeatCold Heat = "cold" // 线关时主人始终没回
)

// State 是一个角色的导演状态（proactive_state 一行）
type State struct {
	Character  string
	Factor     float64   // 间隔系数，起步 1
	QuietUntil time.Time // 主人说别烦以后到这个时间前不找；零值没有
	ColdStreak int       // 连续冷了几次
	NextWake   time.Time // 下次该醒的时间；零值还没排过
	LastWake   time.Time // 上次醒；零值没醒过
}

// Snapshot 是每次到点取的一份信号。零值的时间都表示"没有 / 不知道"
type Snapshot struct {
	LastContact      time.Time // 上次找到主人（线里真发过话）的时间
	LastHeat         Heat      // 上一条线关闭时的热度（只用来记日志）
	ThreadsToday     int       // 今天开线并真发过话的次数（不含 unknown 以外的失败）
	OpenThread       bool      // 有开着的线
	FollowUpDue      bool      // 开着的线到了续醒时间
	OwnerLastActive  time.Time // 任一平台库里主人最后一次发言（含本地私聊）
	LocalLastInput   time.Time // 本地窗口里主人最后一次输入
	WindowForeground bool      // 本地窗口显示着且有焦点
}

// Decision 是 Decide 的结论
type Decision struct {
	Kind   Kind
	Reason string
	NextAt time.Time // 下次再看的时间
}

// Passes 是不是放行（叫模型）
func (d Decision) Passes() bool { return d.Kind == KindWake || d.Kind == KindFollowUp }

// Rand 返回 [0,1) 的随机数；测试里换成固定的
type Rand func() float64

func (r Rand) f() float64 {
	if r == nil {
		return 0
	}
	v := r()
	if v < 0 {
		return 0
	}
	if v >= 1 {
		return 0.999999
	}
	return v
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }

func jitter(r Rand, max time.Duration) time.Duration { return time.Duration(r.f() * float64(max)) }

// Interval 是下次醒前要等多久：基准在 baseMin～baseMax 分钟里均匀随机，乘系数，夹在 minGap～maxGap 之间
func Interval(p config.Proactive, factor float64, r Rand) time.Duration {
	if factor <= 0 {
		factor = 1
	}
	base := float64(p.BaseMinMinutes) + r.f()*float64(p.BaseMaxMinutes-p.BaseMinMinutes)
	d := time.Duration(base * factor * float64(time.Minute))
	if lo := minutes(p.MinGapMinutes); d < lo {
		d = lo
	}
	if hi := time.Duration(p.MaxGapHours) * time.Hour; d > hi {
		d = hi
	}
	return d
}

// clockMinutes 是 t 在 t 自己时区里从 0 点起的分钟数
func clockMinutes(t time.Time) int { return t.Hour()*60 + t.Minute() }

// sleepRange 取配置里的睡觉区间（分钟数）；写错的配置在载入时已经拒了，这里解析失败退回默认的 01:00～09:00
func sleepRange(p config.Proactive) (start, end int) {
	s, e1 := config.ParseClock(p.Sleep.Start)
	e, e2 := config.ParseClock(p.Sleep.End)
	if e1 != nil || e2 != nil || s == e {
		return 60, 9 * 60
	}
	return s, e
}

// InSleep 现在是不是在默认睡觉时段内（按 now 自己的时区；区间可以跨午夜）
func InSleep(p config.Proactive, now time.Time) bool {
	s, e := sleepRange(p)
	m := clockMinutes(now)
	if s < e {
		return m >= s && m < e
	}
	return m >= s || m < e
}

// SleepEnd 是 now 之后最近一次睡觉时段结束的时刻（now 在时段内就是这一段的结束）
func SleepEnd(p config.Proactive, now time.Time) time.Time {
	_, e := sleepRange(p)
	t := time.Date(now.Year(), now.Month(), now.Day(), e/60, e%60, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(now.Year(), now.Month(), now.Day()+1, e/60, e%60, 0, 0, now.Location())
	}
	return t
}

// NextMidnight 是 now 所在本地日的下一个 0 点
func NextMidnight(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

func sameDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	b = b.In(a.Location())
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// maxColdStreak 是连续冷几次以后当天不再找（规格第七节第 2 条）
const maxColdStreak = 3

func within(t, now time.Time, d time.Duration) bool {
	return !t.IsZero() && !t.After(now) && now.Sub(t) <= d
}

// Active 主人现在有没有活跃迹象：activeWithin 内在任一平台发过言或在本地窗口输入过，或本地窗口正在前台
func Active(p config.Proactive, sg Snapshot, now time.Time) bool {
	d := minutes(p.ActiveWithinMinutes)
	return sg.WindowForeground || within(sg.OwnerLastActive, now, d) || within(sg.LocalLastInput, now, d)
}

// Chatting 主人是不是正在聊：任一平台 chattingWithin 内发过言，或本地窗口在前台且本地 chattingWithin 内有输入
func Chatting(p config.Proactive, sg Snapshot, now time.Time) bool {
	d := minutes(p.ChattingWithinMinutes)
	return within(sg.OwnerLastActive, now, d) || (sg.WindowForeground && within(sg.LocalLastInput, now, d))
}

// Decide 是到点以后的代码检查（规格第七节第 4、6 条），按顺序，第一道拦下的就是结论：
// 别烦、连冷、今天上限、已有开着的线、主人正在聊、睡觉软信号，都过了才放行。
// 续醒（线开着且到了续醒时间）只过别烦、正在聊、睡觉软信号，被拦一律顺延 deferMinutes；
// 新线再多两道：连冷和今天上限。关着、没列的角色不会走到这里（调度壳不为它们写日志）。
func Decide(p config.Proactive, st State, sg Snapshot, now time.Time, r Rand) Decision {
	followUp := sg.OpenThread && sg.FollowUpDue
	deferTo := now.Add(minutes(p.DeferMinutes))
	skip := func(reason string, next time.Time) Decision {
		if followUp {
			next = deferTo
		}
		return Decision{Kind: KindSkip, Reason: reason, NextAt: next}
	}

	if st.QuietUntil.After(now) {
		return skip(ReasonSnoozed, st.QuietUntil.Add(jitter(r, 30*time.Minute)))
	}
	if !followUp {
		if st.ColdStreak >= maxColdStreak && sameDay(st.LastWake, now) {
			return skip(ReasonColdStreak, NextMidnight(now).Add(jitter(r, 60*time.Minute)))
		}
		if sg.ThreadsToday >= p.MaxThreadsPerDay {
			return skip(ReasonDailyCap, NextMidnight(now).Add(jitter(r, 60*time.Minute)))
		}
		if sg.OpenThread {
			return skip(ReasonAlreadyOpen, deferTo)
		}
	}
	if Chatting(p, sg, now) {
		return skip(ReasonOwnerChatting, deferTo)
	}
	reason := ReasonDue
	if InSleep(p, now) {
		if !Active(p, sg, now) {
			return skip(ReasonAsleepNoActive, SleepEnd(p, now).Add(jitter(r, 60*time.Minute)))
		}
		reason = ReasonDueAsleepActive
	}
	d := Decision{Kind: KindWake, Reason: reason, NextAt: now.Add(Interval(p, st.Factor, r))}
	if followUp {
		d.Kind = KindFollowUp
	}
	return d
}

// CloseKind 是一条线关闭时导演要知道的结果（第七节第 2 条）
type CloseKind int

const (
	CloseHot     CloseKind = iota // 热
	CloseWarm                     // 温
	CloseCold                     // 冷：始终没回
	CloseQuiet                    // 想了没开口：不改系数，不算冷
	CloseSnoozed                  // 主人说别烦：不改系数，另设 quiet_until
)

// OnClose 线关闭时更新系数和连冷次数（第 2 步的线会调它；第 1 步没有线，只有测试用）：
// 热 factor×heatFactor 不低于 factorMin；温向 1 靠拢 30%；冷 factor×coldFactor 不高于 factorMax，连冷加一；
// 热和温把连冷清零；想了没开口、别烦都不动。
func OnClose(p config.Proactive, st State, k CloseKind) State {
	if st.Factor <= 0 {
		st.Factor = 1
	}
	switch k {
	case CloseHot:
		st.Factor = max(p.FactorMin, st.Factor*p.HeatFactor)
		st.ColdStreak = 0
	case CloseWarm:
		st.Factor += (1 - st.Factor) * 0.3
		st.ColdStreak = 0
	case CloseCold:
		st.Factor = min(p.FactorMax, st.Factor*p.ColdFactor)
		st.ColdStreak++
	}
	return st
}

// Snooze 主人说别烦：到 until 前不找，系数不动
func Snooze(st State, until time.Time) State {
	st.QuietUntil = until
	return st
}

// Inputs 是写进日志 inputs 列的快照：只有数字和布尔，不放会话名和文字。-1 表示没有或不知道
type Inputs struct {
	SinceContactMin       int     `json:"sinceContactMin"`
	Heat                  string  `json:"heat,omitempty"`
	Factor                float64 `json:"factor"`
	ColdStreak            int     `json:"coldStreak"`
	ThreadsToday          int     `json:"threadsToday"`
	OpenThread            bool    `json:"openThread"`
	InSleep               bool    `json:"inSleep"`
	OwnerLastActiveAgoMin int     `json:"ownerLastActiveAgoMin"`
	LocalLastInputAgoMin  int     `json:"localLastInputAgoMin"`
	WindowForeground      bool    `json:"windowForeground"`
	MissedMin             int     `json:"missedMin,omitempty"` // 只有 missed-offline：晚了多少分钟
}

func agoMin(t, now time.Time) int {
	if t.IsZero() {
		return -1
	}
	d := now.Sub(t)
	if d < 0 {
		return 0
	}
	return int(d / time.Minute)
}

// SnapshotInputs 把当时的状态和信号变成 Inputs
func SnapshotInputs(p config.Proactive, st State, sg Snapshot, now time.Time) Inputs {
	return Inputs{
		SinceContactMin: agoMin(sg.LastContact, now), Heat: string(sg.LastHeat), Factor: st.Factor, ColdStreak: st.ColdStreak,
		ThreadsToday: sg.ThreadsToday, OpenThread: sg.OpenThread, InSleep: InSleep(p, now),
		OwnerLastActiveAgoMin: agoMin(sg.OwnerLastActive, now), LocalLastInputAgoMin: agoMin(sg.LocalLastInput, now),
		WindowForeground: sg.WindowForeground,
	}
}
