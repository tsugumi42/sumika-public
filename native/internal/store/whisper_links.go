package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 悄悄话关联表 whisper_links（trace 库，规格-群聊引用和路由 15.2）：只存“哪条悄悄话引起了哪次处理”，
// 对话日志据此到别的表里取过程。投影只读；写只由悄悄话服务做。

// WhisperLink 是一条悄悄话指给一位角色的那一行。
type WhisperLink struct {
	Batch, Character, Source, WhisperConversation string
	EventSeq                                      int64
	Delivery                                      string
	StepAt                                        int64 // steer_step；本版没有插话，恒为 0
	RootRun                                       string
	NoteSeq                                       int64
	State, Detail                                 string
	SourceBusy, Selftest                          bool
	CreatedAt, StartedAt, EndedAt                 int64
}

// 对话日志一页的批数上限：一页最多这么多批（批次整批给，不切开）。
const WhisperLogBatchMax = 200

// ErrLogCursor 说游标不合法或不属于这个会话。
var ErrLogCursor = errors.New("日志游标不合法")

// PutWhisperLink 写一行（排队时）；同批同角色重复写只更新状态类的列。
func (s *Store) PutWhisperLink(ctx context.Context, l WhisperLink) error {
	if s.Trace == nil {
		return errors.New("没有运行记录库")
	}
	_, err := s.Trace.ExecContext(ctx, `INSERT INTO whisper_links(batch, character_id, source_conversation, whisper_conversation, event_seq, delivery, steer_step, root_run_id, note_seq, state, detail, source_busy, selftest, created_at, started_at, ended_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(batch, character_id) DO UPDATE SET state=excluded.state, detail=excluded.detail, root_run_id=excluded.root_run_id, note_seq=excluded.note_seq, started_at=excluded.started_at, ended_at=excluded.ended_at`,
		l.Batch, l.Character, l.Source, l.WhisperConversation, l.EventSeq, orDefault(l.Delivery, "internal"), l.StepAt, l.RootRun, l.NoteSeq, l.State, l.Detail, b2i(l.SourceBusy), b2i(l.Selftest), l.CreatedAt, l.StartedAt, l.EndedAt)
	return err
}

// SetWhisperLinkState 记状态变化；root 非空时补上根运行，noteSeq、startedAt、endedAt 非 0 时才写。
func (s *Store) SetWhisperLinkState(ctx context.Context, batch, character, state, detail, root string, noteSeq, startedAt, endedAt int64) error {
	if s.Trace == nil {
		return errors.New("没有运行记录库")
	}
	_, err := s.Trace.ExecContext(ctx, `UPDATE whisper_links SET state=?, detail=?,
		root_run_id = CASE WHEN ? <> '' THEN ? ELSE root_run_id END,
		note_seq = CASE WHEN ? > 0 THEN ? ELSE note_seq END,
		started_at = CASE WHEN ? > 0 THEN ? ELSE started_at END,
		ended_at = CASE WHEN ? > 0 THEN ? ELSE ended_at END
		WHERE batch=? AND character_id=?`, state, detail, root, root, noteSeq, noteSeq, startedAt, startedAt, endedAt, endedAt, batch, character)
	return err
}

// LogCursor 是游标：上一页最后一批的 (created_at, batch)，绑定来源会话。
type LogCursor struct {
	Source string
	At     int64
	Batch  string
}

// Encode 把游标写成字符串（给界面和 ctl 原样带回来）。
func (c LogCursor) Encode() string {
	return strconv.FormatInt(c.At, 10) + "|" + c.Batch + "|" + c.Source
}

// ParseLogCursor 解游标；空串是第一页。来源对不上（换了会话的游标）报 ErrLogCursor。
func ParseLogCursor(s, source string) (LogCursor, error) {
	if s == "" {
		return LogCursor{Source: source}, nil
	}
	p := strings.SplitN(s, "|", 3)
	if len(p) != 3 || p[2] != source {
		return LogCursor{}, ErrLogCursor
	}
	at, err := strconv.ParseInt(p[0], 10, 64)
	if err != nil || at < 0 || p[1] == "" {
		return LogCursor{}, ErrLogCursor
	}
	return LogCursor{Source: source, At: at, Batch: p[1]}, nil
}

const linkCols = `batch, character_id, source_conversation, whisper_conversation, event_seq, delivery, steer_step, root_run_id, note_seq, state, detail, source_busy, selftest, created_at, started_at, ended_at`

// WhisperLinkBatches 取来源会话里最新的 batches 批（按 created_at 降序），每批带它的全部角色行；
// after 是上一页的游标（空是第一页）。整批一起返回，批次不被页界切开。
// 走 (source_conversation, created_at, batch) 索引按序读，凑够批数就停，不随历史总量增长。
func (s *Store) WhisperLinkBatches(ctx context.Context, source string, after LogCursor, batches int) ([]WhisperLink, error) {
	if s.Trace == nil {
		return nil, errors.New("没有运行记录库")
	}
	if batches <= 0 || batches > WhisperLogBatchMax {
		return nil, fmt.Errorf("每页批数要在 1 到 %d 之间", WhisperLogBatchMax)
	}
	q := `SELECT created_at, batch FROM whisper_links WHERE source_conversation = ?`
	args := []any{source}
	if after.Batch != "" {
		q += ` AND (created_at, batch) < (?, ?)`
		args = append(args, after.At, after.Batch)
	}
	rows, err := s.Trace.QueryContext(ctx, q+` ORDER BY created_at DESC, batch DESC`, args...)
	if err != nil {
		return nil, err
	}
	var picks []string
	for rows.Next() {
		var at int64
		var b string
		if err := rows.Scan(&at, &b); err != nil {
			rows.Close()
			return nil, err
		}
		if len(picks) == 0 || picks[len(picks)-1] != b {
			if len(picks) == batches {
				break
			}
			picks = append(picks, b)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(picks) == 0 {
		return nil, nil
	}
	in := make([]any, 0, len(picks)+1)
	for _, b := range picks {
		in = append(in, b)
	}
	in = append(in, source)
	return s.scanLinks(ctx, `SELECT `+linkCols+` FROM whisper_links WHERE batch IN (`+strings.TrimSuffix(strings.Repeat("?,", len(picks)), ",")+`) AND source_conversation = ? ORDER BY created_at DESC, batch DESC, character_id`, in...)
}

// WhisperLinksOf 取一批的全部行。
func (s *Store) WhisperLinksOf(ctx context.Context, batch string) ([]WhisperLink, error) {
	if s.Trace == nil {
		return nil, errors.New("没有运行记录库")
	}
	return s.scanLinks(ctx, `SELECT `+linkCols+` FROM whisper_links WHERE batch = ? ORDER BY character_id`, batch)
}

// WhisperLinksRecent 列最近的几行（whisper list 用）：selftest 为真只列自检的，为假只列真实的。
func (s *Store) WhisperLinksRecent(ctx context.Context, selftest bool, limit int) ([]WhisperLink, error) {
	if s.Trace == nil {
		return nil, errors.New("没有运行记录库")
	}
	return s.scanLinks(ctx, `SELECT `+linkCols+` FROM whisper_links WHERE selftest = ? ORDER BY created_at DESC, batch DESC, character_id LIMIT ?`, b2i(selftest), limit)
}

func (s *Store) scanLinks(ctx context.Context, q string, args ...any) ([]WhisperLink, error) {
	rows, err := s.Trace.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhisperLink
	for rows.Next() {
		var l WhisperLink
		var busy, self int
		if err := rows.Scan(&l.Batch, &l.Character, &l.Source, &l.WhisperConversation, &l.EventSeq, &l.Delivery, &l.StepAt, &l.RootRun, &l.NoteSeq, &l.State, &l.Detail, &busy, &self, &l.CreatedAt, &l.StartedAt, &l.EndedAt); err != nil {
			return nil, err
		}
		l.SourceBusy, l.Selftest = busy != 0, self != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
