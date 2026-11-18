package design

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design/colorx"
)

// ContrastCheck 是一对“字或图标 × 它常放的底色”的对比度（规格 V4）。
type ContrastCheck struct {
	Name, Bg   string
	Ratio, Min float64
	// Required 为假：现行配色就不保证这一对（比如第三级文字压在凹块上），只记录、不拦。
	Required bool
}

// Pass 是这一对是否达标。
func (c ContrastCheck) Pass() bool { return c.Ratio >= c.Min }

// ContrastChecks 列出 t 这个模式下要看的全部对比度；chars 是要一起检查的角色色（默认角色色、群友名字色）。
func (t Tokens) ContrastChecks(chars ...colorx.OKLCH) []ContrastCheck {
	c := t.Color
	type bg struct {
		name string
		c    ui.Color
	}
	all := []bg{{"窗口底", c.Bg}, {"内容层", c.Surface}, {"凹块", c.Sunken}, {"她的气泡", c.Bubble}, {"聊天底", c.ChatBg}}
	var out []ContrastCheck
	add := func(name string, fg ui.Color, min float64, req bool, bgs ...bg) {
		for _, b := range bgs {
			out = append(out, ContrastCheck{Name: name, Bg: b.name, Ratio: colorx.Contrast(fg, b.c), Min: min, Required: req})
		}
	}
	add("正文", c.Text, 4.5, true, all...)
	add("次要文字", c.Text2, 4.5, true, all...)
	add("第三级文字", c.Text3, 4.5, false, all...)
	add("危险色", c.Danger, 4.5, true, all[0], all[1])
	add("成功色（图标）", c.Ok, 3, true, all[0], all[1], all[2])
	for _, ch := range chars {
		a := t.AccentOf(ch)
		h := fmt.Sprintf("色相 %.0f", ch.H)
		add(h+" 的名字字色", a.Text, 4.5, false, all[0], all[1], all[3])
		add(h+" 深档上的白字", a.OnStrong, 4.5, true, bg{"强调深档", a.Strong})
		add(h+" 深档上的次要字", a.OnStrongMuted, 4.5, true, bg{"强调深档", a.Strong})
		add(h+" 实底上的字", a.On, 4.5, false, bg{"角色色实底", a.Base})
	}
	return out
}
