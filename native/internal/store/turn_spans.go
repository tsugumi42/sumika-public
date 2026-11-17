package store

// Span 是 turn_spans 的一行：一轮里的一段耗时（规格-回合耗时时间线）。
type Span struct {
	Kind, Name string
	StartedAt  int64 // 毫秒
	Ms         int64
	Outcome    string // ok / failed
}

// SpanRetentionDays 是时间线留多少天。
const SpanRetentionDays = 30

// AddSpans 把一轮的时间线整批写进去。回合结束时调一次，回合中途不碰库。
func (s *Store) AddSpans(turnID string, spans []Span) error {
	if turnID == "" || len(spans) == 0 {
		return nil
	}
	tx, err := s.Trace.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, sp := range spans {
		outcome := sp.Outcome
		if outcome == "" {
			outcome = "ok"
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO turn_spans (turn_id, seq, kind, name, started_at, ms, outcome) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			turnID, i, sp.Kind, sp.Name, sp.StartedAt, sp.Ms, outcome); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneSpans 清掉早于 before（毫秒）的时间线，返回清了几行。启动时调一次。
func (s *Store) PruneSpans(before int64) (int64, error) {
	r, err := s.Trace.Exec(`DELETE FROM turn_spans WHERE started_at < ?`, before)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
