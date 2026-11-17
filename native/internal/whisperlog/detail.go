package whisperlog

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/store"
)

// 一条全文的字数上限：思考最多 32 KB（trace 里本来就是这个封顶），其余也不无限读。
const detailMax = 32 * 1024

// lineBreaks 把所有会显示成换行的写法统一成 \n（先换 \r\n，再换单独的 \r）。
var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", " ", "\n", "\u0085", "\n", "\v", "\n", "\f", "\n")

// Detail 读一条的全文。id 必须属于 source 这个来源会话的某一批，否则一律报 ErrNoEntry（不区分“没有”和“不是这个会话的”）。
// withThinking 为假时思考原文不给，只标 Hidden（ctl 默认；界面点开时传真）。
func (r *Reader) Detail(ctx context.Context, source, id string, withThinking bool) (ctlproto.LogDetail, error) {
	d, err := r.detail(ctx, source, id, withThinking)
	if err != nil {
		return ctlproto.LogDetail{}, err
	}
	return ctlproto.CleanLogDetail(d), nil // 外来文字出去前过控制字符和方向覆盖符
}

func (r *Reader) detail(ctx context.Context, source, id string, withThinking bool) (ctlproto.LogDetail, error) {
	if r.St == nil || source == "" {
		return ctlproto.LogDetail{}, ErrBadRequest
	}
	kind, rest, ok := strings.Cut(id, ":")
	if !ok || rest == "" {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	switch kind {
	case "say", "arr", "note":
		batch, char, _ := strings.Cut(rest, ":")
		links, err := r.St.WhisperLinksOf(ctx, batch)
		if err != nil {
			return ctlproto.LogDetail{}, err
		}
		var mine []store.WhisperLink
		for _, l := range links {
			if l.Source == source && (kind == "say" || l.Character == char) {
				mine = append(mine, l)
			}
		}
		if len(mine) == 0 {
			return ctlproto.LogDetail{}, ErrNoEntry
		}
		return r.batchDetail(ctx, kind, id, mine)
	case "step":
		return r.stepDetail(ctx, source, id, rest, withThinking)
	case "task", "ret":
		return r.taskDetail(ctx, source, id, kind, rest)
	}
	return ctlproto.LogDetail{}, ErrNoEntry
}

func (r *Reader) batchDetail(ctx context.Context, kind, id string, links []store.WhisperLink) (ctlproto.LogDetail, error) {
	l := links[0]
	switch kind {
	case "say":
		we, err := r.whisperEvents(ctx, links)
		if err != nil {
			return ctlproto.LogDetail{}, err
		}
		var b strings.Builder
		seen := map[string]bool{}
		for _, l := range links {
			for _, p := range we[l.EventSeq] {
				if p.Quote != nil && !seen[p.Quote.ID] {
					seen[p.Quote.ID] = true
					// 被引的是别人的话：每行前面加“｜ ”，里面写得再像“对 X 说：”也冒充不了下面主人的那一行
					// 先把各种换行（\r\n、单独的 \r、U+2028/2029、U+0085、\v、\f）统一成 \n 再加前缀：不然原文里的 \r 或 U+2028
					// 显示成换行却没被加“｜ ”，能伪造出一行“对 X 说：”（2026-11-11 Bun 复核发现，两边都有）
					b.WriteString("引用 " + oneLine(p.Quote.Name, 40) + "：\n｜ " + strings.ReplaceAll(strings.TrimSpace(lineBreaks.Replace(p.Quote.Text)), "\n", "\n｜ ") + "\n\n")
				}
			}
		}
		for _, l := range links {
			var texts []string
			for _, p := range we[l.EventSeq] {
				if t := strings.TrimSpace(p.Text); t != "" {
					texts = append(texts, t)
				}
			}
			if len(texts) > 0 {
				b.WriteString("对 " + l.Character + " 说：" + strings.Join(texts, " / ") + "\n")
			}
		}
		return ctlproto.LogDetail{ID: id, Kind: ctlproto.LogWhisper, Title: "悄悄话", Text: clipBytes(strings.TrimSpace(b.String()), detailMax)}, nil
	case "arr":
		text := "状态：" + l.State
		if l.Detail != "" {
			text += "\n" + l.Detail
		}
		if l.SourceBusy {
			text += "\n悄悄话到的时候她在来源会话里有回合在跑（没打断，后台处理）"
		}
		return ctlproto.LogDetail{ID: id, Kind: ctlproto.LogArrange, Title: "安排 " + l.Character, Text: text}, nil
	}
	if l.NoteSeq == 0 {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	var text string
	err := r.St.Events.QueryRowContext(ctx, `SELECT text FROM events WHERE seq = ? AND kind = ? AND conversation = ?`, l.NoteSeq, store.KindWhisperNote, l.WhisperConversation).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	if err != nil {
		return ctlproto.LogDetail{}, err
	}
	return ctlproto.LogDetail{ID: id, Kind: ctlproto.LogNote, Title: "处理记录 " + l.Character, Text: clipBytes(text, detailMax)}, nil
}

// owns 说这个回合是不是来源会话 source 里某一批的处理：回合 id 等于关联里的 root，或（还在处理、没拿到 root 时）是她悄悄话会话里
// 开始处理之后最早的一轮。不属于这个来源的一律当没有。
func (r *Reader) owns(ctx context.Context, source, turn string) (store.WhisperLink, bool, error) {
	var t turnRow
	err := r.St.Trace.QueryRowContext(ctx, `SELECT id, character_id, conversation FROM turns WHERE id = ?`, turn).Scan(&t.ID, &t.Character, &t.Conversation)
	if errors.Is(err, sql.ErrNoRows) {
		return store.WhisperLink{}, false, nil
	}
	if err != nil {
		return store.WhisperLink{}, false, err
	}
	rows, err := r.St.Trace.QueryContext(ctx, `SELECT batch FROM whisper_links WHERE source_conversation = ? AND whisper_conversation = ? AND character_id = ? AND (root_run_id = ? OR (root_run_id = '' AND started_at > 0))`,
		source, t.Conversation, t.Character, turn)
	if err != nil {
		return store.WhisperLink{}, false, err
	}
	var batches []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return store.WhisperLink{}, false, err
		}
		batches = append(batches, b)
	}
	if err := rows.Close(); err != nil {
		return store.WhisperLink{}, false, err
	}
	for _, b := range batches {
		links, err := r.St.WhisperLinksOf(ctx, b)
		if err != nil {
			return store.WhisperLink{}, false, err
		}
		for _, l := range links {
			if l.Source != source || l.Character != t.Character {
				continue
			}
			if l.RootRun == turn {
				return l, true, nil
			}
			if l.RootRun == "" && l.StartedAt > 0 {
				turns, err := r.turnsOf(ctx, []store.WhisperLink{l})
				if err != nil {
					return store.WhisperLink{}, false, err
				}
				if got, ok := turns[l.Character+"|"+l.Batch]; ok && got.ID == turn {
					return l, true, nil
				}
			}
		}
	}
	return store.WhisperLink{}, false, nil
}

func (r *Reader) stepDetail(ctx context.Context, source, id, stepID string, withThinking bool) (ctlproto.LogDetail, error) {
	var turn, kind, name, preview, args, approval string
	var isErr int
	err := r.St.Trace.QueryRowContext(ctx, `SELECT COALESCE(turn_id,''), kind, COALESCE(name,''), substr(COALESCE(preview,''),1,?), COALESCE(args_json,''), COALESCE(is_error,0), COALESCE(approval_json,'') FROM steps WHERE id = ?`,
		detailMax, stepID).Scan(&turn, &kind, &name, &preview, &args, &isErr, &approval)
	if errors.Is(err, sql.ErrNoRows) {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	if err != nil {
		return ctlproto.LogDetail{}, err
	}
	if _, ok, err := r.owns(ctx, source, turn); err != nil {
		return ctlproto.LogDetail{}, err
	} else if !ok {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	d := ctlproto.LogDetail{ID: id, Kind: ctlproto.LogTool, Title: name, Text: preview, Args: clipBytes(args, detailMax)}
	switch {
	case kind == "thinking":
		d.Kind, d.Title = ctlproto.LogThink, "思考"
		if !withThinking {
			d.Text, d.Hidden = "", true
		}
	case isMemoryTool(name):
		d.Kind = ctlproto.LogMemory
	}
	if approval != "" && kind != "thinking" {
		d.Text += "\n\n批准：" + approval
	}
	if isErr != 0 {
		d.Text = "（出错）" + d.Text
	}
	return d, nil
}

func (r *Reader) taskDetail(ctx context.Context, source, id, kind, taskID string) (ctlproto.LogDetail, error) {
	var parent, state, payload, result string
	err := r.St.Trace.QueryRowContext(ctx, `SELECT parent_run, state, substr(payload,1,?), substr(result,1,?) FROM kernel_tasks WHERE id = ?`, detailMax, detailMax, taskID).Scan(&parent, &state, &payload, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	if err != nil {
		return ctlproto.LogDetail{}, err
	}
	if _, ok, err := r.owns(ctx, source, parent); err != nil {
		return ctlproto.LogDetail{}, err
	} else if !ok {
		return ctlproto.LogDetail{}, ErrNoEntry
	}
	if kind == "task" {
		return ctlproto.LogDetail{ID: id, Kind: ctlproto.LogSpawn, Title: "唤起子任务", Text: payload}, nil
	}
	return ctlproto.LogDetail{ID: id, Kind: ctlproto.LogReturn, Title: "子任务返回（" + state + "）", Text: result}, nil
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
