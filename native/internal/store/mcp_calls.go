package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// MCP 调用留痕（表 kernel_mcp_calls）。一行是一次已经交给传输层的 tools/call，状态：
//
//	dispatched   已派发，结局还没回来
//	settled      结局已知（对方回了结果）
//	unknown      发出去了但不知道对方有没有执行完（超时、被停止、断线）：这个服务被隔离，不再派发新调用
//	terminated   本机进程树已确认退出（旧调用不可能还在跑）：不挡新调用
//	risk_accepted 主人明确接受“结果未知”的风险后解除隔离：记录保留，不改成成功
//
// 只有 dispatched、unknown 挡新调用。reason 列存一段 JSON（MCPNote）：这一行的来历（传输种类、哪个进程、什么时候、为什么到了现在的状态）
// 和解除记录（谁、什么时候、原因）；表结构不变。读到不是 JSON 的旧内容当作主人当时写的解除原因。
const (
	MCPStateDispatched   = "dispatched"
	MCPStateSettled      = "settled"
	MCPStateUnknown      = "unknown"
	MCPStateTerminated   = "terminated"
	MCPStateRiskAccepted = "risk_accepted"
	MCPTransportHTTP     = "http"
	MCPTransportStdio    = "stdio"
	// MCPTransportStdioRemote：进程在本机、副作用在别处（转给远端的桥、docker、ssh 等，见 mcpclient/bridge.go）。
	// 启动收尾按远端对待，不自动当作已终止
	MCPTransportStdioRemote = "stdio-remote"
)

// 结局未知的原因。只放固定的词，不放服务返回的文字
const (
	MCPCauseTimeout   = "timeout"   // 调用时限到了
	MCPCauseCancelled = "cancelled" // 主人或模型停止了这次调用、或上下文取消
	MCPCauseError     = "error"     // 传输层出错、对方回了错误、写库失败
	MCPCauseHostExit  = "host_exit" // 程序退出或被杀时这次调用还没结局
)

// MCPNote 是 reason 列里的 JSON
type MCPNote struct {
	Transport    string `json:"transport,omitempty"`    // stdio | http：派发那一刻的传输，不按现在的配置推断
	Process      string `json:"process,omitempty"`      // 派发它的进程的随机号；启动收尾只动别的进程留下的行
	DispatchedAt int64  `json:"dispatchedAt,omitempty"` // 毫秒时间戳
	EndedAt      int64  `json:"endedAt,omitempty"`      // 离开 dispatched 的时间
	Cause        string `json:"cause,omitempty"`
	ReleasedAt   int64  `json:"releasedAt,omitempty"`
	ReleasedBy   string `json:"releasedBy,omitempty"`
	ReleaseWhy   string `json:"releaseReason,omitempty"`
}

// MCPCall 是列出隔离时的一行
type MCPCall struct {
	ID, Server, State string
	Note              MCPNote
}

// processID 每个进程一个随机号，写进它派发的每一行；同一个进程里后台反复开关也是同一个号
var processID = func() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}()

// ProcessID 是这个进程的随机号（测试里用来造“上一个进程留下的行”）
func ProcessID() string { return processID }

func (s EcosystemStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func parseNote(raw string) MCPNote {
	var n MCPNote
	if raw == "" {
		return n
	}
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return MCPNote{ReleaseWhy: raw} // 旧版本直接写的解除原因
	}
	return n
}

func (n MCPNote) encode() string {
	b, _ := json.Marshal(n)
	return string(b)
}

// mcpBlocking 是挡新调用的状态
const mcpBlocking = `('dispatched','unknown')`

func (s EcosystemStore) MCPBlocked(ctx context.Context, server string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM kernel_mcp_calls WHERE server_id=? AND state IN `+mcpBlocking, server).Scan(&n)
	return n > 0, err
}

// MCPDispatch 在向传输层提交前记下派发；服务已有挡着的行就拒绝。transport 是 stdio 或 http。
func (s EcosystemStore) MCPDispatch(ctx context.Context, id, server, binding, transport string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM kernel_mcp_calls WHERE server_id=? AND state IN `+mcpBlocking, server).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("MCP 服务因未知调用隔离")
	}
	note := MCPNote{Transport: transport, Process: processID, DispatchedAt: s.now().UnixMilli()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kernel_mcp_calls(id,server_id,state,binding,reason) VALUES(?,?,'dispatched',?,?)`, id, server, binding, note.encode()); err != nil {
		return err
	}
	return tx.Commit()
}

// changeState 在一个事务里把 id 这行从 from 状态里的任一个改成 to，并让 edit 改备注
func (s EcosystemStore) changeState(ctx context.Context, id, to string, from []string, edit func(*MCPNote)) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, raw string
	if err = tx.QueryRowContext(ctx, `SELECT state,reason FROM kernel_mcp_calls WHERE id=?`, id).Scan(&state, &raw); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	ok := false
	for _, f := range from {
		ok = ok || f == state
	}
	if !ok {
		return nil
	}
	note := parseNote(raw)
	if edit != nil {
		edit(&note)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kernel_mcp_calls SET state=?,reason=? WHERE id=?`, to, note.encode(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// MCPEnd 记下结局。known 为真是对方回了结果；否则是“结果未知”，cause 写原因（MCPCause*）
func (s EcosystemStore) MCPEnd(ctx context.Context, id string, known bool, cause string) error {
	to := MCPStateUnknown
	if known {
		to = MCPStateSettled
	}
	at := s.now().UnixMilli()
	return s.changeState(ctx, id, to, []string{MCPStateDispatched}, func(n *MCPNote) {
		n.EndedAt = at
		if !known {
			n.Cause = cause
		}
	})
}

// MCPTerminated 记下本机进程树已确认退出：这次调用不可能还在跑，不再挡这个服务
func (s EcosystemStore) MCPTerminated(ctx context.Context, id string) error {
	at := s.now().UnixMilli()
	return s.changeState(ctx, id, MCPStateTerminated, []string{MCPStateDispatched, MCPStateUnknown}, func(n *MCPNote) {
		if n.EndedAt == 0 {
			n.EndedAt = at
		}
	})
}

// MCPQuarantined 列出现在挡着新调用的行（dispatched、unknown），旧的在前
func (s EcosystemStore) MCPQuarantined(ctx context.Context) ([]MCPCall, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,server_id,state,reason FROM kernel_mcp_calls WHERE state IN `+mcpBlocking+` ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPCall
	for rows.Next() {
		var c MCPCall
		var raw string
		if err = rows.Scan(&c.ID, &c.Server, &c.State, &raw); err != nil {
			return nil, err
		}
		c.Note = parseNote(raw)
		out = append(out, c)
	}
	return out, rows.Err()
}

// MaxMCPReleaseReason 是解除原因的字数上限
const MaxMCPReleaseReason = 500

// CleanReleaseReason 去掉首尾空白和控制字符；空的或超长的返回错误。解除一定要有一句人写的原因
func CleanReleaseReason(reason string) (string, error) {
	reason = ctlproto.CleanReason(reason)
	if reason == "" {
		return "", fmt.Errorf("缺少解除隔离的原因")
	}
	if len([]rune(reason)) > MaxMCPReleaseReason {
		return "", fmt.Errorf("解除原因最多 %d 个字", MaxMCPReleaseReason)
	}
	return reason, nil
}

// MCPRelease 是主人接受“旧调用结果未知”的风险后解除某个服务的隔离：把它挡着的行改成 risk_accepted，
// 每一行的备注里记谁、什么时候、什么原因，行本身不删、不改成成功。返回解除了几行。
// 只能由认证的本地管理入口调用，不能从模型工具暴露；调用方负责确认这个服务现在没有正在进行的调用。
func (s EcosystemStore) MCPRelease(ctx context.Context, server, actor, reason string) (int, error) {
	reason, err := CleanReleaseReason(reason)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(actor) == "" {
		return 0, fmt.Errorf("缺少解除人")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,reason FROM kernel_mcp_calls WHERE server_id=? AND state IN `+mcpBlocking, server)
	if err != nil {
		return 0, err
	}
	type row struct{ id, raw string }
	var found []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, r)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	at := s.now().UnixMilli()
	for _, r := range found {
		note := parseNote(r.raw)
		note.ReleasedAt, note.ReleasedBy, note.ReleaseWhy = at, actor, reason
		if _, err = tx.ExecContext(ctx, `UPDATE kernel_mcp_calls SET state='risk_accepted',reason=? WHERE id=?`, note.encode(), r.id); err != nil {
			return 0, err
		}
	}
	return len(found), tx.Commit()
}

// MCPRecovery 是启动收尾的结果
type MCPRecovery struct{ Terminated, Unknown int }

// MCPRecoverAfterRestart 在启动时给上一个进程留下的、永远等不到结果的行收尾。只动别的进程派发的行，本进程自己的不碰。
//
//   - 只动 dispatched。别的状态（unknown、terminated、risk_accepted）原样不动：unknown 的行带着当时的原因（超时、被停止……），
//     也可能是“已终止没写进库”“旧进程没能确认退出”才保持隔离的，重启不能让它们悄悄恢复；
//   - 本机进程（传输种类恰好是 stdio，副作用就在进程树里）派发的 dispatched 行，且这个平台上宿主退出会带走子进程树（childrenDieWithHost）：
//     记成 terminated（原因 host_exit），不再挡；
//   - 其余的 dispatched（远端 HTTP、转给别处的本机进程 stdio-remote、没记传输的旧行、reason 不是合法 JSON 的行、传输种类不认识的行、
//     平台不保证子进程退出）：改成 unknown（程序退出时没有结果，如实记），继续挡着，等主人用解除入口带原因接受风险。
//
// 状态从来不改成 settled。
func (s EcosystemStore) MCPRecoverAfterRestart(ctx context.Context, childrenDieWithHost bool) (MCPRecovery, error) {
	var out MCPRecovery
	calls, err := s.MCPQuarantined(ctx)
	if err != nil {
		return out, err
	}
	at := s.now().UnixMilli()
	for _, c := range calls {
		if c.Note.Process == processID {
			continue
		}
		switch {
		case c.State == MCPStateDispatched && c.Note.Transport == MCPTransportStdio && childrenDieWithHost:
			err = s.changeState(ctx, c.ID, MCPStateTerminated, []string{MCPStateDispatched, MCPStateUnknown}, func(n *MCPNote) {
				n.EndedAt, n.Cause = at, MCPCauseHostExit
			})
			out.Terminated++
		case c.State == MCPStateDispatched:
			err = s.changeState(ctx, c.ID, MCPStateUnknown, []string{MCPStateDispatched}, func(n *MCPNote) {
				n.EndedAt, n.Cause = at, MCPCauseHostExit
			})
			out.Unknown++
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
