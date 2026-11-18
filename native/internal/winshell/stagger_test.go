package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 错开淡入的总时长：不管几项，整组都在 StaggerCap 里放完；只有前 StaggerMax 项参与。
func TestStaggerCap(t *testing.T) {
	for _, n := range []int{1, 2, 3, 5, 8, 12, 13, 40, 1000} {
		if total := StaggerTotal(n); total > StaggerCap || total <= 0 {
			t.Errorf("n=%d 总时长 %v，应在 (0, %v]", n, total, StaggerCap)
		}
		prev := time.Duration(-1)
		for i := range min(n, StaggerMax) {
			d := StaggerDelay(i, n)
			if d < prev {
				t.Errorf("n=%d 第 %d 项晚开始 %v，比前一项 %v 还早", n, i, d, prev)
			}
			prev = d
		}
		if n > StaggerMax && StaggerDelay(StaggerMax, n) >= 0 {
			t.Errorf("n=%d 第 %d 项不该参与", n, StaggerMax)
		}
	}
	// 不知道几项（0）也封顶
	for i := range StaggerMax {
		if d := StaggerDelay(i, 0); d+staggerEach() > StaggerCap {
			t.Errorf("n 未知时第 %d 项 %v 超出上限", i, d)
		}
	}
}

// 逐帧：每一项一个接一个亮起来，后面的项比前面的晚；整组按时放完；慢放按倍数变长；减少动态效果时没有位移。
func TestStaggerInFrames(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		for _, scale := range []float64{1, 4} {
			old := SetMotionScale(scale)
			k := testKit()
			k.SetMaterial(MaterialSolid)
			const n = 8
			show := false
			tt := motionTester(func(c *ui.Context) {
				c.SetTheme(&k.Theme)
				c.Root().Background(ui.RGB(0, 0, 0))
				ui.Column(c.Key("root")).Width(200).Padding(10).Gap(4).Children(func() {
					if !show {
						return
					}
					for i := range n {
						row := ui.Box(c.Key(i)).FillWidth().Height(20).Background(ui.RGB(255, 255, 255))
						k.StaggerIn(c, row, i, n, true)
					}
				})
			}, 240, 300, reduce)
			for range 3 {
				step(tt, 50*time.Millisecond)
			}
			show = true
			step(tt, time.Millisecond)
			bright := func(i int) int { return lum(tt.Image().RGBAAt(100, 10+i*24+10)) }
			firstFull := make([]time.Duration, n)
			startAt := make([]time.Duration, n)
			for i := range firstFull {
				firstFull[i], startAt[i] = -1, -1
			}
			var maxMove int // 行下面 2 像素的缝里亮了多少：行往下偏出去才会亮
			for f := 0; f < int(float64(StaggerCap)*scale/float64(8*time.Millisecond))+40; f++ {
				at := time.Duration(f) * 8 * time.Millisecond
				for i := range n {
					b := bright(i)
					if b > 6 && startAt[i] < 0 {
						startAt[i] = at
					}
					if b >= 760 && firstFull[i] < 0 {
						firstFull[i] = at
					}
					maxMove = max(maxMove, lum(tt.Image().RGBAAt(100, 10+i*24+22)))
				}
				step(tt, 8*time.Millisecond)
			}
			limit := time.Duration(float64(StaggerCap)*scale) + 40*time.Millisecond
			for i := range n {
				if firstFull[i] < 0 || firstFull[i] > limit {
					t.Errorf("reduce=%v 倍数 %v 第 %d 项 %v 才完全亮起，上限 %v", reduce, scale, i, firstFull[i], limit)
				}
				if i > 0 && startAt[i] < startAt[i-1] {
					t.Errorf("reduce=%v 倍数 %v 第 %d 项比前一项先开始", reduce, scale, i)
				}
			}
			if startAt[n-1] <= startAt[0] {
				t.Errorf("reduce=%v 倍数 %v 最后一项没有比第一项晚开始（%v %v）", reduce, scale, startAt[n-1], startAt[0])
			}
			if !reduce && scale == 1 && maxMove <= 6 {
				t.Errorf("平时应有一点上移，缝里一直没亮：%d", maxMove)
			}
			if reduce && maxMove > 6 {
				t.Errorf("减少动态效果时不该上移，缝里亮了 %d", maxMove)
			}
			t.Logf("reduce=%v 倍数 %v：第一项 %v 起、%v 满；最后一项 %v 起、%v 满", reduce, scale, startAt[0], firstFull[0], startAt[n-1], firstFull[n-1])
			SetMotionScale(old)
		}
	}
}

// 设置页换分类：右边的各组错开淡入；页面刚打开时直接就位（没人换过东西不放动画）。
func TestSettingsPaneStaggersOnCategoryChange(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	sel := "a"
	gen := 0
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		c.Root().Background(ui.RGB(0, 0, 0))
		k.SettingsPane(c, "pane", SettingsPaneSpec{
			Categories: []Category{{ID: "a", Label: "甲类"}, {ID: "b", Label: "乙类"}}, Selected: sel, OnSelect: func(id string) { sel = id },
			Height: 400, Gen: gen,
			Content: func() {
				for i := range 3 {
					k.SettingGroup(c, "g"+sel+string(rune('0'+i)), "组", func() {
						k.SettingItem(c, "i", "名字", "", SettingItemOptions{}, func() {})
					})
				}
			},
		})
	}, 1000, 420, false)
	tt.Frame()
	r, ok := tt.Find("名字")
	if !ok {
		t.Fatal("找不到设置项")
	}
	px := func() int { return lum(tt.Image().RGBAAt(int(r.X)+4, int(r.Y)+4)) }
	full := px()
	if full == 0 {
		t.Fatal("页面刚打开就该直接可见")
	}
	for _, change := range []func(){func() { sel = "b" }, func() { gen++ }} {
		settle(tt)
		change()
		step(tt, 4*time.Millisecond)
		early := px()
		step(tt, 250*time.Millisecond)
		late := px()
		if abs(early-full) < 10 || abs(late-full) > 3 {
			t.Errorf("换分类后第一组应先是淡的、后放完：%d → %d（放完的样子 %d）", early, late, full)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
