package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// 来源身份由原始事件核对；subject、模型自报来源和范围标签都不能证明是谁说的。
func fillSourceRefs(db *sql.DB, items []Item) error {
	by := refIndex(items)
	for _, table := range []string{"facts", "episodes"} {
		column := "source_seqs"
		fields := "id, source_seqs"
		prefix := "f:"
		if table == "episodes" {
			column = "from_seq"
			fields = "id, from_seq, to_seq"
			prefix = "e:"
		}
		var have int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&have); err != nil {
			return err
		}
		if have == 0 {
			continue
		}
		rows, err := db.Query(`SELECT ` + fields + ` FROM ` + table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var seqs sql.NullString
			var from, to sql.NullInt64
			if table == "facts" {
				err = rows.Scan(&id, &seqs)
			} else {
				err = rows.Scan(&id, &from, &to)
			}
			if err != nil {
				rows.Close()
				return err
			}
			if it := by[prefix+id]; it != nil {
				if table == "facts" {
					if json.Unmarshal([]byte(seqs.String), &it.SourceSeqs) != nil {
						it.SourceSeqs = nil
					}
				} else {
					it.FromSeq, it.ToSeq = from.Int64, to.Int64
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if table == "facts" {
			if err := fillWhisperSources(db, by); err != nil {
				return err
			}
			if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('facts') WHERE name='learned_by'`).Scan(&have); err != nil {
				return err
			}
			if have > 0 {
				r, err := db.Query(`SELECT id, coalesce(learned_by,'') FROM facts`)
				if err != nil {
					return err
				}
				for r.Next() {
					var id, char string
					if err = r.Scan(&id, &char); err != nil {
						r.Close()
						return err
					}
					if it := by["f:"+id]; it != nil {
						it.LearnedBy = char
					}
				}
				err = r.Err()
				r.Close()
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// fillWhisperSources 读悄悄话处理那一轮写下的事实的来源（迁移 0003 的 whisper_json；没迁移的库没有这一列，按没有读）
func fillWhisperSources(db *sql.DB, by map[string]*Item) error {
	var have int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('facts') WHERE name='whisper_json'`).Scan(&have); err != nil || have == 0 {
		return err
	}
	rows, err := db.Query(`SELECT id, whisper_json FROM facts WHERE whisper_json IS NOT NULL AND whisper_json <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if it := by["f:"+id]; it != nil {
			it.Whisper = parseWhisper(raw)
		}
	}
	return rows.Err()
}

func sourcePlace(conv string) string {
	switch PlaceOf(ChannelOf(conv), conv, "") {
	case LocalPrivate:
		return "本地私聊"
	case LocalGroup:
		return "本地群聊"
	case PlatformPrivate:
		return "平台私聊"
	default:
		return "平台群聊"
	}
}

// 只装饰最终给模型看的条目，原话按会话定位，跨平台同 seq 不会串源。
func (m *Memory) labelSources(ctx context.Context, hits []Hit) {
	defer func() {
		// 悄悄话来的事实，不论后面哪一步认没认出原话，来源前面都先写明“这是从某次悄悄话来的”
		for i := range hits {
			if hits[i].Whisper != nil {
				hits[i].Source = hits[i].Whisper.Mark() + "；" + hits[i].Source
			}
		}
	}()
	for i := range hits {
		h := &hits[i]
		h.Source = "来源未核实"
		if h.Conversation != "" {
			h.Source += "；记录会话=" + escapeUserText(CleanLine(h.Conversation, 200))
		}
		if h.Conversation == "" {
			continue
		}
		events := m.opt.Events
		if h.LearnedBy != "" && h.LearnedBy != m.opt.Character && m.opt.Shared != nil && m.opt.Shared.opt.SourceEvents != nil {
			var err error
			events, err = m.opt.Shared.opt.SourceEvents(h.LearnedBy)
			if err != nil {
				m.logf("记忆来源：ref=%s reason=events_error：%v", h.Ref, err)
				continue
			}
		}
		if events == nil {
			continue
		}
		seqs := h.SourceSeqs
		if h.Type == "fact" {
			if len(seqs) == 0 {
				continue
			}
			// src 最多十条，逐个精确读取，避免把不在引用里的主人发言当来源。
			var labels []string
			valid := true
			for _, s := range seqs {
				if s <= 0 {
					valid = false
					break
				}
				evs, err := events.Events(ctx, h.Conversation, s-1, 1)
				if err != nil {
					m.logf("记忆来源：ref=%s reason=events_error：%v", h.Ref, err)
					valid = false
					break
				}
				if len(evs) != 1 || evs[0].Seq != s || !sourceEventOK(evs[0], h.Conversation) {
					valid = false
					break
				}
				who, tag, _ := m.speaker(evs[0])
				label := who + tag
				found := false
				for _, v := range labels {
					if v == label {
						found = true
					}
				}
				if !found {
					labels = append(labels, label)
				}
			}
			if valid {
				h.Source = strings.Join(labels, "、") + "在" + sourcePlace(h.Conversation) + "说的；会话=" + escapeUserText(CleanLine(h.Conversation, 200))
			}
		} else {
			if h.FromSeq <= 0 || h.ToSeq < h.FromSeq {
				continue
			}
			evs, err := events.Events(ctx, h.Conversation, h.FromSeq-1, maxBatchEvents)
			if err != nil {
				m.logf("记忆来源：ref=%s reason=events_error：%v", h.Ref, err)
				continue
			}
			var labels []string
			for _, ev := range evs {
				if ev.Seq > h.ToSeq {
					break
				}
				if !sourceEventOK(ev, h.Conversation) {
					continue
				}
				who, tag, _ := m.speaker(ev)
				label := who + tag
				found := false
				for _, v := range labels {
					if v == label {
						found = true
					}
				}
				if !found {
					labels = append(labels, label)
				}
			}
			if len(labels) > 0 {
				h.Source = strings.Join(labels, "、") + "在" + sourcePlace(h.Conversation) + "的对话整理；会话=" + escapeUserText(CleanLine(h.Conversation, 200))
			}
		}
	}
}

func sourceEventOK(ev Event, conv string) bool {
	return ev.Conversation == conv && ev.Kind == "message" && ev.Seen && !ev.Revoked && !ev.Unsent
}

func sourceMark(h Hit) string {
	source := h.Source
	if source == "" {
		source = "来源未核实"
		if h.Conversation != "" {
			source += "；记录会话=" + escapeUserText(CleanLine(h.Conversation, 200))
		}
	}
	return fmt.Sprintf("〔来源：%s〕", source)
}
