package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sumika-native/internal/kernel"
)

type EcosystemStore struct {
	DB *sql.DB
	// Now 给 MCP 留痕写时间；空用系统时钟（测试里换成假钟）
	Now func() time.Time
}

type TaskRecord struct {
	ID, ParentRun, ToolID, Digest, RootID, State, Payload, Result string
	Owner                                                         kernel.Scope
}

func (s EcosystemStore) CreateTask(ctx context.Context, t TaskRecord) (TaskRecord, bool, error) {
	owner, _ := json.Marshal(t.Owner)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TaskRecord{}, false, err
	}
	defer tx.Rollback()
	old, err := readTask(tx.QueryRowContext(ctx, `SELECT id,parent_run,tool_id,payload_digest,owner,root_id,state,payload,result FROM kernel_tasks WHERE parent_run=? AND tool_id=?`, t.ParentRun, t.ToolID))
	if err == nil {
		if old.Digest != t.Digest || old.Owner != t.Owner {
			return TaskRecord{}, false, fmt.Errorf("同一工具调用内容冲突")
		}
		return old, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kernel_tasks(id,parent_run,tool_id,payload_digest,owner,root_id,state,payload) VALUES(?,?,?,?,?,?,?,?)`, t.ID, t.ParentRun, t.ToolID, t.Digest, string(owner), t.RootID, t.State, t.Payload)
	if err != nil {
		return TaskRecord{}, false, err
	}
	return t, true, tx.Commit()
}
func readTask(row *sql.Row) (TaskRecord, error) {
	var t TaskRecord
	var owner string
	err := row.Scan(&t.ID, &t.ParentRun, &t.ToolID, &t.Digest, &owner, &t.RootID, &t.State, &t.Payload, &t.Result)
	if err == nil {
		err = json.Unmarshal([]byte(owner), &t.Owner)
	}
	return t, err
}
func (s EcosystemStore) Task(ctx context.Context, id string) (TaskRecord, error) {
	return readTask(s.DB.QueryRowContext(ctx, `SELECT id,parent_run,tool_id,payload_digest,owner,root_id,state,payload,result FROM kernel_tasks WHERE id=?`, id))
}

// OwnedTasks 按会话授权列出任务，新的在前；只是列表，不开放读结果。
func (s EcosystemStore) OwnedTasks(ctx context.Context, scope kernel.Scope, limit int) ([]TaskRecord, error) {
	owner, _ := json.Marshal(scope)
	rows, err := s.DB.QueryContext(ctx, `SELECT id,parent_run,tool_id,payload_digest,owner,root_id,state,payload,result FROM kernel_tasks WHERE owner=? ORDER BY rowid DESC LIMIT ?`, string(owner), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var raw string
		if err = rows.Scan(&t.ID, &t.ParentRun, &t.ToolID, &t.Digest, &raw, &t.RootID, &t.State, &t.Payload, &t.Result); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &t.Owner); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s EcosystemStore) FinishTask(ctx context.Context, t TaskRecord, held bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE kernel_tasks SET state=?,result=? WHERE id=? AND state='running'`, t.State, t.Result, t.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	owner, _ := json.Marshal(t.Owner)
	state := "pending"
	if held {
		state = "held"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_task_mailbox(task_id,owner,state) VALUES(?,?,?)`, t.ID, string(owner), state); err != nil {
		return err
	}
	return tx.Commit()
}

// PendingTasks 读取不会清邮箱；输入保存失败时，下个安全边界必须还能取得同一任务。
func (s EcosystemStore) PendingTasks(ctx context.Context, scope kernel.Scope) ([]TaskRecord, error) {
	owner, _ := json.Marshal(scope)
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.parent_run,t.tool_id,t.payload_digest,t.owner,t.root_id,t.state,t.payload,t.result FROM kernel_tasks t JOIN kernel_task_mailbox m ON m.task_id=t.id WHERE m.owner=? AND m.state='pending' ORDER BY t.id`, string(owner))
	if err != nil {
		return nil, err
	}
	var out []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var raw string
		if err = rows.Scan(&t.ID, &t.ParentRun, &t.ToolID, &t.Digest, &raw, &t.RootID, &t.State, &t.Payload, &t.Result); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &t.Owner); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CommitTasks 由宿主持久接纳后按稳定 task ID 确认；同一 receiving run 的重试幂等。
func (s EcosystemStore) CommitTasks(ctx context.Context, scope kernel.Scope, run string, ids []string) error {
	owner, _ := json.Marshal(scope)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var state, receiving string
		if err = tx.QueryRowContext(ctx, `SELECT state,receiving_run FROM kernel_task_mailbox WHERE task_id=? AND owner=?`, id, string(owner)).Scan(&state, &receiving); err != nil {
			return err
		}
		if state == "delivered" && receiving == run {
			continue
		}
		if state != "pending" {
			return fmt.Errorf("task 邮箱不可接纳")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE kernel_task_mailbox SET state='delivered',receiving_run=? WHERE task_id=? AND owner=? AND state='pending'`, run, id, string(owner)); err != nil {
			return err
		}
	}
	return tx.Commit()

}
func (s EcosystemStore) RecoverTasks(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE kernel_tasks SET state='interrupted' WHERE state='running'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kernel_task_mailbox SET state='held' WHERE state='pending'`); err != nil {
		return err
	}
	return tx.Commit()
}

type WebQuota struct {
	Dimension, Window string
	Limit, Amount     int64
}

func (s EcosystemStore) WebDispatch(ctx context.Context, id, owner, root string, credits int64, quotas []WebQuota) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range quotas {
		if q.Amount < 0 || q.Limit < q.Amount {
			return fmt.Errorf("web 额度不足")
		}
		var used int64
		err = tx.QueryRowContext(ctx, `SELECT used FROM kernel_web_quota WHERE dimension=? AND window=?`, q.Dimension, q.Window).Scan(&used)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if used > q.Limit-q.Amount {
			return fmt.Errorf("web 额度不足")
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_web_usage(id,owner,root_id,state,credits) VALUES(?,?,?,'dispatched',?)`, id, owner, root, credits); err != nil {
		return err
	}
	for _, q := range quotas {
		if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_web_quota(dimension,window,used) VALUES(?,?,?) ON CONFLICT(dimension,window) DO UPDATE SET used=used+excluded.used`, q.Dimension, q.Window, q.Amount); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_web_charges(request_id,dimension,window,amount) VALUES(?,?,?,?)`, id, q.Dimension, q.Window, q.Amount); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s EcosystemStore) WebSettle(ctx context.Context, id string, actual *int64) error {
	state := "unknown"
	if actual != nil {
		state = "settled"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE kernel_web_usage SET state=?,actual=? WHERE id=? AND state='dispatched'`, state, actual, id)
	return err
}

// WebConsume 先于缓存和单飞扣逻辑次数；同 Root 重启不返还。
func (s EcosystemStore) WebConsume(ctx context.Context, quotas []WebQuota) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range quotas {
		if q.Amount < 1 || q.Limit < q.Amount {
			return fmt.Errorf("web 逻辑额度不足")
		}
		var used int64
		err = tx.QueryRowContext(ctx, `SELECT used FROM kernel_web_quota WHERE dimension=? AND window=?`, q.Dimension, q.Window).Scan(&used)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if used > q.Limit-q.Amount {
			return fmt.Errorf("web 逻辑额度不足")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_web_quota(dimension,window,used) VALUES(?,?,?) ON CONFLICT(dimension,window) DO UPDATE SET used=used+excluded.used`, q.Dimension, q.Window, q.Amount); err != nil {
			return err
		}
	}
	return tx.Commit()
}
