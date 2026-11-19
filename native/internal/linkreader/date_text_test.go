package linkreader

import "testing"

func TestIsoDateText(t *testing.T) {
	for in, want := range map[string]string{
		"2011 年 4 月 18 日":  "2011-04-18",
		"2025年11月16日":      "2025-11-16",
		"2027年05月":         "2027-05",
		" 2026 年 8 月 1 日 ": "2026-08-01",
		"即将推出":             "即将推出",
		"Q3 2026":          "Q3 2026",
		"2026 年第三季度":       "2026 年第三季度",
		"2026年13月1日":       "2026年13月1日",
		"2026年2月32日":       "2026年2月32日",
		"":                 "",
	} {
		if got := isoDateText(in); got != want {
			t.Errorf("%q：得到 %q，应为 %q", in, got, want)
		}
	}
}
