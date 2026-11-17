package ctlserver

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/ctlproto"
)

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logCapture) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// 只读的命令不写日志（Agent 会反复轮询）；会改状态的各记一行，只有命令名和子命令
func TestOnlyStateChangingRequestsAreLogged(t *testing.T) {
	lc := &logCapture{}
	f := &fakeBackend{}
	name, _ := start(t, f, Options{Logf: lc.logf})
	for _, req := range []ctlproto.Request{
		{Cmd: "status"}, {Cmd: "logs"}, {Cmd: "logs", N: 5}, {Cmd: "gate", Args: []string{"list"}},
	} {
		ask(t, name, req)
	}
	if got := lc.all(); got != "" {
		t.Fatalf("只读命令不该写日志：%q", got)
	}
	for _, req := range []ctlproto.Request{
		{Cmd: "receive", Args: []string{"start"}},
		{Cmd: "receive", Args: []string{"stop"}},
		{Cmd: "window", Args: []string{"show"}},
		{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}},
		{Cmd: "auto-receive", Args: []string{"on"}},
		{Cmd: "autostart", Args: []string{"off"}},
	} {
		ask(t, name, req)
	}
	want := "控制接口：receive start → 成功\n控制接口：receive stop → 成功\n控制接口：window show → 成功\n控制接口：gate set → 成功\n控制接口：auto-receive on → 成功\n控制接口：autostart off → 成功"
	if got := lc.all(); got != want {
		t.Fatalf("日志：\n%s\n应是\n%s", got, want)
	}
	if strings.Contains(lc.all(), "10001") {
		t.Fatal("日志里不该有群号")
	}
}

// 请求里别人给的文字不原样进日志：换行能伪造一行、冒充别的东西
func TestUntrustedTextNeverReachesTheLog(t *testing.T) {
	lc := &logCapture{}
	f := &fakeBackend{}
	name, _ := start(t, f, Options{Logf: lc.logf})
	forged := "status\n2026-11-01 12:00:00 后台状态：接收中\n"
	ask(t, name, ctlproto.Request{Cmd: forged})
	ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"start\n伪造的一行"}})
	ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set\r\n伪造", "qq:group:1", "off"}})
	got := lc.all()
	if strings.Contains(got, "伪造") || strings.Contains(got, "接收中") || strings.Count(got, "\n") != 2 {
		t.Fatalf("请求里的文字进了日志：%q", got)
	}
	if !strings.Contains(got, "收到不认识的命令") {
		t.Fatalf("不认识的命令应记一句固定的话：%q", got)
	}
}

// 被拒的请求也要记一笔，而且和成功的分得开：事后要查"有没有人试过让群开口"。日志里只有固定的词，没有群号、档位原文
func TestRejectedRequestsAreLoggedApartFromSuccessful(t *testing.T) {
	lc := &logCapture{}
	f := &fakeBackend{}
	name, _ := start(t, f, Options{Logf: lc.logf})

	ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "speak"}})
	ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", " SPEAK"}})
	ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:private:90001:80002", "off"}})
	ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}})
	f.err = ctlproto.Errorf(ctlproto.CodeReadOnly, "先开始接收")
	ask(t, name, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "dry"}})
	f.err = nil
	ask(t, name, ctlproto.Request{Cmd: "quit", Args: []string{"多余的"}})
	ask(t, name, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "999h"})

	want := "控制接口：gate set → 被拒（invalid）\n" +
		"控制接口：gate set → 被拒（invalid）\n" +
		"控制接口：gate set → 被拒（invalid）\n" +
		"控制接口：gate set → 成功\n" +
		"控制接口：gate set → 没做成（readonly）\n" +
		"控制接口：quit → 被拒（invalid）\n" +
		"控制接口：receive start → 被拒（invalid）"
	if got := lc.all(); got != want {
		t.Fatalf("日志：\n%s\n应是\n%s", got, want)
	}
	if got := lc.all(); strings.Contains(got, "speak") || strings.Contains(got, "10001") || strings.Contains(got, "90001") {
		t.Errorf("日志里不该有档位原文或会话号：%q", got)
	}
}

// 读不懂的请求（不是 JSON、版本不对、有不认识的字段、太长）在命令表之前就被拒了：也记一笔固定的话，不带请求原文
func TestUnreadableRequestsAreLogged(t *testing.T) {
	lc := &logCapture{}
	f := &fakeBackend{}
	name, _ := start(t, f, Options{Logf: lc.logf})
	raw(t, name, []byte("这不是 JSON\n伪造的一行"))
	raw(t, name, []byte(`{"v":99,"cmd":"status"}`))
	raw(t, name, []byte(`{"v":1,"cmd":"gate","args":["set","qq:group:1","speak"],"extra":"伪造"}`))
	raw(t, name, append([]byte(`{"v":1,"cmd":"`), make([]byte, ctlproto.MaxRequestBytes+10)...))
	got := lc.all()
	want := "控制接口：收到读不懂的请求 → 被拒（invalid）\n控制接口：收到读不懂的请求 → 被拒（version）\n控制接口：收到读不懂的请求 → 被拒（invalid）\n控制接口：收到读不懂的请求 → 被拒（invalid）"
	if got != want {
		t.Fatalf("日志：\n%s\n应是\n%s", got, want)
	}
	wantCalls(t, f)
}
