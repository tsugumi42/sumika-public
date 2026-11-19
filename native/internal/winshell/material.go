package winshell

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// Material 是浮在聊天内容上的控制层（标题栏、输入框）的材质，设计语言第二节第 2 层“跟着面板材质设置”。
type Material int

const (
	MaterialLiquid Material = iota // 液态玻璃（默认，照原型 glassOr）：glass.Glass，叠一层内容色，通透度 0.6
	MaterialBlur                   // 磨砂（照 Bun 版 data-material="glass"）：背景模糊 24 加 72%（输入框 76%）的内容色底
	MaterialSolid                  // 实色：预设的 Surface
)

// ParseMaterial 把设置里存的字符串变成材质；不认识的回到液态玻璃。
func ParseMaterial(s string) Material {
	switch s {
	case "blur":
		return MaterialBlur
	case "solid":
		return MaterialSolid
	}
	return MaterialLiquid
}

// String 是存进设置的名字。
func (m Material) String() string {
	switch m {
	case MaterialBlur:
		return "blur"
	case MaterialSolid:
		return "solid"
	}
	return "liquid"
}

// 磨砂档的数值照 Bun 版 chat.css 的磨砂材质：backdrop-filter blur(24px)；底色是内容色的 72%，输入框 76%（保证打字时看得清）。
// CSS 的 px 就是 DIP，不用换算。Bun 的 saturate(1.8) 在 mygo 的 Blur 里没有对应项，不做（记在规格里）。
const (
	blurRadius      float32 = 24
	blurAlphaHeader float32 = .72
	blurAlphaInput  float32 = .76
	blurAlphaFloat  float32 = .84 // 浮层：设计语言第六节“84% 底色 + 24px 磨砂”
	edgeHeightExtra         = 20  // 滚动边缘比控制层多出的高度：Bun 的 --g + --s3（8 + 12）
)

// Level 是用玻璃的哪一层：标题栏、输入框（第 2 层控制），或菜单、下拉、对话框（第 4 层浮层）。
// 越往上字越多、越要读得清，玻璃越实（组件规格总则第三节第 5 条：浮层用常规玻璃）。
type Level int

const (
	LevelHeader Level = iota
	LevelInput
	LevelFloat
)

// SetMaterial 设面板材质。系统开了“降低透明度”时（Frame 每帧读进 Reduce）不管选了什么都是实色（设计语言第七节）。
func (k *Kit) SetMaterial(m Material) { k.Mat = m }

// EffectiveMaterial 是这一帧真正用的材质。
func (k *Kit) EffectiveMaterial() Material {
	if k.Reduce {
		return MaterialSolid
	}
	return k.Mat
}

// Glass 给浮在聊天内容上的控制层上材质：
//   - 液态玻璃：glass.Glass，玻璃上叠一层内容色（标题栏 20%、输入框 35%）、通透度 0.6；mygo 的玻璃默认是 macOS 的磨砂，
//     面板越大越磨，照 Bun 的 50% / 70% 叠上去真窗口里成了一条实心白（11-06 真窗口截图），所以浓度比 Bun 低；
//   - 磨砂：glass.Blur 背景模糊，加内容色的半透明底；
//   - 实色：solid（预设的 Surface 派生色）。
func (k *Kit) Glass(e ui.Element, level Level, solid ui.Color) {
	surface := k.T.C.Surface
	switch k.EffectiveMaterial() {
	case MaterialLiquid:
		tint, base := GlassHeader, surface
		switch level {
		case LevelInput:
			tint = GlassComposer
		case LevelFloat:
			tint, base = GlassFloat, k.T.C.Float
		}
		e.Material(glass.Glass{Tint: base.Alpha(tint), Clarity: 0.6})
	case MaterialBlur:
		a, base := blurAlphaHeader, surface
		switch level {
		case LevelInput:
			a = blurAlphaInput
		case LevelFloat:
			a, base = blurAlphaFloat, k.T.C.Float
		}
		e.Material(glass.Blur{Radius: blurRadius}).Background(base.Alpha(a))
	default:
		e.Background(solid)
	}
}

// FloatPanel 是菜单、下拉、对话框这类浮层面板的底：浮层档的玻璃（实色时是 float）、一圈细边，再加浮层那一档阴影（总则第二节第 4 层，FloatShadow）。
// 浮层下面不能再是玻璃（不叠玻璃），所以只给弹出到内容或遮罩上面的面板用。
func (k *Kit) FloatPanel(e ui.Element) ui.Element {
	k.Glass(e, LevelFloat, k.T.C.Float)
	k.FloatShadow(e)
	return e.Border(sunkenRing, k.P.Line) // 边线和凹块（下拉入口）的同色同粗
}

// FloatMenu 是 FloatPanel 加上菜单、选择器的进出场：从贴着按钮的那条边展开、关掉时缩回去（chatui.Tokens.MenuMorph，主人 2026-11-15）；
// 退场期间拷贝保留最后的内容，不收点击和焦点。用 ChatTokens：画廊慢放时弹簧也跟着放慢。
func (k *Kit) FloatMenu(c *ui.Context, e ui.Element) ui.Element {
	k.FloatPanel(e)
	// 用 FloatMenu 的面板都是控件圆角；阴影交给 MenuMorph 在长开以后淡入（浮层档，浓度乘 a）
	return k.ChatTokens().MenuMorph(c, e, k.G.RadiusControl, func(a float32) {
		e.Shadow(0, floatY, floatBlur, 0, k.shadowInk(k.pick(floatAlphaLight, floatAlphaDark)*a))
	})
}

// NoteBar 是输入框位置上的一条提示（这里只能看之类，照 Bun 的只读条）：和输入框同一层，玻璃底、圆角同心、左边一个图标。
func (k *Kit) NoteBar(c *ui.Context, key, text string) {
	k.noteBar(c, key, text, "", nil)
}

// NoteBarAction 是同 NoteBar 的一条提示，后面跟一个按钮（“去设置”这类）。常驻说明仍然只有一行。
func (k *Kit) NoteBarAction(c *ui.Context, key, text, action string, fn func()) {
	k.noteBar(c, key, text, action, fn)
}

func (k *Kit) noteBar(c *ui.Context, key, text, action string, fn func()) {
	g, p := k.G, k.P
	r := g.RadiusCard - g.SpaceSmall
	bar := ui.Row(c.Key(key)).Label(text).FillWidth().Shrink(0).Padding(g.ControlInset+g.SpaceSmall).Gap(g.SpaceSmall).AlignItems(ui.Center).Radius(r).Border(1, p.Line)
	k.Glass(bar, LevelInput, p.Composer)
	k.surface(c, bar, key, "note", r, g.ControlInset+g.SpaceSmall).Children(func() {
		ui.Icon(c, IconInfo).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Muted)
		ui.Text(c.Key(key+"-text"), text).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).Grow(1).MinWidth(0)
		if action != "" {
			k.Button(c, key+"-action", action, false, false, fn).Shrink(0)
		}
	})
}

// MessageFade 是消息列上下两头的淡出蒙版（照 Bun .su-thread）：消息在标题栏、输入区底下慢慢淡掉，
// 到聊天窗边缘之前已经看不见，不会被窗口边切出一条直线。实色档控制层不透明，用不着。
func (k *Kit) MessageFade(e ui.Element, height float32) ui.Element {
	if k.EffectiveMaterial() == MaterialSolid || height <= 96 {
		return e
	}
	black := ui.RGB(0, 0, 0)
	return e.MaskLinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Transparent, At: 6 / height}, {Color: black, At: 40 / height},
		{Color: black, At: 1 - 56/height}, {Color: ui.Transparent, At: 1 - 14/height},
	}})
}

// ScrollEdges 画控制层下面那一条滚动边缘（Apple 的 soft scroll edge，Bun 版 .su-edge）：三层由强到弱的渐进模糊，
// 模糊半径 10、5、2，各自带一条渐变蒙版，高度 = 控制层高 + 20。液态玻璃和磨砂档画，实色或降低透明度时不画（设计语言 7.1）。
// 上边那条在标题栏下面，下边那条在输入区下面；宿主在消息列之后、输入区之前调用，让输入区压在边缘上面。
func (k *Kit) ScrollEdges(c *ui.Context, l ChatLayout) {
	if k.EffectiveMaterial() == MaterialSolid {
		return
	}
	k.edge(c, "top", true, k.G.HeaderHeight+l.Inset-k.G.SpaceSmall+edgeHeightExtra)
	k.edge(c, "bottom", false, l.Bottom-l.Inset+edgeHeightExtra)
}

func (k *Kit) edge(c *ui.Context, name string, top bool, height float32) {
	black, clear := ui.RGB(0, 0, 0), ui.Transparent
	angle := float32(0) // 往上：从下边缘起算
	if top {
		angle = 180
	}
	stops := [3][]ui.GradientStop{
		{{Color: black, At: 0}, {Color: black, At: .12}, {Color: clear, At: .46}},
		{{Color: clear, At: .08}, {Color: black, At: .30}, {Color: clear, At: .74}},
		{{Color: clear, At: .28}, {Color: black, At: .60}, {Color: clear, At: 1}},
	}
	radii := [3]float32{10, 5, 2}
	for i := range radii {
		e := ui.Box(c.Key("edge-" + name + string(rune('1'+i)))).Absolute().Left(0).Right(0).Height(height)
		if top {
			e.Top(0)
		} else {
			e.Bottom(0)
		}
		e.Material(glass.Blur{Radius: radii[i], Mask: &ui.LinearGradient{Angle: angle, Stops: stops[i]}})
	}
}

// 气泡玻璃的底色浓度（背景模糊半径和磨砂档同一个 blurRadius）：层级靠浓度分，气泡最淡，标题栏 .72、输入框 .76、浮层 .84 依次更浓。
// 只有模糊加半透明底，不做边缘折射（折射只留给标题栏、输入区、菜单这些浮在最上面的少数几样）。
const (
	BubbleAlphaThin  float32 = .5  // 淡：能看出壁纸的波点和光晕
	BubbleAlphaThick float32 = .75 // 浓：接近标题栏，只剩一点透
	BubbleAlphaMin   float32 = .3
	BubbleAlphaMax   float32 = .98 // 贴近实色的一头也要能调：壁纸很淡，填充色一旦低了气泡就和背景分不开（主人 2026-11-15）
)

// BubbleGlass 给聊天宿主（chatui.Host.BubbleGlass）一个气泡材质回调：背景模糊 blurRadius 加 bg 的 alpha 浓度底。
// 退回实色（返回 nil）的情况：材质设成实色或系统开了降低透明度、聊天区没有壁纸（模糊的只是一片平色，白白花钱）、浓度到 1。
func (k *Kit) BubbleGlass(alpha float32) func(e ui.Element, bg ui.Color, radii [4]float32) {
	return k.BubbleGlassBlur(alpha, blurRadius)
}

// BubbleGlassBlur 同 BubbleGlass，模糊半径由调用方给：气泡玻璃有两个各自可调的量，
// 填充色的不透明度（和背景分得多开）和模糊的程度（背后的东西糊掉多少），主人 2026-11-15。
// mygo 里一个元素的“材质”和“底色”只画其中一样（ui/paint.go 的 background：fillMaterial 时只画材质），
// 所以不能写 Material(...).Background(...)：那样填充色根本不画，气泡就是一块纯模糊。填充色用 Draw 画在模糊上面、内容下面。
func (k *Kit) BubbleGlassBlur(alpha, radius float32) func(e ui.Element, bg ui.Color, radii [4]float32) {
	if k.EffectiveMaterial() == MaterialSolid || !k.S.Wallpaper || alpha >= 1 {
		return nil
	}
	return func(e ui.Element, bg ui.Color, radii [4]float32) {
		tint := bg.Alpha(alpha)
		e.Material(glass.Blur{Radius: radius}).Draw(func(pt *ui.Painter, r ui.Rect) { pt.FillRadii(r, radii, tint) })
	}
}
