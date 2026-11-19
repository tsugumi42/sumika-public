package store

import (
	"context"
	"database/sql"
	"strings"
)

// 计划表是发送结果的来源；不把没有确认的试运行记录伪装成已经发出。
func enrichDisplayState(ctx context.Context, tx *sql.Tx, items []DisplayRow) error {
	marks := make([]string, len(items))
	args := make([]any, len(items))
	bySeq := make(map[int64]int, len(items))
	for i, r := range items {
		marks[i] = "?"
		args[i] = r.Seq
		bySeq[r.Seq] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_seq,kind,state FROM outbound_plans WHERE event_seq IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var kind, state string
		if err := rows.Scan(&seq, &kind, &state); err != nil {
			return err
		}
		i := bySeq[seq]
		items[i].HasPlan = true
		items[i].Unsent = kind == PlanKindDryRun || state == PlanSending || state == PlanFailed || state == PlanUnknown || state == PlanAborted
		items[i].Partial = state == PlanPartial
		items[i].Aborted = state == PlanAborted
	}
	return rows.Err()
}
