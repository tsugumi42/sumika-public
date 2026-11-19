package main

import (
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/assets"
	"sumika-native/internal/catalog"
	"sumika-native/internal/chatui"
	"sumika-native/internal/uitest"
	"sumika-native/internal/winshell"
)

// 默认窗口的逻辑尺寸，和 main.go 一致；屏幕是 125% 缩放。
const testW, testH = windowWidth, windowHeight

func testGallery(t *testing.T) *gallery {
	t.Helper()
	chatui.EmojiDir = assets.TwemojiDir()
	t.Cleanup(func() { chatui.EmojiDir = "" })
	return newGallery(testFont(), "")
}

// testFont 一个进程只注册一次仓库里的文渊圆体子集（466 KB）。不用 loadLook：它每次都注册完整字体，
// 而 mygo 的字体表只进不出、每个新 Tester 都整份复制一遍，整包跑下来超过 8 GB（2026-11-18）。
func testFont() string {
	testFontOnce.Do(func() {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", "WenYuanTiny.ttf"))
		if err == nil && ui.RegisterFont(data, "SumikaTestRounded") == nil {
			testFontName = "SumikaTestRounded"
		}
	})
	return testFontName
}

var (
	testFontOnce sync.Once
	testFontName string
)

func open(t *testing.T, g *gallery) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(g.view, testW, testH)
	tt.SetScale(1.25)
	tt.Frame()
	tt.Advance(2 * time.Second)
	return tt
}

// nodes 收集树里满足条件的节点（先序）。
func nodes(n *uitest.Node, pred func(*uitest.Node) bool, out *[]*uitest.Node) {
	if pred(n) {
		*out = append(*out, n)
	}
	for _, ch := range n.Children {
		nodes(ch, pred, out)
	}
}

// clickLast 点最后一个（也就是最靠下的，试一试里的）名字为 label 的控件的中心：状态排里有同名的，点它没有可见结果。
func clickLast(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	var got []*uitest.Node
	nodes(uitest.Tree(tt), func(n *uitest.Node) bool { return (n.Label == label || n.Text == label) && n.Box.W > 0 && !n.Hidden }, &got)
	if len(got) == 0 {
		t.Fatalf("找不到 %q", label)
	}
	b := got[len(got)-1].Box
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
	tt.Advance(50 * time.Millisecond)
}

// saveShot 在设了 GALLERY_SHOT 时把整个窗口存成 window 子目录里的 PNG。
func saveShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("GALLERY_SHOT")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "window"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "window", name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func want(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	if !tt.HasText(text) {
		t.Errorf("界面上应有 %q；现在有 %q", text, tt.Texts())
	}
}

// 每个条目在亮、暗下打开，开着“显示层”也不崩；默认窗口里一族的状态排和试一试在一屏内放得下（不用滚动）。
// 字号拉到最大时放不放得下只报告不判错。设 GALLERY_SHOT=目录 时把整个窗口存成 PNG（window 子目录）。
func TestGalleryEntries(t *testing.T) {
	shot := os.Getenv("GALLERY_SHOT")
	if shot != "" {
		if err := os.MkdirAll(filepath.Join(shot, "window"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range catalog.All() {
		for _, dark := range []bool{false, true} {
			mode := map[bool]string{false: "light", true: "dark"}[dark]
			t.Run(e.ID+"/"+mode, func(t *testing.T) {
				g := testGallery(t)
				g.sel = e.ID
				if dark {
					g.dark = 1
				}
				tt := open(t, g)
				root := uitest.Tree(tt)
				body, scroll := root.Find("stage-body/"+e.ID), root.Find("stage-scroll")
				if body == nil || scroll == nil {
					t.Fatal("树里没有舞台")
				}
				if over := body.Box.Y + body.Box.H - (scroll.Box.Y + scroll.Box.H); over > 0 {
					t.Errorf("默认窗口 %dx%d 里舞台内容超出 %.0f 像素，要滚动", testW, testH, over)
				}
				// 字号最大
				g.text, g.radius, g.density = chatui.TextMax, chatui.RadiusMax, chatui.DensityMax
				tt.Frame()
				tt.Advance(time.Second)
				root = uitest.Tree(tt)
				body, scroll = root.Find("stage-body/"+e.ID), root.Find("stage-scroll")
				t.Logf("字号、圆角、疏密都拉到最大：内容高 %.0f，可用高 %.0f（超出 %.0f）", body.Box.H, scroll.Box.H, max(0, body.Box.Y+body.Box.H-(scroll.Box.Y+scroll.Box.H)))
				g.text, g.radius, g.density = chatui.TextDefault, chatui.RadiusDefault, chatui.DensityDefault
				tt.Frame()
				tt.Advance(time.Second)

				saveShot(t, tt, e.ID+"-"+mode)
				// 显示层
				g.layers = true
				tt.Frame()
				tt.Advance(time.Second)
				if g.layerErr != "" {
					t.Fatalf("显示层不可用：%s", g.layerErr)
				}
				if g.depth < 1 {
					t.Errorf("显示层没数到任何容器，depth=%d", g.depth)
				}
				t.Logf("最深 %d 层", g.depth)
				if shot != "" {
					f, err := os.Create(filepath.Join(shot, "window", e.ID+"-"+mode+"-layers.png"))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					if err := png.Encode(f, tt.Image()); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

// 每个组件族“试一试”里点一下，看得见的结果真的变了。真窗口里的手感、悬停和按下的动画这里验证不了。
func TestGalleryInteractions(t *testing.T) {
	pick := func(t *testing.T, id string) (*gallery, *ui.Tester) {
		g := testGallery(t)
		g.sel = id
		return g, open(t, g)
	}
	t.Run("button", func(t *testing.T) {
		_, tt := pick(t, "button")
		want(t, tt, "已经发出 0 条。对话还没收藏。")
		clickLast(t, tt, "发送给晴枝")
		want(t, tt, "已经发出 1 条。对话还没收藏。")
		clickLast(t, tt, "收藏这段对话")
		want(t, tt, "已经发出 1 条。对话已收藏。")
		clickLast(t, tt, "撤回上一条")
		want(t, tt, "已经发出 0 条。对话已收藏。")
		saveShot(t, tt, "button-after-clicks")
	})
	t.Run("choice", func(t *testing.T) {
		_, tt := pick(t, "choice")
		clickLast(t, tt, "每月")
		want(t, tt, "会在每月 1 号早上 9 点提醒你整理纸灯笔记。")
		clickLast(t, tt, "晴枝")
		want(t, tt, "显示 晴枝 的 17 条对话。")
		clickLast(t, tt, "睡前提醒喝水")
		clickLast(t, tt, "同步到另一台电脑")
		want(t, tt, "睡前提醒：开；同步：已勾选。")
	})
	t.Run("input", func(t *testing.T) {
		_, tt := pick(t, "input")
		clickLast(t, tt, "每天提醒几次 加")
		want(t, tt, "每天提醒 4 次。")
		clickLast(t, tt, "每天提醒几次 减")
		clickLast(t, tt, "每天提醒几次 减")
		want(t, tt, "每天提醒 2 次。")
		clickLast(t, tt, "搜索笔记")
		tt.Type("纸")
		want(t, tt, "找到 1 条：纸灯.md")
		clickLast(t, tt, "回复用的模型")
		tt.Advance(300 * time.Millisecond) // 下拉是入口自己长开的，等它长到位再点选项
		clickLast(t, tt, "本地小模型")
		want(t, tt, "之后的回复用「本地小模型」。")
		var sl []*uitest.Node
		nodes(uitest.Tree(tt), func(n *uitest.Node) bool { return n.Label == "提示音音量" && n.Box.W > 100 }, &sl)
		if len(sl) == 0 {
			t.Fatal("找不到滑块")
		}
		b := sl[len(sl)-1].Box
		tt.ClickAt(b.X+b.W-4, b.Y+b.H/2) // 点到滑块最右端
		tt.Advance(50 * time.Millisecond)
		want(t, tt, "提示音音量 100%。")
	})
	t.Run("bubble", func(t *testing.T) {
		_, tt := pick(t, "bubble")
		clickLast(t, tt, "写一句话")
		tt.Type("明天下午三点开会")
		clickLast(t, tt, "发送")
		tt.Advance(time.Second)
		want(t, tt, "明天下午三点开会")
		want(t, tt, "这个想法不错，晚点我把它排进今天的事里。")
		clickLast(t, tt, "写一句话")
		tt.Type("这一条发不出去")
		clickLast(t, tt, "发一条失败的")
		tt.Advance(time.Second)
		if _, ok := tt.Access("发送失败，重试"); !ok {
			t.Fatal("失败的消息旁边应有重试角标")
		}
		clickLast(t, tt, "发送失败，重试")
		tt.Advance(2 * time.Second)
		if _, ok := tt.Access("发送失败，重试"); ok {
			t.Error("点了重试等一会儿，角标应当消失")
		}
		saveShot(t, tt, "bubble-after-send")
	})
	t.Run("approval", func(t *testing.T) {
		_, tt := pick(t, "approval")
		want(t, tt, "还在等你决定。")
		clickLast(t, tt, "允许一次")
		want(t, tt, "已发出，等后台确认。")
		tt.Advance(2 * time.Second)
		want(t, tt, "允许了，命令执行完了。")
		clickLast(t, tt, "重来")
		want(t, tt, "还在等你决定。")
		clickLast(t, tt, "更多选择")
		found := false
		for _, it := range tt.Menu() {
			if it == "拒绝并说明原因…" {
				found = true
			}
		}
		if !found {
			t.Errorf("“⋯”里应有“拒绝并说明原因…”，现在是 %q", tt.Menu())
		}
		tt.CloseMenu()
		clickLast(t, tt, "拒绝")
		want(t, tt, "拒绝了，命令没有执行。")
		saveShot(t, tt, "approval-after-deny")
	})
}

// 切换：点列表、J / K、上下方向键都立刻换，筛选框里打字能筛；在筛选框里打 j 是打字，不切。
func TestGallerySwitching(t *testing.T) {
	g := testGallery(t)
	tt := open(t, g)
	all := catalog.All()
	if g.sel != all[0].ID {
		t.Fatalf("一开始应选第一条，现在是 %s", g.sel)
	}
	tt.Key(0, ui.KeyJ)
	tt.Frame()
	if g.sel != all[1].ID {
		t.Errorf("按 J 应到第二条，现在是 %s", g.sel)
	}
	tt.Key(0, ui.KeyK)
	tt.Frame()
	if g.sel != all[0].ID {
		t.Errorf("按 K 应回第一条，现在是 %s", g.sel)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if g.sel != all[1].ID {
		t.Errorf("按下方向键应到第二条，现在是 %s", g.sel)
	}
	if err := tt.Click(all[4].Family); err != nil {
		t.Fatal(err)
	}
	if g.sel != all[4].ID {
		t.Errorf("点“%s”应选中它，现在是 %s", all[4].Family, g.sel)
	}
	if err := tt.Click("筛选组件"); err != nil {
		t.Fatal(err)
	}
	tt.Type("jj气泡")
	tt.Advance(100 * time.Millisecond)
	if g.filter != "jj气泡" {
		t.Errorf("在筛选框里打字应进输入框，现在 filter=%q", g.filter)
	}
	g.filter = "气泡"
	tt.Frame()
	if g.sel != "bubble" || len(g.visible()) != 1 {
		t.Errorf("筛“气泡”应只剩聊天气泡，现在选中 %s，剩 %d 条", g.sel, len(g.visible()))
	}
}

// 改外观开关，舞台立刻跟着变：夜间、字号、圆角、疏密、底色、材质各改一次，令牌都换了，顶栏那套不动。
func TestGalleryAppearance(t *testing.T) {
	g := testGallery(t)
	tt := open(t, g)
	base, chromeText := g.stage.G.BodySize, g.chrome.G.BodySize
	g.dark, g.neutral, g.material, g.text, g.radius, g.density = 1, 2, 1, chatui.TextMax, chatui.RadiusMax, chatui.DensityMax
	tt.Frame()
	if !g.stage.S.Dark || g.stage.S.Text != chatui.TextMax || g.stage.S.Radius != chatui.RadiusMax || g.stage.S.Density != chatui.DensityMax {
		t.Errorf("舞台令牌没跟着变：%+v", g.stage.S)
	}
	if g.stage.G.BodySize <= base {
		t.Errorf("字号拉到最大，舞台正文字号应变大：%v → %v", base, g.stage.G.BodySize)
	}
	if g.chrome.G.BodySize != chromeText || g.chrome.S.Text != chatui.TextDefault {
		t.Errorf("顶栏和列表不该跟着字号滑块变")
	}
	if g.stage.Mat != 1 {
		t.Errorf("材质没接上，Mat=%v", g.stage.Mat)
	}
}

// 选中和切换：条目里点一下，现在在哪看得见；顶栏的“重放”（和 R 键）把状态拨回初始；慢放只罩住舞台，每一帧建完倍数还原成 1。
func TestGalleryMotionEntryReplayAndSlow(t *testing.T) {
	g := testGallery(t)
	g.sel = "motion"
	tt := open(t, g)
	want(t, tt, "现在在“聊天”。")
	clickLast(t, tt, "运行")
	tt.Advance(time.Second)
	want(t, tt, "现在在“运行”。")
	if err := tt.Click("重放 R"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	want(t, tt, "现在在“聊天”。")
	if g.replays != 1 {
		t.Errorf("按“重放”后计数 %d，应为 1", g.replays)
	}
	clickLast(t, tt, "设置")
	tt.Advance(time.Second)
	want(t, tt, "现在在“设置”。")
	tt.Key(0, ui.KeyR)
	tt.Advance(time.Second)
	want(t, tt, "现在在“聊天”。")
	if g.replays != 2 {
		t.Errorf("按 R 后计数 %d，应为 2", g.replays)
	}

	// 慢放 1/10：点一下分段，不到一秒还没滑到位；正常速度下早就到位了。倍数每帧建完都回到 1。
	probe := func(speed int) float32 {
		g := testGallery(t)
		g.sel, g.speed = "motion", speed
		tt := open(t, g)
		clickLast(t, tt, "每年")
		tt.Advance(800 * time.Millisecond)
		tt.Frame()
		if winshell.MotionScale() != 1 {
			t.Fatalf("速度 %d：建完一帧后慢放倍数没有还原：%v", speed, winshell.MotionScale())
		}
		hs := g.stage.SegHandles[g.sel+"/seg"]
		x := hs.Slide.Rect.X
		tt.Advance(20 * time.Second)
		tt.Frame()
		return abs32f(x - hs.Slide.Rect.X)
	}
	if normal, slow := probe(0), probe(2); normal > 1 || slow < 10 {
		t.Errorf("800 毫秒后离终点：正常 %.1f（应到位），1/10 慢放 %.1f（应还在路上）", normal, slow)
	}
}

func abs32f(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
