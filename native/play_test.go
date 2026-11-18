package main

// 第十步的测试：动图在视图里按时间推帧、循环用完停住、离屏不推、减少动态效果只显示首帧、看大图只显示首帧、
// 同屏 20 张的帧数和每秒上传字节数（规格 4.3，T-anim-3、T-play、T-offscreen、T-灯箱、T-cache-key、T-count）。
// 数据全是编的：动图是测试里现造的 GIF，取图走假网络，不连 7720，不读 data/。

import (
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// liveTester 不减少动态效果的 Tester（testerOf 为了让入场动画直接到位，把 ReduceMotion 打开了）
func liveTester(w, h int, view func(c *ui.Context)) *ui.Tester {
	tt := ui.NewTester(view, w, h)
	tt.SetPreferences(ui.Preferences{TextScale: 1})
	tt.Frame()
	return tt
}

func animJSON(hash string, w, h int, sticker bool) string {
	extra := ""
	if sticker {
		extra = `,"sticker":true`
	}
	return fmt.Sprintf(`{"type":"image","hash":%q,"width":%d,"height":%d,"animated":true%s}`, hash, w, h, extra)
}

var threeColors = []colorN{cRed, cGreen, cBlue}

// animSetup 一个带动图的聊天：hash → GIF 字节
func animSetup(t *testing.T, files map[string][]byte, msgs ...*msg) (*app, *uiQueue, *imgNet) {
	t.Helper()
	n := &imgNet{files: files}
	a, q := imgApp(t, n, msgs...)
	return a, q, n
}

func animationEntry(t *testing.T, a *app, hash string) *imgEntry {
	t.Helper()
	for k, e := range a.is().m {
		if k.hash == hash && k.anim {
			return e
		}
	}
	t.Fatalf("没有动图记录 %s", hash)
	return nil
}

// 贴纸按时间循环，读取一次原件；上传量对应实际解码帧，不能随控件尺寸重定时。
func TestStickerAnimPlaysAndLoops(t *testing.T) {
	gif3 := gifSample(t, 300, 300, threeColors, []int{10, 10, 10}, 0) // 每帧 100 毫秒
	a, q, n := animSetup(t, map[string][]byte{"s": gif3}, imgMsg(false, "a", animJSON("s", 300, 300, true)))
	tt := liveTester(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	e := animationEntry(t, a, "qq/s")
	if e.state != imgOK || e.anim == nil || len(e.anim.frames) != 3 {
		t.Fatalf("动图没解出来：状态 %d、%v", e.state, e.anim)
	}
	if got := n.tiers("s"); len(got) != 1 || got[0] != 0 {
		t.Fatalf("请求的档 %v，动图要取原文件（档 0）一次", got)
	}
	want := []int{0, 1, 2, 0, 1}
	for i, step := range []time.Duration{0, 100, 100, 100, 100} {
		tt.Advance(step*time.Millisecond + 10*time.Millisecond)
		if got := e.anim.cur; got != want[i] {
			t.Fatalf("第 %d 步（%v 之后）在第 %d 帧，要第 %d 帧", i, step, got, want[i])
		}
	}
	w, h := e.bmp.Size()
	if st := a.is(); st.updates != 4 || st.uploaded != 4*w*h*4 {
		t.Fatalf("推了%d次、上传%d字节，实际帧%d×%d", st.updates, st.uploaded, w, h)
	}
}

// 只播一次的（没有 NETSCAPE 块）：播完停在末帧，视图不再要帧
func TestAnimPlayOnceStopsAndStopsAsking(t *testing.T) {
	g := gifSample(t, 64, 64, []colorN{cRed, cGreen}, []int{5, 5}, -1)
	a, q, _ := animSetup(t, map[string][]byte{"s": g}, imgMsg(false, "a", animJSON("s", 64, 64, true)))
	tt := liveTester(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	e := animationEntry(t, a, "qq/s")
	tt.Advance(time.Second)
	if e.anim.cur != 1 {
		t.Fatalf("播完在第 %d 帧，要停在末帧（1）", e.anim.cur)
	}
	// 播完后再过 10 秒：没有排着的重画，也没有再推帧
	updates := a.is().updates
	tt.Advance(10 * time.Second)
	if tt.FramePending() || a.is().updates != updates {
		t.Fatalf("播完还在要帧：FramePending=%v，推帧 %d → %d", tt.FramePending(), updates, a.is().updates)
	}
}

// T-anim-3：减少动态效果只显示第一帧，视图不要帧
func TestAnimReduceMotionShowsFirstFrame(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{5, 5, 5}, 0)
	a, q, _ := animSetup(t, map[string][]byte{"s": g}, imgMsg(false, "a", animJSON("s", 64, 64, true)))
	tt := testerOf(900, 600, func(c *ui.Context) { a.chatView(c) }) // ReduceMotion 开着
	settleImgs(a, q, tt)
	e := animationEntry(t, a, "qq/s")
	if e.state != imgOK || e.anim == nil {
		t.Fatalf("动图没解出来")
	}
	tt.Advance(2 * time.Second)
	if e.anim.cur != 0 || a.is().updates != 0 || tt.FramePending() {
		t.Fatalf("减少动态效果时在第 %d 帧、推帧 %d 次、FramePending=%v，要停在第 0 帧、不推、不要帧",
			e.anim.cur, a.is().updates, tt.FramePending())
	}
}

// advance 的三种模式（离屏的行不推帧不排重画；播过又改成减少动态效果，回到第一帧）
func TestAdvanceModes(t *testing.T) {
	an := newAnimation([]*image.RGBA{rgbaOf(1), rgbaOf(2), rgbaOf(3)}, []int{100, 100, 100}, 0)
	e := &imgEntry{state: imgOK, anim: an, bmp: ui.NewBitmap(an.frames[0])}
	st := newImageStore(nil, nil)
	mode := playLive
	builds := 0
	tt := liveTester(100, 100, func(c *ui.Context) {
		builds++
		st.advance(c, e, mode)
		ui.Box(c).Size(10, 10)
	})
	tt.Advance(250 * time.Millisecond)
	if an.cur != 2 || builds < 3 {
		t.Fatalf("在屏上：第 %d 帧、构建 %d 次，要第 2 帧、至少 3 次", an.cur, builds)
	}
	mode = playHold
	tt.Frame()
	built := builds
	tt.Advance(5 * time.Second)
	if builds > built+1 {
		t.Fatalf("不在可见范围还在要帧：构建 %d → %d", built, builds)
	}
	if an.cur != 2 {
		t.Fatalf("不在可见范围，画面应停在原处（第 2 帧），在第 %d 帧", an.cur)
	}
	mode = playStill
	tt.Frame()
	if an.cur != 0 {
		t.Fatalf("改成减少动态效果后在第 %d 帧，要回到第一帧", an.cur)
	}
}

// T-offscreen：300 行，滚在底部，只有可见行取图、解码、推帧；顶上的动图所在行没构建，不取
func TestOffscreenAnimNotFetchedNorPlayed(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{5, 5, 5}, 0)
	var msgs []*msg
	for i := 0; i < 300; i++ {
		switch i {
		case 0:
			msgs = append(msgs, imgMsg(false, fmt.Sprintf("m%d", i), animJSON("top", 64, 64, true)))
		case 299:
			msgs = append(msgs, imgMsg(false, fmt.Sprintf("m%d", i), animJSON("bottom", 64, 64, true)))
		default:
			msgs = append(msgs, imgMsg(false, fmt.Sprintf("m%d", i), fmt.Sprintf("第 %d 句闲话", i)))
		}
	}
	a, q, n := animSetup(t, map[string][]byte{"top": g, "bottom": g}, msgs...)
	tt := liveTester(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	tt.Advance(time.Second)
	if got := n.tiers("top"); len(got) != 0 {
		t.Fatalf("顶上离屏的动图被取了：%v", got)
	}
	for key := range a.is().m {
		if key.hash == "qq/top" {
			t.Fatal("离屏动图有缓存记录")
		}
	}
	e := animationEntry(t, a, "qq/bottom")
	if e.state != imgOK || e.anim == nil || a.is().updates == 0 {
		t.Fatalf("可见动图没有播放：%v，推帧%d次", e, a.is().updates)
	}
}

// 同一张图出现在两条消息里：一条记录、一次解码，每次到点只推一次帧（不是两次）
func TestSameAnimTwiceOnScreenUpdatesOnce(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{10, 10, 10}, 0)
	a, q, n := animSetup(t, map[string][]byte{"s": g},
		imgMsg(false, "a", animJSON("s", 64, 64, true)), imgMsg(false, "b", animJSON("s", 64, 64, true)))
	tt := liveTester(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	if got := n.tiers("s"); len(got) != 1 {
		t.Fatalf("同一张图取了 %d 次", len(got))
	}
	if a.is().decoded.Load() != 1 {
		t.Fatalf("解码 %d 次，要 1 次", a.is().decoded.Load())
	}
	before := a.is().updates
	tt.Advance(350 * time.Millisecond)
	if got := a.is().updates - before; got != 3 {
		t.Fatalf("350 毫秒（帧间隔 100）里推了 %d 次，要 3 次（一个动图一次）", got)
	}
}

// 看大图里的动图（2026-11-02 主人："点开预览发现gif不会动"）：正在看的这张用列表里在播的位图，前后各一张只备首帧
// （stillBitmap，另一个位图，不被播放改写）；关着的时候不建首帧；都不取 1536 档
func TestAnimLightboxPlaysCurrent(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{10, 10, 10}, 0)
	a, q, n := animSetup(t, map[string][]byte{"p": g}, imgMsg(false, "a", "看", animJSON("p", 64, 64, false)))
	tt := liveTester(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	e := animationEntry(t, a, "qq/p")
	if e.anim == nil {
		t.Fatal("动图没解出来")
	}
	imgs := []seg{
		segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "p"}, Animated: true}),
		segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "p"}, Animated: true}),
	}
	shown := []*imgEntry{e, e}
	closed := a.largeOf(imgs, shown, false, 0)
	if closed[0] != e.bmp || e.still != nil {
		t.Fatal("看大图关着的时候不该建首帧位图")
	}
	open := a.largeOf(imgs, shown, true, 0)
	if open[0] != e.bmp {
		t.Fatal("正在看的动图要用在播的位图")
	}
	if open[1] != e.still || open[1] == nil || open[1] == e.bmp {
		t.Fatal("旁边那张只备首帧位图")
	}
	for _, tier := range n.tiers("p") {
		if tier != 0 {
			t.Fatalf("动图的看大图不该取别的档，请求了 %v", n.tiers("p"))
		}
	}
	if a.is().peek(imgKey{"qq/p", lightboxTier, false}) != nil {
		t.Fatal("动图的看大图取了 1536 档")
	}
}

// 点开一张 GIF 看大图，时间往前走，帧要换（看大图开着时也在推帧）
func TestAnimLightboxAdvances(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{10, 10, 10}, 0)
	a, q, _ := animSetup(t, map[string][]byte{"p": g}, imgMsg(false, "a", animJSON("p", 64, 64, false)))
	tt := liveTester(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	e := animationEntry(t, a, "qq/p")
	if err := tt.Click("图片"); err != nil {
		t.Fatalf("点图：%v（%v）", err, tt.Texts())
	}
	tt.Frame()
	seen := map[int]bool{e.anim.cur: true}
	for range 6 {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
		seen[e.anim.cur] = true
	}
	if len(seen) < 2 {
		t.Fatalf("看大图开着 300 毫秒，帧一直是 %v", seen)
	}
}

// 普通动图悬停显示 GIF 角标；静态图和贴纸没有。
func TestGifBadge(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{10, 10, 10}, 0)
	n := &imgNet{files: map[string][]byte{"anim": g}, orig: map[string][2]int{"still": {64, 64}}}
	a, q := imgApp(t, n, imgMsg(false, "a", animJSON("anim", 64, 64, false)), imgMsg(false, "b", imgJSON("still", 64, 64)))
	tt := liveTester(900, 900, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	image, _ := tt.Find("图片")
	tt.Move(image.X+image.W/2, image.Y+image.H/2)
	tt.Frame()
	rs := 0
	for _, s := range tt.Texts() {
		if s == "GIF" {
			rs++
		}
	}
	if rs != 1 {
		t.Fatalf("角标出现 %d 次，要 1 次（只有动图那条）：%q", rs, tt.Texts())
	}
	// 贴纸：不加角标
	a2, q2, _ := animSetup(t, map[string][]byte{"s": g}, imgMsg(false, "a", animJSON("s", 64, 64, true)))
	tt2 := liveTester(900, 600, func(c *ui.Context) { a2.chatView(c) })
	settleImgs(a2, q2, tt2)
	if _, ok := tt2.Find("GIF"); ok {
		t.Fatal("贴纸有角标")
	}
}

// 超预算只留首帧：位图有、没有动图；首帧位图就是 bmp（角标由调用方照 seg.Animated 画，见 TestGifBadge）
func TestOverBudgetEntryHasNoAnim(t *testing.T) {
	t.Parallel()
	big := gifSample(t, 1024, 1024, []colorN{cRed, cGreen, cBlue, cRed, cGreen, cBlue, cRed, cGreen}, []int{5, 5, 5, 5, 5, 5, 5, 5}, 0)
	st := newImageStore(nil, nil)
	an, b, err := st.decodeAnim(big, 1024) // 8 帧 × 1024 × 1024 × 4 = 32 MiB，超过 24 MiB
	if err != nil || an != nil || b == nil {
		t.Fatalf("动图 %v、位图 %v、错误 %v", an, b, err)
	}
	if e := (&imgEntry{state: imgOK, bmp: b}); e.stillBitmap() != b {
		t.Fatal("没有动图时首帧位图就是 bmp")
	}
}

func rgbaOf(v uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img
}

// 同屏 20 张动图（规格 4.3）：帧数看各自间隔的并集；量每秒上传字节和每帧耗时。数字写进提交说明
func TestTwentyAnimsOnScreen(t *testing.T) {
	type cfg struct {
		name    string
		sticker bool
		srcW    int
		srcH    int
		delays  func(i int) int // 厘秒
		builds  [2]int          // 1 秒里构建次数的范围
	}
	cases := []cfg{
		{"20 张贴纸，间隔都是 100 毫秒", true, 300, 300, func(int) int { return 10 }, [2]int{9, 12}},
		{"20 张贴纸，一半 40 毫秒一半 100 毫秒", true, 300, 300, func(i int) int { return []int{4, 10}[i%2] }, [2]int{28, 32}},
		{"20 张普通动图，间隔都是 100 毫秒", false, 600, 450, func(int) int { return 10 }, [2]int{9, 12}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string][]byte{}
			var msgs []*msg
			for i := 0; i < 20; i++ {
				h := fmt.Sprintf("a%d", i)
				d := c.delays(i)
				files[h] = gifSample(t, c.srcW, c.srcH, []colorN{cRed, cGreen, cBlue, cYellow}, []int{d, d, d, d}, 0)
				msgs = append(msgs, imgMsg(false, h, animJSON(h, c.srcW, c.srcH, c.sticker)))
			}
			a, q, _ := animSetup(t, files, msgs...)
			builds := 0
			tt := liveTester(900, 7000, func(ctx *ui.Context) { builds++; a.chatView(ctx) })
			settleImgs(a, q, tt)
			st := a.is()
			playing := 0
			for _, e := range st.m {
				if e.anim != nil {
					playing++
				}
			}
			if playing != 20 {
				t.Fatalf("解出动图 %d 张，要 20 张", playing)
			}
			tt.Advance(time.Second) // 入场动画、列表跟随到底这些开头的动静放完，再量
			frameBytes := len(animationEntry(t, a, "qq/a0").anim.frames[0].Pix)
			builds0, up0, upd0 := builds, st.uploaded, st.updates
			start := time.Now()
			tt.Advance(time.Second)
			el := time.Since(start)
			nb, bytes, upd := builds-builds0, st.uploaded-up0, st.updates-upd0
			if nb < c.builds[0] || nb > c.builds[1] {
				t.Fatalf("1 秒里构建 %d 次，要在 %v 之间（各自间隔的并集，不是 20 倍）", nb, c.builds)
			}
			if bytes > 96<<20 {
				t.Fatalf("每秒上传 %.1f MB，超过 96 MB 的暂定线", float64(bytes)/(1<<20))
			}
			t.Logf("同屏 20 张：1 秒构建 %d 次、推帧 %d 次、上传 %.1f MB（每帧 %d KB）；Tester 里每次构建平均 %.1f ms（含 CPU 光栅，不是真窗口的数）",
				nb, upd, float64(bytes)/(1<<20), frameBytes/1024, float64(el.Milliseconds())/float64(nb))
			if upd > 0 && bytes/upd != frameBytes {
				t.Fatalf("每帧上传%d字节，实际帧%d字节", bytes/upd, frameBytes)
			}
		})
	}
}

// 解码不在视图里重做（T-count）：动图播了几秒，decode 的次数不变
func TestAnimNoRedecodeWhilePlaying(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{5, 5, 5}, 0)
	a, q, n := animSetup(t, map[string][]byte{"s": g}, imgMsg(false, "a", animJSON("s", 64, 64, true)))
	tt := liveTester(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	tt.Advance(3 * time.Second)
	if a.is().decoded.Load() != 1 || len(n.tiers("s")) != 1 {
		t.Fatalf("播放时解码 %d 次、取 %d 次，要各 1 次", a.is().decoded.Load(), len(n.tiers("s")))
	}
}

// 预判错了的老数据（没有 animated 标记的 GIF）走静态路径，只显示首帧，不报错
func TestUnflaggedGifShowsFirstFrame(t *testing.T) {
	g := gifSample(t, 64, 64, threeColors, []int{5, 5, 5}, 0)
	a, q, _ := animSetup(t, map[string][]byte{"o": g}, imgMsg(false, "a", imgJSON("o", 64, 64)))
	tt := liveTester(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	e := a.is().peek(imgKey{"qq/o", 128, false})
	if e == nil || e.state != imgOK || e.anim != nil {
		t.Fatalf("老数据：%v", e)
	}
}

// 行在不在可见范围：首尾行之内才算（上下各 2 行的预构建、焦点行在范围外，不推帧）
func TestRowVisible(t *testing.T) {
	t.Parallel()
	a := &app{}
	st := a.is()
	st.frame, a.visFrame, a.visFirst, a.visLast = 5, 5, 10, 20
	for row, want := range map[int]bool{8: false, 9: false, 10: true, 15: true, 20: true, 21: false, 22: false} {
		a.rowAt = row
		if got := a.rowVisible(); got != want {
			t.Errorf("第 %d 行 visible=%v，要 %v", row, got, want)
		}
	}
}
