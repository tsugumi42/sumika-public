package ctlserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// fakeBackend 记下每个调用：测试断言"调到了哪个后端函数、带什么参数"，以及被拒的请求"一个后端函数都没调"
type fakeBackend struct {
	mu    sync.Mutex
	calls []string

	status ctlproto.Status
	recv   ctlproto.Receive
	logs   ctlproto.Logs
	list   ctlproto.GateList
	change ctlproto.GateChange
	err    error // 下一个会返回错误的调用用它

	blockStart chan struct{} // 非 nil：ReceiveStart 等它关闭或 ctx 取消
	quitCalled chan struct{}
	quitGate   chan struct{} // 非 nil：Quit 等它关闭才返回（测"先回复、后退出"）
	panicOn    string
}

func (f *fakeBackend) rec(format string, args ...any) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
	f.mu.Unlock()
}

func (f *fakeBackend) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeBackend) boom(name string) {
	if f.panicOn == name {
		panic("故意的")
	}
}

func (f *fakeBackend) Status(context.Context) (ctlproto.Status, error) {
	f.rec("status")
	f.boom("status")
	return f.status, f.err
}
func (f *fakeBackend) ReceiveStart(ctx context.Context, wait time.Duration) (ctlproto.Receive, error) {
	f.rec("receive-start wait=%v", wait)
	if f.blockStart != nil {
		select {
		case <-f.blockStart:
		case <-ctx.Done():
			return ctlproto.Receive{}, ctx.Err()
		}
	}
	return f.recv, f.err
}
func (f *fakeBackend) ReceiveStop(context.Context) (ctlproto.Receive, error) {
	f.rec("receive-stop")
	return f.recv, f.err
}
func (f *fakeBackend) ShowWindow(context.Context) error { f.rec("window-show"); return f.err }
func (f *fakeBackend) Logs(_ context.Context, n int, _ ctlproto.LogFilter) (ctlproto.Logs, error) {
	f.rec("logs n=%d", n)
	return f.logs, f.err
}
func (f *fakeBackend) GateList(context.Context) (ctlproto.GateList, error) {
	f.rec("gate-list")
	return f.list, f.err
}
func (f *fakeBackend) GateSet(_ context.Context, conv, mode string) (ctlproto.GateChange, error) {
	f.rec("gate-set %s %s", conv, mode)
	c := f.change
	if c.Conversation == "" { // 没特别指定时回“改的就是请求里的这个群”，和真后端一样给完整会话号（服务端出去前打码）
		c.Conversation, c.After = conv, mode
	}
	return c, f.err
}
func (f *fakeBackend) SetAutoReceive(_ context.Context, on bool) error {
	f.rec("auto-receive %v", on)
	return f.err
}
func (f *fakeBackend) SetAutostart(_ context.Context, on bool) error {
	f.rec("autostart %v", on)
	return f.err
}
func (f *fakeBackend) Quit() {
	f.rec("quit")
	if f.quitGate != nil {
		<-f.quitGate
	}
	if f.quitCalled != nil {
		close(f.quitCalled)
	}
}

func needWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("控制接口走 Windows 命名管道")
	}
}

var pipeSeq struct {
	sync.Mutex
	n int
}

// testPipe 是带测试名的管道名：绝不连真实的 sumika-ctl-<SID>。
// 名字带进程号：Go 在 Windows 上的 time.Now 只有约 0.5 毫秒分辨率，两个进程同时跑同一个测试时，
// 时间戳和各自从 1 开始的计数器都可能相同，建管道会因同名报 Access is denied（ListenPipe 对已存在的名字不抢）。
func testPipe(t *testing.T) string {
	t.Helper()
	pipeSeq.Lock()
	pipeSeq.n++
	n := pipeSeq.n
	pipeSeq.Unlock()
	name := strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, t.Name())
	return fmt.Sprintf(`\\.\pipe\sumika-ctl-test-%s-p%d-%d-%d`, name, os.Getpid(), time.Now().UnixNano(), n)
}

// start 起一个接了 fake 的服务端，返回管道名
func start(t *testing.T, b Backend, opt Options) (string, *Server) {
	t.Helper()
	needWindows(t)
	name := testPipe(t)
	ln, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(b, opt)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve 返回了错误：%v", err)
		}
	})
	return name, srv
}

func dial(t *testing.T, name string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := ctlproto.Dial(ctx, name)
	if err != nil {
		t.Fatalf("连不上 %s：%v", name, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// raw 发一行原始字节，读回一个回复
func raw(t *testing.T, name string, line []byte) ctlproto.Reply {
	t.Helper()
	c := dial(t, name)
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(append(append([]byte(nil), line...), '\n')); err != nil {
		t.Fatalf("写请求：%v", err)
	}
	return readReply(t, c)
}

func readReply(t *testing.T, c net.Conn) ctlproto.Reply {
	t.Helper()
	b, err := ctlproto.ReadLine(bufio.NewReader(c), 1<<20)
	if err != nil {
		t.Fatalf("读回复：%v", err)
	}
	var r ctlproto.Reply
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("回复不是 JSON：%v\n%s", err, b)
	}
	return r
}

func ask(t *testing.T, name string, req ctlproto.Request) ctlproto.Reply {
	t.Helper()
	if req.V == 0 {
		req.V = ctlproto.Version
	}
	b, _ := json.Marshal(req)
	return raw(t, name, b)
}

func dataOf[T any](t *testing.T, r ctlproto.Reply) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Data, &v); err != nil {
		t.Fatalf("data 解不开：%v\n%s", err, r.Data)
	}
	return v
}

func wantCalls(t *testing.T, f *fakeBackend, want ...string) {
	t.Helper()
	got := f.got()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("后端的调用不对：\n得到 %q\n应是 %q", got, want)
	}
}

// ───────────── 验收 1：每个命令一个测试，调到后端、回复字段和退出码对 ─────────────

func TestStatus(t *testing.T) {
	f := &fakeBackend{status: ctlproto.Status{Mode: "archive", Phase: "archive", Reason: "只看历史", Account: "***01", Pid: 4242, Version: "devel"}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "status"})
	if !r.OK || ctlproto.ExitCode(r) != 0 {
		t.Fatalf("%+v", r)
	}
	s := dataOf[ctlproto.Status](t, r)
	if s.Phase != "archive" || s.Account != "***01" || s.Pid != 4242 {
		t.Fatalf("%+v", s)
	}
	wantCalls(t, f, "status")
}

func TestReceiveStartAndStop(t *testing.T) {
	f := &fakeBackend{recv: ctlproto.Receive{Phase: "receiving", Mode: "receive", Receiving: true, Settled: true}}
	name, _ := start(t, f, Options{})
	if r := ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"start"}}); !r.OK || !dataOf[ctlproto.Receive](t, r).Receiving {
		t.Fatalf("%+v", r)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "90s"}); !r.OK {
		t.Fatalf("%+v", r)
	}
	f.recv = ctlproto.Receive{Phase: "archive", Mode: "archive", Reason: "已停止接收", Settled: true}
	if r := ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"stop"}}); !r.OK || dataOf[ctlproto.Receive](t, r).Phase != "archive" {
		t.Fatalf("%+v", r)
	}
	wantCalls(t, f, "receive-start wait=0s", "receive-start wait=1m30s", "receive-stop")
}

func TestWindowShow(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	if r := ask(t, name, ctlproto.Request{Cmd: "window", Args: []string{"show"}}); !r.OK {
		t.Fatalf("%+v", r)
	}
	wantCalls(t, f, "window-show")
}

func TestLogsLineCounts(t *testing.T) {
	f := &fakeBackend{logs: ctlproto.Logs{Lines: []string{"甲", "乙"}}}
	name, _ := start(t, f, Options{})
	for _, n := range []int{0, 1, 500} {
		r := ask(t, name, ctlproto.Request{Cmd: "logs", N: n})
		if !r.OK || len(dataOf[ctlproto.Logs](t, r).Lines) != 2 {
			t.Fatalf("n=%d：%+v", n, r)
		}
	}
	wantCalls(t, f, "logs n=50", "logs n=1", "logs n=500")
	for _, n := range []int{-1, 501, 100000} {
		if r := ask(t, name, ctlproto.Request{Cmd: "logs", N: n}); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Fatalf("n=%d 应被拒：%+v", n, r)
		}
	}
	wantCalls(t, f, "logs n=50", "logs n=1", "logs n=500")
}

func TestGateListAndSet(t *testing.T) {
	f := &fakeBackend{
		list:   ctlproto.GateList{Groups: []ctlproto.GateRow{{Conversation: "qq:group:10001", Mode: "dry", Source: "default"}}},
		change: ctlproto.GateChange{Conversation: "qq:group:10001", Before: "speak", After: "off"},
	}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})
	if g := dataOf[ctlproto.GateList](t, r); !r.OK || len(g.Groups) != 1 || g.Groups[0].Mode != "dry" || g.Groups[0].Conversation != "qq:group:***01" {
		t.Fatalf("%+v", r)
	}
	for _, mode := range []string{"off", "dry"} {
		r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", mode}})
		if c := dataOf[ctlproto.GateChange](t, r); !r.OK || c.Conversation != "qq:group:***01" || c.Handle == "" {
			t.Fatalf("%s：%+v", mode, r)
		}
	}
	wantCalls(t, f, "gate-list", "gate-set qq:group:10001 off", "gate-set qq:group:10001 dry")
}

func TestAutoReceiveAndAutostart(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	for _, cmd := range []string{"auto-receive", "autostart"} {
		for _, v := range []string{"on", "off"} {
			r := ask(t, name, ctlproto.Request{Cmd: cmd, Args: []string{v}})
			if !r.OK || dataOf[ctlproto.Toggle](t, r).On != (v == "on") {
				t.Fatalf("%s %s：%+v", cmd, v, r)
			}
		}
	}
	wantCalls(t, f, "auto-receive true", "auto-receive false", "autostart true", "autostart false")
}

// quit：先回"正在退出"，客户端拿到回复以后后端才收到 Quit
func TestQuitRepliesBeforeQuitting(t *testing.T) {
	f := &fakeBackend{quitGate: make(chan struct{}), quitCalled: make(chan struct{})}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "quit"})
	if !r.OK || !dataOf[ctlproto.Quitting](t, r).Quitting {
		t.Fatalf("%+v", r)
	}
	// 回复已经到手，Quit 还卡在 quitGate 上：证明回复不等 Quit
	deadline := time.Now().Add(5 * time.Second)
	for len(f.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-f.quitCalled:
		t.Fatal("Quit 在放行之前就返回了")
	default:
	}
	close(f.quitGate)
	select {
	case <-f.quitCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("回复之后没有调 Quit")
	}
	wantCalls(t, f, "quit")
}

// ───────────── 协议：版本、格式、长度、超时 ─────────────

func TestProtocolErrorsNeverReachTheBackend(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	cases := []struct {
		what string
		line string
		code string
		exit int
	}{
		{"版本太新", `{"v":2,"cmd":"status"}`, ctlproto.CodeVersion, 4},
		{"版本是 0", `{"v":0,"cmd":"status"}`, ctlproto.CodeVersion, 4},
		{"没有版本", `{"cmd":"status"}`, ctlproto.CodeVersion, 4},
		{"版本不认识时多出来的字段不挡着回版本错", `{"v":9,"cmd":"status","future":true}`, ctlproto.CodeVersion, 4},
		{"不是 JSON", `status`, ctlproto.CodeInvalid, 3},
		{"数组", `[1]`, ctlproto.CodeInvalid, 3},
		{"字段类型不对", `{"v":1,"cmd":5}`, ctlproto.CodeInvalid, 3},
		{"多出不认识的字段", `{"v":1,"cmd":"status","sql":"select 1"}`, ctlproto.CodeInvalid, 3},
		{"不认识的命令", `{"v":1,"cmd":"sql"}`, ctlproto.CodeInvalid, 3},
		{"命令是空", `{"v":1,"cmd":""}`, ctlproto.CodeInvalid, 3},
		{"命令大小写变体", `{"v":1,"cmd":"STATUS"}`, ctlproto.CodeInvalid, 3},
		{"status 多带参数", `{"v":1,"cmd":"status","args":["x"]}`, ctlproto.CodeInvalid, 3},
		{"receive 没带子命令", `{"v":1,"cmd":"receive"}`, ctlproto.CodeInvalid, 3},
		{"receive 子命令不认识", `{"v":1,"cmd":"receive","args":["restart"]}`, ctlproto.CodeInvalid, 3},
		{"receive stop 带 wait", `{"v":1,"cmd":"receive","args":["stop"],"wait":"5s"}`, ctlproto.CodeInvalid, 3},
		{"status 带 wait", `{"v":1,"cmd":"status","wait":"5s"}`, ctlproto.CodeInvalid, 3},
		{"wait 写成不是时长", `{"v":1,"cmd":"receive","args":["start"],"wait":"很久"}`, ctlproto.CodeInvalid, 3},
		{"wait 是负数", `{"v":1,"cmd":"receive","args":["start"],"wait":"-5s"}`, ctlproto.CodeInvalid, 3},
		{"wait 是零", `{"v":1,"cmd":"receive","args":["start"],"wait":"0s"}`, ctlproto.CodeInvalid, 3},
		{"wait 太长", `{"v":1,"cmd":"receive","args":["start"],"wait":"6m"}`, ctlproto.CodeInvalid, 3},
		{"status 带 n", `{"v":1,"cmd":"status","n":5}`, ctlproto.CodeInvalid, 3},
		{"window 子命令不认识", `{"v":1,"cmd":"window","args":["hide"]}`, ctlproto.CodeInvalid, 3},
		{"logs 带位置参数", `{"v":1,"cmd":"logs","args":["../../x"]}`, ctlproto.CodeInvalid, 3},
		{"auto-receive 值是 true", `{"v":1,"cmd":"auto-receive","args":["true"]}`, ctlproto.CodeInvalid, 3},
		{"auto-receive 大小写变体", `{"v":1,"cmd":"auto-receive","args":["ON"]}`, ctlproto.CodeInvalid, 3},
		{"autostart 没带值", `{"v":1,"cmd":"autostart"}`, ctlproto.CodeInvalid, 3},
		{"quit 带参数", `{"v":1,"cmd":"quit","args":["now"]}`, ctlproto.CodeInvalid, 3},
		{"gate 没带子命令", `{"v":1,"cmd":"gate"}`, ctlproto.CodeInvalid, 3},
		{"gate 子命令不认识", `{"v":1,"cmd":"gate","args":["delete","qq:group:1"]}`, ctlproto.CodeInvalid, 3},
		{"gate set 少参数", `{"v":1,"cmd":"gate","args":["set","qq:group:10001"]}`, ctlproto.CodeInvalid, 3},
		{"gate set 多参数", `{"v":1,"cmd":"gate","args":["set","qq:group:10001","off","x"]}`, ctlproto.CodeInvalid, 3},
		{"gate list 带参数", `{"v":1,"cmd":"gate","args":["list","qq:group:10001"]}`, ctlproto.CodeInvalid, 3},
	}
	for _, c := range cases {
		r := raw(t, name, []byte(c.line))
		if r.OK || r.Code != c.code || ctlproto.ExitCode(r) != c.exit || r.Error == "" {
			t.Errorf("%s：%s\n得到 %+v（退出码 %d）", c.what, c.line, r, ctlproto.ExitCode(r))
		}
	}
	// 超过 64 KB 的请求
	big := append([]byte(`{"v":1,"cmd":"status","args":["`), make([]byte, ctlproto.MaxRequestBytes)...)
	for i := range big[len(`{"v":1,"cmd":"status","args":["`):] {
		big[len(`{"v":1,"cmd":"status","args":["`)+i] = 'a'
	}
	big = append(big, `"]}`...)
	if r := raw(t, name, big); r.OK || r.Code != ctlproto.CodeInvalid {
		t.Errorf("过长的请求：%+v", r)
	}
	wantCalls(t, f)
}

// 请求刚好 64 KB 以内（但命令不对）也按命令不对回，不是"太长"；刚好卡边界不 panic
func TestRequestLengthBoundary(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	prefix := `{"v":1,"cmd":"`
	for _, extra := range []int{-1, 0, 1} {
		cmd := strings.Repeat("a", ctlproto.MaxRequestBytes-len(prefix)-2+extra) // 加上 `"}` 刚好 MaxRequestBytes+extra
		line := prefix + cmd + `"}`
		r := raw(t, name, []byte(line))
		tooLong := len(line) > ctlproto.MaxRequestBytes
		if r.OK || r.Code != ctlproto.CodeInvalid || strings.Contains(r.Error, "KB") != tooLong {
			t.Errorf("长度 %d（上限 %d）：%+v", len(line), ctlproto.MaxRequestBytes, r)
		}
	}
	wantCalls(t, f)
}

// 对方连上以后什么也不发：读超时以后服务端自己断开，不占着
func TestIdleClientIsDropped(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{IOTimeout: 150 * time.Millisecond})
	c := dial(t, name)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	began := time.Now()
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("没发请求，服务端却给了数据")
	}
	if time.Since(began) > 3*time.Second {
		t.Fatalf("服务端 %v 才断开一个空闲连接", time.Since(began))
	}
	// 之后别的客户端照常能用
	if r := ask(t, name, ctlproto.Request{Cmd: "status"}); !r.OK {
		t.Fatalf("%+v", r)
	}
}

// ───────────── 后端的错误怎么变成回复 ─────────────

func TestBackendErrorsBecomeCodes(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	req := ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}}
	f.err = ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史")
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeReadOnly || ctlproto.ExitCode(r) != 1 {
		t.Fatalf("%+v", r)
	}
	f.err = errors.New("磁盘满了")
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed || r.Error != "磁盘满了" || ctlproto.ExitCode(r) != 1 {
		t.Fatalf("%+v", r)
	}
	f.err = &ctlproto.Error{Code: ctlproto.CodeTimeout, Msg: "等到时限", Data: ctlproto.Receive{Phase: "switching"}}
	r := ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "1s"})
	if r.OK || r.Code != ctlproto.CodeTimeout || dataOf[ctlproto.Receive](t, r).Phase != "switching" {
		t.Fatalf("失败时应带着当时的状态：%+v", r)
	}
	// 后端 panic：回 failed，服务端还活着
	f.err, f.panicOn = nil, "status"
	if r := ask(t, name, ctlproto.Request{Cmd: "status"}); r.OK || r.Code != ctlproto.CodeFailed {
		t.Fatalf("%+v", r)
	}
	f.panicOn = ""
	if r := ask(t, name, ctlproto.Request{Cmd: "status"}); !r.OK {
		t.Fatalf("panic 之后服务端不能用了：%+v", r)
	}
}

// 退出码表：每个码一行，其余命令行为都靠它
func TestExitCodes(t *testing.T) {
	for _, c := range []struct {
		r    ctlproto.Reply
		want int
	}{
		{ctlproto.Reply{OK: true}, 0},
		{ctlproto.Reply{Code: ctlproto.CodeFailed}, 1},
		{ctlproto.Reply{Code: ctlproto.CodeTimeout}, 1},
		{ctlproto.Reply{Code: ctlproto.CodeReadOnly}, 1},
		{ctlproto.Reply{Code: ctlproto.CodeNotRunning}, 2},
		{ctlproto.Reply{Code: ctlproto.CodeInvalid}, 3},
		{ctlproto.Reply{Code: ctlproto.CodeDenied}, 3},
		{ctlproto.Reply{Code: ctlproto.CodeDemo}, 3},
		{ctlproto.Reply{Code: ctlproto.CodeVersion}, 4},
		{ctlproto.Reply{Code: "没见过的码"}, 1},
		{ctlproto.Reply{OK: false}, 1},
	} {
		if got := ctlproto.ExitCode(c.r); got != c.want {
			t.Errorf("%+v 的退出码是 %d，应是 %d", c.r, got, c.want)
		}
	}
}

// ───────────── 演示模式 ─────────────

func TestDemoModeRefusesWrites(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{Demo: true})
	for _, req := range []ctlproto.Request{
		{Cmd: "receive", Args: []string{"start"}},
		{Cmd: "receive", Args: []string{"stop"}},
		{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}},
		{Cmd: "gate", Args: []string{"list"}},
		{Cmd: "auto-receive", Args: []string{"on"}},
	} {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo || ctlproto.ExitCode(r) != 3 {
			t.Errorf("%+v：%+v", req, r)
		}
	}
	// 演示里写错的参数仍然先按 invalid 回（speak 不会因为演示就进得来）
	if r := ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "speak"}}); r.Code != ctlproto.CodeInvalid {
		t.Errorf("演示里 speak：%+v", r)
	}
	wantCalls(t, f)
	// status、logs、window、quit 在演示里照常
	for _, req := range []ctlproto.Request{{Cmd: "status"}, {Cmd: "logs"}, {Cmd: "window", Args: []string{"show"}}} {
		if r := ask(t, name, req); !r.OK {
			t.Errorf("%+v：%+v", req, r)
		}
	}
}

// ───────────── Close：退出流程第一步 ─────────────

func TestCloseStopsAcceptingAndCancelsInFlight(t *testing.T) {
	f := &fakeBackend{blockStart: make(chan struct{})}
	name, srv := start(t, f, Options{})
	got := make(chan ctlproto.Reply, 1)
	go func() {
		got <- ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "60s"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	srv.Close()
	select {
	case r := <-got:
		if r.OK || r.Error != "桌面程序正在退出" {
			t.Fatalf("在途的请求应回正在退出：%+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 之后在途的请求没有收到回复")
	}
	if !srv.Wait(5 * time.Second) {
		t.Fatal("在途的处理没有做完")
	}
	// 新连接连不上
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, err := ctlproto.Dial(ctx, name); err == nil {
		c.Close()
		t.Fatal("Close 之后还连得上管道")
	}
	srv.Close() // 重复调用不出事
}

// Close 不等在途的处理：在界面线程里调也不会卡
func TestCloseDoesNotBlock(t *testing.T) {
	f := &fakeBackend{blockStart: make(chan struct{})}
	name, srv := start(t, f, Options{CloseGrace: 100 * time.Millisecond})
	// 在途的请求在另一个 goroutine 里问，回复通过通道交回来：测试结束前一定等到它。
	// 原来是 go ask(t, …)——测试函数一返回，t.Cleanup 就把客户端连接关了，这个 goroutine 还没读到回复就得到
	// "file already closed" 并在别的 goroutine 里调 t.Fatalf（单独跑 400 遍红 4～6 次）
	reply := make(chan ctlproto.Reply, 1)
	go func() {
		c := dial(t, name)
		c.SetDeadline(time.Now().Add(10 * time.Second))
		b, _ := json.Marshal(ctlproto.Request{V: ctlproto.Version, Cmd: "receive", Args: []string{"start"}, Wait: "60s"})
		c.Write(append(b, '\n'))
		line, err := ctlproto.ReadLine(bufio.NewReader(c), 1<<20)
		var r ctlproto.Reply
		if err != nil {
			r.Error = err.Error()
		} else if json.Unmarshal(line, &r) != nil {
			r.Error = "回复不是 JSON：" + string(line)
		}
		reply <- r
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() { srv.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close 被在途的处理卡住了")
	}
	// Close 以后在途的请求收到“正在退出”，而不是被硬断开
	select {
	case r := <-reply:
		if r.OK || r.Error != "桌面程序正在退出" {
			t.Fatalf("在途的请求应收到“桌面程序正在退出”：%+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 以后在途的请求没有收到回复")
	}
}
