package main

// 记忆页的骨架：结构照真实内容排，高度才对得上（见 loadui 的说明）。draw 为 false 是"还没到出现的时候"：
// 位置排好、什么都不画，到点那一帧只是画出来，布局不动。

import (
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/loadui"
)

// memSkeletonRowCount 骨架排多少行：多出窗口的部分被裁掉，所以按"屏再高也排得下"取；真实列表是虚拟的，不受这个数影响
const memSkeletonRowCount = 30

// memSkelRow 一行：外壳、内边距、两行字的字号和行间距都照 memRow（列表）和 memRecent（最近变动）里的一行
func memSkelRow(c *ui.Context, draw bool) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(t.Space(3)).Padding(t.Space(2), t.Space(3)).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(t.Space(0.5)).Children(func() {
			loadui.Bar(c.Key("a"), 72, 0, draw)
			loadui.Bar(c.Key("b"), 48, t.Rem(0.8), draw)
		})
	})
}

// memListSkeleton 列表中栏的骨架：外框同 memRows 里的 ui.List（占满、同内边距）
func memListSkeleton(c *ui.Context, draw bool) {
	t := c.Theme()
	ui.Column(c).Grow(1).MinHeight(0).Padding(t.Space(2), t.Space(4)).Clip().Children(func() {
		for i := range memSkeletonRowCount {
			c.Key(i)
			memSkelRow(c, draw)
		}
	})
}

// memRecentSkeleton 最近变动的骨架：外框同 memRecent 里的 Scroll 加 Column
func memRecentSkeleton(c *ui.Context, draw bool) {
	t := c.Theme()
	ui.Column(c).Grow(1).MinHeight(0).Padding(t.Space(2), t.Space(4)).Gap(t.Space(1)).Clip().Children(func() {
		for i := range memSkeletonRowCount {
			c.Key(i)
			memSkelRow(c, draw)
		}
	})
}

// memDetailSkeleton 详情的骨架：正文两行、状态徽章、几行"名字 + 值"。真实详情的行数随这一条变，这里按最常见的形状排，
// 换成内容时整块高度会变一点（列在滚动区里，不推动上面的标签和标题）
func memDetailSkeleton(c *ui.Context, draw bool) {
	t := c.Theme()
	loadui.Bar(c.Key("t1"), 100, 0, draw)
	loadui.Bar(c.Key("t2"), 64, 0, draw)
	loadui.Bar(c.Key("badge"), 36, t.Rem(0.85), draw)
	for i := range 6 {
		c.Key(i)
		ui.Row(c).FillWidth().Gap(t.Space(3)).Children(func() {
			loadui.Bar(c.Key("k"), 22, t.Rem(0.85), draw)
			loadui.Bar(c.Key("v"), 50, 0, draw)
		})
	}
}
