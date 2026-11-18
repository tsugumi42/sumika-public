package winshell

import (
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 规格-过渡动画 D3：右栏换选项卡，下面的内容淡入；启动那一帧和没换时不淡。减少动态时照样淡（只是透明度）。
func TestSideTabSwitchFades(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		s := &State{}
		tab := "资料"
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			ui.Box(c.Key("host")).Fill().Children(func() {
				k.side(c, s, &SideSpec{TabKey: tab, Tabs: func() {}, Content: func() {
					ui.Box(c.Key("body-" + tab)).FillWidth().Height(300).Background(ui.RGB(0, 0, 200))
				}}, sideColumn, nil)
			})
		}, 400, 600, reduce)
		tt.Frame()
		tt.Frame()
		px := func() color.RGBA { return tt.Image().RGBAAt(200, 250) }
		if p := px(); p.B != 200 {
			t.Fatalf("reduce=%v 启动那一帧内容就该不透明：%+v", reduce, p)
		}
		tab = "上下文"
		tt.Frame()
		start := px()
		step(tt, 40*time.Millisecond)
		mid := px()
		settle(tt)
		end := px()
		if d0, d1 := dist(start.B, end.B), dist(mid.B, end.B); end.B != 200 || !(d0 > d1 && d1 > 0) {
			t.Errorf("reduce=%v 换选项卡淡入三帧蓝通道 %d → %d → %d", reduce, start.B, mid.B, end.B)
		}
		tt.Frame()
		if p := px(); p.B != 200 {
			t.Errorf("reduce=%v 没换选项卡又淡了：%+v", reduce, p)
		}
	}
}

// 规格-过渡动画 C21：标题栏状态字换了内容要放进出场（这一帧之后还在要帧），没换不要帧。
func TestHeaderStatusFades(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	s := &State{}
	status := "在线"
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		k.ChatOnly(c, s, ChatSpec{Key: "a", Header: HeaderSpec{Title: "测试", Subtitle: status}, Body: func(ChatLayout) {}})
	}, 600, 400, false)
	tt.Frame()
	settle(tt)
	if tt.FramePending() {
		t.Fatal("没变化时不该要帧")
	}
	if !tt.HasText("在线") {
		t.Fatalf("状态字没画出来：%v", tt.Texts())
	}
	status = "正在输入"
	tt.Frame()
	if !tt.FramePending() {
		t.Error("状态字换了应当在放淡入")
	}
	settle(tt)
	if !tt.HasText("正在输入") || tt.HasText("在线") {
		t.Errorf("放完应只剩新的状态字：%v", tt.Texts())
	}
}
