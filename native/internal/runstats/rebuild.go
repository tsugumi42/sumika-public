package runstats

// 重建留痕的读取（规格-提示词前缀稳定第八节）：每天、每个场合重建几次、为什么、丢了几条、有没有摘要覆盖。
// 数据在 model_runs.context_json 的 rebuild 键里（只在重建后的第一步写）；rebuild 列不为空而没有这个键的是上线前的旧记录，单列。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"sumika-native/internal/ctlproto"
)

// rebuildPlaces 是场合的显示顺序
var rebuildPlaces = []string{ctlproto.ScenePlaceLocalPrivate, ctlproto.ScenePlaceLocalGroup, ctlproto.ScenePlaceQQPrivate, ctlproto.ScenePlaceQQGroup,
	ctlproto.ScenePlaceWhisper, ctlproto.ScenePlaceUnknown}

// rebuildTrace 是 context_json 里 rebuild 键的内容（和 turn.RebuildTrace 的 JSON 一致；这里不引 turn，只按字段名读）
type rebuildTrace struct {
	Reason  string `json:"reason"`
	Kept    int64  `json:"kept"`
	Dropped int64  `json:"dropped"`
	Covered int64  `json:"covered"`
	Count   string `json:"count"`
}

type rebuildAcc struct {
	row     ctlproto.RebuildRow
	reasons map[string]int64
	kept    int64
}

func (a *rebuildAcc) add(t *rebuildTrace) {
	a.row.Rebuilds++
	if t == nil {
		a.row.Untraced++
		return
	}
	if a.reasons == nil {
		a.reasons = map[string]int64{}
	}
	a.reasons[t.Reason]++
	if t.Count != "exact" {
		a.row.NotCounted++ // 没数出来的：不进平均和最大，也不当 0
		return
	}
	a.row.Counted++
	a.row.DroppedTotal += t.Dropped
	a.row.DroppedMax = max(a.row.DroppedMax, t.Dropped)
	a.kept += t.Kept
	if t.Covered > 0 {
		a.row.Covered++
	}
}

func (a *rebuildAcc) finish() ctlproto.RebuildRow {
	r := a.row
	if r.Counted > 0 {
		r.DroppedAvg = float64(r.DroppedTotal) / float64(r.Counted)
		r.KeptAvg = float64(a.kept) / float64(r.Counted)
	}
	for id, n := range a.reasons {
		r.Reasons = append(r.Reasons, ctlproto.RebuildReasonCount{Reason: id, Count: n})
	}
	sort.Slice(r.Reasons, func(i, j int) bool {
		if r.Reasons[i].Count != r.Reasons[j].Count {
			return r.Reasons[i].Count > r.Reasons[j].Count
		}
		return r.Reasons[i].Reason < r.Reasons[j].Reason
	})
	return r
}

// Rebuild 汇总 r 这段时间的重建：按场合的合计（Places）和按天、场合的明细（Rows，新的天在前）。
// 场合按会话串算（placeOf），报告里不带会话串。只读
func (s *Service) Rebuild(ctx context.Context, r ctlproto.UsageRange) (ctlproto.RebuildReport, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if r.FromMS < 0 || r.ToMS <= r.FromMS {
		return ctlproto.RebuildReport{}, errors.New("时间范围不对")
	}
	// 回复的第一步才有 rebuild 列；context_json 不是合法 JSON 的行 json_extract 会报错，先用 json_valid 挡住（按没有留痕算旧记录）
	rows, err := s.db.QueryContext(ctx, `SELECT started_at, conversation,
 CASE WHEN json_valid(context_json) THEN json_extract(context_json, '$.rebuild') END
 FROM model_runs WHERE stage = 'reply' AND rebuild IS NOT NULL AND started_at >= ? AND started_at < ?`, r.FromMS, r.ToMS)
	if err != nil {
		return ctlproto.RebuildReport{}, s.queryErr(ctx, err)
	}
	defer rows.Close()
	type key struct{ day, place string }
	days := map[key]*rebuildAcc{}
	places := map[string]*rebuildAcc{}
	for rows.Next() {
		var at int64
		var conv string
		var raw sql.NullString
		if err := rows.Scan(&at, &conv, &raw); err != nil {
			return ctlproto.RebuildReport{}, s.queryErr(ctx, err)
		}
		var tr *rebuildTrace
		if raw.Valid && raw.String != "" {
			var t rebuildTrace
			if json.Unmarshal([]byte(raw.String), &t) == nil && t.Reason != "" {
				tr = &t
			}
		}
		place := placeOf(conv)
		k := key{time.UnixMilli(at).In(s.loc).Format("2006-01-02"), place}
		d := days[k]
		if d == nil {
			d = &rebuildAcc{row: ctlproto.RebuildRow{Date: k.day, Place: place}}
			days[k] = d
		}
		p := places[place]
		if p == nil {
			p = &rebuildAcc{row: ctlproto.RebuildRow{Place: place}}
			places[place] = p
		}
		d.add(tr)
		p.add(tr)
	}
	if err := rows.Err(); err != nil {
		return ctlproto.RebuildReport{}, s.queryErr(ctx, err)
	}
	rep := ctlproto.RebuildReport{Range: r}
	order := func(place string) int {
		for i, p := range rebuildPlaces {
			if p == place {
				return i
			}
		}
		return len(rebuildPlaces)
	}
	for _, p := range places {
		rep.Places = append(rep.Places, p.finish())
	}
	sort.Slice(rep.Places, func(i, j int) bool { return order(rep.Places[i].Place) < order(rep.Places[j].Place) })
	for _, d := range days {
		rep.Rows = append(rep.Rows, d.finish())
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return order(a.Place) < order(b.Place)
	})
	return rep, nil
}
