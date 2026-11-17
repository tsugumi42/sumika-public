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

var ErrReviewLimit = errors.New("审阅次数已达根上限")

type ReviewRecord struct {
	ID, Digest, Kind, State string
	Request                 kernel.ReviewRequest
	Verdict                 kernel.Verdict
	Error                   string
}

// BeginReview 在模型派发之前记次数；失败请求也占额度，不能用坏 JSON 无限重试。
func (s *Store) BeginReview(ctx context.Context, r ReviewRecord, limit int) (bool, error) {
	body, err := json.Marshal(r.Request)
	if err != nil {
		return false, err
	}
	owner, err := json.Marshal(r.Request.Invocation.Scope)
	if err != nil {
		return false, err
	}
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, old string
	err = tx.QueryRowContext(ctx, `SELECT state, digest FROM kernel_reviews WHERE id=?`, r.ID).Scan(&state, &old)
	if err == nil {
		if old != r.Digest {
			return false, fmt.Errorf("checkpoint 内容冲突")
		}
		if state != "failed" {
			return false, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(attempts),0) FROM kernel_reviews WHERE root_id=? AND kind=?`, r.Request.Invocation.RootID, r.Kind).Scan(&count); err != nil {
		return false, err
	}
	if count >= limit {
		return false, ErrReviewLimit
	}
	if state == "failed" {
		_, err = tx.ExecContext(ctx, `UPDATE kernel_reviews SET state='running',attempts=attempts+1,error='',verdict_json=NULL WHERE id=?`, r.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kernel_reviews(id,root_id,kind,owner_json,request_json,digest,state,created_at) VALUES(?,?,?,?,?,?,'running',?)`, r.ID, r.Request.Invocation.RootID, r.Kind, string(owner), string(body), r.Digest, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// FinishReview 将 verdict 和 cursor 同时持久化；失败状态不成为可采用 cursor。
func (s *Store) FinishReview(ctx context.Context, id string, v kernel.Verdict, reviewErr error) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	state, msg := "complete", ""
	if reviewErr != nil {
		state, msg = "failed", reviewErr.Error()
	}
	result, err := s.Trace.ExecContext(ctx, `UPDATE kernel_reviews SET verdict_json=?, state=?, error=? WHERE id=? AND state='running'`, string(b), state, msg, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("审阅结局不存在或已提交")
	}
	return nil
}

// ReviewFindings 不摘邮箱；接纳事务成功之后才能另行确认，失败可以安全重取。
func (s *Store) ReviewFindings(ctx context.Context, inv kernel.Invocation) ([]ReviewRecord, error) {
	owner, err := json.Marshal(inv.Scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.Trace.QueryContext(ctx, `SELECT id,digest,request_json,verdict_json FROM kernel_reviews WHERE root_id=? AND owner_json=? AND kind='advisor' AND state='complete' AND delivered=0 ORDER BY created_at,id`, inv.RootID, string(owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewRecord
	for rows.Next() {
		var r ReviewRecord
		var req, v string
		if err := rows.Scan(&r.ID, &r.Digest, &req, &v); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(req), &r.Request); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(v), &r.Verdict); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AcceptReview 把 K1 的输入接纳与 delivered 放进同一事务，失败不清邮箱。
// accept 在这个事务内复查停止/权限代次并按 finding ID 幂等写入。
func (s *Store) AcceptReview(ctx context.Context, id string, accept func(*sql.Tx) error) (bool, error) {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var delivered int
	if err := tx.QueryRowContext(ctx, `SELECT delivered FROM kernel_reviews WHERE id=? AND state='complete'`, id).Scan(&delivered); err != nil {
		return false, err
	}
	if delivered != 0 {
		return false, nil
	}
	if err := accept(tx); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE kernel_reviews SET delivered=1 WHERE id=?`, id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ReviewByID(ctx context.Context, id string) (ReviewRecord, bool, error) {
	var r ReviewRecord
	var req string
	var v sql.NullString
	err := s.Trace.QueryRowContext(ctx, `SELECT id,digest,kind,state,request_json,verdict_json,error FROM kernel_reviews WHERE id=?`, id).Scan(&r.ID, &r.Digest, &r.Kind, &r.State, &req, &v, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if err = json.Unmarshal([]byte(req), &r.Request); err != nil {
		return r, false, err
	}
	if v.Valid {
		err = json.Unmarshal([]byte(v.String), &r.Verdict)
	}
	return r, true, err
}
