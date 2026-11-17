package main

import (
	"testing"

	"sumika-native/internal/api"
)

// 会话行右键：只接已有的“标为已读”。有未读时可点，没未读、不标已读的数据源时变灰写原因，
// 不是 QQ 会话或数据源根本没有 ReadMarker 时没有菜单。
func TestConvRowMenuMarkRead(t *testing.T) {
	e := newMarkEnv(t, 5, 3)
	e.a.convs[0].Last = lastLine{Text: "最后一句", CreatedAt: 424242}

	items := e.a.convRowMenu(markConv)
	if len(items) != 1 || items[0].Label != "标为已读" || items[0].Disabled || items[0].Action == nil {
		t.Fatalf("有未读该有一个可点的“标为已读”：%+v", items)
	}
	items[0].Action()
	e.r.wait(t, 1)
	e.settle()
	if call := e.r.calls[0]; call.upTo != 424242 || call.key.Conversation != markConv {
		t.Fatalf("该标到这个会话最后一条的时间：%+v", call)
	}
	if e.a.rm.busy || e.a.rm.at != 0 {
		t.Fatalf("手动标不碰当前会话自动标的状态：busy=%v at=%d", e.a.rm.busy, e.a.rm.at)
	}

	e.a.convs[0].Unread = 0
	if items = e.a.convRowMenu(markConv); len(items) != 1 || !items[0].Disabled || items[0].Trailing != "没有未读" || items[0].Action != nil {
		t.Fatalf("没有未读该变灰写原因：%+v", items)
	}
	e.a.convs[0].Unread = 3
	e.a.rm.off = true
	if items = e.a.convRowMenu(markConv); len(items) != 1 || !items[0].Disabled || items[0].Trailing != "这里不标已读" {
		t.Fatalf("数据源不标已读该变灰写原因：%+v", items)
	}
	e.a.rm.off = false
	if items = e.a.convRowMenu("local:r1"); items != nil {
		t.Fatalf("本地会话没有已读，不该有菜单：%+v", items)
	}
	e.a.reader = demoReader{}
	if items = e.a.convRowMenu(markConv); items != nil {
		t.Fatalf("数据源没有 ReadMarker 不该有菜单：%+v", items)
	}
}

// 数据源说不标（archive）：手动标也只记 off，不崩、不记日志。
func TestMarkReadNowUnsupportedTurnsOff(t *testing.T) {
	e := newMarkEnv(t, 5, 3)
	e.a.convs[0].Last = lastLine{Text: "最后一句", CreatedAt: 1000}
	e.r.mu.Lock()
	e.r.err = api.ErrReadMarkUnsupported
	e.r.mu.Unlock()
	e.a.markReadNow(markConv)
	e.r.wait(t, 1)
	e.until(t, "记下 off", func() bool { return e.a.rm.off })
	if len(e.logs) != 0 {
		t.Fatalf("不标不是出错：%q", e.logs)
	}
}

// 整条路径：新界面 QQ 列表里右键一行 → 菜单里点“标为已读” → 后台收到这个会话。
func TestWorkbenchRowContextMenuMarksRead(t *testing.T) {
	r := &markReader{got: make(chan struct{}, 8)}
	a := shellApp()
	a.demoMode = false
	a.rail, a.reader = 1, r
	a.convs[0].Last = lastLine{Text: "在吗", CreatedAt: 777000}
	a.rm.fg.Store(false)
	tt := shellTester(a, 2048, 1030)
	if err := tt.RightClick("编造的朋友"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("标为已读") {
		t.Fatalf("右键该出菜单：%q", tt.Texts())
	}
	if err := tt.Click("标为已读"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	r.wait(t, 1)
	if call := r.calls[0]; call.upTo != 777000 || call.key.Conversation != "qq:private:80001:90001" {
		t.Fatalf("后台收到的不对：%+v", call)
	}
}
