package runstats

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"sumika-native/internal/ctlproto"
)

// ConvTurns 读一个会话最近的模型请求，按轮分组（规格-右栏上下文）。走 ix_runs_conv，不查提示词那几列大文本。
func (s *Service) ConvTurns(ctx context.Context, conversation string, maxRuns int) (ctlproto.ConvTurnsReport, error) {
	if conversation == "" {
		return ctlproto.ConvTurnsReport{}, errors.New("没有会话")
	}
	if maxRuns <= 0 || maxRuns > ctlproto.ConvTurnsMaxRuns {
		maxRuns = ctlproto.ConvTurnsMaxRuns
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(turn_id,''), id, character_id, model, started_at, COALESCE(total_ms,0), COALESCE(ttft_ms,0),
		COALESCE(input_tokens,0), COALESCE(cache_read_tokens,0), COALESCE(cache_write_tokens,0), COALESCE(output_tokens,0), COALESCE(reasoning_tokens,0),
		COALESCE(error_class,''), COALESCE(error,'')
		FROM model_runs WHERE conversation = ? ORDER BY started_at DESC, id DESC LIMIT ?`, conversation, maxRuns+1)
	if err != nil {
		return ctlproto.ConvTurnsReport{}, s.queryErr(ctx, err)
	}
	defer rows.Close()
	var rep ctlproto.ConvTurnsReport
	index := map[string]int{}
	n := 0
	for rows.Next() {
		var id, character, class, failure string
		var r ctlproto.ConvRequest
		var write int64
		if err := rows.Scan(&id, &r.ID, &character, &r.Model, &r.AtMS, &r.TotalMs, &r.TTFTMs, &r.Input, &r.CacheRead, &write, &r.Output, &r.Reasoning, &class, &failure); err != nil {
			return ctlproto.ConvTurnsReport{}, s.queryErr(ctx, err)
		}
		if n++; n > maxRuns {
			rep.Truncated = true
			break
		}
		if id == "" {
			continue // 后台任务不算一轮
		}
		r.Model = s.canon(r.Model)
		r.Input += r.CacheRead + write
		if failure != "" {
			r.Failed = class
			if r.Failed == "" {
				r.Failed = "unknown"
			}
		}
		i, ok := index[id]
		if !ok {
			i = len(rep.Turns)
			index[id] = i
			rep.Turns = append(rep.Turns, ctlproto.ConvTurn{ID: id, Character: character})
		}
		rep.Turns[i].Requests = append(rep.Turns[i].Requests, r)
	}
	if err := rows.Err(); err != nil {
		return ctlproto.ConvTurnsReport{}, s.queryErr(ctx, err)
	}
	for i := range rep.Turns {
		t := &rep.Turns[i]
		// 读的时候是倒序，一轮里改回先后顺序
		for a, b := 0, len(t.Requests)-1; a < b; a, b = a+1, b-1 {
			t.Requests[a], t.Requests[b] = t.Requests[b], t.Requests[a]
		}
		t.StartedAt = t.Requests[0].AtMS
	}
	rows.Close() // 只读连接只有一条，读下一张表前要放开
	if err := s.fillTurnEnds(ctx, &rep); err != nil {
		return ctlproto.ConvTurnsReport{}, err
	}
	if err := s.fillLatestWindow(ctx, &rep); err != nil {
		return ctlproto.ConvTurnsReport{}, err
	}
	return rep, nil
}

// fillLatestWindow 给最近一次成功、有输入的请求补上它记下的窗口上限（头像上下文环用）。
// 只读这一行的 context_json 里的一个数，别的行照旧不碰这一列（规格-右栏上下文 第九节：大文本列只在点开详情时读）。
// 没记、不是合法 JSON 都当 0（界面退回模型列表里的窗口），不报错。
func (s *Service) fillLatestWindow(ctx context.Context, rep *ctlproto.ConvTurnsReport) error {
	for i := range rep.Turns {
		reqs := rep.Turns[i].Requests
		for j := len(reqs) - 1; j >= 0; j-- {
			if reqs[j].Failed != "" || reqs[j].Input <= 0 {
				continue
			}
			var w sql.NullInt64
			err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN json_valid(context_json) THEN json_extract(context_json, '$.breakdown.window') END
				FROM model_runs WHERE id = ?`, reqs[j].ID).Scan(&w)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return s.queryErr(ctx, err)
			}
			if w.Valid && w.Int64 > 0 {
				reqs[j].Window = w.Int64
			}
			return nil
		}
	}
	return nil
}

// fillTurnEnds 从 turns 表补结束时间和结局；还没写的（进行中）留空。
func (s *Service) fillTurnEnds(ctx context.Context, rep *ctlproto.ConvTurnsReport) error {
	if len(rep.Turns) == 0 {
		return nil
	}
	args := make([]any, len(rep.Turns))
	marks := make([]string, len(rep.Turns))
	index := make(map[string]int, len(rep.Turns))
	for i, t := range rep.Turns {
		args[i], marks[i] = t.ID, "?"
		index[t.ID] = i
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(started_at,0), COALESCE(ended_at,0), COALESCE(outcome,'') FROM turns WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return s.queryErr(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, outcome string
		var started, ended int64
		if err := rows.Scan(&id, &started, &ended, &outcome); err != nil {
			return s.queryErr(ctx, err)
		}
		t := &rep.Turns[index[id]]
		t.EndedAt, t.Outcome = ended, outcome
		if started > 0 && started < t.StartedAt {
			t.StartedAt = started
		}
	}
	if err := rows.Err(); err != nil {
		return s.queryErr(ctx, err)
	}
	return nil
}
