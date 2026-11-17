package ctlserver

import (
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// loop add|edit 的日程选项：形状不对的全部 invalid，后端一个函数都没调到。
func TestLoopOptionShapeTable(t *testing.T) {
	add := []string{"add", "早安"}
	rows := []struct {
		what string
		req  ctlproto.Request
	}{
		{"选项用在 goal 上", ctlproto.Request{Cmd: "goal", Args: []string{"show"}, Loop: &ctlproto.LoopArgs{Self: true}}},
		{"选项用在 loop list 上", ctlproto.Request{Cmd: "loop", Args: []string{"list"}, Loop: &ctlproto.LoopArgs{Self: true}}},
		{"选项用在 loop pause 上", ctlproto.Request{Cmd: "loop", Args: []string{"pause", "1"}, Loop: &ctlproto.LoopArgs{Notify: "none"}}},
		{"选项用在 status 上", ctlproto.Request{Cmd: "status", Loop: &ctlproto.LoopArgs{Self: true}}},
		{"两种日程", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Every: "30m", Daily: "09:00"}}},
		{"间隔单位不对", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Every: "30s"}}},
		{"间隔不是数字", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Every: "abcm"}}},
		{"间隔过长的数", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Every: strings.Repeat("9", 7) + "m"}}},
		{"每天时间越界", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Daily: "24:00"}}},
		{"每天时间少个零", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Daily: "9:00"}}},
		{"每周没写几点", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Weekly: "1,3"}}},
		{"每周星期是 0", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Weekly: "0,1", At: "09:00"}}},
		{"每周星期是 8", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Weekly: "8", At: "09:00"}}},
		{"每周星期末尾逗号", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Weekly: "1,", At: "09:00"}}},
		{"只写 at", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{At: "09:00"}}},
		{"通知档不对", ctlproto.Request{Cmd: "loop", Args: add, Loop: &ctlproto.LoopArgs{Notify: "loud"}}},
		{"add 带选项但没写任务", ctlproto.Request{Cmd: "loop", Args: []string{"add"}, Loop: &ctlproto.LoopArgs{Self: true}}},
		{"edit 带空选项又没写文字", ctlproto.Request{Cmd: "loop", Args: []string{"edit", "3"}, Loop: &ctlproto.LoopArgs{}}},
		{"edit 编号不对", ctlproto.Request{Cmd: "loop", Args: []string{"edit", "x", "改"}, Loop: &ctlproto.LoopArgs{Self: true}}},
		{"edit 文字带控制字符", ctlproto.Request{Cmd: "loop", Args: []string{"edit", "3", "改\x1b[2J"}, Loop: &ctlproto.LoopArgs{Self: true}}},
	}
	f := &slashFake{}
	name, _ := start(t, f, Options{})
	for _, r := range rows {
		got := ask(t, name, r.req)
		if got.OK || got.Code != ctlproto.CodeInvalid || ctlproto.ExitCode(got) != 3 {
			t.Errorf("%s：应 invalid，得到 %+v", r.what, got)
		}
	}
	wantCalls(t, &f.fakeBackend)
}

// 合格的选项换成后端用的形状：星期周日 7 → 0，间隔换成秒，文字原样给后端，不拼斜杠命令。
func TestLoopOptionsReachBackend(t *testing.T) {
	yes := true
	cases := []struct {
		what string
		req  ctlproto.Request
		want SlashRequest
	}{
		{"每天", ctlproto.Request{Cmd: "loop", Args: []string{"add", "早安", "问候"}, Loop: &ctlproto.LoopArgs{Daily: "09:30"}},
			SlashRequest{Command: "loop", Sub: "add", Text: "早安 问候", Loop: &ctlproto.LoopPatch{Kind: "daily", At: "09:30"}}},
		{"每周", ctlproto.Request{Cmd: "loop", Args: []string{"add", "周报"}, Loop: &ctlproto.LoopArgs{Weekly: "7,1,5", At: "18:00"}},
			SlashRequest{Command: "loop", Sub: "add", Text: "周报", Loop: &ctlproto.LoopPatch{Kind: "weekly", At: "18:00", Days: []int{0, 1, 5}}}},
		{"间隔加新聊天", ctlproto.Request{Cmd: "loop", Args: []string{"add", "看看"}, Loop: &ctlproto.LoopArgs{Every: "2h", NewChat: &yes}},
			SlashRequest{Command: "loop", Sub: "add", Text: "看看", Loop: &ctlproto.LoopPatch{Kind: "interval", IntervalSec: 7200, NewChat: &yes}}},
		{"edit 只改通知", ctlproto.Request{Cmd: "loop", Args: []string{"edit", "#4"}, Loop: &ctlproto.LoopArgs{Notify: "errors"}},
			SlashRequest{Command: "loop", Sub: "edit", ID: 4, Loop: &ctlproto.LoopPatch{Notify: "errors"}}},
		{"edit 改成自己定", ctlproto.Request{Cmd: "loop", Args: []string{"edit", "4", "新文字"}, Loop: &ctlproto.LoopArgs{Self: true}},
			SlashRequest{Command: "loop", Sub: "edit", ID: 4, Text: "新文字", Loop: &ctlproto.LoopPatch{Kind: "self"}}},
	}
	f := &slashFake{res: ctlproto.SlashResult{Reply: "好", Loops: []ctlproto.LoopTask{{ID: 1, Kind: "weekly\x1b", At: "09:00"}}}}
	name, _ := start(t, f, Options{})
	for _, c := range cases {
		got := ask(t, name, c.req)
		if !got.OK {
			t.Fatalf("%s 应通过：%+v", c.what, got)
		}
		f.smu.Lock()
		last := f.reqs[len(f.reqs)-1]
		f.smu.Unlock()
		if !reflect.DeepEqual(last, c.want) {
			t.Errorf("%s：后端收到 %+v %+v，应是 %+v %+v", c.what, last, last.Loop, c.want, c.want.Loop)
		}
		if d := dataOf[ctlproto.SlashResult](t, got); strings.Contains(d.Loops[0].Kind, "\x1b") {
			t.Errorf("%s：回来的任务字段要清掉控制字符", c.what)
		}
	}
}
