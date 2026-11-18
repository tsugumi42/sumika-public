package proactive

import (
	"database/sql"
	"errors"
	"time"
)

// DBStore 把状态和日志存在 trace.db（迁移 0017：proactive_state、proactive_decisions）
type DBStore struct{ DB *sql.DB }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func (s DBStore) LoadState(character string) (State, bool, error) {
	var st State
	var quiet, next, last int64
	err := s.DB.QueryRow(`SELECT factor, quiet_until, cold_streak, next_wake_at, last_wake_at FROM proactive_state WHERE character_id = ?`, character).
		Scan(&st.Factor, &quiet, &st.ColdStreak, &next, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return State{Character: character, Factor: 1}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	st.Character, st.QuietUntil, st.NextWake, st.LastWake = character, fromMS(quiet), fromMS(next), fromMS(last)
	return st, true, nil
}

func (s DBStore) SaveState(st State) error {
	_, err := s.DB.Exec(`INSERT INTO proactive_state (character_id, factor, quiet_until, cold_streak, next_wake_at, last_wake_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(character_id) DO UPDATE SET factor = excluded.factor, quiet_until = excluded.quiet_until, cold_streak = excluded.cold_streak,
		next_wake_at = excluded.next_wake_at, last_wake_at = excluded.last_wake_at, updated_at = excluded.updated_at`,
		st.Character, st.Factor, ms(st.QuietUntil), st.ColdStreak, ms(st.NextWake), ms(st.LastWake), time.Now().UnixMilli())
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s DBStore) AddRow(r Row) (int64, error) {
	var next any
	if !r.NextAt.IsZero() {
		next = r.NextAt.UnixMilli()
	}
	var inputs any
	if len(r.Inputs) > 0 {
		inputs = string(r.Inputs)
	}
	if r.Count < 1 {
		r.Count = 1
	}
	res, err := s.DB.Exec(`INSERT INTO proactive_decisions (at, character_id, kind, reason, inputs, next_at, thread, model_note, count) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.At.UnixMilli(), r.Character, string(r.Kind), r.Reason, inputs, next, nullable(r.Thread), nullable(r.ModelNote), r.Count)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s DBStore) AddCount(id int64, n int) error {
	_, err := s.DB.Exec(`UPDATE proactive_decisions SET count = count + ? WHERE id = ?`, n, id)
	return err
}

func (s DBStore) Annotate(id int64, reason, thread, note string) error {
	_, err := s.DB.Exec(`UPDATE proactive_decisions SET reason = CASE WHEN ? = '' THEN reason ELSE ? END,
		thread = COALESCE(?, thread), model_note = COALESCE(?, model_note) WHERE id = ?`, reason, reason, nullable(thread), nullable(note), id)
	return err
}

func (s DBStore) HasRowSince(character string, since time.Time) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT count(*) FROM (SELECT 1 FROM proactive_decisions WHERE character_id = ? AND at >= ? LIMIT 1)`, character, since.UnixMilli()).Scan(&n)
	return n > 0, err
}

func (s DBStore) Prune(before time.Time) (int, error) {
	res, err := s.DB.Exec(`DELETE FROM proactive_decisions WHERE at < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Recent 最近 limit 行日志，新的在前。给 sumika-ctl proactive log 用
func (s DBStore) Recent(limit int) ([]Row, error) {
	rows, err := s.DB.Query(`SELECT id, at, character_id, kind, reason, COALESCE(inputs, ''), COALESCE(next_at, 0), COALESCE(thread, ''), COALESCE(model_note, ''), count
		FROM proactive_decisions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var at, next int64
		var kind, inputs string
		if err := rows.Scan(&r.ID, &at, &r.Character, &kind, &r.Reason, &inputs, &next, &r.Thread, &r.ModelNote, &r.Count); err != nil {
			return nil, err
		}
		r.At, r.Kind, r.NextAt = time.UnixMilli(at), Kind(kind), fromMS(next)
		if inputs != "" {
			r.Inputs = []byte(inputs)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// States 读所有角色的导演状态，给 sumika-ctl proactive log 显示下次醒的时间
func (s DBStore) States() ([]State, error) {
	rows, err := s.DB.Query(`SELECT character_id, factor, quiet_until, cold_streak, next_wake_at, last_wake_at FROM proactive_state ORDER BY character_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var st State
		var quiet, next, last int64
		if err := rows.Scan(&st.Character, &st.Factor, &quiet, &st.ColdStreak, &next, &last); err != nil {
			return nil, err
		}
		st.QuietUntil, st.NextWake, st.LastWake = fromMS(quiet), fromMS(next), fromMS(last)
		out = append(out, st)
	}
	return out, rows.Err()
}

// RecentFor 是某个角色最近 limit 行日志，新的在前。给右栏“联系”标签用
func (s DBStore) RecentFor(character string, limit int) ([]Row, error) {
	rows, err := s.DB.Query(`SELECT id, at, character_id, kind, reason, COALESCE(inputs, ''), COALESCE(next_at, 0), COALESCE(thread, ''), COALESCE(model_note, ''), count
		FROM proactive_decisions WHERE character_id = ? ORDER BY id DESC LIMIT ?`, character, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var at, next int64
		var kind, inputs string
		if err := rows.Scan(&r.ID, &at, &r.Character, &kind, &r.Reason, &inputs, &next, &r.Thread, &r.ModelNote, &r.Count); err != nil {
			return nil, err
		}
		r.At, r.Kind, r.NextAt = time.UnixMilli(at), Kind(kind), fromMS(next)
		if inputs != "" {
			r.Inputs = []byte(inputs)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
