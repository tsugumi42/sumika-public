package winshell

import (
	"math"
	"testing"

	"sumika-native/internal/chatui"
)

func testKit() *Kit { return NewKit(chatui.DefaultSettings(), "") }

// 断点：窄于 713 三层，713 到 1005 两栏加抽屉，1006 起三栏（原型 tokens.go 的推法）。
func TestModeBreakpoints(t *testing.T) {
	k := testKit()
	if k.G.BreakpointNarrow != 705 || k.G.BreakpointWide != 998 {
		t.Fatalf("断点 %v / %v，应为 705 / 998", k.G.BreakpointNarrow, k.G.BreakpointWide)
	}
	for _, c := range []struct {
		w    float32
		want Wide
	}{{380, Narrow}, {704, Narrow}, {705, Medium}, {997, Medium}, {998, Full}, {2048, Full}} {
		if got := k.Mode(c.w); got != c.want {
			t.Errorf("宽 %v：档位 %v，应为 %v", c.w, got, c.want)
		}
	}
}

// 列表默认 300；拖过就按拖出来的比例，夹在上限内；头像列固定 62。
func TestPlanListWidth(t *testing.T) {
	k := testKit()
	var s State
	for _, w := range []float32{800, 1100, 1400, 2048} {
		k.Plan(&s, w, false)
		if s.ListWidth != ListDefault {
			t.Errorf("宽 %v：列表 %v，应为 %v", w, s.ListWidth, ListDefault)
		}
	}
	s.Panes.ListRatio = 0.2
	k.Plan(&s, 2048, false)
	if got := s.ListWidth; math.Abs(float64(got-0.2*(2048-64))) > 1 {
		t.Errorf("拖出来的比例没生效：%v", got)
	}
	s.Panes.ListRatio = 0.9 // 夹在 540
	k.Plan(&s, 2048, false)
	if s.ListWidth != ListMax {
		t.Errorf("列表宽超过上限：%v", s.ListWidth)
	}
	s.Panes.Compact = true
	k.Plan(&s, 2048, false)
	if s.ListWidth != 62 {
		t.Errorf("头像列宽 %v，应为 8 + 46 + 8 = 62", s.ListWidth)
	}
}

// 三栏：右栏默认 320，列表 + 右栏放不下就一起收，右栏停在 292，列表停在 260，剩下的给聊天（至少 380）。
func TestPlanThreeColumns(t *testing.T) {
	k := testKit()
	s := State{Panes: Panes{SideOpen: true}}
	k.Plan(&s, 2048, false)
	if s.SideShown != SideDefault {
		t.Errorf("右栏默认宽 %v，应为 %v", s.SideShown, SideDefault)
	}
	for w := float32(1006); w <= 2048; w += 41 {
		k.Plan(&s, w, false)
		chat := w - k.G.NavWidth - s.ListWidth - s.SideShown - 2*SplitSize
		if chat < ChatMin-1 {
			t.Errorf("宽 %v：聊天只剩 %v，不够 %v", w, chat, ChatMin)
		}
		if s.SideShown < SideMin || s.SideShown > SideMax {
			t.Errorf("宽 %v：右栏 %v 越界", w, s.SideShown)
		}
	}
	s.Panes.SideWidth = 600 // 夹在 392
	k.Plan(&s, 2048, false)
	if s.SideShown != SideMax {
		t.Errorf("右栏超过上限：%v", s.SideShown)
	}
}

// 拖分隔条：拖过记下比例；拖到比 130 窄收成头像列；从头像列拖宽过 130 恢复。
func TestDragListCompactTransitions(t *testing.T) {
	k := testKit()
	var s State
	k.Plan(&s, 2048, false)
	s.ListWidth = 400
	k.DragList(&s, 2048, false)
	if s.Panes.ListRatio <= 0 || s.Panes.Compact {
		t.Fatalf("普通拖动：%+v", s.Panes)
	}
	s.ListWidth = 100
	k.DragList(&s, 2048, false)
	if !s.Panes.Compact {
		t.Fatal("拖到 100 应收成头像列")
	}
	s.ListWidth = 300
	k.DragList(&s, 2048, false)
	if s.Panes.Compact {
		t.Fatal("从头像列拖宽到 300 应恢复")
	}
}

// 开关右栏：三栏是切换并排的栏；中等宽度是抽屉（开着时在第三层，再点关）；窄窗口是第三层。
func TestToggleSideByMode(t *testing.T) {
	k := testKit()
	var s State
	k.ToggleSide(&s, 2048)
	if !s.Panes.SideOpen {
		t.Fatal("三栏开右栏没开")
	}
	k.ToggleSide(&s, 2048)
	if s.Panes.SideOpen {
		t.Fatal("三栏再点没关")
	}
	k.ToggleSide(&s, 900)
	if !s.Panes.SideOpen || s.Layer != LayerSide {
		t.Fatalf("中等宽度应打开抽屉：%+v %v", s.Panes, s.Layer)
	}
	k.ToggleSide(&s, 900)
	if s.Panes.SideOpen || s.Layer != LayerChat {
		t.Fatalf("抽屉再点应关：%+v %v", s.Panes, s.Layer)
	}
	s = State{Layer: LayerChat}
	k.ToggleSide(&s, 640)
	if s.Layer != LayerSide {
		t.Fatalf("窄窗口开右栏应进第三层：%v", s.Layer)
	}
	k.ToggleSide(&s, 640)
	if s.Layer != LayerChat {
		t.Fatalf("窄窗口再点应回聊天：%v", s.Layer)
	}
}

func TestParseMaterial(t *testing.T) {
	for in, want := range map[string]Material{"liquid": MaterialLiquid, "blur": MaterialBlur, "solid": MaterialSolid, "": MaterialLiquid, "玻璃": MaterialLiquid} {
		if got := ParseMaterial(in); got != want {
			t.Errorf("%q → %v，应为 %v", in, got, want)
		}
	}
	for _, m := range []Material{MaterialLiquid, MaterialBlur, MaterialSolid} {
		if ParseMaterial(m.String()) != m {
			t.Errorf("%v 的名字存下来再读不回来", m)
		}
	}
	k := testKit()
	k.SetMaterial(MaterialBlur)
	k.Reduce = true
	if k.EffectiveMaterial() != MaterialSolid {
		t.Error("降低透明度时必须是实色")
	}
}

// 几何令牌和原型默认值逐项一致（默认外观下每个数都是原型 tokens.go 里写的基准）。
func TestGeometryDefaults(t *testing.T) {
	g := testKit().G
	for name, c := range map[string][2]float32{
		"导航宽": {g.NavWidth, 64}, "顶行高": {g.HeaderHeight, 56}, "导航项高": {g.NavItemHeight, 56}, "会话头像": {g.ConversationAvatar, 46},
		"会话内距": {g.ConversationInset, 8}, "搜索框高": {g.SearchHeight, 48}, "控件高": {g.HitSmall, 32}, "头像": {g.AvatarSize, 32},
		"控件圆角": {g.RadiusControl, 12}, "分段圆角": {g.RadiusSegment, 8}, "导航项圆角": {g.RadiusNavItem, 16}, "输入框最小高": {g.ComposerMin, 48}, // 胶囊（ADR-351）：等于控件大档
		"头像列宽": {g.ListCompactWidth, 62}, "面板圆角": {g.RadiusPanel, 28},
	} {
		if c[0] != c[1] {
			t.Errorf("%s：%v，原型是 %v", name, c[0], c[1])
		}
	}
}

// 三栏开着右栏，拖窄到抽屉档：右栏接着以浮动面板显示，不凭空消失。
func TestSideOpenSurvivesShrinkToMedium(t *testing.T) {
	k := testKit()
	s := State{Panes: Panes{SideOpen: true}, Layer: LayerChat}
	k.Plan(&s, 2048, false)
	k.Plan(&s, 900, false)
	if !k.SideFloating(&s, 900) {
		t.Fatalf("拖窄到中等宽度，开着的右栏没有浮出来：%+v %v", s.Panes, s.Layer)
	}
}

// 不看宽度打开右栏（点“本轮用量”）：抽屉档和窄窗口都能显示。
func TestOpenSideShowsInEveryMode(t *testing.T) {
	k := testKit()
	for _, w := range []float32{2048, 900, 640} {
		var s State
		s.Layer = LayerChat
		s.OpenSide()
		k.Plan(&s, w, false)
		shown := s.Panes.SideOpen
		switch k.Mode(w) {
		case Medium:
			shown = k.SideFloating(&s, w)
		case Narrow:
			shown = s.Layer == LayerSide
		}
		if !shown {
			t.Errorf("宽 %v：OpenSide 后右栏没显示 %+v %v", w, s.Panes, s.Layer)
		}
	}
}
