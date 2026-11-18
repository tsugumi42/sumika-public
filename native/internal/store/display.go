package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// DisplayScope 由装配方给出可查看的角色，不从显示名推断权限。
type DisplayScope struct {
	Platform, Conversation, Character string
	Characters                        []string
	// ShownOnly 只要聊天区会显示的行：普通消息和宿主写的系统行。撤回、回应、进群这些通知聊天区不显示，
	// 会话列表的预览也不能拿它们当最后一条（规则和根包 showsAsRow 同一条，改一处要改两处）。
	ShownOnly bool
}
type DisplayRow struct {
	Seq                                                                                  int64
	ID, Channel, Conversation, ActorKind, ActorID, Kind, Text, Segments, ReplyTo, Origin string
	DisplayName                                                                          string
	ActorUID                                                                             string // 发言人的 QQ 号（display_names.go）：群友取自 actor_id，主人取自段里的 from.id；取不到为空
	CreatedAt                                                                            int64
	Recalled                                                                             bool
	Unsent, Partial, Aborted                                                             bool
	HasPlan                                                                              bool
	Reply                                                                                *DisplayRow
	Reactions                                                                            []DisplayReaction  // 被贴的表情，按时间排（display_reactions.go）
	AtNames                                                                              map[string]string  // 被 @ 的号 → 显示名（display_names.go），取不到的不在里面
	MediaMeta                                                                            map[int]MediaFetch // 图片段的已存元数据，按原段下标取
	// QuoteTargets 是主人分块消息里每个引用块指向的那条（含第一个，和 Reply 同一行的拷贝），按事件 id 取；
	// 范围和可见性与 Reply 同一套，看不到的不在里面。
	QuoteTargets map[string]DisplayRow
}
type DisplayPage struct {
	Items      []DisplayRow
	NextCursor string
	HasMore    bool
	HighWater  int64
	// Before 是这一页最老一条再往前的那条消息（kind=message、同一个观察范围）；没有更早的消息时为空。
	// 只给界面算最老那一行"是不是一组的第一条、是不是新的一天"用，让它不用等前面的行加载进来才知道。没做 enrichDisplay，只有基本字段。
	Before *DisplayRow
}
type displayCursor struct {
	Platform, Conversation, Character string
	At, Seq                           int64
}

var ErrDisplayScope = errors.New("会话不在可见范围内")
var ErrDisplayParameter = errors.New("展示查询参数错误")

func containsCharacter(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// 校验和查询共用同一个事务，话题所有权不能在两次读取之间变化。
func validateDisplay(ctx context.Context, tx *sql.Tx, scope DisplayScope) error {
	platform, err := PlatformOfConversation(scope.Conversation)
	if err != nil || platform != scope.Platform || len(scope.Characters) == 0 {
		return ErrDisplayScope
	}
	if scope.Character != "" && !containsCharacter(scope.Characters, scope.Character) {
		return ErrDisplayScope
	}
	parts := strings.Split(scope.Conversation, ":")
	if platform == "qq" {
		if (len(parts) == 3 && parts[1] == "group" && parts[2] != "") || (len(parts) == 4 && parts[1] == "private" && parts[2] != "" && parts[3] != "") {
			return nil
		}
		return ErrDisplayScope
	}
	if platform == "local" {
		if id, ok := LocalGroupID(scope.Conversation); ok {
			return validateLocalGroupDisplay(ctx, tx, scope, id)
		}
	}
	if platform != "local" || len(parts) < 2 || !containsCharacter(scope.Characters, parts[1]) || (scope.Character != "" && scope.Character != parts[1]) {
		return ErrDisplayScope
	}
	if len(parts) == 2 {
		return nil
	}
	if len(parts) != 4 || parts[2] != "topic" || parts[3] == "" {
		return ErrDisplayScope
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT character_id FROM local_topics WHERE id = ?`, parts[3]).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDisplayScope
		}
		return err
	}
	if owner != parts[1] {
		return ErrDisplayScope
	}
	return nil
}

func displayFilter(scope DisplayScope, alias string) (string, []any) {
	// 定期任务还没分类的和回了 [无事] 的行不显示（history.NotSaidSQL 里后两项），所有读取走这一个过滤
	hidden := `COALESCE(` + alias + `.origin,'') NOT IN ('automation:loop-pending', 'automation:loop-quiet')`
	if scope.ShownOnly {
		hidden += ` AND (` + alias + `.kind = 'message' OR (` + alias + `.kind = 'notice' AND ` + alias + `.actor_kind = 'system'))`
	}
	if scope.Platform != "qq" {
		return hidden, nil
	}
	ids := scope.Characters
	if scope.Character != "" {
		ids = []string{scope.Character}
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	return hidden + ` AND EXISTS (SELECT 1 FROM observations o WHERE o.event_seq = ` + alias + `.seq AND o.character_id IN (` + strings.Join(marks, ",") + `))`, args
}

const displayColumns = `e.seq,e.id,e.channel,e.conversation,e.actor_kind,e.actor_id,e.kind,e.text,COALESCE(e.segments_json,''),COALESCE(e.reply_to,''),COALESCE(e.origin,''),e.created_at`

func scanDisplay(rows *sql.Rows) ([]DisplayRow, error) {
	var out []DisplayRow
	for rows.Next() {
		var r DisplayRow
		if err := rows.Scan(&r.Seq, &r.ID, &r.Channel, &r.Conversation, &r.ActorKind, &r.ActorID, &r.Kind, &r.Text, &r.Segments, &r.ReplyTo, &r.Origin, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReadDisplay 保留通知和未发送记录；游标只用于时间排序，不兼任新增水位。
func (s *Store) ReadDisplay(ctx context.Context, scope DisplayScope, cursor string, limit int) (DisplayPage, error) {
	if limit <= 0 || limit > 1000 {
		return DisplayPage{}, ErrDisplayParameter
	}
	var c displayCursor
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return DisplayPage{}, ErrDisplayParameter
		}
		if json.Unmarshal(data, &c) != nil || c.Platform != scope.Platform || c.Conversation != scope.Conversation || c.Character != scope.Character || c.Seq <= 0 {
			return DisplayPage{}, ErrDisplayParameter
		}
	}
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DisplayPage{}, err
	}
	defer tx.Rollback()
	if err := validateDisplay(ctx, tx, scope); err != nil {
		return DisplayPage{}, err
	}
	var page DisplayPage
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&page.HighWater); err != nil {
		return DisplayPage{}, err
	}
	filter, args := displayFilter(scope, "e")
	query := `SELECT ` + displayColumns + ` FROM events e INDEXED BY ix_events_conv_time WHERE e.conversation = ? AND ` + filter
	args = append([]any{scope.Conversation}, args...)
	if cursor != "" {
		query += ` AND (e.created_at < ? OR (e.created_at = ? AND e.seq < ?))`
		args = append(args, c.At, c.At, c.Seq)
	}
	query += ` ORDER BY e.created_at DESC,e.seq DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return DisplayPage{}, err
	}
	page.Items, err = scanDisplay(rows)
	rows.Close()
	if err != nil {
		return DisplayPage{}, err
	}
	page.HasMore = len(page.Items) > limit
	if page.HasMore {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		data, _ := json.Marshal(displayCursor{scope.Platform, scope.Conversation, scope.Character, last.CreatedAt, last.Seq})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
		if page.Before, err = readBefore(ctx, tx, scope, last.CreatedAt, last.Seq); err != nil {
			return DisplayPage{}, err
		}
	}
	if err := enrichDisplay(ctx, tx, scope, page.Items); err != nil {
		return DisplayPage{}, err
	}
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page, tx.Commit()
}

// 引用目标一次、撤回和回应一次（readNotices）批量读取，目标可在另一页，但不能跨越观察范围。
func enrichDisplay(ctx context.Context, tx *sql.Tx, scope DisplayScope, items []DisplayRow) error {
	if len(items) == 0 {
		return nil
	}
	refs := map[string]bool{}
	for _, r := range items {
		refs[r.ID] = true
		if r.ReplyTo != "" {
			refs[r.ReplyTo] = true
		}
		for _, id := range localPartQuoteIDs(r.Segments) {
			refs[id] = true
		}
	}
	filter, fa := displayFilter(scope, "e")
	marks := make([]string, 0, len(refs))
	args := make([]any, 0, len(refs)+1+len(fa))
	for id := range refs {
		marks = append(marks, "(?)")
		args = append(args, id)
	}
	args = append(args, scope.Conversation)
	args = append(args, fa...)
	// 目标表在外层逐 ID 走唯一索引，避免 IN 列表较长时改扫整个会话。
	rows, err := tx.QueryContext(ctx, `WITH wanted(id) AS (VALUES `+strings.Join(marks, ",")+`) SELECT `+displayColumns+` FROM wanted CROSS JOIN events e WHERE e.id=wanted.id AND e.conversation=? AND `+filter, args...)
	if err != nil {
		return err
	}
	targets, err := scanDisplay(rows)
	rows.Close()
	if err != nil {
		return err
	}
	byID := make(map[string]DisplayRow, len(targets))
	for _, r := range targets {
		byID[r.ID] = r
	}
	recalled, reactions, err := readNotices(ctx, tx, scope, refs)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Recalled = recalled[items[i].ID]
		if r, ok := byID[items[i].ReplyTo]; ok {
			r.Recalled = recalled[r.ID]
			items[i].Reply = &r
		}
		items[i].Reactions = reactions[items[i].ID]
		for _, id := range localPartQuoteIDs(items[i].Segments) {
			if r, ok := byID[id]; ok {
				r.Recalled = recalled[r.ID]
				if items[i].QuoteTargets == nil {
					items[i].QuoteTargets = map[string]DisplayRow{}
				}
				items[i].QuoteTargets[id] = r
			}
		}
	}
	if err := enrichDisplayNames(ctx, tx, scope, items); err != nil {
		return err
	}
	if err := enrichDisplayMedia(ctx, tx, items); err != nil {
		return err
	}
	return enrichDisplayState(ctx, tx, items)
}
