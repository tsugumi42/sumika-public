// Package remind 是提醒的后台核心（规格-提醒）：提醒表、周期算法、调度器、工具声明和参数解析。
//
// 本包不 import 回合内核和渠道：到点怎么说、发到哪由宿主以函数注入（Options.Ring），所以能脱离界面和模型单独测试。
// 谁能调这些工具也由宿主判（app/remindwire.go），本包只管"这个来源能不能这么改"。
package remind

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Status 是提醒的状态
type Status string

const (
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusDone      Status = "done"      // 响完了，或被标成完成
	StatusCancelled Status = "cancelled" // 被取消
)

// Ended 说这个状态是不是已经结束（不再响）
func (s Status) Ended() bool { return s == StatusDone || s == StatusCancelled }

// Source 是这条提醒是谁建的
type Source string

const (
	SourceOwner  Source = "owner"  // 主人在界面上建的
	SourceChat   Source = "chat"   // 她在和主人说话时建的
	SourceThread Source = "thread" // 她在主动联系的线里自己建的
)

// 投递目标。只有这两个，都指向主人（规格第十一节第 5 条）
const (
	TargetLocal = "local"
	TargetQQ    = "qq"
)

// DeliveryState 是一次响铃在一个目标上的结果
type DeliveryState string

const (
	DeliverySending DeliveryState = "sending" // 已落盘、正在发：重启时看到它记成 unknown，不重发
	DeliverySent    DeliveryState = "sent"
	DeliveryFailed  DeliveryState = "failed"
	DeliveryUnknown DeliveryState = "unknown" // 不知道发没发出去，不重发
	DeliveryDryRun  DeliveryState = "dry-run" // 钳制成试运行：写了库没发出去
)

const (
	// TextMax 是提醒内容的字数上限
	TextMax = 500
	// LateAfter 是过了点多久才算“晚了”、要在句尾说明
	LateAfter = 2 * time.Minute
	// MaxAhead 是最远能定到多久以后
	MaxAhead = 10 * 365 * 24 * time.Hour
	// OwnerMinInterval 是主人自己建周期提醒的底线（和 /loop 一样）
	OwnerMinInterval = time.Minute
	// DefaultMaxActive、DefaultMinIntervalMinutes、DefaultThreadMaxActive 是配置的默认值
	DefaultMaxActive          = 20
	DefaultMinIntervalMinutes = 60
	DefaultThreadMaxActive    = 3

	historyKeep = 20
	endedKeep   = 30 * 24 * time.Hour
	// maxCountedSlots：数错过了几次时最多往后数这么多（一分钟一次的提醒关了一个月也不会一直数）
	maxCountedSlots = 1000
)

// Pending 是正在响的这一次；落盘后崩溃也知道这一次还没收尾（规格第六节）
type Pending struct {
	At         time.Time                `json:"at"` // 这一次本该响的时间
	Missed     int                      `json:"missed,omitempty"`
	Deliveries map[string]DeliveryState `json:"deliveries,omitempty"`
}

// Entry 是记录里的一笔
type Entry struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // create、edit、pause、resume、ring、done、cancel、defer
	By   Source    `json:"by,omitempty"`
	Note string    `json:"note,omitempty"`
}

// Reminder 是提醒表里的一行
type Reminder struct {
	ID        int       `json:"id"`
	Character string    `json:"character"`
	Conv      string    `json:"conv"` // 本地那一路写到哪个会话：console:<角色>，或建它的自检话题
	Text      string    `json:"text"`
	At        time.Time `json:"at"` // 第一次响的时间；周期的墙钟时刻、星期、几号都从它取
	Repeat    Repeat    `json:"repeat,omitzero"`
	NextAt    time.Time `json:"nextAt"`
	Source    Source    `json:"source"`
	Targets   []string  `json:"targets,omitempty"` // 空表示跟配置
	Status    Status    `json:"status"`
	EndNote   string    `json:"endNote,omitempty"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	Created   time.Time `json:"created"`
	Rings     int       `json:"rings,omitempty"`
	LastRing  time.Time `json:"lastRing,omitzero"`
	LastNote  string    `json:"lastNote,omitempty"`
	Pending   *Pending  `json:"pending,omitempty"`
	History   []Entry   `json:"history,omitempty"`
}

func (r Reminder) clone() Reminder {
	r.Targets = append([]string(nil), r.Targets...)
	r.Repeat.Weekdays = append([]int(nil), r.Repeat.Weekdays...)
	r.History = append([]Entry(nil), r.History...)
	if r.Pending != nil {
		p := *r.Pending
		if p.Deliveries != nil {
			m := make(map[string]DeliveryState, len(p.Deliveries))
			for k, v := range p.Deliveries {
				m[k] = v
			}
			p.Deliveries = m
		}
		r.Pending = &p
	}
	return r
}

func (r *Reminder) note(e Entry) {
	r.History = append(r.History, e)
	if len(r.History) > historyKeep {
		r.History = append([]Entry(nil), r.History[len(r.History)-historyKeep:]...)
	}
}

// Key 是这条提醒这一次响铃的幂等键：写进事件 segments，重启后据它认出已经送达的（规格第六节）
func Key(id int, at time.Time) string { return fmt.Sprintf("%d:%d", id, at.UnixMilli()) }

// SourceText 是界面和列表里写的来源
func (s Source) Text() string {
	switch s {
	case SourceOwner:
		return "主人建的"
	case SourceChat:
		return "她建的"
	case SourceThread:
		return "她主动建的"
	}
	return string(s)
}

// StatusText 是状态的说法
func (s Status) Text() string {
	switch s {
	case StatusActive:
		return "进行中"
	case StatusPaused:
		return "已暂停"
	case StatusDone:
		return "已完成"
	case StatusCancelled:
		return "已取消"
	}
	return string(s)
}

// TargetsText 把目标写成人话；空的由调用方解析成配置的默认值后再传
func TargetsText(targets []string) string {
	var parts []string
	for _, t := range targets {
		switch t {
		case TargetLocal:
			parts = append(parts, "本地窗口")
		case TargetQQ:
			parts = append(parts, "QQ 私聊")
		}
	}
	return strings.Join(parts, "和")
}

// Line 是给 reminder_list 看的一行
func (r Reminder) Line(now time.Time, defaults []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s · %s · %s · 发到%s", r.ID, clip(r.Text, 40), r.whenText(now), r.Source.Text(), TargetsText(r.TargetsOr(defaults)))
	if r.Repeat.None() {
		b.WriteString(" · 一次性")
	} else {
		b.WriteString(" · " + r.Repeat.Text())
	}
	return b.String()
}

func (r Reminder) whenText(now time.Time) string {
	switch r.Status {
	case StatusActive:
		return "下次 " + TimeText(r.NextAt, now)
	case StatusPaused:
		return "已暂停"
	}
	return r.Status.Text()
}

// TargetsOr 是这条提醒实际的目标：自己写了用自己的，没写用给的默认值
func (r Reminder) TargetsOr(defaults []string) []string {
	if len(r.Targets) > 0 {
		return append([]string(nil), r.Targets...)
	}
	return append([]string(nil), defaults...)
}

var weekdayZh = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// TimeText 写成“2026-11-10 周二 09:00”，按 now 的时区
func TimeText(t, now time.Time) string {
	t = t.In(now.Location())
	return t.Format("2006-01-02 ") + weekdayZh[t.Weekday()] + t.Format(" 15:04")
}

// ShortTime 写成“11-10 09:00”，今天只写时分
func ShortTime(t, now time.Time) string {
	t = t.In(now.Location())
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// DurationText 写成“3 小时 12 分”“5 分钟”“2 天 3 小时”
func DurationText(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Minute)
	switch {
	case d < time.Minute:
		return "不到 1 分钟"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	case d < 24*time.Hour:
		if m := int(d % time.Hour / time.Minute); m != 0 {
			return fmt.Sprintf("%d 小时 %d 分", int(d/time.Hour), m)
		}
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	}
	if h := int(d % (24 * time.Hour) / time.Hour); h != 0 {
		return fmt.Sprintf("%d 天 %d 小时", int(d/(24*time.Hour)), h)
	}
	return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
}

// LateNote 是晚了的那句说明（宿主补在句尾）；没晚返回空
func LateNote(scheduled, now time.Time, missed int) string {
	late := now.Sub(scheduled)
	if late < LateAfter && missed == 0 {
		return ""
	}
	s := "本该 " + ShortTime(scheduled, now) + " 提醒，晚了 " + DurationText(late)
	if missed > 0 {
		s += fmt.Sprintf("；这段时间一共错过 %d 次", missed+1)
	}
	return "（" + s + "）"
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Clip 给宿主用：折叠空白后限长
func Clip(s string, n int) string { return clip(s, n) }
