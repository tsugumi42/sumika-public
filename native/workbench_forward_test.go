package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// 新界面的合并转发在右栏：不是模态窗，聊天和窗口按钮都还在；嵌套能返回；看完右栏恢复原样。
func TestWorkbenchForwardInSide(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local).UnixMilli()
	r := &forwardUIReader{views: map[string]api.ForwardView{
		"outer": {ID: "outer", State: "ready", Nodes: []api.ForwardNode{
			{UID: "100", Name: "编的甲", At: t0, Segments: []api.DisplaySegment{{Kind: "text", Text: "编的外层"}}},
			{UID: "101", Name: "编的丙", At: t0, Segments: []api.DisplaySegment{{Kind: "forward", Ref: "inner", Forward: &api.ForwardBrief{Title: "编的乙的聊天记录", Count: 1}}}},
		}},
		"inner": {ID: "inner", State: "ready", Nodes: []api.ForwardNode{{UID: "200", Name: "编的乙", At: t0, Segments: []api.DisplaySegment{{Kind: "text", Text: "编的里层"}}}}},
	}}
	q := &uiQueue{}
	a := shellApp()
	a.reader, a.postFn = r, q.post
	t.Cleanup(a.stopForward)
	a.winControls = &winshell.WindowControls{Minimize: func() {}, Toggle: func() {}, Close: func() {}}
	tt := shellTester(a, 2048, 1030)
	frames := func() {
		settleMem(q)
		tt.Frame()
		settleMem(q)
		tt.Frame()
	}
	if a.workbench.shell.st.Panes.SideOpen {
		t.Fatal("开始时右栏该关着")
	}
	a.openForward(api.EventRef{Conversation: a.keyOf(a.sel)}, "outer", nil)
	frames()
	if !a.workbench.shell.st.Panes.SideOpen {
		t.Fatal("看转发要打开右栏")
	}
	if !hasKey(tt, "forward-head") || !tt.HasText("编的外层") {
		t.Fatalf("转发内容该在右栏：%q", tt.Texts())
	}
	if !tt.HasText("今晚的月色真好") {
		t.Fatalf("聊天还该看得见：%q", tt.Texts())
	}
	if _, ok := tt.Find("关闭"); !ok && len(keysLike(tt, "window-")) == 0 {
		t.Fatalf("窗口按钮要一直在，不能被盖住：%q", tt.Texts())
	}
	for _, k := range uiBoxes(tt) {
		if k.Key == "forward-window" {
			t.Fatal("不该再是模态窗")
		}
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		out, _ := os.Create(filepath.Join(dir, "转发右栏.png"))
		png.Encode(out, tt.Image())
		out.Close()
	}
	if err := tt.Click("编的乙的聊天记录"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if !tt.HasText("编的里层") || tt.HasText("编的外层") {
		t.Fatalf("嵌套后该看里层：%q", tt.Texts())
	}
	if err := tt.Click("返回上一层"); err != nil {
		t.Fatal(err)
	}
	frames()
	if !tt.HasText("编的外层") {
		t.Fatalf("返回后外层丢了：%q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	frames()
	if a.forward.open || a.workbench.shell.st.Panes.SideOpen {
		t.Fatalf("Esc 关掉后右栏该恢复：open=%v side=%v", a.forward.open, a.workbench.shell.st.Panes.SideOpen)
	}
	// 用户自己把右栏关掉也算看完了；原来开着的不被关
	a.workbench.shell.st.Panes.SideOpen = true
	a.openForward(api.EventRef{Conversation: a.keyOf(a.sel)}, "outer", nil)
	frames()
	if err := tt.Click("关闭聊天记录"); err != nil {
		t.Fatal(err)
	}
	frames()
	if a.forward.open || !a.workbench.shell.st.Panes.SideOpen {
		t.Fatalf("原来开着的右栏要留着：open=%v side=%v", a.forward.open, a.workbench.shell.st.Panes.SideOpen)
	}
	// 换会话时收掉
	a.openForward(api.EventRef{Conversation: a.keyOf(a.sel)}, "outer", nil)
	frames()
	a.sel = "home:g1"
	frames()
	if a.forward.open {
		t.Fatal("换了会话还开着别的会话的转发")
	}
}

// 放不下三栏时合并转发在浮动右栏里：聊天区还在、嵌套能返回；Esc 先退一层、最外层再关；关掉后右栏恢复。
func TestWorkbenchForwardInFloatSide(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local).UnixMilli()
	r := &forwardUIReader{views: map[string]api.ForwardView{
		"outer": {ID: "outer", State: "ready", Nodes: []api.ForwardNode{
			{UID: "100", Name: "编的甲", At: t0, Segments: []api.DisplaySegment{{Kind: "text", Text: "编的外层"}}},
			{UID: "101", Name: "编的丙", At: t0, Segments: []api.DisplaySegment{{Kind: "forward", Ref: "inner", Forward: &api.ForwardBrief{Title: "编的乙的聊天记录", Count: 1}}}},
		}},
		"inner": {ID: "inner", State: "ready", Nodes: []api.ForwardNode{{UID: "200", Name: "编的乙", At: t0, Segments: []api.DisplaySegment{{Kind: "text", Text: "编的里层"}}}}},
	}}
	q := &uiQueue{}
	a := shellApp()
	a.reader, a.postFn = r, q.post
	t.Cleanup(a.stopForward)
	a.winControls = &winshell.WindowControls{Minimize: func() {}, Toggle: func() {}, Close: func() {}}
	tt := shellTester(a, 900, 900)
	frames := func() {
		settleMem(q)
		tt.Frame()
		settleMem(q)
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
	}
	a.openForward(api.EventRef{Conversation: a.keyOf(a.sel)}, "outer", nil)
	frames()
	if !hasKey(tt, "float-panel") || !hasKey(tt, "forward-head") || !tt.HasText("编的外层") {
		t.Fatalf("转发内容该在浮动右栏：%q", tt.Texts())
	}
	if !tt.HasText("今晚的月色真好") {
		t.Fatalf("聊天还该看得见：%q", tt.Texts())
	}
	if err := tt.Click("编的乙的聊天记录"); err != nil {
		t.Fatalf("%v；%q", err, tt.Texts())
	}
	frames()
	if !tt.HasText("编的里层") {
		t.Fatalf("嵌套后该看里层：%q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	frames()
	if !a.forward.open || !tt.HasText("编的外层") || !hasKey(tt, "float-panel") {
		t.Fatalf("Esc 应先退一层：open=%v %q", a.forward.open, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	frames()
	if a.forward.open || a.workbench.shell.st.Panes.SideOpen || hasKey(tt, "float-panel") {
		t.Fatalf("最外层再按 Esc 该关掉并恢复：open=%v side=%v", a.forward.open, a.workbench.shell.st.Panes.SideOpen)
	}
}
