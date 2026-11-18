package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// 隔离：一次已经交给传输层的调用没有拿到结果（超时、被停止、断线），对方可能还在执行，
// 这个服务就不再派发新调用，免得重复执行有副作用的操作（规格-MCP客户端 2.4）。
// 留痕在库里（store/mcp_calls.go），重启不丢；解除只有一条路：主人在本地管理入口带原因接受风险，见 Release。

// ErrQuarantined 是“服务被隔离”的错误；调用方用 errors.Is 判断，给主人看的提示里要写怎么解除。
var ErrQuarantined = errors.New("MCP 服务因旧调用结果未知被隔离")

// ErrCallInFlight 是解除时这个服务正有一次调用在进行：等它结束再解除。
var ErrCallInFlight = errors.New("这个 MCP 服务正有一次调用在进行，等它结束再解除")

// transportOf 是这份配置的传输种类：只有 http 和 stdio 两种，没写 type 的是 stdio。
func transportOf(cfg ServerConfig) string {
	if cfg.Type == "http" {
		return "http"
	}
	return "stdio"
}

// Release 是主人带原因接受“旧调用结果未知”的风险后解除某个服务的隔离，返回解除了几行留痕。
// 留痕按 store.MCPRelease 记（谁、什么时候、原因，旧行保留、不改成成功）。
// 只给认证的本地管理入口用，不能做成模型能调的工具。这个服务正有调用在进行时拒绝：它那一行不是“遗留”。
// 解除不重连：连接断了的服务，调用方（宿主）用留存的配置再 Connect。
func (c *Client) Release(ctx context.Context, id, actor, reason string) (int, error) {
	n, err := c.release(ctx, id, actor, reason)
	if err != nil {
		return 0, err
	}
	c.changed(id)
	return n, nil
}

func (c *Client) release(ctx context.Context, id, actor, reason string) (int, error) {
	c.mu.Lock()
	s := c.servers[id]
	c.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		// 检查和改库在同一把锁里：传输层记派发也在这把锁里，不会有调用在两者之间溜进去
		if s.pending != nil {
			return 0, ErrCallInFlight
		}
	}
	n, err := c.journal.MCPRelease(ctx, id, actor, reason)
	if err != nil {
		return 0, fmt.Errorf("解除 %s 的隔离失败：%w", id, err)
	}
	return n, nil
}

// Health 是一个服务在内存里的连接状态（隔离状态在库里，见 store.MCPQuarantined）。
type Health struct {
	ID        string
	Transport string // stdio | http
	Connected bool   // 已连上、目录已发布，模型能搜到
	// 本机服务自动重连的状态：Reconnect 空 = 没事，retrying = 正在重连，exhausted = 次数用完；Note 是给人看的一句话
	Reconnect, Note string
}

// Health 列出连接过的服务，按 id 排序。
func (c *Client) Health() []Health {
	c.mu.Lock()
	servers := make(map[string]*server, len(c.servers))
	for id, s := range c.servers {
		servers[id] = s
	}
	c.mu.Unlock()
	out := make([]Health, 0, len(servers))
	for id, s := range servers {
		s.mu.Lock()
		h := Health{ID: id, Transport: transportOf(s.config), Connected: s.active, Note: s.recovery.lastErr}
		switch {
		case s.unconfirmedWhy() != "":
			h.Reconnect, h.Note = "exhausted", s.unconfirmedWhy()
		case s.recovery.exhausted != "":
			h.Reconnect, h.Note = "exhausted", s.recovery.exhausted
		case s.recovery.running:
			h.Reconnect = "retrying"
		}
		out = append(out, h)
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
