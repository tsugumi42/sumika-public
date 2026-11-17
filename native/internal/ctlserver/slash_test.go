package ctlserver

import (
	"context"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/ctlproto"
)

type slashFake struct {
	fakeBackend
	smu  sync.Mutex
	reqs []SlashRequest
	res  ctlproto.SlashResult
	err  error
}

func (f *slashFake) Slash(_ context.Context, req SlashRequest) (ctlproto.SlashResult, error) {
	f.rec("slash")
	f.smu.Lock()
	f.reqs = append(f.reqs, req)
	f.smu.Unlock()
	return f.res, f.err
}

// goal、loop 的形状表：每一格都真发一次请求，不合格的全部 invalid（退出码 3），后端一个函数都没调到。
func TestSlashShapeTable(t *testing.T) {
	long := strings.Repeat("长", ctlproto.MaxChatText+1)
	rows := []struct {
		what string
		req  ctlproto.Request
	}{
		{"goal 不认识的子命令", ctlproto.Request{Cmd: "goal", Args: []string{"nuke"}}},
		{"goal 子命令大小写变体", ctlproto.Request{Cmd: "goal", Args: []string{"SET", "x"}}},
		{"goal set 没写目标", ctlproto.Request{Cmd: "goal", Args: []string{"set"}}},
		{"goal set 全空白", ctlproto.Request{Cmd: "goal", Args: []string{"set", " ", "\t"}}},
		{"goal set 以斜杠开头（借目标再发命令）", ctlproto.Request{Cmd: "goal", Args: []string{"set", "/loop 1m 干活"}}},
		{"goal edit 以斜杠开头", ctlproto.Request{Cmd: "goal", Args: []string{"edit", "/goal clear"}}},
		{"goal set 超长", ctlproto.Request{Cmd: "goal", Args: []string{"set", long}}},
		{"goal set 带 ESC", ctlproto.Request{Cmd: "goal", Args: []string{"set", "做事\x1b[2J"}}},
		{"goal set 带 NUL", ctlproto.Request{Cmd: "goal", Args: []string{"set", "做事\x00"}}},
		{"goal pause 夹带参数", ctlproto.Request{Cmd: "goal", Args: []string{"pause", "现在"}}},
		{"goal show 夹带参数", ctlproto.Request{Cmd: "goal", Args: []string{"show", "x"}}},
		{"goal clear 夹带参数", ctlproto.Request{Cmd: "goal", Args: []string{"clear", "all"}}},
		{"goal 夹带 text", ctlproto.Request{Cmd: "goal", Args: []string{"show"}, Text: "你好"}},
		{"goal 夹带 new", ctlproto.Request{Cmd: "goal", Args: []string{"show"}, New: true}},
		{"goal 夹带 wait", ctlproto.Request{Cmd: "goal", Args: []string{"show"}, Wait: "5s"}},
		{"goal 角色带冒号", ctlproto.Request{Cmd: "goal", Args: []string{"show"}, Character: "a:b"}},
		{"goal 角色带斜杠", ctlproto.Request{Cmd: "goal", Args: []string{"show"}, Character: "../x"}},
		{"loop 不认识的子命令", ctlproto.Request{Cmd: "loop", Args: []string{"stop", "1"}}},
		{"loop add 没写任务", ctlproto.Request{Cmd: "loop", Args: []string{"add"}}},
		{"loop list 夹带参数", ctlproto.Request{Cmd: "loop", Args: []string{"list", "1"}}},
		{"loop pause 没写编号", ctlproto.Request{Cmd: "loop", Args: []string{"pause"}}},
		{"loop remove 没写编号", ctlproto.Request{Cmd: "loop", Args: []string{"remove"}}},
		{"loop remove 编号不是数字", ctlproto.Request{Cmd: "loop", Args: []string{"remove", "all"}}},
		{"loop remove 编号为 0", ctlproto.Request{Cmd: "loop", Args: []string{"remove", "0"}}},
		{"loop remove 编号为负", ctlproto.Request{Cmd: "loop", Args: []string{"remove", "-1"}}},
		{"loop remove 编号过长", ctlproto.Request{Cmd: "loop", Args: []string{"remove", strings.Repeat("9", 10)}}},
		{"loop resume 夹带多余字", ctlproto.Request{Cmd: "loop", Args: []string{"resume", "1", "2"}}},
		{"loop edit 没写要改的", ctlproto.Request{Cmd: "loop", Args: []string{"edit", "1"}}},
		{"loop add 超长", ctlproto.Request{Cmd: "loop", Args: []string{"add", long}}},
		{"loop add 带控制字符", ctlproto.Request{Cmd: "loop", Args: []string{"add", "1h", "干活\x1b]0;x\x07"}}},
		{"loop 夹带 n", ctlproto.Request{Cmd: "loop", Args: []string{"list"}, N: 3}},
		{"loop 夹带 cursor", ctlproto.Request{Cmd: "loop", Args: []string{"list"}, Cursor: new(string)}},
		{"别的命令夹带 character", ctlproto.Request{Cmd: "status", Character: "example"}},
	}
	f := &slashFake{}
	name, _ := start(t, f, Options{})
	for _, r := range rows {
		got := ask(t, name, r.req)
		if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != ctlproto.ExitDenied || got.Error == "" {
			t.Errorf("%s：应被拒（invalid、退出码 3），得到 %+v", r.what, got)
		}
	}
	wantCalls(t, &f.fakeBackend)
}

// 合格的写法：后端拿到拼好的斜杠命令全文、子命令名和编号；命令的回复和现状原样回到 data，外来文字清掉控制字符。
func TestSlashForwardsValidCommands(t *testing.T) {
	type tc struct {
		args []string
		want SlashRequest
	}
	goals := []tc{
		{nil, SlashRequest{Command: "goal", Sub: "show", Text: "/goal"}},
		{[]string{"show"}, SlashRequest{Command: "goal", Sub: "show", Text: "/goal"}},
		{[]string{"set", "建", "hello.txt", "并读回"}, SlashRequest{Command: "goal", Sub: "set", Text: "/goal 建 hello.txt 并读回"}},
		{[]string{"edit", "改成别的"}, SlashRequest{Command: "goal", Sub: "edit", Text: "/goal edit 改成别的"}},
		{[]string{"pause"}, SlashRequest{Command: "goal", Sub: "pause", Text: "/goal pause"}},
		{[]string{"resume"}, SlashRequest{Command: "goal", Sub: "resume", Text: "/goal resume"}},
		{[]string{"clear"}, SlashRequest{Command: "goal", Sub: "clear", Text: "/goal clear"}},
	}
	loops := []tc{
		{nil, SlashRequest{Command: "loop", Sub: "list", Text: "/loop"}},
		{[]string{"list"}, SlashRequest{Command: "loop", Sub: "list", Text: "/loop"}},
		{[]string{"add", "30m", "报一下现在几点"}, SlashRequest{Command: "loop", Sub: "add", Text: "/loop 30m 报一下现在几点"}},
		{[]string{"add", "自己定节奏"}, SlashRequest{Command: "loop", Sub: "add", Text: "/loop 自己定节奏"}},
		{[]string{"edit", "#3", "2h"}, SlashRequest{Command: "loop", Sub: "edit", ID: 3, Text: "/loop edit 3 2h"}},
		{[]string{"edit", "3", "改成", "新任务"}, SlashRequest{Command: "loop", Sub: "edit", ID: 3, Text: "/loop edit 3 改成 新任务"}},
		{[]string{"pause", "2"}, SlashRequest{Command: "loop", Sub: "pause", ID: 2, Text: "/loop pause 2"}},
		{[]string{"resume", "#2"}, SlashRequest{Command: "loop", Sub: "resume", ID: 2, Text: "/loop resume 2"}},
		{[]string{"remove", "7"}, SlashRequest{Command: "loop", Sub: "remove", ID: 7, Text: "/loop stop 7"}},
	}
	f := &slashFake{res: ctlproto.SlashResult{Reply: "好", Goal: &ctlproto.GoalState{Text: "目标\x1b[2J", Status: "active"}}}
	name, _ := start(t, f, Options{})
	check := func(cmd string, c tc) {
		got := ask(t, name, ctlproto.Request{Cmd: cmd, Args: c.args, Character: "example"})
		if !got.OK {
			t.Fatalf("%s %v 应通过：%+v", cmd, c.args, got)
		}
		want := c.want
		want.Character = "example"
		f.smu.Lock()
		last := f.reqs[len(f.reqs)-1]
		f.smu.Unlock()
		if last != want {
			t.Fatalf("%s %v：后端收到 %+v，应是 %+v", cmd, c.args, last, want)
		}
		d := dataOf[ctlproto.SlashResult](t, got)
		if d.Reply != "好" || d.Loops == nil || strings.Contains(d.Goal.Text, "\x1b") {
			t.Fatalf("data 不对（回复要在、loops 要是 []、外来文字要清）：%+v", d)
		}
	}
	for _, c := range goals {
		check("goal", c)
	}
	for _, c := range loops {
		check("loop", c)
	}
}

// 后端的带码错误原样变成回复；宿主没接扩展回 readonly；演示模式不碰后端。
func TestSlashBackendErrors(t *testing.T) {
	f := &slashFake{err: ctlproto.Errorf(ctlproto.CodeInvalid, "命令不合格：撞词")}
	name, _ := start(t, f, Options{})
	got := ask(t, name, ctlproto.Request{Cmd: "goal", Args: []string{"set", "pause"}})
	if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != 3 || !strings.Contains(got.Error, "撞词") {
		t.Fatalf("后端的 invalid 要原样回：%+v", got)
	}
	g := &fakeBackend{}
	name2, _ := start(t, g, Options{})
	if r := ask(t, name2, ctlproto.Request{Cmd: "loop", Args: []string{"list"}}); r.OK || r.Code != ctlproto.CodeReadOnly {
		t.Fatalf("没接扩展应回 readonly：%+v", r)
	}
	h := &slashFake{}
	name3, _ := start(t, h, Options{Demo: true})
	if r := ask(t, name3, ctlproto.Request{Cmd: "goal", Args: []string{"show"}}); r.OK || r.Code != ctlproto.CodeDemo {
		t.Fatalf("演示模式应回 demo：%+v", r)
	}
	wantCalls(t, &h.fakeBackend)
}
