package main

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/linktemplate"
)

type linkPageFake struct {
	*demoLinkSettings
	failure error
	writes  int
}

func (f *linkPageFake) SetLinkSettings(ctx context.Context, ch api.LinkSettingsChange, version string) (api.LinkSettingsView, error) {
	f.writes++
	if f.failure != nil {
		v, e := f.LinkSettings(ctx)
		if e != nil {
			return v, e
		}
		return v, f.failure
	}
	return f.demoLinkSettings.SetLinkSettings(ctx, ch, version)
}

type linkHarness struct {
	a             *app
	fake          *linkPageFake
	tt            *ui.Tester
	q             chan func()
	width, height int
}

func newLinkHarness(t *testing.T, width, height, count int) *linkHarness {
	t.Helper()
	f := &linkPageFake{demoLinkSettings: newDemoLinkSettings()}
	for i := len(f.known); i < count; i++ {
		f.known = append(f.known, api.KnownLinkConversation{ID: fmt.Sprintf("qq:group:%d", 93001+i), Name: fmt.Sprintf("编的讨论室 %04d", i), Kind: "group", Account: "91***01"})
	}
	h := &linkHarness{fake: f, q: make(chan func(), 32), width: width, height: height}
	h.a = &app{realTab: tabPlugins}
	h.a.linkSettings.writer = f
	h.a.postFn = func(fn func()) { h.q <- fn }
	h.tt = ui.NewTester(h.a.realSettingsView, width, height)
	h.tt.SetScale(1.25)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h.tt.Frame()
	h.wait(t)
	return h
}
func (h *linkHarness) wait(t *testing.T) {
	t.Helper()
	select {
	case fn := <-h.q:
		fn()
		h.tt.Frame()
	case <-time.After(uiWaitLimit):
		t.Fatal("插件设置worker没有返回")
	}
}
func (h *linkHarness) click(t *testing.T, label string) {
	t.Helper()
	for range 8 {
		r, ok := h.tt.Find(label)
		if !ok {
			t.Fatalf("找不到 %s：%v", label, h.tt.Texts())
		}
		if r.Y >= 0 && r.Y+r.H <= float32(h.height) {
			if e := h.tt.Click(label); e != nil {
				t.Fatal(e)
			}
			h.tt.Frame()
			return
		}
		dy := float32(-400)
		if r.Y < 0 {
			dy = 400
		}
		h.tt.Scroll(float32(h.width-20), float32(h.height/2), 0, dy)
		h.tt.Frame()
	}
	t.Fatalf("不能滚动到 %s", label)
}
func TestPluginSettingsInteraction(t *testing.T) {
	h := newLinkHarness(t, 2048, 1500, 1000) // 展开设置加了翻译四行（含翻译模型）、站点页加了合并转发说明和显示简介，页面变长，高度跟着加
	h.click(t, "链接解析（link-reader）")
	h.click(t, "选择会话")
	h.wait(t)
	h.click(t, "搜索会话")
	h.tt.Type("纸灯朋友 1")
	h.tt.Frame()
	label := "纸灯朋友 1 · 91***01"
	h.click(t, label)
	h.wait(t)
	id := "qq:private:91001:92013"
	if got := h.a.linkSettings.view.Configured.Expand.Conversations; len(got) != 1 || got[0] != id {
		t.Fatal("选择没有按稳定会话ID保存", got)
	}
	h.click(t, "完成")
	h.click(t, "黑名单")
	if h.a.linkSettings.view.Configured.Expand.Mode != "whitelist" || !h.tt.HasText("所选会话不展开，其他合格QQ会话会展开") {
		t.Fatal("黑名单未确认就生效或提示不完整")
	}
	h.click(t, "确认切换")
	h.wait(t)
	if h.a.linkSettings.view.Configured.Expand.Mode != "blacklist" {
		t.Fatal("黑名单未保存")
	}
	h.click(t, "私聊开始状态")
	h.click(t, "编辑插件文字")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("编的失败提示")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	if h.a.linkSettings.view.Configured.Expand.PrivateStatus != "编的失败提示" {
		t.Fatal("文字编辑丢失")
	}
	h.click(t, "下载视频")
	h.wait(t)
	h.click(t, "单个视频超时（秒，1～600）")
	h.click(t, "编辑插件数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("45")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	e := h.a.linkSettings.view.Configured.Expand
	if !e.Video.Enabled || e.Video.TimeoutSeconds != 45 {
		t.Fatal("视频设置未保存", e)
	}
	h.fake.failure = errors.New("编的磁盘写入失败")
	h.click(t, "自动展开")
	h.wait(t)
	if h.a.linkSettings.view.Configured.Expand.Enabled || !strings.Contains(h.a.linkSettings.note, "磁盘写入失败") {
		t.Fatal("保存失败后仍显示新值")
	}
	h.fake.failure = nil
	h.a.linkSettings.leave()
	h.tt.Frame()
	h.wait(t)
	if h.a.linkSettings.view.Configured.Expand.Video.TimeoutSeconds != 45 || h.a.linkSettings.view.Configured.Expand.PrivateStatus != "编的失败提示" {
		t.Fatal("重开没有读取保存值")
	}
	for _, w := range []int{2048, 900, 480} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			h := newLinkHarness(t, w, 1030, 200)
			h.click(t, "链接解析（link-reader）")
			r, ok := h.tt.Find("自动展开")
			if !ok || r.W <= 0 || r.H < 32 || r.X < 0 || r.X+r.W > float32(w) {
				t.Fatalf("行框越界或点击目标不足：%+v", r)
			}
			t.Logf("%d DIP, scale=1.25: 自动展开行=(%.1f,%.1f %.1fx%.1f)", w, r.X, r.Y, r.W, r.H)
		})
	}
}

// 模板编辑：选站点、点按钮插入占位符、预览随文字刷新、非法时不能保存、保存后配置里有这份模板
func xSite() linktemplate.Site {
	site, _, _ := linktemplate.SiteByKey("x")
	return site
}

func TestLinkTemplateEditor(t *testing.T) {
	h := newLinkHarness(t, 2048, 1030, 20)
	h.click(t, "链接解析（link-reader）")
	h.click(t, "站点")
	h.click(t, "X / 推特")
	if h.a.linkSettings.site != "x" {
		t.Fatalf("没有切到 X：%q", h.a.linkSettings.site)
	}
	h.click(t, "消息模板")
	if !h.tt.HasText("编辑模板：X / 推特") || h.a.linkSettings.tpl != xSite().Source(false) {
		t.Fatalf("编辑框没有放进内置默认：%q", h.a.linkSettings.tpl)
	}
	// 预览是示例数据渲染的，图片类写成方块
	if !h.tt.HasText("[图片]") {
		t.Fatal("预览里该有图片的方块", h.tt.Texts())
	}
	// 点按钮：占位符追加到末尾，预览立刻刷新
	h.click(t, "$点赞")
	if !strings.HasSuffix(h.a.linkSettings.tpl, "$点赞") {
		t.Fatalf("按钮没有插入占位符：%q", h.a.linkSettings.tpl)
	}
	h.tt.Frame()
	if !h.tt.HasText("https://example.invalid/item/1\n5") && !h.tt.HasText("5") {
		t.Fatal("预览没有刷新", h.tt.Texts())
	}
	saveLinkShot(t, h, "link-template-editor.png") // 只在设了 LINK_SHOT_DIR 时存图，给人看效果
	// 写错的模板：显示友好的错误，保存不可用
	h.a.linkSettings.tpl = "$标题1是错的 $不存在"
	h.tt.Frame()
	if !h.tt.HasText("第 1 个字：不认识") && !h.tt.HasText("不认识 $") {
		t.Fatal("非法模板没有显示错误", h.tt.Texts())
	}
	writes := h.fake.writes
	if err := h.tt.Click("保存"); err == nil && h.fake.writes != writes {
		t.Fatal("非法模板不该能保存")
	}
	// 改回合法的并保存：下一条链接用的就是它
	h.a.linkSettings.tpl = "$作者｜$发布时间[｜浏览 $浏览]\n$链接"
	h.tt.Frame()
	h.click(t, "保存")
	h.wait(t)
	if got := h.a.linkSettings.view.Configured.Expand.Templates["x"]; got != "$作者｜$发布时间[｜浏览 $浏览]\n$链接" {
		t.Fatalf("模板没有保存：%q，提示：%q", got, h.a.linkSettings.note)
	}
}

// x、telegram 的“超过多少字节折成合并转发”显示字节门槛，别的站点仍显示字数；改了能写回配置（ADR-391）。
func TestLinkForwardLimitShowsBytesForX(t *testing.T) {
	h := newLinkHarness(t, 2048, 1500, 20)
	h.click(t, "链接解析（link-reader）")
	h.click(t, "站点")
	h.click(t, "X / 推特")
	if _, ok := h.tt.Find(forwardBytesTitle); !ok {
		t.Fatalf("X 没有字节门槛这一行：%v", h.tt.Texts())
	}
	if !h.tt.HasText(forwardBytesHint) {
		t.Fatal("字节门槛下面缺一行说明", h.tt.Texts())
	}
	if h.tt.HasText("超过多少字折成合并转发（0 不按字数）") {
		t.Fatal("X 不该再显示已失效的字数门槛")
	}
	if !h.tt.HasText("16500") {
		t.Fatalf("X 没显示默认 16500：%v", h.tt.Texts())
	}
	h.click(t, forwardBytesTitle)
	h.click(t, "编辑插件数值")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("16000")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wait(t)
	if got := h.a.linkSettings.view.Configured.Expand.Sites["x"].ForwardOverBytes; got == nil || *got != 16000 {
		t.Fatalf("字节门槛没写回配置：%+v，提示：%q", got, h.a.linkSettings.note)
	}
	// B 站仍按字数：没有字节那一行，有字数那一行
	h.click(t, "站点")
	h.click(t, "B站动态")
	for range 10 {
		if _, ok := h.tt.Find("超过多少字折成合并转发（0 不按字数）"); ok {
			break
		}
		h.tt.Scroll(1000, 500, 0, 400)
		h.tt.Frame()
	}
	if _, ok := h.tt.Find(forwardBytesTitle); ok {
		t.Fatalf("B 站不该出现字节门槛：%v", h.tt.Texts())
	}
	if _, ok := h.tt.Find("超过多少字折成合并转发（0 不按字数）"); !ok {
		t.Fatalf("B 站仍应显示字数门槛：%v", h.tt.Texts())
	}
}

// saveLinkShot 把当前画面存成 PNG（环境变量 LINK_SHOT_DIR 指向的目录）；没设就什么也不做。
func saveLinkShot(t *testing.T, h *linkHarness, name string) {
	dir := os.Getenv("LINK_SHOT_DIR")
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
