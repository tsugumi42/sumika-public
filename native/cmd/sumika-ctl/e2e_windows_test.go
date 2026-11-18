package main

// 客户端接真的服务端、真的命名管道（带测试名）：命令行 → 管道 → internal/ctlserver 的校验 → 假后端，再走回来变成退出码和输出。
// 服务端是只在这个测试里引进来的；发出去的 sumika-ctl 不链接它（internal/archcheck 守着，那边查的是不含测试文件的依赖）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

type stubBackend struct {
	mu    sync.Mutex
	calls []string
	quit  chan struct{}
}

func (b *stubBackend) rec(s string) { b.mu.Lock(); b.calls = append(b.calls, s); b.mu.Unlock() }
func (b *stubBackend) Status(context.Context) (ctlproto.Status, error) {
	b.rec("status")
	return ctlproto.Status{Phase: "archive", Mode: "archive", Reason: "只看历史", Account: "***01", Version: "测试", Pid: 1, Alerts: []ctlproto.Alert{}}, nil
}
func (b *stubBackend) ReceiveStart(_ context.Context, wait time.Duration) (ctlproto.Receive, error) {
	b.rec(fmt.Sprintf("receive-start %v", wait))
	r := ctlproto.Receive{Phase: "archive", Mode: "archive", Reason: "开始接收失败：测试", Settled: true}
	return r, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: r.Reason, Data: r}
}
func (b *stubBackend) ReceiveStop(context.Context) (ctlproto.Receive, error) {
	b.rec("receive-stop")
	return ctlproto.Receive{Phase: "archive", Mode: "archive", Settled: true}, nil
}
func (b *stubBackend) ShowWindow(context.Context) error { b.rec("window-show"); return nil }
func (b *stubBackend) Logs(_ context.Context, n int, _ ctlproto.LogFilter) (ctlproto.Logs, error) {
	b.rec(fmt.Sprintf("logs %d", n))
	return ctlproto.Logs{Lines: []string{"一行"}}, nil
}
func (b *stubBackend) GateList(context.Context) (ctlproto.GateList, error) {
	b.rec("gate-list")
	return ctlproto.GateList{Groups: []ctlproto.GateRow{{Conversation: "qq:group:10001", Mode: "dry", Source: "default"}}}, nil
}
func (b *stubBackend) GateSet(_ context.Context, conv, mode string) (ctlproto.GateChange, error) {
	b.rec("gate-set " + conv + " " + mode)
	return ctlproto.GateChange{}, ctlproto.Errorf(ctlproto.CodeReadOnly, "现在是只看历史模式，库是只读的，先开始接收再调档位")
}
func (b *stubBackend) SetAutoReceive(_ context.Context, on bool) error {
	b.rec(fmt.Sprintf("auto-receive %v", on))
	return nil
}
func (b *stubBackend) SetAutostart(_ context.Context, on bool) error {
	b.rec(fmt.Sprintf("autostart %v", on))
	return errors.New("注册表写不了")
}
func (b *stubBackend) Quit() { b.rec("quit"); close(b.quit) }

var pipeSeq atomic.Int64

// testPipeName 给测试管道取名：带进程号和进程内计数器。只靠 time.Now 不够：Windows 上它约 0.5 毫秒分辨率，
// 两个进程同时跑同一个测试会取到同一个名字，建管道报 Access is denied。绝不连真实的 sumika-ctl-<SID>。
func testPipeName(tag string) string {
	return fmt.Sprintf(`\\.\pipe\sumika-ctl-test-%s-p%d-%d-%d`, tag, os.Getpid(), time.Now().UnixNano(), pipeSeq.Add(1))
}

func TestClientAgainstRealServer(t *testing.T) {
	name := testPipeName("e2e")
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	b := &stubBackend{quit: make(chan struct{})}
	srv := ctlserver.New(b, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }

	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb, dial)
		return code, out.String(), errb.String()
	}
	if code, out, _ := do("status"); code != 0 || !strings.Contains(out, "阶段：只看历史（archive）") {
		t.Fatalf("status：%d %q", code, out)
	}
	if code, out, _ := do("--json", "status"); code != 0 || !strings.Contains(out, `"phase":"archive"`) {
		t.Fatalf("status --json：%d %q", code, out)
	}
	// 后端的失败带着状态传回来，退出码 1
	if code, _, errs := do("receive", "start", "--wait=30s"); code != 1 || !strings.Contains(errs, "开始接收失败：测试") || !strings.Contains(errs, "只看历史（archive）") {
		t.Fatalf("receive start：%d %q", code, errs)
	}
	if code, _, _ := do("receive", "stop"); code != 0 {
		t.Fatalf("receive stop：%d", code)
	}
	if code, out, _ := do("logs", "-n", "3"); code != 0 || out != "一行\n" {
		t.Fatalf("logs：%d %q", code, out)
	}
	// speak 到不了后端：被拒，退出码 3，提示里说请在界面上改
	if code, _, errs := do("gate", "set", "qq:group:10001", "speak"); code != 3 || !strings.Contains(errs, "界面") {
		t.Fatalf("speak：%d %q", code, errs)
	}
	if code, _, _ := do("gate", "set", "private:1", "off"); code != 3 {
		t.Fatalf("坏会话：%d", code)
	}
	// 合法的 off 到了后端，后端回 readonly → 退出码 1
	if code, _, errs := do("gate", "set", "qq:group:10001", "off"); code != 1 || !strings.Contains(errs, "readonly") {
		t.Fatalf("readonly：%d %q", code, errs)
	}
	if code, out, _ := do("gate", "list"); code != 0 || !strings.Contains(out, "qq:group:***01") || !strings.Contains(out, "句柄 ") || strings.Contains(out, "10001") {
		t.Fatalf("gate list 要打码并给句柄：%d %q", code, out)
	}
	// 句柄 → 服务端解析成完整会话 → 后端收到的是完整会话；后端回 readonly，退出码 1
	code, jout, _ := do("--json", "gate", "list")
	var lr ctlproto.Reply
	var gl ctlproto.GateList
	if code != 0 || json.Unmarshal([]byte(jout), &lr) != nil || json.Unmarshal(lr.Data, &gl) != nil || len(gl.Groups) != 1 || !ctlproto.IsHandle(gl.Groups[0].Handle) || strings.Contains(jout, "10001") {
		t.Fatalf("gate list --json：%d %q", code, jout)
	}
	if code, _, errs := do("gate", "set", gl.Groups[0].Handle, "off"); code != 1 || !strings.Contains(errs, "readonly") {
		t.Fatalf("用句柄 gate set：%d %q", code, errs)
	}
	if code, _, _ := do("gate", "set", "qq:group:***01", "off"); code != 3 { // 打码的写法不是群的名字
		t.Fatalf("打码的写法：%d", code)
	}
	if code, _, errs := do("autostart", "on"); code != 1 || !strings.Contains(errs, "注册表写不了") {
		t.Fatalf("autostart：%d %q", code, errs)
	}
	if code, _, _ := do("auto-receive", "ON"); code != 3 {
		t.Fatalf("auto-receive 大小写变体：%d", code)
	}
	if code, _, _ := do("nonsense"); code != 3 {
		t.Fatalf("不认识的命令：%d", code)
	}
	if code, out, _ := do("quit"); code != 0 || !strings.Contains(out, "正在退出") {
		t.Fatalf("quit：%d %q", code, out)
	}
	select {
	case <-b.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("quit 的回复之后后端没收到 Quit")
	}
	want := "status|status|receive-start 30s|receive-stop|logs 3|gate-set qq:group:10001 off|gate-list|gate-list|gate-list|gate-set qq:group:10001 off|autostart true|quit"
	b.mu.Lock()
	got := strings.Join(b.calls, "|")
	b.mu.Unlock()
	if got != want {
		t.Fatalf("后端收到的调用：\n%s\n应是\n%s", got, want)
	}
}
