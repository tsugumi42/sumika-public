package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/lookpreset"
)

// 新界面三处 chatui.Host（消息、流式草稿、审批卡）都要设 SelfChar：表情回应、引用高亮、审批卡按钮用的"我的强调色"
// 是从它推的，零值会推出接近黑色的颜色。这里断言三处拿到的就是原型预设里"我"的强调色。
func TestWorkbenchHostsUsePrototypeSelfAccent(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := mdApp()
		a.msgs = []*msg{{id: "u1", who: "我", mine: true, text: "一句话"}}
		var hosts map[string]chatui.Host
		tt := ui.NewTester(func(c *ui.Context) {
			a.applyLook(c)
			hosts = map[string]chatui.Host{"消息": a.chatHostFor(c, a.msgs[0], 0), "草稿": a.draftHost(c, 400), "审批": a.approvalHost(c)}
			ui.Box(c).Size(1, 1)
		}, 400, 300)
		tt.SetDark(dark)
		tt.Frame()
		want := lookpreset.Tokens(dark).AccentOf(chatui.DefaultChar)
		preset := lookpreset.Light()
		if dark {
			preset = lookpreset.Night()
		}
		if want.Base != preset.Colors().Control().PrimaryBackground() {
			t.Fatalf("dark=%v 原型的我的强调色 %v 和预设的主色 %v 不一致", dark, want.Base, preset.Colors().Control().PrimaryBackground())
		}
		broken := lookpreset.Tokens(dark).AccentOf(chatui.OKLCH{}).Base
		if broken == want.Base {
			t.Fatal("零值推出的颜色和原型强调色一样，这条回归测试就没有意义了")
		}
		for name, h := range hosts {
			if got := h.T.AccentOf(h.SelfChar); got.Base != want.Base || got.Soft != want.Soft || got.Text != want.Text {
				t.Errorf("dark=%v %s宿主的我的强调色 %v，应为原型的 %v（零值会得到 %v）", dark, name, got.Base, want.Base, broken)
			}
		}
	}
}
