package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 菜单展开时阴影不再“突然弹出来”（主人 2026-11-16）：面板还在被裁着露出来的时候不画阴影（裁剪会连阴影一起裁，
// 等裁剪撤掉那一帧才冒出来），长开以后阴影浓度从 0 逐帧淡到 1。
func TestMenuMorphShadowFadesInAfterUnfold(t *testing.T) {
	open := false
	var alphas []float32 // 每帧 shadow 被调用时的浓度；没调用记 0
	var frame float32 = -1
	tt := ui.NewTester(func(c *ui.Context) {
		frame = -1
		ui.Column(c.Key("root")).Padding(10).Fill().Children(func() {
			m := ui.MenuBase(c.Key("m"), &open)
			m.Trigger.Label("开").Size(80, 30).Children(func() { ui.Text(c, "开") })
			m.Popup(func(panel ui.Element) {
				Derive(DefaultSettings()).MenuMorph(c, panel.Width(60).Padding(4).Background(ui.RGB(90, 90, 90)), 0, func(a float32) { frame = a })
				for _, s := range []string{"甲", "乙", "丙"} {
					m.Item().Label(s).Height(28).Children(func() { ui.Text(c, s) })
				}
			})
		})
	}, 300, 300)
	tt.Frame()
	if err := tt.Click("开"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		alphas = append(alphas, max(frame, 0))
		if !tt.FramePending() {
			break
		}
	}
	// 前几帧还在展开：阴影是 0；之后至少两帧介于 0 和 1 之间（淡入），最后是 1
	if alphas[0] != 0 || alphas[1] != 0 {
		t.Errorf("展开刚开始就画了阴影：%v", alphas)
	}
	mid := 0
	for _, a := range alphas {
		if a > 0.02 && a < 0.98 {
			mid++
		}
	}
	if mid < 2 {
		t.Errorf("阴影没有逐帧淡入（中间值只有 %d 帧）：%v", mid, alphas)
	}
	if last := alphas[len(alphas)-1]; last < 0.99 {
		t.Errorf("长开以后阴影没到满：%v", alphas)
	}
}
