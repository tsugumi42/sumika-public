package store

// 出站的发送计划和段怎么入库、怎么改状态（规格-渠道中间件 第十节 10.1 第 7 步、10.3、10.4，第十五节第 2 步）。
// 只在 Go 自己的库上用（OpenFresh 打开的，有 0003 建的 outbound_plans、outbound_parts）。
//
// 一条回复一条计划，计划里的每一段一行。两条硬规矩：
//   - 计划、她这句话的 events、各段（状态"待发"）在一个事务里落库，之后才有第一次 Send（崩了以后留下的是"发送中"，不是"没记录"）；
//   - 段的每次状态转移都是条件更新（只有当前状态在 from 里才改）：发送的 goroutine、回声认领、关闭、重启几方同时碰同一段时，
//     先到的算数，后到的发现改不动（返回 ok=false）自己去看现在是什么状态，不会互相盖掉。
//     计划的汇总状态在同一个事务里重算，所以任何时候提交出去的库里，计划的状态都等于段状态的汇总。

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 计划的种类（outbound_plans.kind）：这次回复是怎么处理的
const (
	PlanKindSend        = "send"        // 发
	PlanKindDryRun      = "dry-run"     // 试运行：写库、不发
	PlanKindBlocked     = "blocked"     // 拦下（守卫、字数保险丝）：没有段，不存原文
	PlanKindInterrupted = "interrupted" // 已打断：这一轮没说出口，没有段，不存原文
	PlanKindError       = "error"       // 出错：没有段，不存原文
)

// 计划的状态（outbound_plans.state）。send 计划按段汇总；别的种类的状态就是种类本身（拦下的状态是 blocked……）
const (
	PlanSending = "sending" // 还有段没到终态（待发、发送中、等重试）
	PlanDone    = "done"    // 全部确认
	PlanPartial = "partial" // 有确认的，也有失败、没发的
	PlanFailed  = "failed"  // 一段也没确认，有段明确失败
	PlanUnknown = "unknown" // 有段结果未知：不知道发出去没有，既不是失败也不是完成
	PlanAborted = "aborted" // 一段也没发就被关掉、切档、重启了
)

// 段的状态（outbound_parts.state）
const (
	PartPending   = "pending"   // 待发
	PartSending   = "sending"   // 发送中：调了 Send，回执没回
	PartRetrying  = "retrying"  // 明确失败，次数没用完，等着重试
	PartConfirmed = "confirmed" // 确认：终态
	PartFailed    = "failed"    // 明确失败且用完重试（或适配器不支持）：终态
	PartUnknown   = "unknown"   // 结果未知：不重试；5 分钟内还能被回声认领
	PartAborted   = "aborted"   // 已中止：关闭、切档、重启、前面的段没发出去：终态
	PartDryRun    = "dry-run"   // 试运行的段：没发，也不是中止
)

// 回声能认领的段状态：还没到终态的，和结果未知的（10.4"回声认领"）
var echoClaimable = []string{PartSending, PartRetrying, PartUnknown}

// ErrNoPlan：没有这个计划
var ErrNoPlan = errors.New("没有这个发送计划")

// NewPart 是计划里的一段
type NewPart struct {
	Kind    string // text / image / emoji / quote / poke（types.Part*）
	Text    string // text 段的文字
	Ref     string // 图片哈希、表情 id、被引用的平台消息号、被戳的人
	PauseMS int    // 发这一段之前停多久（从上一段到终态算起），拆条时定死
}

// NewPlan 是要入库的一条计划
type NewPlan struct {
	ID           string
	CharacterID  string
	Conversation string
	Account      string // 用哪个号发
	Origin       string // 谁要发：character、automation:<插件名>
	Kind         string // PlanKind*
	Reason       string // 非 send 的计划为什么是这样（守卫拦下、保险丝、被打断……），不放原文
	// Event 是她这句话写进 events 的那一行；nil 就不写（拦下的、已打断的、出错的、只有戳一戳的）
	Event     *NewEvent
	Observers []string // 看到这行的角色（她自己）
	Parts     []NewPart
	At        int64 // 毫秒
}

func (p NewPlan) validate() error {
	switch {
	case p.ID == "" || p.CharacterID == "" || p.Conversation == "" || p.Account == "" || p.Origin == "":
		return errors.New("发送计划没填全（编号、角色、会话、号、来源）")
	}
	switch p.Kind {
	case PlanKindSend, PlanKindDryRun:
		if len(p.Parts) == 0 {
			return errors.New("发送计划没有段")
		}
		for i, pt := range p.Parts {
			if pt.Kind == "" || (pt.Kind == "message" && pt.Text == "") {
				return fmt.Errorf("第 %d 段没填全（种类、文字）", i)
			}
			if pt.PauseMS < 0 {
				return fmt.Errorf("第 %d 段的停顿是负的", i)
			}
		}
	case PlanKindBlocked, PlanKindInterrupted, PlanKindError:
		// 拦下的可能含私事，已打断、出错的没有说出口：都不存原文，也就没有段、没有 events 那一行
		if len(p.Parts) != 0 || p.Event != nil {
			return fmt.Errorf("%s 的计划不存原文：不能带段和事件", p.Kind)
		}
	default:
		return fmt.Errorf("计划的种类 %q 不认识", p.Kind)
	}
	return nil
}

// AppendPlan 在一个事务里写：她这句话的 events（有的话）、计划、各段（send 的是待发，dry-run 的是 dry-run）。
// 返回写进 events 的那一行的 seq，没写事件就是 0。有一步失败，三样都不在
func (s *Store) AppendPlan(p NewPlan) (eventSeq int64, err error) {
	if err := p.validate(); err != nil {
		return 0, err
	}
	tx, err := s.Events.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var seq sql.NullInt64
	if p.Event != nil {
		n, _, err := insertEvent(tx, *p.Event)
		if err != nil {
			return 0, err
		}
		if _, err := observe(tx, n, p.Observers); err != nil {
			return 0, err
		}
		seq = sql.NullInt64{Int64: n, Valid: true}
	}
	state, partState := p.Kind, PartDryRun
	var doneAt sql.NullInt64
	if p.Kind == PlanKindSend {
		state, partState = PlanSending, PartPending
	}
	if len(p.Parts) == 0 || p.Kind == PlanKindDryRun {
		doneAt = sql.NullInt64{Int64: p.At, Valid: true} // 不用发的，创建就是终态
	}
	if _, err := tx.Exec(`INSERT INTO outbound_plans (id, character_id, conversation, event_seq, account, origin, kind, reason, state, created_at, done_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.CharacterID, p.Conversation, seq, p.Account, p.Origin, p.Kind, nullable(p.Reason), state, p.At, doneAt); err != nil {
		return 0, err
	}
	for i, pt := range p.Parts {
		if _, err := tx.Exec(`INSERT INTO outbound_parts (plan_id, idx, kind, text, ref, pause_ms, state) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, i, pt.Kind, nullable(pt.Text), nullable(pt.Ref), pt.PauseMS, partState); err != nil {
			return 0, err
		}
	}
	return seq.Int64, s.commitChanged(tx, p.Conversation)
}

// Part 是库里的一段
type Part struct {
	PlanID     string
	Idx        int
	Kind       string
	Text       string
	Ref        string
	PauseMS    int
	State      string
	Attempts   int
	PlatformID string
	Via        string // reply（回包）、echo（回声认领）
	Err        string
	SentAt     int64 // 第一次调 Send 的时刻（毫秒）；没调过是 0
}

// Plan 是库里的一条计划，带着它的段（按 idx 排）
type Plan struct {
	ID           string
	CharacterID  string
	Conversation string
	EventSeq     int64 // 0 是没有
	Account      string
	Origin       string
	Kind         string
	Reason       string
	State        string
	CreatedAt    int64
	DoneAt       int64 // 0 是还没结束
	Parts        []Part
}

// GetPlan 读一条计划和它的段；没有返回 ErrNoPlan
func (s *Store) GetPlan(id string) (Plan, error) {
	var p Plan
	var seq, doneAt sql.NullInt64
	var reason sql.NullString
	err := s.Events.QueryRow(`SELECT id, character_id, conversation, event_seq, account, origin, kind, reason, state, created_at, done_at
		FROM outbound_plans WHERE id = ?`, id).
		Scan(&p.ID, &p.CharacterID, &p.Conversation, &seq, &p.Account, &p.Origin, &p.Kind, &reason, &p.State, &p.CreatedAt, &doneAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNoPlan
	}
	if err != nil {
		return Plan{}, err
	}
	p.EventSeq, p.DoneAt, p.Reason = seq.Int64, doneAt.Int64, reason.String
	rows, err := s.Events.Query(`SELECT plan_id, idx, kind, text, ref, pause_ms, state, attempts, platform_id, via, err, sent_at
		FROM outbound_parts WHERE plan_id = ? ORDER BY idx`, id)
	if err != nil {
		return Plan{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var pt Part
		var text, ref, pid, via, e sql.NullString
		var sent sql.NullInt64
		if err := rows.Scan(&pt.PlanID, &pt.Idx, &pt.Kind, &text, &ref, &pt.PauseMS, &pt.State, &pt.Attempts, &pid, &via, &e, &sent); err != nil {
			return Plan{}, err
		}
		pt.Text, pt.Ref, pt.PlatformID, pt.Via, pt.Err, pt.SentAt = text.String, ref.String, pid.String, via.String, e.String, sent.Int64
		p.Parts = append(p.Parts, pt)
	}
	return p, rows.Err()
}

// PartState 是一段现在的状态
func (s *Store) PartState(planID string, idx int) (string, error) {
	var st string
	err := s.Events.QueryRow(`SELECT state FROM outbound_parts WHERE plan_id = ? AND idx = ?`, planID, idx).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoPlan
	}
	return st, err
}

// BeginSend 把一段从待发（或等重试）转成发送中：尝试次数加一，第一次时记下 sent_at。
// ok=false 说明这一段现在不是待发也不是等重试（已经被中止了，或者被回声认领了），不能发，调用方去看现在是什么状态
func (s *Store) BeginSend(planID string, idx int, now int64) (attempts int, ok bool, err error) {
	err = s.Events.QueryRow(`UPDATE outbound_parts SET state = ?, attempts = attempts + 1, sent_at = COALESCE(sent_at, ?)
		WHERE plan_id = ? AND idx = ? AND state IN (?, ?) RETURNING attempts`,
		PartSending, now, planID, idx, PartPending, PartRetrying).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return attempts, err == nil, err
}

// PartEnd 是一次转移带的结果
type PartEnd struct {
	PlatformID string // 确认时的平台消息号
	Via        string // reply / echo
	Err        string // 失败、未知、中止的原因（不含原文、令牌）
}

// EndPart 把一段从 from 里的某个状态转成 to；当前状态不在 from 里就不动，返回 ok=false。
// 转成确认而且带着平台消息号的，同一个事务里把"这个平台消息号 → 这次回复的事件"记进 channel_msgs（回执后写映射，第十四节"发送映射"一行）：
// 之后协议端把它推回来，入库一站按键去重，不会再存成一条"不是她说的"
func (s *Store) EndPart(planID string, idx int, from []string, to string, e PartEnd, now int64) (ok bool, err error) {
	tx, err := s.Events.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	ok, err = endPart(tx, planID, idx, from, to, e)
	if err != nil || !ok {
		return false, err
	}
	if err := settle(tx, planID, now); err != nil {
		return false, err
	}
	return true, s.commitPlanChanged(tx, planID)
}

func inList(vs []string) (string, []any) {
	ph := make([]string, len(vs))
	args := make([]any, len(vs))
	for i, v := range vs {
		ph[i], args[i] = "?", v
	}
	return strings.Join(ph, ", "), args
}

func endPart(tx *sql.Tx, planID string, idx int, from []string, to string, e PartEnd) (bool, error) {
	ph, fromArgs := inList(from)
	args := append([]any{to, nullable(e.PlatformID), nullable(e.Via), nullable(e.Err), planID, idx}, fromArgs...)
	res, err := tx.Exec(`UPDATE outbound_parts SET state = ?, platform_id = ?, via = ?, err = ? WHERE plan_id = ? AND idx = ? AND state IN (`+ph+`)`, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if to == PartConfirmed && HasPlatformID(e.PlatformID) {
		// 计划没有事件（只有戳一戳）就没有可映射的，SELECT 不出行
		if _, err := tx.Exec(`INSERT OR IGNORE INTO channel_msgs (channel, conversation, platform_id, event_seq)
			SELECT ev.channel, pl.conversation, ?, pl.event_seq FROM outbound_plans pl JOIN events ev ON ev.seq = pl.event_seq WHERE pl.id = ?`,
			e.PlatformID, planID); err != nil {
			return false, err
		}
	}
	return true, nil
}

// AbortPlanParts 把一条计划里还没发的段（待发、等重试）都转成已中止，返回转了几段。
// 前面有段没发出去、切档时用：发送中的段不动，让它自己有结果
func (s *Store) AbortPlanParts(planID, reason string, now int64) (int, error) {
	tx, err := s.Events.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE outbound_parts SET state = ?, err = ? WHERE plan_id = ? AND state IN (?, ?)`,
		PartAborted, nullable(reason), planID, PartPending, PartRetrying)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if err := settle(tx, planID, now); err != nil {
		return 0, err
	}
	return int(n), s.commitPlanChanged(tx, planID)
}

// EchoClaim 是一个回声要认领的段的条件（10.4"回声认领"）：按账号、会话、段类型筛选，窗口从这一段第一次调 Send 起算
type EchoClaim struct {
	Channel      string // 渠道：查"这个平台消息号认识了没有"要用（channel_msgs 的键是渠道、会话、平台消息号），必填
	Account      string // 发送用的号，也就是回声里的发言人
	Conversation string
	PlatformID   string // 回声的平台消息号
	Kind         string // 候选段类型；空值选 message。
	Since        int64  // sent_at 不早于这个时刻。
	// Match 比较候选内容；转发调用方可额外拒绝含媒体的候选。
	Match func(partText string) bool
	// Score 给了就代替 Match：0 是对不上，越大越该认，同分取发得最早的。
	// 出口用它让 md5 强对上的、正有人等的段排在“等过、已放弃”的段前面（复核：重发段的回声被放弃的原段抢走）
	Score func(planID string, idx int, partText string) int
}

// ClaimEcho 找一段能认领这个回声的（发送中、等重试、结果未知，在窗口内，键对得上），把它转成确认（via=echo）。
// 候选里发得最早的先认（先到先认；给了 Score 就先比分），每一段只认一次。同一个事务里记下映射、重算计划状态。没有对得上的返回 found=false。
//
// 这个平台消息号已经认识了（channel_msgs 里有）就一律 found=false，不看候选：它要么是同一个回声又到了一次（同群双号各交一份、
// 重连重推、补录和实时重叠），要么是回包已经确认过的那一段的回声，都不是另一段同文字的段的回声。
// 认了就把没确认的段假报成确认，号也记错；那一段真正的回声来时又没有段可认，被存成 origin = human 的重复事件（清单⑤×⑬）
func (s *Store) ClaimEcho(c EchoClaim, now int64) (planID string, idx int, found bool, err error) {
	if c.Channel == "" {
		return "", 0, false, errors.New("回声认领没有渠道")
	}
	if !HasPlatformID(c.PlatformID) {
		return "", 0, false, nil // 没有号，映射写不了；认领的意义就是把这个号和我们的事件连起来
	}
	tx, err := s.Events.Begin()
	if err != nil {
		return "", 0, false, err
	}
	defer tx.Rollback()

	var known bool
	if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM channel_msgs WHERE channel = ? AND conversation = ? AND platform_id = ?)`,
		c.Channel, c.Conversation, c.PlatformID).Scan(&known); err != nil {
		return "", 0, false, err
	}
	if known {
		return "", 0, false, nil
	}

	kind := c.Kind
	if kind == "" {
		kind = "message"
	}
	ph, stArgs := inList(echoClaimable)
	args := append([]any{c.Account, c.Conversation, kind, c.Since}, stArgs...)
	rows, err := tx.Query(`SELECT p.plan_id, p.idx, p.text FROM outbound_parts p JOIN outbound_plans pl ON pl.id = p.plan_id
		WHERE pl.account = ? AND pl.conversation = ? AND pl.kind = 'send' AND p.kind = ? AND p.sent_at >= ? AND p.state IN (`+ph+`)
		ORDER BY p.sent_at, pl.created_at, p.plan_id, p.idx`, args...)
	if err != nil {
		return "", 0, false, err
	}
	type cand struct {
		plan string
		idx  int
	}
	var hit *cand
	best := 0
	for rows.Next() {
		var pid string
		var i int
		var text sql.NullString
		if err := rows.Scan(&pid, &i, &text); err != nil {
			rows.Close()
			return "", 0, false, err
		}
		score := 0
		switch {
		case c.Score != nil:
			score = c.Score(pid, i, text.String)
		case (kind == "forward" && c.Match == nil) || (c.Match != nil && c.Match(text.String)):
			score = 1
		}
		if score > best {
			hit, best = &cand{pid, i}, score
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", 0, false, err
	}
	rows.Close() // 这个事务只有一条连接：读完关掉，才能接着写
	if hit == nil {
		return "", 0, false, nil
	}
	ok, err := endPart(tx, hit.plan, hit.idx, echoClaimable, PartConfirmed, PartEnd{PlatformID: c.PlatformID, Via: "echo"})
	if err != nil || !ok {
		return "", 0, false, err
	}
	if err := settle(tx, hit.plan, now); err != nil {
		return "", 0, false, err
	}
	return hit.plan, hit.idx, true, s.commitChanged(tx, c.Conversation)
}

// Swept 是 Sweep 清理了多少
type Swept struct {
	Plans   int // 清理了几条还没结束的计划
	Unknown int // 发送中 → 结果未知，几段
	Aborted int // 待发、等重试 → 已中止，几段
}

// Sweep 清理所有还没结束的计划：发送中的段转成结果未知（可能已经在协议端发出去了，不重发），待发和等重试的转成已中止（几分钟前的话，不续发）。
// 启动时（上次没关干净）和关闭时（等回执的时间到了）用，规格 10.6。计划状态相应重算
func (s *Store) Sweep(unknownWhy, abortWhy string, now int64) (Swept, error) {
	return s.sweep("", unknownWhy, abortWhy, now)
}

// SweepPlan 同 Sweep，只清理这一条计划（一条计划的发送出了意外时用，别的会话在发的不能动）
func (s *Store) SweepPlan(planID, unknownWhy, abortWhy string, now int64) (Swept, error) {
	return s.sweep(planID, unknownWhy, abortWhy, now)
}

func (s *Store) sweep(only, unknownWhy, abortWhy string, now int64) (Swept, error) {
	tx, err := s.Events.Begin()
	if err != nil {
		return Swept{}, err
	}
	defer tx.Rollback()
	var out Swept
	rows, err := tx.Query(`SELECT id FROM outbound_plans WHERE state = ? AND (? = '' OR id = ?)`, PlanSending, only, only)
	if err != nil {
		return Swept{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Swept{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Swept{}, err
	}
	rows.Close()
	out.Plans = len(ids)
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE outbound_parts SET state = ?, err = ? WHERE plan_id = ? AND state = ?`, PartUnknown, nullable(unknownWhy), id, PartSending)
		if err != nil {
			return Swept{}, err
		}
		n, _ := res.RowsAffected()
		out.Unknown += int(n)
		res, err = tx.Exec(`UPDATE outbound_parts SET state = ?, err = ? WHERE plan_id = ? AND state IN (?, ?)`, PartAborted, nullable(abortWhy), id, PartPending, PartRetrying)
		if err != nil {
			return Swept{}, err
		}
		n, _ = res.RowsAffected()
		out.Aborted += int(n)
		if err := settle(tx, id, now); err != nil {
			return Swept{}, err
		}
	}
	return out, s.commitPlanChanged(tx, ids...)
}

// SummarizeParts 是 send 计划的汇总状态，由各段状态算出（10.4"计划的状态"）。还有段没到终态（待发、发送中、等重试）返回 PlanSending。
//   - 任何段结果未知 → unknown（不知道发出去没有，不是失败也不是完成）
//   - 全部确认 → done
//   - 一段也没确认：有段明确失败 → failed；否则（都没发就被中止）→ aborted
//   - 有确认的，也有失败或没发的 → partial
//
// failed、aborted 是规格没列的两种：规格只写了完成、部分、结果未知，第一段就失败或一段没发就被关掉时写"部分"会骗人
func SummarizeParts(states []string) string {
	var confirmed, failed, unknown, aborted int
	for _, s := range states {
		switch s {
		case PartConfirmed:
			confirmed++
		case PartFailed:
			failed++
		case PartUnknown:
			unknown++
		case PartAborted:
			aborted++
		default:
			return PlanSending
		}
	}
	switch {
	case len(states) == 0:
		return PlanSending
	case unknown > 0:
		return PlanUnknown
	case confirmed == len(states):
		return PlanDone
	case confirmed > 0:
		return PlanPartial
	case failed > 0:
		return PlanFailed
	default:
		return PlanAborted
	}
}

// settle 在事务里重算一条 send 计划的状态：还有段在途就不动（保持 sending）；都到了终态就写汇总，第一次写时记 done_at。
// 结果未知的段被回声认领成确认以后会再来一次，汇总跟着变（unknown → done 或 partial），这是 state 唯一会被改第二次的情形
func settle(tx *sql.Tx, planID string, now int64) error {
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM outbound_plans WHERE id = ?`, planID).Scan(&kind); err != nil {
		return err
	}
	if kind != PlanKindSend {
		return nil
	}
	rows, err := tx.Query(`SELECT state FROM outbound_parts WHERE plan_id = ?`, planID)
	if err != nil {
		return err
	}
	var states []string
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			rows.Close()
			return err
		}
		states = append(states, st)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	sum := SummarizeParts(states)
	if sum == PlanSending {
		return nil
	}
	_, err = tx.Exec(`UPDATE outbound_plans SET state = ?, done_at = COALESCE(done_at, ?) WHERE id = ?`, sum, now, planID)
	return err
}
