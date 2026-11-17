package lifetools

import (
	"strings"
	"testing"
	"time"
)

// 预期值都是公开的历法事实（春节、节气、节日的公历日期），不是拿库自己算的结果回填的
func TestFactsKnownDates(t *testing.T) {
	cases := []struct {
		y        int
		m        time.Month
		d        int
		weekday  string
		lunar    string // 农历年加月日
		jieqi    string
		festival string
	}{
		{2026, 2, 17, "星期二", "丙午年（马）正月初一", "", "春节"},
		{2026, 2, 16, "星期一", "乙巳年（蛇）腊月廿九", "", "除夕"},
		{2027, 2, 6, "星期六", "丁未年（羊）正月初一", "", "春节"},
		{2026, 4, 5, "星期日", "丙午年（马）二月十八", "清明", ""},
		{2026, 2, 4, "星期三", "乙巳年（蛇）腊月十七", "立春", ""},
		{2026, 6, 21, "星期日", "丙午年（马）五月初七", "夏至", ""},
		{2026, 12, 22, "星期二", "丙午年（马）冬月十四", "冬至", ""},
		{2026, 6, 19, "星期五", "丙午年（马）五月初五", "", "端午"},
		{2026, 9, 25, "星期五", "丙午年（马）八月十五", "", "中秋"},
		{2026, 10, 1, "星期四", "丙午年（马）八月廿一", "", "国庆"},
		// 农历年按正月初一换：2026-01-10 还在乙巳年
		{2026, 1, 10, "星期六", "乙巳年（蛇）冬月廿二", "", ""},
		{2000, 1, 1, "星期六", "己卯年（兔）冬月廿五", "", "元旦"},
	}
	for _, c := range cases {
		f := Facts(c.y, c.m, c.d)
		if f.Weekday != c.weekday {
			t.Errorf("%s 星期：%s，要 %s", f.Date, f.Weekday, c.weekday)
		}
		if got := f.LunarYear + f.LunarDay; c.lunar != "" && got != c.lunar {
			t.Errorf("%s 农历：%s，要 %s", f.Date, got, c.lunar)
		}
		if f.JieQi != c.jieqi {
			t.Errorf("%s 节气：%q，要 %q", f.Date, f.JieQi, c.jieqi)
		}
		if c.festival != "" && !strings.Contains(strings.Join(f.Festivals, "、"), c.festival) {
			t.Errorf("%s 节日：%v，要含 %s", f.Date, f.Festivals, c.festival)
		}
	}
}

func TestFactsWeekend(t *testing.T) {
	if !Facts(2027, 2, 13).IsWeekend || !Facts(2027, 2, 14).IsWeekend || Facts(2027, 2, 15).IsWeekend {
		t.Fatal("周末判断错了")
	}
}

// 不是节气的日子写上一个、下一个节气（立冬 2026-11-07、小雪 2026-11-22 是公开的节气日期）
func TestFactsNeighbourJieQi(t *testing.T) {
	f := Facts(2026, 11, 9)
	if f.JieQi != "" || f.PrevJieQi != "立冬 2026-11-07" || f.NextJieQi != "小雪 2026-11-22" {
		t.Fatalf("%+v", f)
	}
}

// 范围两头不崩：每一天都能算出星期、农历（节气表跨年的 1 月和 12 月最容易出错）
func TestFactsRangeEdges(t *testing.T) {
	for _, y := range []int{MinYear, MinYear + 1, 2099, MaxYear} {
		for _, m := range []time.Month{1, 2, 12} {
			for _, d := range []int{1, 15, 28} {
				f := Facts(y, m, d)
				if f.Weekday == "" || f.LunarDay == "" || f.LunarYear == "" || f.PrevJieQi == "" || f.NextJieQi == "" {
					t.Errorf("%04d-%02d-%02d：%+v", y, int(m), d, f)
				}
			}
		}
	}
}
