// Package loop 是 /loop（定期任务）的后台核心：任务表、调度器、schedule_next 工具的后台逻辑、
// "[无事]"暗号识别、/loop 命令解析和列表文字（规格-目标和循环 第三节）。
//
// 本包不 import 回合内核和渠道：到点"在这个会话里发起一轮"、会话是否忙、会话是否有进行中的目标，
// 都由宿主以函数注入（Options），所以能脱离界面和模型单独测试。
package loop

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Status 是任务的状态
type Status string

const (
	StatusRunning Status = "running"
	StatusPaused  Status = "paused"
	StatusStopped Status = "stopped"
)

// ResultKind 是一轮的结果
type ResultKind string

const (
	ResultNone    ResultKind = ""        // 还没跑过
	ResultContent ResultKind = "content" // 有内容，要显示
	ResultQuiet   ResultKind = "quiet"   // 回了暗号 [无事]，不显示、不通知
	ResultError   ResultKind = "error"   // 出错，Note 写原因
)

// 通知档：有内容和出错都提醒（默认，和一直以来的行为一样）、只提醒出错、不提醒。[无事] 的轮任何档都不提醒
const (
	NotifyContent = "content"
	NotifyErrors  = "errors"
	NotifyNone    = "none"
)

// QuietToken 是"没什么要报告"的固定暗号：整条回答去掉空白后恰好是它才算
const QuietToken = "[无事]"

const (
	// DefaultTTL 是任务默认的存活时间
	DefaultTTL = 7 * 24 * time.Hour
	// MaxConsecutiveErrors 连续出错到这个次数就停
	MaxConsecutiveErrors = 3
	// MinInterval 是固定间隔的下限
	MinInterval = time.Minute
	// MaxInterval 是命令里能写的固定间隔上限
	MaxInterval = 365 * 24 * time.Hour
	// 自定节奏时 schedule_next 的秒数范围，以及她不调用时的默认间隔
	MinNextSeconds = 60
	MaxNextSeconds = 86400
	DefaultNext    = time.Hour

	historyKeep = 50
	stoppedKeep = 30 * 24 * time.Hour
)

// Entry 是运行历史里的一笔
type Entry struct {
	At     time.Time  `json:"at"`
	Result ResultKind `json:"result"`
	Note   string     `json:"note,omitempty"` // 出错原因、"错过了 X 次"
	Missed int        `json:"missed,omitempty"`
}

// Task 是任务表里的一行
type Task struct {
	ID      int    `json:"id"`
	Session string `json:"session"`
	Prompt  string `json:"prompt"`
	// Interval 为 0 且没有 At，表示不带间隔，由她每轮结束时用 schedule_next 自己定
	Interval time.Duration `json:"interval,omitempty"`
	// At、Days 是"每天 / 每周几 某时"的日程（本机时区，At 写 "15:04"，Days 0 是周日）；和 Interval 二选一。
	// 老文件没有这两项，按 Interval 推成固定间隔或自定节奏，所以文件版本不用升
	At   string `json:"at,omitempty"`
	Days []int  `json:"days,omitempty"`
	// Notify 是通知档（NotifyContent 等），空等于 NotifyContent；NewChat 为真时每一轮在新开的话题里跑
	Notify  string    `json:"notify,omitempty"`
	NewChat bool      `json:"newChat,omitempty"`
	NextRun time.Time `json:"nextRun"`
	// NextReason 是她上次定节奏时写的理由（只有自定节奏的任务有）
	NextReason   string     `json:"nextReason,omitempty"`
	Runs         int        `json:"runs"`
	LastRun      time.Time  `json:"lastRun,omitempty"`
	LastResult   ResultKind `json:"lastResult,omitempty"`
	LastNote     string     `json:"lastNote,omitempty"`
	ConsecErrors int        `json:"consecErrors,omitempty"`
	// Expires 为零值表示不停
	Expires    time.Time `json:"expires,omitempty"`
	Status     Status    `json:"status"`
	StopReason string    `json:"stopReason,omitempty"`
	StoppedAt  time.Time `json:"stoppedAt,omitempty"`
	Created    time.Time `json:"created"`
	// PendingMissed 是启动时算出的错过次数，下一轮的输入和历史里写明后清零
	PendingMissed int     `json:"pendingMissed,omitempty"`
	History       []Entry `json:"history,omitempty"`
}

// SelfPaced 是否由她自己定节奏
func (t Task) SelfPaced() bool { return t.Schedule().Kind() == KindSelf }

// Schedule 是任务的日程
func (t Task) Schedule() Schedule { return Schedule{Interval: t.Interval, At: t.At, Days: t.Days} }

// NotifyLevel 是生效的通知档（空按默认）
func (t Task) NotifyLevel() string {
	switch t.Notify {
	case NotifyErrors, NotifyNone:
		return t.Notify
	}
	return NotifyContent
}

// Line 是给界面和命令行用的"列表一行"，例如
// "#3 每 30 分钟 · 报一下天气 · 已跑 5 次 · 下次 14:30"。now 决定下次时间要不要带日期。
func (t Task) Line(now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s · %s · 已跑 %d 次", t.ID, t.cadence(), clip(t.Prompt, 20), t.Runs)
	switch t.Status {
	case StatusRunning:
		fmt.Fprintf(&b, " · 下次 %s", clockText(t.NextRun, now))
	case StatusPaused:
		b.WriteString(" · 已暂停")
	case StatusStopped:
		b.WriteString(" · 已停止")
		if t.StopReason != "" {
			b.WriteString("：" + t.StopReason)
		}
	}
	if t.Status != StatusStopped {
		if t.Expires.IsZero() {
			b.WriteString(" · 不自动停")
		} else {
			fmt.Fprintf(&b, " · %s 到期", clockText(t.Expires, now))
		}
	}
	if t.NewChat {
		b.WriteString(" · 每次新聊天")
	}
	switch t.NotifyLevel() {
	case NotifyErrors:
		b.WriteString(" · 只提醒出错")
	case NotifyNone:
		b.WriteString(" · 不提醒")
	}
	return b.String()
}

func (t Task) cadence() string {
	return t.Schedule().Text()
}

// DurationText 把间隔写成日常说法：30 分钟、2 小时、1 天，不整的退到分钟
func DurationText(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d 天", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d 小时", d/time.Hour)
	default:
		return fmt.Sprintf("%d 分钟", int(d.Round(time.Minute)/time.Minute))
	}
}

func clockText(t, now time.Time) string {
	t = t.In(now.Location())
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// Classify 判断一轮的回答：整条去掉空白后恰好是 [无事] 才算"无事"，正文里夹着不算。
// 空回答也不会显示任何东西，按"无事"记，原因写进 note。
func Classify(reply string) (kind ResultKind, note string) {
	t := strings.TrimSpace(reply)
	switch t {
	case QuietToken:
		return ResultQuiet, ""
	case "":
		return ResultQuiet, "回答为空"
	}
	return ResultContent, ""
}
