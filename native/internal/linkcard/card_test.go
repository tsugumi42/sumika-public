package linkcard

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCardKeepsReadableFields(t *testing.T) {
	t.Chdir(t.TempDir())
	cover := image.NewRGBA(image.Rect(0, 0, 320, 180))
	draw.Draw(cover, cover.Bounds(), &image.Uniform{C: color.RGBA{R: 180, G: 210, B: 240, A: 255}}, image.Point{}, draw.Src)
	renderer := New()
	defer renderer.Close()
	d := Data{Title: strings.Repeat("纸船试航😀LongTitle ", 8), Author: "UP主：纸灯实验室", DateDuration: "2026-11-01 18:30 +08:00｜02:15", Stats: []string{"播放 0｜收藏 未返回｜转发 4｜评论 8", "弹幕 6｜点赞 52｜投币 10"}, Cover: ui.NewBitmap(cover)}
	out, err := renderer.Render(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 720 || img.Bounds().Dy() > 960 || format != "png" || out.MIME != "image/png" {
		t.Fatalf("编码或尺寸：%v %s %s", img.Bounds(), format, out.MIME)
	}
	bottom := 0
	for _, f := range out.Layout {
		if f.Y < bottom || f.X < 24 || f.X+f.W > 696 || f.Y+f.H > img.Bounds().Dy() {
			t.Fatalf("字段越界或重叠：%+v", f)
		}
		bottom = f.Y + f.H
		if f.Name == "cover" {
			continue
		}
		ink := 0
		for y := f.Y; y < f.Y+f.H; y++ {
			for x := f.X; x < f.X+f.W; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if a > 0 && r < 45000 && g < 45000 && b < 45000 {
					ink++
				}
			}
		}
		if ink < 10 {
			t.Fatalf("字段未画出可读文字：%+v ink=%d", f, ink)
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("正常卡片创建了%d个文件", len(entries))
	}
	broken := d
	broken.Stats = make([]string, 40)
	for i := range broken.Stats {
		broken.Stats[i] = "关键数据不得截掉"
	}
	if _, err := renderer.Render(context.Background(), broken); err == nil {
		t.Fatal("超高卡片静默丢关键数据")
	}
	if _, err := DecodeCover([]byte("坏图")); err == nil {
		t.Fatal("坏封面当成功")
	}
	t.Logf("卡片=%dx%d %s %d字节；语义字段=%d；正常文件=0", out.Width, out.Height, out.MIME, len(out.Bytes), len(out.Layout))
}

// 新 B-3：通用卡片的统计行竖线有的发红、有的发蓝：文字按子像素（LCD）抗锯齿画在不透明背景上，一像素宽的竖线边缘有彩色。
// 发出去的是一张图，不该带彩边；没有封面的卡片里文字是中性色，每个像素的 R、G、B 应该一样。
func TestCardTextHasNoColorFringes(t *testing.T) {
	t.Chdir(t.TempDir())
	renderer := New()
	defer renderer.Close()
	out, err := renderer.Render(context.Background(), Data{
		Title: "辉夜大小姐想让我告白", Author: "第二季 | 已完结，共12话", DateDuration: "评分：9.8（21.7万 人评分）｜首播：2020-04-12",
		Stats: []string{"播放 2.54亿｜弹幕 391.3万｜点赞 194.4万", "投币 237.9万｜追番 804万｜转发 27.1万 | 评论 3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	colored := 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			lo, hi := min(r, g, b), max(r, g, b)
			if hi-lo > 3*257 { // 允许 3/255 的取整误差
				colored++
			}
		}
	}
	if colored != 0 {
		t.Fatalf("文字边缘有 %d 个彩色像素（子像素抗锯齿）", colored)
	}
}
