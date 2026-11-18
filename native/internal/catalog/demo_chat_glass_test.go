package catalog

import (
	"testing"

	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 气泡玻璃什么时候退回实色：材质是实色、系统降低透明度、聊天背景没有壁纸、浓度到 1（实色档）。其余情况给回调。
func TestBubbleGlassFallsBackToSolid(t *testing.T) {
	newKit := func(wall bool) *winshell.Kit {
		s := chatui.DefaultSettings()
		s.Wallpaper = wall
		return winshell.NewKit(s, "")
	}
	k := newKit(true)
	if k.BubbleGlass(winshell.BubbleAlphaThin) == nil {
		t.Error("液态玻璃加壁纸，淡档该给玻璃回调")
	}
	k.SetMaterial(winshell.MaterialBlur)
	if k.BubbleGlass(winshell.BubbleAlphaThick) == nil {
		t.Error("磨砂加壁纸，浓档该给玻璃回调")
	}
	if k.BubbleGlass(1) != nil {
		t.Error("浓度 1 是实色档，不该给回调")
	}
	k.SetMaterial(winshell.MaterialSolid)
	if k.BubbleGlass(winshell.BubbleAlphaThin) != nil {
		t.Error("材质设成实色，气泡该退回实色")
	}
	k = newKit(true)
	k.Reduce = true
	if k.BubbleGlass(winshell.BubbleAlphaThin) != nil {
		t.Error("系统开了降低透明度，气泡该退回实色")
	}
	if newKit(false).BubbleGlass(winshell.BubbleAlphaThin) != nil {
		t.Error("聊天背景没有壁纸，气泡该退回实色")
	}
	for _, l := range []BubbleLook{{Mode: 0}, {Mode: 1}, {Mode: 2}, {Mode: 3, Custom: .6}} {
		a := l.Alpha()
		if a < winshell.BubbleAlphaMin && l.Mode != 0 || a > 1 {
			t.Errorf("模式 %d 的浓度 %v 超出范围", l.Mode, a)
		}
	}
	if (BubbleLook{Mode: 0}).Alpha() != 1 {
		t.Error("实色模式的浓度应是 1")
	}
}
