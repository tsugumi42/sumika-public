package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/catalog"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 画廊自己的两个尺寸：列表宽固定，顶栏和列表不跟着外观滑块变（拖字号时手底下的滑块不能跑）。
const (
	listWidth float32 = 200 // 自有尺寸：左边列表的固定宽，不随字号滑块变
	toolSlide float32 = 128 // 自有尺寸：顶栏滑块的固定宽
)

// gallery 是画廊的全部状态。chrome 是顶栏和列表用的一套令牌（只跟亮暗、底色走），stage 是舞台用的（跟全部开关走）。
type gallery struct {
	font, mono string
	chrome     *winshell.Kit
	stage      *winshell.Kit
	store      *catalog.Store
	entries    []catalog.Entry

	// 顶栏开关
	dark, neutral, material int
	text, radius, density   float32
	layers                  bool
	speed                   int // 动画速度：0 正常、1 四分之一、2 十分之一（只放慢舞台里的控件）
	replays                 int // 按过几次“重放”

	sel      string // 选中的条目 ID
	filter   string
	stageH   ui.Handle
	filterH  ui.Handle
	focusNew bool // 刚切了条目：这一帧不放动画

	// 显示层：Draw 时扫描的结果；shown 是上次构建时界面上写的层数，不一样就再要一帧
	depth, shown int
	layerErr     string
}

func newGallery(font, mono string) *gallery {
	g := &gallery{font: font, mono: mono, store: catalog.NewStore(), entries: catalog.All(),
		text: chatui.TextDefault, radius: chatui.RadiusDefault, density: chatui.DensityDefault}
	g.chrome, g.stage = winshell.NewKit(chatui.DefaultSettings(), font), winshell.NewKit(chatui.DefaultSettings(), font)
	g.apply()
	if len(g.entries) > 0 {
		g.sel = g.entries[0].ID
	}
	g.benchStart()
	return g
}

// benchStart 读几个只给量帧耗时用的环境变量，让自动化客户端不用点就能进到要量的画面；一个都没设就什么也不做：
// SUMIKA_GALLERY_ENTRY 选条目；SUMIKA_GALLERY_BUBBLE 选气泡材质（solid、thin、thick）；SUMIKA_GALLERY_BAR=0 去掉长对话上的玻璃浮条；SUMIKA_GALLERY_SOLO=1 只留长对话那张卡；SUMIKA_GALLERY_SPEED=1/2 一开始就慢放（1/4、1/10）；SUMIKA_GALLERY_DARK=1 夜间。
func (g *gallery) benchStart() {
	if id := os.Getenv("SUMIKA_GALLERY_ENTRY"); id != "" {
		g.sel = id
	}
	switch os.Getenv("SUMIKA_GALLERY_BUBBLE") {
	case "thin":
		g.store.Look.Mode = 1
	case "thick":
		g.store.Look.Mode = 2
	}
	if os.Getenv("SUMIKA_GALLERY_BAR") == "0" {
		g.store.Look.Bar = false
	}
	g.store.Look.Solo = os.Getenv("SUMIKA_GALLERY_SOLO") == "1"
	switch os.Getenv("SUMIKA_GALLERY_SPEED") { // 动画速度：1 是 1/4，2 是 1/10
	case "1":
		g.speed = 1
	case "2":
		g.speed = 2
	}
	if os.Getenv("SUMIKA_GALLERY_DARK") == "1" {
		g.dark = 1
	}
}

// apply 把顶栏的开关换成两套令牌；设置没变 Kit.Set 什么也不做。
func (g *gallery) apply() {
	s := chatui.DefaultSettings()
	s.Dark, s.Neutral = g.dark == 1, chatui.Neutral(g.neutral)
	g.chrome.Set(s, g.font)
	s.Text, s.Radius, s.Density = g.text, g.radius, g.density
	g.stage.Set(s, g.font)
	g.stage.SetMaterial(winshell.Material(g.material))
	g.chrome.SetMaterial(winshell.Material(g.material))
}

// visible 是筛选后的条目（按族名、名字、简介、ID 里有没有输入的字，不分大小写）。
func (g *gallery) visible() []catalog.Entry {
	q := strings.ToLower(strings.TrimSpace(g.filter))
	if q == "" {
		return g.entries
	}
	var out []catalog.Entry
	for _, e := range g.entries {
		if strings.Contains(strings.ToLower(e.Family+" "+e.Name+" "+e.Blurb+" "+e.ID), q) {
			out = append(out, e)
		}
	}
	return out
}

func (g *gallery) index(list []catalog.Entry) int {
	for i, e := range list {
		if e.ID == g.sel {
			return i
		}
	}
	return -1
}

// move 在筛选后的列表里走 d 步，到头不绕回去。
func (g *gallery) move(d int) {
	list := g.visible()
	if len(list) == 0 {
		return
	}
	i := g.index(list)
	if i < 0 {
		i = 0
	} else {
		i = max(0, min(len(list)-1, i+d))
	}
	g.pick(list[i].ID)
}

// replay 把当前条目的状态拨回初始并重放进场（顶栏的“重放”和 R 键）：值和展开状态清掉，
// 选中块会从现在的位置滑回初始项，错开淡入靠换代号重播。
func (g *gallery) replay() {
	g.store.Reset(g.sel)
	g.replays++
}

// speedFactor 是动画速度分段对应的慢放倍数。
func (g *gallery) speedFactor() float64 {
	switch g.speed {
	case 1:
		return 4
	case 2:
		return 10
	}
	return 1
}

func (g *gallery) pick(id string) {
	if g.sel != id {
		g.sel, g.focusNew = id, true
	}
}

func (g *gallery) view(c *ui.Context) {
	g.apply()
	k := g.chrome
	c.SetTheme(&k.Theme)
	c.Root().Background(k.P.Canvas)

	list := g.visible()
	if g.index(list) < 0 && len(list) > 0 { // 筛选把选中的筛掉了：跳到第一个剩下的
		g.pick(list[0].ID)
	}
	// 键盘：J / K 在没有文字框接字时切；上下方向键在焦点不在舞台里时切（舞台里的滑块、分段、下拉要用方向键）
	if !c.FocusTakesText() {
		if c.Shortcut(0, ui.KeyJ) {
			g.move(1)
		}
		if c.Shortcut(0, ui.KeyK) {
			g.move(-1)
		}
		if c.Shortcut(0, ui.KeyR) {
			g.replay()
		}
	}
	if !c.FocusWithin(g.stageH) {
		if c.Shortcut(0, ui.KeyDown) {
			g.move(1)
		}
		if c.Shortcut(0, ui.KeyUp) {
			g.move(-1)
		}
	}
	if g.focusNew {
		c.DisableAnimations() // 切换没有转场：这一帧里动画一律到终点
		g.focusNew = false
	}

	ui.Column(c.Key("gallery")).Fill().Children(func() {
		g.toolbar(c)
		ui.Row(c.Key("body")).Fill().AlignItems(ui.Stretch).Grow(1).MinHeight(0).Children(func() {
			g.sidebar(c, list)
			g.stagePane(c, list)
		})
	})
}

// toolbar 是顶上常驻的开关条：亮暗、底色冷暖、三个滑块、面板材质、显示层。放不下就换行。
func (g *gallery) toolbar(c *ui.Context) {
	k := g.chrome
	sp := k.G
	slider := func(key, label string, v *float32, lo, hi, step float32, ticks bool) {
		f := float64(*v)
		ui.Row(c.Key(key + "-bit")).Gap(sp.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, label).FontSize(sp.LabelSize).FixedLineHeight(sp.MetaLine).TextColor(k.P.Muted).SingleLine()
			k.Slider(c, key, &f, lo, hi, step, label, ticks).Width(toolSlide)
			// 拖滑块时数字变得很勤：RollNumber 压到一秒滚一次，停下补到最新值
			k.RollNumber(c, key+"-num", float64(*v), func(f float64) string { return fmt.Sprintf("%g", f) }).FontSize(sp.LabelSize).FixedLineHeight(sp.MetaLine).TextColor(k.P.Muted).SingleLine().MinWidth(sp.ValueWidth)
		})
		*v = float32(f)
	}
	seg := func(key string, v *int, names ...string) { // 分段控件在换行的行里会被挤窄、字折行：包一层，不让它缩
		ui.Row(c.Key(key + "-wrap")).Shrink(0).Children(func() { k.Segment(c, key, v, names) })
	}
	ui.Row(c.Key("toolbar")).FillWidth().Wrap().Gap(sp.SpacePanel).AlignItems(ui.Center).Padding(sp.SpaceSmall).Children(func() {
		ui.Row(c.Key("tb-speed-bit")).Gap(sp.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "动画速度").FontSize(sp.LabelSize).FixedLineHeight(sp.MetaLine).TextColor(k.P.Muted).SingleLine()
			ui.Row(c.Key("tb-speed-wrap")).Shrink(0).Children(func() { // 三格按字数取宽，“1/10”不被挤成省略号
				k.SegmentWith(c, "tb-speed", &g.speed, []string{"正常", "1/4", "1/10"}, winshell.SegmentOptions{Fit: true})
			})
			k.Button(c, "tb-replay", "重放 R", false, false, g.replay)
		})
		seg("tb-dark", &g.dark, "亮色", "夜间")
		seg("tb-neutral", &g.neutral, "冷", "中", "暖")
		slider("tb-text", "字号", &g.text, chatui.TextMin, chatui.TextMax, 1, true)
		slider("tb-radius", "圆角", &g.radius, chatui.RadiusMin, chatui.RadiusMax, 1, false)
		slider("tb-density", "疏密", &g.density, chatui.DensityMin, chatui.DensityMax, 1, false)
		// 材质是几选一的模式，用分段；各格按字数取宽，“液态玻璃”四个字不会折行
		ui.Row(c.Key("tb-material-wrap")).Shrink(0).Children(func() {
			k.SegmentWith(c, "tb-material", &g.material, []string{"液态玻璃", "磨砂", "实色"}, winshell.SegmentOptions{Fit: true})
		})
		ui.Row(c.Key("tb-layers")).Gap(sp.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
			k.Switch(c, "tb-layers-sw", "显示层", &g.layers)
			ui.Text(c, "显示层").FontSize(sp.LabelSize).FixedLineHeight(sp.MetaLine).SingleLine()
		})
	})
}

// sidebar 是左边的族列表：顶上筛选框，下面一行一族。点一下或上下键切，没有转场。
func (g *gallery) sidebar(c *ui.Context, list []catalog.Entry) {
	k := g.chrome
	sp := k.G
	ui.Column(c.Key("sidebar")).Width(listWidth).Shrink(0).FillHeight().Padding(sp.SpaceSmall).Gap(sp.SpaceSmall).Children(func() {
		k.SearchField(c, "filter", &g.filter, "筛选组件", &g.filterH)
		for _, e := range list {
			selected := e.ID == g.sel
			b := ui.ButtonBase(c.Key("item-"+e.ID)).Label(e.Family).FillWidth().Height(sp.HitSmall).Padding(0, sp.ControlInset).AlignItems(ui.Center).Radius(sp.RadiusSegment).Selected(selected).TextColor(k.P.Ink)
			if b.Clicked() {
				g.pick(e.ID)
			}
			switch {
			case selected:
				k.Lift(b)
			case b.Pressed():
				b.Background(k.P.Press)
			case b.Hovered():
				b.Background(k.P.Hover)
			}
			b.Children(func() {
				w := sp.StrongWeight
				if selected {
					w = sp.BoldWeight
				}
				ui.Text(c, e.Family).Grow(1).FontSize(sp.LabelSize).FontWeight(w).FixedLineHeight(sp.MetaLine).SingleLine()
			})
		}
		if len(list) == 0 {
			k.Note(c, "没有符合的组件。", false)
		}
	})
}

// stagePane 是右边的舞台：标题和简介、当前组件的例子；整块用 stage 那套令牌（ui.Themed），弹出层也跟着。
func (g *gallery) stagePane(c *ui.Context, list []catalog.Entry) {
	s := g.stage
	sp := s.G
	cur, ok := catalog.Find(g.sel)
	vw, _ := c.Size()
	// 慢放只罩住舞台这一段构建（顶栏和左边的列表照常速度）；这一帧建完立刻还原，正式界面的代码从不碰这个倍数
	defer winshell.SetMotionScale(winshell.SetMotionScale(g.speedFactor()))
	ui.Themed(c.Key("stage"), &s.Theme).Grow(1).MinWidth(0).FillHeight().Bind(&g.stageH).Children(func() {
		ui.Scroll(c.Key("stage-scroll")).Fill().Padding(sp.SpacePanel).Gap(sp.SpaceSection).Children(func() {
			if !ok {
				s.EmptyState(c, "none", "没有可看的组件", "左边的筛选框里清掉字，或者在 catalog/register.go 里加一条。", "")
				return
			}
			g.stageHeader(c, cur)
			env := catalog.Env{Kit: s, Store: g.store, ID: cur.ID, Mono: g.mono, Room: float32(vw) - listWidth - 2*sp.SpacePanel, Replay: g.replays}
			ui.Column(c.Key("stage-body/" + cur.ID)).FillWidth().Gap(sp.SpaceSection).Children(func() { cur.Demo(c, env) })
		})
		if g.layers {
			g.layerOverlay(c)
		}
	})
}

func (g *gallery) stageHeader(c *ui.Context, cur catalog.Entry) {
	s := g.stage
	sp, p := s.G, s.P
	ui.Row(c.Key("stage-head")).FillWidth().Gap(sp.SpacePanel).AlignItems(ui.Start).Children(func() {
		ui.Column(c.Key("stage-title")).Grow(1).MinWidth(0).Gap(sp.SpaceMicro).Children(func() {
			ui.Text(c, cur.Name).FontSize(sp.TitleSize).FontWeight(sp.BoldWeight).FixedLineHeight(sp.TitleLine)
			ui.Text(c, cur.Blurb).FontSize(sp.LabelSize).FixedLineHeight(sp.MetaLine).TextColor(p.Muted).Wrap()
		})
		if g.layers {
			g.shown = g.depth
			ui.Row(c.Key("stage-layers")).Gap(sp.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
				if g.layerErr != "" {
					ui.Text(c, "显示层不可用："+g.layerErr).FontSize(sp.MetaSize).FixedLineHeight(sp.MetaLine).TextColor(s.T.C.DangerText)
					return
				}
				ui.Text(c, fmt.Sprintf("最深 %d 层", g.depth)).FontSize(sp.LabelSize).FontWeight(sp.BoldWeight).FixedLineHeight(sp.MetaLine)
				for l := 1; l <= g.depth; l++ {
					ui.Text(c.Key(fmt.Sprint("lv", l)), fmt.Sprint(l)).FontSize(sp.LabelSize).FontWeight(sp.BoldWeight).FixedLineHeight(sp.MetaLine).TextColor(g.levelColor(l))
				}
			})
		}
	})
}

// levelColor 是第 l 层的线色：沿用头像色板的八种，换层换色。
func (g *gallery) levelColor(l int) ui.Color {
	t := g.stage.T
	return t.AccentOf(chatui.CharColors[(l-1)%len(chatui.CharColors)]).Strong
}

// layerOverlay 是盖在舞台上的一层透明元素：画的时候读一遍元素树，给每个容器描一圈细线。
// 层数在画的时候才知道，和上次界面上写的不一样就再要一帧，让右上角的字跟上。
func (g *gallery) layerOverlay(c *ui.Context) {
	ui.Box(c.Key("layer-overlay")).Absolute().Left(0).Right(0).Top(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		res, err := layerScan(c, "stage-body/"+g.sel)
		if err != nil {
			if msg := err.Error(); g.layerErr != msg {
				g.layerErr = msg
				p.AnimationFrame()
			}
			return
		}
		if g.layerErr != "" || g.depth != res.Depth || g.shown != res.Depth {
			g.layerErr, g.depth = "", res.Depth
			p.AnimationFrame()
		}
		p.Clip(r, 0, func() {
			for _, b := range res.Boxes {
				p.Stroke(b.Rect, g.levelColor(b.Level), b.Radius, 1)
			}
		})
	})
}
