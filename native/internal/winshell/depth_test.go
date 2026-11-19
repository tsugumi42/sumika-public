package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// collectSurfaces 收集一帧里各控件登记的圆角（Surface 挂钩），键是控件键。
func collectSurfaces(k *Kit) (map[string]SurfaceInfo, map[string]ui.Rect) {
	infos, rects := map[string]SurfaceInfo{}, map[string]ui.Rect{}
	k.Surface = func(c *ui.Context, e ui.Element, info SurfaceInfo) {
		infos[info.ID] = info
		rects[info.ID] = e.Bounds()
	}
	return infos, rects
}

// 单行控件一律胶囊（圆角 = 高度一半），内层与外层同心（内圆角 = 外圆角 − 内距）。
func TestSingleLineControlsArePills(t *testing.T) {
	k := testKit()
	infos, rects := collectSurfaces(k)
	g := k.G
	text, find, sel := "", "", 0
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c).Fill().Padding(10).Gap(8).Children(func() {
			k.Button(c, "btn", "保存", true, false, func() {})
			k.TextField(c, "field", &text, "名字").Width(200)
			k.SearchField(c, "find", &find, "搜索", nil).Width(200)
			k.Stepper(c, "step", "次数", 3, StepperSpec{Min: 1, Max: 9}, func(float64) {})
			k.Segment(c, "seg", &sel, []string{"甲", "乙"})
		})
	}, 400, 400)
	tt.Frame()
	tt.Frame()
	for _, id := range []string{"btn", "field", "find", "step", "seg"} {
		info, r := infos[id], rects[id]
		if r.H == 0 {
			t.Fatalf("%s 没登记", id)
		}
		if want := r.H / 2; abs32(info.Radius-want) > 0.01 {
			t.Errorf("%s：圆角 %v，应为高 %v 的一半 %v", id, info.Radius, r.H, want)
		}
	}
	if got, want := infos["step-minus"].Radius, infos["step"].Radius-g.SpaceTight; abs32(got-want) > 0.01 {
		t.Errorf("加减键圆角 %v，应为外框 %v 减内距 %v", got, infos["step"].Radius, g.SpaceTight)
	}
	if got, want := infos["seg0"].Radius, infos["seg"].Radius-g.SpaceTight; abs32(got-want) > 0.01 {
		t.Errorf("分段选项圆角 %v，应为轨道 %v 减内距 %v", got, infos["seg"].Radius, g.SpaceTight)
	}
}

// 下拉是一个整体：展开的面板就是入口自己长大，同宽、左对齐、顶边重合，圆角还是入口那个数（高度的一半），项和面板同心。
func TestDropdownIsOneThing(t *testing.T) {
	k := testKit()
	infos, rects := collectSurfaces(k)
	g := k.G
	open := false
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Width(300).Padding(10).Children(func() {
			k.Dropdown(c, "dd", "模型", &open, 1, []string{"甲", "乙", "丙"}, func(int) {}, nil)
		})
	}, 400, 400)
	tt.Frame()
	if err := tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(time.Second) // 等长大的动画播完
	tt.Frame()
	tt.Frame()
	tr, pn := rects["dd"], rects["dd-popup"]
	if pn.W == 0 {
		t.Fatal("面板没登记")
	}
	if abs32(tr.X-pn.X) > 0.5 || abs32(tr.W-pn.W) > 0.5 || abs32(tr.Y-pn.Y) > 0.5 {
		t.Errorf("面板 %+v 应和入口 %+v 同宽、左对齐、顶边重合", pn, tr)
	}
	if want := tr.H / 2; abs32(infos["dd"].Radius-want) > 0.01 {
		t.Errorf("入口是单行，圆角 %v 应为 %v", infos["dd"].Radius, want)
	}
	if infos["dd-popup"].Radius != infos["dd"].Radius {
		t.Errorf("面板圆角 %v，应和入口的 %v 一样（长大时弧度不跳）", infos["dd-popup"].Radius, infos["dd"].Radius)
	}
	if got, want := infos["dd-0"].Radius, infos["dd-popup"].Radius-g.SpaceTight; abs32(got-want) > 0.01 {
		t.Errorf("面板里的项圆角 %v，应为面板圆角减内距 %v", got, want)
	}
}
