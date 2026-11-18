package catalog

import (
	"github.com/egoist/mygo/ui"
)

// 舞台里排版用的几个小件：节标题、一排并列的格子、带小字标签的格子。
// 间距、字号全取 Kit 的令牌；这些容器自己不带底色、不带圆角，数“显示层”时不会虚高。

// Section 是舞台里的一节：小标题加内容。
func (e Env) Section(c *ui.Context, name, title string, body func()) {
	g := e.Kit.G
	ui.Column(c.Key(e.Key(name))).FillWidth().Gap(g.SpaceSmall).Children(func() {
		e.Kit.SectionHeading(c, title)
		body()
	})
}

// Cells 是一排并列的格子，放不下就换行，不出滚动条。
func (e Env) Cells(c *ui.Context, name string, body func()) {
	g := e.Kit.G
	ui.Row(c.Key(e.Key(name))).FillWidth().Wrap().Gap(g.SpaceMedium).AlignItems(ui.Start).Children(body)
}

// Cell 是一个格子：上面是组件，下面一行小字写它是哪种状态。
func (e Env) Cell(c *ui.Context, name, label string, body func()) {
	g, p := e.Kit.G, e.Kit.P
	ui.Column(c.Key(e.Key(name))).Gap(g.SpaceTight).AlignItems(ui.Start).Shrink(0).Children(func() {
		body()
		ui.Text(c, label).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).SingleLine()
	})
}

// Line 是试一试里的一行说明文字：写“点了以后发生了什么”，弱一档。
func (e Env) Line(c *ui.Context, name, text string) {
	g, p := e.Kit.G, e.Kit.P
	ui.Text(c.Key(e.Key(name)), text).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted)
}

// Play 是试一试里的一组：控件在上，结果文字在下，两样靠得近、和别的组拉开。
func (e Env) Play(c *ui.Context, name string, body func()) {
	g := e.Kit.G
	ui.Column(c.Key(e.Key(name))).Gap(g.SpaceSmall).AlignItems(ui.Start).Shrink(0).Children(body)
}
