package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/embed"
)

// BaseSchema 是 Bun 版 memory.db 的五张基础表，也是新建角色记忆库和主人共用池的冻结基线：
// 以后加表、加列只走 migrations/ 新文件，不改这里；导出只是给建库和测试夹具用，生产打开已有库不执行它。
// 共用池只存主人自身事实。角色事务先提交持久交接记录，成功发布后删交接；崩溃重放沿事实和证据的既有幂等规则。
// 角色库不再写这份事实，因此不会出现两个角色各自提升、取代同一件主人事实。
const BaseSchema = `
CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, text TEXT NOT NULL, subject TEXT, visibility TEXT NOT NULL,
 source_seqs TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'active', superseded_by TEXT, created_at INTEGER NOT NULL,
 origin TEXT, conversation TEXT, audience_at_origin TEXT, speech_act TEXT, attributed_to TEXT, scope TEXT, no_mention INTEGER NOT NULL DEFAULT 0,
 event_at INTEGER, learned_at INTEGER, valid_from INTEGER, valid_to INTEGER, temporal TEXT);
CREATE TABLE episodes (id TEXT PRIMARY KEY, conversation TEXT NOT NULL, text TEXT NOT NULL, visibility TEXT NOT NULL,
 from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, created_at INTEGER NOT NULL, scope TEXT, origin TEXT, audience_at_origin TEXT,
 event_at INTEGER, learned_at INTEGER, status TEXT NOT NULL DEFAULT 'active');
CREATE TABLE fact_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT, fact_id TEXT NOT NULL, op TEXT NOT NULL, at INTEGER NOT NULL, by TEXT NOT NULL, reason TEXT);
CREATE TABLE summary_state (conversation TEXT PRIMARY KEY, last_seq INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE memory_jobs (conversation TEXT NOT NULL, from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, version INTEGER NOT NULL,
 done_at INTEGER NOT NULL, PRIMARY KEY (conversation, from_seq, to_seq, version));`

// OpenOwnerPool 的文件由宿主显式提供；只读且尚无文件时返回 nil（没有任何已发布事实）。不迁移角色旧事实。
// 可写时走升级护栏（规格-库版本与升级护栏 第 8 条）：迁移前拍快照、失败换回并返回错误，App 照旧整个不启动；
// 快照目录和库相对数据目录的路径由 Options.SnapshotDir / SnapshotRel 给（App 传 paths 的值），没给就放在库旁边。
func OpenOwnerPool(ctx context.Context, file string, e embed.Embedder, people []Person, opt Options) (*Memory, error) {
	existed := false
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		if !opt.Writable {
			return nil, nil
		}
	} else if err != nil {
		return nil, err
	} else {
		existed = true
	}
	if opt.Writable {
		// 交给护栏之前先只读确认已有的是一个记忆库：外人的库直接报错，不拍快照、不动文件
		if existed {
			if err := checkMemoryDBShape(ctx, file, true); err != nil {
				return nil, err
			}
		}
		tracks, err := memoryTracks()
		if err != nil {
			return nil, err
		}
		snapshotDir, rel := ownerSnapshot(opt, file)
		db, rep, err := dbguard.Open(ctx, file, tracks, dbguard.Options{
			SnapshotDir: snapshotDir,
			RelName:     rel,
			OpenDB: func(file string, fresh bool) (*sql.DB, error) {
				return openBaseDB(ctx, file, fresh)
			},
		})
		if db != nil {
			db.Close()
		}
		if err != nil {
			return nil, err
		}
		if len(rep.Newer) > 0 && opt.Logf != nil {
			for _, n := range rep.Newer {
				opt.Logf("主人共用记忆库的迁移轨 %s 比程序新（%d > %d），按兼容版本打开", n.Track, n.Version, n.Latest)
			}
		}
	}
	opt.Character = "owner-pool"
	opt.Shared = nil
	return Open(ctx, file, e, people, opt)
}

// ownerSnapshot 是主人共用记忆库快照的目录和库相对数据目录的路径；选项没给时放在库文件旁边，方便工具和测试。
func ownerSnapshot(opt Options, file string) (string, string) {
	dir, rel := opt.SnapshotDir, opt.SnapshotRel
	if dir == "" {
		dir = filepath.Join(filepath.Dir(file), "upgrade-snapshots")
	}
	if rel == "" {
		rel = filepath.Base(file)
	}
	return dir, rel
}

// openBaseDB 是护栏拿的可写连接：fresh 时先建基础表（BaseSchema），再交给护栏跑 memory 轨。
func openBaseDB(ctx context.Context, file string, fresh bool) (*sql.DB, error) {
	if fresh {
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	db, err := openWritable(file)
	if err != nil {
		return nil, err
	}
	if fresh {
		// 基础表要么全建、要么一张不建：半套表会让下次启动认不出是空库，也不再重建（复核 M1）
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, BaseSchema); err != nil {
			_ = tx.Rollback()
			db.Close()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

type sharedAction struct {
	By       string
	OnDup    DupMode
	Audience *Audience
	Batch    string
	Fact     *FactOp     `json:",omitempty"`
	Evidence *EvidenceOp `json:",omitempty"`
	Confirm  *ConfirmOp  `json:",omitempty"`
	Revoke   *RevokeOp   `json:",omitempty"`
}

func (a sharedAction) op() Op {
	switch {
	case a.Fact != nil:
		return *a.Fact
	case a.Evidence != nil:
		return *a.Evidence
	case a.Confirm != nil:
		return *a.Confirm
	default:
		return *a.Revoke
	}
}

func (t *txRun) sharedAction(ctx context.Context, op Op) (sharedAction, bool, error) {
	pool := t.w.m.opt.Shared
	if pool == nil {
		return sharedAction{}, false, nil
	}
	a := sharedAction{By: t.b.By, OnDup: t.b.OnDup, Audience: t.b.Audience}
	has := func(id string) bool { _, by := pool.snapshot(); return by["f:"+id] != nil }
	switch o := op.(type) {
	case FactOp:
		if !o.Share || o.Subject != "owner" || (o.Kind != "fact" && o.Kind != "preference") {
			return a, false, nil
		}
		// 已有专属同文不能因为这次模型漏了限制而变成共用。
		live, dead, err := t.findDup(ctx, o.Subject, o.Text)
		if err != nil {
			return a, false, err
		}
		if live != nil || dead != nil {
			return a, false, nil
		}
		o.LearnedBy = t.w.m.opt.Character
		o.Share = false
		a.Fact = &o
	case EvidenceOp:
		if !has(o.FactID) {
			return a, false, nil
		}
		a.Evidence = &o
	case ConfirmOp:
		if !has(o.FactID) {
			return a, false, nil
		}
		a.Confirm = &o
	case RevokeOp:
		if !has(o.ID) {
			return a, false, nil
		}
		a.Revoke = &o
	default:
		return a, false, nil
	}
	return a, true, nil
}

func (t *txRun) queueShared(ctx context.Context, a sharedAction) (Outcome, error) {
	id := t.w.newID()
	if t.sharedBatch == "" {
		t.sharedBatch = id
	}
	a.Batch = t.sharedBatch
	raw, err := json.Marshal(a)
	if err != nil {
		return Outcome{}, err
	}
	if _, err = t.tx.ExecContext(ctx, `INSERT INTO shared_outbox(id,payload) VALUES (?,?)`, id, string(raw)); err != nil {
		return Outcome{}, err
	}
	t.count("shared_queued")
	return Outcome{Result: ResultAdded, Ref: "pending:" + id}, nil
}

// FlushShared 在正式启动和每次写入前后执行；失败如实返回，交接记录留在角色库供重启恢复。
func (m *Memory) FlushShared(ctx context.Context) error {
	if m.opt.Shared == nil || m.w == nil {
		return nil
	}
	m.shareMu.Lock()
	defer m.shareMu.Unlock()
	_, err := m.flushShared(ctx)
	return err
}

func (m *Memory) flushShared(ctx context.Context) (map[string]Outcome, error) {
	done := map[string]Outcome{}
	unlock, err := m.w.lock(ctx, DefaultWriteWait)
	if err != nil {
		return nil, err
	}
	rows, err := m.w.db.QueryContext(ctx, `SELECT id,payload FROM shared_outbox ORDER BY rowid`)
	if err != nil {
		unlock()
		return nil, err
	}
	type pending struct {
		id     string
		action sharedAction
	}
	var pendingRows []pending
	for rows.Next() {
		var p pending
		var raw string
		if err = rows.Scan(&p.id, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &p.action); err != nil {
			break
		}
		if p.action.Batch == "" || (p.action.Fact == nil && p.action.Evidence == nil && p.action.Confirm == nil && p.action.Revoke == nil) {
			err = errors.New("共用池交接记录没有批次或动作")
			break
		}
		pendingRows = append(pendingRows, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	unlock()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(pendingRows); {
		a := pendingRows[start].action
		end := start + 1
		for end < len(pendingRows) && pendingRows[end].action.Batch == a.Batch {
			end++
		}
		ops := make([]Op, end-start)
		ids := make([]any, end-start)
		for i, p := range pendingRows[start:end] {
			ops[i], ids[i] = p.action.op(), p.id
		}
		res, err := m.opt.Shared.apply(ctx, Batch{By: a.By, OnDup: a.OnDup, Ops: ops, Audience: a.Audience, confirmFrom: m})
		if err != nil {
			return nil, fmt.Errorf("发布共用记忆：%w", err)
		}
		unlock, err := m.w.lock(ctx, DefaultWriteWait)
		if err != nil {
			return nil, err
		}
		_, err = m.w.db.ExecContext(ctx, `DELETE FROM shared_outbox WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...)
		unlock()
		if err != nil {
			return nil, err
		}
		for i, p := range pendingRows[start:end] {
			done["pending:"+p.id] = res.Outcomes[i]
		}
		start = end
	}
	return done, nil
}

// remember 直接在最终归属的库提交；编号错或目标已被改过时不留下永远失败的交接记录。
func (m *Memory) rememberTarget(ctx context.Context, op FactOp) (*writer, FactOp, error) {
	if m.opt.Shared == nil || !op.Share {
		return m.w, op, nil
	}
	unlock, err := m.w.lock(ctx, m.rememberWait)
	if err != nil {
		return nil, op, err
	}
	defer unlock()
	tx, err := m.w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, op, err
	}
	defer tx.Rollback()
	t := txRun{w: m.w, tx: tx, dups: map[string]map[string][]*dupRow{}}
	live, dead, err := t.findDup(ctx, op.Subject, op.Text)
	if err != nil {
		return nil, op, err
	}
	if live != nil || dead != nil {
		return m.w, op, nil
	}
	op.Share, op.LearnedBy = false, m.opt.Character
	return m.opt.Shared.w, op, nil
}

func exclusiveSource(text string) bool {
	for _, s := range []string{"只告诉你", "只跟你说", "只有你知道", "别告诉", "不要告诉", "别跟", "不要跟", "专属", "昵称", "称呼", "我们的约定", "和你的约定", "一起经历"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// 只有直接的主人原话和明确的非专属分类能开放共享；名字和模型自报权限不参与判断。
func shareableFact(kind, subject string, onlyHer *bool, direct bool, source string) bool {
	return subject == "owner" && (kind == "fact" || kind == "preference") && onlyHer != nil && !*onlyHer && direct && !exclusiveSource(source)
}
