package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// B：导航五项（本地、QQ、记忆、运行、设置）只有图标，后三项贴栏底；点一项切页并让分段回到私聊。
func TestShellNavItemsAndClick(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	var ys []float32
	for _, n := range []string{"本地", "QQ", "记忆", "运行", "设置"} {
		r, ok := tt.Find(n)
		if !ok || !closeTo(r.W, 44) || !closeTo(r.H, 44) || !closeTo(r.X, 10) {
			t.Fatalf("导航项 %s：%v %v，应为 x=10、44×44", n, r, ok)
		}
		ys = append(ys, r.Y)
	}
	for i := 1; i < len(ys); i++ {
		if ys[i] <= ys[i-1] {
			t.Fatalf("导航顺序不对：%v", ys)
		}
	}
	if ys[4] < 900 {
		t.Fatalf("设置没贴栏底：y=%v", ys[4])
	}
	a.workbench.shell.seg[0] = segGroup
	click(t, tt, "QQ")
	if a.rail != 1 || a.workbench.shell.seg != [2]int{} {
		t.Fatalf("点 QQ：rail=%d 分段=%v", a.rail, a.workbench.shell.seg)
	}
	if !tt.HasText("编造的朋友") {
		t.Fatalf("QQ 页没有 QQ 私聊：%v", tt.Texts())
	}
	click(t, tt, "本地")
	if a.rail != 0 || !tt.HasText("白羽") {
		t.Fatal("点本地没回本地页")
	}
}

// B3：导航项悬停、按下的底色各不相同，也不等于栏底色；选中的是浅灰底。
func TestShellNavStates(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	r, _ := tt.Find("QQ")
	px := func() ui.Color {
		p := tt.Image().RGBAAt(int((r.X+3)*1.25), int((r.Y+r.H/2)*1.25))
		return ui.Color{R: p.R, G: p.G, B: p.B, A: p.A}
	}
	rest := px()
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	hover := px()
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	press := px()
	tt.CancelPress()
	if rest == hover || hover == press || rest == press {
		t.Fatalf("悬停 / 按下底色没分开：静止 %v 悬停 %v 按下 %v", rest, hover, press)
	}
	sel, _ := tt.Find("本地")
	p := tt.Image().RGBAAt(int((sel.X+3)*1.25), int((sel.Y+sel.H/2)*1.25))
	if (ui.Color{R: p.R, G: p.G, B: p.B, A: p.A}) == rest {
		t.Fatal("选中项底色和未选中一样")
	}
}
