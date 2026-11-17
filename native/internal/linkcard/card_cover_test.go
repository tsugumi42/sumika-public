package linkcard

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"testing"

	vpxwebp "github.com/gen2brain/vpx/webp"
	xwebp "golang.org/x/image/webp"
)

// 回归：Steam 创意工坊的封面是 VP8X 扩展格式的 WebP，以前卡片上印“封面下载或解码失败”。
// 样本用两帧动图（一定是 VP8X），并确认 x/image/webp 确实解不开，这条测试才抓得住“少导入解码器”。
func TestDecodeCoverAcceptsExtendedWebP(t *testing.T) {
	frame := func(c color.RGBA) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, 32, 24))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
		return img
	}
	var buf bytes.Buffer
	anim := &vpxwebp.WEBP{Image: []image.Image{frame(color.RGBA{R: 200, A: 255}), frame(color.RGBA{B: 200, A: 255})}, Delay: []int{100, 100}}
	if err := vpxwebp.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if string(raw[12:16]) != "VP8X" {
		t.Fatalf("样本不是 VP8X：%q", raw[12:16])
	}
	if _, err := xwebp.Decode(bytes.NewReader(raw)); err == nil {
		t.Fatal("x/image/webp 居然解开了：这条回归不再说明问题")
	}
	if bmp, err := DecodeCover(raw); err != nil || bmp == nil {
		t.Fatalf("扩展格式 WebP 封面解不开：%v", err)
	}
}

// 没有封面的页面（Mastodon 帖子之类）不画封面区；有封面但没取到才画占位块并写明原因。
func TestCardWithoutCoverHasNoCoverArea(t *testing.T) {
	t.Chdir(t.TempDir())
	renderer := New()
	defer renderer.Close()
	has := func(d Data) (bool, int) {
		out, err := renderer.Render(context.Background(), d)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range out.Layout {
			if f.Name == "cover" {
				return true, out.Height
			}
		}
		return false, out.Height
	}
	base := Data{Title: "纸云的帖子", Author: "纸云 @paper", DateDuration: "11-07 23:28"}
	noCover, hNo := has(base)
	if noCover {
		t.Fatal("没有封面的卡片不该有封面区")
	}
	failed := base
	failed.CoverIssue = "封面下载失败"
	withIssue, hIssue := has(failed)
	if !withIssue || hIssue-hNo < 378 {
		t.Fatalf("封面没取到时应保留占位块：%v 高度 %d 对 %d", withIssue, hIssue, hNo)
	}
}
