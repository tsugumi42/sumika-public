package winshell

import (
	"fmt"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// Kit 是外壳用的一整套令牌：外观设置推出 chatui 令牌，再推出几何、调色板和 mygo 主题。
// 换设置时 Set 重算；设置没变就什么也不做。
type Kit struct {
	S         chatui.Settings
	T         chatui.Tokens
	G         Geometry
	P         Palette
	Theme     ui.Theme
	Font      string
	Wall      chatui.Wallpaper // 聊天区背景图案：只给一个元素用
	Win       *WindowControls  // 自己画的窗口按钮；没有真窗口时为 nil
	menuTheme ui.Theme
	anchors   map[string]*ui.Handle    // 下拉入口：面板跟它同宽
	searches  map[string]*ui.Handle    // 带搜索下拉的搜索框
	waits     map[string]*searchWait   // 防抖搜索框
	steppers  map[string]*stepperState // 加减框的文字草稿
	popSeen   map[*bool]bool           // 对话框面板已经播过进场的（按 open 指针记）
	// PreviewWall 是设置页预览框的背景图案（另一个元素，不能和 Wall 共用）
	PreviewWall chatui.Wallpaper
	Mat         Material // 控制层（标题栏、输入框）的材质
	Reduce      bool     // 系统开了“降低透明度”：材质一律退回实色

	ready bool
	used  chatui.Settings
	// Surface 是测试挂钩：外壳里每块表面、控件登记一次，几何测试量四边等距和圆角同心用。平时为 nil。
	Surface func(c *ui.Context, e ui.Element, info SurfaceInfo)
	// SegHandles 分段控件量位置用
	SegHandles map[string]*segHandles
}

// SurfaceInfo 是 Surface 挂钩收到的一块表面。
type SurfaceInfo struct {
	ID, Kind      string
	Radius, Inset float32
}

// NewKit 按外观设置建一套令牌。
func NewKit(s chatui.Settings, font string) *Kit {
	k := &Kit{}
	k.Set(s, font)
	return k
}

// Set 换外观设置；设置没变且字体没变就什么也不做。返回是否重算了。
func (k *Kit) Set(s chatui.Settings, font string) bool {
	s = s.Normalize()
	if k.ready && s == k.used && font == k.Font {
		return false
	}
	k.S, k.used, k.Font, k.ready = s, s, font, true
	k.T = chatui.Derive(s)
	k.G = NewGeometry(k.T)
	k.P = NewPalette(k.T)
	k.Theme = NewTheme(k.T, k.P, font)
	return true
}

func (k *Kit) surface(c *ui.Context, e ui.Element, id, kind string, radius, inset float32) ui.Element {
	if k.Surface != nil {
		k.Surface(c, e, SurfaceInfo{id, kind, radius, inset})
	}
	return e
}

// Button 是原型的普通按钮：32 高、圆角 radiusControl，主按钮强调色，其余悬停浅底。
func (k *Kit) Button(c *ui.Context, key, label string, primary, disabled bool, fn func()) ui.Element {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(g.HitSmall).MinWidth(g.HitSmall).Padding(g.ControlInset).Radius(g.RadiusControl).Disabled(disabled).OnClick(fn)
	bg, ink := ui.Transparent, p.Ink
	if primary {
		bg, ink = p.Accent, p.OnAccent
		if !disabled && b.Pressed() { // 主按钮悬停、按下走角色色阶梯的深一档（设计语言第七节）
			bg = p.AccentPress
		} else if !disabled && b.Hovered() {
			bg = p.AccentHover
		}
	} else if b.Pressed() {
		bg = p.Press
	} else if b.Hovered() {
		bg = p.Hover
	}
	b.Background(b.AnimateColor("fill", bg, ControlFade)).TextColor(ink)
	k.pressScale(c, b, k.T.D.Num.ScalePress, disabled)
	if disabled {
		b.Opacity(DisabledAlpha)
	}
	b.Children(func() { ui.Text(c, label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine() })
	return k.surface(c, b, key, "button", g.RadiusControl, g.ControlInset)
}

// IconButton 是 32×32 的圆形图标钮：悬停、按下各一层底，有读屏名和悬停提示。
func (k *Kit) IconButton(c *ui.Context, key, label string, icon *ui.SVG, action func()) ui.Element {
	return k.IconButtonWith(c, key, label, icon, IconButtonOptions{}, action)
}

// IconButtonOptions 是图标钮的选项，零值就是平常的样子。
type IconButtonOptions struct {
	Disabled bool // 禁用：和文字按钮一样整体降到 opacity.disabled，不悬停、不缩放、点了没反应
}

// IconButtonWith 是带选项的图标钮。
func (k *Kit) IconButtonWith(c *ui.Context, key, label string, icon *ui.SVG, o IconButtonOptions, action func()) ui.Element {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(label).Size(g.HitSmall, g.HitSmall).Center().Radius(g.HitSmall / 2).TextColor(p.Ink).Disabled(o.Disabled).OnClick(action)
	k.Tip(c, b, label)
	bg := ui.Transparent
	if !o.Disabled && b.Pressed() { // 禁用时不叠悬停、按下的底
		bg = p.Press
	} else if !o.Disabled && b.Hovered() {
		bg = p.Hover
	}
	b.Background(b.AnimateColor("fill", bg, ControlFade)).Children(func() { ui.Icon(c, icon).Size(g.IconSize, g.IconSize) })
	k.pressScale(c, b, k.T.D.Num.ScalePressIcon, o.Disabled)
	if o.Disabled {
		b.Opacity(DisabledAlpha)
	}
	return k.surface(c, b, key, "icon-button", g.HitSmall/2, 0)
}

// pressScale 是按下缩放（ADR-349，试用）：按住时用 motion.press 的时长缩到 scale，松手用不过冲的弹簧回原大；
// 颜色反馈照旧保留，缩放是叠加的。禁用、系统要求减少动态效果时不缩。
// 会弹出菜单、下拉的触发钮不要接它：弹层按触发钮的矩形对位，缩了会对不准。
func (k *Kit) pressScale(c *ui.Context, b ui.Element, scale float64, disabled bool) {
	target, a := float32(1), ui.Animation{Spring: k.T.Smooth}
	if b.Pressed() && !disabled && !c.Preferences().ReduceMotion {
		target, a = float32(scale), ui.Animation{Duration: k.T.D.Motion.MotionPress, Ease: RouteMotion.Ease}
	}
	if s := b.AnimateTo("press-scale", target, a); s != 1 {
		b.TransformOrigin(.5, .5).Scale(s, s)
	}
}

// TextField 是输入框：外轮廓圆角、四边内距相等，基座保留 IME、焦点和编辑行为。
func (k *Kit) TextField(c *ui.Context, key string, value *string, label string) ui.Element {
	g := k.G
	e := ui.TextInputBase(c.Key(key), value).Label(label).Height(g.RowHeight).Padding(g.FieldInset).Radius(g.RadiusControl).Background(k.P.Bubble).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine)
	return k.surface(c, e, key, "input", g.RadiusControl, g.FieldInset)
}

// Lift 是“选中”这一族（会话行、导航项、设置分类）的样子：浮起的一块加一层细阴影（照 Bun --sel-lift、--sh-1）；
// 深色没有白块，亮一档。悬停和按下仍是各自的浅底。
func (k *Kit) Lift(e ui.Element) ui.Element {
	if k.S.Dark {
		return e.Background(k.P.Ink.Alpha(.11)).Shadow(0, 1, 3, 0, ui.RGB(0, 0, 0).Alpha(.3))
	}
	return e.Background(k.P.Composer).Shadow(0, 1, 3, 0, ui.RGB(0, 0, 0).Alpha(.1))
}

// FloatShadow 是浮在聊天内容上的面板那层影子（比 CardShadow 深、散得开），浮动右栏用。
func (k *Kit) FloatShadow(e ui.Element) ui.Element {
	a := float32(.16)
	if k.S.Dark {
		a = .5
	}
	return e.Shadow(0, 8, 24, 0, ui.RGB(0, 0, 0).Alpha(a))
}

// CardShadow 是浮动卡（聊天窗、右栏）那层很淡的影子，照 Bun --sh-1。
func (k *Kit) CardShadow(e ui.Element) ui.Element {
	a := float32(.08)
	if k.S.Dark {
		a = .3
	}
	return e.Shadow(0, 1, 3, 0, ui.RGB(0, 0, 0).Alpha(a))
}

// Avatar 画指定尺寸的圆头像：有位图就画位图，没有就是角色色实底加首字（首字号取 尺寸 × chatui.AvatarInitial，不小于 labelSize）。
// index 选调色板里的头像色。
func (k *Kit) Avatar(c *ui.Context, key, name string, index int, size float32, img *ui.Bitmap) {
	k.avatarShaped(c, key, name, index, size, size/2, img)
}

// SquareAvatar 是角色的圆角方头像（照 Bun：角色方、QQ 上的人圆）。圆角按尺寸比例，照 Bun .su-avatar 的 0.32 倍。
func (k *Kit) SquareAvatar(c *ui.Context, key, name string, index int, size float32, img *ui.Bitmap) {
	k.avatarShaped(c, key, name, index, size, size*.32, img)
}

func (k *Kit) avatarShaped(c *ui.Context, key, name string, index int, size, radius float32, img *ui.Bitmap) {
	g, p := k.G, k.P
	if index < 0 {
		index = -index
	}
	index %= len(p.Avatar)
	e := ui.Box(c.Key("avatar-"+key)).Label("头像："+key).Size(size, size).Shrink(0).Radius(radius).Center().Background(p.Avatar[index]).TextColor(p.AvatarInk[index])
	if img != nil {
		e.Clip().Children(func() { ui.Image(c.Key("img"), img).Size(size, size).Radius(radius) })
	} else {
		_, n := utf8.DecodeRuneInString(name)
		initial := name[:n]
		font := max(g.LabelSize, size*chatui.AvatarInitial)
		e.Children(func() {
			ui.Text(c, initial).FontSize(font).FontWeight(g.BoldWeight).FixedLineHeight(font * g.LabelLine / g.LabelSize)
		})
	}
	k.surface(c, e, "avatar-"+key, "avatar", radius, 0)
}

type segHandles struct {
	Track      ui.Handle
	Cells      [8]ui.Handle
	IndX, IndW float32 // 这一帧选中块画在轨道里的位置（测试读它量回弹）
}

// Segment 是分段控件：轨道里一块选中块，按上一帧量到的选中格位置滑过去，位移回弹、宽度平滑（ADR-285 第 6 条；
// 减少动态效果时 mygo 直接到位）。第一帧还没量到位置时选中格自己上底色。返回这一帧选中项是否变了。
func (k *Kit) Segment(c *ui.Context, key string, value *int, names []string) (changed bool) {
	g, p := k.G, k.P
	if k.SegHandles == nil {
		k.SegHandles = map[string]*segHandles{}
	}
	hs := k.SegHandles[key]
	if hs == nil {
		hs = &segHandles{}
		k.SegHandles[key] = hs
	}
	before := *value
	fill := p.Composer
	if k.S.Dark {
		fill = p.Selected
	}
	parts := ui.SegmentedBase(c.Key(key), value, len(names))
	parts.Track.Bind(&hs.Track)
	track, cell := hs.Track.Bounds(c), ui.Rect{}
	if *value >= 0 && *value < len(hs.Cells) {
		cell = hs.Cells[*value].Bounds(c)
	}
	measured := track.W > 0 && cell.W > 0
	if measured {
		x := parts.Track.Spring("ind-x", cell.X-track.X, k.T.Bouncy)
		w := parts.Track.Spring("ind-w", cell.W, k.T.Smooth)
		hs.IndX, hs.IndW = x, w
		parts.Track.Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(ui.Rect{X: r.X + x, Y: r.Y + g.SpaceTight, W: w, H: g.HitSmall}, fill, g.RadiusSegment)
		})
	}
	parts.Track.Padding(g.SpaceTight).Gap(g.SpaceTight).Background(p.Bubble).Radius(g.RadiusControl).Children(func() {
		for i, name := range names {
			b := parts.Segment(i).Label(name).Grow(1).Height(g.HitSmall).Center().Radius(g.RadiusSegment).TextColor(p.Muted)
			if i < len(hs.Cells) {
				b.Bind(&hs.Cells[i])
			}
			if i == *value {
				if !measured {
					b.Background(fill)
				}
				b.TextColor(p.Ink)
			} else if b.Pressed() {
				b.Background(p.Press)
			} else if b.Hovered() {
				b.Background(p.Hover)
			}
			b.Children(func() { ui.Text(c, name).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).Padding(0, g.ControlInset) }) // 放窄时字不贴边
			k.surface(c, b, key+fmt.Sprint(i), "segment", g.RadiusSegment, 0)
		}
	})
	k.surface(c, parts.Track, key, "segment-track", g.RadiusControl, g.SpaceTight)
	return *value != before
}

// Chips 是筛选片（chip）：一排药丸，点哪个选哪个，放不下换行；和分段控件分开——分段是“几选一、固定几格”，
// 筛选片是“选项多、会换行”的筛选（记忆页的角色、种类、状态）。药丸四边内距相等（控件内距），整颗圆角。返回选中项是否变了。
func (k *Kit) Chips(c *ui.Context, key string, labels []string, sel *int) (changed bool) {
	g, p := k.G, k.P
	ui.Row(c.Key(key)).Wrap().Gap(g.SpaceSmall).Children(func() {
		for i, name := range labels {
			selected := i == *sel
			b := ui.ButtonBase(c.Key(fmt.Sprint("chip-", i))).Label(name).Height(g.HitSmall).Padding(g.ControlInset).Radius(g.HitSmall / 2).Selected(selected).TextColor(p.Ink)
			if b.Clicked() && *sel != i { // 点击在这一帧的构建里读，选中项存在调用方的变量里，每帧换标签也不丢
				*sel, changed = i, true
			}
			// 选中和分段、会话行同一种样子：浮起的块（Lift）；没选中的是凹块，悬停、按下各深一层
			bg := p.Bubble
			switch {
			case b.Pressed():
				bg = p.Press
			case b.Hovered():
				bg = p.Hover
			}
			if selected {
				k.Lift(b)
			} else {
				b.Background(bg)
			}
			b.Transition(SelectFade)
			b.Children(func() {
				w := g.StrongWeight
				if selected {
					w = g.BoldWeight
				}
				ui.Text(c, name).FontSize(g.MetaSize).FontWeight(w).FixedLineHeight(g.MetaLine).SingleLine()
			})
			k.surface(c, b, fmt.Sprint(key, "-chip-", i), "chip", g.HitSmall/2, g.ControlInset)
		}
	})
	return changed
}
