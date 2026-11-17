package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/freeapi"
)

// 设置 → 插件 → 免费接口：用 ui.NewTester 走真实的 view，数据是编的演示后台。

type freeHarness struct {
	*linkHarness
	demo *demoFreeAPI
}

func newFreeHarness(t *testing.T, width, height int) *freeHarness {
	t.Helper()
	demo := newDemoFreeAPI()
	h := &linkHarness{q: make(chan func(), 32), width: width, height: height}
	h.fake = &linkPageFake{demoLinkSettings: newDemoLinkSettings()}
	h.a = &app{realTab: tabPlugins}
	h.a.linkSettings.writer = h.fake
	h.a.linkSettings.free.writer = demo
	h.a.postFn = func(fn func()) { h.q <- fn }
	h.tt = ui.NewTester(h.a.realSettingsView, width, height)
	h.tt.SetScale(1.25)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h.tt.Frame()
	h.wait(t) // 插件页自己的读取
	h.wait(t) // 免费接口的读取
	return &freeHarness{linkHarness: h, demo: demo}
}

func freeShot(t *testing.T, h *freeHarness, name string) {
	dir := os.Getenv("FREEAPI_SHOT_DIR")
	if dir == "" {
		return
	}
	h.tt.Frame()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestFreeAPIPageListsEveryProviderWithState(t *testing.T) {
	h := newFreeHarness(t, 2048, 1030)
	h.click(t, "免费接口（天气、节假日、汇率）")
	for _, want := range []string{"定期检测接口", "立即检测全部", "天气每人每天", "汇率每群每小时", "日历每人每天"} {
		if !h.tt.HasText(want) {
			t.Errorf("缺 %q：%v", want, h.tt.Texts())
		}
	}
	for _, p := range freeapi.DefaultCatalog().Providers {
		name := p.Name
		if p.Grey {
			name += "（非公开接口）"
		}
		h.scrollTo(t, name)
	}
	for _, want := range []string{"退避到", "需处理（格式变了）", "已关闭", "正常 · 置顶", "37/100"} {
		if !h.hasPart(want) {
			t.Errorf("缺状态文字 %q", want)
		}
	}
	freeShot(t, h, "free-apis-list.png")
}

func (h *freeHarness) hasPart(s string) bool {
	for _, x := range h.tt.Texts() {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

func (h *freeHarness) scrollTo(t *testing.T, label string) {
	t.Helper()
	for range 30 {
		r, ok := h.tt.Find(label)
		if ok && r.Y >= 0 && r.Y+r.H <= float32(h.height) {
			return
		}
		dy := float32(500) // 正数往下滚；在上面（滚过头了）就往回滚
		if ok && r.Y < 0 {
			dy = -500
		}
		h.tt.Scroll(float32(h.width-20), float32(h.height/2), 0, dy)
		h.tt.Frame()
	}
	r, ok := h.tt.Find(label)
	t.Fatalf("滚不到 %s：%+v %v", label, r, ok)
}

func (h *freeHarness) state(id string) freeapi.ProviderView {
	for _, c := range h.demo.view.Categories {
		for _, p := range c.Providers {
			if p.ID == id {
				return p
			}
		}
	}
	return freeapi.ProviderView{}
}

func TestFreeAPIPageProviderActions(t *testing.T) {
	h := newFreeHarness(t, 2048, 1030)
	h.click(t, "免费接口（天气、节假日、汇率）")
	h.scrollTo(t, "Open-Meteo")
	h.click(t, "Open-Meteo")
	h.click(t, "置顶")
	h.wait(t)
	if !h.state("open-meteo").Pinned {
		t.Fatal("置顶没有生效")
	}
	h.click(t, "关闭")
	h.wait(t)
	if p := h.state("open-meteo"); !p.Off || p.State != freeapi.StateOff {
		t.Fatalf("关闭没有生效：%+v", p)
	}
	freeShot(t, h, "free-apis-provider.png")
	// 立即检测：结果写成一行提示
	h.click(t, "立即检测")
	h.wait(t)
	if h.demo.probes != 1 || !h.tt.HasText("检测完成：演示数据，没有联网") {
		t.Fatalf("立即检测没有走通：%d %v", h.demo.probes, h.tt.Texts())
	}
	// 退避中的才有“清除退避”
	h.scrollTo(t, "MET Norway")
	h.click(t, "MET Norway")
	h.click(t, "清除退避")
	h.wait(t)
	if h.state("met-norway").State != freeapi.StateOK {
		t.Fatal("清除退避没有生效")
	}
	if h.tt.HasText("清除退避") {
		t.Error("退避清掉后不该再有这个按钮")
	}
}

func TestFreeAPIPageEditsLimitsAndProbe(t *testing.T) {
	h := newFreeHarness(t, 2048, 1030)
	h.click(t, "免费接口（天气、节假日、汇率）")
	h.click(t, "天气每人每天")
	h.click(t, "编辑免费接口数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("30")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	if got := h.demo.view.Limits[0].PerActorDaily; got != 30 {
		t.Fatalf("每人每天没保存：%d", got)
	}
	// 0 是合法值（不限）
	h.click(t, "天气每群每小时")
	h.click(t, "编辑免费接口数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("0")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	if h.demo.view.Limits[0].PerConversationHourly != 0 {
		t.Fatal("0 应当能存")
	}
	// 探测间隔越界：不提交，对话框里提示
	h.click(t, "检测间隔（小时，1～48）")
	h.click(t, "编辑免费接口数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("99")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if h.demo.view.ProbeHours != 12 || !h.tt.HasText("请输入范围内的整数") {
		t.Fatalf("越界不该提交：%d %v", h.demo.view.ProbeHours, h.tt.Texts())
	}
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("24")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	if h.demo.view.ProbeHours != 24 {
		t.Fatalf("间隔没保存：%d", h.demo.view.ProbeHours)
	}
	h.click(t, "定期检测接口")
	h.wait(t)
	if h.demo.view.ProbeEnabled {
		t.Fatal("探测开关没关上")
	}
	freeShot(t, h, "free-apis-settings.png")
}

// 配置在别处被改过（版本对不上）：不写，刷新并说明
func TestFreeAPIPageVersionConflict(t *testing.T) {
	h := newFreeHarness(t, 2048, 1030)
	h.click(t, "免费接口（天气、节假日、汇率）")
	h.demo.mu.Lock()
	h.demo.view.Version = "demo-2"
	h.demo.mu.Unlock()
	h.click(t, "天气每人每天")
	h.click(t, "编辑免费接口数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("5")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	if h.demo.view.Limits[0].PerActorDaily == 5 || !h.hasPart("配置刚被别处改过") {
		t.Fatalf("版本冲突不该写：%v", h.tt.Texts())
	}
}

// 窄窗口和长列表：十一家提供方的行不挤在一起、不横向溢出
func TestFreeAPIPageNarrowWindow(t *testing.T) {
	h := newFreeHarness(t, 900, 1030)
	h.click(t, "免费接口（天气、节假日、汇率）")
	start := time.Now()
	for _, p := range freeapi.DefaultCatalog().Providers {
		name := p.Name
		if p.Grey {
			name += "（非公开接口）"
		}
		h.scrollTo(t, name)
		if r, ok := h.tt.Find(name); !ok || r.X < 0 || r.X+r.W > 900 {
			t.Errorf("%s 超出窗口宽度：%+v", name, r)
		}
	}
	t.Logf("十一家逐个滚到：%v", time.Since(start))
	freeShot(t, h, "free-apis-narrow.png")
}
