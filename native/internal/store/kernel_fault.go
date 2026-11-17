package store

import (
	"context"

	"sumika-native/internal/kernel"
)

// 内核故障的重试要“写完读回”：UPDATE 没报错不等于写上了（WHERE 没命中也是成功），重试据此如实说成没成。

// KernelRunRow 是库里这一轮现在的样子；没有这一行返回 sql.ErrNoRows。
func (s *Store) KernelRunRow(ctx context.Context, run string) (state kernel.RunState, outcome kernel.Outcome, drained bool, err error) {
	var o *string
	err = s.Trace.QueryRowContext(ctx, `SELECT state,outcome,drained FROM kernel_runs WHERE run_id=?`, run).Scan(&state, &o, &drained)
	if o != nil {
		outcome = kernel.Outcome(*o)
	}
	return state, outcome, drained, err
}

// HoldKernelInput 把一句已受理、没能往下处理的话标成 held：和重启时的恢复同一个结果，
// 本地的话留给主人在“没处理的话”里放行或放弃。只认 accepted，别的状态不动（返回 nil）。
func (s *Store) HoldKernelInput(ctx context.Context, id string) error {
	_, err := s.Trace.ExecContext(ctx, `UPDATE kernel_inbox SET state='held',revision=revision+1 WHERE input_id=? AND state='accepted'`, id)
	return err
}
