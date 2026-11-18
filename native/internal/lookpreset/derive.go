package lookpreset

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/chatui"
)

// 预设要把 MewTypeUI 的每一个颜色键都写上，不留一个落回 Telegram 的值（主人 2026-11-08："不能在某些还没有实现的地方套用之前的旧主题"）。
// colors() 里手写的是"原型"值（原型有现成的令牌）；其余键由 derive 按设计语言第三节的层级推出来，记为"推导"：
// 底色按 内容层（Surface）、窗口底（Bg）、导航（Rail）、凹块（Sunken、SunkenHover）取档，悬停 / 按下往凹一档，选中用强调色浅档，
// 文字取 Text / Text2 / Text3 三级，细线取 Line，危险、成功取原型的 Danger、Ok，强调色上的字取 acc.On。

const (
	FromProto   = "原型"
	FromDerived = "推导"
)

// AllColorKeys 是 MewTypeUI 的全部颜色键。
func AllColorKeys() []string {
	d := theme.Light().Definition()
	keys := make([]string, 0, len(d.Colors))
	for k := range d.Colors {
		keys = append(keys, k)
	}
	return keys
}

// Provenance 说每个颜色键的值来自哪里（原型 / 推导）。
func Provenance(dark bool) map[string]string {
	explicit := explicitColors(dark)
	out := map[string]string{}
	for _, k := range AllColorKeys() {
		if _, ok := explicit[k]; ok {
			out[k] = FromProto
		} else {
			out[k] = FromDerived
		}
	}
	return out
}

func colors(dark bool) map[string]string {
	out := explicitColors(dark)
	t := Tokens(dark)
	for _, k := range AllColorKeys() {
		if _, ok := out[k]; !ok {
			out[k] = hex(derive(k, t))
		}
	}
	return out
}

func has(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// derive 按键名里的词给一个颜色键定值；顺序就是优先级。
func derive(key string, t chatui.Tokens) ui.Color {
	c, acc := t.C, t.AccentOf(chatui.DefaultChar)
	k := strings.ToLower(key)
	hover, pressed := has(k, "hover"), has(k, "pressed")
	active, selected := has(k, "active"), has(k, "selected")
	black := ui.Color{A: 115}
	if t.S.Dark {
		black = ui.Color{A: 140}
	}
	clear := ui.Color{}
	if m := peerKey.FindStringSubmatch(k); m != nil {
		// 头像和发言人名字的 8 种颜色：原型的角色色（CharColors）
		i := int(m[1][0]-'1') % len(chatui.CharColors)
		pa := t.AccentOf(chatui.CharColors[i])
		switch {
		case has(k, "userpicbackground"):
			return pa.Base
		case has(k, "selected"):
			return pa.OnStrong
		}
		return pa.Text
	}
	switch {
	// 窗口标题栏：和窗口底同一档，按钮平时透明、悬停压一层、关闭键悬停变危险色
	case k == "titlebar.background" || k == "titlebar.backgroundactive":
		return c.Bg
	case k == "titlebar.shadow":
		return clear
	case strings.HasPrefix(k, "titlebar.buttonclose"):
		switch {
		case has(k, "foreground") && hover:
			return c.OnDanger
		case has(k, "foreground"):
			return c.Text2
		case hover:
			return c.Danger
		}
		return clear
	case strings.HasPrefix(k, "titlebar.button"):
		switch {
		case has(k, "foreground") && (hover || active):
			return c.Text
		case has(k, "foreground"):
			return c.Text2
		case hover:
			return c.Hover
		}
		return clear
	case strings.HasPrefix(k, "titlebar.foreground"):
		if active {
			return c.Text
		}
		return c.Text2
	// 浮层（菜单、对话框、提示）：原型的 Float 档；提示条和轻提示反色
	case k == "menu.background" || k == "dialog.background" || k == "tooltip.background":
		return c.Float
	case k == "tooltip.borderforeground":
		return c.FloatRing
	case k == "toast.background":
		return c.Text
	case k == "toast.foreground":
		return c.Surface
	case has(k, "scrollbar") && has(k, "track"):
		if hover {
			return c.Hover
		}
		return clear
	case has(k, "shadow"):
		return c.ShadowA
	case has(k, "spellunderline"):
		return c.Danger
	case has(k, "border", "separator", "divider", "outline", "stroke", ".bar", "track"):
		return c.Line
	case has(k, "overlay", "fade", "scrim", "cover", "dim"):
		return black
	case has(k, "thumb"):
		if hover {
			return c.Text3
		}
		return c.LineStrong
	case has(k, "danger", "error", "missed", "outdated", "scam", "draft"):
		return c.DangerText
	case has(k, "good", "success", "online", "sent"):
		if has(k, "foreground", "text", "icon") {
			return c.OkText
		}
		return c.Ok
	case has(k, "link"):
		return acc.Text
	case has(k, "foreground", "text", "icon", "name", "title", "count", "number", "label", "arrow", "close", "cancel", "placeholder"):
		switch {
		case has(k, "primary", "badge", "unread", "radial", "countercounter", "tray", "play", "verified", "send"):
			if has(k, "icon") && !has(k, "send") && !has(k, "badge", "unread", "radial", "tray", "play", "verified") {
				return c.Text2
			}
			return acc.On
		case has(k, "muted", "timestamp", "placeholder", "sub", "secondary", "status", "preview", "about", "additional", "hint", "service", "disabled", "inactive", "waveform"):
			return c.Text3
		case has(k, "icon", "arrow", "close", "cancel"):
			if hover || active || selected {
				return c.Text
			}
			return c.Text2
		}
		return c.Text
	case has(k, "primary", "badge", "unread", "radial", "tray", "play", "verified", "dot", "drop", "point", "handle", "progress", "accent", "check"):
		if has(k, "muted") {
			return c.LineStrong
		}
		return acc.Base
	}
	// 其余当底色。
	switch {
	case has(k, "muted"):
		return c.Line
	case pressed:
		return c.SunkenHover
	case hover:
		return c.Sunken
	case active, selected:
		return acc.Soft
	case has(k, "window", "mainmenu", "screen", "image"):
		return c.Bg
	case has(k, "sidebar", "rail"):
		return c.Rail
	case has(k, "search", "bar", "reply", "pinned", "button", "cloud", "tolatest", "sample", "keyboard", "kb"):
		return c.Sunken
	}
	return c.Surface
}

var peerKey = regexp.MustCompile(`^avatar\.peer(\d)`)
