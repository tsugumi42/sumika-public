package parse

import (
	"strings"
	"testing"
)

func TestParseAmount(t *testing.T) {
	good := map[string]int{
		"7":     700,
		"12.50": 1250,
		"0.05":  5,
		"19.99": 1999,
		" 3 ":   300,
		"12.5":  1250,
		"0.5":   50,
	}
	for in, want := range good {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v，想要 %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "1.234", "-1", "1.x"} {
		if got, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) 应该报错，拿到 %d", in, got)
		}
	}
}

func TestParseDate(t *testing.T) {
	if d, err := ParseDate("2026-03-01"); err != nil || d.Day() != 1 {
		t.Errorf("正常日期解析失败：%v %v", d, err)
	}
	for _, in := range []string{"2026-02-30", "2026/03/01", "", "tomorrow"} {
		if _, err := ParseDate(in); err == nil {
			t.Errorf("ParseDate(%q) 应该报错", in)
		}
	}
}

func TestParseLine(t *testing.T) {
	o, err := ParseLine("2026-03-01, Ann ,12.50,10")
	if err != nil || o.Customer != "Ann" || o.Amount != 1250 || o.DiscountPct != 10 {
		t.Errorf("得到 %+v, %v", o, err)
	}
	o, err = ParseLine("2026-03-02,bob,12.5,0")
	if err != nil || o.Amount != 1250 {
		t.Errorf("一位小数的金额：得到 %+v, %v", o, err)
	}
	for _, in := range []string{"", "2026-03-01,ann,5", "2026-03-01,,5,0", "2026-03-01,ann,5,101", "2026-03-01,ann,5,x"} {
		if _, err := ParseLine(in); err == nil {
			t.Errorf("ParseLine(%q) 应该报错", in)
		}
	}
}

func TestParseAll(t *testing.T) {
	in := "# 表头\n\n2026-03-01,ann,5,0\nbad line\n2026-03-02,bob,6,0\n"
	orders, bad := ParseAll(strings.NewReader(in))
	if len(orders) != 2 || bad != 1 {
		t.Errorf("订单 %d 笔、坏行 %d，想要 2 和 1", len(orders), bad)
	}
}
