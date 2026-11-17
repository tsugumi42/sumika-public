package runstats

// 回合耗时时间线的读取（规格-回合耗时时间线）：汇总每种环节花了多久，或列出一轮的时间线。

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"sumika-native/internal/ctlproto"
)

// Spans 读时间线：a.Turn 不空列这一轮（last 是最近一轮），否则汇总 r 这段时间。
func (s *Service) Spans(ctx context.Context, r ctlproto.UsageRange, a ctlproto.StatsArgs) (ctlproto.SpansReport, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if a.Turn != "" {
		return s.spanTurn(ctx, a.Turn)
	}
	if r.FromMS < 0 || r.ToMS <= r.FromMS {
		return ctlproto.SpansReport{}, errors.New("时间范围不对")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, name, ms, outcome, turn_id FROM turn_spans WHERE started_at >= ? AND started_at < ?`, r.FromMS, r.ToMS)
	if err != nil {
		return ctlproto.SpansReport{}, s.queryErr(ctx, err)
	}
	defer rows.Close()
	type key struct{ kind, name string }
	type acc struct {
		stat ctlproto.SpanStat
		p    percentiles
	}
	groups := map[key]*acc{}
	turns := map[string]bool{}
	for rows.Next() {
		var k key
		var ms int64
		var outcome, turn string
		if err := rows.Scan(&k.kind, &k.name, &ms, &outcome, &turn); err != nil {
			return ctlproto.SpansReport{}, s.queryErr(ctx, err)
		}
		g := groups[k]
		if g == nil {
			g = &acc{stat: ctlproto.SpanStat{Kind: k.kind, Name: k.name}}
			groups[k] = g
		}
		g.stat.Count++
		g.stat.TotalMs += ms
		g.stat.MaxMs = max(g.stat.MaxMs, ms)
		if outcome != "ok" {
			g.stat.Failed++
		}
		g.p.add(ms)
		turns[turn] = true
	}
	if err := rows.Err(); err != nil {
		return ctlproto.SpansReport{}, s.queryErr(ctx, err)
	}
	rep := ctlproto.SpansReport{Range: r, Turns: int64(len(turns))}
	for _, g := range groups {
		g.stat.P50Ms, g.stat.P95Ms = g.p.quantiles()
		rep.Stats = append(rep.Stats, g.stat)
	}
	sort.Slice(rep.Stats, func(i, j int) bool {
		a, b := rep.Stats[i], rep.Stats[j]
		if a.TotalMs != b.TotalMs {
			return a.TotalMs > b.TotalMs
		}
		return a.Kind+a.Name < b.Kind+b.Name
	})
	return rep, nil
}

func (s *Service) spanTurn(ctx context.Context, turn string) (ctlproto.SpansReport, error) {
	if turn == "last" {
		err := s.db.QueryRowContext(ctx, `SELECT turn_id FROM turn_spans ORDER BY started_at DESC, seq DESC LIMIT 1`).Scan(&turn)
		if errors.Is(err, sql.ErrNoRows) {
			return ctlproto.SpansReport{}, nil
		}
		if err != nil {
			return ctlproto.SpansReport{}, s.queryErr(ctx, err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, name, started_at, ms, outcome FROM turn_spans WHERE turn_id = ? ORDER BY started_at, seq LIMIT ?`, turn, ctlproto.SpansMaxRows+1)
	if err != nil {
		return ctlproto.SpansReport{}, s.queryErr(ctx, err)
	}
	defer rows.Close()
	rep := ctlproto.SpansReport{Turn: turn}
	for rows.Next() {
		var row ctlproto.SpanRow
		if err := rows.Scan(&row.Kind, &row.Name, &row.StartedAt, &row.Ms, &row.Outcome); err != nil {
			return ctlproto.SpansReport{}, s.queryErr(ctx, err)
		}
		rep.Rows = append(rep.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return ctlproto.SpansReport{}, s.queryErr(ctx, err)
	}
	if len(rep.Rows) > ctlproto.SpansMaxRows {
		rep.Rows, rep.Truncated = rep.Rows[:ctlproto.SpansMaxRows], true
	}
	return rep, nil
}
