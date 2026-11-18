package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sumika-native/internal/kernel"
)

var ErrInputConflict = errors.New("输入身份或正文冲突")
var ErrInputLimit = errors.New("待处理输入已达上限")

type KernelInput struct {
	Identity                           kernel.Invocation
	Domain, ClientKey, Digest, Payload string
	Mode                               kernel.InputMode
	Sequence, Revision                 uint64
	State                              kernel.InputState
	AcceptedAt                         int64
}

// AppendInputEvent 跨库只承诺事件幂等；trace 写失败后同键可以补登记，不伪装跨库事务。
func (s *Store) AppendInputEvent(ctx context.Context, id, digest string, e NewEvent, observers []string) (int64, error) {
	// 存档里的 digest 就是幂等凭证：自检标记（tags）、主人分块输入（规格-引用，e.Segments 是 {"local_parts":[…]}）的键依次跟在后面。
	// 同一输入生成的字节永远相同（map 编码按键排序），所以仍可整串比较；两样都没有时与原来一字不差。
	digest = fmt.Sprintf(`{"kernel_input_digest":%q}`, digest)
	if len(e.Tags) > 0 {
		tags, err := json.Marshal(e.Tags)
		if err != nil {
			return 0, err
		}
		digest = digest[:len(digest)-1] + `,"tags":` + string(tags) + "}"
	}
	if e.Segments != nil {
		extra, err := json.Marshal(e.Segments)
		if err != nil || len(extra) < 3 || extra[0] != '{' {
			return 0, errors.Join(err, errors.New("输入事件的附加存档必须是非空对象"))
		}
		digest = digest[:len(digest)-1] + "," + string(extra[1:])
	}
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var seq int64
	var old string
	err = tx.QueryRowContext(ctx, `SELECT seq,segments_json FROM events WHERE id=?`, id).Scan(&seq, &old)
	if err == nil {
		if old != digest {
			return 0, ErrInputConflict
		}
		return seq, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO events(id,channel,conversation,actor_kind,actor_id,origin,kind,text,segments_json,reply_to,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, e.Channel, e.Conversation, e.ActorKind, e.ActorID, e.Origin, e.Kind, e.Text, digest, e.ReplyTo, e.CreatedAt)
	if err != nil {
		return 0, err
	}
	seq, err = r.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = observe(tx, seq, observers); err != nil {
		return 0, err
	}
	return seq, s.commitChanged(tx, e.Conversation)
}

func scanKernelInput(row interface{ Scan(...any) error }) (KernelInput, error) {
	var in KernelInput
	var identity string
	err := row.Scan(&identity, &in.Domain, &in.ClientKey, &in.Digest, &in.Payload, &in.Mode, &in.Sequence, &in.Revision, &in.State, &in.AcceptedAt)
	if err == nil {
		err = json.Unmarshal([]byte(identity), &in.Identity)
	}
	return in, err
}

const kernelInputColumns = `identity_json,domain,client_key,digest,payload_json,mode,sequence,revision,state,accepted_at`

func (s *Store) KernelInput(ctx context.Context, domain, key string) (KernelInput, error) {
	return scanKernelInput(s.Trace.QueryRowContext(ctx, `SELECT `+kernelInputColumns+` FROM kernel_inbox WHERE domain=? AND client_key=?`, domain, key))
}

func (s *Store) AcceptKernelInput(ctx context.Context, in KernelInput, perDomain, global int) (KernelInput, bool, error) {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return in, false, err
	}
	defer tx.Rollback()
	old, err := scanKernelInput(tx.QueryRowContext(ctx, `SELECT `+kernelInputColumns+` FROM kernel_inbox WHERE domain=? AND client_key=?`, in.Domain, in.ClientKey))
	if err == nil {
		if old.Digest != in.Digest {
			return in, false, ErrInputConflict
		}
		return old, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return in, false, err
	}
	var total, local int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(domain=?),0) FROM kernel_inbox WHERE state='accepted'`, in.Domain).Scan(&total, &local); err != nil {
		return in, false, err
	}
	if total >= global || local >= perDomain {
		return in, false, ErrInputLimit
	}
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0)+1 FROM kernel_inbox WHERE domain=?`, in.Domain).Scan(&in.Sequence); err != nil {
		return in, false, err
	}
	in.Revision, in.State = 1, kernel.InputAccepted
	raw, err := json.Marshal(in.Identity)
	if err != nil {
		return in, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kernel_inbox(input_id,domain,client_key,digest,identity_json,payload_json,mode,sequence,revision,state,run_id,accepted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, in.Identity.InputID, in.Domain, in.ClientKey, in.Digest, string(raw), in.Payload, in.Mode, in.Sequence, in.Revision, in.State, in.Identity.RunID, in.AcceptedAt)
	if err != nil {
		return in, false, err
	}
	if in.Mode != kernel.InputSteer {
		_, err = tx.ExecContext(ctx, `INSERT INTO kernel_runs(run_id,identity_json,state) VALUES(?,?,'queued')`, in.Identity.RunID, string(raw))
	}
	if err != nil {
		return in, false, err
	}
	return in, true, tx.Commit()
}

func (s *Store) AdmitKernelInput(ctx context.Context, id string) error {
	r, err := s.Trace.ExecContext(ctx, `UPDATE kernel_inbox SET state='admitted',revision=revision+1 WHERE input_id=? AND state='accepted'`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return ErrInputConflict
	}
	return err
}

// RequeueKernelInput 保留 InputID，晚到插话换成独立激活，不能沿用旧运行的权限或取消代次。
func (s *Store) RequeueKernelInput(ctx context.Context, in KernelInput) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(in.Identity)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE kernel_inbox SET mode='queue',identity_json=?,run_id=?,revision=revision+1 WHERE input_id=? AND state='accepted'`, string(raw), in.Identity.RunID, in.Identity.InputID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInputConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_runs(run_id,identity_json,state) VALUES(?,?,'queued')`, in.Identity.RunID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CancelKernelInputs(ctx context.Context, domain string, through uint64, scope kernel.Scope) error {
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	_, err = s.Trace.ExecContext(ctx, `UPDATE kernel_inbox SET state='cancelled',revision=revision+1 WHERE domain=? AND sequence<=? AND json_extract(identity_json,'$.Scope')=? AND state='accepted'`, domain, through, string(raw))
	return err
}

func (s *Store) KernelRunState(ctx context.Context, run string, state kernel.RunState, outcome kernel.Outcome, drained bool) error {
	_, err := s.Trace.ExecContext(ctx, `UPDATE kernel_runs SET state=?,outcome=coalesce(outcome,nullif(?,'')),drained=?,revision=revision+1 WHERE run_id=? AND drained=0`, state, outcome, drained, run)
	return err
}

func (s *Store) RecoverKernelInputs(ctx context.Context) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 别的进程管的渠道（local 交给 Bun 时的 console）的在途输入和运行不碰：身份里的 Scope.Channel 认
	var skip string
	var args []any
	for _, ch := range s.foreignChannels {
		skip += ` AND coalesce(json_extract(identity_json,'$.Scope.Channel'),'') <> ?`
		args = append(args, ch)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE kernel_inbox SET state='held',revision=revision+1 WHERE state='accepted'` + skip, args},
		{`UPDATE kernel_runs SET state='settled',outcome=coalesce(outcome,'interrupted'),drained=1,revision=revision+1 WHERE drained=0` + skip, args},
		{`UPDATE kernel_activities SET state='unknown' WHERE state='running'`, nil},
	} {
		if _, err = tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ActivityStart(ctx context.Context, id string, in kernel.Invocation) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.Trace.ExecContext(ctx, `INSERT INTO kernel_activities(id,run_id,identity_json,state) VALUES(?,?,?,'running')`, id, in.RunID, string(raw))
	return err
}
func (s *Store) ActivityEnd(ctx context.Context, id string, cause error) error {
	var text any
	if cause != nil {
		text = cause.Error()
	}
	r, err := s.Trace.ExecContext(ctx, `UPDATE kernel_activities SET state='exited',error=? WHERE id=? AND state='running'`, text, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return fmt.Errorf("活动 %s 不在运行中", id)
	}
	return err
}
func (s *Store) LinkKernelDelivery(ctx context.Context, id string, in kernel.Invocation, kind string) error {
	r, err := s.Trace.ExecContext(ctx, `INSERT INTO kernel_delivery_links(id,run_id,step_id,kind) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET id=id WHERE run_id=excluded.run_id AND step_id=excluded.step_id AND kind=excluded.kind`, id, in.RunID, in.StepID, kind)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return ErrInputConflict
	}
	return err
}

// KernelDeliveryIdentity 仅供宿主按已保存计划继续关联回执；不是模型可调用的读取接口。
func (s *Store) KernelDeliveryIdentity(ctx context.Context, id string) (kernel.Invocation, error) {
	var in kernel.Invocation
	var raw, step string
	err := s.Trace.QueryRowContext(ctx, `SELECT r.identity_json,l.step_id FROM kernel_delivery_links l JOIN kernel_runs r ON r.run_id=l.run_id WHERE l.id=?`, id).Scan(&raw, &step)
	if errors.Is(err, sql.ErrNoRows) {
		return in, nil
	}
	if err != nil {
		return in, err
	}
	if err = json.Unmarshal([]byte(raw), &in); err != nil {
		return in, err
	}
	in.StepID = step
	return in, nil
}

func KernelPlatform(stores Stores, channel string) (string, error) {
	if platform, err := PlatformOf(channel); err == nil {
		return platform, nil
	}
	st, err := stores.For(channel)
	if err != nil {
		return "", err
	}
	for _, named := range stores.Each() {
		if named.Store == st {
			return named.Platform, nil
		}
	}
	return "", fmt.Errorf("渠道 %s 没有平台登记", channel)
}

// RecordStepIdentity 先绑定宿主 ToolID，provider call id 只用于模型协议配对。
func (s *Store) RecordStepIdentity(id string, in kernel.Invocation, ordinal int) error {
	if id != in.ToolID || in.ToolID == "" {
		return ErrInputConflict
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var order any
	if ordinal >= 0 {
		order = ordinal
	}
	r, err := s.Trace.Exec(`INSERT INTO steps(id,character_id,conversation,turn_id,kind,status,identity_json,provider_call_id,ordinal) VALUES(?,?,?,?,'tool','waiting',?,?,?)
		ON CONFLICT(id) DO UPDATE SET identity_json=excluded.identity_json,provider_call_id=excluded.provider_call_id,ordinal=coalesce(excluded.ordinal,steps.ordinal)
		WHERE steps.identity_json IS NULL OR steps.identity_json=excluded.identity_json`, id, in.Scope.CharacterID, in.Scope.Conversation, in.RunID, string(raw), in.ProviderCallID, order)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return ErrInputConflict
	}
	return err
}

func (s *Store) AdmitKernelInputs(ctx context.Context, ids []string) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		r, err := tx.ExecContext(ctx, `UPDATE kernel_inbox SET state='admitted',revision=revision+1 WHERE input_id=? AND state='accepted'`, id)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInputConflict
		}
	}
	return tx.Commit()
}

// HeldKernelInputs 列出重启后没人处理的输入（state=held），按受理顺序；domain 为空列全部会话。
func (s *Store) HeldKernelInputs(ctx context.Context, domain string) ([]KernelInput, error) {
	q, args := `SELECT `+kernelInputColumns+` FROM kernel_inbox WHERE state='held'`, []any{}
	if domain != "" {
		q, args = q+` AND domain=?`, append(args, domain)
	}
	rows, err := s.Trace.QueryContext(ctx, q+` ORDER BY accepted_at,sequence`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KernelInput
	for rows.Next() {
		in, err := scanKernelInput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// KernelInputByID 按输入号取一行；没有这行返回 sql.ErrNoRows。
func (s *Store) KernelInputByID(ctx context.Context, id string) (KernelInput, error) {
	return scanKernelInput(s.Trace.QueryRowContext(ctx, `SELECT `+kernelInputColumns+` FROM kernel_inbox WHERE input_id=?`, id))
}

// ReleaseHeldKernelInput 把 held 的输入换成一次新的排队运行（保留 InputID 和原文事件，运行身份由调用方重新分配）。
// 只认 held：同一句话不会被放行两次。
func (s *Store) ReleaseHeldKernelInput(ctx context.Context, in KernelInput) error {
	tx, err := s.Trace.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(in.Identity)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE kernel_inbox SET state='accepted',mode='queue',identity_json=?,run_id=?,revision=revision+1 WHERE input_id=? AND state='held'`, string(raw), in.Identity.RunID, in.Identity.InputID)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrInputConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_runs(run_id,identity_json,state) VALUES(?,?,'queued')`, in.Identity.RunID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

// CancelKernelInput 把一句还没开始处理（accepted 排队中或 held）的话标成已取消；只改状态，原文事件和载荷都留着。
func (s *Store) CancelKernelInput(ctx context.Context, id string) error {
	r, err := s.Trace.ExecContext(ctx, `UPDATE kernel_inbox SET state='cancelled',revision=revision+1 WHERE input_id=? AND state IN ('accepted','held')`, id)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrInputConflict
	}
	return nil
}
