package chatui

import "testing"

// 表情回应药丸在每一档字号下都不小于 24 DIP，表情框到按钮四边的等距关系不变。
func TestChipHeightMinimum(t *testing.T) {
	for _, text := range []float32{TextMin, 13, 14, 15, 16, 18, TextMax} {
		tk := Derive(Settings{Text: text, Radius: RadiusMin, Density: DensityMin})
		if tk.ChipH < 24 {
			t.Errorf("字号 %v：药丸高 %v，小于 24", text, tk.ChipH)
		}
		if got := tk.ChipH - tk.ChipEmoji - 2*tk.ChipInset; got > 0.15 || got < -0.15 {
			t.Errorf("字号 %v：表情框到按钮上下不等距，差 %v", text, got)
		}
		if got := tk.ChipH - tk.ChipAvatar - 2*tk.ChipPad; got > 0.15 || got < -0.15 {
			t.Errorf("字号 %v：头像到按钮上下不等距，差 %v", text, got)
		}
	}
}
