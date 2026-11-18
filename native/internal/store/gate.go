package store

// 群聊闸门要读写的库（规格-渠道中间件 第八节，规格-群聊闸门 第三～五节）：
//   - events.db：她上次看完以后别人说的话（PendingMessages）、最近 5 分钟的窗口（RecentWindow）；
//   - trace.db：窗口状态（gate_state）、每次判断一行（gate_decisions）、频率规则（gate_rules）、档位（gate_modes）。
// 表都在 0001 的镜像里，和 Bun 版同一份结构，所以 Go 自己的库和 Bun 的库用同一套列。

import (
	"database/sql"
	"errors"
)

// PendingRow 是一条消息事件的几个字段，段原样（JSON 文本）交给调用方解
type PendingRow struct {
	Seq       int64
	ActorKind string
	ActorID   string
	Origin    string
	CreatedAt int64 // 毫秒；平台时间，补录来的是当初发出的时间
	Text      string
	Segments  sql.NullString
}

const pendingCols = `e.seq, e.actor_kind, e.actor_id, COALESCE(e.origin, ''), e.created_at, e.text, e.segments_json`

func scanPending(rows *sql.Rows) ([]PendingRow, error) {
	defer rows.Close()
	var out []PendingRow
	for rows.Next() {
		var r PendingRow
		if err := rows.Scan(&r.Seq, &r.ActorKind, &r.ActorID, &r.Origin, &r.CreatedAt, &r.Text, &r.Segments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pendingWhere 是"她上次看完以后、别人说的话"的条件（别名 e）：这个会话、seq 在 afterSeq 之后、是消息；不是这个角色自己的（她的号发的都不算，
// 含真人登录她的号手动发的），不是插件自动发的（automation:*），不是补录来的。参数顺序：会话、afterSeq、角色
const pendingWhere = `e.conversation = ? AND e.seq > ? AND e.kind = 'message' AND NOT (e.actor_kind = 'character' AND e.actor_id = ?)
	AND COALESCE(e.origin, '') NOT GLOB 'automation:*' AND NOT ` + backfillSQL

// PendingMessages 是这个会话里 afterSeq 之后、别人说的话（见 pendingWhere），被撤回的不算；只取最近 limit 条，按 seq 正序（旧的在前）。
// earlier 是比取到的更早的还有几条（只在取满了 limit 时才数；最多数到 earlierCap+1 就停，数满了调用方说"一万条以上"），里面撤回的没逐条核，会多算进去
// （撤回很少；Bun 版也是这样）。
//
// 撤回的判断和 RecentMessages 一样：有一条同会话、更晚的 notice（recall）指向它。不指定索引也走得动：条件里 seq 有下界、按 seq 倒序，
// 走 ix_events_conv（会话, seq）读到够 limit 条就停
func (s *Store) PendingMessages(conversation, characterID string, afterSeq int64, limit, earlierCap int) (rows []PendingRow, earlier int, err error) {
	if limit <= 0 {
		return nil, 0, nil
	}
	r, err := s.Events.Query(`SELECT `+pendingCols+` FROM events e WHERE `+pendingWhere+`
		AND NOT EXISTS (SELECT 1 FROM events r WHERE r.conversation = e.conversation AND r.seq > e.seq AND r.kind = 'notice' AND r.reply_to = e.id
			AND json_valid(r.segments_json) AND json_extract(r.segments_json, '$.notice') = 'recall')
		ORDER BY e.seq DESC LIMIT ?`, conversation, afterSeq, characterID, limit)
	if err != nil {
		return nil, 0, err
	}
	if rows, err = scanPending(r); err != nil {
		return nil, 0, err
	}
	if len(rows) == limit {
		oldest := rows[len(rows)-1].Seq
		err = s.Events.QueryRow(`SELECT count(*) FROM (SELECT 1 FROM events e WHERE `+pendingWhere+` AND e.seq < ? LIMIT ?)`,
			conversation, afterSeq, characterID, oldest, earlierCap+1).Scan(&earlier)
		if err != nil {
			return nil, 0, err
		}
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, earlier, nil
}

// RecentWindow 是这个会话里时间在 (fromMS, toMS] 的消息（含她自己说的、补录的，不做筛选），按 seq 正序，加上这个角色在 toMS 以前说的最后一条
// （last，没有是 nil）：算最近 5 分钟她说话的占比、离她上次说话多久。last 可能不在窗口里
func (s *Store) RecentWindow(conversation, characterID string, fromMS, toMS int64) (rows []PendingRow, last *PendingRow, err error) {
	r, err := s.Events.Query(`SELECT `+pendingCols+` FROM events e INDEXED BY ix_events_conv_time
		WHERE e.conversation = ? AND e.kind = 'message' AND e.created_at > ? AND e.created_at <= ? ORDER BY e.seq`, conversation, fromMS, toMS)
	if err != nil {
		return nil, nil, err
	}
	if rows, err = scanPending(r); err != nil {
		return nil, nil, err
	}
	l, err := s.Events.Query(`SELECT `+pendingCols+` FROM events e INDEXED BY ix_events_conv_time
		WHERE e.conversation = ? AND e.kind = 'message' AND e.actor_kind = 'character' AND e.actor_id = ? AND e.created_at <= ?
		ORDER BY e.created_at DESC, e.seq DESC LIMIT 1`, conversation, characterID, toMS)
	if err != nil {
		return nil, nil, err
	}
	lr, err := scanPending(l)
	if err != nil {
		return nil, nil, err
	}
	if len(lr) == 1 {
		last = &lr[0]
	}
	return rows, last, nil
}

// EventRow 按（会话, seq）取一条事件的 PendingRow 字段，没有 ok 为假。
// 带上会话：seq 只在一个库（一个平台）里唯一，选错库时取到空，不会取到别的平台那一行（规格-聊天记录按平台分库 第四节第 2 条）
func (s *Store) EventRow(conversation string, seq int64) (r PendingRow, ok bool, err error) {
	rows, err := s.Events.Query(`SELECT `+pendingCols+` FROM events e WHERE e.conversation = ? AND e.seq = ?`, conversation, seq)
	if err != nil {
		return PendingRow{}, false, err
	}
	out, err := scanPending(rows)
	if err != nil || len(out) == 0 {
		return PendingRow{}, false, err
	}
	return out[0], true, nil
}

// ───────────── trace.db ─────────────

// LoadGateState 读一个（角色，会话）的窗口状态：JSON 文本和"上次看完"的 seq。没存过 ok 为假
func (s *Store) LoadGateState(key string) (state string, sinceSeq int64, ok bool, err error) {
	err = s.Trace.QueryRow(`SELECT state, since_seq FROM gate_state WHERE key = ?`, key).Scan(&state, &sinceSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return state, sinceSeq, err == nil, err
}

// SaveGateState 存窗口状态
func (s *Store) SaveGateState(key, state string, sinceSeq, now int64) error {
	_, err := s.Trace.Exec(`INSERT INTO gate_state (key, state, since_seq, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET state = excluded.state, since_seq = excluded.since_seq, updated_at = excluded.updated_at`, key, state, sinceSeq, now)
	return err
}

// 闸门放行以后回合的结局（gate_decisions.outcome）。前五个沿用 Bun；被拦、被打断是渠道中间件补的。
const (
	GateOutcomeSpoke       = "spoke"       // 正常结束，至少一句写进说话计划
	GateOutcomeDry         = "dry"         // 正常结束，至少一句写进试运行计划，也算开口
	GateOutcomeSelfSkip    = "self-skip"   // 正常结束，模型没有回复正文
	GateOutcomeError       = "error"       // 内核出错、裁决异常，或回复写库失败
	GateOutcomeFuse        = "fuse"        // 回复全被拦下，最后的原因是字数保险丝
	GateOutcomeBlocked     = "blocked"     // 回复全被拦下：守卫、档位检查，或改写以后为空
	GateOutcomeInterrupted = "interrupted" // 回合被打断，缓冲的回复不发
)

// GateDecisionRow 是 gate_decisions 的一行（规格-群聊闸门 第五节）。可空的数字用指针
type GateDecisionRow struct {
	ID            int64
	At            int64
	CharacterID   string
	Conversation  string
	Seq           int64
	Batch         int
	Features      string // JSON，没有是空
	Probability   *float64
	Threshold     *float64
	Expected      *float64
	Target        *float64
	Actual        *int
	Frequency     float64
	Verdict       string
	Reason        string
	Outcome       string
	OutcomeReason string
}

// AddGateDecision 记一次判断，返回 id
func (s *Store) AddGateDecision(d GateDecisionRow) (int64, error) {
	var feat any
	if d.Features != "" {
		feat = d.Features
	}
	res, err := s.Trace.Exec(`INSERT INTO gate_decisions (at, character_id, conversation, seq, batch, features, probability, threshold, expected, target, actual, frequency, verdict, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.At, d.CharacterID, d.Conversation, d.Seq, d.Batch, feat, d.Probability, d.Threshold, d.Expected, d.Target, d.Actual, d.Frequency, d.Verdict, d.Reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GateDecisions 读一个会话最近的判断，新的在前
func (s *Store) GateDecisions(conversation string, limit int) ([]GateDecisionRow, error) {
	rows, err := s.Trace.Query(`SELECT id, at, character_id, conversation, seq, batch, COALESCE(features, ''), probability, threshold, expected, target, actual, frequency, verdict, reason,
		COALESCE(outcome, ''), COALESCE(outcome_reason, '') FROM gate_decisions WHERE conversation = ? ORDER BY id DESC LIMIT ?`, conversation, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GateDecisionRow
	for rows.Next() {
		var d GateDecisionRow
		if err := rows.Scan(&d.ID, &d.At, &d.CharacterID, &d.Conversation, &d.Seq, &d.Batch, &d.Features, &d.Probability, &d.Threshold, &d.Expected, &d.Target, &d.Actual,
			&d.Frequency, &d.Verdict, &d.Reason, &d.Outcome, &d.OutcomeReason); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetGateOutcome 只写第一次结局；关机封库后迟到的回填安静丢掉。
func (s *Store) SetGateOutcome(id int64, outcome, reason string) error {
	owner := s.outcomeStore()
	owner.outcomeMu.Lock()
	defer owner.outcomeMu.Unlock()
	if owner.outcomesSealed {
		return nil
	}
	var r any
	if reason != "" {
		r = reason
	}
	_, err := s.Trace.Exec(`UPDATE gate_decisions SET outcome = ?, outcome_reason = ? WHERE id = ? AND outcome IS NULL`, outcome, r, id)
	return err
}

func (s *Store) outcomeStore() *Store {
	if s.outcomeOwner != nil {
		return s.outcomeOwner
	}
	return s
}

// SealOutcomes 在关库前接手所有未完成的放行判断，并挡住迟到的判断、回合结局。
// 和写结局共用锁：已经开始的写完才封，封住以后不再碰连接。
func (s *Store) SealOutcomes() error {
	owner := s.outcomeStore()
	owner.outcomeMu.Lock()
	defer owner.outcomeMu.Unlock()
	if owner.outcomesSealed {
		return nil
	}
	_, err := s.Trace.Exec(interruptGateDecisions)
	if err == nil {
		owner.outcomesSealed = true
	}
	return err
}

const interruptGateDecisions = `UPDATE gate_decisions SET outcome = 'interrupted', outcome_reason = '后台关闭，这一轮没做完'
	WHERE outcome IS NULL AND verdict IN ('forced', 'enter')`

// GateRule 是 gate_rules 的一行
type GateRule struct {
	Conversation string // "*" 是所有群
	Slot         string // 空是全天；"HH:MM-HH:MM"；"temp" 是临时的那一格
	Frequency    float64
	Until        int64 // 毫秒；0 是没有
}

// GateRules 读这个会话和"所有群"的频率规则
func (s *Store) GateRules(conversation string) ([]GateRule, error) {
	rows, err := s.Trace.Query(`SELECT conversation, slot, frequency, COALESCE(until, 0) FROM gate_rules WHERE conversation IN (?, '*')`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GateRule
	for rows.Next() {
		var r GateRule
		if err := rows.Scan(&r.Conversation, &r.Slot, &r.Frequency, &r.Until); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetGateRule 改一条频率规则；frequency 给 nil 是删掉这一层
func (s *Store) SetGateRule(r GateRule, remove bool) error {
	if remove {
		_, err := s.Trace.Exec(`DELETE FROM gate_rules WHERE conversation = ? AND slot = ?`, r.Conversation, r.Slot)
		return err
	}
	var until any
	if r.Until != 0 {
		until = r.Until
	}
	_, err := s.Trace.Exec(`INSERT INTO gate_rules (conversation, slot, frequency, until) VALUES (?, ?, ?, ?)
		ON CONFLICT(conversation, slot) DO UPDATE SET frequency = excluded.frequency, until = excluded.until`, r.Conversation, r.Slot, r.Frequency, until)
	return err
}

// SetGateRuleWithState 在同一个 trace 事务里改规则和归属标记，失败时一起回滚。
// remove 时一起删；r 为 nil 时只删标记（原规则已被主人改过）。
func (s *Store) SetGateRuleWithState(r *GateRule, key string, remove bool, now int64) error {
	return s.setGateRuleWithState(r, key, remove, now, nil)
}

func (s *Store) setGateRuleWithState(r *GateRule, key string, remove bool, now int64, afterRule func() error) error {
	tx, err := s.Trace.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r != nil {
		if remove {
			_, err = tx.Exec(`DELETE FROM gate_rules WHERE conversation = ? AND slot = ?`, r.Conversation, r.Slot)
		} else {
			var until any
			if r.Until != 0 {
				until = r.Until
			}
			_, err = tx.Exec(`INSERT INTO gate_rules (conversation, slot, frequency, until) VALUES (?, ?, ?, ?)
				ON CONFLICT(conversation, slot) DO UPDATE SET frequency = excluded.frequency, until = excluded.until`, r.Conversation, r.Slot, r.Frequency, until)
		}
		if err != nil {
			return err
		}
	}
	// 只给测试用：在规则改完、标记还没改时返回错误。
	if afterRule != nil {
		if err := afterRule(); err != nil {
			return err
		}
	}
	if remove {
		_, err = tx.Exec(`DELETE FROM gate_state WHERE key = ?`, key)
	} else {
		_, err = tx.Exec(`INSERT INTO gate_state (key, state, since_seq, updated_at) VALUES (?, '{}', 0, ?)
			ON CONFLICT(key) DO UPDATE SET state = excluded.state, since_seq = excluded.since_seq, updated_at = excluded.updated_at`, key, now)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GateModeOf 读一个会话的档位设置：这个会话自己的，没有就看所有群（"*"）的；都没设 ok 为假。值是 Bun 版的 off / dry / speak
func (s *Store) GateModeOf(conversation string) (mode string, ok bool, err error) {
	rows, err := s.Trace.Query(`SELECT conversation, mode FROM gate_modes WHERE conversation IN (?, '*')`, conversation)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var own, all string
	for rows.Next() {
		var c, m string
		if err := rows.Scan(&c, &m); err != nil {
			return "", false, err
		}
		if c == conversation {
			own = m
		} else {
			all = m
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	switch {
	case own != "":
		return own, true, nil
	case all != "":
		return all, true, nil
	}
	return "", false, nil
}

// SetGateMode 改一个会话的档位设置；mode 给空是删掉（回到所有群的 / 默认的）
func (s *Store) SetGateMode(conversation, mode string, now int64) error {
	if mode == "" {
		_, err := s.Trace.Exec(`DELETE FROM gate_modes WHERE conversation = ?`, conversation)
		return err
	}
	_, err := s.Trace.Exec(`INSERT INTO gate_modes (conversation, mode, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(conversation) DO UPDATE SET mode = excluded.mode, updated_at = excluded.updated_at`, conversation, mode, now)
	return err
}
