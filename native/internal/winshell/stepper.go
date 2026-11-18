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
	down    bool // 最近一次变化是变小：数字从上面落下来；变大从下面升上来
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
	if st.seen && st.last != value {
		st.down = value < st.last
	}
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
	outer := g.Pill(g.RowHeight)         // 单行：外框是胶囊，“凹下去”（Sunken）
	btnR := g.Inner(outer, g.SpaceTight) // 加减键和外框同心：外圆角 − 内距
	box := ui.Row(c.Key(key)).Shrink(0).Height(g.RowHeight).Padding(g.SpaceTight).Gap(g.SpaceTight).AlignItems(ui.Center).Radius(outer)
	k.SunkenField(box, st.focused)
	if o.Disabled {
		box.Opacity(DisabledAlpha)
	}
	k.surface(c, box, key, "stepper", outer, g.SpaceTight).Children(func() {
		button := func(suffix, name string, icon *ui.SVG, dir int) {
			next, can := stepValue(value, dir, o)
			disabled := o.Disabled || !can
			b := ui.ButtonBase(c.Key(key+suffix)).Label(label+" "+name).Size(inner, inner).Center().Radius(btnR).TextColor(p.Ink).Disabled(disabled).OnClick(func() { onChange(next) })
			if disabled {
				b.Opacity(DisabledAlpha)
			}
			k.Flat(b, ui.Transparent, !disabled && b.Pressed(), !disabled && b.Hovered()) // 加减键是“平的”
			b.Children(func() { ui.Icon(c, icon).Size(g.IconSmall, g.IconSmall) })
			k.surface(c, b, key+suffix, "button", btnR, 0)
		}
		button("-minus", "减", chatui.Icon("minus", false), -1)
		// 有没有单位，中间这一格总宽都一样：同一页几个加减框的左边缘对齐
		inputW := g.ValueWidth * 2
		unitW := stepperUnitWidth(o.Unit, g.IconSmall)
		if o.Unit != "" {
			inputW -= unitW + g.SpaceTight
		}
		ui.Row(c.Key(key + "-cell")).Height(inner).Gap(g.SpaceTight).AlignItems(ui.Center).Children(func() {
			// 输入框管打字和焦点；没在编辑时它的字透明，上面叠一个只读的 Text 负责逐位滚（mygo 的 ContentTransition 只管 Text，不管输入框）。
			// 点加减、外面改了值才会变，一次一次地滚，不节流（跟手的操作）；输入中直接显示输入框自己的字（铁律 1）。
			ui.Box(c.Key(key + "-field")).Width(inputW).Height(inner).Shrink(0).Children(func() {
				in := ui.TextInputBase(c.Key(key+"-input"), &st.text).Label(label).Placeholder(o.ZeroLabel).Disabled(o.Disabled).
					Width(inputW).Height(inner).Padding(g.SpaceTight).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextAlign(ui.Center).FontFeatures(chatui.Tabular).FocusRing(false)
				in.OnChange(func() { st.text = stepperDigits(st.text) })
				in.OnSubmit(commit)
				foc := in.Focused()
				if foc != st.focused {
					st.focused = foc
					if !foc { // 点别处离开输入框也算打完了
						commit()
					}
				}
				if !foc && shown != "" {
					in.TextColor(ui.Transparent)
					k.NumberRoll(ui.Text(c.Key(key+"-roll"), shown), k.snappy(), st.down).Absolute().Left(0).Right(0).Top(0).Bottom(0).
						Padding(g.SpaceTight).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextAlign(ui.Center).TextColor(p.Ink).SingleLine().PassThrough()
				}
			})
			if o.Unit != "" {
				ui.Text(c.Key(key+"-unit"), o.Unit).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine().Shrink(0).Width(unitW)
			}
		})
		button("-plus", "加", chatui.Icon("add", false), 1)
	})
}

// stepperUnitWidth 是单位那一格的宽：一个汉字（“条”“秒”）占一个小图标宽，两个字（“分钟”“小时”）占两个，
// 西文字符按六成算、不小于一格（“%”“MB”“token”）。以前一律只给一格宽，“分钟”会被截成“…”。
func stepperUnitWidth(unit string, icon float32) float32 {
	var w float32
	for _, r := range unit {
		if r > 0x7f {
			w += icon
		} else {
			w += icon * .6
		}
	}
	return max(w, icon)
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
