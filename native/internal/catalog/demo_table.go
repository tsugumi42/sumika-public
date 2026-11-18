package catalog

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 表格例子里的三张表：内容都是编的。
const (
	tableShort = "| 项目 | 状态 |\n|---|---|\n| 纸灯笔记 | 已补完 |\n| 窗边那段 | 进行中 |\n| 测试 | 通过 |\n"
	tableLong  = "这是今天的进度：\n\n| 文件 | 数量 | 说明 |\n|:--|--:|---|\n| `notes/纸灯.md` | 12 | 把昨天说到一半的窗边那段补完整，再把灯罩换成和纸的那条也记进去，免得下次又忘了从哪里接着写 |\n| `notes/窗边.md` | 3 | 只改了两个错字 |\n| `todo.md` | 128 | **全部**重新排过一遍，按[优先级](https://example.com)分了三档 |\n\n弄完我们接着聊。"
	tableWide  = "这周的记录：\n\n| 日期 | 早餐 | 午餐 | 晚餐 | 睡前 | 步数 | 心率 | 体重 | 睡眠 | 咖啡 | 饮水 | 心情 | 备注 |\n|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|---|\n| 11-10 | 6:40 | 12:10 | 19:30 | 23:15 | 8230 | 72 | 58.4 | 7.5 | 2 | 1800 | 平静 | 晴 |\n| 11-11 | 6:55 | 12:30 | 19:10 | 23:40 | 10412 | 70 | 58.2 | 6.8 | 3 | 2100 | 开心 | 下雨 |\n| 11-12 | 7:05 | 12:20 | 20:00 | 0:10 | 6120 | 75 | 58.6 | 6.1 | 4 | 1500 | 疲倦 | 熬夜 |\n"
)

// tableDemo：Markdown 表格在气泡里的样子：短表按内容宽、有长文字列的排满并换行、列很多的在表自己里横向滚动。
// 数字列右对齐；风格和代码块、引用块一样（圆角同心、表头凹块底、行间细线）。格子里的字仍是 markdown 画的（行内代码、加粗、链接照常）。
func tableDemo(c *ui.Context, e Env) {
	g := e.Kit.G
	wide := min(e.Room, g.ControlWide*2)
	e.Section(c, "states", "三种表", func() {
		e.Cells(c, "row", func() {
			e.Cell(c, "short", "两三列的短表：按内容宽，不硬撑满", func() {
				e.ChatCard(c, "short-card", wide, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{her("tb-short", 0, tableShort)})
				})
			})
			e.Cell(c, "long", "有长文字列：排满气泡，长列换行，数字列右对齐", func() {
				e.ChatCard(c, "long-card", wide, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{her("tb-long", 0, tableLong)})
				})
			})
			e.Cell(c, "wide", "列很多：在表自己里横向滚动，不撑破气泡", func() {
				e.ChatCard(c, "wide-card", wide, func(col float32) {
					messages(c, e.ChatHost(col, false), []*chatui.Msg{her("tb-wide", 0, tableWide)})
				})
			})
		})
	})
}
