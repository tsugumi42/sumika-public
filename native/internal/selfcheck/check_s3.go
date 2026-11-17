package selfcheck

import "context"

// receiptUnknown 是 S3 outlet.receipt_unknown：发出去的消息等不到回声，按"结果未知"收场，当天次数达到阈值。
type receiptUnknown struct{ threshold int64 }

func (receiptUnknown) ID() string   { return "outlet.receipt_unknown" }
func (receiptUnknown) Name() string { return "发送无回执（当天次数）" }
func (c receiptUnknown) Eval(_ context.Context, e *Env) Result {
	n := e.DayCount(OutletReceiptUnknown)
	r := Result{State: Ok, Value: n, Threshold: c.threshold}
	if n >= c.threshold {
		r.State = Bad
	}
	return r
}
