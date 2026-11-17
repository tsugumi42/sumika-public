package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 新界面里记忆页、运行页的分段和筛选片走 winshell 的，旧界面照旧：选一次，值跟着变。
func TestLegacyChoiceHelpersFollowMainView(t *testing.T) {
	for _, workbench := range []bool{true, false} {
		a := shellApp()
		if !workbench {
			a.mainView = ""
		}
		sel, chip := 0, "事实"
		tt := ui.NewTester(func(c *ui.Context) {
			a.applyLook(c)
			kit := a.wbKit(false, "")
			if workbench {
				c.SetTheme(&kit.Theme)
			}
			ui.Column(c).Fill().Padding(10).Gap(10).Children(func() {
				a.segmentedChoice(c, "run-seg", "分组", []string{"按天", "按周"}, &sel)
				a.chipChoice(c, "chips-种类", "种类", []string{"全部", "事实", "约定"}, &chip)
			})
		}, 500, 300)
		tt.Frame()
		tt.Frame()
		if got := a.workbench.shell.kit.SegHandles["run-seg"] != nil; got != workbench {
			t.Fatalf("workbench=%v：分段走 winshell 的=%v", workbench, got)
		}
		if err := tt.Click("按周"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if sel != 1 {
			t.Fatalf("workbench=%v：点按周后 sel=%d", workbench, sel)
		}
		if err := tt.Click("约定"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if chip != "约定" {
			t.Fatalf("workbench=%v：点约定后 chip=%q", workbench, chip)
		}
	}
}
