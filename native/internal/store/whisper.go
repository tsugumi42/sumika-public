package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"sumika-native/internal/audience"
)

// 悄悄话的存取（规格-群聊引用和路由 7.1）。悄悄话事件、状态事件、处理记录都存在悄悄话会话
// （audience.IsWhisper）的 events 里，不在来源会话的事件表里：读来源会话的程序（提示词组装、闸门、QQ 发送、
// 记录器、补录、导出、聊天区读取）天然读不到它们。这里的方法只认悄悄话会话串，别的会话一律拒绝。

// 悄悄话会话里除了主人那条悄悄话（kind=message）之外的两种事件。history 重建只取 kind=message，所以不会进模型历史。
const (
	KindWhisperState = "whisper_state" // 一次状态变化：排队、处理中、结局
	KindWhisperNote  = "whisper_note"  // 处理记录：她最后写的那段话，结局是完成才会进来源会话的背景
)

// ErrNotWhisper 说给的会话串不是悄悄话会话。
var ErrNotWhisper = errors.New("不是悄悄话会话")

// WhisperRow 是悄悄话会话里读出的一行。
type WhisperRow struct {
	Seq          int64
	ID           string
	Conversation string
	Kind         string
	ActorKind    string
	Text         string
	Segments     string
	CreatedAt    int64
}

// AppendWhisperEvents 在一个事务里写几条事件（悄悄话会话里的），要么都写进去要么都不写；observers[i] 是第 i 条的观察者。
func (s *Store) AppendWhisperEvents(evs []NewEvent, observers [][]string) (seqs []int64, ids []string, err error) {
	if len(evs) != len(observers) {
		return nil, nil, errors.New("事件和观察者数量对不上")
	}
	convs := make([]string, 0, len(evs))
	for _, e := range evs {
		if !audience.IsWhisper(e.Conversation) || e.Channel != "console" {
			return nil, nil, ErrNotWhisper
		}
		convs = append(convs, e.Conversation)
	}
	tx, err := s.Events.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	for i, e := range evs {
		seq, id, err := insertEvent(tx, e)
		if err != nil {
			return nil, nil, err
		}
		if _, err := observe(tx, seq, observers[i]); err != nil {
			return nil, nil, err
		}
		seqs, ids = append(seqs, seq), append(ids, id)
	}
	return seqs, ids, s.commitChanged(tx, convs...)
}

func scanWhisperRows(rows *sql.Rows) ([]WhisperRow, error) {
	defer rows.Close()
	var out []WhisperRow
	for rows.Next() {
		var r WhisperRow
		var seg sql.NullString
		if err := rows.Scan(&r.Seq, &r.ID, &r.Conversation, &r.Kind, &r.ActorKind, &r.Text, &seg, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Segments = seg.String
		out = append(out, r)
	}
	return out, rows.Err()
}

const whisperCols = `seq, id, conversation, kind, actor_kind, text, segments_json, created_at`

// WhisperEvents 读一个悄悄话会话里某种事件，按 seq 升序；kind 空是全部种类。
func (s *Store) WhisperEvents(ctx context.Context, conversation, kind string) ([]WhisperRow, error) {
	if !audience.IsWhisper(conversation) {
		return nil, ErrNotWhisper
	}
	q, args := `SELECT `+whisperCols+` FROM events WHERE conversation = ?`, []any{conversation}
	if kind != "" {
		q, args = q+` AND kind = ?`, append(args, kind)
	}
	rows, err := s.Events.QueryContext(ctx, q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	return scanWhisperRows(rows)
}

// WhisperStateEvents 读所有悄悄话会话里的状态事件（启动恢复、按批号查状态用），按 seq 升序。
// batch 非空只取这一批；列出 limit 条最新的之前先按批号过滤，不全表扫描里的聊天事件（条件里有 kind 和会话前缀）。
func (s *Store) WhisperStateEvents(ctx context.Context, batch string) ([]WhisperRow, error) {
	q, args := `SELECT `+whisperCols+` FROM events WHERE kind = ? AND conversation LIKE 'console:%:whisper:%'`, []any{KindWhisperState}
	if batch != "" {
		q, args = q+` AND json_extract(segments_json, '$.whisper_state.batch') = ?`, append(args, batch)
	}
	rows, err := s.Events.QueryContext(ctx, q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	all, err := scanWhisperRows(rows)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all { // LIKE 的通配不严格：形状再用认串函数过一遍
		if audience.IsWhisper(r.Conversation) {
			out = append(out, r)
		}
	}
	return out, nil
}

// WhisperNoteEvents 读某个角色针对某个来源会话的处理记录，最新的 limit 条，返回时按时间升序。
func (s *Store) WhisperNoteEvents(ctx context.Context, character, source string, limit int) ([]WhisperRow, error) {
	conv := audience.WhisperConversation(character, source)
	rows, err := s.Events.QueryContext(ctx, `SELECT `+whisperCols+` FROM events WHERE conversation = ? AND kind = ? ORDER BY seq DESC LIMIT ?`, conv, KindWhisperNote, limit)
	if err != nil {
		return nil, err
	}
	out, err := scanWhisperRows(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// WhisperSegment 取事件 segments_json 里某个键下的对象；不是 JSON 或没有这个键返回 false。
func WhisperSegment(segments, key string, into any) bool {
	if strings.TrimSpace(segments) == "" {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(segments), &m) != nil || m[key] == nil {
		return false
	}
	return json.Unmarshal(m[key], into) == nil
}
