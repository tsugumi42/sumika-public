package loop

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ScheduleKind 是日程的种类
type ScheduleKind string

const (
	KindInterval ScheduleKind = "interval" // 固定间隔
	KindDaily    ScheduleKind = "daily"    // 每天某时
	KindWeekly   ScheduleKind = "weekly"   // 每周某几天某时
	KindSelf     ScheduleKind = "self"     // 让她自己定下次
)

// Schedule 是"什么时候跑"。三种写法互斥：Interval>0 是固定间隔；At 非空是每天（Days 空）或每周几（Days 非空）某时；
// 都没有就是让她每轮结束时自己定。时间按本机时区算。
type Schedule struct {
	Interval time.Duration
	At       string // "15:04"
	Days     []int  // 0 是周日 … 6 是周六
}

var weekdayChar = []string{"日", "一", "二", "三", "四", "五", "六"}

// Kind 认日程的种类（不检查合不合法，合法性看 Validate）
func (s Schedule) Kind() ScheduleKind {
	switch {
	case s.Interval > 0:
		return KindInterval
	case s.At != "" && len(s.Normalized().Days) > 0:
		return KindWeekly
	case s.At != "":
		return KindDaily
	}
	return KindSelf
}

// Clock 是否按钟点（每天、每周）
func (s Schedule) Clock() bool { k := s.Kind(); return k == KindDaily || k == KindWeekly }

// Normalized 把星期排序去重；七天全选等于每天
func (s Schedule) Normalized() Schedule {
	seen := map[int]bool{}
	var days []int
	for _, d := range s.Days {
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	sort.Ints(days)
	if len(days) == 7 {
		days = nil
	}
	s.Days = days
	return s
}

func parseClock(at string) (h, m int, err error) {
	bad := fmt.Errorf("时间「%s」写得不对：用 24 小时制的 时:分，例如 09:30", at)
	if len(at) != 5 || at[2] != ':' {
		return 0, 0, bad
	}
	for _, i := range []int{0, 1, 3, 4} {
		if at[i] < '0' || at[i] > '9' {
			return 0, 0, bad
		}
	}
	h, m = int(at[0]-'0')*10+int(at[1]-'0'), int(at[3]-'0')*10+int(at[4]-'0')
	if h > 23 || m > 59 {
		return 0, 0, fmt.Errorf("时间「%s」不在 00:00 到 23:59 之间", at)
	}
	return h, m, nil
}

// Validate 检查日程：间隔和钟点不能同时写；钟点要能解析；星期在 0 到 6 之间
func (s Schedule) Validate() error {
	if s.Interval != 0 {
		if s.At != "" || len(s.Days) > 0 {
			return fmt.Errorf("固定间隔和每天、每周几点不能同时写")
		}
		if s.Interval < MinInterval || s.Interval > MaxInterval {
			return fmt.Errorf("间隔要在 %s 到 %s 之间", DurationText(MinInterval), DurationText(MaxInterval))
		}
		return nil
	}
	if s.At == "" {
		if len(s.Days) > 0 {
			return fmt.Errorf("选了星期几，还要写几点")
		}
		return nil
	}
	if _, _, err := parseClock(s.At); err != nil {
		return err
	}
	for _, d := range s.Days {
		if d < 0 || d > 6 {
			return fmt.Errorf("星期要在 0（周日）到 6（周六）之间，收到 %d", d)
		}
	}
	return nil
}

// Next 是严格晚于 after 的下一个钟点；不是钟点日程返回零值。after 的时区就是日程的时区
func (s Schedule) Next(after time.Time) time.Time {
	if !s.Clock() {
		return time.Time{}
	}
	h, m, err := parseClock(s.At)
	if err != nil {
		return time.Time{}
	}
	days := s.Normalized().Days
	ok := func(w time.Weekday) bool {
		if len(days) == 0 {
			return true
		}
		for _, d := range days {
			if d == int(w) {
				return true
			}
		}
		return false
	}
	y, mo, d := after.Date()
	for i := 0; i <= 8; i++ {
		c := time.Date(y, mo, d+i, h, m, 0, 0, after.Location())
		if c.After(after) && ok(c.Weekday()) {
			return c
		}
	}
	return time.Time{}
}

// Text 是日程的日常说法：每 30 分钟、每天 09:00、每周一三五 09:00、自定节奏
func (s Schedule) Text() string {
	switch s.Kind() {
	case KindInterval:
		return "每 " + DurationText(s.Interval)
	case KindDaily:
		return "每天 " + s.At
	case KindWeekly:
		var b strings.Builder
		for _, d := range s.Normalized().Days {
			if d >= 0 && d <= 6 {
				b.WriteString(weekdayChar[d])
			}
		}
		return "每周" + b.String() + " " + s.At
	}
	return "自定节奏"
}

// countMissed 数 from（本该跑的那次）之后、now 之前又漏了几次钟点
func (s Schedule) countMissed(from, now time.Time) int {
	n := 0
	for cur := s.Next(from); !cur.IsZero() && !cur.After(now) && n < 1000; cur = s.Next(cur) {
		n++
	}
	return n
}
