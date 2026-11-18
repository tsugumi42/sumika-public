package main

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/remotepick"
)

// 插件页“日历和天气”的所在地：搜索下拉选地点，选中存规范结果，清空，保存失败和版本冲突如实写。
// 全程假后台，不碰真实配置。

type lifeFakeWriter struct {
	*demoLocation
	failure error
	searchF func(ctx context.Context, q string) ([]api.Place, error)
	writes  int
}

func (f *lifeFakeWriter) SearchPlaces(ctx context.Context, q string) ([]api.Place, error) {
	if f.searchF != nil {
		return f.searchF(ctx, q)
	}
	return f.demoLocation.SearchPlaces(ctx, q)
}

func (f *lifeFakeWriter) SetLocation(ctx context.Context, loc *config.Location, base string) (api.LocationView, error) {
	f.writes++
	if f.failure != nil {
		v, _ := f.demoLocation.Location(ctx)
		return v, f.failure
	}
	return f.demoLocation.SetLocation(ctx, loc, base)
}

type lifeHarness struct {
	a  *app
	tt *ui.Tester
	q  chan func()
	f  *lifeFakeWriter
}

func newLifeHarness(t *testing.T, dark bool) *lifeHarness {
	t.Helper()
	a := shellApp()
	a.rail = 4
	a.realTab = tabPlugins
	f := &lifeFakeWriter{demoLocation: newDemoLocation()}
	a.lifeSettings.writer = f
	a.linkSettings.writer = &linkPageFake{demoLinkSettings: newDemoLinkSettings()}
	h := &lifeHarness{a: a, q: make(chan func(), 64), f: f}
	a.postFn = func(fn func()) { h.q <- fn }
	h.tt = shellTester(a, 2048, 1030)
	if dark {
		a.look.settings.Mode = "night"
		h.tt.Frame()
		h.tt.Frame()
	}
	h.pumpUntil(t, func() bool { return a.lifeSettings.loaded })
	return h
}

// pumpUntil 放行后台回来的结果，直到条件成立
func (h *lifeHarness) pumpUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !ok() {
		select {
		case fn := <-h.q:
			fn()
			h.tt.Frame()
			h.tt.Frame()
		case <-deadline:
			t.Fatalf("等不到：%v", h.tt.Texts())
		}
	}
}

// shot 存“日历和天气”这一块的截图（截组件区域，不整页缩小）；LOOK_SHOT 指向存放目录
func (h *lifeHarness) shot(t *testing.T, name string) {
	t.Helper()
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		return
	}
	head, ok := h.tt.Find("日历和天气")
	if !ok {
		t.Fatal("截图找不到“日历和天气”")
	}
	img := h.tt.Image()
	const scale = 1.25 // 屏幕 2560×1440、125%，逻辑 2048×1030
	r := image.Rect(int((head.X-24)*scale), int((head.Y-44)*scale), int((head.X+940)*scale), int((head.Y+320)*scale)).Intersect(img.Bounds())
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img.SubImage(r)); err != nil {
		t.Fatal(err)
	}
}

func (h *lifeHarness) search(t *testing.T, text string) {
	t.Helper()
	if err := h.tt.Click("搜索城市或地区"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Type(text)
	h.tt.Frame()
	h.tt.Frame()
	h.tt.Advance(remotepick.DefaultDebounce + 50*time.Millisecond)
	h.tt.Frame()
	h.tt.Frame()
}

func TestLifeSettingsPickAndClear(t *testing.T) {
	h := newLifeHarness(t, false)
	for _, want := range []string{"日历和天气", "所在地", "没有设置"} {
		if !h.tt.HasText(want) {
			t.Fatalf("页面缺 %q：%q", want, h.tt.Texts())
		}
	}
	h.shot(t, "插件页-所在地-没设.png")
	// 拼音搜：候选显示成“海淀，北京市，中国”，右边写群里会说到哪一级
	h.search(t, "haidian")
	h.pumpUntil(t, func() bool { return len(h.a.lifeSettings.pick.Items) > 0 })
	for _, want := range []string{"海淀，北京市，中国", "群里说到：北京市", "海淀，某省，某国", "只知道到省，群里当不知道"} {
		if !h.tt.HasText(want) {
			t.Fatalf("候选缺 %q：%q", want, h.tt.Texts())
		}
	}
	h.shot(t, "插件页-所在地-候选.png")
	// 鼠标选中：存的是规范结果（含城市、经纬度、时区），不是打的原文
	if err := h.tt.Click("海淀，北京市，中国"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.pumpUntil(t, func() bool { return h.a.lifeSettings.view.Location.Set() })
	got := h.a.lifeSettings.view.Location
	if got.Name != "海淀，北京市，中国" || got.CityName != "北京市" || got.Timezone != "Asia/Shanghai" || got.Latitude == nil || *got.Latitude != 39.99 {
		t.Fatalf("存的该是规范结果：%+v", got)
	}
	h.tt.Frame()
	if !h.tt.HasText("海淀，北京市，中国") || h.tt.HasText("haidian") || !h.tt.HasText("清空") {
		t.Fatalf("选中后该显示规范结果和清空：%q", h.tt.Texts())
	}
	h.shot(t, "插件页-所在地-已设置.png")
	// 清空
	if err := h.tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.pumpUntil(t, func() bool { return !h.a.lifeSettings.view.Location.Set() })
	if !h.tt.HasText("没有设置") || h.tt.HasText("清空") {
		t.Fatalf("清空后：%q", h.tt.Texts())
	}
	if h.f.writes != 2 {
		t.Fatalf("写了 %d 次", h.f.writes)
	}
}

func TestLifeSettingsSearchFailureAndConflict(t *testing.T) {
	h := newLifeHarness(t, false)
	h.f.searchF = func(context.Context, string) ([]api.Place, error) {
		return nil, errors.New("没取到候选：网络不通或对方没回，稍后再试")
	}
	h.search(t, "beijing")
	h.pumpUntil(t, func() bool { return h.a.lifeSettings.pick.Err != "" })
	if !h.tt.HasText("没取到候选：网络不通或对方没回，稍后再试") || !h.tt.HasText("重试") {
		t.Fatalf("失败要有文字：%q", h.tt.Texts())
	}
	h.shot(t, "插件页-所在地-失败.png")
	// 保存时版本冲突：如实写，并用最新的视图
	h.f.searchF = nil
	if err := h.tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.pumpUntil(t, func() bool { return h.a.lifeSettings.pick.Err == "" })
	h.f.demoLocation.mu.Lock()
	h.f.demoLocation.ver += 3 // 别处改过
	h.f.demoLocation.mu.Unlock()
	h.search(t, "x")
	h.a.lifeSettings.pick.Text = ""
	place := demoPlaces()[2]
	h.a.lifeSave(&place.Location)
	h.pumpUntil(t, func() bool { return h.a.lifeSettings.note != "" })
	if h.a.lifeSettings.view.Location.Set() || !h.tt.HasText("设置在别处改过，已重新读取，请再选一次") {
		t.Fatalf("冲突：%+v %q", h.a.lifeSettings.view, h.tt.Texts())
	}
}

func TestLifeSettingsSaveFailureIsShown(t *testing.T) {
	h := newLifeHarness(t, true)
	h.f.failure = errors.New("设置保存失败：磁盘写不进去")
	place := demoPlaces()[0]
	h.a.lifeSave(&place.Location)
	h.pumpUntil(t, func() bool { return h.a.lifeSettings.note != "" })
	if h.a.lifeSettings.view.Location.Set() || !h.tt.HasText("设置保存失败：磁盘写不进去") {
		t.Fatalf("失败要如实写，且不显示成已设置：%q", h.tt.Texts())
	}
	h.shot(t, "插件页-所在地-保存失败-深色.png")
}
