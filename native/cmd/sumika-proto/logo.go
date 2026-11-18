package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

// logo.svg 是 docs/assets/logo/icon.svg 的原样拷贝（ADR-230 的乌鸫图标；go:embed 拿不到上级目录，只好拷一份）。
// 用 ui.Image 按 SVG 自己的颜色画（渐变、遮罩 mygo 都支持）；ui.Icon 会整个染成文字色，不能用。
// 改了 logo 以后这份也要跟着换。
//
//go:embed logo.svg
var appLogoSVG []byte

var appLogo = ui.MustParseSVG(appLogoSVG)
