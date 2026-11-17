package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

func redBitmap(c color.RGBA) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return ui.NewBitmap(img)
}

func mediaMsg(id string, segs ...seg) *msg {
	var views []segview.Segment
	for i := range segs {
		segs[i].position = i
		views = append(views, segmentView(segs[i], i))
	}
	return &msg{id: id, segs: segs, version: 1, blocks: segview.BlocksOf(views)}
}

func workbenchMediaApp(m *msg) (*app, *ui.Tester) {
	a := &app{sel: "c", msgs: []*msg{m}, gen: 1, srcGen: 1}
	a.list = a.newList()
	st := a.is()
	st.scale = 1
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 900, 600)
	return a, tt
}

var pureRed = color.RGBA{255, 0, 0, 255}

func pixelAtCenter(t *testing.T, tt *ui.Tester, label string) color.RGBA {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("没找到 %q", label)
	}
	img := tt.Image()
	if img == nil {
		t.Skip("没有离屏图")
	}
	return img.RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
}

// 新视图要把宿主的媒体接口交给气泡，缩略图已经在缓存里时直接画出图，不再停在 [图片] 占位。
func TestWorkbenchDrawsImage(t *testing.T) {
	s := seg{DisplaySegment: api.DisplaySegment{Kind: "image", Width: 80, Height: 40,
		Media: &api.MediaRef{Hash: "abc", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}}
	m := mediaMsg("m", s)
	a, tt := workbenchMediaApp(m)
	w := float32(120) // 80×40 的小图不放大，宽度只被最窄 120 兜底（chatui.mediaMinW）
	a.is().m[keyOf(m.segs[0], w, false, 1)] = &imgEntry{state: imgOK, bmp: redBitmap(pureRed)}
	tt.Frame()
	tt.Frame()
	if tt.HasText("[图片]") {
		t.Fatalf("还是占位：%v", tt.Texts())
	}
	if got := pixelAtCenter(t, tt, "查看图片"); got != pureRed {
		t.Fatalf("图片中心像素 %v，应该是缩略图的红色", got)
	}
}

// QQ 的贴纸（emoji + sticker）是一张图，行内小黄脸有图时按图画，不是 [表情] 文字或药丸。
func TestWorkbenchDrawsStickerAndFace(t *testing.T) {
	s := seg{DisplaySegment: api.DisplaySegment{Kind: "emoji", Sticker: true, Ref: "s1", Width: 100, Height: 100,
		Media: &api.MediaRef{Hash: "stk", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}}
	m := mediaMsg("m", s)
	a, tt := workbenchMediaApp(m)
	w := float32(240) // chatui.mediaSize 的上限
	a.is().m[keyOf(m.segs[0], w, true, 1)] = &imgEntry{state: imgOK, bmp: redBitmap(pureRed)}
	tt.Frame()
	tt.Frame()
	if tt.HasText("[表情]") || tt.HasText("[图片]") {
		t.Fatalf("贴纸还是文字占位：%v", tt.Texts())
	}
	if got := pixelAtCenter(t, tt, "查看图片"); got != pureRed {
		t.Fatalf("贴纸中心像素 %v", got)
	}

	// 普通小黄脸（face）：图已取到时，行内按图画，文字里不再出现 [表情]
	face := seg{DisplaySegment: api.DisplaySegment{Kind: "face", Ref: "14"}}
	m2 := mediaMsg("f", face)
	a2, tt2 := workbenchMediaApp(m2)
	a2.faces = &faceStore{table: map[string]faceInfo{"14": {Name: "微笑", Kind: "classic"}},
		imgs: map[string]*faceEntry{"14": {state: faceOK, bmp: redBitmap(pureRed)}}}
	tt2.Frame()
	tt2.Frame()
	if tt2.HasText("[表情]") || tt2.HasText("[微笑]") {
		t.Fatalf("小黄脸还是文字：%v", tt2.Texts())
	}
	img := tt2.Image()
	if img == nil {
		return
	}
	n := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] == 255 && img.Pix[i+1] == 0 && img.Pix[i+2] == 0 {
			n++
		}
	}
	if n < 20 {
		t.Fatalf("没画出小黄脸的图（红色像素 %d）", n)
	}
}

// 设 NEWUI_SHOT=目录时存组件截图（只截气泡区域，像素比 1.25）；平时跳过。
func cropShot(t *testing.T, tt *ui.Tester, label, name string) {
	t.Helper()
	dir := os.Getenv("NEWUI_SHOT")
	if dir == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("没找到 %q", label)
	}
	img := tt.Image()
	if img == nil {
		t.Fatal("没有图")
	}
	const pad = 8
	sc := float32(1.25)
	box := image.Rect(int((r.X-pad)*sc), int((r.Y-pad)*sc), int((r.X+r.W+pad)*sc), int((r.Y+r.H+pad)*sc)).Intersect(img.Bounds())
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img.SubImage(box)); err != nil {
		t.Fatal(err)
	}
}

func TestShotWorkbenchMedia(t *testing.T) {
	if os.Getenv("NEWUI_SHOT") == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	// 图：一张蓝紫渐变的 PNG 当缩略图
	grad := image.NewRGBA(image.Rect(0, 0, 160, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 160; x++ {
			grad.Set(x, y, color.RGBA{uint8(60 + x), uint8(90 + y), 200, 255})
		}
	}
	bmp := ui.NewBitmap(grad)
	s := seg{DisplaySegment: api.DisplaySegment{Kind: "image", Width: 160, Height: 80,
		Media: &api.MediaRef{Hash: "abc", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}}
	m := mediaMsg("m", s)
	a, tt := workbenchMediaApp(m)
	tt.SetScale(1.25)
	a.is().scale = 1.25
	a.is().m[keyOf(m.segs[0], 240, false, 1.25)] = &imgEntry{state: imgOK, bmp: bmp}
	tt.Frame()
	tt.Frame()
	cropShot(t, tt, "气泡", "3-image.png")

	// 贴纸 + 小黄脸
	st := seg{DisplaySegment: api.DisplaySegment{Kind: "emoji", Sticker: true, Ref: "s1", Width: 100, Height: 100,
		Media: &api.MediaRef{Hash: "stk", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}}
	m2 := mediaMsg("s", st)
	a2, tt2 := workbenchMediaApp(m2)
	tt2.SetScale(1.25)
	a2.is().scale = 1.25
	a2.is().m[keyOf(m2.segs[0], 240, true, 1.25)] = &imgEntry{state: imgOK, bmp: ui.NewBitmap(grad)}
	tt2.Frame()
	tt2.Frame()
	cropShot(t, tt2, "气泡", "4-sticker.png")
}
