package memsource

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/memory"
)

// FindWindow 搜索 [sinceAt, untilAt) 毫秒时间窗内她看到的原话，排除指定会话。
// 空 excludeConversation 不排除会话；其他匹配、排序、分页及撤回规则与 Find 相同。
// 只提供存储入口，不接入回合注入。
func (s *Source) FindWindow(ctx context.Context, excludeConversation string, like []string, sinceAt, untilAt int64, limit int) ([]memory.Event, error) {
	if sinceAt >= untilAt {
		return nil, nil
	}
	return s.find(ctx, findScope{excludeConversation: excludeConversation, window: true, sinceAt: sinceAt, untilAt: untilAt}, like, limit)
}

type findScope struct {
	conversation        string
	excludeConversation string
	window              bool
	sinceAt, untilAt    int64
}

// likeLiteral 取 LIKE 模式中最长的必含连续字串，解析与 ESCAPE '\' 一致。
// 不能直接去掉两边的 %：端口也允许通配模式，转义后的 %、_、反斜杠却是正文。
// SQLite LIKE 在 NUL 终止，NUL 后面的内容不能成为候选的必要条件。
func likeLiteral(pattern string) string {
	var best, run []rune
	escaped := false
	flush := func() {
		if len(run) > len(best) {
			best = append(best[:0], run...)
		}
		run = run[:0]
	}
	for _, r := range pattern {
		if r == 0 {
			break
		}
		if escaped {
			run = append(run, r)
			escaped = false
		} else {
			switch r {
			case '\\':
				escaped = true
			case '%', '_':
				flush()
			default:
				run = append(run, r)
			}
		}
	}
	flush()
	return string(best)
}

// shortToken 与迁移里的 lower(substr(...)) 对齐。
// 只折叠 ASCII：SQLite LIKE 不把 Ä/ä 或全角字符视为相等。
func shortToken(text string) string {
	b := []byte(text)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func (s *Source) findQuery(scope findScope, like []string) (string, []any) {
	where := []string{"o.character_id = ?"}
	args := []any{s.character}
	from := "events e INDEXED BY ix_events_time"
	at, seq := "e.created_at", "e.seq"
	var tri, short []string
	if !scope.window {
		for _, pattern := range like {
			literal := likeLiteral(pattern)
			switch n := utf8.RuneCountInString(literal); {
			case n >= 3:
				tri = append(tri, `"`+strings.ReplaceAll(literal, `"`, `""`)+`"`)
			case n > 0:
				short = append(short, shortToken(literal))
			}
		}
	}
	switch {
	case scope.window:
		// 先走有界时间索引；常用单字的全历史倒排表可能远大于这个时间窗。
		where = append(where, "e.created_at >= ? AND e.created_at < ?")
		args = append(args, scope.sinceAt, scope.untilAt)
	case len(tri) > 0:
		from = "events e"
		where = append(where, "e.seq IN (SELECT rowid FROM events_trigram WHERE events_trigram MATCH ?)")
		args = append(args, strings.Join(tri, " AND "))
	case len(short) > 0:
		// 优先双字，减少从常用单字开始的候选读取；按索引顺序取得有效的一页即停。
		for i := 1; i < len(short); i++ {
			if utf8.RuneCountInString(short[i]) > utf8.RuneCountInString(short[0]) {
				short[0], short[i] = short[i], short[0]
			}
		}
		from = "events_short p"
		at, seq = "p.created_at", "p.event_seq"
		remaining := short[1:]
		var anchors []string
		for _, token := range short {
			if utf8.RuneCountInString(token) == utf8.RuneCountInString(short[0]) {
				anchors = append(anchors, token)
			}
		}
		if len(anchors) > 1 {
			// 同长度词以最小倒排表为起点；统计只读覆盖主键，不读正文，也不扫无关词。
			where = append(where, "p.token = (SELECT token FROM events_short WHERE token IN ("+placeholders(len(anchors))+") GROUP BY token ORDER BY count(*) LIMIT 1)")
			args = append(args, anys(anchors)...)
			remaining = short // 起点由 SQL 选择，所以每个原词都要核对。
		} else {
			where = append(where, "p.token = ?")
			args = append(args, short[0])
		}
		// 在读正文之前按主键求其余短词交集，避免稀疏 AND 查询读大量无关正文。
		for i, token := range remaining {
			alias := "p" + strconv.Itoa(i+1)
			from += " CROSS JOIN events_short " + alias + " ON " + alias + ".created_at = p.created_at AND " + alias + ".event_seq = p.event_seq"
			where = append(where, alias+".token = ?")
			args = append(args, token)
		}
		from += " CROSS JOIN events e ON e.seq = p.event_seq"
	}
	if scope.conversation != "" {
		where = append(where, "e.conversation = ?")
		args = append(args, scope.conversation)
	}
	if scope.excludeConversation != "" {
		where = append(where, "e.conversation <> ?")
		args = append(args, scope.excludeConversation)
	}
	for _, pattern := range like {
		where = append(where, `e.text LIKE ? ESCAPE '\'`)
		args = append(args, pattern)
	}
	// CROSS JOIN 固定候选在前，防止 SQLite 从 character_id 的全部观察记录开始。
	return `SELECT ` + cols + ` FROM ` + from + ` CROSS JOIN observations o ON o.event_seq = e.seq
		WHERE ` + strings.Join(where, " AND ") + ` AND (` + at + `, ` + seq + `) < (?, ?)
		ORDER BY ` + at + ` DESC, ` + seq + ` DESC LIMIT ?`, args
}

func (s *Source) find(ctx context.Context, scope findScope, like []string, limit int) ([]memory.Event, error) {
	if len(like) == 0 || limit <= 0 {
		return nil, nil
	}
	query, args := s.findQuery(scope, like)
	args = append(args, int64(1)<<62, int64(1)<<62, findPage)
	var out []memory.Event
	for len(out) < limit {
		rs, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		rows, err := scan(rs)
		if err != nil {
			return nil, err
		}
		if err := s.fill(ctx, rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !r.ev.Revoked && !r.ev.Unsent && len(out) < limit {
				out = append(out, r.ev)
			}
			args[len(args)-3], args[len(args)-2] = r.ev.At, r.ev.Seq
		}
		if len(rows) < findPage {
			break
		}
	}
	return out, nil
}
