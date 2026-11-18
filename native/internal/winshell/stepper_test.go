package winshell

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/uitest"
)

type stepperRig struct {
	k     *Kit
	tt    *ui.Tester
	value float64
	spec  StepperSpec
	calls []float64
	apply bool // 外面接受就把值改过去；不接受就留着（模拟校验失败）
}

func newStepperRig(value float64, spec StepperSpec) *stepperRig {
	r := &stepperRig{k: testKit(), value: value, spec: spec, apply: true}
	r.tt = ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&r.k.Theme)
		ui.Column(c.Key("page")).Width(600).Children(func() {
			r.k.Row(c, "row", "标题", "", func() {
				r.k.Stepper(c, "win", "窗口", r.value, r.spec, func(v float64) {
					r.calls = append(r.calls, v)
					if r.apply {
						r.value = v
					}
				})
			})
			ui.Text(c.Key("elsewhere"), "别处").Height(30)
		})
	}, 700, 400)
	r.tt.Frame()
	r.tt.Frame()
	return r
}

func (r *stepperRig) click(t *testing.T, label string) {
	t.Helper()
	if err := r.tt.Click(label); err != nil {
		t.Fatal(label, err)
	}
	r.tt.Frame()
	r.tt.Frame()
}

func (r *stepperRig) typeIn(t *testing.T, text string) {
	t.Helper()
	r.click(t, "窗口")
	r.tt.Key(ui.Cmd, ui.KeyA)
	r.tt.Type(text)
	r.tt.Frame()
}

// 点加减钮：一次一步，一步就调一次 onChange，不弹窗、不要再点保存。
func TestStepperButtonsStepAndStopAtEnds(t *testing.T) {
	r := newStepperRig(2000, StepperSpec{Min: 1000, Max: 4000, Step: 1000, AllowZero: true, ZeroLabel: "跟模型"})
	r.click(t, "窗口 加")
	r.click(t, "窗口 加")
	r.click(t, "窗口 加") // 已经在 4000 了，加钮灰掉，点了没反应
	if len(r.calls) != 2 || r.calls[0] != 3000 || r.calls[1] != 4000 || r.value != 4000 {
		t.Fatalf("加到头：调用 %v 值 %v", r.calls, r.value)
	}
	for _, want := range []float64{3000, 2000, 1000, 0} {
		r.click(t, "窗口 减")
		if r.value != want {
			t.Fatalf("往下一步应是 %v，得到 %v（调用 %v）", want, r.value, r.calls)
		}
	}
	// 0 是"跟模型"，再减没有动作；从 0 加回最小值
	n := len(r.calls)
	r.click(t, "窗口 减")
	if len(r.calls) != n {
		t.Fatalf("0 上再减不该有动作：%v", r.calls)
	}
	r.click(t, "窗口 加")
	if r.value != 1000 {
		t.Fatalf("从 0 加一步应回到下限 1000，得到 %v", r.value)
	}
}

// 输入框：打字过滤成数字，回车交出去；点别处离开也交；打的和现在一样不调。
func TestStepperTypedValueCommitsOnEnterAndBlur(t *testing.T) {
	r := newStepperRig(2000, StepperSpec{Min: 1000, Step: 1000})
	r.typeIn(t, "75ab00")
	if got := r.k.steppers["win"].text; got != "7500" {
		t.Fatalf("字母该被滤掉：%q", got)
	}
	r.tt.Key(0, ui.KeyEnter)
	r.tt.Frame()
	if len(r.calls) != 1 || r.calls[0] != 7500 || r.value != 7500 {
		t.Fatalf("回车后：调用 %v 值 %v", r.calls, r.value)
	}
	r.typeIn(t, "9000")
	r.click(t, "别处")
	r.tt.Frame()
	if len(r.calls) != 2 || r.calls[1] != 9000 {
		t.Fatalf("点别处离开也该交出去：%v", r.calls)
	}
	r.typeIn(t, "9000")
	r.tt.Key(0, ui.KeyEnter)
	r.tt.Frame()
	if len(r.calls) != 2 {
		t.Fatalf("没改的值不该再调：%v", r.calls)
	}
}

// 外面拒绝（校验没过）：框里的字退回现在的值，不卡在打错的数上。
func TestStepperRejectedValueSnapsBack(t *testing.T) {
	r := newStepperRig(2000, StepperSpec{Min: 1000, Step: 1000})
	r.apply = false
	r.typeIn(t, "999")
	r.tt.Key(0, ui.KeyEnter)
	r.tt.Frame()
	r.tt.Frame()
	if len(r.calls) != 1 || r.value != 2000 || r.k.steppers["win"].text != "2000" {
		t.Fatalf("拒绝后该回到 2000：调用 %v 值 %v 字 %q", r.calls, r.value, r.k.steppers["win"].text)
	}
}

// 保存中（Disabled）：加减钮和输入框都不响应。
func TestStepperDisabledIgnoresInput(t *testing.T) {
	r := newStepperRig(2000, StepperSpec{Min: 1000, Step: 1000, Disabled: true})
	r.click(t, "窗口 加")
	r.click(t, "窗口 减")
	if len(r.calls) != 0 {
		t.Fatalf("禁用时不该有动作：%v", r.calls)
	}
}

// 带百分号的比例：整数百分比，步长 5，值到顶就停。
func TestStepperPercentUnit(t *testing.T) {
	r := newStepperRig(95, StepperSpec{Min: 10, Max: 100, Step: 5, Unit: "%"})
	if !r.tt.HasText("%") {
		t.Fatalf("没有百分号：%q", r.tt.Texts())
	}
	r.click(t, "窗口 加")
	r.click(t, "窗口 加")
	if r.value != 100 || len(r.calls) != 1 {
		t.Fatalf("95 加到 100 就停：值 %v 调用 %v", r.value, r.calls)
	}
}

func TestStepValueRules(t *testing.T) {
	o := StepperSpec{Min: 0.1, Max: 1, Step: 0.1}
	v, ok := stepValue(0.7, 1, o)
	if !ok || v != 0.8 {
		t.Fatalf("0.7 加一步：%v %v", v, ok)
	}
	if v, ok = stepValue(1, 1, o); ok || v != 1 {
		t.Fatalf("到顶不能再加：%v %v", v, ok)
	}
	if v, ok = stepValue(0.1, -1, o); ok || v != 0.1 {
		t.Fatalf("到底不能再减：%v %v", v, ok)
	}
}

// 加减框自己满足几何规矩：四边内距相等、子不越内距、圆角同心。
func TestStepperGeometry(t *testing.T) {
	r := newStepperRig(2000, StepperSpec{Min: 1000, Step: 1000, AllowZero: true, ZeroLabel: "跟模型", Unit: "K"})
	root := uitest.Tree(r.tt)
	row := root.Find("row")
	if row == nil || root.Find("win") == nil {
		t.Fatal("没有找到加减框")
	}
	var pairs []uitest.Pair
	uitest.CheckTree(t, row, "加减框", r.k.T.RInnerMin, &pairs)
	if len(pairs) == 0 {
		t.Fatal("一块都没查到，检查走空了")
	}
	uitest.Equal(t, "加减框", "行高", row.Box.H, r.k.G.RowHeight)
}
