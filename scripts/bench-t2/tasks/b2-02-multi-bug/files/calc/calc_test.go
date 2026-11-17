package calc

import (
	"testing"
	"time"

	"example.com/ledger/parse"
)

func TestDiscount(t *testing.T) {
	cases := []struct{ amount, pct, want int }{
		{1000, 10, 100},
		{1999, 10, 199},
		{500, 0, 0},
		{500, 100, 500},
	}
	for _, c := range cases {
		if got := Discount(c.amount, c.pct); got != c.want {
			t.Errorf("Discount(%d, %d) = %d，想要 %d", c.amount, c.pct, got, c.want)
		}
	}
}

func TestTax(t *testing.T) {
	cases := []struct{ amount, bps, want int }{
		{10000, 700, 700},
		{150, 700, 11},
		{99, 700, 7},
		{1, 700, 0},
		{0, 700, 0},
	}
	for _, c := range cases {
		if got := Tax(c.amount, c.bps); got != c.want {
			t.Errorf("Tax(%d, %d) = %d，想要 %d", c.amount, c.bps, got, c.want)
		}
	}
}

func TestOrderTotal(t *testing.T) {
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	a := parse.Order{Date: day, Customer: "a", Amount: 1000, DiscountPct: 10}
	if got := OrderTotal(a, 700); got != 963 {
		t.Errorf("OrderTotal(a) = %d，想要 963", got)
	}
	b := parse.Order{Date: day, Customer: "b", Amount: 1550}
	if got := OrderTotal(b, 700); got != 1659 {
		t.Errorf("OrderTotal(b) = %d，想要 1659", got)
	}
}

func TestAverage(t *testing.T) {
	if got := Average([]int{100, 200, 301}); got != 200 {
		t.Errorf("Average = %d，想要 200", got)
	}
	if got := Average(nil); got != 0 {
		t.Errorf("没有订单时 Average = %d，想要 0", got)
	}
}

func TestMedian(t *testing.T) {
	in := []int{5, 1, 3}
	if got := Median(in); got != 3 {
		t.Errorf("Median = %d，想要 3", got)
	}
	if in[0] != 5 {
		t.Error("Median 不该改动传入的切片")
	}
	if got := Median([]int{1, 2, 3, 4}); got != 2 {
		t.Errorf("偶数个：Median = %d，想要 2", got)
	}
	if got := Median(nil); got != 0 {
		t.Errorf("空：Median = %d，想要 0", got)
	}
}
