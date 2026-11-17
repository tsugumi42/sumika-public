package main

// 运行页怎么画（规格-运行页 第八节）：外壳是"标题加标签栏加内容区"，照设置页的形态。
// 控件走 legacy_choice.go 的入口：标签是分段控件；表格用 mygo 的 ui.OutlineTable（固定表头、点列头排序、
// 数字列右对齐、第一列带展开箭头，规格第八节第 6 条要核对的几项都有，没有缺口，核对结果写在规格第十二节）；
// 加载占位按 loadstate 的规则，骨架在 runskeleton.go。占比横条和按天的小条是用令牌自绘的矩形（Table 没有这种单元格）。
// 颜色、字号、间距全用主题令牌，不写色值。

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/winshell"
)

const runWide = 1000 // 内容区比这窄，两块表格上下叠着放

func (a *app) runPageView(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	ui.Column(c).Grow(1).MinWidth(0).Padding(t.Space(4), t.Space(6)).Gap(t.Space(2)).Children(func() {
		a.pageTitle(c, "运行")
		ui.Row(c.Key("run-tabs-row")).MaxWidth(420).Children(func() {
			a.segmentedChoice(c, "run-tabs", "运行页标签", runTabNames, &p.tab)
		})
		ui.Box(c.Key(p.tab)).Grow(1).MinHeight(0).Children(func() {
			switch p.tab {
			case runTabStatus:
				ui.Scroll(c).Grow(1).Children(func() {
					ui.Column(c).Padding(t.Space(4), 0).MaxWidth(860).Children(func() { a.statusBody(c) })
				})
			case runTabResources:
				a.resourcesView(c)
			case runTabUsage:
				a.usageView(c)
			case runTabStorage:
				a.storageView(c)
			case runTabQuality:
				a.qualityView(c)
			}
		})
	})
}

// ───────────── 共用的小件 ─────────────

// statCell 是概览条里的一格：小字名称在上，大字数值在下
func statCell(c *ui.Context, label, value string, danger bool) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(1)).MinWidth(0).Children(func() {
		ui.Text(c, label).TextColor(t.TextMuted).FontSize(t.Rem(0.85)).SingleLine()
		v := ui.Text(c, value).Bold().FontSize(t.Rem(1.3)).FontFeatures("tnum").SingleLine()
		if danger {
			v.TextColor(t.Danger)
		}
	})
}

// numText 是表格里的数字：等宽数字，右对齐由列管
func numText(c *ui.Context, s string) ui.Element {
	return ui.Text(c, s).FontFeatures("tnum").SingleLine()
}

func mutedText(c *ui.Context, s string) ui.Element {
	return ui.Text(c, s).TextColor(c.Theme().TextMuted).SingleLine()
}

// barOf 是横向小条：frac 在 0 到 1 之间，轨道用边框色，填充用强调色（单色，不另起色板）
func barOf(c *ui.Context, frac float64, h float32) {
	t := c.Theme()
	if frac < 0 || frac != frac {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	ui.Row(c).FillWidth().Height(h).Radius(h / 2).Background(t.Border).Children(func() {
		if frac > 0 {
			ui.Box(c).Grow(float32(frac)).FillHeight().Radius(h / 2).Background(t.Accent)
		}
		if frac < 1 {
			ui.Box(c).Grow(float32(1 - frac)).FillHeight()
		}
	})
}

// staleNote 是读失败但保留旧数据时的提示：已过期的角标加一行原因和旧数据的时间
func (a *app) staleNote(c *ui.Context, reason string, at time.Time) {
	t := c.Theme()
	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		a.badge(c, "已过期", winshell.BadgeWarn)
		mutedText(c, fmt.Sprintf("刷新失败：%s；下面是 %s 的数据", reason, at.Format("15:04:05")))
	})
}

// ───────────── 数字的写法 ─────────────

// fmtCount 带千分位的整数
func fmtCount(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	out := make([]byte, 0, len(s)+len(s)/3)
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// fmtTokens token 数：一万以内写全，往上用"万""亿"
func fmtTokens(n int64) string {
	switch {
	case n >= 100000000:
		return fmt.Sprintf("%.2f 亿", float64(n)/1e8)
	case n >= 10000:
		return fmt.Sprintf("%.1f 万", float64(n)/1e4)
	}
	return fmtCount(n)
}

func fmtPct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// fmtMS 耗时：不到一秒写毫秒，往上写秒
func fmtMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d 毫秒", ms)
	}
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

func fmtBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k*k:
		return fmt.Sprintf("%.2f TB", f/(k*k*k*k))
	case f >= k*k*k:
		return fmt.Sprintf("%.2f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.1f KB", f/k)
	}
	return fmt.Sprintf("%d 字节", n)
}
