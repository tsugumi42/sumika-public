package proactive

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/config"
)

// 话题线的存取和并发（规格-主动联系 第四、五、九节）：状态机在 thread.go（纯函数），这里在一把锁里
// "读线 → Apply → 存线"，并管发送记录（先写后发）和被认领的主人消息。库是 trace.db（迁移 0018）。
// 不 import 回合内核：写聊天库、叫模型、发通知都由宿主（app）做。

// 发送记录的状态
const (
	SendPending = "pending" // 先写后发：发送前写下这一行
	SendSent    = "sent"
	SendFailed  = "failed"  // 没发出去：不算开口、不算冷
	SendUnknown = "unknown" // 结果不明（含重启时的 pending）：按发过算，不自动重发
	SendThought = "thought" // send: think 档，本想对谁说什么
)

// 认领记录的状态
const (
	ClaimPending = "pending" // 线还没表态
	ClaimReplied = "replied" // 线回了
	ClaimHanded  = "handed"  // 交回给普通路径了
	ClaimLost    = "lost"    // 进程重启时还在等：线不再接，主人那句由宿主起普通回合补回（不重放转述）
)

// ErrCap 这条线的发送条数已经用完；ErrOpenExists 这个角色已经有开着的线；ErrNoThread 没有这条线
var (
	ErrCap        = errors.New("proactive：这条线的发送条数用完了")
	ErrOpenExists = errors.New("proactive：这个角色已经有开着的线")
	ErrNoThread   = errors.New("proactive：没有这条线")
	ErrDupClaim   = errors.New("proactive：这条消息已经被认领过")
)

// Send 是 proactive_sends 的一行
type Send struct {
	ID           int64
	Thread       string
	PlanID       string // 线号加序号，幂等键
	Conversation string // 发到哪个会话（本地是 console:<角色>）
	EventSeq     int64
	EventID      string
	Label        string // 地点的叫法（"本地私聊"）
	Scene        string // 场合（词汇表）
	State        string
	Summary      string // 一句话摘要，只本机显示
	At           time.Time
}

// Claim 是 proactive_claims 的一行
type Claim struct {
	Conversation string
	Seq          int64
	Thread       string
	State        string
	At           time.Time
}

// Threads 管线的状态和记录。Config 每次取（配置热更新）；所有写都在 mu 里读-改-写
type Threads struct {
	db     *sql.DB
	config func() config.Proactive
	now    func() time.Time
	mu     sync.Mutex
}

// NewThreads 建管理器。now 为空用 time.Now
func NewThreads(db *sql.DB, cfg func() config.Proactive, now func() time.Time) *Threads {
	if now == nil {
		now = time.Now
	}
	return &Threads{db: db, config: cfg, now: now}
}

const threadCols = `id, character_id, conversation, state, opened_at, opened_reason, first_send_at, last_send_at, last_wake_at, awaiting_since,
 followups, sends, owner_replies, first_reply_at, owner_replied_at, chat_last_at, blocked_convs, closed_at, close_reason, conclusion`

type scanner interface{ Scan(dest ...any) error }

func scanThread(r scanner) (Thread, error) {
	var t Thread
	var state, reason, blocked string
	var opened, first, last, wake, aw, firstReply, replied, chat, closed int64
	if err := r.Scan(&t.ID, &t.Character, &t.Conversation, &state, &opened, &t.OpenedReason, &first, &last, &wake, &aw,
		&t.FollowUps, &t.Sends, &t.OwnerReplies, &firstReply, &replied, &chat, &blocked, &closed, &reason, &t.Conclusion); err != nil {
		return Thread{}, err
	}
	t.State, t.CloseReason = ThreadState(state), CloseReason(reason)
	t.OpenedAt, t.FirstSendAt, t.LastSendAt, t.LastWakeAt, t.AwaitingSince = fromMS(opened), fromMS(first), fromMS(last), fromMS(wake), fromMS(aw)
	t.FirstReplyAt, t.OwnerRepliedAt, t.ChatLastAt, t.ClosedAt = fromMS(firstReply), fromMS(replied), fromMS(chat), fromMS(closed)
	if blocked != "" {
		t.BlockedConvs = strings.Split(blocked, "\n")
	}
	return t, nil
}

func (m *Threads) load(id string) (Thread, error) {
	t, err := scanThread(m.db.QueryRow(`SELECT `+threadCols+` FROM proactive_threads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, ErrNoThread
	}
	return t, err
}

func (m *Threads) save(t Thread) error {
	_, err := m.db.Exec(`UPDATE proactive_threads SET state=?, opened_reason=?, first_send_at=?, last_send_at=?, last_wake_at=?, awaiting_since=?,
		followups=?, sends=?, owner_replies=?, first_reply_at=?, owner_replied_at=?, chat_last_at=?, blocked_convs=?, closed_at=?, close_reason=?, conclusion=? WHERE id=?`,
		string(t.State), t.OpenedReason, ms(t.FirstSendAt), ms(t.LastSendAt), ms(t.LastWakeAt), ms(t.AwaitingSince),
		t.FollowUps, t.Sends, t.OwnerReplies, ms(t.FirstReplyAt), ms(t.OwnerRepliedAt), ms(t.ChatLastAt), strings.Join(t.BlockedConvs, "\n"),
		ms(t.ClosedAt), string(t.CloseReason), t.Conclusion, t.ID)
	return err
}

// Get 按线号取
func (m *Threads) Get(id string) (Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(id)
}

// OpenOf 是这个角色开着的线（没有 ok 为假）
func (m *Threads) OpenOf(character string) (Thread, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openOf(character)
}

func (m *Threads) openOf(character string) (Thread, bool, error) {
	t, err := scanThread(m.db.QueryRow(`SELECT `+threadCols+` FROM proactive_threads WHERE character_id = ? AND state != 'closed' ORDER BY opened_at DESC LIMIT 1`, character))
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, false, nil
	}
	return t, err == nil, err
}

// Recent 是这个角色最近 n 条线，新的在前（界面"去过哪"、日志用）
func (m *Threads) Recent(character string, n int) ([]Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT `+threadCols+` FROM proactive_threads WHERE character_id = ? ORDER BY opened_at DESC, id DESC LIMIT ?`, character, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Create 开一条新线（状态想）。已经有开着的线（maxOpenThreads）就拒绝，id 撞了也拒绝
func (m *Threads) Create(t Thread) (Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := m.config().MaxOpenThreads
	if max < 1 {
		max = 1
	}
	var open int
	if err := m.db.QueryRow(`SELECT count(*) FROM proactive_threads WHERE character_id = ? AND state != 'closed'`, t.Character).Scan(&open); err != nil {
		return Thread{}, err
	}
	if open >= max {
		return Thread{}, ErrOpenExists
	}
	t.State = ThreadThinking
	if t.LastWakeAt.IsZero() {
		t.LastWakeAt = t.OpenedAt
	}
	_, err := m.db.Exec(`INSERT INTO proactive_threads (id, character_id, conversation, state, opened_at, last_wake_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.Character, t.Conversation, string(t.State), ms(t.OpenedAt), ms(t.LastWakeAt))
	if err != nil {
		return Thread{}, err
	}
	return m.load(t.ID)
}

// Apply 把一件事落到线上：读 → 状态机 → 存。ErrNoop、ErrImpossible、ErrClosed 时线不变、不存，原样返回线和错误
func (m *Threads) Apply(id string, ev Event) (Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apply(id, ev)
}

func (m *Threads) apply(id string, ev Event) (Thread, error) {
	t, err := m.load(id)
	if err != nil {
		return Thread{}, err
	}
	if ev.At.IsZero() {
		ev.At = m.now()
	}
	nt, err := Apply(m.config(), t, ev)
	if err != nil {
		return t, err
	}
	return nt, m.save(nt)
}

// SetOpenedReason 记下第一次开口的理由（限 60 字）；已经有了不覆盖
func (m *Threads) SetOpenedReason(id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.load(id)
	if err != nil {
		return err
	}
	if t.OpenedReason != "" {
		return nil
	}
	t.OpenedReason = ClipNote(reason, 60)
	return m.save(t)
}

// Block 本线内不再认领这个会话（hand_back）
func (m *Threads) Block(id, conv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.load(id)
	if err != nil {
		return err
	}
	if t.Blocked(conv) {
		return nil
	}
	t.BlockedConvs = append(t.BlockedConvs, conv)
	return m.save(t)
}

// BeginSend 是先写后发的前半：线没关、发送条数没用完（算上还在途的 pending），才写一行 pending 并返回。
// 发送条数用完返回 ErrCap；线关了返回 ErrClosed
func (m *Threads) BeginSend(id, conv, label, scene, summary string) (Send, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.load(id)
	if err != nil {
		return Send{}, err
	}
	if !t.Open() {
		return Send{}, ErrClosed
	}
	var inflight, total int
	if err := m.db.QueryRow(`SELECT count(*) FROM proactive_sends WHERE thread = ?`, id).Scan(&total); err != nil {
		return Send{}, err
	}
	if err := m.db.QueryRow(`SELECT count(*) FROM proactive_sends WHERE thread = ? AND sent_state = ?`, id, SendPending).Scan(&inflight); err != nil {
		return Send{}, err
	}
	if t.Sends+inflight >= m.config().MaxSendsPerThread {
		return Send{}, ErrCap
	}
	now := m.now()
	s := Send{Thread: id, PlanID: fmt.Sprintf("%s#%d", id, total+1), Conversation: conv, Label: label, Scene: scene, State: SendPending, Summary: ClipNote(summary, 40), At: now}
	res, err := m.db.Exec(`INSERT INTO proactive_sends (thread, plan_id, conversation, target_label, scene, sent_state, summary, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Thread, s.PlanID, s.Conversation, s.Label, s.Scene, s.State, s.Summary, ms(now))
	if err != nil {
		return Send{}, err
	}
	s.ID, _ = res.LastInsertId()
	return s, nil
}

// FinishSend 是后半：按结果改发送记录。sent、unknown 把线推进到"等回复"（Apply EvSent）；failed、thought 只改记录，线不动
func (m *Threads) FinishSend(sendID int64, state string, eventSeq int64, eventID string) (Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var thread string
	if err := m.db.QueryRow(`SELECT thread FROM proactive_sends WHERE id = ?`, sendID).Scan(&thread); err != nil {
		return Thread{}, err
	}
	if _, err := m.db.Exec(`UPDATE proactive_sends SET sent_state = ?, event_seq = ?, event_id = ? WHERE id = ?`, state, eventSeq, eventID, sendID); err != nil {
		return Thread{}, err
	}
	if state == SendSent || state == SendUnknown {
		return m.apply(thread, Event{Kind: EvSent, At: m.now()})
	}
	return m.load(thread)
}

func scanSend(r scanner) (Send, error) {
	var s Send
	var at int64
	err := r.Scan(&s.ID, &s.Thread, &s.PlanID, &s.Conversation, &s.EventSeq, &s.EventID, &s.Label, &s.Scene, &s.State, &s.Summary, &at)
	s.At = fromMS(at)
	return s, err
}

const sendCols = `id, thread, plan_id, conversation, event_seq, event_id, target_label, scene, sent_state, summary, at`

// Sends 是这条线的全部发送记录，旧的在前
func (m *Threads) Sends(thread string) ([]Send, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT `+sendCols+` FROM proactive_sends WHERE thread = ? ORDER BY id`, thread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Send
	for rows.Next() {
		s, err := scanSend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecentSentTo 是这个角色最近 n 条真发给 conv 的话（sent、unknown；failed、thought、pending 没说出口，不算），新的在前。
// 按线的角色和发送记录自己的会话两道条件取，给醒来行“最近说过什么”用：conv 传主人的本地私聊，别的会话一条也进不来
func (m *Threads) RecentSentTo(character, conv string, n int) ([]Send, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT s.id, s.thread, s.plan_id, s.conversation, s.event_seq, s.event_id, s.target_label, s.scene, s.sent_state, s.summary, s.at
		FROM proactive_sends s JOIN proactive_threads t ON t.id = s.thread
		WHERE t.character_id = ? AND s.conversation = ? AND s.sent_state IN ('sent','unknown') ORDER BY s.at DESC, s.id DESC LIMIT ?`, character, conv, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Send
	for rows.Next() {
		s, err := scanSend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecentConcluded 是这个角色最近 n 条已关、发过话、写了结论的线，新的在前（醒来行“上次说到哪”用）
func (m *Threads) RecentConcluded(character string, n int) ([]Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT `+threadCols+` FROM proactive_threads WHERE character_id = ? AND state = 'closed' AND sends > 0 AND conclusion != ''
		ORDER BY closed_at DESC, id DESC LIMIT ?`, character, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastSendTo 是这条线最后一次发到某会话的、真发出去的（sent 或 unknown）那条；没有 ok 为假
func (m *Threads) LastSendTo(thread, conv string) (Send, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := scanSend(m.db.QueryRow(`SELECT `+sendCols+` FROM proactive_sends WHERE thread = ? AND conversation = ? AND sent_state IN ('sent','unknown') ORDER BY id DESC LIMIT 1`, thread, conv))
	if errors.Is(err, sql.ErrNoRows) {
		return Send{}, false, nil
	}
	return s, err == nil, err
}

// SendByEventID 按写进目标会话的那条事件 id 找发送记录（主人引用线发的消息时认它）
func (m *Threads) SendByEventID(conv, eventID string) (Send, bool, error) {
	if eventID == "" {
		return Send{}, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := scanSend(m.db.QueryRow(`SELECT `+sendCols+` FROM proactive_sends WHERE conversation = ? AND event_id = ? AND sent_state IN ('sent','unknown') LIMIT 1`, conv, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return Send{}, false, nil
	}
	return s, err == nil, err
}

// AddClaim 登记一条被认领的主人消息。同一条（会话 + seq）只能登记一次，重复返回 ErrDupClaim
func (m *Threads) AddClaim(conv string, seq int64, thread string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.db.Exec(`INSERT INTO proactive_claims (conversation, seq, thread, state, at) VALUES (?, ?, ?, ?, ?)`, conv, seq, thread, ClaimPending, ms(at))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrDupClaim
	}
	return err
}

// SetClaim 改认领记录的状态；只在原状态是 pending 时才改（replied、handed 只发生一次），返回改没改
func (m *Threads) SetClaim(conv string, seq int64, state string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.db.Exec(`UPDATE proactive_claims SET state = ? WHERE conversation = ? AND seq = ? AND state = ?`, state, conv, seq, ClaimPending)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClaimsOf 是这条线的认领记录，按时间旧的在前；state 非空只取这个状态
func (m *Threads) ClaimsOf(thread, state string) ([]Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := `SELECT conversation, seq, thread, state, at FROM proactive_claims WHERE thread = ?`
	args := []any{thread}
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	rows, err := m.db.Query(q+` ORDER BY at, seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var c Claim
		var at int64
		if err := rows.Scan(&c.Conversation, &c.Seq, &c.Thread, &c.State, &at); err != nil {
			return nil, err
		}
		c.At = fromMS(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SentToday 是这个角色今天（dayStart 起）开了线并真发过话的条数：按线第一次发话的时间算，结果不明的也算
func (m *Threads) SentToday(character string, dayStart time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	err := m.db.QueryRow(`SELECT count(*) FROM proactive_threads WHERE character_id = ? AND sends > 0 AND first_send_at >= ?`, character, ms(dayStart)).Scan(&n)
	return n, err
}

// LastContacted 是这个角色最近一条真发过话的线（取上次接触的时间、上一条线的热度）；没有 ok 为假
func (m *Threads) LastContacted(character string) (Thread, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := scanThread(m.db.QueryRow(`SELECT `+threadCols+` FROM proactive_threads WHERE character_id = ? AND sends > 0 ORDER BY last_send_at DESC LIMIT 1`, character))
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, false, nil
	}
	return t, err == nil, err
}

// OpenThreads 是所有角色开着的线
func (m *Threads) OpenThreads() ([]Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openThreadsLocked()
}

// RecoverReport 是启动恢复做了什么
type RecoverReport struct {
	Unknown int      // pending 的发送行记成 unknown 的条数
	Lost    int      // 认领中途丢的条数
	Threads []string // 状态有变的线号

	// LostClaims 是这次改成 lost 的认领：主人那句话已经在聊天库里、却没人回，由宿主起一轮普通回合补上（不叫线，不重放转述）
	LostClaims []Claim
	// RestartClosed 是这次因为"想着、没发过话"被关成 error(restart) 的线：宿主要补做收线（结论、导演系数和下次醒）
	RestartClosed []Thread
}

// Recover 重启后恢复（规格第四节"存储和重启恢复"）：pending 的发送行一律记 unknown，线按"发过话"算，不自动重发；
// 想着的线：发过话的回到等回复，没发过的关成 error(restart)；等回复、聊着照旧，计时用库里的时间戳重算；
// 还在等的认领记 lost 并列进 LostClaims（线不再接，主人那句由宿主起普通回合补回）；想着被关掉的线列进 RestartClosed（宿主补做收线）。
// 同一进程里再调一次没有副作用（该改的都已改过）
func (m *Threads) Recover() (RecoverReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rep RecoverReport
	changed := map[string]bool{}
	rows, err := m.db.Query(`SELECT id, thread, at FROM proactive_sends WHERE sent_state = ? ORDER BY id`, SendPending)
	if err != nil {
		return rep, err
	}
	type pend struct {
		id     int64
		thread string
		at     time.Time
	}
	var pends []pend
	for rows.Next() {
		var p pend
		var at int64
		if err := rows.Scan(&p.id, &p.thread, &at); err != nil {
			rows.Close()
			return rep, err
		}
		p.at = fromMS(at)
		pends = append(pends, p)
	}
	rows.Close()
	for _, p := range pends {
		if _, err := m.db.Exec(`UPDATE proactive_sends SET sent_state = ? WHERE id = ?`, SendUnknown, p.id); err != nil {
			return rep, err
		}
		rep.Unknown++
		t, err := m.load(p.thread)
		if err != nil {
			continue
		}
		if t.Open() {
			if _, err := m.apply(p.thread, Event{Kind: EvSent, At: p.at}); err == nil {
				changed[p.thread] = true
			}
		} else { // 线已经关了（比如先被关再发）：只补发送数，免得"今天找了几次"漏算
			t.Sends++
			if t.FirstSendAt.IsZero() {
				t.FirstSendAt = p.at
			}
			if t.LastSendAt.Before(p.at) {
				t.LastSendAt = p.at
			}
			if err := m.save(t); err != nil {
				return rep, err
			}
			changed[p.thread] = true
		}
	}
	// 先取再改：同一进程里再调一次时已经没有 pending，LostClaims 为空，不会重复补回
	crows, err := m.db.Query(`SELECT conversation, seq, thread, state, at FROM proactive_claims WHERE state = ? ORDER BY at, seq`, ClaimPending)
	if err != nil {
		return rep, err
	}
	for crows.Next() {
		var c Claim
		var at int64
		if err := crows.Scan(&c.Conversation, &c.Seq, &c.Thread, &c.State, &at); err != nil {
			crows.Close()
			return rep, err
		}
		c.At = fromMS(at)
		rep.LostClaims = append(rep.LostClaims, c)
	}
	crows.Close()
	res, err := m.db.Exec(`UPDATE proactive_claims SET state = ? WHERE state = ?`, ClaimLost, ClaimPending)
	if err != nil {
		return rep, err
	}
	n, _ := res.RowsAffected()
	rep.Lost = int(n)
	open, err := m.openThreadsLocked()
	if err != nil {
		return rep, err
	}
	for _, t := range open {
		if t.State != ThreadThinking {
			continue
		}
		if nt, err := m.apply(t.ID, Event{Kind: EvRestart, At: m.now()}); err == nil {
			changed[t.ID] = true
			if nt.State == ThreadClosed {
				rep.RestartClosed = append(rep.RestartClosed, nt)
			}
		}
	}
	for id := range changed {
		rep.Threads = append(rep.Threads, id)
	}
	return rep, nil
}

func (m *Threads) openThreadsLocked() ([]Thread, error) {
	rows, err := m.db.Query(`SELECT ` + threadCols + ` FROM proactive_threads WHERE state != 'closed' ORDER BY opened_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
