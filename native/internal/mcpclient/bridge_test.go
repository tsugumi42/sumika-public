package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/store"
)

// 转给别处的本机服务（桥、容器、ssh）记成 stdio-remote，启动收尾不自动当作已终止；认不出的照本机处理。
func TestDispatchTransportRecognizesBridges(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		want string
	}{
		{"普通本机 node", ServerConfig{Command: "node", Args: []string{"server.js"}}, "stdio"},
		{"npx 起本机包", ServerConfig{Command: "npx", Args: []string{"-y", "@scope/some-files-server"}}, "stdio"},
		{"远端 HTTP", ServerConfig{Type: "http", URL: "https://example.com/mcp"}, "http"},
		{"npx mcp-remote", ServerConfig{Command: "npx", Args: []string{"-y", "mcp-remote", "https://example.com/mcp"}}, "stdio-remote"},
		{"带版本的 mcp-remote", ServerConfig{Command: "npx", Args: []string{"mcp-remote@latest", "https://example.com"}}, "stdio-remote"},
		{"带作用域和版本的桥", ServerConfig{Command: "npx", Args: []string{"@x/supergateway@1.2.3"}}, "stdio-remote"},
		{"uvx mcp-proxy", ServerConfig{Command: "uvx", Args: []string{"mcp-proxy", "http://x"}}, "stdio-remote"},
		{"docker", ServerConfig{Command: "docker", Args: []string{"run", "-i", "--rm", "img"}}, "stdio-remote"},
		{"docker 带路径和 exe、大小写", ServerConfig{Command: `C:\Program Files\Docker\DOCKER.EXE`, Args: []string{"run", "-i"}}, "stdio-remote"},
		{"podman", ServerConfig{Command: "podman", Args: []string{"run", "-i", "img"}}, "stdio-remote"},
		{"wsl", ServerConfig{Command: "wsl.exe", Args: []string{"node", "/home/x/server.js"}}, "stdio-remote"},
		{"ssh", ServerConfig{Command: "ssh", Args: []string{"host", "server"}}, "stdio-remote"},
		{"cmd /c 里套 docker", ServerConfig{Command: "cmd", Args: []string{"/c", "docker", "run", "-i", "img"}}, "stdio-remote"},
		{"名字只是带了这几个字", ServerConfig{Command: "node", Args: []string{"docker-notes-server.js"}}, "stdio"},
	}
	for _, c := range cases {
		if got := dispatchTransport(c.cfg); got != c.want {
			t.Errorf("%s：得到 %s，应为 %s", c.name, got, c.want)
		}
	}
}

// 在线的本机服务调用留下的行记着传输种类 stdio、进程号、时间（启动收尾靠它们判断）。
func TestStdioDispatchRecordsTransportInNote(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.journal.DB.QueryRowContext(t.Context(), `SELECT reason FROM kernel_mcp_calls WHERE server_id='near'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var note store.MCPNote
	if err := json.Unmarshal([]byte(raw), &note); err != nil || note.Transport != "stdio" || note.Process != store.ProcessID() || note.DispatchedAt == 0 {
		t.Fatalf("%s %+v %v", raw, note, err)
	}
}

// 关旧连接时没能确认本机进程树退出：不起新进程，自动的、显式的连接都被拒，状态可见，换回真的关闭函数也不行（只有重启才清）。
func TestStdioUnconfirmedOldProcessBlocksAnyNewProcess(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	real := treeConfirm
	treeConfirm = func(g *processGroup) error {
		real(g) // 进程树其实退干净了，只是“确认”这一步报告失败
		return errors.New("模拟：进程树没能确认退出")
	}
	restored := false
	restore := func() {
		if !restored {
			treeConfirm, restored = real, true
		}
	}
	t.Cleanup(restore)
	f.client.mu.Lock()
	session := f.client.servers["near"].session
	f.client.mu.Unlock()
	session.Close() // 管道断
	f.waitFor(t, "发现旧进程没能确认退出", func() bool { return strings.Contains(f.health().Note, "没能确认") })
	f.client.recoverWG.Wait()
	if h := f.health(); h.Connected || h.Reconnect != "exhausted" {
		t.Fatalf("状态要可见：%+v", h)
	}
	if err := f.client.Connect(t.Context(), f.cfg); !errors.Is(err, ErrOldProcessUnconfirmed) {
		t.Fatalf("显式连接起了第二个进程：%v", err)
	}
	restore()
	if err := f.client.Connect(t.Context(), f.cfg); !errors.Is(err, ErrOldProcessUnconfirmed) {
		t.Fatalf("进程内没法再确认，标记不能自己清：%v", err)
	}
	if got := strings.Join(f.helperLog(t), ","); got != "start" {
		t.Fatalf("不该又起进程：%s", got)
	}
}

// 调用失败后关连接，进程树没能确认退出：不记已终止（服务继续被挡）、不自动重连、显式连接也被拒。
func TestStdioCallFailureWithUnconfirmedTreeStaysIsolated(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	real := treeConfirm
	treeConfirm = func(g *processGroup) error {
		real(g)
		return errors.New("模拟：进程树没能确认退出")
	}
	t.Cleanup(func() { treeConfirm = real })
	if _, err := f.resolved(t, "crash").Execute(t.Context()); err == nil {
		t.Fatal("崩溃的调用被当成成功")
	}
	f.client.recoverWG.Wait()
	if !f.state(t) {
		t.Fatal("进程树没确认退出，却记成已终止、不挡了")
	}
	if h := f.health(); h.Connected || h.Reconnect != "exhausted" {
		t.Fatalf("状态要可见：%+v", h)
	}
	if err := f.client.Connect(t.Context(), f.cfg); !errors.Is(err, ErrOldProcessUnconfirmed) && !errors.Is(err, ErrQuarantined) {
		t.Fatalf("显式连接起了第二个进程：%v", err) // 留痕还是 unknown，先被隔离拦住；两道都是拦
	}
	if got := strings.Join(f.helperLog(t), ","); got != "start,call:crash" {
		t.Fatalf("不该又起进程：%s", got)
	}
}

// 回归（第二条路）：握手后发布前连接代次变了（被别的连接或停用顶掉），关刚起的进程时进程树没确认退出：
// 同样要在时限内返回、置上标记，不能在持锁关连接时自己等自己。
func TestStdioHandshakeGenerationChangedWhileTreeUnconfirmedDoesNotDeadlock(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	if err := f.client.Disable("near"); err != nil {
		t.Fatal(err)
	}
	real := treeConfirm
	treeConfirm = func(g *processGroup) error {
		real(g)
		return errors.New("模拟：进程树没能确认退出")
	}
	afterCatalogHook = func(s *server) {
		s.mu.Lock()
		s.generation++ // 模拟别的连接或停用在握手期间顶掉了这一代
		s.mu.Unlock()
	}
	t.Cleanup(func() { treeConfirm, afterCatalogHook = real, nil })
	done := make(chan error, 1)
	go func() { done <- f.client.Connect(t.Context(), f.cfg) }()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, ErrQuarantined) {
			t.Fatalf("应因“连接代次已变”被拒：%v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Connect 30 秒没返回：代次变化那一支在持锁关连接时又取了这把锁")
	}
	if f.client.treeConfirmed(f.client.servers["near"]) {
		t.Fatal("进程树没确认退出的标记要被置上")
	}
}

// flipBlocked 第一次查 MCPBlocked 说没挡，之后都说挡：模拟“握手期间旧调用变成 unknown”，让连接在持锁的发布步骤里被拒并关掉刚握手成功的进程
type flipBlocked struct {
	store.EcosystemStore
	calls atomic.Int32
	armed atomic.Bool
}

func (j *flipBlocked) MCPBlocked(ctx context.Context, server string) (bool, error) {
	if !j.armed.Load() {
		return j.EcosystemStore.MCPBlocked(ctx, server)
	}
	return j.calls.Add(1) > 1, nil
}

// 回归：握手后在发布步骤里被隔离复核拒绝、关刚起的进程时进程树又没能确认退出——关连接回调要写“未确认”标记，
// 不能再去取同一把服务锁（曾经自己等自己，Connect 永不返回、Client.Close 也卡死）。
func TestStdioHandshakeRejectedWhileTreeUnconfirmedDoesNotDeadlock(t *testing.T) {
	j := &flipBlocked{}
	f := newStdioFixtureWith(t, 60000, false, func(s store.EcosystemStore) Journal { j.EcosystemStore = s; return j })
	if err := f.client.Disable("near"); err != nil {
		t.Fatal(err)
	}
	real := treeConfirm
	treeConfirm = func(g *processGroup) error {
		real(g)
		return errors.New("模拟：进程树没能确认退出")
	}
	t.Cleanup(func() { treeConfirm = real })
	j.armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- f.client.Connect(t.Context(), f.cfg) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrQuarantined) {
			t.Fatalf("应因隔离复核被拒：%v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Connect 30 秒没返回：关连接的回调在持着服务锁时又取了这把锁")
	}
	if f.health().Note == "" || f.client.treeConfirmed(f.client.servers["near"]) {
		t.Fatalf("进程树没确认退出的标记要被置上：%+v", f.health())
	}
}
