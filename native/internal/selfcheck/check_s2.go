package selfcheck

import "context"

// intakeStoreFailed 是 S2 intake.store_failed：消息已经收下，但写库失败、没进库。
// 阈值 1（写库失败本来就不该有，一次性的 SQLITE_BUSY 也要看到）；只看当天累计，过了零点自然恢复。
type intakeStoreFailed struct{}

func (intakeStoreFailed) ID() string   { return "intake.store_failed" }
func (intakeStoreFailed) Name() string { return "收到的消息入库失败" }
func (intakeStoreFailed) Eval(_ context.Context, e *Env) Result {
	n := e.DayCount(IntakeStoreFailed)
	r := Result{State: Ok, Value: n, Threshold: 1}
	if n >= 1 {
		r.State = Bad
		if c := e.Live.LastStoreError(); c != "" {
			r.Example = map[string]string{"errorClass": c}
		}
	}
	return r
}
