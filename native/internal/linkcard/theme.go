package linkcard

import (
	"time"
	// Windows 上不靠系统时区库：用 Go 自带的时区数据找 Asia/Shanghai
	_ "time/tzdata"

	"github.com/egoist/mygo/ui"
)

// 推特卡片的配色：照 react-tweet 的 light 和 dark（就是 X 的“昏暗”）两套变量
const (
	ThemeAuto  = "auto"
	ThemeLight = "light"
	ThemeDark  = "dark"
)

var beijing = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600) // 中国没有夏令时，取不到时区数据时固定东八区
}()

// ThemeAt 是这一刻该用的配色：设置写死 light / dark 就照设置；auto（以及没写、写了别的）按北京时间，
// 06:00 ≤ t < 18:00 浅色，其余深色。不看本机时区。
func ThemeAt(setting string, now time.Time) string {
	switch setting {
	case ThemeLight, ThemeDark:
		return setting
	}
	if h := now.In(beijing).Hour(); h >= 6 && h < 18 {
		return ThemeLight
	}
	return ThemeDark
}

type tweetColors struct {
	bg, border, text, secondary, blue, placeholder ui.Color
	badgeBlue, badgeGold, badgeGray                ui.Color
}

func colorsFor(theme string) tweetColors {
	c := tweetColors{
		bg: ui.RGB(255, 255, 255), border: ui.RGB(207, 217, 222), text: ui.RGB(15, 20, 25), secondary: ui.RGB(83, 100, 113),
		blue: ui.RGB(29, 155, 240), placeholder: ui.RGB(247, 249, 249),
	}
	if theme == ThemeDark {
		c.bg, c.border, c.text, c.secondary, c.placeholder = ui.RGB(21, 32, 43), ui.RGB(66, 83, 100), ui.RGB(247, 249, 249), ui.RGB(139, 152, 165), ui.RGB(30, 39, 50)
	}
	// 认证标：蓝、金、灰。蓝和灰照 react-tweet；金是商家认证常见的金黄
	c.badgeBlue, c.badgeGold, c.badgeGray = ui.RGB(29, 155, 240), ui.RGB(226, 176, 22), ui.RGB(130, 154, 171)
	return c
}
