package main

import (
	"math"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/ctlproto"
)

// 规格-输入框胶囊和会话信息 第六节 A1、A2、A4（ADR-351）。

func pillApp(text string, usage bool) *app {
	a := shellApp()
	if text != "" {
		a.composerOf(a.sel).Main.Text = text
	}
	if usage {
		st := &a.workbench.shell.sideContext
		st.conv, st.has = a.sel, true
		st.report = ctlproto.ConvTurnsReport{Turns: []ctlproto.ConvTurn{{ID: "t1", Requests: []ctlproto.ConvRequest{
			{ID: "r0", Model: "demo/m", Input: 50000},
			{ID: "r1", Model: "demo/m", Input: 81000}, // 最后一次成功的请求
			{ID: "r2", Model: "demo/m", Input: 90000, Failed: "timeout"},
		}}}}
		a.models.view.Models = []api.ModelOption{{ID: "demo/m", ContextWindow: 131000, WindowSource: "provider"}}
		a.models.loaded = true
	}
	return a
}

// A1、A2：空着是胶囊（高 = 控件大档，圆角一半）；打三行往上长，外框圆角不变，发送键贴底。
func TestComposerIsPill(t *testing.T) {
	tok := chatui.Derive(chatui.DefaultSettings()) // shellApp 固定 16 号
	tt := shellTester(pillApp("", false), 1100, 700)
	one, ok := tt.Find("输入区")
	if !ok {
		t.Fatal("没找到输入区")
	}
	if want := chatui.ComposerMinHeight(tok); math.Abs(float64(one.H-want)) > 0.6 || math.Abs(float64(want-tok.HLg)) > 0.6 {
		t.Errorf("单行输入框高 %.1f，应是胶囊高 %.1f（= 控件大档 %.0f）", one.H, want, tok.HLg)
	}
	tt = shellTester(pillApp("第一行\n第二行\n第三行", false), 1100, 700)
	three, _ := tt.Find("输入区")
	// 三行时高 = 三行字 + 上下内距（单行时撑高的是发送键，所以比单行多出的不到两整行）
	if want := 3*tok.BodyLine + (one.H - chatui.ComposerSend(tok)); math.Abs(float64(three.H-want)) > 1 {
		t.Errorf("三行时输入框高 %.1f，应是三行字加上下内距 %.1f", three.H, want)
	}
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatal("没找到发送键")
	}
	if gap := (three.Y + three.H) - (send.Y + send.H); gap > one.H/2 {
		t.Errorf("多行时发送键离底边 %.1f，应贴底（单行时的内距）", gap)
	}
}

// A4：有请求数据时头像外有上下文环，悬停写最后一次成功请求的用量；没有数据时不画环。
func TestHeaderContextRing(t *testing.T) {
	tt := shellTester(pillApp("", true), 1100, 700)
	if !tt.HasText("上下文用了 62%（8.1 万 / 13.1 万）") {
		if _, ok := tt.Access("上下文用了 62%（8.1 万 / 13.1 万）"); !ok {
			t.Errorf("头像环的读屏名应是“上下文用了 62%%（8.1 万 / 13.1 万）”：%v", tt.Texts())
		}
	}
	tt = shellTester(pillApp("", false), 1100, 700)
	if _, ok := tt.Access("上下文用了 62%（8.1 万 / 13.1 万）"); ok {
		t.Error("没有请求数据时不该画上下文环")
	}
	a := pillApp("", true)
	a.models.view.Models[0].WindowSource = "default" // 窗口是猜的
	tt = shellTester(a, 1100, 700)
	if _, ok := tt.Access("上下文用了 62%（8.1 万 / 13.1 万）"); ok {
		t.Error("模型窗口不知道时不该算百分比")
	}
}

// 窗口先用请求自己记下的上限：模型列表里查不到（改过名、换了线路）也画得出环（待办“头像环改从请求取窗口”）。
func TestHeaderContextRingRequestWindow(t *testing.T) {
	a := pillApp("", true)
	a.models.view.Models = nil
	a.workbench.shell.sideContext.report.Turns[0].Requests[1].Window = 162000 // r1：最后一次成功的请求
	tt := shellTester(a, 1100, 700)
	if _, ok := tt.Access("上下文用了 50%（8.1 万 / 16.2 万）"); !ok {
		t.Errorf("该按请求记下的窗口 16.2 万算：%v", tt.Texts())
	}
	a.models.view.Models = []api.ModelOption{{ID: "demo/m", ContextWindow: 131000, WindowSource: "provider"}}
	tt = shellTester(a, 1100, 700)
	if _, ok := tt.Access("上下文用了 50%（8.1 万 / 16.2 万）"); !ok {
		t.Error("请求记下的窗口应优先于模型列表")
	}
}
