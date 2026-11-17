package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"sumika-native/internal/segview"
)

// 带链接的消息右键有复制链接、打开链接；右键菜单的高亮是中性色，不是强调色。
func TestMessageMenuItems(t *testing.T) {
	const text = "看这个 https://example.com/a?b=1 怎么样"
	a := shellApp()
	a.sel, a.title, a.group, a.rail = "qq:group:90002", "编造", true, 1
	a.msgs = []*msg{{id: "m", who: "虚构甲", actor: "200", text: text, blocks: segview.TextBlocks(text), at: 60000}}
	a.list = a.newList()
	tt := shellTester(a, 1400, 800)
	box, ok := tt.Find(text)
	if !ok {
		t.Fatalf("没有这条消息：%q", tt.Texts())
	}
	tt.RightClickAt(box.X+10, box.Y+box.H/2)
	tt.Frame()
	tt.Frame()
	items := tt.Menu()
	for _, want := range []string{"复制", "复制链接", "打开链接"} {
		if !slices.Contains(items, want) {
			t.Fatalf("菜单里没有 %s：%q", want, items)
		}
	}
	r, ok := tt.Find("复制链接")
	if !ok {
		t.Fatal("没画出菜单项")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Frame()
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "右键菜单.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	kit := a.workbench.shell.kit
	x, y := int((r.X+2)*1.25), int((r.Y+r.H/2)*1.25)
	got := tt.Image().RGBAAt(x, y)
	acc := kit.P.Accent
	if got.R == acc.R && got.G == acc.G && got.B == acc.B {
		t.Fatalf("菜单高亮还是强调色 %v", got)
	}
}
