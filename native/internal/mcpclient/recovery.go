package mcpclient

import (
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 本机服务（子进程）的自动重连（规格-MCP客户端 2.7、第 258 行起的参数表）。
//
// 什么时候重连：本机服务的子进程退出、管道断了、或一次调用失败后宿主关了连接并确认进程树已经退出。
// 什么时候不重连：
//   - 服务被隔离（库里还有“结果未知”的旧调用）：Connect 自己会拒绝，重连不能绕过隔离；
//   - 远端 HTTP 服务：它的“断开”不等于旧调用结束，也没有进程可拉；
//   - 主人显式停用、改配置、重新连接、关机：重连代次（epoch）一变，正在进行的重连作废；
//   - 关旧连接时没能确认进程树退出：禁止起新代。
//
// 次数和间隔：0.5、1、2、4、8 秒，最多 5 次；重连成功后连续运行满 stableAfter，下一次断开才重新从头数
// （崩溃循环不会无限起进程）。用完次数停在 exhausted，等主人用 mcp reconnect 或 mcp release 再试。
// 绝不重放：重连只重建连接，已经交出去的 tools/call 不会再发。

// 参数；测试里改小
var (
	recoverDelays = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	stableAfter   = 30 * time.Second
)

var errSuperseded = errors.New("重连已被新的连接或停用取代")

// recovery 是一个服务的自动重连状态，由 server.mu 保护
type recovery struct {
	epoch       uint64 // 主人每次显式连接、停用就加一；重连循环带着启动时的值，见到不同就退出
	running     bool
	attempts    int
	exhausted   string // 非空：次数用完（或不能重连）的原因
	lastErr     string
	connectedAt time.Time // 最近一次自动重连成功的时间
}

// reset 是主人显式连接、停用：旧的重连作废，预算清零
func (r *recovery) reset() {
	r.epoch++
	r.running = false
	r.attempts = 0
	r.exhausted = ""
	r.lastErr = ""
}

func (c *Client) logff(format string, args ...any) {
	c.mu.Lock()
	f := c.logf
	c.mu.Unlock()
	if f != nil {
		f(format, args...)
	}
}

// SetLogf 登记自动重连的日志函数
func (c *Client) SetLogf(f func(format string, args ...any)) {
	c.mu.Lock()
	c.logf = f
	c.mu.Unlock()
}

// ErrOldProcessUnconfirmed：上一个本机进程树没能确认退出，不起新的（规格 2.7：无法确认则不起新代）
var ErrOldProcessUnconfirmed = errors.New("旧进程没能确认退出，不能再起新的")

// unconfirmedWhy 是“旧进程未确认退出”的说明；空 = 没有这回事。不取 s.mu，任何路径上都能调
func (s *server) unconfirmedWhy() string {
	why, _ := s.unconfirmed.Load().(string)
	return why
}

// markUnconfirmed 记下“旧进程未确认退出”（s.unconfirmed）：之后任何连接（自动、显式）都被拒，进程内没法再确认，直到 Sumika 重启才清。
// 由进程连接关闭时的 onTree 回调（process.go）触发：只有强杀失败、等不到整棵树退出才算，管道已关之类的小错不算。
func (c *Client) markUnconfirmed(s *server, id string, err error) {
	s.unconfirmed.Store("关旧连接时没能确认进程树已退出（" + err.Error() + "）；为防同时有两个进程，不再启动新的，重启 Sumika 后再连")
	c.logff("MCP 服务 %s 的旧进程没能确认退出：%v", id, err)
}

// treeConfirmed 是最近一次关连接时进程树有没有确认退出（没有标记 = 确认了，或根本没有进程）
func (c *Client) treeConfirmed(s *server) bool {
	return s.unconfirmedWhy() == ""
}

// closeOld 关一个旧连接（回收本机进程树）。返回的错误可能只是管道已关之类的小错；进程树有没有确认退出看 s.unconfirmed。
func (c *Client) closeOld(id string, old *mcp.ClientSession, cancels *cancelTracker) error {
	err := closeSession(old, cancels)
	if err != nil {
		c.logff("MCP 服务 %s 关旧连接时报错：%v", id, err)
	}
	return err
}

// watch 盯着一个连接：它在没有调用进行时意外结束（子进程退出、管道断），就关掉它并安排重连。
// 有调用在进行时不管：那次调用会因连接断了而失败，走调用失败的收尾（resolved.go）。
func (c *Client) watch(s *server, session *mcp.ClientSession, generation uint64) {
	_ = session.Wait()
	for {
		s.mu.Lock()
		if s.generation != generation || s.session != session {
			s.mu.Unlock()
			return
		}
		if s.pending == nil {
			break // 持着 s.mu 往下走
		}
		// 有调用在进行：连接断了那次调用会失败，由它的收尾处理（关连接、记已终止、安排重连）；
		// 收尾做完再看——调用成功返回后进程才退出的话，这里接手
		done := s.pendingDone
		s.mu.Unlock()
		select {
		case <-done:
		case <-c.ctx.Done():
			return
		}
	}
	old, cancels := s.session, s.cancels
	id := s.config.ID
	stdio := transportOf(s.config) == "stdio"
	s.active = false
	s.generation++
	s.session, s.cancels, s.entries = nil, nil, nil
	s.mu.Unlock()
	c.logff("MCP 服务 %s 的连接断了", id)
	c.closeOld(id, old, cancels) // 回收进程树；没确认退出（s.unconfirmed）就不起新的
	if stdio {
		s.mu.Lock()
		c.startRecoveryLocked(s) // 旧进程没确认退出（s.unconfirmed）时里面不会起
		s.mu.Unlock()
	}
	c.changed(id)
}

// recoverAfterFailure 在本机服务的一次调用失败、宿主关了连接并确认进程树已退出以后调：
// 调用开始后主人没有显式连接、停用过（epoch 没变）才重连。
func (c *Client) recoverAfterFailure(s *server, epoch uint64) {
	s.mu.Lock()
	if s.recovery.epoch == epoch {
		c.startRecoveryLocked(s)
	}
	s.mu.Unlock()
}

// startRecoveryLocked 起一个重连循环（调用时持有 s.mu）。已经有一个在跑、已经连上、不是本机服务、已关机，都不起。
func (c *Client) startRecoveryLocked(s *server) {
	if transportOf(s.config) != "stdio" || s.active || s.recovery.running || s.recovery.exhausted != "" || s.unconfirmedWhy() != "" {
		return
	}
	if !s.recovery.connectedAt.IsZero() && time.Since(s.recovery.connectedAt) >= stableAfter {
		s.recovery.attempts = 0 // 上次重连后稳定运行够久了，预算重新算
	}
	if s.recovery.attempts >= len(recoverDelays) {
		s.recovery.exhausted = fmt.Sprintf("已经连续重连 %d 次都没成功或很快又断了", len(recoverDelays))
		if s.recovery.lastErr != "" {
			s.recovery.exhausted += "：" + s.recovery.lastErr
		}
		return
	}
	c.recoverMu.Lock()
	if c.closed {
		c.recoverMu.Unlock()
		return
	}
	c.recoverWG.Add(1)
	c.recoverMu.Unlock()
	s.recovery.running = true
	go c.recoverLoop(s, s.recovery.epoch)
}

func (c *Client) recoverLoop(s *server, epoch uint64) {
	defer c.recoverWG.Done()
	for {
		s.mu.Lock()
		if s.recovery.epoch != epoch {
			s.mu.Unlock()
			return
		}
		i := s.recovery.attempts
		cfg := s.config
		if i >= len(recoverDelays) {
			s.recovery.running = false
			s.recovery.exhausted = fmt.Sprintf("重连 %d 次都没成功：%s", len(recoverDelays), s.recovery.lastErr)
			s.mu.Unlock()
			c.logff("MCP 服务 %s 自动重连用完 %d 次，放弃：%s", cfg.ID, len(recoverDelays), s.recovery.lastErr)
			c.changed(cfg.ID)
			return
		}
		s.recovery.attempts++
		delay := recoverDelays[i]
		s.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-c.ctx.Done():
			timer.Stop()
			return
		}
		err := c.connect(c.ctx, cfg, &epoch)
		switch {
		case err == nil:
			s.mu.Lock()
			if s.recovery.epoch == epoch {
				s.recovery.running = false
				s.recovery.lastErr = ""
				s.recovery.connectedAt = time.Now()
			}
			s.mu.Unlock()
			c.logff("MCP 服务 %s 已自动重连（第 %d 次）", cfg.ID, i+1)
			c.changed(cfg.ID)
			return
		case errors.Is(err, errSuperseded) || c.ctx.Err() != nil:
			return
		case errors.Is(err, ErrQuarantined):
			// 隔离优先：不重连也不计次数；解除后由主人重连
			s.mu.Lock()
			if s.recovery.epoch == epoch {
				s.recovery.running = false
				s.recovery.attempts-- // 没有真的重连，不吃预算
				s.recovery.lastErr = "服务被隔离，没有自动重连"
			}
			s.mu.Unlock()
			c.logff("MCP 服务 %s 被隔离，不自动重连", cfg.ID)
			c.changed(cfg.ID)
			return
		}
		s.mu.Lock()
		if s.recovery.epoch == epoch {
			s.recovery.lastErr = err.Error()
		}
		s.mu.Unlock()
		c.logff("MCP 服务 %s 第 %d 次自动重连失败：%v", cfg.ID, i+1, err)
	}
}
