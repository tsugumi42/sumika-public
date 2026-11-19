package ctlserver

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

type proactiveFake struct {
	fakeBackend
	got []int
	rep ctlproto.ProactiveLog
	err error
}

func (f *proactiveFake) ProactiveLog(_ context.Context, n int) (ctlproto.ProactiveLog, error) {
	f.rec("proactive-log")
	f.got = append(f.got, n)
	return f.rep, f.err
}

// 后端收到的是校验过的行数（默认 50），回复原样交回，只读命令不写日志
func TestProactiveLogPassesN(t *testing.T) {
	f := &proactiveFake{rep: ctlproto.ProactiveLog{Rows: []ctlproto.ProactiveRow{{ID: 1, Character: "aa", Kind: "skip", Reason: "snoozed", Count: 1}}}}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "proactive", Args: []string{"log"}})
	if !r.OK || len(dataOf[ctlproto.ProactiveLog](t, r).Rows) != 1 {
		t.Fatalf("%+v", r)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "proactive", Args: []string{"log"}, N: 500}); !r.OK {
		t.Fatalf("500 行是上限，应放行：%+v", r)
	}
	if len(f.got) != 2 || f.got[0] != 50 || f.got[1] != 500 {
		t.Errorf("后端收到的行数不对：%v", f.got)
	}
	if len(logs) != 0 {
		t.Errorf("只读命令不该写日志：%v", logs)
	}
}

// 形状表：被拒的格子都不碰后端
func TestProactiveShapeTable(t *testing.T) {
	rows := []ctlproto.Request{
		{Cmd: "proactive"},
		{Cmd: "proactive", Args: []string{"set"}},
		{Cmd: "proactive", Args: []string{"snooze", "5"}},
		{Cmd: "proactive", Args: []string{"log", "x"}},
		{Cmd: "proactive", Args: []string{"LOG"}},
		{Cmd: "proactive", Args: []string{"log"}, N: -1},
		{Cmd: "proactive", Args: []string{"log"}, N: ctlproto.MaxLogLines + 1},
		{Cmd: "proactive", Args: []string{"log"}, Wait: "5s"},
		{Cmd: "proactive", Args: []string{"log"}, Text: "话"},
		{Cmd: "proactive", Args: []string{"log"}, New: true},
		{Cmd: "proactive", Args: []string{"log"}, Stats: &ctlproto.StatsArgs{Days: 1}},
		{Cmd: "proactive", Args: []string{"log"}, Link: &ctlproto.LinkArgs{}},
		{Cmd: "status", N: 5}, // -n 仍然只给 logs、proactive log、backfill gaps
	}
	f := &proactiveFake{}
	name, _ := start(t, f, Options{})
	for i, req := range rows {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("第 %d 格 %+v：应被拒（invalid），得到 %+v", i, req, r)
		}
	}
	wantCalls(t, &f.fakeBackend)
}

// 演示模式回 demo、后端没接回 failed、后端的带码错误原样传回
func TestProactiveModes(t *testing.T) {
	req := ctlproto.Request{Cmd: "proactive", Args: []string{"log"}}
	f := &proactiveFake{}
	name, _ := start(t, f, Options{Demo: true})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	wantCalls(t, &f.fakeBackend)
	name, _ = start(t, &fakeBackend{}, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("没接主动联系：%+v", r)
	}
	boom := &proactiveFake{err: ctlproto.Errorf(ctlproto.CodeFailed, "读主动联系日志出错")}
	name, _ = start(t, boom, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("后端出错：%+v", r)
	}
}

// 日志里的外来文字（备注）出去前清掉控制字符：换行、ESC 不能原样过去
func TestProactiveLogCleansForeignText(t *testing.T) {
	f := &proactiveFake{rep: ctlproto.ProactiveLog{
		States: []ctlproto.ProactiveState{{Character: "a\nb"}},
		Rows:   []ctlproto.ProactiveRow{{Character: "aa", Kind: "skip", Reason: "error", Note: "第一行\n[假的系统行]\x1b[2J", Thread: "t\r1"}},
	}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "proactive", Args: []string{"log"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	l := dataOf[ctlproto.ProactiveLog](t, r)
	n := l.Rows[0].Note
	if strings.ContainsAny(n, "\n\r\x1b") || !strings.Contains(n, "假的系统行") || strings.ContainsAny(l.Rows[0].Thread, "\r\n") || strings.ContainsAny(l.States[0].Character, "\r\n") {
		t.Fatalf("外来文字没清干净：%q %q %q", n, l.Rows[0].Thread, l.States[0].Character)
	}
}
