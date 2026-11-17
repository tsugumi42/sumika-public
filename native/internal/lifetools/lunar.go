package lifetools

import (
	"container/list"
	"fmt"
	"time"

	"github.com/6tail/lunar-go/calendar"
)

// 本地算的那一块：星期、农历、节气、传统节日。历法用 6tail/lunar-go（MIT），不自己写。
// 年份干支用 GetYearInGanZhi（按正月初一换年，和农历年同步；ByLiChun 是按立春换，Exact 是按立春精确时刻换，写日历给人看用前者）。

// 能查的年份范围：lunar-go 的节气表和农历表覆盖 1900～2100，两头留一年余量
const (
	MinYear = 1901
	MaxYear = 2100
)

// DayFacts 是一天里不用联网就知道的事
type DayFacts struct {
	Date      string // 2027-01-31
	Weekday   string // 星期日
	IsWeekend bool
	LunarYear string // 丁未年（羊）
	LunarDay  string // 正月初二
	JieQi     string // 当天是节气才有：立春
	PrevJieQi string // 上一个节气和日期：冬至 2026-12-22
	NextJieQi string // 下一个节气和日期
	Festivals []string
}

func listStrings(l *list.List) []string {
	var out []string
	for e := l.Front(); e != nil; e = e.Next() {
		if s, ok := e.Value.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Facts 算某一天（只看年月日）的日历事实。年份超出范围由调用方先拦
func Facts(y int, m time.Month, d int) DayFacts {
	solar := calendar.NewSolarFromYmd(y, int(m), d)
	lunar := solar.GetLunar()
	wd := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday()
	f := DayFacts{
		Date:      fmt.Sprintf("%04d-%02d-%02d", y, int(m), d),
		Weekday:   "星期" + solar.GetWeekInChinese(),
		IsWeekend: wd == time.Saturday || wd == time.Sunday,
		LunarYear: lunar.GetYearInGanZhi() + "年（" + lunar.GetYearShengXiao() + "）",
		LunarDay:  lunar.GetMonthInChinese() + "月" + lunar.GetDayInChinese(),
		JieQi:     lunar.GetJieQi(),
	}
	if p := lunar.GetPrevJieQi(); p != nil {
		f.PrevJieQi = p.GetName() + " " + p.GetSolar().ToYmd()
	}
	if n := lunar.GetNextJieQi(); n != nil {
		f.NextJieQi = n.GetName() + " " + n.GetSolar().ToYmd()
	}
	// 农历传统节日（春节、元宵、端午、七夕、中秋、重阳、除夕等），再加公历的节日（元旦、劳动节、国庆等）；不收"寒食""三伏"这类的杂项
	f.Festivals = append(f.Festivals, listStrings(lunar.GetFestivals())...)
	f.Festivals = append(f.Festivals, listStrings(solar.GetFestivals())...)
	return f
}
