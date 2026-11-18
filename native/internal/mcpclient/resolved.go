package mcpclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
)

type ResolvedCall struct {
	client     *Client
	server     *server
	access     ecosystem.Access
	entry      Entry
	args       json.RawMessage
	generation uint64
	id         string
	dispatched bool
	approved   bool
}

func (c *Client) Resolve(ctx context.Context, a ecosystem.Access, id, digest string, args json.RawMessage) (*ResolvedCall, error) {
	var parsed map[string]any
	if err := json.Unmarshal(args, &parsed); err != nil || parsed == nil {
		return nil, fmt.Errorf("MCP arguments 必须为 object")
	}
	c.mu.Lock()
	servers := make([]*server, 0, len(c.servers))
	for _, s := range c.servers {
		servers = append(servers, s)
	}
	c.mu.Unlock()
	for _, s := range servers {
		s.mu.Lock()
		if visible(a, s) {
			for _, e := range s.entries {
				if e.PublicID == id {
					if e.SchemaDigest != digest || e.Status != "ready" {
						s.mu.Unlock()
						return nil, fmt.Errorf("MCP schema 已变或不可调用")
					}
					if err := e.schema.Validate(parsed); err != nil {
						s.mu.Unlock()
						return nil, fmt.Errorf("MCP 参数不符 schema: %w", err)
					}
					p := &ResolvedCall{client: c, server: s, access: a, entry: e, args: append(json.RawMessage(nil), args...), generation: s.generation, id: ecosystem.ID()}
					s.mu.Unlock()
					return p, nil
				}
			}
		}
		s.mu.Unlock()
	}
	return nil, ecosystem.ErrDenied
}
func (p *ResolvedCall) Target() (server, raw, public, digest string) {
	return p.entry.ServerID, p.entry.RawName, p.entry.PublicID, p.entry.SchemaDigest
}
func (p *ResolvedCall) Arguments() json.RawMessage { return append(json.RawMessage(nil), p.args...) }
func (p *ResolvedCall) validLocked(ctx context.Context) bool {
	s := p.server
	if !s.active || s.generation != p.generation || s.binding != p.entry.Binding || p.access.Check(ctx, "mcp:"+s.config.ID) != nil {
		return false
	}
	for _, e := range s.entries {
		if e.PublicID == p.entry.PublicID {
			return e.RawName == p.entry.RawName && e.SchemaDigest == p.entry.SchemaDigest && e.Status == "ready"
		}
	}
	return false
}
func (p *ResolvedCall) Valid() bool {
	p.server.mu.Lock()
	defer p.server.mu.Unlock()
	return p.validLocked(context.Background())
}

// Approve 在调度准入阶段审批同一个冻结目标，不占远端执行槽。
func (p *ResolvedCall) Approve(ctx context.Context, approve func(context.Context, *ResolvedCall) error) error {
	if approve == nil {
		return fmt.Errorf("没有 MCP 当次审批")
	}
	if err := approve(ctx, p); err != nil {
		return err
	}
	p.server.mu.Lock()
	defer p.server.mu.Unlock()
	if !p.validLocked(ctx) {
		return ecosystem.ErrDenied
	}
	p.approved = true
	return nil
}
func (p *ResolvedCall) Binding() string { return p.entry.Binding }

// Fingerprint 是服务连接目标的稳定指纹，重启后同一份配置算出同一个值；放行规则写进磁盘，认这个，不认每次启动都变的 Binding。
func (p *ResolvedCall) Fingerprint() string { return p.entry.Fingerprint }

// ReadOnlyClaim 是服务器自己声明这个工具只读（MCP annotations）。没有核实，只写在审批卡上，不拿来放宽。
func (p *ResolvedCall) ReadOnlyClaim() bool { return p.entry.ReadOnlyHint }
func (p *ResolvedCall) Execute(ctx context.Context) (*mcp.CallToolResult, error) {
	s := p.server
	select {
	case s.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.slot }()
	s.mu.Lock()
	if !p.approved || !p.validLocked(ctx) {
		s.mu.Unlock()
		return nil, ecosystem.ErrDenied
	}
	session, cancels := s.session, s.cancels
	epoch := s.recovery.epoch // 调用中主人显式连接、停用过，失败后就不自动重连了
	timeout := s.config.Sumika.CallTimeoutMs
	// 收尾必须按这次派发的 transport 判断，不能读并发重载后的服务配置。
	isHTTP := s.config.Type == "http"
	blocked, err := p.client.journal.MCPBlocked(ctx, s.config.ID)
	if err != nil || blocked {
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w：%s 之前有一次调用结果未知，要所有者在本机确认后才能再用；不要重试，也不要换服务名绕开", ErrQuarantined, s.config.ID)
	}
	s.pending = p
	done := make(chan struct{})
	s.pendingDone = done
	sentBefore := cancels.count()
	s.mu.Unlock()
	// pending 要一直留到收尾（落留痕、关连接、安排重连）做完才清：连接监视协程见到 pending 就等着，
	// 不会在这次调用的收尾做完之前抢先接管同一条连接（否则“已终止”可能在进程树确认退出之前就写进库，重连也会多算一次）
	defer func() {
		s.mu.Lock()
		s.pending = nil
		close(done)
		s.mu.Unlock()
	}()
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: p.entry.RawName, Arguments: p.args})
	s.mu.Lock()
	dispatched := p.dispatched
	s.mu.Unlock()
	if dispatched && err != nil && callCtx.Err() != nil {
		// SDK 在取消后异步补发 notifications/cancelled；记下这笔欠账，关连接前要等它发完。
		cancels.owe(sentBefore)
	}
	if dispatched {
		// 请求取消不妨碍落 unknown；由宿主活动保证数据库直到此处仍可用。
		persistCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		journalErr := p.client.journal.MCPEnd(persistCtx, p.id, err == nil, endCause(ctx, callCtx, err))
		stop()
		err = errors.Join(err, journalErr)
	}
	if err != nil && !isHTTP && dispatched {
		// 关连接的小错（管道已经关了之类）不代表进程还在；进程树有没有确认退出看 treeConfirmed，没确认就不记已终止、不重连
		p.client.disable(p.entry.ServerID, false)
		var closeErr error
		if !p.client.treeConfirmed(s) {
			closeErr = ErrOldProcessUnconfirmed
		} else {
			persistCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			closeErr = p.client.journal.MCPTerminated(persistCtx, p.id)
			stop()
		}
		if closeErr == nil {
			// 进程树已确认退出、留痕已记成已终止：这个本机服务不挡，按退避重新拉起（重连不能绕过隔离，Connect 自己会查）
			p.client.recoverAfterFailure(s, epoch)
		}
		err = errors.Join(err, closeErr)
	}
	if err != nil && dispatched {
		// 结果未知而被隔离，或本机连接已关：让宿主刷新提醒
		p.client.changed(p.entry.ServerID)
	}
	if err != nil {
		return nil, fmt.Errorf("MCP 调用失败或结局未知（%s）", p.entry.ServerID)
	}
	return result, nil
}

// endCause 是调用没拿到结果时记进留痕的原因（和 store.MCPCause* 同一组词）：调用时限到了、被停止或取消、其他出错。
func endCause(ctx, callCtx context.Context, err error) string {
	switch {
	case err == nil:
		return ""
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "error"
}
func encodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(raw string, v any) error {
	if len(raw) > 2048 {
		return fmt.Errorf("cursor_invalidated")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// SaveResult 与 result_read 共用 K2 的实体与授权，不另存 MCP 全文副本。
func SaveResult(ctx context.Context, store kernel.ResultStore, inv kernel.Invocation, result *mcp.CallToolResult) (kernel.ResultRef, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return kernel.ResultRef{}, err
	}
	return store.Put(ctx, inv, "application/json", bytes.NewReader(b), true)
}
