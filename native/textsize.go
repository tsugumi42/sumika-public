package main

// 字号是全局设置：亮色和夜间两份主题一起改，字号和间距按同一个比例缩放。
import (
	"math"
	"strconv"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
)

// textSizes 是设置里能选的基准字号（正文字号）；没存过设置时默认 15（desksettings.DefaultTextSize），预设自己的正文字号是 13。
var textSizes = []float32{13, 14, 15, 16, 17, 18}

// textSizeLabels 和 textSizes 一一对应，给分段控件用。
var textSizeLabels = func() []string {
	out := make([]string, len(textSizes))
	for i, v := range textSizes {
		out[i] = strconv.Itoa(int(v))
	}
	return out
}()

// scaleText 把主题的所有字号和间距按"新正文字号 ÷ 现在的正文字号"同比例缩放（实现在 desksettings，默认外观也用它）。
func scaleText(d *theme.Definition, size float32) { desksettings.ScaleText(d, size) }

// legacyTextSize 是桌面外观里存的正文字号（设置页“字号”那一行）。新界面用它定令牌的字号档（chatui.Settings.Text，夹在 13～20），
// 没读到就是默认值（desksettings.DefaultTextSize，15）。临时：新设置页（原型外壳迁移 H）有了自己的外观设置之后，这个桥和这一行一起删。
func (a *app) legacyTextSize() float32 {
	if t := a.look.current; t != nil {
		return t.Typography().Body().Size()
	}
	return desksettings.DefaultTextSize
}

// textSizeIndex 取离正文字号最近的一档；存的是档外的值（手改文件）时设置页显示最近的一档，实际字号不动，直到用户重新选。
func textSizeIndex(body float32) int {
	best := 0
	for i, v := range textSizes {
		if math.Abs(float64(v-body)) < math.Abs(float64(textSizes[best]-body)) {
			best = i
		}
	}
	return best
}

// changeLookBoth 对亮色和夜间两份主题做同一个修改；任何一份编译失败都两份一起不落，免得切换深浅后字号对不上。
func (a *app) changeLookBoth(edit func(*theme.Definition)) {
	s := &a.look
	ld, nd := s.light.Definition(), s.night.Definition()
	edit(&ld)
	edit(&nd)
	lt, err := theme.Compile(ld)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	nt, err := theme.Compile(nd)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	s.invalid = ""
	s.settings.Light, s.light = ld, lt
	s.settings.Night, s.night = nd, nt
	if s.current != nil && s.current.Dark() {
		s.current = nt
	} else {
		s.current = lt
	}
	a.persistLook()
}

// setTextSizeLive 拖字号滑块时调用：界面实时跟着变，但不写盘（a.look.textDirty 记着，松手再 persistLook 一次）。
func (a *app) setTextSizeLive(size float32) {
	s := &a.look
	ld, nd := s.light.Definition(), s.night.Definition()
	scaleText(&ld, size)
	scaleText(&nd, size)
	lt, err := theme.Compile(ld)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	nt, err := theme.Compile(nd)
	if err != nil {
		s.invalid = err.Error()
		return
	}
	s.invalid = ""
	s.settings.Light, s.light = ld, lt
	s.settings.Night, s.night = nd, nt
	if s.current != nil && s.current.Dark() {
		s.current = nt
	} else {
		s.current = lt
	}
	s.textDirty = true
}

// setTextSize 在设置页选了一档之后调用。
func (a *app) setTextSize(size float32) {
	a.changeLookBoth(func(d *theme.Definition) { scaleText(d, size) })
}

// textSizeRow 是设置页"字号"那一行。
func (a *app) textSizeRow(c *ui.Context) {
	e := &a.look.editor
	row(c, "字号", "", func() {
		choice.Segments(c, "字号", textSizeLabels, &e.sizeIdx).OnChange(func() { a.setTextSize(textSizes[e.sizeIdx]) })
	})
}
