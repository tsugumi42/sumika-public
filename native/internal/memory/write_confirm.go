package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// 待确认的事实攒够证据变有效（规格-记忆写入上线 W3b，ADR-160）。
// 在写事务里、证据刚记完的那一刻数（merge 和 addEvidence 调 confirmByEvidence），不在读路径、不在定时任务里：
// 两个会话同时给同一条记第三次证据，写锁把它们串起来，后一个在事务里重新数行数，看到已经是有效的就不动。
// 提升只改状态和谁放行的（confirmed_by = ai）、speech_act，范围一个字不动——这是它和 remember 同文放行（promote，会并上范围）的唯一区别，
// 所以单独一个函数，不复用 promote。日志只写计数和短码，不写正文。

const (
	defaultConfirmOccasions = 3 // ADR-160 说"2～3 次"，取严的那头
	minConfirmOccasions     = 2 // 不论配成几都不低于它：一次证据不够
)

// defaultConfirmKinds：默认可以自动提升的种类。promise 和 boundary 每轮都给、直接塑造她的行为，群友三天说三遍不该让它生效，要主人 remember 放行
var defaultConfirmKinds = []string{"fact", "preference"}

func (w *writer) confirmOccasions() int { return ConfirmOccasionsFor(w.m.opt.ConfirmOccasions) }

// ConfirmOccasionsFor：配置里的 confirmOccasions 实际要几次算数。零或负用默认 3，配成 1 也按 2 算（一次永远不够）。
// 写事务和记忆页的"已攒 N / M 次"都调它，页面不另抄一遍
func ConfirmOccasionsFor(configured int) int {
	if configured <= 0 {
		return defaultConfirmOccasions
	}
	return max(configured, minConfirmOccasions)
}

// confirmKind：这一种事实攒够证据能不能自动提升。nil 用默认；不认识的种类永远不提升
func (w *writer) confirmKind(kind string) bool {
	kinds := w.m.opt.ConfirmKinds
	if kinds == nil {
		kinds = defaultConfirmKinds
	}
	return slices.Contains(factKinds, kind) && slices.Contains(kinds, kind)
}

// countOnce：同一个事务里，同一条事实的同一个计数只数一次（一批里一条候选可能先后收到几次证据）
func (t *txRun) countOnce(key, factID string) {
	k := key + "|" + factID
	if t.once[k] {
		return
	}
	if t.once == nil {
		t.once = map[string]bool{}
	}
	t.once[k] = true
	t.count(key)
}

// evidenceTally 是一条候选现在的证据账（fact_evidence 的行是不同的（天，会话））
type evidenceTally struct {
	rows   int // 证据行数
	self   int // 其中本人说的（by_subject 为真），每行各算一次
	others int // 别人转述的，按说话人去重后的人数：一个说话人整个候选最多算 1 次；没记说话人的不算（认不出是谁，不能当一个人）
}

// occasions 是算数的次数
func (e evidenceTally) occasions() int { return e.self + e.others }

// EvidenceTally 是 evidenceTally 给包外（记忆页）的样子：同一份账，字段公开
type EvidenceTally struct{ Rows, Self, Others, Occasions int }

// rowQuerier：*sql.DB 和 *sql.Tx 都满足，写事务（事务里现数）和只读页面（读连接上数）共用同一个计次函数
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// tallyEvidence 数这条候选现在的证据账。规则（规格 W3b 的计次规则）：本人说的每行一次；转述的按说话人去重；
// 够不够还要看"至少 1 次本人"（confirmByEvidence）。说话人的比较不分大小写、去首尾空白（只会合并得更多，即更严）。
// 写事务（txRun.tally）和记忆页（TallyEvidence）都走它，规则只有这一份（规格-记忆页 4.1）
func tallyEvidence(ctx context.Context, q rowQuerier, factID string) (evidenceTally, error) {
	var e evidenceTally
	err := q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(by_subject <> 0), 0),
		(SELECT count(DISTINCT lower(trim(speaker))) FROM fact_evidence WHERE fact_id = ? AND by_subject = 0 AND trim(coalesce(speaker, '')) <> '')
		FROM fact_evidence WHERE fact_id = ?`, factID, factID).Scan(&e.rows, &e.self, &e.others)
	return e, err
}

// tally 在事务里现数这条候选的证据账
func (t *txRun) tally(ctx context.Context, factID string) (evidenceTally, error) {
	return tallyEvidence(ctx, t.tx, factID)
}

// TallyEvidence 给记忆页：在只读连接上数同一份账
func TallyEvidence(ctx context.Context, db *sql.DB, factID string) (EvidenceTally, error) {
	e, err := tallyEvidence(ctx, db, factID)
	return EvidenceTally{Rows: e.rows, Self: e.self, Others: e.others, Occasions: e.occasions()}, err
}

// wantsSupersede：这条候选写入时记下的"想取代谁"（add 修订的 after_json.wants_supersede，W2）。没有、读不懂都当没有
func (t *txRun) wantsSupersede(ctx context.Context, factID string) (string, error) {
	var after sql.NullString
	err := t.tx.QueryRowContext(ctx, `SELECT after_json FROM fact_revisions WHERE fact_id = ? AND op = 'add' ORDER BY id LIMIT 1`, factID).Scan(&after)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil || !after.Valid {
		return "", err
	}
	var m struct {
		Wants string `json:"wants_supersede"`
	}
	if json.Unmarshal([]byte(after.String), &m) != nil {
		return "", nil
	}
	return m.Wants, nil
}

// confirmByEvidence：这条待确认的事实攒够证据了就提升成有效（status = active，confirmed_by = ai），返回提升了没有。按顺序：
//  1. 事务里重读状态，只动 candidate（已经被放行、撤销、取代、提升过的不动）；种类要在 ConfirmKinds 里；
//  2. 算数的次数 ≥ confirmOccasions，并且其中至少 1 次是本人说的（关于某人自己的事不由别人投票，规格-记忆-v2 4.4 第 1 条）；
//  3. 候选写入时记下想取代某条旧事实（wants_supersede）：旧的主体和候选不一样 → 当没写（supersede_subject_mismatch），照常提升；
//     旧的还是 active 且 confirmed_by = ai → 同一个事务里取代它再提升；
//     旧的受保护（导入的、主人放行过的、不认识的值）→ 这条不提升，计 confirm_blocked_protected（它凑出来的三次证据不能压过主人的放行）；
//     旧的已经不是 active → 不取代，照常提升；
//  4. AutoConfirm 为假只记账：不取代、不改状态、不写修订，计 would_confirm。
//
// 范围一个字不动。记一条 confirm 修订（by 是这一批的 By，也就是 summary；before / after 里有证据行数和算数的次数）
func (t *txRun) confirmByEvidence(ctx context.Context, id string) (bool, error) {
	var kind, status, subject string
	err := t.tx.QueryRowContext(ctx, `SELECT kind, status, coalesce(subject, '') FROM facts WHERE id = ?`, id).Scan(&kind, &status, &subject)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "candidate") {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !t.w.confirmKind(kind) {
		return false, nil
	}
	tl, err := t.tally(ctx, id)
	if err != nil {
		return false, err
	}
	if tl.self < 1 || tl.occasions() < t.w.confirmOccasions() {
		return false, nil
	}
	wants, err := t.wantsSupersede(ctx, id)
	if err != nil {
		return false, err
	}
	if wants != "" {
		// 写入时已经核对过主体；这里是提升时再核一次（事务里现读，旧事实的主体不会变，但记下的编号可能来自别处）。
		// 对不上就当没写想取代谁：候选自己的证据够了，照常提升，只是不顶掉别人名下的事
		if oldSubject, found, err := t.factSubject(ctx, wants); err != nil {
			return false, err
		} else if found && oldSubject != subject {
			t.countOnce(CountSupersedeSubjectMismatch, id)
			wants = ""
		}
	}

	if !t.w.m.AutoConfirming() || (t.b.confirmFrom != nil && !t.b.confirmFrom.AutoConfirming()) {
		// 记账模式：一个字节都不改。想取代的旧事实受保护的，开了开关也提不了，不能算进"会提升"里
		if wants != "" {
			_, res, err := t.supersedeTarget(ctx, wants)
			if err != nil {
				return false, err
			}
			if res == supersedeProtected {
				t.countOnce(CountConfirmBlockedProtected, id)
				return false, nil
			}
		}
		t.countOnce(CountWouldConfirm, id)
		return false, nil
	}

	replaced := false
	if wants != "" {
		_, res, err := t.supersede(ctx, wants, id)
		if err != nil {
			return false, err
		}
		if res == supersedeProtected {
			t.countOnce(CountConfirmBlockedProtected, id)
			return false, nil
		}
		replaced = res == supersedeDone
		if replaced {
			t.count(CountSuperseded)
		}
	}
	r, err := t.tx.ExecContext(ctx, `UPDATE facts SET status = 'active', confirmed_by = 'ai', speech_act = ? WHERE id = ? AND status = 'candidate'`, nullIfEmpty(speechActs[kind]), id)
	if err != nil {
		return false, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		// 同一个事务里刚读过它还是 candidate，写锁又在手上：到这里说明库被别的连接改了，整批作废比悄悄继续安全
		return false, fmt.Errorf("提升 %s：状态在事务里变了", shortRef(id))
	}
	before := map[string]any{"status": "candidate", "evidence_rows": tl.rows}
	after := map[string]any{"status": "active", "confirmed_by": "ai", "evidence_rows": tl.rows, "occasions": tl.occasions()}
	if replaced {
		after["superseded"] = wants
	}
	if err := t.revise(ctx, id, "confirm", before, after, fmt.Sprintf("攒够 %d 次不同场合", tl.occasions())); err != nil {
		return false, err
	}
	t.markDup(id, "active")
	t.count(CountConfirmed)
	t.w.logf("记忆[%s]：%s 攒够证据，提升成有效（%d 次不同场合，其中本人 %d 次）", t.w.m.opt.Character, shortRef(id), tl.occasions(), tl.self)
	return true, nil
}

// confirmOp 是 ConfirmOp 在事务里的样子：同一个 confirmByEvidence，结局写成 Outcome
func (t *txRun) confirmOp(ctx context.Context, op ConfirmOp) (Outcome, error) {
	confirmed, err := t.confirmByEvidence(ctx, op.FactID)
	if err != nil {
		return Outcome{}, err
	}
	if confirmed {
		return Outcome{Result: ResultConfirmed, Ref: "f:" + op.FactID, Status: "active", Confirmed: true}, nil
	}
	var status string
	if err := t.tx.QueryRowContext(ctx, `SELECT status FROM facts WHERE id = ?`, op.FactID).Scan(&status); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	return Outcome{Result: ResultNoop, Ref: "f:" + op.FactID, Status: status}, nil
}

// ConfirmSweepResult 是启动扫描这一趟的账：看了几条候选（Checked，只数范围在放行场合里、已经进事务的）、提升了几条、
// 几条因为想取代的旧事实受保护而没提升、几条因为范围不在放行的场合里没碰（Skipped，只数种类能自动提升、证据行数够的，不进 Checked）
type ConfirmSweepResult struct{ Checked, Confirmed, Blocked, Skipped int }

// ConfirmSweep：打开自动提升（Options.AutoConfirm）的那次启动——以及以后每次启动，成本是一条查询——对现有的待确认事实补跑一遍
// confirmByEvidence（同一规则、每条一个事务、记 confirm 修订）。它为的是：记账模式下已经攒够证据的候选，不用各自再等一次证据，
// 就和主人在报告里看到的"会提升"那一批对得上。AutoConfirm 为假什么都不做；Writable 为假返回 ErrNotWritable。
// 只挑种类在 ConfirmKinds 里、证据行数够的候选，剩下的不用进事务。
// 范围不在放行场合里的候选不碰（scopeSweepable，复核后补）：导入的旧候选里有范围是群、别人的私聊的，证据行数够也不能在启动这一刻悄悄变成有效——
// 写入场合的名单仍独立生效；共用池也用发起角色的名单及当前自动提升状态，不借池本身的开关放宽。
func (m *Memory) ConfirmSweep(ctx context.Context) (ConfirmSweepResult, error) {
	w, err := m.writer()
	if err != nil {
		return ConfirmSweepResult{}, err
	}
	var r ConfirmSweepResult
	if !m.AutoConfirming() {
		return r, nil
	}
	places := m.writePlaces()
	r, err = m.confirmSweep(ctx, w, places)
	if err != nil || m.opt.Shared == nil || !m.opt.Shared.AutoConfirming() {
		return r, err
	}
	shared, err := m.confirmSweep(ctx, m.opt.Shared.w, places)
	r.Checked += shared.Checked
	r.Confirmed += shared.Confirmed
	r.Blocked += shared.Blocked
	r.Skipped += shared.Skipped
	return r, err
}

func (m *Memory) confirmSweep(ctx context.Context, w *writer, places []string) (r ConfirmSweepResult, err error) {
	ids, err := w.sweepCandidates(ctx)
	if err != nil {
		return r, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		scopes, err := w.sweepScopes(ctx, id)
		if err != nil {
			return r, err
		}
		if !scopesSweepable(places, scopes) {
			r.Skipped++
			continue
		}
		batch := Batch{By: BySummary, Ops: []Op{ConfirmOp{FactID: id}}, Wait: summaryWriteWait}
		if w.m != m {
			batch.confirmFrom = m
		}
		res, err := w.m.apply(ctx, batch)
		if err != nil {
			return r, err
		}
		r.Checked++
		r.Confirmed += res.Counts[CountConfirmed]
		r.Blocked += res.Counts[CountConfirmBlockedProtected]
	}
	return r, nil
}

// scopesSweepable：启动补提升能不能碰这条候选。"谁能做什么"的表，每个范围都要放行，一个不放行整条不碰：
//
//	owner       → 名单非空（主人的事在任何放行场合里都会写成 owner 范围）
//	conv:<会话> → 这个会话的场合在名单里；平台私聊要等读了事件才知道是不是主人的，补提升不读事件，所以平台私聊必须名单里有 platform-private（只有 owner-private 不够，更紧）
//	person:<人> → 名单里有 platform-private（某人的私聊只有放行所有人的私聊才会写出这种范围）
//	public、认不出的 → 不放行
//
// 没有范围（不该有）也不放行。名单是 places 的取值（WritePlaces）
func scopesSweepable(places, scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, s := range scopes {
		ok := false
		switch {
		case s == "owner":
			ok = len(places) > 0
		case strings.HasPrefix(s, "conv:"):
			if place, known := convPlace(strings.TrimPrefix(s, "conv:")); known {
				if place == PlatformPrivate {
					ok = slices.Contains(places, WritePlacePlatformPrivate)
				} else {
					ok = slices.Contains(places, writePlaceOf(place))
				}
			}
		case strings.HasPrefix(s, "person:"):
			ok = slices.Contains(places, WritePlacePlatformPrivate)
		}
		if !ok {
			return false
		}
	}
	return true
}

// sweepScopes：这条候选现在的范围组（item_scopes 是真源；没有行就退回 facts.scope，再没有按最严的 owner 算）
func (w *writer) sweepScopes(ctx context.Context, id string) ([]string, error) {
	rows, err := w.db.QueryContext(ctx, `SELECT scope FROM item_scopes WHERE ref = ? ORDER BY scope`, "f:"+id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	var scope sql.NullString
	if err := w.db.QueryRowContext(ctx, `SELECT scope FROM facts WHERE id = ?`, id).Scan(&scope); err != nil {
		return nil, err
	}
	return scopeList(scope.String), nil
}

// sweepCandidates：待确认、种类能自动提升、证据行数已经够次数的事实编号。读完就放开连接，再一条一条进事务
func (w *writer) sweepCandidates(ctx context.Context) ([]string, error) {
	var kinds []any
	for _, k := range factKinds {
		if w.confirmKind(k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	rows, err := w.db.QueryContext(ctx, `SELECT f.id FROM facts f WHERE f.status = 'candidate' AND f.kind IN (`+strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")+`)
		AND (SELECT count(*) FROM fact_evidence e WHERE e.fact_id = f.id) >= ? ORDER BY f.id`, append(kinds, w.confirmOccasions())...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
