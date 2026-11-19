package chatui

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design"
)

// Neutral 是底色的冷暖档（Bun appearance.ts:66-69）。
type Neutral int

const (
	NeutralCool Neutral = iota
	NeutralPlain
	NeutralWarm
)

// Settings 是外观页的四个滑块和两个开关；令牌全部从它推出来。
type Settings struct {
	Text      float32 // 正文字号 12～20，默认 16
	Radius    float32 // 按钮圆角 4～24，默认 12（Bun 默认 20，主人嫌原型太圆）
	Density   float32 // 间距单位 6～10，默认 8
	Neutral   Neutral
	Dark      bool
	Wallpaper bool // 聊天区波点光晕背景
	Glass     bool // 标题栏、输入框用玻璃
	Ligatures bool // 等宽字（命令、输出、diff、路径）连字；默认开（主人 2026-11-06："开也没关系""可以做成设置吗"）
}

// 设置范围；滑块和 Normalize 共用。
const (
	TextMin, TextMax, TextDefault          float32 = 12, 20, 16
	RadiusMin, RadiusMax, RadiusDefault    float32 = 4, 24, 12
	DensityMin, DensityMax, DensityDefault float32 = 6, 10, 8
)

// DefaultSettings 是工作台第一次打开时的外观。
func DefaultSettings() Settings {
	return Settings{Text: TextDefault, Radius: RadiusDefault, Density: DensityDefault, Wallpaper: true, Glass: true, Ligatures: true}
}

func clamp(v, lo, hi float32) float32 { return max(lo, min(hi, v)) }

// Normalize 把越界或零值的设置拉回范围内。
func (s Settings) Normalize() Settings {
	if s.Text == 0 {
		s.Text = TextDefault
	}
	if s.Radius == 0 {
		s.Radius = RadiusDefault
	}
	if s.Density == 0 {
		s.Density = DensityDefault
	}
	s.Text = clamp(s.Text, TextMin, TextMax)
	s.Radius = clamp(s.Radius, RadiusMin, RadiusMax)
	s.Density = clamp(s.Density, DensityMin, DensityMax)
	return s
}

// Tokens 是一份设置推出来的全部尺寸、颜色和动效。字段名照 Bun 的令牌名，方便对照。
type Tokens struct {
	S  Settings
	TS float32 // 字号比例 Text/15

	FS                                    [7]float32 // fs0～fs6
	Body                                  float32    // 正文字号 = FS[4]
	BodyLine                              float32    // 正文行高 = Body×1.6
	ControlLine                           float32    // 单行控件行高 = Body×1.25
	NameLine                              float32    // 气泡里名字的行高 = fs3×1.3，贴着正文不再另隔
	InfoLine                              float32    // 右下角时间的行高 = fs0×1.25
	ChipH, ChipEmoji, ChipInset           float32    // 表情回应按钮高、表情框边长、表情框到按钮边
	ChipAvatar, ChipAvShift, ChipAvStroke float32    // 头像直径、相邻叠多少、描边
	ChipPad                               float32    // 按钮四边内边距 = (高 − 头像) / 2
	ChipPadR, ChipGap, ChipBetween        float32
	Weight                                [4]int // 400 / 500 / 600 / 700，深色各减 40

	G                           float32 // 间距单位
	S1, S2, S3, S4, S5, S6      float32
	Inset, InsetTight, InsetCtl float32

	RCtl, R1, R2, RWin, RJoin float32
	RInnerMin                 float32

	HSm, HMd, HLg float32 // 控件高

	AvatarMsg float32

	BubblePadV, BubblePadH float32
	BubbleShare            float32 // 气泡最大宽 / 消息列宽
	GapInGroup, GapGroups  float32
	JoinWindow             time.Duration // 合组的最大间隔

	Badge, BadgeHit float32 // 失败角标直径、命中区

	C Palette
	D design.Tokens // 令牌来源；新代码直接用 D，旧字段是第 3 步留下的薄壳

	Spring           design.Springs // 弹簧预设（利落、灵敏、柔和、弹跳、拉伸一对），位移尺寸缩放只从这里挑
	Fast, Msg, Panel time.Duration
}

// Palette 是按用途起名的颜色（Bun tokens.css:57-100、290-312）。
type Palette struct {
	Surface, Bg, Rail, Sunken, SunkenHover, Line, LineStrong ui.Color
	Text, Text2, Text3                                       ui.Color
	Bubble                                                   ui.Color // 她的气泡
	ChatBg                                                   ui.Color // 聊天区底色（壁纸画在它上面）
	Hover, Press                                             ui.Color
	Danger, DangerHover, OnDanger                            ui.Color // 危险色（失败角标）和它的悬停档
	Ok, OkText                                               ui.Color // 成功色：作图标（对底 ≥ 3）、作文字（对底 ≥ 4.5），改文件的 +N、diff 加的行
	DangerText                                               ui.Color // 危险色的文字档：改文件的 −N、diff 减的行（压在同色淡底上也过 4.5，照 Bun --danger-text）
	DiffTint                                                 float32  // diff 行底叠同色的浓度（Codex 浅 15%、深 23%）
	ShadowA, ShadowB                                         ui.Color // sh-1 的两层
	Float, FloatRing                                         ui.Color
	DotInk                                                   ui.Color // 壁纸波点
}

// Accent 是一个角色色推出来的阶梯（Bun a1～a12 的用途子集）。
type Accent struct {
	Base, Hover, Press ui.Color // a9、a10、按下
	Soft, SoftHover    ui.Color // a3、a4：你的气泡、选中
	Line               ui.Color // a7：你的气泡细边
	Text               ui.Color // a11：角色色文字
	On                 ui.Color // 角色色实底上的字
	Face               ui.Color // 首字头像的底：角色色本身，深浅色模式都一样（照 Bun --c），上面放 On
	// Strong 是"整块填强调色、上面放白字"的底（会话列表选中行这类），OnStrong 白字，OnStrongMuted 90% 白的次要字。
	// 不写死明度：见 design/colorx 的 Strong（主人 11-06：以后换主题色要能按公式自动更新）。
	Strong, OnStrong, OnStrongMuted ui.Color
	Glow                            OKLCH // 壁纸光晕用的原色
}

// minChipH 是表情回应按钮的最小高度（DIP）：WCAG 2.2 目标尺寸 24×24。
const minChipH = 24.0

// DesignSettings 把外观设置换成 design 包的设置（玻璃、连字不影响令牌）。
func (s Settings) DesignSettings() design.Settings {
	n := map[Neutral]string{NeutralCool: design.NeutralCool, NeutralPlain: design.NeutralPlain, NeutralWarm: design.NeutralWarm}[s.Neutral]
	return design.Settings{Text: s.Text, Radius: s.Radius, Density: s.Density, Dark: s.Dark, Neutral: n, Wallpaper: s.Wallpaper}
}

// Derive 从设置推出令牌。令牌里有名字的量全部从 internal/design 取（规格-设计令牌 第十节第 3 步，薄壳）；
// 表情回应按钮、气泡内边距这些还没进令牌的组件量照旧在这里按字号算，第 6 步迁移时挪进组件层。
func Derive(s Settings) Tokens {
	s = s.Normalize()
	d := design.Derive(s.DesignSettings())
	m := d.Dim
	t := Tokens{S: s, TS: s.Text / 15, D: d}
	t.FS = [7]float32{m.FontSize0, m.FontSize1, m.FontSize2, m.FontSize3, m.FontSize4, m.FontSize5, m.FontSize6}
	t.Body, t.BodyLine, t.ControlLine = m.FontSize4, m.FontLineBody, m.FontLineUi
	t.NameLine = round1(t.FS[3] * 1.3)
	t.InfoLine = round1(t.FS[0] * 1.25)
	// 表情回应按钮：Telegram 高 22、图标 18、头像 20（叠 7、描边 1）、数字前隔 3、数字后 7、按钮之间隔 4，按字号比例缩放。
	// 表情框四边等距：框到按钮上、下、左的距离一样（主人 11-06："emoji没四边等距"），头像到按钮右、上、下也一样。
	k := t.TS
	t.ChipH, t.ChipEmoji = round1(22*k), round1(18*k)
	t.ChipInset = round1((t.ChipH - t.ChipEmoji) / 2)
	// 头像外圈是一圈按钮底色（描边画在头像里面，外缘和按钮底融在一起，不在按钮边上多出一圈），
	// 里面的圆和表情框一样大：到按钮上、下、右的距离 = 表情框到上、下、左的距离。
	t.ChipAvStroke = round1(1 * k)
	t.ChipAvatar, t.ChipAvShift = t.ChipEmoji+2*t.ChipAvStroke, round1(7*k)
	t.ChipPad = (t.ChipH - t.ChipAvatar) / 2 // 按钮四边内边距（几何检查要四边相等）；表情框再用外边距补到 ChipInset
	t.ChipPadR, t.ChipGap, t.ChipBetween = round1(7*k), round1(3*k), round1(4*k)
	if t.ChipH < minChipH {
		// 点击目标不小于 24 DIP（设计自查第 15 条）：字号小时药丸不跟着缩到 24 以下。各项取整数，
		// 按钮宽度向上取整后右边才不比上下宽（按比例放大会得到小数，宽度多出的零头落在头像右边）。
		t.ChipH, t.ChipEmoji, t.ChipAvStroke, t.ChipAvShift = minChipH, 20, 1, 8
		t.ChipInset = (t.ChipH - t.ChipEmoji) / 2
		t.ChipAvatar = t.ChipEmoji + 2*t.ChipAvStroke
		t.ChipPad = (t.ChipH - t.ChipAvatar) / 2
		t.ChipPadR, t.ChipGap, t.ChipBetween = 8, 3, 4
	}
	n := d.Num
	t.Weight = [4]int{design.Weight(n.FontWeightNormal), design.Weight(n.FontWeightMedium), design.Weight(n.FontWeightSemi), design.Weight(n.FontWeightBold)}
	t.G = m.SpaceS2
	t.S1, t.S2, t.S3, t.S4, t.S5, t.S6 = m.SpaceS1, m.SpaceS2, m.SpaceS3, m.SpaceS4, m.SpaceS5, m.SpaceS6
	t.Inset, t.InsetTight, t.InsetCtl = m.SpaceInset, m.SpaceInsetTight, m.SpaceInsetCtl
	t.HSm, t.HMd, t.HLg = m.SizeCtlSm, m.SizeCtlMd, m.SizeCtlLg
	t.RInnerMin = m.RadiusInnerMin
	t.RCtl, t.R1, t.R2, t.RWin, t.RJoin = m.RadiusCtl, m.Radius1, m.Radius2, m.RadiusWin, m.RadiusJoin
	t.AvatarMsg = 34
	// 竖内边距 + 行距的一半 = 字到气泡上下的距离；横内边距取同一个数，字形到四边等距（主人 11-06 审查）
	t.BubblePadV = round1(t.S2 - 0.5)
	t.BubblePadH = round1(t.BubblePadV + (t.BodyLine-t.Body)/2)
	t.BubbleShare = 0.8
	t.GapInGroup, t.GapGroups = t.S1, t.S3
	t.JoinWindow = 900 * time.Second
	t.Badge, t.BadgeHit = 18, m.SizeHitMin
	t.C = paletteOf(d)
	t.Spring = d.Springs()
	t.Fast, t.Msg, t.Panel = d.Motion.MotionFast, d.Motion.MotionMsg, d.Motion.MotionPanel
	return t
}

func paletteOf(d design.Tokens) Palette {
	c, n := d.Color, d.Num
	return Palette{Surface: c.Surface, Bg: c.Bg, Rail: c.Rail, Sunken: c.Sunken, SunkenHover: c.SunkenHover, Line: c.Line, LineStrong: c.LineStrong,
		Text: c.Text, Text2: c.Text2, Text3: c.Text3, Bubble: c.Bubble, ChatBg: c.ChatBg, Hover: c.Hover, Press: c.Press,
		Danger: c.Danger, DangerHover: c.DangerHover, OnDanger: c.OnDanger, Ok: c.Ok, OkText: c.OkText, DangerText: c.DangerText,
		DiffTint: float32(n.DiffTint), ShadowA: c.ShadowA, ShadowB: c.ShadowB, Float: c.Float, FloatRing: c.FloatRing, DotInk: c.DotInk}
}

// BodyLeading 是正文行高对字号的倍数（给按倍数排版的 markdown 用），和 BodyLine 同源。
func (t Tokens) BodyLeading() float32 { return t.BodyLine / t.Body }

func round1(v float32) float32 { return float32(math.Round(float64(v)*10) / 10) }

// MinIconContrast 是要看清的图标、图形对底色至少要有的对比度（WCAG 2 1.4.11）。
const MinIconContrast = 3.0

// MinTextContrast 是正文和要读的小图形对底色至少要有的 WCAG 2 对比度（1.4.3 AA 普通文字）。
const MinTextContrast = 4.5

// reach 让颜色对所有底色都达到 min 对比度：已经达到就原样返回，否则只沿 dir（+1 变亮、-1 变暗）调明度，取刚好过线的那一档。
func reach(o OKLCH, dir, min float64, bgs ...ui.Color) OKLCH {
	ok := func(l float64) bool {
		c := OKLCH{l, o.C, o.H, 1}.Color()
		for _, bg := range bgs {
			if Contrast(c, bg) < min {
				return false
			}
		}
		return true
	}
	if ok(o.L) {
		return o
	}
	near, far := o.L, 0.0
	if dir > 0 {
		far = 1
	}
	for range 40 {
		mid := (near + far) / 2
		if ok(mid) {
			far = mid
		} else {
			near = mid
		}
	}
	return OKLCH{far, o.C, o.H, 1}
}

// DefaultChar 是 Bun 的默认角色色 A（appearance.ts:61-64）。
var DefaultChar = oklch(0.75, 0.1, 195)

// CharColors 是本地角色的默认色（Bun A、B 两个，其余在色相上等距排开，我定的）。
var CharColors = []OKLCH{
	oklch(0.75, 0.1, 195), oklch(0.71, 0.11, 232), oklch(0.72, 0.12, 350), oklch(0.76, 0.11, 70),
	oklch(0.73, 0.12, 150), oklch(0.7, 0.12, 290), oklch(0.74, 0.11, 25), oklch(0.75, 0.1, 115),
}

// AccentOf 推出角色色阶梯（配方在 design 的 accent.recipe.*，结果由 design 缓存）。
func (t Tokens) AccentOf(char OKLCH) Accent { return t.deriveAccent(char) }

func (t Tokens) deriveAccent(char OKLCH) Accent {
	a := t.D.AccentOf(char.Design())
	return Accent{Base: a.Base, Hover: a.Hover, Press: a.Press, Soft: a.Soft, SoftHover: a.SoftHover, Line: a.Line, Text: a.Text,
		On: a.On, Face: a.Face, Strong: a.Strong, OnStrong: a.OnStrong, OnStrongMuted: a.OnStrongMuted, Glow: char}
}

// AvatarInitial 是首字头像里字号占直径的比例，照 Telegram（empty_userpic.cpp:300：size × 13 / 33）；
// 消息头像、会话行、顶栏、成员头像同一个比例（审查 D4：原来消息头像是 0.42，和工作台不一族）。
const AvatarInitial = 13.0 / 33

// ReactorInitial 是表情回应里 20 的小头像的首字比例：按 13/33 只有 8，认不出，放大到半径（我定的）。
const ReactorInitial = 0.5

// 引用竖条、正在输入三个点的尺寸（随字号缩放；基准值照 Telegram 引用竖条 3、Bun chat.css:231-237 的点 7、间隔 5、上浮 3）。
const quoteBarBase, typingDotBase, typingGapBase, typingLiftBase = 3, 7, 5, 3

// 正在输入三个点的节奏（Bun chat.css:231-237）：一个点亮起 360ms、回落到 720ms、整轮 1200ms，后一个点晚 150ms。
const (
	typingStep  = 150 * time.Millisecond
	typingPeak  = 360 * time.Millisecond
	typingFall  = 720 * time.Millisecond
	typingCycle = 1200 * time.Millisecond
)

// Tabular 是等宽数字的 OpenType 特性：时间、计数、未读数用它，数字变了不挤动同行的东西（R046；审查 D2）。
const Tabular = "tnum"

// JoinRadius 是连续气泡相贴那几个角的圆角，固定 6（Telegram、Grok 实测都是 6；ADR-285 第 3 条）。
const JoinRadius = 6

// StrongMinContrast 是 Strong 档上白字和次要字至少要有的 WCAG 2 对比度（1.4.3 AA 普通文字；Fluent 2、Windows、Apple 同用）。
const StrongMinContrast = MinTextContrast

// strongMutedAlpha 是 Strong 档上次要字的白色不透明度。
const strongMutedAlpha = 0.9

// over 是 fg 以不透明度 k 盖在 bg 上的颜色（sRGB 里混合，和绘制时一样）。
func over(fg ui.Color, k float64, bg ui.Color) ui.Color {
	m := func(x, y uint8) uint8 { return uint8(float64(x)*k + float64(y)*(1-k) + .5) }
	return ui.Color{R: m(fg.R, bg.R), G: m(fg.G, bg.G), B: m(fg.B, bg.B), A: 255}
}

// telegramNameHues 是 Telegram 群名字七色（colors.palette:292-313，按 chat_style.cpp:1501-1507 的顺序），
// 换成色相后按 a11 的明度规则出字色，保证压在气泡上读得清。
var telegramNameHues = []float64{29, 52, 300, 140, 205, 245, 355}

// NameColor 给不是本地角色的群成员按 id 取名字色（Telegram：id 取模 7）。
func (t Tokens) NameColor(id uint64) ui.Color {
	h := telegramNameHues[id%uint64(len(telegramNameHues))]
	return t.AccentOf(oklch(0.62, 0.14, h)).Text
}
