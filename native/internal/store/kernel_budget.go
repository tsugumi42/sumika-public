package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UpdateBudget 在同一事务中定位执行号、修改根账本并登记执行号，禁止先查余额再另行扣款。
// root 为空时按已登记的执行号定位；回调错误回滚所有修改。
func (s *Store) UpdateBudget(ctx context.Context, root, id string, update func([]byte) ([]byte, error)) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var registered string
	if id != "" {
		err = tx.QueryRowContext(ctx, `SELECT root_id FROM budget_reservations WHERE id=?`, id).Scan(&registered)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if registered != "" {
			if root != "" && root != registered {
				return fmt.Errorf("budget reservation %q belongs to another root", id)
			}
			root = registered
		}
	}
	if root == "" {
		return fmt.Errorf("budget reservation %q not found", id)
	}
	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM root_budget WHERE root_id=?`, root).Scan(&data)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	next, err := update(data)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO root_budget(root_id,state_json) VALUES(?,?) ON CONFLICT(root_id) DO UPDATE SET state_json=excluded.state_json`, root, next); err != nil {
		return err
	}
	if id != "" && registered == "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_reservations(id,root_id) VALUES(?,?)`, id, root); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ReadBudget(ctx context.Context, root string) ([]byte, error) {
	var data []byte
	err := s.Trace.QueryRowContext(ctx, `SELECT state_json FROM root_budget WHERE root_id=?`, root).Scan(&data)
	return data, err
}
