package selfcheck_test

import (
	"context"
	"testing"

	"sumika-native/internal/selfcheck"
)

// S3：当天无回执次数到阈值才 bad，阈值以下是 ok（偶发的一两次是正常）
func TestS3ReceiptUnknown(t *testing.T) {
	th := selfcheck.DefaultThresholds()
	th.ReceiptUnknownPerDay = 3
	c := findCheck(t, "outlet.receipt_unknown", th)
	live := selfcheck.NewCounters()
	for i := 1; i <= 4; i++ {
		live.Add(selfcheck.OutletReceiptUnknown)
		r := c.Eval(context.Background(), envAt(t0, live))
		want := selfcheck.Ok
		if i >= 3 {
			want = selfcheck.Bad
		}
		if r.State != want || r.Value != int64(i) || r.Threshold != 3 {
			t.Fatalf("第 %d 次：%+v，想要 %s", i, r, want)
		}
	}
}
