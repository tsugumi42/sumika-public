package chatui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 只合并相邻的读和搜（Lorca agents.go:511-525）：中间夹了别的种类就断开。
func TestSegments(t *testing.T) {
	mk := func(kinds ...StepKind) []*Step {
		var out []*Step
		for i, k := range kinds {
			out = append(out, &Step{ID: fmt.Sprint(i), Kind: k, State: StepDone})
		}
		return out
	}
	for _, c := range []struct {
		kinds []StepKind
		want  []int // 每段几步
	}{
		{[]StepKind{StepRead, StepRead, StepSearch}, []int{3}},
		{[]StepKind{StepRead, StepCommand, StepRead}, []int{1, 1, 1}},
		{[]StepKind{StepThink, StepRead, StepSearch, StepEdit, StepSearch}, []int{1, 2, 1, 1}},
		{[]StepKind{StepSearch}, []int{1}},
	} {
		var got []int
		for _, seg := range Segments(mk(c.kinds...)) {
			got = append(got, len(seg.Steps))
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%v 分段 %v，期望 %v", c.kinds, got, c.want)
		}
	}
}

// 合并行的话：有一步在跑就是进行时，全完了是过去时；只有一种就只写那一种。
func TestExploreSummary(t *testing.T) {
	r := func(st StepState) *Step { return &Step{Kind: StepRead, State: st} }
	s := func(st StepState) *Step { return &Step{Kind: StepSearch, State: st} }
	for _, c := range []struct {
		steps []*Step
		want  string
	}{
		{[]*Step{r(StepDone), r(StepDone), s(StepDone)}, "读了 2 个文件、搜了 1 次"},
		{[]*Step{r(StepDone), s(StepRunning)}, "正在读 1 个文件、搜 1 次"},
		{[]*Step{r(StepDone), r(StepDone)}, "读了 2 个文件"},
		{[]*Step{s(StepRunning), s(StepDone)}, "正在搜 2 次"},
		{[]*Step{s(StepDone), s(StepDone)}, "搜了 2 次"},
	} {
		if got := exploreSummary(c.steps); got != c.want {
			t.Errorf("得到 %q，期望 %q", got, c.want)
		}
	}
}

func processTester(h Host, steps []*Step, open map[string]*bool) *ui.Tester {
	view := func(c *ui.Context) {
		c.Root().Background(h.T.C.ChatBg)
		h.Process(c, "p", CharColors[0], steps, func(id string) *bool {
			if open[id] == nil {
				open[id] = new(bool)
			}
			return open[id]
		})
	}
	tt := ui.NewTester(view, 700, 500)
	tt.Frame()
	tt.Frame()
	return tt
}

// 读搜行：合并行能点开，展开后每步一行；失败只出角标、不写"失败""完成"；行四边内距一样、高不低于控件小档。
func TestExploreRow(t *testing.T) {
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		tk := Derive(s)
		h := Host{T: tk, Column: 600}
		steps := []*Step{{ID: "a", Kind: StepRead, State: StepDone, Target: "notes/a.md"}, {ID: "b", Kind: StepSearch, State: StepFailed, Target: "纸灯"}}
		open := map[string]*bool{}
		tt := processTester(h, steps, open)
		if !tt.HasText("读了 1 个文件、搜了 1 次") {
			t.Fatalf("外观 %+v 没有合并行：%v", s, tt.Texts())
		}
		for _, w := range []string{"失败", "完成", "成功"} {
			for _, x := range tt.Texts() {
				if strings.Contains(x, w) {
					t.Errorf("出现了说明文字 %q：%q", w, x)
				}
			}
		}
		row := tree(tt).path("p", "explore-a", "row")
		if row == nil {
			t.Fatal("找不到合并行")
		}
		if row.Box.H < tk.HSm-0.5 {
			t.Errorf("行高 %.1f 低于控件小档 %.1f", row.Box.H, tk.HSm)
		}
		for k := 1; k < 4; k++ {
			if !near(row.Pad[k], row.Pad[0]) {
				t.Errorf("行内距不等：%v", row.Pad)
			}
		}
		if tree(tt).path("p", "explore-a", "row", "badge") == nil {
			t.Error("有一步失败，合并行没有角标")
		}
		*open["a"] = true
		tt.Frame()
		tt.Frame()
		if !tt.HasText("读了 notes/a.md") || !tt.HasText("搜了 纸灯") {
			t.Errorf("展开后没有每步一行：%v", tt.Texts())
		}
	}
}

// 思考行：进行中"正在想"不显示正文（设置关）；设置开时边想边显示正文（ADR-286，两种都测）；完成"想了 N 秒"，展开看正文。
func TestThinkRow(t *testing.T) {
	tk := Derive(DefaultSettings())
	for _, live := range []bool{false, true} {
		h := Host{T: tk, Column: 600, LiveOpen: live}
		st := &Step{ID: "t", Kind: StepThink, State: StepRunning, Thought: "先看看她昨天说了什么"}
		open := map[string]*bool{}
		tt := processTester(h, []*Step{st}, open)
		if !tt.HasText("正在想") {
			t.Fatalf("进行中没有正在想：%v", tt.Texts())
		}
		if got := tt.HasText(st.Thought); got != live {
			t.Errorf("想的时候展开=%v，进行中正文显示=%v", live, got)
		}
		st.State, st.Seconds = StepDone, 3
		tt.Frame()
		tt.Frame()
		if !tt.HasText("想了 3 秒") {
			t.Errorf("完成后没有想了 3 秒：%v", tt.Texts())
		}
		if tt.HasText(st.Thought) {
			t.Error("完成后没展开就显示了正文")
		}
		*open["t"] = true
		tt.Frame()
		tt.Frame()
		if !tt.HasText(st.Thought) {
			t.Error("展开后没有正文")
		}
	}
}

// 成功色作图标对各层底 ≥ 3、作文字 ≥ 4.5（浅深都量）。
func TestOkContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, wall := range []bool{false, true} {
			s := DefaultSettings()
			s.Dark, s.Wallpaper = dark, wall
			c := Derive(s).C
			for _, bg := range []ui.Color{c.Bg, c.Surface, c.Sunken, c.Bubble} {
				if r := Contrast(c.Ok, bg); r < MinIconContrast {
					t.Errorf("深=%v 成功色图标对比 %.2f < 3", dark, r)
				}
				if r := Contrast(c.OkText, bg); r < MinTextContrast {
					t.Errorf("深=%v 成功色文字对比 %.2f < 4.5", dark, r)
				}
			}
		}
	}
}
