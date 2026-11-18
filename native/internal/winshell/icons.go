package winshell

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// logo.svg 是原型 cmd/sumika-proto/logo.svg 的原样拷贝（ADR-230 的乌鸫图标）；按 SVG 自己的颜色画，不染成文字色。
//
//go:embed logo.svg
var logoSVG []byte

// Logo 是导航顶行的图标。
var Logo = ui.MustParseSVG(logoSVG)

func icon(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

// 图标全部用 Bun 版同一套 MingCute（chatui.Icon，Apache-2.0）。只有窗口的最小化、最大化、还原、关闭四个键照 Windows 标题栏
// 自己画（window_buttons.go，MingCute 没有对应的）；icon() 只给它们用。
var (
	IconHome     = chatui.Icon("home", false)
	IconInfo     = chatui.Icon("info", false)
	IconChat     = chatui.Icon("qq", false)
	IconSettings = chatui.Icon("settings", false)
	IconSide     = chatui.Icon("sidebar", false)
	IconClose    = chatui.Icon("close", false)
	IconSearch   = chatui.Icon("search", false)
	IconSpark    = chatui.Icon("sparkles", false)
	IconEye      = chatui.Icon("eye", false)
	IconPlus     = chatui.Icon("add", false)
	IconBack     = chatui.Icon("left", false)
	IconRight    = chatui.Icon("right", false)
	IconGroup    = chatui.Icon("group", false)
	IconMemory   = chatui.Icon("brain", false)
	IconRuntime  = chatui.Icon("dashboard", false)
)
