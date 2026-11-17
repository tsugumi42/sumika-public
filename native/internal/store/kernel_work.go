package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sumika-native/internal/kernel"
)

var ErrWorkConflict = errors.New("工作版本或权限已改变")

func scopeJSON(s kernel.Scope) string { b, _ := json.Marshal(s); return string(b) }

// ReserveResult 在创建文件和执行生产者前占足单结果上限；同根并发不能透支。
func (s *Store) ReserveResult(ctx context.Context, inv kernel.Invocation, max, rootMax int64) (string, error) {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO result_roots(root_id) VALUES (?)`, inv.RootID); err != nil {
		return "", err
	}
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM steps WHERE id=? AND character_id=? AND conversation=? AND turn_id=? AND kind='tool'`, inv.ToolID, inv.Scope.CharacterID, inv.Scope.Conversation, inv.RunID).Scan(&name); err != nil {
		return "", err
	}
	r, err := tx.ExecContext(ctx, `UPDATE result_roots SET reserved=reserved+? WHERE root_id=? AND bytes+reserved+?<=?`, max, inv.RootID, max, rootMax)
	if err != nil {
		return "", err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return "", fmt.Errorf("工作结果容量不足或归属不符；未开始采集")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO result_reservations(tool_id,root_id,bytes,created_at) VALUES (?,?,?,?)`, inv.ToolID, inv.RootID, max, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	return name, tx.Commit()
}
func (s *Store) ReleaseResult(ctx context.Context, root, tool string) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var reserved int64
	err = tx.QueryRowContext(ctx, `DELETE FROM result_reservations WHERE root_id=? AND tool_id=? RETURNING bytes`, root, tool).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE result_roots SET reserved=reserved-? WHERE root_id=?`, reserved, root); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CommitResult(ctx context.Context, root, name string, ref kernel.ResultRef, reserved, now int64) error {
	b, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO result_refs(id,root_id,owner_json,ref_json,tool_name,created_at) VALUES (?,?,?,?,?,?)`, ref.ID, root, scopeJSON(ref.Owner), string(b), name, now); err != nil {
		return err
	}
	var held int64
	if err = tx.QueryRowContext(ctx, `DELETE FROM result_reservations WHERE root_id=? AND tool_id=? RETURNING bytes`, root, ref.ToolID).Scan(&held); err != nil {
		return err
	}
	if held != reserved {
		return fmt.Errorf("结果预留不匹配")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE result_roots SET reserved=reserved-?,bytes=bytes+? WHERE root_id=?`, reserved, ref.Bytes, root); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ResultRef(ctx context.Context, id string) (kernel.ResultRef, string, error) {
	var raw, name string
	err := s.Trace.QueryRowContext(ctx, `SELECT ref_json,tool_name FROM result_refs WHERE id=?`, id).Scan(&raw, &name)
	if err != nil {
		return kernel.ResultRef{}, "", err
	}
	var ref kernel.ResultRef
	err = json.Unmarshal([]byte(raw), &ref)
	return ref, name, err
}
func (s *Store) ResultFiles(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Trace.QueryContext(ctx, `SELECT id FROM result_refs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
func (s *Store) ReleaseWorkResults(ctx context.Context, owner kernel.Scope, root string) error {
	// 归档由可信宿主显式调用；无此动作，30 天也不删除证据。
	_, err := s.Trace.ExecContext(ctx, `UPDATE result_refs SET retained=0 WHERE root_id=? AND owner_json=? AND NOT EXISTS (SELECT 1 FROM work_roots WHERE root_id=? AND json_array_length(state_json,'$.Pending')>0)`, root, scopeJSON(owner), root)
	return err
}
func (s *Store) ExpiredResults(ctx context.Context, before int64) ([]kernel.ResultRef, error) {
	rows, err := s.Trace.QueryContext(ctx, `SELECT ref_json FROM result_refs WHERE retained=0 AND created_at<? AND NOT EXISTS (SELECT 1 FROM work_roots w WHERE w.root_id=result_refs.root_id)`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kernel.ResultRef
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ref kernel.ResultRef
		if err := json.Unmarshal([]byte(raw), &ref); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
func (s *Store) DeleteResult(ctx context.Context, ref kernel.ResultRef) (bool, error) {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var root string
	err = tx.QueryRowContext(ctx, `SELECT root_id FROM result_refs WHERE id=?`, ref.ID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM result_refs WHERE id=? AND retained=0 AND NOT EXISTS (SELECT 1 FROM work_roots WHERE root_id=result_refs.root_id)`, ref.ID)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE result_roots SET bytes=bytes-? WHERE root_id=?`, ref.Bytes, root); err != nil {
			return false, err
		}
	}
	return n == 1, tx.Commit()
}

// WorkHead 的 revision 只在 Save 时递增，追加序列独立；摘要期间追加不会覆盖 tail。
type WorkHead struct {
	State   kernel.WorkState
	Ordinal uint64
	Stamp   string
}
type WorkEntry struct {
	Ordinal uint64
	RunID   string
	Payload []byte
	Digest  string
}

func (s *Store) OpenWork(ctx context.Context, inv kernel.Invocation, stamp string) (WorkHead, error) {
	state := kernel.WorkState{RootID: inv.RootID, Owner: inv.Scope}
	b, _ := json.Marshal(state)
	_, err := s.Trace.ExecContext(ctx, `INSERT OR IGNORE INTO work_roots(root_id,owner_json,stamp,state_json) VALUES (?,?,?,?)`, inv.RootID, scopeJSON(inv.Scope), stamp, string(b))
	if err != nil {
		return WorkHead{}, err
	}
	return s.LoadWork(ctx, inv)
}
func (s *Store) LoadWork(ctx context.Context, inv kernel.Invocation) (WorkHead, error) {
	var h WorkHead
	var raw string
	err := s.Trace.QueryRowContext(ctx, `SELECT state_json,ordinal,stamp FROM work_roots WHERE root_id=? AND owner_json=?`, inv.RootID, scopeJSON(inv.Scope)).Scan(&raw, &h.Ordinal, &h.Stamp)
	if err != nil {
		return h, err
	}
	err = json.Unmarshal([]byte(raw), &h.State)
	return h, err
}
func (s *Store) SetWorkStamp(ctx context.Context, inv kernel.Invocation, stamp string) error {
	r, err := s.Trace.ExecContext(ctx, `UPDATE work_roots SET stamp=?,revision=revision+CASE WHEN stamp<>? THEN 1 ELSE 0 END,state_json=CASE WHEN stamp<>? THEN json_set(state_json,'$.Revision',revision+1) ELSE state_json END WHERE root_id=? AND owner_json=?`, stamp, stamp, stamp, inv.RootID, scopeJSON(inv.Scope))
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrWorkConflict
	}
	return nil
}
func (s *Store) AppendWork(ctx context.Context, inv kernel.Invocation, payload []byte, digest string) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE work_roots SET ordinal=ordinal+1 WHERE root_id=? AND owner_json=?`, inv.RootID, scopeJSON(inv.Scope))
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrWorkConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_entries(root_id,owner_json,ordinal,run_id,payload,digest) SELECT root_id,owner_json,ordinal,?,?,? FROM work_roots WHERE root_id=? AND owner_json=?`, inv.RunID, payload, digest, inv.RootID, scopeJSON(inv.Scope))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) WorkEntries(ctx context.Context, inv kernel.Invocation, after, through uint64, limit int) ([]WorkEntry, error) {
	rows, err := s.Trace.QueryContext(ctx, `SELECT ordinal,run_id,payload,digest FROM work_entries WHERE root_id=? AND owner_json=? AND ordinal>? AND ordinal<=? ORDER BY ordinal LIMIT ?`, inv.RootID, scopeJSON(inv.Scope), after, through, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkEntry
	for rows.Next() {
		var e WorkEntry
		if err := rows.Scan(&e.Ordinal, &e.RunID, &e.Payload, &e.Digest); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WorkRoot 是一个工作根的头：哪一根、记到第几条、折叠到第几条。
type WorkRoot struct {
	RootID                  string
	Ordinal, ThroughOrdinal uint64
}

// WorkRootsOf 按开根先后倒着列这个 owner 的工作根（没有条目的不列）。owner_json 全等，所以另一个角色、会话、发言人或
// 另一份授权的根都不会出现；扫 work_roots 不走索引，只在重建实例时用，limit 封住回看多远。
func (s *Store) WorkRootsOf(ctx context.Context, owner kernel.Scope, limit int) ([]WorkRoot, error) {
	rows, err := s.Trace.QueryContext(ctx, `SELECT root_id,ordinal,coalesce(json_extract(state_json,'$.ThroughOrdinal'),0) FROM work_roots WHERE owner_json=? AND ordinal>0 ORDER BY rowid DESC LIMIT ?`, scopeJSON(owner), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkRoot
	for rows.Next() {
		var r WorkRoot
		if err := rows.Scan(&r.RootID, &r.Ordinal, &r.ThroughOrdinal); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) WorkDigest(ctx context.Context, inv kernel.Invocation, through uint64) (string, error) {
	return workDigest(ctx, s.Trace.QueryContext, inv, through)
}

// workDigest 流式核对整个前缀，不能只认最后一条而漏掉较早记录的变化。
func workDigest(ctx context.Context, query func(context.Context, string, ...any) (*sql.Rows, error), inv kernel.Invocation, through uint64) (string, error) {
	if through == 0 {
		return "", nil
	}
	rows, err := query(ctx, `SELECT digest FROM work_entries WHERE root_id=? AND owner_json=? AND ordinal<=? ORDER BY ordinal`, inv.RootID, scopeJSON(inv.Scope), through)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	var count uint64
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return "", err
		}
		h.Write([]byte(digest))
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != through {
		return "", ErrWorkConflict
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Store) SaveWork(ctx context.Context, inv kernel.Invocation, expected uint64, stamp, digest string, state kernel.WorkState) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d, err := workDigest(ctx, tx.QueryContext, inv, state.ThroughOrdinal)
	if err != nil {
		return err
	}
	if d != digest {
		return ErrWorkConflict
	}
	state.Revision = expected + 1
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE work_roots SET revision=?,state_json=? WHERE root_id=? AND owner_json=? AND revision=? AND stamp=? AND ordinal>=?`, state.Revision, string(b), inv.RootID, scopeJSON(inv.Scope), expected, stamp, state.ThroughOrdinal)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrWorkConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_snapshots(root_id,owner_json,revision,through_ordinal,stamp,prefix_digest,state_json) VALUES (?,?,?,?,?,?,?)`, inv.RootID, scopeJSON(inv.Scope), state.Revision, state.ThroughOrdinal, stamp, digest, string(b))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// LegacyTodo 只迁移本角色在本会话的成功清单，不能借同群另一角色的计划。
func (s *Store) LegacyTodo(ctx context.Context, scope kernel.Scope) (string, error) {
	var args string
	err := s.Trace.QueryRowContext(ctx, `SELECT args_json FROM steps WHERE character_id=? AND conversation=? AND name='todo_write' AND is_error=0 AND status='done' ORDER BY started_at DESC LIMIT 1`, scope.CharacterID, scope.Conversation).Scan(&args)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return args, err
}

func (s *Store) StepResult(id string, ref kernel.ResultRef) error {
	_, err := s.Trace.Exec(`UPDATE steps SET full_hash=?,result_ref=? WHERE id=?`, ref.SHA256, ref.ID, id)
	return err
}

func (s *Store) ReapResultReservations(ctx context.Context, before int64, active []string) error {
	raw, _ := json.Marshal(active)
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT root_id,tool_id,bytes FROM result_reservations WHERE created_at<? AND tool_id NOT IN (SELECT value FROM json_each(?))`, before, string(raw))
	if err != nil {
		return err
	}
	type held struct {
		root, tool string
		bytes      int64
	}
	var all []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.root, &h.tool, &h.bytes); err != nil {
			rows.Close()
			return err
		}
		all = append(all, h)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, h := range all {
		if _, err := tx.ExecContext(ctx, `DELETE FROM result_reservations WHERE tool_id=?`, h.tool); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE result_roots SET reserved=reserved-? WHERE root_id=?`, h.bytes, h.root); err != nil {
			return err
		}
	}
	return tx.Commit()
}
