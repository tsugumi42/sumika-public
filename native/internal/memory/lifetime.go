package memory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// memoryLifetime 使用原有 valid_to；到期仅停止现状召回，不推断完成。
// 没写时态的：边界、承诺、偏好按 stable（漏写不该让它们几天后悄悄不再被想起），其余保守按 temporary。
func memoryLifetime(kind, temporal, until string, now time.Time) (string, *int64, error) {
	temporal = strings.TrimSpace(temporal)
	if temporal == "" && until == "" && stableByDefault[kind] {
		temporal = "stable"
	}
	if temporal == "" || temporal == "now" || temporal == "plan" || temporal == "past" || temporal == "stopped" {
		temporal = "temporary"
	}
	if !temporalSet[temporal] {
		return "", nil, errors.New("invalid temporal")
	}
	if until != "" {
		at, err := time.Parse(time.RFC3339, until)
		if err != nil || temporal != "temporary" || !at.After(now) {
			return "", nil, errors.New("invalid valid_until")
		}
		ms := at.UnixMilli()
		return temporal, &ms, nil
	}
	if temporal == "temporary" {
		ms := now.Add(TempDays * 24 * time.Hour).UnixMilli()
		return temporal, &ms, nil
	}
	return temporal, nil, nil
}

// refreshLifetime 同文更新只接受更晚的本人来源，且不能动主人确认/导入条目。
// 不改变 learned_at（证据和创建时间语义不变），用 event_at 防止旧批次倒灌期限。
// stableByDefault：没写时态时默认长期有效的几种
var stableByDefault = map[string]bool{"boundary": true, "promise": true, "preference": true}

func (t *txRun) refreshLifetime(ctx context.Context, live *dupRow, op FactOp) error {
	if op.EventAt <= 0 {
		return nil
	}
	var confirmed, temporal sql.NullString
	var until, eventAt sql.NullInt64
	if err := t.tx.QueryRowContext(ctx, `SELECT confirmed_by, temporal, valid_to, event_at FROM facts WHERE id=?`, live.id).Scan(&confirmed, &temporal, &until, &eventAt); err != nil {
		return err
	}
	// 有效的只刷新 ai 的；owner_said 只让 remember 刷新（整理的合并也会走到这里，它不能动主人说过的事实的期限）
	mayRefresh := confirmed.String == "ai" || (confirmed.String == ConfirmedOwnerSaid && t.b.By == ByRememberAI)
	if (live.status == "active" && !mayRefresh) || op.EventAt <= eventAt.Int64 {
		return nil
	}
	before := map[string]any{"temporal": temporal.String, "event_at": eventAt.Int64}
	if until.Valid {
		before["valid_to"] = until.Int64
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE facts SET temporal=?, valid_to=?, event_at=? WHERE id=?`, op.Temporal, op.ValidTo, op.EventAt, live.id); err != nil {
		return err
	}
	return t.revise(ctx, live.id, "lifetime", before, map[string]any{"temporal": op.Temporal, "valid_to": op.ValidTo, "event_at": op.EventAt, "source_seqs": op.SourceSeqs}, "更晚的本人来源更新期限，不代表完成")
}
