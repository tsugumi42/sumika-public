package chatui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design"
)

// 薄壳的接线（规格-设计令牌 第十节第 3 步）：Palette 的每个颜色按同名字段取 design.Colors，
// 尺寸按对照表取对应令牌。抄错一个字段名（比如把 Text2 接成 Text3）这里就红。
func TestTokensShellWiring(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, n := range []Neutral{NeutralCool, NeutralPlain, NeutralWarm} {
			for _, wp := range []bool{true, false} {
				for _, look := range [][3]float32{{TextDefault, RadiusDefault, DensityDefault}, {TextMin, RadiusMin, DensityMin}, {TextMax, RadiusMax, DensityMax}} {
					s := DefaultSettings()
					s.Dark, s.Neutral, s.Wallpaper = dark, n, wp
					s.Text, s.Radius, s.Density = look[0], look[1], look[2]
					tk := Derive(s)
					d := design.Derive(s.DesignSettings())
					pv, cv := reflect.ValueOf(tk.C), reflect.ValueOf(d.Color)
					for i := range pv.NumField() {
						f := pv.Type().Field(i)
						got, ok := pv.Field(i).Interface().(ui.Color)
						if !ok {
							continue
						}
						want := cv.FieldByName(f.Name)
						if !want.IsValid() {
							t.Fatalf("Palette.%s 在 design.Colors 里没有同名字段", f.Name)
						}
						if got != want.Interface().(ui.Color) {
							t.Errorf("%+v：Palette.%s 接错了", s, f.Name)
						}
					}
					m := d.Dim
					pairs := map[string][2]float32{
						"FS[0]": {tk.FS[0], m.FontSize0}, "FS[6]": {tk.FS[6], m.FontSize6}, "Body": {tk.Body, m.FontSize4},
						"BodyLine": {tk.BodyLine, m.FontLineBody}, "ControlLine": {tk.ControlLine, m.FontLineUi},
						"S1": {tk.S1, m.SpaceS1}, "S2": {tk.S2, m.SpaceS2}, "S3": {tk.S3, m.SpaceS3}, "S4": {tk.S4, m.SpaceS4}, "S5": {tk.S5, m.SpaceS5}, "S6": {tk.S6, m.SpaceS6},
						"Inset": {tk.Inset, m.SpaceInset}, "InsetTight": {tk.InsetTight, m.SpaceInsetTight}, "InsetCtl": {tk.InsetCtl, m.SpaceInsetCtl},
						"RCtl": {tk.RCtl, m.RadiusCtl}, "R1": {tk.R1, m.Radius1}, "R2": {tk.R2, m.Radius2}, "RWin": {tk.RWin, m.RadiusWin}, "RJoin": {tk.RJoin, m.RadiusJoin},
						"HSm": {tk.HSm, m.SizeCtlSm}, "HMd": {tk.HMd, m.SizeCtlMd}, "HLg": {tk.HLg, m.SizeCtlLg},
					}
					for k, v := range pairs {
						if v[0] != v[1] {
							t.Errorf("%+v：%s = %v，令牌是 %v", s, k, v[0], v[1])
						}
					}
				}
			}
		}
	}
	// 默认设置下的几个关键值（主人 2026-11-10 看过并排图后定的）
	tk := Derive(DefaultSettings())
	if tk.BodyLine != 26 || tk.HSm != 32 || tk.HMd != 40 || tk.HLg != 48 || tk.S2 != 8 || tk.Panel.Milliseconds() != 230 {
		t.Errorf("默认值：行高 %v、控件高 %v/%v/%v、间距 %v、换层 %v", tk.BodyLine, tk.HSm, tk.HMd, tk.HLg, tk.S2, tk.Panel)
	}
}
