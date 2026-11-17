// Package lookpreset 是 Sumika 自己的内置外观预设：原型（cmd/sumika-proto）那套配色。
// 做成 MewTypeUI 的 Definition 覆盖表（base 是 Telegram 的 desktop-light / desktop-dark，上面盖原型的值），
// 不改 MewTypeUI。用户没改过外观（只有 base）时按它编译；改过的字段照旧生效。
package lookpreset

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/chatui"
)

func hex(c ui.Color) string {
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// Tokens 是预设对应的原型令牌：默认外观设置、按深浅推出。
func Tokens(dark bool) chatui.Tokens {
	s := chatui.DefaultSettings()
	s.Dark = dark
	return chatui.Derive(s)
}

// explicitColors 列出原型有现成令牌的颜色角色；一个令牌可以对应多个主题键。其余键见 derive.go。
func explicitColors(dark bool) map[string]string {
	t := Tokens(dark)
	c, acc := t.C, t.AccentOf(chatui.DefaultChar)
	out := map[string]string{}
	set := func(v ui.Color, keys ...string) {
		for _, k := range keys {
			out[k] = hex(v)
		}
	}
	// 窗口、文字、控件
	set(c.Bg, "window.background")
	set(c.Hover, "window.backgroundHover")
	set(c.Press, "window.backgroundPressed")
	set(c.Text, "window.text")
	set(c.Text2, "window.mutedText")
	set(c.Surface, "control.surfaceBackground")
	set(c.Sunken, "control.surfaceBackgroundHover")
	set(c.SunkenHover, "control.surfaceBackgroundPressed")
	set(c.Line, "control.border")
	set(acc.Base, "control.primaryBackground", "control.focus", "selection.background")
	set(acc.Hover, "control.primaryBackgroundHover")
	set(acc.Press, "control.primaryBackgroundPressed")
	set(acc.On, "control.primaryForeground")
	set(c.Danger, "semantic.danger")
	// 导航（原型的 Rail 档）
	set(c.Rail, "sidebar.background")
	set(c.SunkenHover, "sidebar.backgroundActive")
	set(c.Press, "sidebar.backgroundPressed")
	set(c.Text2, "sidebar.textForeground", "sidebar.iconForeground")
	set(c.Text, "sidebar.textForegroundActive", "sidebar.iconForegroundActive")
	// 会话列表：选中行是强调色深档 + 白字
	set(c.Bg, "conversation.background")
	set(c.Hover, "conversation.backgroundHover")
	set(c.Press, "conversation.pressedBackground")
	set(acc.Strong, "conversation.backgroundActive", "conversation.pressedBackgroundActive", "conversation.unreadBackground", "conversation.unreadBackgroundHover", "conversation.unreadForegroundActive")
	set(c.Text, "conversation.nameForeground", "conversation.nameForegroundHover")
	set(acc.OnStrong, "conversation.nameForegroundActive", "conversation.unreadForeground", "conversation.unreadForegroundHover", "conversation.unreadBackgroundActive")
	set(c.Text3, "conversation.textForeground", "conversation.textForegroundHover", "conversation.timestampForeground", "conversation.timestampForegroundHover")
	set(acc.OnStrongMuted, "conversation.textForegroundActive", "conversation.timestampForegroundActive")
	// 顶栏、输入区
	set(c.Bg, "header.background")
	set(c.Surface, "compose.background")
	set(c.Text, "compose.text")
	set(c.Text3, "compose.placeholder", "compose.placeholderFocused")
	// 气泡：入站是第 1 层、我的是强调色浅档，文字和时间照原型
	set(c.Bubble, "message.in.background")
	set(c.Text, "message.in.text", "message.out.text")
	set(c.Text3, "message.in.timestampForeground")
	set(acc.Text, "message.in.link", "message.out.link", "message.out.timestampForeground")
	set(acc.Soft, "message.out.background")
	set(c.ShadowA, "message.in.shadow", "message.out.shadow")
	return out
}

// metrics 是圆角（原型默认字号下：气泡大圆角、相贴小角、控件圆角）。
func metrics(dark bool) map[string]float32 {
	t := Tokens(dark)
	return map[string]float32{"message.radiusLarge": t.R1, "message.radiusSmall": t.RJoin, "control.radiusLarge": t.RCtl}
}

// Definition 是预设的主题定义：Telegram 的 base 上盖原型的值。
func Definition(dark bool) theme.Definition {
	base := theme.LightBase
	if dark {
		base = theme.DarkBase
	}
	return theme.Definition{Version: theme.Version, Base: base, Colors: colors(dark), Metrics: metrics(dark)}
}

// Bare 说的是"用户什么都没设"：只有 base，没有颜色、尺寸、材质的覆盖项。
// 字体、字号、动效不算，改字体不该把配色打回 Telegram。
func Bare(d theme.Definition) bool {
	return d.Base != "" && len(d.Colors) == 0 && len(d.Metrics) == 0 && len(d.Materials) == 0
}

// Resolve 把没自定义的主题换成预设；字体、字号、动效这些没被预设占用的字段原样带上。有覆盖项的主题原样返回。
func Resolve(d theme.Definition) theme.Definition {
	if !Bare(d) {
		return d
	}
	p := Definition(d.Base == theme.DarkBase)
	p.Dark, p.Font, p.Typography, p.Motion = d.Dark, d.Font, d.Typography, d.Motion
	return p
}

// Compile 编译一份外观里的主题；没自定义的先换成预设。
func Compile(d theme.Definition) (*theme.Theme, error) { return theme.Compile(Resolve(d)) }

var light = sync.OnceValue(func() *theme.Theme {
	return must(Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase}))
})

var night = sync.OnceValue(func() *theme.Theme {
	return must(Compile(theme.Definition{Version: theme.Version, Base: theme.DarkBase}))
})

func must(t *theme.Theme, err error) *theme.Theme {
	if err != nil {
		panic(err)
	}
	return t
}

// Light、Night 是默认外观编出来的预设主题，替代 theme.Light()、theme.Dark() 当出厂主题。
func Light() *theme.Theme { return light() }
func Night() *theme.Theme { return night() }

// Telegram 是 Telegram 配色的完整定义（外观页的"配色预设"选它）。完整导出后不再是 Bare，所以不会被换回原型。
func Telegram(dark bool) theme.Definition {
	if dark {
		return theme.Dark().Definition()
	}
	return theme.Light().Definition()
}

// Pristine 说的是这份已编译的主题仍带着预设的全部颜色和圆角（用户没改过任何一项），
// 新界面据此直接走原型的画法（壁纸、细边、令牌原样），而不是用主题字段近似。
func Pristine(t *theme.Theme) bool {
	if t == nil {
		return false
	}
	def, p := t.Definition(), Definition(t.Dark())
	for k, v := range p.Colors {
		if def.Colors[k] != v {
			return false
		}
	}
	for k, v := range p.Metrics {
		if got, ok := def.Metrics[k]; !ok || !reflect.DeepEqual(got, v) {
			return false
		}
	}
	return true
}

// 外观页"配色预设"下拉的选项。"自定义"只用来显示改过的状态，选它不做事。
const (
	NameProto    = "原型"
	NameTelegram = "Telegram"
	NameCustom   = "自定义"
)

var Names = []string{NameProto, NameTelegram, NameCustom}

// Name 说这份主题现在是哪个预设。
func Name(t *theme.Theme) string {
	switch {
	case Pristine(t):
		return NameProto
	case sameColors(t.Definition(), Telegram(t.Dark())):
		return NameTelegram
	}
	return NameCustom
}

func sameColors(a, b theme.Definition) bool {
	return reflect.DeepEqual(a.Colors, b.Colors) && reflect.DeepEqual(a.Metrics, b.Metrics)
}

// Pair 给出选了某个预设后亮色、夜间两份定义：颜色和尺寸换成预设的，字体、字号、动效沿用 light、night 里已有的。
// 选"自定义"或不认识的名字返回 ok=false。
func Pair(name string, light, night theme.Definition) (l, n theme.Definition, ok bool) {
	var build func(dark bool) theme.Definition
	switch name {
	case NameProto:
		build = func(dark bool) theme.Definition {
			base := theme.LightBase
			if dark {
				base = theme.DarkBase
			}
			return theme.Definition{Version: theme.Version, Base: base}
		}
	case NameTelegram:
		build = Telegram
	default:
		return light, night, false
	}
	keep := func(old, d theme.Definition) theme.Definition {
		if old.Font != nil {
			d.Font = old.Font
		}
		if old.Typography != nil {
			d.Typography = old.Typography
		}
		if old.Motion != nil {
			d.Motion = old.Motion
		}
		return d
	}
	return keep(light, build(false)), keep(night, build(true)), true
}
