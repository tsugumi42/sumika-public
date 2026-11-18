package linkcard

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/assets"
	"sumika-native/internal/assets/emoji"
)

// 表情换成 Twemoji 的图：㊗️、❤️、旗帜画成彩色，不带 VS16 的 ㊗ 仍是字；没有图的序列退回字。本机没有 Twemoji 就跳过
func TestTweetCardTwemoji(t *testing.T) {
	dir := assets.TwemojiDir()
	if dir == "" {
		t.Skip("找不到 Twemoji（native/twemoji 不进 git），跳过彩色表情的断言")
	}
	r := testRenderer(t, Options{Theme: func() string { return ThemeLight }, EmojiDir: dir})
	tw := sampleTweet()
	tw.Author.Name = "纸云💧"
	tw.Text = "㊗️❤️🇯🇵✨"
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	// 量和画同一份 Span：版式里的正文高度和量出来的一样，表情没把行撑高
	m := r.fonts.measurer()
	_, h1, _ := m.block(tw.Text, bodySize, bodyLine, 400, 516, 0)
	_, h0, _ := m.block("国Ag", bodySize, bodyLine, 400, 516, 0)
	if h1 > h0+0.5 {
		t.Fatalf("表情把一行撑高了：%v > %v", h1, h0)
	}
	img, _, err := image.Decode(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	// 正文那块要有鲜艳的颜色（㊗️ 的红、❤️ 的红、✨ 的黄）；单色字形只有黑灰
	body := fieldOf(t, out, "body")
	// 数成片的纯色像素（红：㊗️、❤️；蓝：💧）；字的抗锯齿边只有零星几个偏色像素
	count := func(f FieldLayout, want func(r, g, b uint32) bool) int {
		n := 0
		for y := f.Y; y < f.Y+f.H; y++ {
			for x := f.X; x < f.X+f.W; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if want(r>>8, g>>8, b>>8) {
					n++
				}
			}
		}
		return n
	}
	red := func(r, g, b uint32) bool { return r > 190 && g < 90 && b < 90 }
	blue := func(r, g, b uint32) bool { return b > 190 && r < 140 && g > 120 }
	if n := count(body, red); n < 300 {
		t.Fatalf("正文的 ㊗️、❤️ 没画成彩色：%d 个红像素", n)
	}
	if n := count(fieldOf(t, out, "name"), blue); n < 30 {
		t.Fatalf("名字里的 💧 没画成彩色：%d", n)
	}
	// 不带 VS16 的 ㊗ 按字画：没有鲜艳像素
	tw.Text = "㊗㊗㊗"
	if out, err = r.RenderTweet(context.Background(), tw); err != nil {
		t.Fatal(err)
	}
	img, _, _ = image.Decode(bytes.NewReader(out.Bytes))
	if n := count(fieldOf(t, out, "body"), red); n > 20 {
		t.Fatalf("不带 VS16 的 ㊗ 应按字画：%d 个红像素", n)
	}
	// Twemoji 画的表情算进 Emoji，不当缺字
	cov := r.fonts.check("㊗️❤️🇯🇵✨")
	if cov.Emoji != 4 || len(cov.Missing) != 0 || cov.Total != 4 {
		t.Fatalf("覆盖：%+v", cov)
	}
	// 目录里没有这张图的序列退回字画（不画空框）
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "1f44d.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 36 36"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	set := emoji.NewSet(empty)
	if sp, objs := emojiSpans(set, "A㊗️B", 20, 24); len(objs) != 0 || len(sp) != 1 || sp[0].Text != "A㊗️B" {
		t.Fatalf("没有图的序列应原样当字：%+v %v", sp, objs)
	}
}
