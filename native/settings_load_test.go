package main

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
)

// 设置页几处"正在读取…""正在保存…"（loadstate 的规则）：阈值内不出现，慢了才出现，出现的位置事先占着或不插新行。
// 时间用 Tester.Advance 推进。

// slowModelReads 读模型设置前等门
type slowModelReads struct {
	*modelFake
	gate chan struct{}
}

func (s *slowModelReads) ModelSettings(ctx context.Context) (api.ModelSettingsView, error) {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return api.ModelSettingsView{}, ctx.Err()
	}
	return s.modelFake.ModelSettings(ctx)
}

func newSlowModelHarness(t *testing.T) (*modelHarness, *slowModelReads) {
	t.Helper()
	f := &modelFake{demoModelSettings: newDemoModelSettings([]character{{ID: "test-a", Name: "编造角色"}})}
	slow := &slowModelReads{modelFake: f, gate: make(chan struct{})}
	h := &modelHarness{fake: f, q: make(chan func(), 32)}
	h.a = &app{realTab: tabModels, chars: []character{{ID: "test-a", Name: "编造角色"}}}
	h.a.models.writer = slow
	h.a.postFn = func(fn func()) { h.q <- fn }
	h.tt = testerOf(2048, 1030, h.a.realSettingsView)
	return h, slow
}

func (h *modelHarness) advance(d time.Duration) {
	h.tt.Advance(d)
	h.tt.Frame()
}

// 模型页第一次读：阈值内状态那一格是空的（不写"还没有读取"也不写"正在读取"），慢了才写；读到以后换成真状态
func TestSettingsLoadModelFirstReadDelayed(t *testing.T) {
	h, slow := newSlowModelHarness(t)
	for i := 0; i < 5; i++ {
		for _, bad := range []string{"正在读取模型设置", "还没有读取模型列表"} {
			if h.tt.HasText(bad) {
				t.Fatalf("第 %d 帧（%d 毫秒）出现了 %q：%v", i, i*50, bad, h.tt.Texts())
			}
		}
		h.advance(50 * time.Millisecond)
	}
	h.advance(100 * time.Millisecond)
	if !h.tt.HasText("正在读取模型设置…") {
		t.Fatalf("过了阈值应写正在读取：%v", h.tt.Texts())
	}
	close(slow.gate)
	h.wait(t)
	if !h.tt.HasText("46 个") || h.tt.HasText("正在读取模型设置") {
		t.Fatalf("读到以后应是真状态：%v", h.tt.Texts())
	}
}

// 快：门一开就回，全程没有"正在读取"
func TestSettingsLoadModelFastReadNeverShowsHint(t *testing.T) {
	h, slow := newSlowModelHarness(t)
	h.advance(100 * time.Millisecond)
	close(slow.gate)
	h.wait(t)
	h.advance(time.Second)
	if h.tt.HasText("正在读取模型设置") || !h.tt.HasText("46 个") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

// 保存：快的保存不出"正在保存…"，慢的在阈值后才出现（出现时才插这一行，快的保存不会让页面抖一下）
func TestSettingsLoadModelSaveHintDelayed(t *testing.T) {
	h := newModelHarness(t)
	h.fake.gate = make(chan struct{})
	h.selectModel(t)
	for i := 0; i < 5; i++ {
		if h.tt.HasText("正在保存") {
			t.Fatalf("第 %d 帧（%d 毫秒）就写了正在保存：%v", i, i*50, h.tt.Texts())
		}
		h.advance(50 * time.Millisecond)
	}
	close(h.fake.gate) // 250 毫秒时回来：从头到尾没出现过
	h.wait(t)
	h.advance(time.Second)
	if h.tt.HasText("正在保存") {
		t.Fatalf("快的保存出现了提示：%v", h.tt.Texts())
	}
	h2 := newModelHarness(t)
	h2.fake.gate = make(chan struct{})
	h2.selectModel(t)
	h2.advance(350 * time.Millisecond)
	if !h2.tt.HasText("正在保存") {
		t.Fatalf("慢的保存过了阈值应有提示：%v", h2.tt.Texts())
	}
	close(h2.fake.gate)
	h2.wait(t)
}

// slowLinkReads 读插件设置前等门
type slowLinkReads struct {
	*demoLinkSettings
	gate chan struct{}
}

func (s *slowLinkReads) LinkSettings(ctx context.Context) (api.LinkSettingsView, error) {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return api.LinkSettingsView{}, ctx.Err()
	}
	return s.demoLinkSettings.LinkSettings(ctx)
}

// 插件设置页第一次读：展开以后阈值内不写"正在读取插件设置…"，慢了才写
func TestSettingsLoadLinkFirstReadDelayed(t *testing.T) {
	slow := &slowLinkReads{demoLinkSettings: newDemoLinkSettings(), gate: make(chan struct{})}
	q := make(chan func(), 32)
	a := &app{realTab: tabPlugins}
	a.linkSettings.writer = slow
	a.postFn = func(fn func()) { q <- fn }
	tt := ui.NewTester(a.realSettingsView, 2048, 1030)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if err := tt.Click("链接解析（link-reader）"); err != nil { // 点这一行的按钮展开（"设置"两个字在左边的分类里也有）
		t.Fatal(err)
	}
	tt.Frame()
	for i := 0; i < 5; i++ {
		if tt.HasText("正在读取插件设置") {
			t.Fatalf("第 %d 帧（%d 毫秒）就写了正在读取：%v", i, i*50, tt.Texts())
		}
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("正在读取插件设置…") {
		t.Fatalf("过了阈值应写正在读取：%v", tt.Texts())
	}
	close(slow.gate)
	(<-q)()
	tt.Frame()
	if tt.HasText("正在读取插件设置") {
		t.Fatalf("读到以后提示还在：%v", tt.Texts())
	}
}

// 设置页读配置（还没有读到）：阈值内不写"正在读配置…"，慢了才写；读到以后换成内容
func TestSettingsLoadRealConfigHintDelayed(t *testing.T) {
	a := &app{realTab: tabMemory}
	tt := ui.NewTester(a.realSettingsView, 2048, 1030)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	for i := 0; i < 5; i++ {
		if tt.HasText("正在读配置") {
			t.Fatalf("第 %d 帧（%d 毫秒）就写了正在读配置", i, i*50)
		}
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("正在读配置…") {
		t.Fatalf("过了阈值应写正在读配置：%v", tt.Texts())
	}
	a.real = api.Result[api.Settings]{State: api.Ready}
	tt.Frame()
	if tt.HasText("正在读配置") {
		t.Fatalf("读到以后提示还在：%v", tt.Texts())
	}
}

// 数据位置还没定位出来：同样阈值后才写"正在定位数据…"
func TestSettingsLoadDataLocationHintDelayed(t *testing.T) {
	a := &app{realTab: tabData}
	tt := ui.NewTester(a.realSettingsView, 2048, 1030)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	for i := 0; i < 5; i++ {
		if tt.HasText("正在定位数据") {
			t.Fatalf("第 %d 帧（%d 毫秒）就写了正在定位数据", i, i*50)
		}
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("正在定位数据…") {
		t.Fatalf("过了阈值应写正在定位数据：%v", tt.Texts())
	}
	a.location.current = backend.RootLocation{Dir: "D:/编造的目录", Source: "程序目录"}
	tt.Frame()
	if tt.HasText("正在定位数据") || !tt.HasText("当前根目录：D:/编造的目录") {
		t.Fatalf("定位到以后应显示根目录：%v", tt.Texts())
	}
}
