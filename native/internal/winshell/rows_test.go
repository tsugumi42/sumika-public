package winshell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/uitest"
)

// 同一页里开关、下拉、值行、纯说明的一行，行高都是 RowHeight：上下留白只有一种（审查第 8 条）。
// 分段控件本身比 RowHeight 高，行跟着控件走，但不会比控件多出一圈外包的内距。
func TestSettingsRowsShareOneHeight(t *testing.T) {
	k := testKit()
	on, open, seg := false, false, 1
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("page")).Width(600).Children(func() {
			k.Row(c, "r-switch", "开关", "", func() { k.Switch(c, "sw", "开关", &on) })
			k.Row(c, "r-drop", "下拉", "", func() { k.Dropdown(c, "dd", "下拉", &open, 0, []string{"一", "二"}, func(int) {}, nil) })
			k.Row(c, "r-seg", "分段", "", func() { k.Segment(c, "sg", &seg, []string{"甲", "乙", "丙"}) })
			k.Row(c, "r-plain", "只有名字", "", func() {})
			ui.Box(c.Key("r-value")).FillWidth().Children(func() { k.ValueRow(c, "值行", "当前值", true, false, func() {}) })
		})
	}, 700, 600)
	tt.Frame()
	tt.Frame()
	root := uitest.Tree(tt)
	h := func(key string) float32 {
		n := root.Find(key)
		if n == nil {
			t.Fatalf("没有 %s", key)
		}
		return n.Box.H
	}
	want := k.G.RowHeight
	for _, key := range []string{"r-switch", "r-drop", "r-plain", "r-value"} {
		uitest.Equal(t, key, "行高", h(key), want)
	}
	if got := h("r-seg"); got < want || got > want+k.G.SpaceSmall {
		t.Fatalf("分段那一行高 %.1f，应在 %.1f 到 %.1f 之间", got, want, want+k.G.SpaceSmall)
	}
}

// 右边放一个定宽输入框加一个勾选框（勾选框自带 FillWidth）时，左边的名字和说明不能被挤成一字一行（2026-11-11 主人截图）
func TestSettingsRowControlsDoNotSqueezeLabel(t *testing.T) {
	k := testKit()
	text, on := "", false
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("page")).Width(600).Children(func() {
			k.Row(c, "r", "作用范围", "允许、每次问可以只对一个项目目录、只对当前对话；都不填是全局。拒绝一律全局", func() {
				k.TextField(c, "tf", &text, "项目目录").Width(220)
				k.Check(c, "ck", "只对当前对话", &on, false)
			})
		})
	}, 700, 600)
	tt.Frame()
	tt.Frame()
	root := uitest.Tree(tt)
	label, ctl := root.Find("label"), root.Find("ctl")
	if label == nil || ctl == nil {
		t.Fatal("找不到左栏或控件区")
	}
	if label.Box.W < 160 {
		t.Fatalf("左边名字和说明只有 %.1f 宽（控件区 %.1f）：被控件挤扁了", label.Box.W, ctl.Box.W)
	}
	if ctl.Box.W > 420 {
		t.Fatalf("控件区 %.1f 宽，比输入框加勾选框需要的多：勾选框还在抢宽度", ctl.Box.W)
	}
}
