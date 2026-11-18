package main

// 运行页用量、存储两个标签的骨架：外框、间距、统计格的两行字号、表格每行 Space(8) 的高度都照真实内容排，
// 表格行数按"常见的行数"取（真实行数随数据变，换成内容时表格高度会变一点，但上面的工具栏和统计格不动）。
// draw 为 false 是"还没到出现的时候"：位置排好、什么都不画。

import (
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/loadui"
)

// runSkeletonTableRows 骨架表格画几行数据行（不含表头）
const runSkeletonTableRows = 8

// runSkelStat 统计格：照 statCell（0.85rem 的小字名称、1.3rem 的大字数值）
func runSkelStat(c *ui.Context, draw bool) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(1)).Width(t.Space(24)).Children(func() {
		loadui.Bar(c.Key("l"), 60, t.Rem(0.85), draw)
		loadui.Bar(c.Key("v"), 80, t.Rem(1.3), draw)
	})
}

// runSkelTable 表格：外框、表头加 n 行，每行 Space(8) 高，高度公式同 usageTable、storageTable
func runSkelTable(c *ui.Context, draw bool, n int) {
	t := c.Theme()
	h := float32(n+1)*t.Space(8) + 2
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(t.Radius).Clip().Height(h).Children(func() {
		for i := 0; i <= n; i++ {
			c.Key(i)
			ui.Row(c).FillWidth().Height(t.Space(8)).Padding(0, t.Space(3)).Gap(t.Space(4)).AlignItems(ui.Center).Children(func() {
				loadui.Bar(c.Key("a"), 24, t.Rem(0.9), draw)
				loadui.Bar(c.Key("b"), 14, t.Rem(0.9), draw)
				loadui.Bar(c.Key("c"), 14, t.Rem(0.9), draw)
			})
		}
	})
}

// runSkeleton 整个标签的骨架。toolbar 为真时最上面多一行筛选工具栏（用量有，存储没有）；stats 是统计格数
func (a *app) runSkeleton(c *ui.Context, draw, toolbar bool, stats int) {
	t := c.Theme()
	barH := t.Space(9) // 旧界面预设下拉框的高度约 35.8，取 Space(9) = 36，差不到一像素
	if k := a.settingsKit(); k != nil {
		barH = k.G.HitSmall + 2*k.G.SpaceTight // 新界面这一行最高的是分段控件
	}
	ui.Column(c).Grow(1).MinHeight(0).Padding(t.Space(4), 0).Gap(t.Space(4)).MaxWidth(1280).Clip().Children(func() {
		if toolbar {
			ui.Row(c).Wrap().Gap(t.Space(3)).AlignItems(ui.Center).Children(func() {
				for i := 0; i < 4; i++ {
					c.Key(i)
					loadui.Bar(c, 12, 0, draw).Height(barH)
				}
			})
		}
		ui.Row(c).Wrap().Gap(t.Space(8)).GapY(t.Space(3)).Children(func() {
			for i := 0; i < stats; i++ {
				c.Key(i)
				runSkelStat(c, draw)
			}
		})
		runSkelTable(c, draw, runSkeletonTableRows)
	})
}
