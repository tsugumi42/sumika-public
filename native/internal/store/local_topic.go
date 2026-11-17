package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// CreateLocalTopic 给 character 新建一个话题（local_topics 里的一行，会话列表按标题列出它）。
// 给"每次新聊天"的定期任务用，by 写是谁建的（"loop"）。id 已经存在就不动，返回是不是这次新建的；
// 已存在但属于别的角色算失败：不改别人的话题。
func (s *Store) CreateLocalTopic(ctx context.Context, character, id, title, by string, now int64) (bool, error) {
	if character == "" || strings.ContainsAny(character, ": \t\r\n") || id == "" || strings.ContainsAny(id, ": \t\r\n") {
		return false, errors.New("话题的角色或 id 不合格")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return false, errors.New("话题标题是空的")
	}
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT character_id FROM local_topics WHERE id=?`, id).Scan(&owner)
	switch {
	case err == nil:
		if owner != character {
			return false, errors.New("这个话题 id 属于别的角色")
		}
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO local_topics(id,character_id,title,title_by,created_at) VALUES(?,?,?,?,?)`, id, character, title, by, now); err != nil {
		return false, err
	}
	return true, s.commitChanged(tx, "console:"+character+":topic:"+id)
}
