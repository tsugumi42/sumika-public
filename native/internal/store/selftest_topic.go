package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"sumika-native/internal/audience"
)

// 自检话题（sumika-ctl chat）。话题行是本地库里 local_topics 的一行，id 以 audience.SelftestTopicPrefix 开头，标题以"自检"开头：
// 界面的会话列表本来就按标题列话题，所以不用改界面，列表里就写着"自检"。

// SelftestTopicTitle 是自检话题的标题前缀
const SelftestTopicTitle = "自检"

// EnsureSelftestTopic 给 character 建自检话题 id（已存在就不动）。id 必须是自检话题 id，title 空用"自检"。
// 返回是不是这次新建的。已存在但属于别的角色，当失败：不改别人的话题
func (s *Store) EnsureSelftestTopic(ctx context.Context, character, id, title string, now int64) (bool, error) {
	if character == "" || strings.ContainsAny(character, ": \t\r\n") || !audience.IsSelftest(audience.SelftestConversation(character, id)) {
		return false, errors.New("不是自检话题")
	}
	if title == "" {
		title = SelftestTopicTitle
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO local_topics(id,character_id,title,title_by,created_at) VALUES(?,?,?,?,?)`, id, character, title, "ctl", now); err != nil {
		return false, err
	}
	conv := audience.SelftestConversation(character, id)
	return true, s.commitChanged(tx, conv)
}

// LatestSelftestTopic 是 character 最新建的自检话题 id；没有返回空串
func (s *Store) LatestSelftestTopic(ctx context.Context, character string) (string, error) {
	var id string
	err := s.Events.QueryRowContext(ctx, `SELECT id FROM local_topics WHERE character_id=? AND (id=? OR id LIKE ? ESCAPE '\')
		ORDER BY created_at DESC, id DESC LIMIT 1`, character, audience.SelftestTopicPrefix, audience.SelftestTopicPrefix+`-%`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
