package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// LocalWorkspace 是一个本地私聊会话绑定的工作目录。RealPath 是选的时候的真实路径：
// 联接后来改了指向，这一轮要拒掉，不能跟着走到别的目录（规格-按会话选工作目录）。
type LocalWorkspace struct {
	Conversation, CWD, Root, RealPath string
	UpdatedAt                         int64
}

// HasLocalWorkspaces 只读查结构；只读归档打开旧库不跑迁移，没有这张表就当没绑定。
func (s *Store) HasLocalWorkspaces(ctx context.Context) (bool, error) {
	var count int
	err := s.Events.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='local_workspaces'`).Scan(&count)
	return count == 1, err
}

// LocalWorkspaceOf 取这个会话的绑定；没绑定第二个返回值是假。
func (s *Store) LocalWorkspaceOf(ctx context.Context, conversation string) (LocalWorkspace, bool, error) {
	available, err := s.HasLocalWorkspaces(ctx)
	if err != nil || !available {
		return LocalWorkspace{}, false, err
	}
	w := LocalWorkspace{Conversation: conversation}
	err = s.Events.QueryRowContext(ctx, `SELECT cwd,root,real_path,updated_at FROM local_workspaces WHERE conversation=?`, conversation).Scan(&w.CWD, &w.Root, &w.RealPath, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalWorkspace{}, false, nil
	}
	return w, err == nil, err
}

// SetLocalWorkspace 写绑定；校验归宿主（目录是否存在不是库的事）。
func (s *Store) SetLocalWorkspace(ctx context.Context, w LocalWorkspace) error {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertLocalWorkspace(ctx, tx, w); err != nil {
		return err
	}
	return s.commitChanged(tx, w.Conversation)
}

func upsertLocalWorkspace(ctx context.Context, tx *sql.Tx, w LocalWorkspace) error {
	if w.Conversation == "" || w.CWD == "" || w.Root == "" || w.RealPath == "" {
		return errors.New("工作目录绑定缺字段")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO local_workspaces(conversation,cwd,root,real_path,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(conversation) DO UPDATE SET cwd=excluded.cwd,root=excluded.root,real_path=excluded.real_path,updated_at=excluded.updated_at`,
		w.Conversation, w.CWD, w.Root, w.RealPath, w.UpdatedAt)
	return err
}

// ClearLocalWorkspace 删绑定，这个会话回到全局默认；返回真的删掉了没有。
func (s *Store) ClearLocalWorkspace(ctx context.Context, conversation string) (bool, error) {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM local_workspaces WHERE conversation=?`, conversation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, s.commitChanged(tx, conversation)
}

// ReadLocalWorkspaces 是列表要的全部绑定，一次读完，不按会话一条条查。
func (s *Store) ReadLocalWorkspaces(ctx context.Context) (map[string]LocalWorkspace, error) {
	available, err := s.HasLocalWorkspaces(ctx)
	if err != nil || !available {
		return nil, err
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT conversation,cwd,root,real_path,updated_at FROM local_workspaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LocalWorkspace{}
	for rows.Next() {
		var w LocalWorkspace
		if err := rows.Scan(&w.Conversation, &w.CWD, &w.Root, &w.RealPath, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out[w.Conversation] = w
	}
	return out, rows.Err()
}

// RecentLocalWorkspaces 是最近用过的目录，新的在前，同一个目录只留一次（Windows 路径不分大小写）。
func (s *Store) RecentLocalWorkspaces(ctx context.Context, limit int) ([]string, error) {
	available, err := s.HasLocalWorkspaces(ctx)
	if err != nil || !available {
		return nil, err
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT cwd FROM local_workspaces ORDER BY updated_at DESC, conversation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cwd string
		if err := rows.Scan(&cwd); err != nil {
			return nil, err
		}
		seen := false
		for _, have := range out {
			seen = seen || strings.EqualFold(have, cwd)
		}
		if !seen && len(out) < limit {
			out = append(out, cwd)
		}
	}
	return out, rows.Err()
}
