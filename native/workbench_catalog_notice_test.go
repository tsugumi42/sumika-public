package main

// 聊天框上方的“模型来源连不上”一行（规格-模型配置 6.1 第 7 条）：出现、去设置、恢复后消失、
// 被“只能看 / 已停止”这类说明挡住时不画。状态由测试直接放在宿主状态里（生产由 host.watchCatalog 带过来）。

import (
	"testing"

	"github.com/egoist/mygo/ui"
	backend "sumika-native/internal/app"
)

func catalogNoticeApp(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	a := shellApp()
	a.demoMode = false
	a.reader = &fakeInputs{}
	a.host = hostState{Mode: backend.Receive, CatalogSource: "http://127.0.0.1:3425/v1", CatalogError: "编造的网关不可用"}
	return a, shellTester(a, 2048, 1030)
}

func TestCatalogNoticeShowsWhenSourceDown(t *testing.T) {
	_, tt := catalogNoticeApp(t)
	want := "模型来源连不上（127.0.0.1:3425）：打开 Magpie，或在设置 · 模型里换地址"
	if !tt.HasText(want) {
		t.Fatalf("没有这一行：%q", tt.Texts())
	}
	if !tt.HasText("去设置") {
		t.Fatalf("没有“去设置”：%q", tt.Texts())
	}
}

func TestCatalogNoticeOpensModelSettings(t *testing.T) {
	a, tt := catalogNoticeApp(t)
	if err := tt.Click("去设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.rail != 4 || a.realTab != tabModels {
		t.Fatalf("没打开设置的模型页：rail=%d realTab=%d", a.rail, a.realTab)
	}
	if !tt.HasText("Magpie 地址") {
		t.Fatalf("眼前不是模型页：%q", tt.Texts())
	}
}

func TestCatalogNoticeClearsAfterRecovery(t *testing.T) {
	a, tt := catalogNoticeApp(t)
	if !tt.HasText("模型来源连不上") {
		t.Fatalf("先要有这一行：%q", tt.Texts())
	}
	a.host.CatalogError = ""
	tt.Frame()
	tt.Frame()
	if tt.HasText("模型来源连不上") {
		t.Fatalf("来源恢复后这一行还在：%q", tt.Texts())
	}
}

func TestCatalogNoticeYieldsToArchiveNote(t *testing.T) {
	a, tt := catalogNoticeApp(t)
	a.host.Mode = backend.Archive // 只看历史：先显示“请先启动后台”
	tt.Frame()
	if !tt.HasText("请先启动后台，再发送本地消息") {
		t.Fatalf("只看历史没有那句：%q", tt.Texts())
	}
	if tt.HasText("模型来源连不上") {
		t.Fatalf("只看历史时不该显示模型来源提示：%q", tt.Texts())
	}
}
