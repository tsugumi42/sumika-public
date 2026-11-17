package format

import "testing"

func TestMoney(t *testing.T) {
	cases := map[int]string{0: "CNY 0.00", 5: "CNY 0.05", 1250: "CNY 12.50", 100000: "CNY 1000.00"}
	for in, want := range cases {
		if got := Money(in, "CNY"); got != want {
			t.Errorf("Money(%d) = %q，想要 %q", in, got, want)
		}
	}
}
