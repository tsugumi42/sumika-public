package winshell

import (
	"fmt"
	"time"
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
	anchors   map[string]*ui.Handle        // 下拉入口：面板跟它同宽
	searches  map[string]*ui.Handle        // 带搜索下拉的搜索框
	ddUp      map[string]bool              // 下拉朝上长（下面放不下），收起时定，长的过程中不变
	waits     map[string]*searchWait       // 防抖搜索框
	steppers  map[string]*stepperState     // 加减框的文字草稿
	numbers   map[string]*numberState      // 逐位滚动的数字：上次的值和方向（SettingNumber）
	morphs    map[string]*chatui.IconMorph // 图标变形的状态（k.IconMorph）
	dialogs   map[*bool]*dialogMorph       // 对话框的展开收起（按 open 指针记）：开着的、和关了还在缩回去的
	discs     map[string]*discState        // 能展开收起的内容块（Disclose，按 key 记）
	// lastPress、lastPressAt 是最近按下的那颗 Kit 按钮的框（窗口坐标）和时刻：对话框打开时从它长出来（dialogMorph）
	lastPress   ui.Rect
	lastPressAt time.Time
	// PreviewWall 是设置页预览框的背景图案（另一个元素，不能和 Wall 共用）
	PreviewWall chatui.Wallpaper
	Mat         Material // 控制层（标题栏、输入框）的材质
	Reduce      bool     // 系统开了“降低透明度”：材质一律退回实色

	ready   bool
	used    chatui.Settings
	touched bool // 最近一次按下是触屏：悬停放大只认鼠标（pressScale）
	// Surface 是测试挂钩：外壳里每块表面、控件登记一次，几何测试量四边等距和圆角同心用。平时为 nil。
	Surface func(c *ui.Context, e ui.Element, info SurfaceInfo)
	// SegHandles 分段控件量位置用
	SegHandles map[string]*segHandles
	slideSt    map[string]*SlideState // 按键记的选中块（分类行、筛选片、导航、会话行）
	stag       *staggerRun            // SettingsPane 摆内容时挂着：里面的 SettingGroup 按序号错开淡入
	paneSeen   map[string]string      // SettingsPane 上一帧选的分类（换了才错开淡入）
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

// Button 是普通按钮：小一档高（HitSmall）的胶囊（单行，圆角 = 高度的一半），主按钮强调色，其余是“平的”（Flat）。
// 工具栏、卡片里、单独一行的按钮用它；和输入框、步进器、下拉入口放在同一行的用 FieldButton（设计自查第 31 条“同一行只用同一档高”）。
func (k *Kit) Button(c *ui.Context, key, label string, primary, disabled bool, fn func()) ui.Element {
	return k.button(c, key, label, primary, disabled, fn, k.G.HitSmall, k.G.ControlInset)
}

// FieldButton 是和输入类控件同一档高（RowHeight）的按钮：放在输入框、步进器、下拉入口旁边，一行里上下对齐（主人 2026-11-16 定两档高、同一行同一档）。
// 长相和 Button 完全一样，只是高和内距跟输入框走。
func (k *Kit) FieldButton(c *ui.Context, key, label string, primary, disabled bool, fn func()) ui.Element {
	return k.button(c, key, label, primary, disabled, fn, k.G.RowHeight, k.G.FieldInset)
}

func (k *Kit) button(c *ui.Context, key, label string, primary, disabled bool, fn func(), h, inset float32) ui.Element {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(h).MinWidth(h).Radius(g.Pill(h)).Disabled(disabled).OnClick(fn)
	k.pillPadding(b, inset)
	bg, ink := ui.Transparent, p.Ink
	if primary {
		bg, ink = p.Accent, p.OnAccent
		if !disabled && b.Pressed() { // 主按钮悬停、按下走角色色阶梯的深一档（设计语言第七节）
			bg = p.AccentPress
		} else if !disabled && b.Hovered() {
			bg = p.AccentHover
		}
		b.Background(b.AnimateColor("fill", bg, ControlFade))
	} else {
		// 次要按钮是“平的”（Flat）：平时一层浅底，悬停深一层，按下再深一层，不然在面上看不出是按钮
		bg = p.Hover
		if !disabled && b.Pressed() {
			bg = p.Press.Mix(p.Ink, .5)
		} else if !disabled && b.Hovered() {
			bg = p.Press
		}
		b.Background(b.AnimateColor("fill", bg, ControlFade))
	}
	b.TextColor(ink)
	k.pressScale(c, b, k.T.D.Num.ScalePress, disabled)
	if disabled {
		b.Opacity(DisabledAlpha)
	}
	b.Children(func() { ui.Text(c, label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine() })
	return k.surface(c, b, key, "button", g.Pill(h), inset)
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
	return k.iconButton(c, key, label, o, action, func() { ui.Icon(c, icon).Size(k.G.IconSize, k.G.IconSize) })
}

// IconMorphButton 是图标会变形的图标钮：和 IconButtonWith 一样的钮，icon（chatui.Icon 的名字）换了，图标从旧的轮廓流到新的（k.IconMorph）。
// 发送 ↔ 停止、复制 ↔ 勾这类“点一下就换成另一个”的钮用它。label 是读屏名和悬停提示，由调用方随状态换。
func (k *Kit) IconMorphButton(c *ui.Context, key, label, icon string, o IconButtonOptions, action func()) ui.Element {
	return k.iconButton(c, key, label, o, action, func() { k.IconMorph(c, key+"-glyph", icon, k.G.IconSize, k.P.Ink) })
}

func (k *Kit) iconButton(c *ui.Context, key, label string, o IconButtonOptions, action func(), glyph func()) ui.Element {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(label).Size(g.HitSmall, g.HitSmall).Center().Radius(g.HitSmall / 2).TextColor(p.Ink).Disabled(o.Disabled).OnClick(action)
	k.Tip(c, b, label)
	k.Flat(b, ui.Transparent, !o.Disabled && b.Pressed(), !o.Disabled && b.Hovered()) // 禁用时不叠悬停、按下的底
	b.Children(glyph)
	k.pressScale(c, b, k.T.D.Num.ScalePressIcon, o.Disabled)
	if o.Disabled {
		b.Opacity(DisabledAlpha)
	}
	return k.surface(c, b, key, "icon-button", g.HitSmall/2, 0)
}

// pressScale 是按钮的弹簧反馈（ADR-349，照 Fluid 的 SpringButton）：
//   - 按下：用“利落”缩到 scale；松手回 1 用“弹跳”；
//   - 鼠标悬停：用“弹跳”放大到 pressHover。只认鼠标：最近一次按下是触屏的，悬停不放大（mygo 只在按下时告诉我们指针类型）；
//   - 缩放通道正在动的时候，再按它的速度横竖反向挤一点（按下时横向略宽、纵向略扁，回弹时反过来）。
//
// 颜色反馈照旧保留，缩放是叠加的。禁用、系统要求减少动态效果时不缩也不挤。
// 会弹出菜单、下拉的触发钮不要接它：弹层按触发钮的矩形对位，缩了会对不准。
func (k *Kit) pressScale(c *ui.Context, b ui.Element, scale float64, disabled bool) {
	target, a := float32(1), ui.Animation{Spring: k.springy()}
	if !disabled && (b.Pressed() || b.Clicked()) { // 记下按着、点到的这颗：它要是打开对话框，对话框从它长出来（按下松开在同一帧里、按回车时只有 Clicked）
		k.lastPress, k.lastPressAt = b.Bounds(), c.Now()
	}
	if !disabled && !c.Preferences().ReduceMotion {
		if ptr, _, ok := b.PressedPointer(); ok {
			k.touched = ptr == ui.PointerTouch
		}
		switch {
		case b.Pressed():
			target, a = float32(scale), ui.Animation{Spring: k.firm()}
		case b.Hovered() && !k.touched:
			target = pressHover
		}
	}
	s := b.AnimateTo("press-scale", target, a)
	stretch := pressStretch(b.Velocity("press-scale") * float32(motionScale)) // 慢放时速度被拉慢了，乘回去：挤多少不变
	if s != 1 || stretch != 0 {
		b.TransformOrigin(.5, .5).Scale(s-stretch, s+stretch)
	}
}

// 按钮弹簧反馈的两个数（Fluid SpringButton）：悬停放大到 1.025；按缩放通道的速度（每秒）挤一点：速度 × 0.03，封顶 ±0.05。
const (
	pressHover       float32 = 1.025
	pressStretchGain float32 = .03
	pressStretchMax  float32 = .05
)

// pressStretch 是缩放速度 v（每秒）换成的挤压量：变大（v>0）时横向收一点、纵向拉一点，变小时反过来。
func pressStretch(v float32) float32 {
	return max(-pressStretchMax, min(pressStretchMax, v*pressStretchGain))
}

// TextField 是输入框：单行的胶囊，“凹下去”（Sunken），基座保留 IME、焦点和编辑行为。
func (k *Kit) TextField(c *ui.Context, key string, value *string, label string) ui.Element {
	g := k.G
	e := ui.TextInputBase(c.Key(key), value).Label(label).Height(g.RowHeight).Radius(g.Pill(g.RowHeight)).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine)
	k.pillPadding(e, g.FieldInset)
	k.SunkenField(e.FocusRing(false), e.Focused())
	return k.surface(c, e, key, "input", g.Pill(g.RowHeight), g.FieldInset)
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
	Slide      SlideState
	IndX, IndW float32 // 这一帧选中块画在轨道里的位置（测试读它量回弹）
}

// Segment 是分段控件：轨道里一块选中块，用 SlideIndicator 滑到选中格（ADR-285 第 6 条：位移回弹、宽度平滑；
// 减少动态效果时直接到位）。第一帧还没量到位置时选中格自己上底色；选中格的字色 140 毫秒淡成正文色。返回这一帧选中项是否变了。
func (k *Kit) Segment(c *ui.Context, key string, value *int, names []string) (changed bool) {
	return k.SegmentWith(c, key, value, names, SegmentOptions{})
}

// SegmentOptions 是分段控件的选项，零值就是平常的样子（各格等宽、铺满）。
type SegmentOptions struct {
	Fit bool // 各格按字数取宽、不铺满：三四个字的选项放进等宽格会折行时用
}

// SegmentWith 是带选项的分段控件。
func (k *Kit) SegmentWith(c *ui.Context, key string, value *int, names []string, o SegmentOptions) (changed bool) {
	g, p := k.G, k.P
	if k.SegHandles == nil {
		k.SegHandles = map[string]*segHandles{}
	}
	hs := k.SegHandles[key]
	if hs == nil {
		hs = &segHandles{}
		hs.Slide.UseHost(&hs.Track)
		for i := range hs.Cells {
			hs.Slide.UseItem(slideID(i), &hs.Cells[i])
		}
		k.SegHandles[key] = hs
	}
	before := *value
	trackH := g.HitSmall + 2*g.SpaceTight
	trackR := g.Pill(trackH)               // 单行：轨道是胶囊
	cellR := g.Inner(trackR, g.SpaceTight) // 选中块和各格与轨道同心：轨道圆角 − 内距
	parts := ui.SegmentedBase(c.Key(key), value, len(names))
	own := k.SlideIndicator(c, parts.Track, &hs.Slide, slideID(*value), SlideOptions{Radius: cellR, ClipRadius: trackR})
	hs.IndX, hs.IndW = hs.Slide.Rect.X, hs.Slide.Rect.W
	parts.Track.Padding(g.SpaceTight).Gap(g.SpaceTight).Background(p.Track).Radius(trackR).Children(func() { // 轨道是“平的”：只有底色
		for i, name := range names {
			b := parts.Segment(i).Label(name).Height(g.HitSmall).Center().Radius(cellR)
			if !o.Fit {
				b.Grow(1)
			}
			if i < len(hs.Cells) {
				b.Bind(&hs.Cells[i])
			}
			ink := p.Muted
			if i == *value {
				ink = p.Ink
				if own { // 第一帧还没量到位置：自己先上和选中块同色同圆角的底；量到以后一律透明，选中只有滑动的那一块
					k.selectBg(b, selMode)
				} else {
					b.Background(ui.Transparent) // 不写的话按钮基座会按“选中”自己再上一层主题色，和滑动的块叠成两层
				}
			} else if b.Pressed() {
				b.Background(p.Press)
			} else if b.Hovered() {
				b.Background(p.Hover)
			} else {
				b.Background(ui.Transparent) // 每种状态都显式写底：留空的话按钮基座会按“刚取消选中”自己淡出一层
			}
			b.TextColor(b.AnimateColor("ink", ink, ControlFade)) // 选中格的字色淡变成正文色，不是一帧换
			b.Children(func() {
				ui.Text(c, name).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).Padding(0, g.ControlInset).SingleLine()
			}) // 放窄时字不贴边
			k.surface(c, b, key+fmt.Sprint(i), "segment", cellR, 0)
		}
	})
	k.surface(c, parts.Track, key, "segment-track", trackR, g.SpaceTight)
	return *value != before
}

// Chips 是筛选片（chip）：一排药丸，点哪个选哪个，放不下换行；和分段控件分开——分段是“几选一、固定几格”，
// 筛选片是“选项多、会换行”的筛选（记忆页的角色、种类、状态）。药丸四边内距相等（控件内距），整颗圆角。返回选中项是否变了。
// 选中的底色是一块独立的，换选项时从旧的那片滑到新的那片（SlideIndicator）。
func (k *Kit) Chips(c *ui.Context, key string, labels []string, sel *int) (changed bool) {
	k.chipRow(c, key, labels, true, func(i int) bool { return i == *sel }, func(i int) {
		if *sel != i {
			*sel, changed = i, true
		}
	})
	return changed
}

// MultiChips 是多选的筛选片：同样的药丸，点一下切换这一片开或关，可以全不选。on 和 labels 一一对应。返回是否有变化。
// 多选没有“一块滑过去”：每一片自己淡入淡出底色。
func (k *Kit) MultiChips(c *ui.Context, key string, labels []string, on []bool) (changed bool) {
	k.chipRow(c, key, labels, false, func(i int) bool { return i < len(on) && on[i] }, func(i int) {
		if i < len(on) {
			on[i], changed = !on[i], true
		}
	})
	return changed
}

// chipRow 是 Chips 和 MultiChips 共用的一排药丸：isOn 说哪片是选中的，toggle 在点击那一帧被调用；slide 为真时选中块独立、会滑。
func (k *Kit) chipRow(c *ui.Context, key string, labels []string, slide bool, isOn func(i int) bool, toggle func(i int)) {
	g, p := k.G, k.P
	row := ui.Row(c.Key(key)).Wrap().Gap(g.SpaceSmall)
	own := true // 每片自己画底：多选一直是；单选在第一帧量到位置之前
	var st *SlideState
	var fills []ui.Color
	if slide {
		st = k.slides("chips/" + key)
		for i := range labels {
			st.Item(slideID(i))
		}
		sel := ""
		for i := range labels {
			if isOn(i) {
				sel = slideID(i)
			}
		}
		fills = make([]ui.Color, len(labels))
		// 选中块和各片的底色都由这一排来画：先画各片的底（悬停、按下深一层，150 毫秒内淡变），再画滑动的选中块盖在上面。
		own = k.SlideIndicator(c, row, st, sel, SlideOptions{
			Radius: g.HitSmall / 2, ClipRadius: 0,
			Paint: func(pt *ui.Painter, r ui.Rect) { k.selectPaint(pt, r, r.H/2, selTag) },
			Under: func(pt *ui.Painter, hr ui.Rect) {
				for i := range fills {
					if b := st.Item(slideID(i)).Bounds(c); b.W > 0 && fills[i].A > 0 {
						pt.Fill(ui.Rect{X: b.X, Y: b.Y, W: b.W, H: b.H}, fills[i], b.H/2)
					}
				}
			},
		})
	}
	row.Children(func() {
		for i, name := range labels {
			selected := isOn(i)
			b := ui.ButtonBase(c.Key(fmt.Sprint("chip-", i))).Label(name).Height(g.HitSmall).Padding(g.ControlInset).Radius(g.HitSmall / 2).Selected(selected).TextColor(p.Ink)
			if slide {
				b.Bind(st.Item(slideID(i)))
			}
			if b.Clicked() { // 点击在这一帧的构建里读，选中项存在调用方的变量里，每帧换标签也不丢
				toggle(i)
			}
			// 没选中的是“平的”药丸（凹块色的底、没有线），悬停、按下各深一层；
			// 选中的是强调色浅底，和分段、会话行的“浮起白块”分开：筛选片是标签，不是模式切换（主人 2026-11-15）。
			// 下拉里当前选中的项也用这一种（Palette.Self）。
			rest := k.flatFill(p.Track, b.Pressed(), b.Hovered())
			if slide {
				target := rest
				if selected {
					target = p.Track.Alpha(0) // 选中的这片底色淡到透明，上面是滑过来的那一块
				}
				// 悬停、按下的淡变用各片自己的通道；刚取消选中的那片换新通道，直接是没悬停的灰底，不从透明淡回来
				fills[i] = row.AnimateColor(fmt.Sprint(st.FillKey(slideID(i), selected), "-", i), target, ControlFade)
			}
			if own {
				if selected {
					k.selectBg(b, selTag)
				} else {
					b.Background(rest) // 底色的淡变由下面的 Transition 管
				}
				if !slide {
					b.Transition(SelectFade)
				}
			} else {
				b.Background(ui.Transparent) // 底由这一排画；不写的话按钮基座会按“选中”自己上一层主题色
			}
			b.Children(func() {
				t := ui.Text(c, name).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).Padding(0, g.ControlInset).SingleLine() // 字号同分段；左右多留一截，单字的片不会缩成圆球
				if selected {
					t.FontWeight(g.StrongWeight)
				}
			})
			k.surface(c, b, fmt.Sprint(key, "-chip-", i), "chip", g.HitSmall/2, g.ControlInset)
		}
	})
}
