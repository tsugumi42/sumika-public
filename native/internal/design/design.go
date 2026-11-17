// Package design 是 Sumika 界面唯一的设计令牌来源（规格-设计令牌，ADR-347）。
// 数值写在 tokens/*.tokens.json（W3C 设计令牌 2025.10 格式），由 gen 生成 tokens_gen.go；
// 这里只放运行时的三件事：按外观设置缩放尺寸、按模式换色表、按角色色算强调色阶梯，外加给 mygo 的主题桥。
// 只依赖 mygo 和标准库。
package design

//go:generate go run ./gen

import (
	"math"
	"sync"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design/colorx"
)

// 外观设置的范围和默认值（和 chatui 现行的一致）。
const (
	TextMin, TextMax, TextDefault          float32 = 12, 20, 16
	RadiusMin, RadiusMax, RadiusDefault    float32 = 4, 24, 12
	DensityMin, DensityMax, DensityDefault float32 = 6, 10, 8
)

// 底色冷暖，取值和 resolver.json 里 neutral 的 context 同名。
const (
	NeutralCool  = "cool"
	NeutralPlain = "plain"
	NeutralWarm  = "warm"
)

// Settings 是外观页的设置。
type Settings struct {
	Text, Radius, Density float32
	Dark                  bool
	Neutral               string
	Wallpaper             bool
}

// DefaultSettings 是第一次打开时的外观。
func DefaultSettings() Settings {
	return Settings{Text: TextDefault, Radius: RadiusDefault, Density: DensityDefault, Neutral: NeutralCool, Wallpaper: true}
}

func clamp(v, lo, hi float32) float32 { return max(lo, min(hi, v)) }

// Normalize 把零值和越界的设置拉回范围。
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
	if s.Neutral != NeutralPlain && s.Neutral != NeutralWarm {
		s.Neutral = NeutralCool
	}
	return s
}

func (s Settings) modeKey() string {
	a, w := "light", "on"
	if s.Dark {
		a = "dark"
	}
	if !s.Wallpaper {
		w = "off"
	}
	return a + "/" + s.Neutral + "/" + w
}

// Tokens 是一份设置推出来的全部令牌。
type Tokens struct {
	S          Settings
	Dim        Dims
	Color      Colors
	Num        Nums
	Motion     Durations
	Curve      Curves
	Breakpoint Breakpoints
}

// Breakpoints 是由栏宽派生的窗口宽度阈值（规格 4.1.1）：窄于 Narrow 时三层叠放，宽于 Wide 时右栏挤开而不是盖住。
type Breakpoints struct{ Narrow, Wide float32 }

// 尺寸公式的轴和算法，和 tokenspec 的常量一一对应。
const (
	axisFixed = iota
	axisText
	axisSpace
	axisRadius
)

const (
	opBase = iota
	opAlias
	opWrap
	opInner
	opHalf
)

type dimSpec struct {
	base     float32
	axis, op int
	a, b, c  int
}

type modeTable struct {
	colors Colors
	nums   Nums
}

func round(v float32) float32 { return float32(math.Round(float64(v))) }

// Derive 从设置推出令牌：尺寸按规格 3.3 缩放、同心、取整到整数逻辑像素；颜色和数整张换模式表。
func Derive(s Settings) Tokens {
	s = s.Normalize()
	kt := s.Text / TextDefault
	k := [4]float32{axisFixed: 1, axisText: kt, axisSpace: s.Density / DensityDefault * kt, axisRadius: s.Radius / RadiusDefault * kt}
	var v [dimCount]float32
	for i, d := range dimSpecs {
		switch d.op {
		case opBase:
			v[i] = round(d.base * k[d.axis])
		case opAlias:
			v[i] = v[d.a]
		case opWrap:
			v[i] = v[d.a] + v[d.b]
		case opInner:
			v[i] = max(min(v[d.c], v[d.a]), v[d.a]-v[d.b]) // 下限不超过外层
		case opHalf:
			v[i] = round(v[d.a] / 2)
		}
	}
	t := Tokens{S: s, Motion: durations, Curve: curves}
	t.Dim.fill(&v)
	mt := modeTables[s.modeKey()]
	t.Color, t.Num = mt.colors, mt.nums
	d := t.Dim
	t.Breakpoint.Narrow = d.LayoutNav + d.LayoutListMin + d.LayoutSplit + d.LayoutChatMin
	t.Breakpoint.Wide = t.Breakpoint.Narrow + d.LayoutSplit + d.LayoutSideMin
	return t
}

// DimsByPath 按令牌路径列出全部尺寸，比如 "space.s2"。
func (t Tokens) DimsByPath() map[string]float32 { return t.Dim.byPath() }

// ColorByPath 按令牌路径查颜色（检查器、文档、对照表用），比如 "color.text-2"。
func (t Tokens) ColorByPath(p string) (ui.Color, bool) { return t.Color.byPath(p) }

// Weight 是字重令牌换成 mygo 要的整数。
func Weight(w float64) int { return int(math.Round(float64(w))) }

// Spring 是弹簧令牌。
func (t Tokens) Spring(bouncy bool) ui.SpringOptions {
	n := t.Num
	if bouncy {
		return ui.SpringOptions{Mass: n.SpringBouncyMass, Stiffness: n.SpringBouncyStiffness, Damping: n.SpringBouncyDamping}
	}
	return ui.SpringOptions{Mass: n.SpringSmoothMass, Stiffness: n.SpringSmoothStiffness, Damping: n.SpringSmoothDamping}
}

// Accent 是一个角色色推出来的强调色阶梯（照 chatui.Accent）。
type Accent struct {
	Base, Hover, Press              ui.Color
	Soft, SoftHover                 ui.Color
	Line                            ui.Color
	Text                            ui.Color
	On                              ui.Color
	Face                            ui.Color
	Strong, OnStrong, OnStrongMuted ui.Color
	Glow                            colorx.OKLCH
}

type accentKey struct {
	mode string
	char colorx.OKLCH
}

// accentMemo 记住推过的阶梯：每帧每条消息都要问发言人的色阶，推一次要在 OKLCH 里二分找对比度。
var accentMemo struct {
	sync.Mutex
	m map[accentKey]Accent
}

// AccentOf 按 JSON 里的配方（accent.recipe.*）推出角色色阶梯。
func (t Tokens) AccentOf(char colorx.OKLCH) Accent {
	key := accentKey{t.S.modeKey(), char}
	accentMemo.Lock()
	a, ok := accentMemo.m[key]
	accentMemo.Unlock()
	if ok {
		return a
	}
	a = t.deriveAccent(char)
	accentMemo.Lock()
	if accentMemo.m == nil || len(accentMemo.m) >= 4096 {
		accentMemo.m = map[accentKey]Accent{}
	}
	accentMemo.m[key] = a
	accentMemo.Unlock()
	return a
}

func (t Tokens) deriveAccent(char colorx.OKLCH) Accent {
	r := t.Num
	at := func(l, cmax float64) colorx.OKLCH { return colorx.New(l, math.Min(char.C, cmax), char.H) }
	var a Accent
	a.Glow = char
	a.Base, a.Face = char.Color(), char.Color()
	a.Hover = colorx.New(char.L+r.AccentRecipeHoverDl, char.C, char.H).Color()
	a.Press = colorx.New(char.L+r.AccentRecipePressDl, char.C, char.H).Color()
	a.Soft = at(r.AccentRecipeSoftL, r.AccentRecipeSoftCmax).Color()
	a.SoftHover = at(r.AccentRecipeSoftHoverL, r.AccentRecipeSoftHoverCmax).Color()
	a.Line = at(r.AccentRecipeLineL, r.AccentRecipeLineCmax).Color()
	l := math.Min(char.L, r.AccentRecipeTextL)
	if r.AccentRecipeTextDir > 0 {
		l = math.Max(char.L, r.AccentRecipeTextL)
	}
	a.Text = colorx.New(l, char.C*r.AccentRecipeTextCmul, char.H).Color()
	a.On = colorx.New(r.AccentRecipeOnL, r.AccentRecipeOnC, r.AccentRecipeOnH).Color()
	a.Strong, a.OnStrong, a.OnStrongMuted = colorx.Strong(char, r.AccentRecipeStrongMin, r.AccentRecipeStrongMuted)
	return a
}

// Theme 是给 mygo 自带控件（焦点环、滚动条、读屏、没专门画过的控件）的主题；全仓只有这里构造 ui.Theme（规格 7.5）。
func Theme(t Tokens, acc Accent, font string) ui.Theme {
	th := *ui.LightTheme()
	if t.S.Dark {
		th = *ui.DarkTheme()
	}
	c := t.Color
	th.Background, th.Surface, th.SurfaceHover, th.SurfacePressed = c.Bg, c.Bg, c.Sunken, c.SunkenHover
	th.Text, th.TextMuted = c.Text, c.Text2
	th.Accent, th.AccentText, th.AccentHover, th.AccentPressed = acc.Base, acc.On, acc.Hover, acc.Press
	th.Border, th.Focus, th.Danger = c.Line, acc.Base, c.Danger
	th.Scrollbar = c.Scrollbar
	th.Radius, th.Spacing, th.FontSize, th.Font = t.Dim.RadiusCtl, t.Dim.SpaceS1, t.Dim.FontSize4, font
	return th
}
