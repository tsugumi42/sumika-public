package winshell

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// settingsPaneTester 把两组设置放进一个 SettingsPane，返回 Tester 和各控件登记的框。
// 第一组：甲（带说明）、乙（带说明、改过）、丙（没有说明）；第二组：丁。
func settingsPaneTester(t *testing.T, k *Kit, w, h float32, changed bool, open *bool, names []string) (*ui.Tester, map[string]SurfaceInfo, map[string]ui.Rect) {
	t.Helper()
	infos, rects := collectSurfaces(k)
	on := []bool{true, false}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		c.Root().Background(k.P.Canvas)
		k.SettingsPane(c, "pane", SettingsPaneSpec{
			Categories: []Category{{ID: "a", Label: "甲类"}}, Selected: "a", OnSelect: func(string) {}, Height: h,
			Content: func() {
				k.SettingGroup(c, "g1", "第一组", func() {
					k.SettingItem(c, "i1", "名字甲", "说明甲", SettingItemOptions{}, func() { k.SettingValue(c, "值甲") })
					k.SettingItem(c, "i2", "名字乙", "说明乙", SettingItemOptions{Changed: changed, Was: "关"}, func() { k.SettingValue(c, "值乙") })
					k.SettingItem(c, "i3", "名字丙", "", SettingItemOptions{}, func() { k.MultiChips(c, "chips", []string{"一", "二"}, on) })
				})
				k.SettingGroup(c, "g2", "第二组", func() {
					k.SettingItem(c, "i4", "名字丁", "说明丁", SettingItemOptions{}, nil)
					if names != nil {
						k.SettingCollapsed(c, "all", fmt.Sprintf("另有 %d 个（只读）", len(names)), names, open, 3)
					}
				})
			},
		})
	}, int(w), int(h)+20)
	tt.Frame()
	tt.Frame()
	tt.Advance(time.Second)
	return tt, infos, rects
}

// 分类栏、内容区、分组面、凹块、浮起块的底色在亮、暗下互不相同：栏和栏、面和底只靠底色分开。
func TestSettingsPaneColorsAreDistinct(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := chatui.DefaultSettings()
		s.Dark = dark
		k := NewKit(s, "")
		p := k.P
		pairs := []struct {
			a, b ui.Color
			why  string
		}{
			{p.Pane, p.Content, "分类栏和内容区"},
			{p.Content, p.Group, "内容区和分组面"},
			{p.Group, p.Bubble, "分组面和里面的凹块（输入框、步进器、下拉入口）"},
			{p.Content, p.Bubble, "内容区和凹块"},
			{p.Group, k.raisedFill(), "分组面和浮起块（选中的分类、开关圆钮）"},
		}
		for _, pr := range pairs {
			if pr.a == pr.b {
				t.Errorf("暗=%v：%s 同色 %v，分不开", dark, pr.why, pr.a)
			}
		}
		lum := func(c ui.Color) int { return int(c.R) + int(c.G) + int(c.B) }
		if dark == false && lum(p.Content) <= lum(p.Pane) {
			t.Errorf("亮色：内容区应比分类栏亮：%v %v", p.Content, p.Pane)
		}
	}
}

// 间距关系：名字和说明只隔一点点；项和项之间明显更大；组和组之间至少是项间距的两倍。
func TestSettingGroupSpacing(t *testing.T) {
	k := testKit()
	g := k.G
	tt, _, rects := settingsPaneTester(t, k, 1000, 900, false, nil, nil)
	find := func(s string) ui.Rect {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("找不到 %q", s)
		}
		return r
	}
	nameA, hintA, nameB := find("名字甲"), find("说明甲"), find("名字乙")
	hintGap := hintA.Y - (nameA.Y + nameA.H)
	itemGap := nameB.Y - (hintA.Y + hintA.H)
	if hintGap > g.SpaceTight {
		t.Errorf("名字和它的说明隔了 %v，不该比 %v 大", hintGap, g.SpaceTight)
	}
	if itemGap < 2*g.SpaceSmall-.5 || itemGap < 4*hintGap {
		t.Errorf("项和项之间 %v，应至少是 2×%v，且比名字到说明的 %v 大得多", itemGap, g.SpaceSmall, hintGap)
	}
	g1, g2 := rects["g1"], rects["g2"]
	groupGap := g2.Y - (g1.Y + g1.H)
	if groupGap < 2*itemGap {
		t.Errorf("组和组之间 %v，应至少是项间距 %v 的两倍", groupGap, itemGap)
	}
}

// 面最宽 SettingsGroupWidth 且靠左：窗口很宽时不拉满、不居中；窗口窄时收窄到内容区能给的宽。
func TestSettingGroupMaxWidthAndLeftAligned(t *testing.T) {
	k := testKit()
	g := k.G
	_, infos, rects := settingsPaneTester(t, k, 1700, 700, false, nil, nil)
	face := rects["g1"]
	if face.W > g.SettingsGroupWidth+.5 || face.W < g.SettingsGroupWidth-.5 {
		t.Errorf("宽窗口里面宽 %v，应为 %v", face.W, g.SettingsGroupWidth)
	}
	if want := SettingsListWidth + g.SpaceLarge + g.ListInset*0; abs32(face.X-want) > .5 {
		t.Errorf("面左边 %v，应紧挨着内容区左内距 %v（靠左，不居中）", face.X, want)
	}
	if in := infos["g1"]; in.Radius != g.RadiusControl || in.Inset != g.SpaceTight {
		t.Errorf("面的圆角、内距是 %v / %v，应为多行容器档 %v 和 %v", in.Radius, in.Inset, g.RadiusControl, g.SpaceTight)
	}
	_, _, narrow := settingsPaneTester(t, k, 700, 700, false, nil, nil)
	if w, room := narrow["g1"].W, 700-SettingsListWidth-2*g.SpaceLarge; w > room+.5 || w < room-.5 {
		t.Errorf("窄窗口里面宽 %v，应收窄到 %v", w, room)
	}
}

// 小圆点只在“改过”时出现；没改过一个字、一个点都不画（不写“默认值”）。
func TestModifiedDotOnlyWhenChanged(t *testing.T) {
	k := testKit()
	tt, infos, _ := settingsPaneTester(t, k, 1000, 700, false, nil, nil)
	for _, id := range []string{"i1-dot", "i2-dot", "i3-dot"} {
		if _, ok := infos[id]; ok {
			t.Errorf("没改过的项不该有小圆点 %s", id)
		}
	}
	if tt.HasText("默认值") {
		t.Error("界面上不该出现“默认值”三个字")
	}
	k2 := testKit()
	_, infos2, rects2 := settingsPaneTester(t, k2, 1000, 700, true, nil, nil)
	if _, ok := infos2["i2-dot"]; !ok {
		t.Fatal("改过的项名字旁应有小圆点")
	}
	if _, ok := infos2["i1-dot"]; ok {
		t.Error("没改过的项不该有小圆点")
	}
	dot, name := rects2["i2-dot"], rects2["i2"]
	if dot.W != k2.G.SpaceMedium || dot.X < name.X || dot.X > name.X+name.W/2 {
		t.Errorf("小圆点的命中区 %v 应在名字旁边（项框 %v）", dot, name)
	}
}

// 折起来的一长串：收着时不占行，点一下展开成 cols 列，十七个名字三列是六行；再点收回去。
func TestSettingCollapsedRows(t *testing.T) {
	k := testKit()
	names := make([]string, 17)
	for i := range names {
		names[i] = fmt.Sprintf("插件%02d", i+1)
	}
	open := false
	tt, infos, _ := settingsPaneTester(t, k, 1000, 900, false, &open, names)
	count := func(infos map[string]SurfaceInfo) (n int) {
		for id, in := range infos {
			if in.Kind == "setting-list-row" && len(id) > 4 {
				n++
			}
		}
		return n
	}
	if count(infos) != 0 || tt.HasText("插件01") {
		t.Fatal("收着时不该有名字列表")
	}
	if !tt.HasText("另有 17 个（只读）") {
		t.Fatal("收着时应只剩一行概述")
	}
	if err := tt.Click("另有 17 个（只读）"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(400 * time.Millisecond)
	if !open {
		t.Fatal("点概述行应展开")
	}
	if got := count(infos); got != 6 {
		t.Errorf("展开后 17 个名字三列应是 6 行，现在 %d 行", got)
	}
	r1, ok1 := tt.Find("插件01")
	r2, ok2 := tt.Find("插件02")
	if !ok1 || !ok2 || r2.X <= r1.X || abs32(r2.Y-r1.Y) > .5 {
		t.Errorf("同一行的名字应并排：%v %v", r1, r2)
	}
	tt.Advance(time.Second)
	tt.Frame()
	_, rects := collectSurfaces(k) // 先画一帧，记下展开时分组面多高
	tt.Frame()
	var full float32
	for id, r := range rects {
		if id == "g2" {
			full = r.H
		}
	}
	if err := tt.Click("另有 17 个（只读）"); err != nil {
		t.Fatal(err)
	}
	if open {
		t.Error("再点一下应收回去")
	}
	// 收回去是一路缩的（Disclose，主人 2026-11-15“设置页折叠”），不是一下子没了
	var hs []float32
	for i := 0; i < 40; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		hs = append(hs, rects["g2"].H)
		if !tt.FramePending() {
			break
		}
	}
	mid := 0
	for _, h := range hs {
		if h < full-5 && h > hs[len(hs)-1]+5 {
			mid++
		}
	}
	if full == 0 || mid < 3 {
		t.Errorf("收起时分组面的高度中途只有 %d 帧：展开时 %.1f，逐帧 %v", mid, full, hs)
	}
}

// 多选筛选片：点一下切换自己，不影响别的，可以全不选。
func TestMultiChipsToggle(t *testing.T) {
	k := testKit()
	on := []bool{true, false}
	changed := 0
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() {
			if k.MultiChips(c, "m", []string{"甲", "乙"}, on) {
				changed++
			}
		})
	}, 300, 100)
	tt.Frame()
	if err := tt.Click("乙"); err != nil {
		t.Fatal(err)
	}
	if !on[0] || !on[1] || changed != 1 {
		t.Errorf("点乙后应是 [甲 乙] 都开：%v，changed=%d", on, changed)
	}
	if err := tt.Click("甲"); err != nil {
		t.Fatal(err)
	}
	if on[0] || !on[1] {
		t.Errorf("点甲后甲应关、乙不变：%v", on)
	}
}

// 步进器的单位：两个字的单位（分钟、小时）不再被截成“…”，不同单位时整个加减框一样宽。
func TestStepperUnitWidth(t *testing.T) {
	k := testKit()
	g := k.G
	if w := stepperUnitWidth("条", g.IconSmall); w != g.IconSmall {
		t.Errorf("一个字的单位宽 %v，应为 %v", w, g.IconSmall)
	}
	if w := stepperUnitWidth("分钟", g.IconSmall); w != 2*g.IconSmall {
		t.Errorf("两个字的单位宽 %v，应为 %v", w, 2*g.IconSmall)
	}
	widths := map[string]float32{}
	for _, unit := range []string{"条", "分钟", "%"} {
		_, rects := collectSurfaces(k)
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			ui.Column(c).Fill().Padding(10).Children(func() {
				k.Stepper(c, "s", "次数", 3, StepperSpec{Min: 1, Max: 9, Unit: unit}, func(float64) {})
			})
		}, 400, 100)
		tt.Frame()
		tt.Frame()
		widths[unit] = rects["s"].W
		if unit == "分钟" && !tt.HasText("分钟") {
			t.Error("“分钟”应完整显示")
		}
	}
	if abs32(widths["条"]-widths["分钟"]) > .5 || abs32(widths["条"]-widths["%"]) > .5 {
		t.Errorf("不同单位的加减框宽度应一样：%v", widths)
	}
}
