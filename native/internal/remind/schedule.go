package remind

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 周期的单位
const (
	UnitMinute = "minute"
	UnitHour   = "hour"
	UnitDay    = "day"
	UnitWeek   = "week"
	UnitMonth  = "month"
	// UnitNone 只在参数里用：改的时候传它表示变回一次性
	UnitNone = "none"
)

// Repeat 是周期。Unit 空是一次性
type Repeat struct {
	Unit  string `json:"unit,omitempty"`
	Every int    `json:"every,omitempty"` // 每几个单位，0 当 1
	// Weekdays 只给 week：1 是周一，7 是周日；空取第一次响那天的星期
	Weekdays []int `json:"weekdays,omitempty"`
}

// None 说是不是一次性
func (r Repeat) None() bool { return r.Unit == "" }

// IsZero 让 JSON 的 omitzero 省掉一次性的空周期
func (r Repeat) IsZero() bool { return r.None() }

func (r Repeat) every() int {
	if r.Every < 1 {
		return 1
	}
	return r.Every
}

// normalize 校验并整理：单位认识、次数在范围、星期去重排好。错误文字直接给她或主人看
func (r Repeat) normalize() (Repeat, error) {
	switch r.Unit {
	case "":
		return Repeat{}, nil
	case UnitMinute, UnitHour, UnitDay, UnitMonth:
		if len(r.Weekdays) > 0 {
			return Repeat{}, errors.New("weekdays 只有 unit 是 week 时才能写")
		}
	case UnitWeek:
	default:
		return Repeat{}, fmt.Errorf("repeat.unit 只能是 minute、hour、day、week、month，收到 %q", clip(r.Unit, 20))
	}
	if r.Every < 0 || r.Every > 1000 {
		return Repeat{}, fmt.Errorf("repeat.every 要在 1 到 1000 之间，收到 %d", r.Every)
	}
	r.Every = r.every()
	if len(r.Weekdays) > 0 {
		days := slices.Clone(r.Weekdays)
		slices.Sort(days)
		days = slices.Compact(days)
		if days[0] < 1 || days[len(days)-1] > 7 {
			return Repeat{}, errors.New("weekdays 里 1 是周一、7 是周日，只能写 1 到 7")
		}
		r.Weekdays = days
	}
	return r, nil
}

// Gap 是这个周期最短隔多久响一次（给周期下限用）
func (r Repeat) Gap() time.Duration {
	e := time.Duration(r.every())
	switch r.Unit {
	case UnitMinute:
		return e * time.Minute
	case UnitHour:
		return e * time.Hour
	case UnitDay:
		return e * 24 * time.Hour
	case UnitWeek:
		gap := 7 * e * 24 * time.Hour
		for i := 1; i < len(r.Weekdays); i++ {
			if d := time.Duration(r.Weekdays[i]-r.Weekdays[i-1]) * 24 * time.Hour; d < gap {
				gap = d
			}
		}
		return gap
	case UnitMonth:
		return e * 28 * 24 * time.Hour
	}
	return 0
}

// Text 写成人话：每天、每 2 小时、每周一三五、工作日、每月
func (r Repeat) Text() string {
	e := r.every()
	switch r.Unit {
	case UnitMinute:
		return fmt.Sprintf("每 %d 分钟", e)
	case UnitHour:
		if e == 1 {
			return "每小时"
		}
		return fmt.Sprintf("每 %d 小时", e)
	case UnitDay:
		if e == 1 {
			return "每天"
		}
		return fmt.Sprintf("每 %d 天", e)
	case UnitWeek:
		if e == 1 && slices.Equal(r.Weekdays, []int{1, 2, 3, 4, 5}) {
			return "工作日"
		}
		s := "每周"
		if e > 1 {
			s = fmt.Sprintf("每 %d 周", e)
		}
		if len(r.Weekdays) > 0 {
			names := [...]string{"", "一", "二", "三", "四", "五", "六", "日"}
			for _, d := range r.Weekdays {
				s += names[d]
			}
		}
		return s
	case UnitMonth:
		if e == 1 {
			return "每月"
		}
		return fmt.Sprintf("每 %d 个月", e)
	}
	return "一次性"
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func daysBetween(a, b time.Time) int {
	return int(dateOnly(b).Sub(dateOnly(a)) / (24 * time.Hour))
}

func atDay(a time.Time, addDays int) time.Time {
	y, m, d := a.Date()
	h, mi, s := a.Clock()
	return time.Date(y, m, d+addDays, h, mi, s, 0, a.Location())
}

// isoWeekday：周一 1，周日 7
func isoWeekday(t time.Time) int {
	if w := int(t.Weekday()); w != 0 {
		return w
	}
	return 7
}

// atMonth 是 anchor 往后第 k 个月的同一天同一时刻；那个月没有这一天就落到月底
func atMonth(a time.Time, k int) time.Time {
	y, m, d := a.Date()
	h, mi, s := a.Clock()
	first := time.Date(y, m+time.Month(k), 1, h, mi, s, 0, a.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, h, mi, s, 0, a.Location())
}

// Next 是严格晚于 after 的下一次（不早于 anchor）。按 after 的时区算墙钟：每天九点不是每隔 24 小时。一次性返回零值
func Next(r Repeat, anchor, after time.Time) time.Time {
	a := anchor.In(after.Location())
	every := r.every()
	switch r.Unit {
	case UnitMinute, UnitHour:
		d := time.Duration(every) * time.Minute
		if r.Unit == UnitHour {
			d = time.Duration(every) * time.Hour
		}
		if after.Before(a) {
			return a
		}
		return a.Add((after.Sub(a)/d + 1) * d)
	case UnitDay:
		if after.Before(a) {
			return a
		}
		k := daysBetween(a, after) / every * every
		for {
			if c := atDay(a, k); c.After(after) {
				return c
			}
			k += every
		}
	case UnitWeek:
		days := r.Weekdays
		if len(days) == 0 {
			days = []int{isoWeekday(a)}
		}
		monday := atDay(a, 1-isoWeekday(a))
		from := max(daysBetween(monday, after), 0)
		w := from / 7 / every * every
		for {
			for _, d := range days {
				c := atDay(monday, w*7+d-1)
				if !c.Before(a) && c.After(after) {
					return c
				}
			}
			w += every
		}
	case UnitMonth:
		if after.Before(a) {
			return a
		}
		ay, am, _ := a.Date()
		fy, fm, _ := after.Date()
		k := max((fy-ay)*12+int(fm-am), 0) / every * every
		for {
			if c := atMonth(a, k); c.After(after) {
				return c
			}
			k += every
		}
	}
	return time.Time{}
}

// first 是从 at 算起的第一次响：一般就是 at；每周几不含 at 那天时是之后第一个对得上的日子
func first(r Repeat, at time.Time) time.Time {
	if r.None() {
		return at
	}
	return Next(r, at, at.Add(-time.Nanosecond))
}

// countMissed 数 from（含）到 now（含）之间一共有几次；一次性算 1 次
func countSlots(r Repeat, anchor, from, now time.Time) int {
	if r.None() {
		return 1
	}
	n := 0
	for c := from; !c.After(now) && n < maxCountedSlots; c = Next(r, anchor, c) {
		n++
	}
	return n
}

var timeLayouts = []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}

// ParseTime 解析本机时区的时间；也收带时区偏移的 RFC3339。不做自然语言。错误文字里写现在几点，方便她重算
func ParseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	loc := now.Location()
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), nil
	}
	return time.Time{}, fmt.Errorf("时间要写成 2026-11-10 09:00 这样（本机时区，年-月-日 时:分）。现在是 %s。", TimeText(now, now))
}

// checkFuture 查时间在将来且不太远
func checkFuture(t, now time.Time) error {
	if !t.After(now) {
		return fmt.Errorf("这个时间已经过了：%s。现在是 %s，重新算一下。", TimeText(t, now), TimeText(now, now))
	}
	if t.Sub(now) > MaxAhead {
		return fmt.Errorf("这个时间太远了（%s），最远定到 10 年以内。", TimeText(t, now))
	}
	return nil
}

// minutesToAt 把 in_minutes 换成时间
func minutesToAt(v float64, now time.Time) (time.Time, error) {
	if v != float64(int64(v)) || v < 1 || v > 60*24*366 {
		return time.Time{}, fmt.Errorf("in_minutes 要是 1 到 %s 之间的整数（多少分钟以后），收到 %v", strconv.Itoa(60*24*366), v)
	}
	return now.Add(time.Duration(v) * time.Minute), nil
}
