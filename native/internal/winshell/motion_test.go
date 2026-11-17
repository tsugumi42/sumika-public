package winshell

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 过渡动画第一批（规格-过渡动画 第七节）：每个新动效一条测试，量起点、中途、终点三帧；
// 每条都在“减少动态效果”开着和关着各跑一遍：淡入淡出还在、位移为 0、终点逐像素一致。

func motionTester(view func(c *ui.Context), w, h int, reduce bool) *ui.Tester {
	tt := ui.NewTester(view, w, h)
	tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
	return tt
}

// lum 是像素的 R+G+B（0～765）。
func lum(p color.RGBA) int { return int(p.R) + int(p.G) + int(p.B) }

// sameImage 说两张图逐像素一样。
func sameImage(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

// dist 是两个颜色通道的差。
func dist(a, b uint8) int { return int(math.Abs(float64(a) - float64(b))) }

// settle 让动画走完。
func settle(tt *ui.Tester) {
	tt.Advance(time.Second)
	tt.Frame()
}

// step 往前走 d 再画一帧。
func step(tt *ui.Tester, d time.Duration) {
	tt.Advance(d)
	tt.Frame()
}

// 菜单：淡入 + 从触发的一边出来 6 + 缩放 .98（180 毫秒），出场更快（130）；退场期间拷贝还在、不收点击；减少动态时只淡不位移。
func TestMenuPopMotion(t *testing.T) {
	var finals [2]*image.RGBA
	for i, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		s := &State{}
		chosen := 0
		sp := &SidebarSpec{Menu: []MenuItem{{Label: "新建对话", Action: func() { chosen++ }}, {Label: "新建群聊"}}, MenuLabel: "更多"}
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			c.Root().Background(ui.RGB(0, 0, 0))
			ui.Row(c.Key("r")).Padding(20).Children(func() { k.plusMenu(c, s, sp) })
		}, 300, 300, reduce)
		tt.Frame()
		s.menuOpen = true
		tt.Frame()
		r0, _ := tt.Find("新建对话")
		a0 := lum(tt.Image().RGBAAt(int(r0.X)-3, int(r0.Y+r0.H/2)))
		step(tt, 30*time.Millisecond)
		r1, ok := tt.Find("新建对话")
		if !ok {
			t.Fatalf("reduce=%v 菜单没出来", reduce)
		}
		a1 := lum(tt.Image().RGBAAt(int(r1.X)-3, int(r1.Y+r1.H/2)))
		settle(tt)
		rEnd, _ := tt.Find("新建对话")
		aEnd := lum(tt.Image().RGBAAt(int(rEnd.X)-3, int(rEnd.Y+rEnd.H/2)))
		finals[i] = tt.Image()
		if !(a0 < a1 && a1 < aEnd) || a0 > aEnd/10 {
			t.Errorf("reduce=%v 菜单淡入三帧亮度 %d → %d → %d，应从暗逐步到亮", reduce, a0, a1, aEnd)
		}
		dy := abs32(r1.Y - rEnd.Y)
		if reduce && dy > 0.01 {
			t.Errorf("减少动态效果时菜单还在位移：%.2f", dy)
		}
		if !reduce && dy < 1 {
			t.Errorf("菜单进场没有位移：%.2f（应从触发钮那边出来约 6）", dy)
		}
		if got := abs32(r0.Y - rEnd.Y); !reduce && (got < 4 || got > 8) {
			t.Errorf("进场起点离停稳位置 %.2f，应约 6", got)
		}

		// 退场：130 毫秒，期间拷贝还在，点它不触发菜单项，放完就没了
		s.menuOpen = false
		tt.Frame()
		step(tt, 40*time.Millisecond)
		cx, cy := int(rEnd.X)-3, int(rEnd.Y+rEnd.H/2)
		if mid := lum(tt.Image().RGBAAt(cx, cy)); mid <= 0 || mid >= aEnd {
			t.Errorf("reduce=%v 退场中亮度 %d，应在 0 和 %d 之间（拷贝还在、正在淡出）", reduce, mid, aEnd)
		}
		tt.ClickAt(rEnd.X+rEnd.W/2, rEnd.Y+rEnd.H/2)
		tt.Frame()
		if chosen != 0 {
			t.Errorf("reduce=%v 退场的拷贝还在收点击，菜单项被触发了 %d 次", reduce, chosen)
		}
		settle(tt)
		if got := lum(tt.Image().RGBAAt(cx, cy)); got != 0 {
			t.Errorf("reduce=%v 退场放完菜单还在：%d", reduce, got)
		}
		if tt.FramePending() {
			t.Errorf("reduce=%v 动画放完还在要帧", reduce)
		}
	}
	if !sameImage(finals[0], finals[1]) {
		t.Error("减少动态效果和平时，菜单停稳后的画面不一样")
	}
}

// 下拉：同一套进出场，面板和入口同宽；减少动态时只淡不位移。
func TestDropdownPopMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		open := false
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			c.Root().Background(ui.RGB(0, 0, 0))
			ui.Column(c.Key("root")).Width(300).Padding(20).Children(func() {
				k.Dropdown(c, "font", "字体", &open, 0, []string{"苹方", "宋体"}, func(int) {}, nil)
			})
		}, 400, 400, reduce)
		tt.Frame()
		tt.Frame()
		open = true
		tt.Frame()
		step(tt, 30*time.Millisecond)
		r1, ok := tt.Find("宋体")
		if !ok {
			t.Fatalf("reduce=%v 下拉没出来", reduce)
		}
		a1 := lum(tt.Image().RGBAAt(int(r1.X)-3, int(r1.Y+r1.H/2)))
		settle(tt)
		rEnd, _ := tt.Find("宋体")
		aEnd := lum(tt.Image().RGBAAt(int(rEnd.X)-3, int(rEnd.Y+rEnd.H/2)))
		if a1 <= 0 || a1 >= aEnd {
			t.Errorf("reduce=%v 下拉淡入中亮度 %d，应在 0 和 %d 之间", reduce, a1, aEnd)
		}
		if dy := abs32(r1.Y - rEnd.Y); (reduce && dy > 0.01) || (!reduce && dy < 1) {
			t.Errorf("reduce=%v 下拉位移 %.2f", reduce, dy)
		}
		open = false
		tt.Frame()
		step(tt, 40*time.Millisecond)
		if mid := lum(tt.Image().RGBAAt(int(rEnd.X)-3, int(rEnd.Y+rEnd.H/2))); mid <= 0 || mid >= aEnd {
			t.Errorf("reduce=%v 下拉退场中亮度 %d", reduce, mid)
		}
		settle(tt)
		if got := lum(tt.Image().RGBAAt(int(rEnd.X)-3, int(rEnd.Y+rEnd.H/2))); got != 0 {
			t.Errorf("reduce=%v 下拉退场放完还在：%d", reduce, got)
		}
	}
}

// 浮层有阴影：面板外一圈（偏下）比没有阴影时暗。
func TestFloatPanelHasShadow(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	open := false
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		c.Root().Background(ui.RGB(255, 255, 255))
		ui.Column(c.Key("root")).Width(300).Padding(20).Children(func() {
			k.Dropdown(c, "font", "字体", &open, 0, []string{"苹方", "宋体"}, func(int) {}, nil)
		})
	}, 400, 400, true)
	tt.Frame()
	open = true
	tt.Frame()
	settle(tt)
	r, _ := tt.Find("宋体")
	// 选项下面几个像素：在面板外沿之外，阴影（0, 8, 24）在这里压暗白底
	below := tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H+k.G.SpaceTight+k.G.ControlInset)+6)
	if lum(below) >= 3*255 {
		t.Errorf("面板下沿外没有阴影：%+v", below)
	}
}

// 对话框：遮罩 220 毫秒淡入（160 出），面板另外上移 10 缩放 .985；退场拷贝保留内容，Enter、点击都不触发按钮；减少动态时只淡不位移。
func TestDialogMotion(t *testing.T) {
	var finals [2]*image.RGBA
	for i, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		open := false
		acted := 0
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			c.Root().Background(ui.RGB(255, 255, 255))
			k.ActionDialog(c, "d", &open, DialogSpec{Title: "标题", Actions: []DialogAction{{ID: "ok", Label: "好的", Default: true}}, OnAction: func(string) { acted++ }}, func() {
				ui.Text(c, "正文")
			})
		}, 600, 400, reduce)
		tt.Frame()
		open = true
		tt.Frame()
		corner := func() int { return lum(tt.Image().RGBAAt(2, 2)) }
		c0 := corner()
		r0, _ := tt.Find("标题")
		step(tt, 30*time.Millisecond)
		c1 := corner()
		r1, _ := tt.Find("标题")
		settle(tt)
		cEnd := corner()
		rEnd, _ := tt.Find("标题")
		finals[i] = tt.Image()
		if !(c0 > c1 && c1 > cEnd) || c0 != 3*255 {
			t.Errorf("reduce=%v 遮罩淡入三帧 %d → %d → %d，应从白逐步压暗", reduce, c0, c1, cEnd)
		}
		if reduce {
			if abs32(r0.Y-rEnd.Y) > 0.01 || abs32(r1.Y-rEnd.Y) > 0.01 {
				t.Errorf("减少动态效果时对话框还在位移：起点 %.2f 中途 %.2f", r0.Y-rEnd.Y, r1.Y-rEnd.Y)
			}
		} else if d := r0.Y - rEnd.Y; d < 8 || d > 14 {
			t.Errorf("对话框进场起点离停稳位置 %.2f，应约 10（含缩放）", d)
		}

		// 退场
		open = false
		tt.Frame()
		step(tt, 40*time.Millisecond)
		if mid := corner(); mid <= cEnd || mid >= 3*255 {
			t.Errorf("reduce=%v 对话框退场中遮罩亮度 %d，应在 %d 和 765 之间", reduce, mid, cEnd)
		}
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		tt.ClickAt(rEnd.X+rEnd.W/2, rEnd.Y+rEnd.H*3)
		tt.Frame()
		if acted != 0 {
			t.Errorf("reduce=%v 退场的拷贝还在收输入，按钮被触发 %d 次", reduce, acted)
		}
		settle(tt)
		if got := corner(); got != 3*255 {
			t.Errorf("reduce=%v 退场放完遮罩还在：%d", reduce, got)
		}
		// 再开一次还会播进场
		open = true
		tt.Frame()
		if again := corner(); again < 3*255-30 {
			t.Errorf("reduce=%v 第二次打开没有从头淡入：第一帧遮罩 %d", reduce, again)
		}
	}
	if !sameImage(finals[0], finals[1]) {
		t.Error("减少动态效果和平时，对话框停稳后的画面不一样")
	}
}

// 导航换页、窄窗口、聊天里换会话、设置里换分类：新内容 200 毫秒淡入、无位移，旧内容直接换掉；窗口按钮和标题行不动。
func TestChatSwitchFades(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		s := &State{}
		key := "a"
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			k.ChatOnly(c, s, ChatSpec{Key: key, Body: func(l ChatLayout) {
				ui.Box(c.Key("msgs-" + key)).Absolute().Left(0).Right(0).Top(0).Bottom(0).Background(ui.RGB(200, 0, 0))
			}})
		}, 600, 400, reduce)
		tt.Frame()
		tt.Frame()
		px := func() color.RGBA { return tt.Image().RGBAAt(300, 200) }
		if p := px(); p.R != 200 {
			t.Fatalf("reduce=%v 启动那一帧就该是不透明的，没有淡入：%+v", reduce, p)
		}
		key = "b"
		tt.Frame()
		start := px()
		step(tt, 40*time.Millisecond)
		mid := px()
		settle(tt)
		end := px()
		if d0, d1 := dist(start.R, end.R), dist(mid.R, end.R); end.R != 200 || !(d0 > d1 && d1 > 0) {
			t.Errorf("reduce=%v 换会话淡入三帧红通道 %d → %d → %d，应从底色逐步到 200", reduce, start.R, mid.R, end.R)
		}
		if tt.FramePending() {
			t.Errorf("reduce=%v 淡入放完还在要帧", reduce)
		}
		// 没换会话就不再淡入
		tt.Frame()
		if p := px(); p.R != 200 {
			t.Errorf("reduce=%v 同一个会话重画时又淡了：%+v", reduce, p)
		}
	}
}

// 换页：内容区淡入，导航栏不动。
func TestPageSwitchFades(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		s := &State{}
		page := "memory"
		nav := []NavItem{{ID: "memory", Label: "记忆", Icon: IconSide}, {ID: "run", Label: "运行", Icon: IconSide}}
		tt := motionTester(func(c *ui.Context) {
			k.Frame(c, s, &Spec{Nav: nav, Page: page, OnPage: func(id string) { page = id }, Content: func() {
				ui.Box(c.Key("legacy-" + page)).Fill().Background(ui.RGB(0, 150, 0))
			}})
		}, 900, 600, reduce)
		tt.Frame()
		tt.Frame()
		content := func() color.RGBA { return tt.Image().RGBAAt(500, 400) }
		rail := func() color.RGBA { return tt.Image().RGBAAt(8, 300) }
		if p := content(); p.G != 150 {
			t.Fatalf("reduce=%v 启动那一帧内容就该不透明：%+v", reduce, p)
		}
		railBefore := rail()
		page = "run"
		tt.Frame()
		start := content()
		step(tt, 40*time.Millisecond)
		mid := content()
		if rail() != railBefore {
			t.Errorf("reduce=%v 换页时导航栏变了", reduce)
		}
		settle(tt)
		end := content()
		if d0, d1 := dist(start.G, end.G), dist(mid.G, end.G); end.G != 150 || !(d0 > d1 && d1 > 0) {
			t.Errorf("reduce=%v 换页淡入三帧绿通道 %d → %d → %d", reduce, start.G, mid.G, end.G)
		}
	}
}

// 设置里换分类：内容淡入，标题行和分类列表不动。
func TestSettingsCategoryFades(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		s := &State{}
		sel := "look"
		page := "settings"
		nav := []NavItem{{ID: "settings", Label: "设置", Icon: IconSide}}
		st := &SettingsSpec{Categories: []Category{{ID: "look", Label: "外观"}, {ID: "chat", Label: "聊天"}}}
		tt := motionTester(func(c *ui.Context) {
			st.Selected, st.Title = sel, sel
			st.Content = func() {
				ui.Box(c.Key("body-" + sel)).FillWidth().Height(300).Background(ui.RGB(0, 0, 200))
			}
			k.Frame(c, s, &Spec{Nav: nav, Page: page, OnPage: func(string) {}, Settings: st})
		}, 1000, 700, reduce)
		tt.Frame()
		tt.Frame()
		content := func() color.RGBA { return tt.Image().RGBAAt(600, 200) }
		if p := content(); p.B != 200 {
			t.Fatalf("reduce=%v 启动那一帧内容就该不透明：%+v", reduce, p)
		}
		sel = "chat"
		tt.Frame()
		start := content()
		step(tt, 40*time.Millisecond)
		mid := content()
		settle(tt)
		end := content()
		if d0, d1 := dist(start.B, end.B), dist(mid.B, end.B); end.B != 200 || !(d0 > d1 && d1 > 0) {
			t.Errorf("reduce=%v 换分类淡入三帧蓝通道 %d → %d → %d", reduce, start.B, mid.B, end.B)
		}
	}
}

// 选中这一族（导航项）：选中块的底色、阴影 140 毫秒淡进淡出；减少动态时淡变还在（颜色本来就不受它影响）。
func TestSelectFadeOnNav(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		s := &State{}
		page := "memory"
		nav := []NavItem{{ID: "memory", Label: "记忆", Icon: IconSide}, {ID: "run", Label: "运行", Icon: IconSide}}
		tt := motionTester(func(c *ui.Context) {
			k.Frame(c, s, &Spec{Nav: nav, Page: page, OnPage: func(id string) { page = id }, Content: func() {}})
		}, 900, 600, reduce)
		tt.Frame()
		tt.Frame()
		r, ok := tt.Find("运行")
		if !ok {
			t.Fatalf("找不到导航项：%v", tt.Texts())
		}
		sample := func() int { return lum(tt.Image().RGBAAt(int(r.X+r.W/2)+16, int(r.Y+r.H/2))) }
		before := sample()
		page = "run"
		tt.Frame()
		step(tt, 40*time.Millisecond)
		mid := sample()
		settle(tt)
		after := sample()
		if before == after || !(math.Min(float64(before), float64(after)) < float64(mid) && float64(mid) < math.Max(float64(before), float64(after))) {
			t.Errorf("reduce=%v 选中块底色 %d → %d → %d，中途应在两端之间", reduce, before, mid, after)
		}
	}
}

// 悬停提示：停够时间后淡入（150 毫秒）加上移 2，离开淡出（100 毫秒）；减少动态时只淡不位移。
func TestTipMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		k.SetMaterial(MaterialSolid)
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			ui.Box(c.Key("bg")).Fill().Background(ui.RGB(255, 255, 255)).Center().Children(func() {
				k.IconButton(c, "b", "最小化窗口", IconSide, func() {})
			})
		}, 300, 200, reduce)
		tt.Frame()
		dark := func() int {
			img := tt.Image()
			n := 0
			for i := 0; i+3 < len(img.Pix); i += 4 {
				n += 765 - (int(img.Pix[i]) + int(img.Pix[i+1]) + int(img.Pix[i+2]))
			}
			return n
		}
		tt.Move(150, 100)
		tt.Frame()
		step(tt, 300*time.Millisecond) // 悬停底色先淡完，提示还没出（600 毫秒才出）
		base := dark()
		var onset time.Duration
		for onset = 0; onset < 2*time.Second; onset += 16 * time.Millisecond {
			step(tt, 16*time.Millisecond)
			if dark() > base+2000 {
				break
			}
		}
		if onset >= 2*time.Second {
			t.Fatalf("reduce=%v 停在按钮上 2 秒提示也没出来", reduce)
		}
		first := dark()
		step(tt, 40*time.Millisecond)
		mid := dark()
		settle(tt)
		full := dark()
		if !(first < mid && mid < full) {
			t.Errorf("reduce=%v 提示淡入三帧深色量 %d → %d → %d，应逐步变深", reduce, first, mid, full)
		}
		tt.Move(280, 190)
		tt.Frame()
		step(tt, 40*time.Millisecond)
		out := dark()
		if !(base < out && out < full) {
			t.Errorf("reduce=%v 提示退场中深色量 %d，应在 %d 和 %d 之间（拷贝还在淡出）", reduce, out, base, full)
		}
		settle(tt)
		if gone := dark(); gone > base+2000 {
			t.Errorf("reduce=%v 提示退场放完还在：%d（底 %d）", reduce, gone, base)
		}
	}
}

// 勾选框：只有勾上、取消才动（底色和对勾 140 毫秒淡变），进页面时已勾的静止。
func TestCheckFadesOnlyOnChange(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		on := true
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			ui.Box(c.Key("bg")).Fill().Background(k.P.Composer).Children(func() {
				k.Check(c, "chk", "勾选", &on, false)
			})
		}, 200, 100, reduce)
		tt.Frame()
		box := func() color.RGBA {
			return tt.Image().RGBAAt(int(k.G.ControlInset)+int(k.G.IconSize)/2, int(k.G.HitSmall)/2)
		}
		if p := box(); p.R == p.G && p.G == p.B && lum(p) > 600 {
			t.Fatalf("reduce=%v 进页面时已勾的框应是强调色实底：%+v", reduce, p)
		}
		start := box()
		tt.Frame()
		if box() != start || tt.FramePending() {
			t.Errorf("reduce=%v 进页面已勾的框不该在动", reduce)
		}
		on = false
		tt.Frame()
		step(tt, 40*time.Millisecond)
		mid := box()
		settle(tt)
		end := box()
		if mid == start || mid == end {
			t.Errorf("reduce=%v 取消勾选时底色没有淡变：%+v → %+v → %+v", reduce, start, mid, end)
		}
	}
}

// 开关：轨道颜色的混合量带 NonMotion，减少动态效果时颜色照样淡变，圆钮位移直接到位。
func TestSwitchTrackFadesUnderReduceMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		on := false
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			ui.Box(c.Key("bg")).Fill().Background(k.P.Composer).Children(func() { k.Switch(c, "sw", "开关", &on) })
		}, 120, 60, reduce)
		tt.Frame()
		tt.Frame()
		r, _ := tt.Find("开关")
		sample := func() color.RGBA { return tt.Image().RGBAAt(int(r.X)+5, int(r.Y+r.H/2)) }
		on = true
		tt.Frame()
		step(tt, 40*time.Millisecond)
		mid := sample()
		settle(tt)
		end := sample()
		if reduce && mid == end { // 圆钮已经直接到了右边，左边露出的轨道颜色还在淡变
			t.Errorf("减少动态效果时开关轨道颜色是硬切的：中途 %+v 终点 %+v", mid, end)
		}
	}
}
