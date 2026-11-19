package linkcard

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/icons"
)

// 互动行的四个图标取自公共图标包（MingCute line 版，24×24），颜色是 currentColor，跟着 TextColor 走：
// 回复 chat_3、转推 repeat、喜欢 heart、浏览 chart_bar。linkcard 是底层包，不引界面那边的 chatui，直接用 assets/icons。
var actionIconNames = map[string]string{
	"reply":  "chat_3",
	"repost": "repeat",
	"like":   "heart",
	"views":  "chart_bar",
}

var actionIcons = sync.OnceValue(func() map[string]*ui.SVG {
	out := make(map[string]*ui.SVG, len(actionIconNames))
	for name, mc := range actionIconNames {
		out[name] = icons.Must(mc, false)
	}
	return out
})
