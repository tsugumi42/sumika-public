package store

// 回合的结局和"上一个进程没收尾的"（规格-Go后台 8.2 表一 A9"进程被杀以后"，trace 库 0002 的 turns 表）。
// 只在 Go 自己的库上用（OpenFresh 打开的）。做法照 OpenCode：开始前先记、恢复时没结束的一律判成被打断、从不重跑；
// 收拾的时机照出站的 Sweep（建的时候收拾一次，一个库同时只有一个 Hub）

import (
	"context"
	"database/sql"
	"encoding/json"
)

// 回合的结局（和 turn.Outcome* 一样的字，多一个 interrupted）
const (
	TurnInterrupted = "interrupted" // 进程在回合定了之前退出了，下一个进程建 Hub 时补的
)

// steps.details_json 里 interrupted 的值：工具被切断时记的原因（界面上分得出“被停下”和“程序退出时中断”，模型看的结果文字不变）。
// 没有这个标记的旧行：SweepInterrupted 旧版本收拾的认 status=error 加 unknownPreview（StepInterruptedBy）。
const (
	InterruptedByStop = "stop" // 点了停止、或回合被取消：这一步没跑完
	InterruptedByExit = "exit" // 程序退出（正常关机或被杀）时它还在跑
	exitDetails       = `{"interrupted":"exit"}`
)

// 收拾时给步骤写的预览（界面上看得到）
const (
	unknownPreview   = "后台在这个工具跑完之前退出了，不知道结果：可能没执行，也可能已经执行了一部分。"
	abandonedPreview = "后台在等所有者批准时退出了，这个操作没有执行。"
)

// TurnStart 记一个回合开始（先记再跑）
func (s *Store) TurnStart(id, characterID, conversation, channel string, at int64) error {
	_, err := s.Trace.Exec(`INSERT INTO turns (id, character_id, conversation, channel, started_at) VALUES (?, ?, ?, ?, ?)`,
		id, characterID, conversation, nullable(channel), at)
	return err
}

// HasTurn 供运行缓存回收后的准入去重；只读取现有 turns 主键，不恢复或重跑回合。
func (s *Store) HasTurn(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.Trace.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turns WHERE id=?)`, id).Scan(&exists)
	return exists, err
}

// TurnRan 记这个回合拼过参考信息了（被打断的工具提醒就在这时交给她）
func (s *Store) TurnRan(id string) error {
	_, err := s.Trace.Exec(`UPDATE turns SET ran = 1 WHERE id = ?`, id)
	return err
}

// TurnEnd 记一个回合的结局；已经有结局的不改（一个回合只有一个结局：先写的算）
func (s *Store) TurnEnd(id, outcome string, at int64) error {
	owner := s.outcomeStore()
	owner.outcomeMu.Lock()
	defer owner.outcomeMu.Unlock()
	if owner.outcomesSealed {
		return nil
	}
	_, err := s.Trace.Exec(`UPDATE turns SET outcome = ?, ended_at = ? WHERE id = ? AND ended_at IS NULL`, outcome, at, id)
	return err
}

// TurnsSwept 是一次收拾改了多少
type TurnsSwept struct {
	Turns   int // 没有结局的回合，记成 interrupted
	Unknown int // 停在执行中的工具，记成出错、不知道结果
	Waiting int // 停在等审批的，记成取消、没有执行
}

// SweepInterrupted 收拾上一个进程没收尾的（建 Hub 时调；调的时候这个库上不能有别的 Hub 在跑回合）：
// 没有结局的回合记成 interrupted，那时停在执行中的工具名记进它的 unknown_tools；
// 停在执行中的工具记成出错（结果不知道），停在等审批的记成取消（没有执行，approval_json 补上 decision）。
// 一个事务里做完；再调一次什么都不改
func (s *Store) SweepInterrupted(at int64) (TurnsSwept, error) {
	var sw TurnsSwept
	tx, err := s.Trace.Begin()
	if err != nil {
		return sw, err
	}
	defer tx.Rollback()
	turnSkip, stepSkip, turnArgs, stepArgs := s.foreignClauses() // 别的进程管的渠道（local 交给 Bun 时的 console）不碰
	open, err := openTurns(tx, turnSkip, turnArgs)
	if err != nil {
		return sw, err
	}
	for _, id := range open {
		names, err := runningTools(tx, id)
		if err != nil {
			return sw, err
		}
		var unknown any
		if len(names) > 0 {
			b, _ := json.Marshal(names)
			unknown = string(b)
		}
		if _, err := tx.Exec(`UPDATE turns SET outcome = ?, ended_at = ?, unknown_tools = ? WHERE id = ?`, TurnInterrupted, at, unknown, id); err != nil {
			return sw, err
		}
	}
	sw.Turns = len(open)
	r, err := tx.Exec(`UPDATE steps SET status = 'error', is_error = 1, ended_at = ?, preview = ?, details_json = ? WHERE status = 'running'`+stepSkip, append([]any{at, unknownPreview, exitDetails}, stepArgs...)...)
	if err != nil {
		return sw, err
	}
	n, _ := r.RowsAffected()
	sw.Unknown = int(n)
	r, err = tx.Exec(`UPDATE steps SET status = 'cancelled', is_error = 1, ended_at = ?, preview = ?,
		approval_json = json_set(COALESCE(approval_json, '{}'), '$.decision', 'cancelled') WHERE status = 'waiting'`+stepSkip, append([]any{at, abandonedPreview}, stepArgs...)...)
	if err != nil {
		return sw, err
	}
	n, _ = r.RowsAffected()
	sw.Waiting = int(n)
	if _, err := tx.Exec(interruptGateDecisions); err != nil {
		return sw, err
	}
	return sw, tx.Commit()
}

func openTurns(tx *sql.Tx, skip string, args []any) ([]string, error) {
	rs, err := tx.Query(`SELECT id FROM turns WHERE ended_at IS NULL`+skip+` ORDER BY rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rs.Err()
}

func runningTools(tx *sql.Tx, turnID string) ([]string, error) {
	rs, err := tx.Query(`SELECT COALESCE(name, '') FROM steps WHERE turn_id = ? AND kind = 'tool' AND status = 'running' ORDER BY started_at, rowid`, turnID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var n string
		if err := rs.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rs.Err()
}

// InterruptedPreview 是 SweepInterrupted 给“跑到一半进程没了”的工具写的预览；界面据此认出旧版本收拾的行。
func InterruptedPreview() string { return unknownPreview }

// SweptTurn 是一次收拾记成被打断、且有工具步骤没挂上任何回复的回合（界面要给这些步骤一个落脚的行）。
type SweptTurn struct{ ID, CharacterID, Conversation string }

// SweptWithOrphans 查 at 这一刻 SweepInterrupted 记成被打断的回合里，哪些有没挂上回复的工具步骤。
// 只看这一次收拾（ended_at = at），以前的被打断回合不动：不替旧库补行。
func (s *Store) SweptWithOrphans(at int64) ([]SweptTurn, error) {
	rs, err := s.Trace.Query(`SELECT t.id, COALESCE(t.character_id, ''), COALESCE(t.conversation, '') FROM turns t WHERE t.outcome = ? AND t.ended_at = ?
		AND EXISTS (SELECT 1 FROM steps x WHERE x.turn_id = t.id AND x.kind = 'tool' AND x.event_seq IS NULL) ORDER BY t.rowid`, TurnInterrupted, at)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []SweptTurn
	for rs.Next() {
		var t SweptTurn
		if err := rs.Scan(&t.ID, &t.CharacterID, &t.Conversation); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rs.Err()
}

// MarkExitInterrupted 程序正常退出（封口、排空）时这一轮被停下：被切断的工具记成“程序退出时中断”，
// 名字记进回合的 unknown_tools，下一个进程里的第一轮会提醒她（InterruptedTools 和被杀的同一条路）。返回被切断的工具数。
// 只改 InterruptedByStop 标记的步骤，同一轮里正常出错的工具不动；重复调用什么都不改。
func (s *Store) MarkExitInterrupted(turnID string) (int, error) {
	tx, err := s.Trace.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rs, err := tx.Query(`SELECT COALESCE(name, '') FROM steps WHERE turn_id = ? AND kind = 'tool' AND json_valid(details_json) AND json_extract(details_json, '$.interrupted') = ?
		ORDER BY started_at, rowid`, turnID, InterruptedByStop)
	if err != nil {
		return 0, err
	}
	var names []string
	for rs.Next() {
		var n string
		if err := rs.Scan(&n); err != nil {
			rs.Close()
			return 0, err
		}
		names = append(names, n)
	}
	err = rs.Err()
	rs.Close()
	if err != nil || len(names) == 0 {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE steps SET details_json = ? WHERE turn_id = ? AND kind = 'tool' AND json_valid(details_json) AND json_extract(details_json, '$.interrupted') = ?`, exitDetails, turnID, InterruptedByStop); err != nil {
		return 0, err
	}
	b, _ := json.Marshal(names)
	if _, err := tx.Exec(`UPDATE turns SET unknown_tools = ? WHERE id = ?`, string(b), turnID); err != nil {
		return 0, err
	}
	return len(names), tx.Commit()
}

// lookBack 是往回找还没提醒到的被打断回合时，最多看几轮（正常时第一轮就停）
const lookBack = 50

// InterruptedTools：这个会话里还没提醒到她的、被进程退出打断的回合留下的不知道结果的工具名（按先后、同名只留一个）；没有返回空。
// 从最近一轮（不算 except 这一个）往回找：被打断的收下它的工具、接着往前（被打断的那一轮就算拼过参考信息，实例也跟着进程没了）；
// 没拼过参考信息的（一开始就出错、回复前压缩时被停下）提醒没交到她手上，接着往前；遇到拼过参考信息、没被打断的那一轮就停：
// 提醒那时已经给过了，或者那时还没有要提醒的
func (s *Store) InterruptedTools(characterID, conversation, except string) ([]string, error) {
	rs, err := s.Trace.Query(`SELECT COALESCE(outcome, ''), unknown_tools, ran FROM turns WHERE character_id = ? AND conversation = ? AND id != ?
		ORDER BY started_at DESC, rowid DESC LIMIT ?`, characterID, conversation, except, lookBack)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var groups [][]string // 新的在前
	for rs.Next() {
		var outcome string
		var unknown sql.NullString
		var ran bool
		if err := rs.Scan(&outcome, &unknown, &ran); err != nil {
			return nil, err
		}
		// 被杀的回合是 interrupted；正常退出时被停下、有工具被切断的回合也记了 unknown_tools（MarkExitInterrupted），同样算
		if outcome == TurnInterrupted || unknown.Valid {
			if unknown.Valid {
				var names []string
				if err := json.Unmarshal([]byte(unknown.String), &names); err == nil { // 坏行不拦着这一轮：跳过它，别的照常提醒
					groups = append(groups, names)
				}
			}
			continue
		}
		if ran {
			break
		}
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for i := len(groups) - 1; i >= 0; i-- {
		for _, n := range groups[i] {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out, nil
}
