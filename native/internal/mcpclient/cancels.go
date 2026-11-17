package mcpclient

import (
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cancelledMethod 是 MCP 的取消通知。SDK 在调用的 context 取消后异步发它，
// 发送与调用返回之间没有任何同步。
const cancelledMethod = "notifications/cancelled"

// cancelWait 与 SDK 自己给这条通知的时限一致：通知到不了就不无限等。
const cancelWait = 5 * time.Second

// cancelTracker 记录一个连接上取消通知的发出情况，并记下“还欠一条”的调用。
// 它跟着连接走：换连接就换一个，旧连接的通知不会放行新连接。
type cancelTracker struct {
	mu      sync.Mutex
	sent    int           // 已经经过传输层的取消通知数
	changed chan struct{} // 每发出一条就关闭并换新，用来唤醒等待者
	owed    bool          // 有一次调用被取消，它的通知可能还在路上
	owedAt  int           // 该调用开始时的 sent，sent 超过它才算通知已发出
}

func (t *cancelTracker) count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sent
}

// note 在一条取消通知经过传输层（写完或失败）后调用。
func (t *cancelTracker) note() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent++
	if t.changed != nil {
		close(t.changed)
		t.changed = nil
	}
}

// owe 记下：起点为 at 的那次调用已被取消，SDK 会补发取消通知。
func (t *cancelTracker) owe(at int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.owed, t.owedAt = true, at
	t.mu.Unlock()
}

// settle 等欠着的取消通知发出去（最多 cancelWait）。没有欠账立即返回。
func (t *cancelTracker) settle() {
	if t == nil {
		return
	}
	deadline := time.NewTimer(cancelWait)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		if !t.owed || t.sent > t.owedAt {
			t.owed = false
			t.mu.Unlock()
			return
		}
		if t.changed == nil {
			t.changed = make(chan struct{})
		}
		ch := t.changed
		t.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			return
		}
	}
}

// closeSession 关连接前先等取消通知发出去。否则远端那次调用收不到取消，
// 会一直占着它的处理协程，连接关闭或服务停止时也卡在等它退出上。
func closeSession(session *mcp.ClientSession, cancels *cancelTracker) error {
	cancels.settle()
	return session.Close()
}
