package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// fakeServer 是接在 net.Pipe 另一头的假桌面程序：读一个请求（记下来），按 reply 回一个（reply 为 nil 时读完不回，直接等到测试结束）
type fakeServer struct {
	mu     sync.Mutex
	reqs   []ctlproto.Request
	reply  func(ctlproto.Request) string // 回复的整行；空串 = 不回复就关连接
	hang   bool                          // 读完请求以后不回也不关
	demos  []bool
	finish chan struct{}
}

func newFakeServer(t *testing.T, reply func(ctlproto.Request) string) *fakeServer {
	f := &fakeServer{reply: reply, finish: make(chan struct{})}
	t.Cleanup(func() { close(f.finish) })
	return f
}

func (f *fakeServer) dial(_ context.Context, demo bool) (net.Conn, error) {
	c, s := net.Pipe()
	f.mu.Lock()
	f.demos = append(f.demos, demo)
	f.mu.Unlock()
	go func() {
		defer s.Close()
		line, err := ctlproto.ReadLine(bufio.NewReader(s), 1<<20)
		if err != nil {
			return
		}
		var req ctlproto.Request
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		if f.hang {
			<-f.finish
			return
		}
		if out := f.reply(req); out != "" {
			s.Write([]byte(out + "\n"))
		}
	}()
	return c, nil
}

func (f *fakeServer) only(t *testing.T) ctlproto.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 1 {
		t.Fatalf("应该恰好发了一个请求，发了 %d 个：%+v", len(f.reqs), f.reqs)
	}
	return f.reqs[0]
}

func okReply(data string) func(ctlproto.Request) string {
	return func(ctlproto.Request) string {
		if data == "" {
			return `{"ok":true}`
		}
		return `{"ok":true,"data":` + data + `}`
	}
}

func runWith(t *testing.T, f *fakeServer, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb, f.dial)
	return code, out.String(), errb.String()
}

// ───────────── 参数 → 请求：客户端只拆命令行，不替服务端判断哪个参数合法 ─────────────

var yesFlag, noFlag = true, false

func TestArgsBecomeRequests(t *testing.T) {
	const v = ctlproto.Version
	cases := []struct {
		args []string
		want ctlproto.Request
	}{
		{[]string{"status"}, ctlproto.Request{V: v, Cmd: "status"}},
		{[]string{"--json", "status"}, ctlproto.Request{V: v, Cmd: "status"}},
		{[]string{"status", "--json"}, ctlproto.Request{V: v, Cmd: "status"}},
		{[]string{"receive", "start"}, ctlproto.Request{V: v, Cmd: "receive", Args: []string{"start"}}},
		{[]string{"receive", "start", "--wait"}, ctlproto.Request{V: v, Cmd: "receive", Args: []string{"start"}, Wait: "1m0s"}},
		{[]string{"receive", "--wait=90s", "start"}, ctlproto.Request{V: v, Cmd: "receive", Args: []string{"start"}, Wait: "1m30s"}},
		{[]string{"receive", "stop"}, ctlproto.Request{V: v, Cmd: "receive", Args: []string{"stop"}}},
		{[]string{"window", "show"}, ctlproto.Request{V: v, Cmd: "window", Args: []string{"show"}}},
		{[]string{"logs"}, ctlproto.Request{V: v, Cmd: "logs"}},
		{[]string{"logs", "-n", "20"}, ctlproto.Request{V: v, Cmd: "logs", N: 20}},
		{[]string{"logs", "-n=7"}, ctlproto.Request{V: v, Cmd: "logs", N: 7}},
		{[]string{"logs", "-n", "0"}, ctlproto.Request{V: v, Cmd: "logs", N: -1}}, // 0 在请求里是"没给"，写了 -n 0 交给服务端拒
		{[]string{"logs", "-n", "-5"}, ctlproto.Request{V: v, Cmd: "logs", N: -1}},
		{[]string{"gate", "list"}, ctlproto.Request{V: v, Cmd: "gate", Args: []string{"list"}}},
		{[]string{"gate", "set", "qq:group:10001", "off"}, ctlproto.Request{V: v, Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}}},
		// 客户端不拦 speak：原样发出去，拒不拒是服务端的事（这样客户端和服务端不会各有一套规矩）
		{[]string{"gate", "set", "qq:group:10001", "speak"}, ctlproto.Request{V: v, Cmd: "gate", Args: []string{"set", "qq:group:10001", "speak"}}},
		{[]string{"gate", "set", "qq:group:10001", "SPEAK"}, ctlproto.Request{V: v, Cmd: "gate", Args: []string{"set", "qq:group:10001", "SPEAK"}}},
		// 句柄和打码的写法也原样发：认不认是服务端的事
		{[]string{"gate", "set", "ab12cd34", "dry"}, ctlproto.Request{V: v, Cmd: "gate", Args: []string{"set", "ab12cd34", "dry"}}},
		{[]string{"gate", "set", "qq:group:***01", "dry"}, ctlproto.Request{V: v, Cmd: "gate", Args: []string{"set", "qq:group:***01", "dry"}}},
		{[]string{"auto-receive", "on"}, ctlproto.Request{V: v, Cmd: "auto-receive", Args: []string{"on"}}},
		{[]string{"autostart", "off"}, ctlproto.Request{V: v, Cmd: "autostart", Args: []string{"off"}}},
		{[]string{"quit"}, ctlproto.Request{V: v, Cmd: "quit"}},
		{[]string{"sql", "select 1"}, ctlproto.Request{V: v, Cmd: "sql", Args: []string{"select 1"}}}, // 不认识的命令也原样发
		{[]string{"--", "status"}, ctlproto.Request{V: v, Cmd: "status"}},
		// 定期任务的日程选项：原样放进 loop 字段，合不合格是服务端的事
		{[]string{"loop", "add", "--daily", "09:00", "早安"}, ctlproto.Request{V: v, Cmd: "loop", Args: []string{"add", "早安"}, Loop: &ctlproto.LoopArgs{Daily: "09:00"}}},
		{[]string{"loop", "add", "--weekly=1,3", "--at", "18:30", "--new-chat", "周报"}, ctlproto.Request{V: v, Cmd: "loop", Args: []string{"add", "周报"}, Loop: &ctlproto.LoopArgs{Weekly: "1,3", At: "18:30", NewChat: &yesFlag}}},
		{[]string{"loop", "edit", "3", "--every", "2h", "--same-chat", "--notify", "errors"}, ctlproto.Request{V: v, Cmd: "loop", Args: []string{"edit", "3"}, Loop: &ctlproto.LoopArgs{Every: "2h", NewChat: &noFlag, Notify: "errors"}}},
		{[]string{"loop", "edit", "3", "--self"}, ctlproto.Request{V: v, Cmd: "loop", Args: []string{"edit", "3"}, Loop: &ctlproto.LoopArgs{Self: true}}},
	}
	for _, c := range cases {
		f := newFakeServer(t, okReply(`{}`))
		runWith(t, f, c.args...)
		if got := f.only(t); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q → %+v，应是 %+v", c.args, got, c.want)
		}
	}
}

func TestDemoFlagPicksTheDemoPipe(t *testing.T) {
	f := newFakeServer(t, okReply(`{}`))
	runWith(t, f, "--demo", "status")
	runWith(t, f, "status")
	if len(f.demos) != 2 || !f.demos[0] || f.demos[1] {
		t.Fatalf("--demo 只该让第一次连演示的那条管道：%v", f.demos)
	}
}

// ───────────── 回复 → 退出码、--json、给人看的输出 ─────────────

func TestExitCodesFollowReplyCodes(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  int
	}{
		{`{"ok":true,"data":{}}`, 0},
		{`{"ok":false,"error":"x","code":"failed"}`, 1},
		{`{"ok":false,"error":"x","code":"timeout"}`, 1},
		{`{"ok":false,"error":"x","code":"readonly"}`, 1},
		{`{"ok":false,"error":"x","code":"invalid"}`, 3},
		{`{"ok":false,"error":"x","code":"denied"}`, 3},
		{`{"ok":false,"error":"x","code":"demo"}`, 3},
		{`{"ok":false,"error":"x","code":"version"}`, 4},
		{`{"ok":false,"error":"没见过的码","code":"???"}`, 1},
	} {
		f := newFakeServer(t, func(ctlproto.Request) string { return c.reply })
		if code, _, _ := runWith(t, f, "window", "show"); code != c.want {
			t.Errorf("%s → 退出码 %d，应是 %d", c.reply, code, c.want)
		}
	}
}

func TestJSONOutputIsOneObject(t *testing.T) {
	f := newFakeServer(t, okReply(`{"phase":"archive","pid":4242}`))
	code, out, errs := runWith(t, f, "status", "--json")
	if code != 0 || errs != "" || strings.Count(out, "\n") != 1 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	var r struct {
		OK   bool
		Data struct{ Phase string }
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || !r.OK || r.Data.Phase != "archive" {
		t.Fatalf("%v %q", err, out)
	}
	// 失败时 --json 也把回复打在 stdout，退出码非零
	f = newFakeServer(t, func(ctlproto.Request) string { return `{"ok":false,"error":"只读","code":"readonly"}` })
	code, out, errs = runWith(t, f, "--json", "gate", "set", "qq:group:1", "off")
	var e ctlproto.Reply
	if code != 1 || json.Unmarshal([]byte(out), &e) != nil || e.OK || e.Code != "readonly" || errs != "" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestStatusLocalOnlyPhase(t *testing.T) {
	f := newFakeServer(t, okReply(`{"mode":"receive","phase":"local","reason":"本地可用（QQ 没开）；新建了数据目录 D:\\编的目录","account":"","accounts":0,"noRecv":"配置里没有 QQ 小号，现在只开了本地","alerts":[],"version":"v1","pid":4242,"runningTurns":0}`))
	code, out, errs := runWith(t, f, "status")
	if code != 0 || errs != "" {
		t.Fatalf("status 失败：%d %q", code, errs)
	}
	for _, want := range []string{"阶段：本地可用（QQ 没开）（local）", "说明：本地可用（QQ 没开）；新建了数据目录 D:\\编的目录", "不能开始接收：配置里没有 QQ 小号，现在只开了本地"} {
		if !strings.Contains(out, want) {
			t.Errorf("status 的输出里没有 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "接收中") || strings.Contains(out, "接收用的小号") {
		t.Errorf("本地可用不该显示接收中：\n%s", out)
	}
}

func TestHumanOutput(t *testing.T) {
	f := newFakeServer(t, okReply(`{"mode":"archive","phase":"archive","reason":"只看历史","account":"***01","connection":"未连接 QQ","alerts":[{"title":"连接断了","body":"重连中"}],"autoReceive":true,"autostart":false,"dataDir":"D:\\数据","version":"v1","pid":4242,"runningTurns":2}`))
	code, out, _ := runWith(t, f, "status")
	for _, want := range []string{"阶段：只看历史（archive）", "接收用的小号：***01", "连接断了：重连中", "启动后自动接收：开；开机自动启动：关", "进程号：4242", "正在跑的回合：2", `数据目录：D:\数据`} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("status 的输出里没有 %q：\n%s", want, out)
		}
	}
	f = newFakeServer(t, okReply(`{"groups":[{"conversation":"qq:group:***01","handle":"ab12cd34","name":"编的测试群","mode":"speak","source":"own","actual":"dry"},{"conversation":"qq:group:***02","handle":"0f0f0f0f","handleClash":true,"mode":"dry","source":"default"}]}`))
	_, out, _ = runWith(t, f, "gate", "list")
	for _, want := range []string{"qq:group:***01  句柄 ab12cd34  说话（speak）（来源：这个群自己的设置）  实际在用：试运行（dry）  群名：\"编的测试群\"", "qq:group:***02  句柄 0f0f0f0f  试运行（dry）（来源：没设过，用默认）  句柄撞了，gate set 要写完整群号"} {
		if !strings.Contains(out, want) {
			t.Errorf("gate list 的输出里没有 %q：\n%s", want, out)
		}
	}
	f = newFakeServer(t, okReply(`{"conversation":"qq:group:***01","handle":"ab12cd34","before":"speak","after":"off"}`))
	if _, out, _ = runWith(t, f, "gate", "set", "ab12cd34", "off"); !strings.Contains(out, "qq:group:***01（句柄 ab12cd34）的档位：说话（speak） → 只记录（off）") {
		t.Errorf("gate set：%s", out)
	}
	f = newFakeServer(t, okReply(`{"lines":["甲","乙"]}`))
	if _, out, _ = runWith(t, f, "logs"); out != "甲\n乙\n" {
		t.Errorf("logs 应原样一行一行：%q", out)
	}
	f = newFakeServer(t, okReply(`{"phase":"receiving","mode":"receive","receiving":true,"settled":true}`))
	if _, out, _ = runWith(t, f, "receive", "start", "--wait"); !strings.Contains(out, "接收中") {
		t.Errorf("receive start：%s", out)
	}
	f = newFakeServer(t, okReply(`{"phase":"local","mode":"receive","reason":"现在没在接收 QQ（只开了本地）","settled":true}`))
	stopCode, stopOut, stopErrs := runWith(t, f, "receive", "stop")
	if stopCode != 0 || stopErrs != "" || strings.TrimSpace(stopOut) != "现在没在接收 QQ（只开了本地）" {
		t.Errorf("本地可用时 receive stop：%d %q %q", stopCode, stopOut, stopErrs)
	}
	f = newFakeServer(t, func(ctlproto.Request) string {
		return `{"ok":false,"error":"开始接收失败：x","code":"failed","data":{"phase":"archive","mode":"archive","reason":"开始接收失败：x"}}`
	})
	code, out, errs := runWith(t, f, "receive", "start")
	if code != 1 || out != "" || !strings.Contains(errs, "失败（failed）：开始接收失败：x") || !strings.Contains(errs, "现在的阶段：只看历史（archive）") {
		t.Errorf("失败要写在 stderr 并带当时的阶段：%d %q %q", code, out, errs)
	}
}

// ───────────── 验收 5：程序没开着，2 秒内退出码 2 ─────────────

func TestNotRunningExitsWithin2Seconds(t *testing.T) {
	name := `\\.\pipe\sumika-ctl-test-nobody-listens-here`
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	for _, args := range [][]string{{"status"}, {"--json", "status"}, {"quit"}, {"receive", "start", "--wait"}} {
		var out, errb bytes.Buffer
		began := time.Now()
		code := run(args, &out, &errb, dial)
		if code != ctlproto.ExitNotRunning || time.Since(began) > 2*time.Second {
			t.Errorf("%q：退出码 %d，用了 %v", args, code, time.Since(began))
		}
		if args[0] == "--json" {
			var r ctlproto.Reply
			if json.Unmarshal(out.Bytes(), &r) != nil || r.Code != ctlproto.CodeNotRunning || r.OK {
				t.Errorf("--json 也要给一个 JSON 对象：%q", out.String())
			}
		} else if !strings.Contains(errb.String(), "Sumika 没在运行") {
			t.Errorf("提示里要说没在运行：%q", errb.String())
		}
	}
}

// "没在运行"的提示里不能带管道名：里面有当前用户的 SID。名字里放一个编的 SID，真连一次不存在的管道，人读的和 --json 的输出都不许出现
func TestNotRunningMessageHasNoPipeNameOrSID(t *testing.T) {
	name := `\\.\pipe\sumika-ctl-S-1-5-21-111-222-333-1000-test-nobody-listens`
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	for _, args := range [][]string{{"status"}, {"--json", "status"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb, dial); code != ctlproto.ExitNotRunning {
			t.Fatalf("%q：退出码 %d", args, code)
		}
		all := out.String() + errb.String()
		for _, leak := range []string{"S-1-5-", "sumika-ctl-", "pipe", "111-222-333"} {
			if strings.Contains(all, leak) {
				t.Errorf("%q：输出里不该有 %q：%q", args, leak, all)
			}
		}
		if !strings.Contains(all, "Sumika 没在运行") || !strings.Contains(all, "错误号") {
			t.Errorf("%q：应说没在运行并带错误号：%q", args, all)
		}
	}
}

func TestDialErrorOfAnyKindMeansNotRunning(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"status"}, &out, &errb, func(context.Context, bool) (net.Conn, error) { return nil, errors.New("别的错误") })
	if code != ctlproto.ExitNotRunning {
		t.Fatalf("%d", code)
	}
}

// 管道在、但不让用（权限，或者另一头不是当前用户的进程）：denied，退出码 3，文字里不说"没在运行"
// （Agent 读到"没在运行"会以为程序没开，去重试或叫主人开程序；其实是管道被别人占了）
func TestDeniedDialIsNotNotRunning(t *testing.T) {
	denied := func(context.Context, bool) (net.Conn, error) {
		return nil, fmt.Errorf("连管道：%w", &ctlproto.DeniedError{Msg: "没有权限打开控制接口的管道"})
	}
	var out, errb bytes.Buffer
	if code := run([]string{"status"}, &out, &errb, denied); code != ctlproto.ExitDenied || strings.Contains(errb.String(), "没在运行") || !strings.Contains(errb.String(), "denied") {
		t.Errorf("人读的输出：%d %q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	code := run([]string{"--json", "gate", "set", "qq:group:10001", "off"}, &out, &errb, denied)
	var r ctlproto.Reply
	if json.Unmarshal(out.Bytes(), &r) != nil || r.OK || r.Code != ctlproto.CodeDenied || code != ctlproto.ExitDenied || !strings.Contains(r.Error, "没有权限") {
		t.Errorf("--json：%d %q", code, out.String())
	}
}

// ───────────── 坏情况：不回复、断开、乱回复、参数写错 ─────────────

func TestReplyTimeoutAndBrokenReplies(t *testing.T) {
	old, oldSlack := replyLimit, waitSlack
	replyLimit, waitSlack = 150*time.Millisecond, 50*time.Millisecond
	defer func() { replyLimit, waitSlack = old, oldSlack }()

	f := newFakeServer(t, nil)
	f.hang = true
	began := time.Now()
	code, _, errs := runWith(t, f, "status")
	if code != ctlproto.ExitFailed || !strings.Contains(errs, "没有收到回复") || time.Since(began) > 3*time.Second {
		t.Errorf("不回复：%d %q %v", code, errs, time.Since(began))
	}
	// --wait 的等待时间 = wait + slack，不是默认的 replyLimit
	f2 := newFakeServer(t, nil)
	f2.hang = true
	began = time.Now()
	code, _, _ = runWith(t, f2, "receive", "start", "--wait=400ms")
	if code != ctlproto.ExitFailed || time.Since(began) < 400*time.Millisecond {
		t.Errorf("--wait=400ms 应至少等到 400ms：%d %v", code, time.Since(began))
	}

	f3 := newFakeServer(t, func(ctlproto.Request) string { return "" }) // 读完请求直接关
	if code, _, errs := runWith(t, f3, "quit"); code != ctlproto.ExitFailed || !strings.Contains(errs, "断开") {
		t.Errorf("断开：%d %q", code, errs)
	}
	f4 := newFakeServer(t, func(ctlproto.Request) string { return "这不是 JSON" })
	if code, _, errs := runWith(t, f4, "status"); code != ctlproto.ExitFailed || !strings.Contains(errs, "JSON") {
		t.Errorf("乱回复：%d %q", code, errs)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"--bogus", "status"}, {"-x"}, {"status", "-z"}, {"--wait=abc", "status"}, {"--wait=-1s", "receive", "start"},
		{"logs", "-n"}, {"logs", "-n", "abc"}, {"logs", "-n=1.5"},
	} {
		f := newFakeServer(t, okReply(`{}`))
		code, out, errs := runWith(t, f, args...)
		if code != ctlproto.ExitDenied || out != "" || !strings.Contains(errs, "用法") {
			t.Errorf("%q：%d %q %q", args, code, out, errs)
		}
		if len(f.reqs) != 0 {
			t.Errorf("%q：用法错误不该发请求", args)
		}
	}
	// 加了 --json 的调用方（Agent）只读 stdout：命令行写错了也要给一个 JSON 对象（invalid），不是空的；退出码仍是 3，stderr 不另写
	for _, args := range [][]string{
		{"--json"}, {"--json", "--bogus", "status"}, {"--bogus", "--json", "status"}, {"status", "-z", "--json"},
		{"--json", "--wait=abc", "status"}, {"--json", "logs", "-n", "abc"}, {"logs", "-n", "--json"},
	} {
		f := newFakeServer(t, okReply(`{}`))
		code, out, errs := runWith(t, f, args...)
		var r ctlproto.Reply
		if err := json.Unmarshal([]byte(out), &r); err != nil || r.OK || r.Code != ctlproto.CodeInvalid || r.Error == "" {
			t.Errorf("%q：stdout 应是一个 invalid 的 JSON 对象：%q（%v）", args, out, err)
		}
		if code != ctlproto.ExitDenied || errs != "" || len(f.reqs) != 0 || strings.Count(out, "\n") != 1 {
			t.Errorf("%q：退出码 %d，stderr %q，请求 %d 个", args, code, errs, len(f.reqs))
		}
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"status", "--help"}} {
		f := newFakeServer(t, okReply(`{}`))
		if code, out, _ := runWith(t, f, args...); code != 0 || !strings.Contains(out, "sumika-ctl") || len(f.reqs) != 0 {
			t.Errorf("%q：%d", args, code)
		}
	}
}

// 帮助里每个命令都写着，而且说明了 speak 为什么不在这里
func TestUsageMentionsEveryCommand(t *testing.T) {
	for _, c := range []string{"status", "receive start", "receive stop", "window show", "logs", "gate list", "gate set", "auto-receive", "autostart", "quit", "--json", "speak"} {
		if !strings.Contains(usage, c) {
			t.Errorf("帮助里没有 %q", c)
		}
	}
}

// status 里守护进程那一栏：在、已停止拉起、没有，以及最近一次异常退出
func TestHumanOutputWatchdog(t *testing.T) {
	// 状态里的时间按本机时区显示：期望也按本机时区算，CI 机器在 UTC
	at, _ := time.Parse(time.RFC3339, "2026-11-13T19:37:00+08:00")
	incidentLocal := at.Local().Format("2006-01-02 15:04:05")
	base := `"phase":"archive","mode":"archive","alerts":[],"autoReceive":false,"autostart":true,"version":"v1","pid":1`
	for name, c := range map[string]struct {
		wd   string
		want []string
		not  []string
	}{
		"在": {`{"running":true,"supervised":true,"pid":77,"state":"running","restarts":2,"lastIncidentAt":"2026-11-13T19:37:00+08:00","lastReason":"进程退出，退出码 2","lastFile":"2026-11-13_193700.md"}`,
			[]string{"守护进程：在（进程 77，这一轮已重新拉起 2 次）", "最近一次异常退出：" + incidentLocal + "，进程退出，退出码 2（记录 logs\\incidents\\2026-11-13_193700.md）"}, nil},
		"放弃了": {`{"running":false,"supervised":false,"state":"gave-up","restarts":3,"lastIncidentAt":"2026-11-13T19:37:00+08:00","lastReason":"卡死","lastFile":"x.md"}`,
			[]string{"守护进程：已停止拉起（连续异常退出太多次）", "最近一次异常退出"}, nil},
		"直接启动": {`{"running":false,"supervised":false,"restarts":0}`,
			[]string{"守护进程：没有（桌面程序是直接启动的，崩了没人拉起"}, []string{"最近一次异常退出"}},
		"旧状态": {`{"running":false,"supervised":true,"state":"running","restarts":0}`,
			[]string{"守护进程：状态文件太旧，可能已经不在了"}, nil},
	} {
		f := newFakeServer(t, okReply(`{`+base+`,"watchdog":`+c.wd+`}`))
		code, out, _ := runWith(t, f, "status")
		for _, want := range c.want {
			if code != 0 || !strings.Contains(out, want) {
				t.Errorf("%s：status 的输出里没有 %q：\n%s", name, want, out)
			}
		}
		for _, bad := range c.not {
			if strings.Contains(out, bad) {
				t.Errorf("%s：不该出现 %q：\n%s", name, bad, out)
			}
		}
	}
	// 旧版桌面程序（回复里没有这一栏）：什么也不多写
	f := newFakeServer(t, okReply(`{`+base+`}`))
	if _, out, _ := runWith(t, f, "status"); strings.Contains(out, "守护进程") {
		t.Errorf("没有这一栏就不写：\n%s", out)
	}
}

// status 的"自检："一行：有就显示后端给的那句，没有（后台没建好）不显示（规格-运行时自检 第七节）
func TestStatusSelfcheckLine(t *testing.T) {
	f := newFakeServer(t, okReply(`{"mode":"archive","phase":"archive","version":"v1","pid":1,"selfcheck":"2 项异常（intake.unaccounted、turn.stuck），详见 D:/数据/selfcheck/2026-11-13.json"}`))
	code, out, _ := runWith(t, f, "status")
	if code != 0 || !strings.Contains(out, "自检：2 项异常（intake.unaccounted、turn.stuck），详见") {
		t.Errorf("status 应显示自检一行：\n%s", out)
	}
	f = newFakeServer(t, okReply(`{"mode":"archive","phase":"archive","version":"v1","pid":1}`))
	if _, out, _ = runWith(t, f, "status"); strings.Contains(out, "自检：") {
		t.Errorf("后端没给自检就不该显示这一行：\n%s", out)
	}
}
