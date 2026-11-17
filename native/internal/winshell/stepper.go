package winshell

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// StepperSpec 是 Stepper 的取值规则。
type StepperSpec struct {
	Min, Max float64 // 下限、上限；Max 为 0 不设上限
	Step     float64 // 加减一次的量；0 当 1
	// AllowZero：0 也是合法值（"不设""跟模型"这种），和 Min 并列：从 Min 再减一步到 0，从 0 加一步回到 Min。
	AllowZero bool
	ZeroLabel string // 值为 0 时输入框里灰字写什么；空就写 0
	Unit      string // 数字右边的单位，如 "%"
	Disabled  bool   // 保存中或者改不了：加减钮和输入框都不响应
}

// stepperState 是一个加减框的文字草稿：没在编辑时跟着值走，编辑时留着打到一半的字。
type stepperState struct {
	text    string
	last    float64
	seen    bool
	focused bool
}

// Stepper 是设置页一行里的数字框：[−] [数字 单位] [+]，加减点一下就调 onChange，输入框打完回车或点别处也调；
// 一个设置改一个数只要一步，不再是"值行 → 弹窗 → 输入 → 保存"四步。
// value 由外面管（后台确认后才变）：onChange 里外面可以拒绝，拒绝后框里的字回到 value，原因由外面写在行下。
// 加减自己守着 Min、Max 和零值规则；输入框里打的值原样交出去，合不合法由外面按它的规矩判。
func (k *Kit) Stepper(c *ui.Context, key, label string, value float64, o StepperSpec, onChange func(v float64)) {
	g, p := k.G, k.P
	if k.steppers == nil {
		k.steppers = map[string]*stepperState{}
	}
	st := k.steppers[key]
	if st == nil {
		st = &stepperState{}
		k.steppers[key] = st
	}
	shown := stepperText(value, o)
	if !st.seen || st.last != value || !st.focused {
		st.text, st.last, st.seen = shown, value, true
	}
	inner := g.RowHeight - 2*g.SpaceTight
	commit := func() {
		v, ok := parseStepper(st.text, o)
		st.text = shown // 先回到现在的值；外面接受了，下一帧值变了会跟过去
		if ok && v != value && !o.Disabled {
			onChange(v)
		}
	}
	box := ui.Row(c.Key(key)).Shrink(0).Height(g.RowHeight).Padding(g.SpaceTight).Gap(g.SpaceTight).AlignItems(ui.Center).Radius(g.RadiusControl).Background(p.Bubble)
	if o.Disabled {
		box.Opacity(DisabledAlpha)
	}
	k.surface(c, box, key, "stepper", g.RadiusControl, g.SpaceTight).Children(func() {
		button := func(suffix, name string, icon *ui.SVG, dir int) {
			next, can := stepValue(value, dir, o)
			disabled := o.Disabled || !can
			b := ui.ButtonBase(c.Key(key+suffix)).Label(label+" "+name).Size(inner, inner).Center().Radius(g.RadiusSegment).TextColor(p.Ink).Disabled(disabled).OnClick(func() { onChange(next) })
			bg := ui.Transparent
			if !disabled && b.Pressed() {
				bg = p.Press
			} else if !disabled && b.Hovered() {
				bg = p.Hover
			}
			if disabled {
				b.Opacity(DisabledAlpha)
			}
			b.Background(b.AnimateColor("fill", bg, ControlFade)).Children(func() { ui.Icon(c, icon).Size(g.IconSmall, g.IconSmall) })
			k.surface(c, b, key+suffix, "button", g.RadiusSegment, 0)
		}
		button("-minus", "减", chatui.Icon("minus", false), -1)
		// 有没有单位，中间这一格总宽都一样：同一页几个加减框的左边缘对齐
		inputW := g.ValueWidth * 2
		if o.Unit != "" {
			inputW -= g.IconSmall + g.SpaceTight
		}
		ui.Row(c.Key(key + "-cell")).Height(inner).Gap(g.SpaceTight).AlignItems(ui.Center).Children(func() {
			in := ui.TextInputBase(c.Key(key+"-input"), &st.text).Label(label).Placeholder(o.ZeroLabel).Disabled(o.Disabled).
				Width(inputW).Height(inner).Padding(g.SpaceTight).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextAlign(ui.Center)
			in.OnChange(func() { st.text = stepperDigits(st.text) })
			in.OnSubmit(commit)
			if foc := in.Focused(); foc != st.focused {
				st.focused = foc
				if !foc { // 点别处离开输入框也算打完了
					commit()
				}
			}
			if o.Unit != "" {
				ui.Text(c.Key(key+"-unit"), o.Unit).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine().Shrink(0).Width(g.IconSmall)
			}
		})
		button("-plus", "加", chatui.Icon("add", false), 1)
	})
}

// stepperText 是值在输入框里的样子：整数不带小数点，零且有 ZeroLabel 时留空让灰字露出来。
func stepperText(v float64, o StepperSpec) string {
	if v == 0 && o.ZeroLabel != "" {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// stepperDigits 只留数字和第一个小数点。
func stepperDigits(s string) string {
	var b strings.Builder
	dot := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' && !dot:
			dot = true
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseStepper 读输入框里的字；空且允许零就是 0，空而不允许零就当没打（ok=false）。
func parseStepper(text string, o StepperSpec) (float64, bool) {
	text = strings.TrimSpace(text)
	if text == "" || text == "." {
		return 0, o.AllowZero
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return roundStep(v), true
}

// stepValue 是从 v 往 dir（+1 或 −1）走一步落在哪，和能不能走（已在头上就不能）。
func stepValue(v float64, dir int, o StepperSpec) (float64, bool) {
	step := o.Step
	if step <= 0 {
		step = 1
	}
	n := v
	if dir > 0 {
		if v <= 0 && o.AllowZero && o.Min > 0 {
			n = o.Min
		} else {
			n = v + step
			if n < o.Min {
				n = o.Min
			}
		}
		if o.Max > 0 && n > o.Max {
			n = o.Max
		}
	} else {
		n = v - step
		if o.AllowZero && o.Min > 0 && n < o.Min {
			n = 0
		} else if n < o.Min {
			n = o.Min
		}
	}
	n = roundStep(n)
	return n, n != v
}

// roundStep 去掉加减小数的浮点尾巴（0.1 加三次不是 0.30000000000000004）。
func roundStep(v float64) float64 { return math.Round(v*1e6) / 1e6 }
