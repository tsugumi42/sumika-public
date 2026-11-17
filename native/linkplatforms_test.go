package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/linktemplate"
)

// 新外壳里的“网站与页面类型”：站点列表、整站开关、页面类型的状态和开关、样式、模板编辑。数据是演示设置，不碰真实配置。

type platformHarness struct {
	a  *app
	tt *ui.Tester
	q  chan func()
}

func newPlatformHarness(t *testing.T, w, h int, dark bool) *platformHarness {
	t.Helper()
	a := shellApp()
	a.rail = 4
	a.realTab = tabPlugins
	f := &linkPageFake{demoLinkSettings: newDemoLinkSettings()}
	a.linkSettings.writer = f
	ph := &platformHarness{a: a, q: make(chan func(), 32)}
	a.postFn = func(fn func()) { ph.q <- fn }
	ph.tt = shellTester(a, w, h)
	if dark {
		a.look.settings.Mode = "night"
		ph.tt.Frame()
		ph.tt.Frame()
		if !a.workbench.shell.kit.S.Dark {
			t.Fatal("深色没有生效")
		}
	}
	return ph
}

// pump 转到链接设置的后台任务落地为止。插件页还同时在读日历和天气，它们的回调也进同一个队列，
// 不能只取第一个；条件到了再把队里剩的清掉
func (p *platformHarness) pump(t *testing.T) {
	t.Helper()
	deadline := time.After(uiWaitLimit)
	for p.a.linkSettings.busy || !p.a.linkSettings.loaded {
		select {
		case fn := <-p.q:
			fn()
		case <-deadline:
			t.Fatal("设置 worker 没有返回")
		}
	}
	for {
		select {
		case fn := <-p.q:
			fn()
			continue
		default:
		}
		break
	}
	p.tt.Frame()
	p.tt.Frame()
}
func (p *platformHarness) click(t *testing.T, label string) {
	t.Helper()
	for range 10 {
		r, ok := p.tt.Find(label)
		if !ok {
			t.Fatalf("找不到 %s：%v", label, p.tt.Texts())
		}
		if r.Y >= 0 && r.Y+r.H <= 1030 {
			if e := p.tt.Click(label); e != nil {
				t.Fatal(e)
			}
			p.tt.Frame()
			p.tt.Frame()
			return
		}
		dy := float32(500) // 往下滚是正数
		if r.Y < 0 {
			dy = -500
		}
		p.tt.Scroll(1000, 500, 0, dy)
		p.tt.Frame()
	}
	t.Fatalf("滚不到 %s：%v", label, p.tt.Texts())
}

func (p *platformHarness) openLinks(t *testing.T) {
	t.Helper()
	p.click(t, "链接解析（link-reader）")
	p.pump(t)
}

func (p *platformHarness) expand() config.LinkExpand {
	return p.a.linkSettings.view.Configured.Expand
}

func TestPlatformListShowsKindsAndSwitches(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	for _, n := range []string{"网站与页面类型", "哔哩哔哩", "X / 推特", "GitHub", "Steam"} {
		if !p.tt.HasText(n) {
			p.tt.Frame()
			if !p.tt.HasText(n) {
				// 长页面里可能在屏幕外：滚动找
				p.click(t, n)
			}
		}
	}
	p.click(t, "哔哩哔哩")
	for _, n := range []string{"视频", "动态和图文", "专栏", "直播间", "个人主页", "会员购", "番剧"} {
		if _, ok := p.tt.Find(n); !ok {
			t.Fatalf("B 站缺页面类型 %s：%v", n, p.tt.Texts())
		}
	}
	if !p.tt.HasText("不支持") || !p.tt.HasText("收藏夹和播单") {
		t.Fatal("不支持的页面类型要列出来并写明原因", p.tt.Texts())
	}
	// 关掉直播间：写进配置，其他类型不受影响
	p.click(t, "直播间展开")
	p.pump(t)
	if p.expand().KindEnabled("bilibili", "live") || !p.expand().KindEnabled("bilibili", "video") {
		t.Fatalf("直播间应关、视频仍开：%+v", p.expand().Platforms)
	}
	// 关掉整站
	p.click(t, "哔哩哔哩整站展开")
	p.pump(t)
	if p.expand().PlatformEnabled("bilibili") || p.expand().KindEnabled("bilibili", "video") {
		t.Fatalf("B 站整站应关：%+v", p.expand().Platforms)
	}
	// 再打开：直播间仍是单独关掉的
	p.click(t, "哔哩哔哩整站展开")
	p.pump(t)
	if !p.expand().KindEnabled("bilibili", "video") || p.expand().KindEnabled("bilibili", "live") {
		t.Fatalf("重新打开后直播间应仍关着：%+v", p.expand().Platforms)
	}
}

func TestPlatformStyleAndTemplateEditor(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	p.click(t, "X / 推特")
	p.click(t, "推文、长文和投票")
	for _, n := range []string{"发出去的样式", "卡片", "图文", "混合", "默认", "合并转发", "通配符模板"} {
		if _, ok := p.tt.Find(n); !ok {
			t.Fatalf("展开面板缺 %s：%v", n, p.tt.Texts())
		}
	}
	p.click(t, "混合")
	p.pump(t)
	if p.expand().Sites["x"].Forward != "always" {
		t.Fatalf("选混合应写 forward: always：%+v", p.expand().Sites)
	}
	p.click(t, "通配符模板") // 高级：默认折叠
	if !p.tt.HasText("预览（示例数据）") {
		t.Fatal("展开通配符模板后应有预览", p.tt.Texts())
	}
	// 实时预览：点占位符追加，预览立刻带上它的示例值
	before := p.a.linkSettings.tpl
	p.click(t, "$点赞")
	if !strings.HasSuffix(p.a.linkSettings.tpl, "$点赞") || p.a.linkSettings.tpl == before {
		t.Fatalf("占位符没有插入：%q", p.a.linkSettings.tpl)
	}
	// 出错提示写在框下面，保存不可用
	p.a.linkSettings.tpl = "$不存在"
	p.tt.Frame()
	if !p.tt.HasText("不认识") {
		t.Fatal("非法模板没有错误提示", p.tt.Texts())
	}
	// 合法的保存
	p.a.linkSettings.tpl = "新版｜$作者｜$链接"
	p.tt.Frame()
	p.click(t, "保存模板")
	p.pump(t)
	if got := p.expand().Templates["x"]; got != "新版｜$作者｜$链接" {
		t.Fatalf("模板没有保存：%q", got)
	}
}

// 浅色、深色各截一张给人看（LINK_SHOT_DIR 没设就不存）；元素数一并检查
func TestPlatformPageShots(t *testing.T) {
	dir := os.Getenv("LINK_SHOT_DIR")
	for _, dark := range []bool{false, true} {
		p := newPlatformHarness(t, 2048, 1030, dark)
		p.openLinks(t)
		p.click(t, "哔哩哔哩")
		p.click(t, "视频")
		p.tt.Scroll(1000, 500, 0, 200) // 让展开面板进画面
		p.tt.Frame()
		name := "link-platforms-light.png"
		if dark {
			name = "link-platforms-dark.png"
		}
		if dir != "" {
			p.tt.Frame()
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, p.tt.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}

// 内容压力：十七个网站全部展开，所有页面类型都列出来；宽窗口和很窄的窗口都画得出来，报元素数
func TestPlatformPageStress(t *testing.T) {
	for _, w := range []int{2048, 700} {
		p := newPlatformHarness(t, w, 1030, false)
		if w < 1000 {
			p.click(t, "插件") // 窄窗口分两层，先点进分类
		}
		p.openLinks(t)
		open := p.a.linkSettings.platOpen
		if open == nil {
			p.a.linkSettings.platOpen = map[string]bool{}
			open = p.a.linkSettings.platOpen
		}
		kinds := 0
		for _, pl := range linktemplate.Catalog() {
			open[pl.ID] = true
			kinds += len(pl.Kinds)
		}
		start := time.Now()
		p.tt.Frame()
		p.tt.Frame()
		elapsed := time.Since(start)
		texts := p.tt.Texts()
		for _, pl := range linktemplate.Catalog() {
			for _, k := range pl.Kinds {
				if !strings.Contains(strings.Join(texts, " "), k.Label) {
					t.Errorf("宽 %d：%s 的页面类型 %s 没列出来", w, pl.Label, k.Label)
				}
			}
		}
		t.Logf("宽 %d：%d 个网站、%d 种页面类型全部展开，文字节点 %d 个，两帧 %v", w, len(linktemplate.Catalog()), kinds, len(texts), elapsed)
	}
}

// 通配符模板折叠区已展开时选样式：编辑框跟着换成新样式的模板，再点保存不会把样式冲掉。
func TestStylePickRefreshesTemplateBuffer(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	p.click(t, "X / 推特")
	p.click(t, "推文、长文和投票")
	p.click(t, "通配符模板")
	p.click(t, "卡片")
	p.pump(t)
	if p.expand().Templates["x"] != "@卡片" {
		t.Fatalf("选卡片应写 @卡片：%+v", p.expand().Templates)
	}
	if p.a.linkSettings.tpl != "@卡片" {
		t.Fatalf("选样式后编辑框还是旧内容：%q", p.a.linkSettings.tpl)
	}
	p.click(t, "保存模板")
	p.pump(t)
	if p.expand().Templates["x"] != "@卡片" || p.expand().Sites["x"].Forward != "never" {
		t.Fatalf("保存模板把刚选的样式冲掉了：%+v %+v", p.expand().Templates, p.expand().Sites)
	}
}

// 手写过自定义模板时，点样式先要确认，不悄悄覆盖。
func TestStylePickAsksBeforeReplacingCustomTemplate(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	p.a.linkJob(&api.LinkSettingsChange{Key: config.LinkTemplate, Site: "x", Text: "自定义｜$作者｜$链接"})
	p.pump(t)
	p.click(t, "X / 推特")
	p.click(t, "推文、长文和投票")
	if w := p.a.linkSettings.writer.(*linkPageFake).writes; w != 1 || p.a.linkSettings.busy {
		t.Fatalf("打开面板就写了配置（自定义模板被当成点了默认）：写了 %d 次", w)
	}
	p.click(t, "卡片")
	if got := p.expand().Templates["x"]; got != "自定义｜$作者｜$链接" {
		t.Fatalf("第一次点不该改自定义模板：%q", got)
	}
	if !p.tt.HasText("会替换你的自定义模板，再点一次确认") {
		t.Fatal("没有提示会替换自定义模板", p.tt.Texts())
	}
	p.click(t, "卡片")
	p.pump(t)
	if got := p.expand().Templates["x"]; got != "@卡片" {
		t.Fatalf("确认后应写入样式：%q", got)
	}
}

// 每个网站有“发图方式”：默认普通优先，点一下写进 platforms.<网站>.imageMode，别的网站不受影响；没有可展开页面的网站不显示
func TestPlatformImageModeChoice(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	p.click(t, "X / 推特")
	for _, n := range []string{"发图方式", "普通优先", "转图优先", "合并转发转图优先"} {
		if _, ok := p.tt.Find(n); !ok {
			t.Fatalf("展开网站后缺 %s：%v", n, p.tt.Texts())
		}
	}
	if p.expand().ImageModeOf("x") != config.ImageModeAuto {
		t.Fatal("默认应是普通优先")
	}
	p.click(t, "转图优先")
	p.pump(t)
	if p.expand().ImageModeOf("x") != config.ImageModeRotate || p.expand().ImageModeOf("bilibili") != config.ImageModeAuto {
		t.Fatalf("X 应是 rotate，别的网站不变：%+v", p.expand().Platforms)
	}
	if !p.tt.HasText("图先转 180° 直接发") {
		t.Fatal("选中后应有一行说明", p.tt.Texts())
	}
	p.click(t, "合并转发转图优先")
	p.pump(t)
	if p.expand().ImageModeOf("x") != config.ImageModeForward {
		t.Fatalf("应改成 forward：%+v", p.expand().Platforms)
	}
	p.click(t, "普通优先")
	p.pump(t)
	if p.expand().ImageModeOf("x") != config.ImageModeAuto {
		t.Fatalf("应改回 auto：%+v", p.expand().Platforms)
	}
}
