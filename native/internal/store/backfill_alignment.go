package store

import (
	"context"
)

// AlignmentRecord 仅保存固定状态和计数；页进度、会话名和外部错误正文不写快照。
type AlignmentRecord struct {
	JobID, Scope, Trigger, State string
	GapID                        int64
	StartedMS, EndedMS           int64
	Inserted, Duplicates         int
	Messages, MediaJSON, Reason  string
	Covered                      bool
}

const alignmentColumns = `job_id,scope,gap_id,trigger,state,started_ms,ended_ms,inserted,duplicates,messages,media_json,reason,covered`

func (s *Store) SaveAlignment(ctx context.Context, account string, r AlignmentRecord) error {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	slot := "active"
	if r.State == "completed" || r.State == "failed" || r.State == "canceled" {
		slot = "last"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO channel_alignment_latest(account,slot,`+alignmentColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account,slot) DO UPDATE SET job_id=excluded.job_id,scope=excluded.scope,gap_id=excluded.gap_id,trigger=excluded.trigger,state=excluded.state,started_ms=excluded.started_ms,ended_ms=excluded.ended_ms,inserted=excluded.inserted,duplicates=excluded.duplicates,messages=excluded.messages,media_json=excluded.media_json,reason=excluded.reason,covered=excluded.covered`, account, slot, r.JobID, r.Scope, r.GapID, r.Trigger, r.State, r.StartedMS, r.EndedMS, r.Inserted, r.Duplicates, r.Messages, r.MediaJSON, r.Reason, r.Covered)
	if err != nil {
		return err
	}
	if slot == "last" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM channel_alignment_latest WHERE account=? AND slot='active'`, account); err != nil {
			return err
		}
	}
	return s.commitChanged(tx, "@alignment")
}

func (s *Store) ReadAlignment(ctx context.Context, account string) (active, last *AlignmentRecord, err error) {
	var has int
	if err = s.Events.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='channel_alignment_latest'`).Scan(&has); err != nil || has == 0 {
		return
	}
	rows, e := s.Events.QueryContext(ctx, `SELECT slot,`+alignmentColumns+` FROM channel_alignment_latest WHERE account=?`, account)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var slot string
		r := new(AlignmentRecord)
		if err = rows.Scan(&slot, &r.JobID, &r.Scope, &r.GapID, &r.Trigger, &r.State, &r.StartedMS, &r.EndedMS, &r.Inserted, &r.Duplicates, &r.Messages, &r.MediaJSON, &r.Reason, &r.Covered); err != nil {
			return
		}
		if slot == "active" {
			active = r
		} else {
			last = r
		}
	}
	err = rows.Err()
	return
}

// AlignmentGapCounts 每次查现状，不把上次任务的剩余数当作当前值。
func (s *Store) AlignmentGapCounts(ctx context.Context, account string, allowed func(string) bool) (open, abandoned, excluded int, err error) {
	var has int
	if err = s.Events.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='channel_gaps' AND type='table'`).Scan(&has); err != nil || has == 0 {
		return
	}
	rows, e := s.Events.QueryContext(ctx, `SELECT conversation,state,count(*) FROM channel_gaps WHERE channel='qq' AND state IN ('open','blocked','abandoned') AND (conversation LIKE ? OR conversation LIKE 'qq:group:%') GROUP BY conversation,state`, "qq:private:"+account+":%")
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var conv, state string
		var n int
		if err = rows.Scan(&conv, &state, &n); err != nil {
			return
		}
		if !allowed(conv) {
			excluded += n
		} else if state != "abandoned" {
			open += n
		} else {
			abandoned += n
		}
	}
	err = rows.Err()
	return
}

// NotifyBackfill 只使任务投影失效；进度不写 events，也不逐页写快照。
func (s *Store) NotifyBackfill() { s.changed("@alignment") }

// NotifyBackfillMedia 只唤醒媒体统计；任务订阅仍按协调器的发布节奏刷新。
func (s *Store) NotifyBackfillMedia() { s.changed("@alignment-media") }
