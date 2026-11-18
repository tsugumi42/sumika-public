package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// LocalGroup 的成员顺序同时决定未点名时的轮转，不拿显示名当身份。
type LocalGroup struct {
	ID, Name     string
	CreatedAt    int64
	NextPosition int
	Members      []LocalGroupMember
}
type LocalGroupMember struct {
	CharacterID         string
	Position, AutoCount int
	LastAutoAt          int64
}

func LocalGroupID(conversation string) (string, bool) {
	parts := strings.Split(conversation, ":")
	if len(parts) != 3 || parts[0] != "console" || parts[1] != "group" || strings.TrimSpace(parts[2]) == "" || strings.TrimSpace(parts[2]) != parts[2] {
		return "", false
	}
	return parts[2], true
}
func (g LocalGroup) Conversation() string { return "console:group:" + g.ID }
func (g LocalGroup) CharacterIDs() []string {
	ids := make([]string, len(g.Members))
	for i, m := range g.Members {
		ids[i] = m.CharacterID
	}
	return ids
}

// HasLocalGroups 只读查结构；archive 打开旧库不运行迁移。
func (s *Store) HasLocalGroups(ctx context.Context) (bool, error) {
	var count int
	err := s.Events.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='local_groups'`).Scan(&count)
	return count == 1, err
}

func (s *Store) CreateLocalGroup(ctx context.Context, g LocalGroup) error {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO local_groups(id,name,created_at) VALUES (?,?,?)`, g.ID, g.Name, g.CreatedAt); err != nil {
		return err
	}
	for i, m := range g.Members {
		if _, err = tx.ExecContext(ctx, `INSERT INTO local_group_members(group_id,character_id,position) VALUES (?,?,?)`, g.ID, m.CharacterID, i); err != nil {
			return err
		}
	}
	return s.commitChanged(tx, g.Conversation())
}

func (s *Store) ReadLocalGroup(ctx context.Context, conversation string) (LocalGroup, error) {
	id, ok := LocalGroupID(conversation)
	if !ok {
		return LocalGroup{}, ErrDisplayScope
	}
	var g LocalGroup
	err := s.Events.QueryRowContext(ctx, `SELECT id,name,created_at,next_position FROM local_groups WHERE id=?`, id).Scan(&g.ID, &g.Name, &g.CreatedAt, &g.NextPosition)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrDisplayScope
	}
	if err != nil {
		return g, err
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT character_id,position,auto_count,last_auto_at FROM local_group_members WHERE group_id=? ORDER BY position`, id)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var m LocalGroupMember
		if err = rows.Scan(&m.CharacterID, &m.Position, &m.AutoCount, &m.LastAutoAt); err != nil {
			return g, err
		}
		g.Members = append(g.Members, m)
	}
	return g, rows.Err()
}

// ReadLocalGroups 包含空群，角色过滤只能缩到该角色确实加入的群。
func (s *Store) ReadLocalGroups(ctx context.Context, characters []string, character string) ([]LocalGroup, error) {
	available, err := s.HasLocalGroups(ctx)
	if err != nil || !available {
		return nil, err
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT id FROM local_groups ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var groups []LocalGroup
	for _, id := range ids {
		g, err := s.ReadLocalGroup(ctx, "console:group:"+id)
		if errors.Is(err, ErrDisplayScope) {
			continue // 脏数据里 ID 不合法或刚被删的群跳过，不让整张列表失败（和 Bun 一致）
		}
		if err != nil {
			return nil, err
		}
		for _, m := range g.Members {
			if containsCharacter(characters, m.CharacterID) && (character == "" || m.CharacterID == character) {
				groups = append(groups, g)
				break
			}
		}
	}
	return groups, nil
}

// AdvanceLocalGroup 在整个主人轮次串行的前提下落下轮转和自动接话额度。
func (s *Store) AdvanceLocalGroup(ctx context.Context, g LocalGroup) error {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE local_groups SET next_position=? WHERE id=?`, g.NextPosition, g.ID); err != nil {
		return err
	}
	for _, m := range g.Members {
		if _, err = tx.ExecContext(ctx, `UPDATE local_group_members SET auto_count=?,last_auto_at=? WHERE group_id=? AND character_id=?`, m.AutoCount, m.LastAutoAt, g.ID, m.CharacterID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validateLocalGroupDisplay(ctx context.Context, tx *sql.Tx, scope DisplayScope, id string) error {
	rows, err := tx.QueryContext(ctx, `SELECT character_id FROM local_group_members WHERE group_id=?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	visible := false
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return err
		}
		visible = visible || (containsCharacter(scope.Characters, member) && (scope.Character == "" || scope.Character == member))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !visible {
		return ErrDisplayScope
	}
	return nil
}
