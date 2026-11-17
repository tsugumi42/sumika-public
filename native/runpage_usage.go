package main

// 用量标签（规格-运行页 第六节）：顶部时间范围和筛选，概览条，按任务类别的主表（行可展开到按模型），按天或按周的次表。
// 数据全来自 api.StatsReader.Usage：类别的"未接入"、识图的"Go 侧未接入"都由读服务的 Wired 字段说了算，这里不另猜。
// 已知缺口（规格第十二节第 3 步段落有登记）：读数没有"角色"这一维，所以没有按角色筛选；按天、按周只有合计没有按类别，
// 所以类别、模型筛选只管主表和概览，次表不跟着变。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/loadui"
)

const (
	usageTableMaxRows = 12 // 主表最多这么多行高，再多在表里滚
	dayRowH           = 28
)

// usageRow 是主表里的一行：一个类别，或类别下的一个模型
type usageRow struct {
	label    string
	note     string // 名字后面的小字：未知类别的原文、"Go 侧未接入"
	st       ctlproto.UsageStats
	reserved bool // 预留类别：没有记录，写"未接入"，不写 0
	kids     []string
}

// usageRows 按筛选算出主表的各行；roots 是排好序的类别键，预留行沉底，不参与排序
func (p *runPage) usageRows(rep ctlproto.UsageReport) (rows map[string]*usageRow, roots []string) {
	rows = map[string]*usageRow{}
	var live, reserved []string
	for _, t := range rep.Tasks {
		if p.taskPick != "" && t.Key != p.taskPick {
			continue
		}
		r := &usageRow{label: t.Label, st: t.UsageStats}
		if t.Key == ctlproto.TaskUnknown && t.Raw != "" {
			r.note = t.Raw
		}
		if !t.Wired && t.Runs > 0 {
			r.note = "Go 侧未接入"
		}
		if p.modelPick != "" { // 只留这个模型的那一部分
			r.st = ctlproto.UsageStats{}
			for _, m := range t.Models {
				if m.Model == p.modelPick {
					r.st = m.UsageStats
				}
			}
			if r.st.Runs == 0 {
				continue
			}
		} else {
			for _, m := range t.Models {
				k := t.Key + "/" + m.Model
				rows[k] = &usageRow{label: m.Model, st: m.UsageStats}
				r.kids = append(r.kids, k)
			}
			sort.SliceStable(r.kids, func(i, j int) bool { return rows[r.kids[i]].st.Input > rows[r.kids[j]].st.Input })
		}
		if !t.Wired && t.Runs == 0 {
			r.reserved = true
			reserved = append(reserved, t.Key)
		} else {
			live = append(live, t.Key)
		}
		rows[t.Key] = r
	}
	col, desc := p.usageTbl.sort.Column, p.usageTbl.sort.Descending
	val := func(k string) int64 {
		s := rows[k].st
		switch col {
		case "runs":
			return s.Runs
		case "output":
			return s.Output
		case "reasoning":
			return s.Reasoning
		case "avg":
			return s.AvgMs
		case "failed":
			return s.Failed
		}
		return s.Input
	}
	sort.SliceStable(live, func(i, j int) bool {
		if col == "name" {
			if desc {
				return rows[live[i]].label > rows[live[j]].label
			}
			return rows[live[i]].label < rows[live[j]].label
		}
		if desc {
			return val(live[i]) > val(live[j])
		}
		return val(live[i]) < val(live[j])
	})
	return rows, append(live, reserved...)
}

// usageModel 是主表和概览条要的聚合结果。数据、筛选、排序没变就不重算：这些在 build 里每帧做是白费
// （聚合、排序、分组都只依赖读到的数据），所以数据到达时（ver 变了）或筛选、排序变了才算一次，build 里只读结果。
type usageModel struct {
	ok          bool
	ver         uint64
	task, model string
	sort        ui.SortOrder
	rows        map[string]*usageRow
	roots       []string
	tot         ctlproto.UsageStats
	dayMax      int64 // 按天的最大输入，次表的条长按它
	weekMax     int64
	builds      int // 算了几次：测试用它断言 build 里没有每帧重算
}

func (p *runPage) usageModelOf(rep ctlproto.UsageReport) *usageModel {
	m := &p.usageTbl.cache
	if m.ok && m.ver == p.usage.ver && m.task == p.taskPick && m.model == p.modelPick && m.sort == p.usageTbl.sort {
		return m
	}
	builds := m.builds + 1
	*m = usageModel{ok: true, ver: p.usage.ver, task: p.taskPick, model: p.modelPick, sort: p.usageTbl.sort, builds: builds}
	m.rows, m.roots = p.usageRows(rep)
	m.tot = rep.Totals
	if p.taskPick != "" || p.modelPick != "" {
		m.tot = ctlproto.UsageStats{}
		for _, k := range m.roots {
			addStats(&m.tot, m.rows[k].st)
		}
		finishStats(&m.tot)
	}
	for _, b := range rep.Days {
		m.dayMax = max(m.dayMax, b.Input)
	}
	for _, b := range rep.Weeks {
		m.weekMax = max(m.weekMax, b.Input)
	}
	return m
}

func (a *app) usageView(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	b := &p.usage
	if p.usageTbl.sort.Column == "" {
		p.usageTbl.sort = ui.SortOrder{Column: "input", Descending: true}
	}
	v := b.load.View(c.Now())
	loadui.Wake(c, v)
	switch {
	case p.src == nil:
		a.runUnavailable(c, "后台还没起来", "用量要等后台打开以后才能看。状态标签里写着原因。")
		return
	case !b.has && b.unavailable():
		a.runUnavailable(c, "用量暂时看不了", b.err.Error())
		return
	case !b.has && b.err != nil:
		retry := a.errorState(c, "没取到用量", b.err.Error(), "重试")
		if retry {
			a.runFetchUsage()
		}
		return
	case !b.has || v.Skeleton: // 慢了才出骨架；骨架出了以后数据早到也留够时间再换（loadstate）
		a.runSkeleton(c, v.Skeleton, true, 6)
		return
	}
	rep := b.data
	model := p.usageModelOf(rep)
	rows, roots, tot := model.rows, model.roots, model.tot
	root := ui.Scroll(c).Grow(1)
	root.Children(func() {
		col := ui.Column(c).Padding(t.Space(4), 0).Gap(t.Space(4)).MaxWidth(1280)
		narrow := false
		if w := col.LayoutBounds().W; w > 0 && w < runWide {
			narrow = true
		}
		col.Children(func() {
			if b.stale() {
				a.staleNote(c, b.err.Error(), b.at)
			}
			a.usageToolbar(c, rep)
			if rep.Totals.Runs == 0 {
				label, actionLabel := "这段时间没有记录", "改成 30 天"
				if p.rangeKey == "30d" || p.rangeKey == "all" {
					actionLabel = ""
				}
				act := a.emptyState(c, "inbox", label, "换长一点的时间范围看看；没有任何模型请求时这里就是空的。", actionLabel)
				if act {
					p.rangeKey = "30d"
					a.runFetchUsage()
				}
				return
			}
			ui.Row(c).Wrap().Gap(t.Space(8)).GapY(t.Space(3)).Children(func() {
				statCell(c, "总输入", fmtTokens(tot.Input), false)
				statCell(c, "缓存命中率", fmtPct(tot.Hit), false)
				statCell(c, "总输出", fmtTokens(tot.Output), false)
				statCell(c, "请求次数", fmtCount(tot.Runs), false)
				statCell(c, "失败次数", fmtCount(tot.Failed), tot.Failed > 0)
				statCell(c, "没拿到用量", fmtCount(tot.NoUsage), tot.NoUsage > 0)
			})
			panels := func(main, side func()) {
				if narrow {
					ui.Column(c).Gap(t.Space(4)).Children(func() { main(); side() })
					return
				}
				ui.Row(c).Gap(t.Space(4)).AlignItems(ui.Start).Children(func() {
					ui.Column(c).Grow(3).Basis(0).MinWidth(0).Children(main)
					ui.Column(c).Grow(2).Basis(0).MinWidth(0).Children(side)
				})
			}
			panels(func() { a.usageTable(c, rows, roots, tot) }, func() { a.usageBuckets(c, rep) })
		})
	})
}

// runUnavailable 是"不可用"：不是出错，写原因，不给重试
func (a *app) runUnavailable(c *ui.Context, title, reason string) {
	a.emptyState(c, "circle-slash", title, reason, "")
}

// runRangePick 是时间范围下拉，用量、质量两个标签共用一个选择。返回这一帧换了没有。
func (a *app) runRangePick(c *ui.Context) bool {
	p := &a.run
	keys, labels := make([]string, len(runRanges)), make([]string, len(runRanges))
	cur := p.rangeKey
	if cur == "" {
		cur = "7d"
	}
	for i, r := range runRanges {
		keys[i], labels[i] = r.Key, r.Label
	}
	if !a.pickDrop(c, "时间范围", keys, labels, &cur, false) {
		return false
	}
	p.rangeKey = cur
	return true
}

// usageToolbar 是顶部一行：时间范围、类别和模型筛选、按天按周、刷新（下拉用 memoryview.go 的 pickDrop：键翻成标签，标签要两两不同）
func (a *app) usageToolbar(c *ui.Context, rep ctlproto.UsageReport) {
	t := c.Theme()
	p := &a.run
	ui.Row(c).Wrap().Gap(t.Space(3)).GapY(t.Space(2)).AlignItems(ui.Center).Children(func() {
		if a.runRangePick(c) {
			a.runFetchUsage()
		}
		tk, tl := []string{""}, []string{"全部类别"}
		seen := map[string]bool{}
		var models []string
		for _, tsk := range rep.Tasks {
			if tsk.Runs > 0 || tsk.Wired {
				tk, tl = append(tk, tsk.Key), append(tl, tsk.Label)
			}
			for _, m := range tsk.Models {
				if !seen[m.Model] {
					seen[m.Model] = true
					models = append(models, m.Model)
				}
			}
		}
		if p.taskPick != "" && !contains(tk, p.taskPick) {
			p.taskPick = ""
		}
		a.pickDrop(c, "任务类别", tk, uniqLabels(tl), &p.taskPick, false)
		sort.Strings(models)
		mk, ml := append([]string{""}, models...), append([]string{"全部模型"}, models...)
		if p.modelPick != "" && !contains(mk, p.modelPick) {
			p.modelPick = ""
		}
		a.pickDrop(c, "模型", mk, uniqLabels(ml), &p.modelPick, false)
		sel := 0
		if p.byWeek {
			sel = 1
		}
		a.segmentedChoice(c, "run-usage-group", "次表的分组", []string{"按天", "按周"}, &sel)
		p.byWeek = sel == 1
		if a.ghostButton(c, "刷新") {
			a.runFetchUsage()
		}
		if p.usage.load.View(c.Now()).Refreshing {
			mutedText(c, "刷新中…")
		} else if !p.usage.at.IsZero() {
			mutedText(c, "更新于 "+p.usage.at.Format("15:04:05"))
		}
	})
}

func (a *app) usageTable(c *ui.Context, rows map[string]*usageRow, roots []string, tot ctlproto.UsageStats) {
	t := c.Theme()
	p := &a.run
	tbl := &p.usageTbl
	cols := []ui.TableColumn{
		{Title: "类别", ID: "name", MinWidth: 140, Sortable: true},
		{Title: "次数", ID: "runs", Width: 80, Align: ui.End, Sortable: true},
		{Title: "输入", ID: "input", Width: 150, Align: ui.End, Sortable: true},
		{Title: "输出", ID: "output", Width: 84, Align: ui.End, Sortable: true},
		{Title: "思考", ID: "reasoning", Width: 84, Align: ui.End, Sortable: true},
		{Title: "平均耗时", ID: "avg", Width: 96, Align: ui.End, Sortable: true},
		{Title: "失败", ID: "failed", Width: 70, Align: ui.End, Sortable: true},
	}
	tbl.out.List.Sort = &tbl.sort
	n := tbl.out.Rows()
	if n == 0 {
		n = len(roots)
	}
	h := float32(min(n, usageTableMaxRows)+1)*t.Space(8) + 2
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(t.Radius).Clip().Children(func() {
		ui.OutlineTable(c, &tbl.out, cols, roots, func(k string) []string { return rows[k].kids }, func(k string, col int) {
			r := rows[k]
			s := r.st
			switch col {
			case 0:
				ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, r.label).SingleLine().Tooltip(r.label)
					if r.note != "" {
						ui.Text(c, r.note).TextColor(t.TextMuted).FontSize(t.Rem(0.85)).SingleLine()
					}
				})
			case 1:
				if r.reserved {
					mutedText(c, "未接入")
				} else {
					numText(c, fmtCount(s.Runs))
				}
			case 2:
				if r.reserved {
					return
				}
				ui.Row(c).Gap(t.Space(1.5)).AlignItems(ui.Center).Children(func() {
					numText(c, fmtTokens(s.Input))
					if s.Input > 0 {
						ui.Text(c, "命中 "+fmtPct(s.Hit)).TextColor(t.TextMuted).FontSize(t.Rem(0.8)).FontFeatures("tnum").SingleLine()
					}
				})
			case 3:
				if !r.reserved {
					numText(c, fmtTokens(s.Output))
				}
			case 4:
				if !r.reserved {
					numText(c, fmtTokens(s.Reasoning))
				}
			case 5:
				if r.reserved {
					return
				}
				if s.OKRuns == 0 {
					mutedText(c, "—")
				} else {
					numText(c, fmtMS(s.AvgMs))
				}
			case 6:
				if r.reserved {
					return
				}
				e := numText(c, fmtCount(s.Failed))
				if s.Failed > 0 {
					e.TextColor(t.Danger)
				}
			}
		}).Height(h)
		var notes []string
		if tot.NoUsage > 0 {
			notes = append(notes, fmt.Sprintf("%d 次没拿到用量，未计入", tot.NoUsage))
		}
		notes = append(notes, "耗时只算成功的请求；输出已包含思考")
		ui.Box(c).FillWidth().Padding(t.Space(2), t.Space(3)).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			mutedText(c, strings.Join(notes, "；")).FontSize(t.Rem(0.85))
		})
	})
}

// usageBuckets 是次表：一行一天（或一周），横向小条的长度按当天输入量，旁边写数值。新的在上
func (a *app) usageBuckets(c *ui.Context, rep ctlproto.UsageReport) {
	t := c.Theme()
	p := &a.run
	bs := rep.Days
	title, unit := "按天", "天"
	if p.byWeek {
		bs, title, unit = rep.Weeks, "按周", "周"
	}
	max := p.usageTbl.cache.dayMax
	if p.byWeek {
		max = p.usageTbl.cache.weekMax
	}
	n := len(bs)
	at := func(i int) ctlproto.UsageBucket { return bs[n-1-i] }
	p.dayList.Key = func(i int) any { return at(i).Date }
	h := float32(min(maxInt(n, 4), 10))*dayRowH + t.Space(2)
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(t.Radius).Clip().Children(func() {
		ui.Row(c).FillWidth().Padding(t.Space(2), t.Space(3)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).Bold()
			mutedText(c, fmt.Sprintf("%d %s有记录，条的长度按输入量", n, unit)).FontSize(t.Rem(0.85))
		})
		ui.List(c.Key("usage-buckets"), &p.dayList, n, func(i int) {
			b := at(i)
			label := b.Date[5:]
			if p.byWeek {
				label += " 起"
			}
			tip := fmt.Sprintf("%s：输入 %s，输出 %s，%d 次，失败 %d", b.Date, fmtTokens(b.Input), fmtTokens(b.Output), b.Runs, b.Failed)
			ui.Row(c).FillWidth().Height(dayRowH).Padding(0, t.Space(3)).Gap(t.Space(3)).AlignItems(ui.Center).Tooltip(tip).Children(func() {
				ui.Text(c, label).FontFeatures("tnum").TextColor(t.TextMuted).Width(t.Space(18)).Shrink(0).SingleLine()
				ui.Box(c).Grow(1).MinWidth(0).Children(func() {
					frac := 0.0
					if max > 0 {
						frac = float64(b.Input) / float64(max)
					}
					barOf(c, frac, 8)
				})
				ui.Text(c, fmtTokens(b.Input)+" · "+fmtCount(b.Runs)+" 次").FontFeatures("tnum").Width(t.Space(30)).Shrink(0).TextAlign(ui.End).SingleLine()
			})
		}).Height(h)
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
