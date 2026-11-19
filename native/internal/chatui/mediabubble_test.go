package chatui

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

var (
	mbRed   = color.RGBA{255, 0, 0, 255}
	mbReady = func() MediaResult {
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+3] = 255, 255
		}
		return MediaResult{State: MediaReady, Image: ui.NewBitmap(img)}
	}()
)

func imgSeg(pos, w, h int) segview.Segment {
	return segview.Segment{Kind: "image", Position: pos, Width: w, Height: h}
}

func imgMsg(id string, self bool, text string, segs ...segview.Segment) *Msg {
	blocks := []segview.Block{{Kind: segview.Images, Imgs: segs}}
	if text != "" {
		blocks = append(blocks, segview.Block{Kind: segview.Inline, Runs: []segview.Run{{Text: text}}})
	}
	return &Msg{ID: id, Self: self, Name: "甲", Sender: "甲", At: time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local), Blocks: blocks}
}

type mbRig struct {
	tt *ui.Tester
}

func mbRender(t *testing.T, h Host, m *Msg) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("page")).Padding(20).Background(ui.RGB(240, 240, 240)).Children(func() { h.Message(c, m, nil, nil) })
	}, 700, 900)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return tt
}

func mbHost(res MediaResult) Host {
	return Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true, Media: &blockMedia{state: res}}
}

func mbNear(a, b float32) bool { d := a - b; return d > -0.6 && d < 0.6 }

func mustFind(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("没找到 %q：%v", label, tt.Texts())
	}
	return r
}

// 只有图片：图的矩形就是气泡的矩形，时间牌子在图右下角范围里；横图、竖图、超长图都一样。
func TestImageOnlyFillsBubble(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{{"横图", 800, 400}, {"竖图", 400, 800}, {"超长图", 100, 1500}, {"超宽图", 2000, 100}} {
		for _, self := range []bool{false, true} {
			h := mbHost(mbReady)
			m := imgMsg("i", self, "", imgSeg(0, c.w, c.h))
			tt := mbRender(t, h, m)
			bub := mustFind(t, tt, "气泡")
			img := mustFind(t, tt, "查看图片")
			if !mbNear(img.X, bub.X) || !mbNear(img.Y, bub.Y) || !mbNear(img.W, bub.W) || !mbNear(img.H, bub.H) {
				t.Fatalf("%s self=%v：图 %+v 应该贴满气泡 %+v", c.name, self, img, bub)
			}
			if img.W > mediaMaxW+0.5 || img.H > mediaMaxH+0.5 {
				t.Fatalf("%s：图 %.1f×%.1f 超过上限", c.name, img.W, img.H)
			}
			chip := mustFind(t, tt, "时间牌子")
			if chip.X < img.X+img.W/2 || chip.Y < img.Y+img.H/2 || chip.X+chip.W > img.X+img.W+0.5 || chip.Y+chip.H > img.Y+img.H+0.5 {
				t.Fatalf("%s：时间牌子 %+v 不在图 %+v 的右下", c.name, chip, img)
			}
			// 圆角裁切：气泡左上角的像素不是图的红色（被裁掉了），中心是红色
			px := tt.Image()
			if px == nil {
				continue
			}
			if got := px.RGBAAt(int(bub.X)+1, int(bub.Y)+1); got == mbRed {
				t.Fatalf("%s：气泡左上角还是图的颜色，圆角没有裁到图 %v", c.name, got)
			}
			if got := px.RGBAAt(int(bub.X+bub.W/2), int(bub.Y+bub.H/2)); got != mbRed {
				t.Fatalf("%s：图中心 %v 不是红色", c.name, got)
			}
		}
	}
}

// 加载前后气泡尺寸不变。
func TestImageBubbleSizeStableWhileLoading(t *testing.T) {
	for _, withText := range []string{"", "一句配文"} {
		var rects []ui.Rect
		for _, res := range []MediaResult{{State: MediaLoading}, mbReady, {State: MediaFailed}} {
			m := imgMsg("i", true, withText, imgSeg(0, 640, 480))
			rects = append(rects, mustFind(t, mbRender(t, mbHost(res), m), "气泡"))
		}
		for _, r := range rects[1:] {
			if !mbNear(r.W, rects[0].W) || !mbNear(r.H, rects[0].H) {
				t.Fatalf("配文 %q：加载前后气泡尺寸变了 %+v vs %+v", withText, rects[0], r)
			}
		}
	}
}

// 图 + 配文：图贴着气泡的上边和左右边，下边是直的、接着文字区；时间在文字行尾（图下面，不叠在图上）。
func TestImageWithCaption(t *testing.T) {
	h := mbHost(mbReady)
	m := imgMsg("c", false, "一句配文", imgSeg(0, 640, 480))
	tt := mbRender(t, h, m)
	bub, img := mustFind(t, tt, "气泡"), mustFind(t, tt, "查看图片")
	if !mbNear(img.X, bub.X) || !mbNear(img.Y, bub.Y) || !mbNear(img.W, bub.W) {
		t.Fatalf("图 %+v 应贴着气泡 %+v 的上、左、右", img, bub)
	}
	if img.H >= bub.H-h.T.BodyLine {
		t.Fatalf("图高 %.1f、气泡高 %.1f：下面应有文字区", img.H, bub.H)
	}
	if _, ok := tt.Find("时间牌子"); ok {
		t.Fatal("有配文时时间应在文字行尾，不该是叠在图上的牌子")
	}
	// 图下边是直角：气泡左下角（圆角）以外的图角位置，图的左下角像素必须是图的颜色
	if px := tt.Image(); px != nil {
		if got := px.RGBAAt(int(img.X)+1, int(img.Y+img.H)-2); got != mbRed {
			t.Fatalf("图下边的角不是直角：像素 %v", got)
		}
		if got := px.RGBAAt(int(bub.X)+1, int(bub.Y)+1); got == mbRed {
			t.Fatalf("上边的角没裁圆：%v", got)
		}
	}
	// 文字紧跟在图下面，有缩进（不贴边）
	text := mustFind(t, tt, "一句配文")
	if text.X < bub.X+h.T.BubblePadV || text.Y < img.Y+img.H {
		t.Fatalf("配文 %+v 应在图 %+v 下面并留内边距", text, img)
	}
}

// 群聊带名字：名字在气泡头部，图接在名字下面铺满宽度，上边是直的。
func TestImageWithGroupName(t *testing.T) {
	h := mbHost(mbReady)
	h.Group = true
	m := imgMsg("g", false, "", imgSeg(0, 640, 480))
	tt := mbRender(t, h, m)
	bub, img := mustFind(t, tt, "气泡"), mustFind(t, tt, "查看图片")
	if !mbNear(img.X, bub.X) || !mbNear(img.W, bub.W) || !mbNear(img.Y+img.H, bub.Y+bub.H) {
		t.Fatalf("图 %+v 应贴着气泡 %+v 的左、右、下", img, bub)
	}
	if img.Y < bub.Y+h.T.NameLine {
		t.Fatalf("图顶 %.1f 应在名字区（%.1f 高）下面，气泡顶 %.1f", img.Y, h.T.NameLine, bub.Y)
	}
	if px := tt.Image(); px != nil {
		// 图上边是直角：图左上角像素是红色
		if got := px.RGBAAt(int(img.X)+1, int(img.Y)+1); got != mbRed {
			t.Fatalf("名字下面图的上角应是直角：%v", got)
		}
		// 下面外侧是圆的
		if got := px.RGBAAt(int(bub.X)+1, int(bub.Y+bub.H)-2); got == mbRed {
			t.Fatalf("图的外侧下角没有被圆角裁掉：%v", got)
		}
	}
}

// 多张图：格子都在气泡里、互相只隔细缝、外框贴满气泡；张数越多越高。
func TestImageAlbum(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 7} {
		var segs []segview.Segment
		for i := 0; i < n; i++ {
			segs = append(segs, imgSeg(i, 400+i*37, 300))
		}
		tt := mbRender(t, mbHost(mbReady), imgMsg("a", true, "", segs...))
		bub := mustFind(t, tt, "气泡")
		album := mustFind(t, tt, "相册")
		if !mbNear(album.X, bub.X) || !mbNear(album.Y, bub.Y) || !mbNear(album.W, bub.W) || !mbNear(album.H, bub.H) {
			t.Fatalf("n=%d：相册 %+v 应贴满气泡 %+v", n, album, bub)
		}
		want := albumLayout(segs, 1e6)
		if !mbNear(album.H, want.H) {
			t.Fatalf("n=%d：相册高 %.1f，排法算出 %.1f", n, album.H, want.H)
		}
		mustFind(t, tt, "时间牌子")
	}
}

// 贴纸：没有气泡底，贴纸本身画出来，时间牌子叠在贴纸右下。
func TestStickerHasNoBubble(t *testing.T) {
	h := mbHost(mbReady)
	s := imgSeg(0, 200, 200)
	s.Kind, s.Sticker = "emoji", true
	m := imgMsg("s", false, "", s)
	tt := mbRender(t, h, m)
	bub, img := mustFind(t, tt, "气泡"), mustFind(t, tt, "查看图片")
	if !mbNear(img.W, bub.W) || !mbNear(img.H, bub.H) || img.W > stickerMax+0.5 {
		t.Fatalf("贴纸 %+v 应该就是整个气泡盒 %+v，且不超过 %d", img, bub, stickerMax)
	}
	if px := tt.Image(); px != nil {
		// 盒子的角上是页面背景（没有气泡底）
		if got := px.RGBAAt(int(bub.X)+1, int(bub.Y)+1); got != (color.RGBA{240, 240, 240, 255}) {
			t.Fatalf("贴纸四角应透出页面背景，实际 %v", got)
		}
	}
	chip := mustFind(t, tt, "时间牌子")
	if chip.X+chip.W > img.X+img.W+0.5 || chip.Y+chip.H > img.Y+img.H+0.5 {
		t.Fatalf("时间牌子 %+v 应在贴纸 %+v 里", chip, img)
	}
}
