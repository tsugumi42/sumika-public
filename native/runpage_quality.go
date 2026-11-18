package main

// 运行页的质量标签（规格-运行页 第七节）：哪里失败、哪里慢。数据和 sumika-ctl stats quality、stats spans 同一份。
// 排法：顶上一排数字；总表（行可以展开到失败原因）；最慢的环节和每一步的耗时；最近的失败（选一行看详情）。

import (
	"fmt"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/loadui"
	"sumika-native/internal/winshell"
)

// qualityData 是质量标签的一份数据。耗时表、重建表读不到不算这一页失败，空着就行。
type qualityData struct {
	rep     ctlproto.QualityReport
	spans   ctlproto.SpansReport
	rebuild ctlproto.RebuildReport
}

// qualityState 是质量标签几张表的状态。
type qualityState struct {
	main, slow, span, fail, tool, rebuild ui.OutlineState[string]
	selected                              int // 最近的失败选中第几行，-1 是没选
	inited                                bool
}

const qualityTableMaxRows = 12

// spanLabels 是每一步耗时的中文名（规格-回合耗时时间线“记什么”）。
var spanLabels = map[string]string{
	"turn.total":      "整轮",
	"context.images":  "备图",
	"context.plugin":  "插件拼参考信息",
	"context.request": "裁历史、拼请求",
	"model.queue":     "排模型名额",
	"model.wait":      "等第一个字节",
	"model.first":     "等第一段内容",
	"model.stream":    "接收内容",
	"model.retry":     "出错后等重试",
	"tool.prepare":    "工具准备、等审批",
	"tool.queue":      "等工具槽",
	"tool.exec":       "工具执行",
	"tool.save":       "存工具结果",
	"turn.save":       "写库",
	"hook.post_tool":  "钩子（工具后）",
	"hook.stop":       "钩子（结束前）",
}

func spanLabel(kind, name string) string {
	label := spanLabels[kind]
	if label == "" {
		label = kind
	}
	if name != "" {
		return label + " · " + name
	}
	return label
}

// runFetchQuality 读一次质量和耗时（界面线程发起，后台线程查）。规矩和用量一样：有旧数据时不显示加载，读失败留旧的。
func (a *app) runFetchQuality() {
	p := &a.run
	if p.src == nil {
		return
	}
	p.quality.req++
	req, gen, src, ctx := p.quality.req, p.gen, p.src, a.runLife()
	p.quality.loading = true
	p.quality.load.Begin()
	rng, err := a.runRange()
	go func() {
		var data qualityData
		if err == nil {
			data.rep, err = src.Quality(ctx, rng, ctlproto.StatsArgs{})
		}
		if err == nil {
			data.spans, _ = src.Spans(ctx, rng, ctlproto.StatsArgs{})
			data.rebuild, _ = src.Rebuild(ctx, rng)
		}
		a.post(func() {
			if p.gen != gen || p.quality.req != req {
				return
			}
			p.quality.loading = false
			if err != nil {
				p.quality.err = err
				p.quality.load.Fail()
				return
			}
			p.quality.data, p.quality.has, p.quality.err, p.quality.at = data, true, nil, p.clock()
			p.quality.ver++
			p.quality.load.Succeed()
		})
	}()
}

func (a *app) qualityView(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	b := &p.quality
	v := b.load.View(c.Now())
	loadui.Wake(c, v)
	switch {
	case p.src == nil:
		a.runUnavailable(c, "后台还没起来", "质量要等后台打开以后才能看。状态标签里写着原因。")
		return
	case !b.has && b.unavailable():
		a.runUnavailable(c, "质量暂时看不了", b.err.Error())
		return
	case !b.has && b.err != nil:
		if a.errorState(c, "没取到质量", b.err.Error(), "重试") {
			a.runFetchQuality()
		}
		return
	case !b.has || v.Skeleton:
		a.runSkeleton(c, v.Skeleton, true, 6)
		return
	}
	rep := b.data.rep
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(t.Space(4), 0).Gap(t.Space(4)).MaxWidth(1280).Children(func() {
			if b.stale() {
				a.staleNote(c, b.err.Error(), b.at)
			}
			a.qualityToolbar(c)
			if rep.Totals.Runs == 0 && rep.Turns.Total == 0 {
				a.emptyState(c, "inbox", "这段时间没有记录", "换长一点的时间范围看看。", "")
				return
			}
			ui.Row(c).Wrap().Gap(t.Space(8)).GapY(t.Space(3)).Children(func() {
				statCell(c, "请求次数", fmtCount(rep.Totals.Runs), false)
				statCell(c, "失败率", fmtPct(rep.Totals.FailRate), rep.Totals.FailRate > 0)
				statCell(c, "超时", fmtCount(rep.Totals.Timeout), rep.Totals.Timeout > 0)
				statCell(c, "重试", fmtCount(rep.Totals.Retries), false)
				statCell(c, "p50", fmtMS(rep.Totals.P50Ms), false)
				statCell(c, "p95", fmtMS(rep.Totals.P95Ms), false)
			})
			if rep.Unknown.Alarm {
				typoNote(c, fmt.Sprintf("%s 的失败没有分类（%d / %d），该去补分类了", fmtPct(rep.Unknown.Share), rep.Unknown.Count, rep.Unknown.Total), t.Danger)
			}
			if len(rep.Turns.Outcomes) > 0 {
				a.qualityTurns(c, rep.Turns)
			}
			a.qualityMain(c, rep.Rows)
			if len(rep.Slowest) > 0 {
				a.qualitySlowest(c, rep.Slowest)
			}
			if len(b.data.spans.Stats) > 0 {
				a.qualitySpans(c, b.data.spans)
			}
			if len(b.data.rebuild.Places) > 0 {
				a.qualityRebuild(c, b.data.rebuild)
			}
			if len(rep.Tools) > 0 {
				a.qualityTools(c, rep.Tools)
			}
			if len(rep.Recent) > 0 {
				a.qualityRecent(c, rep)
			}
		})
	})
}

func typoNote(c *ui.Context, text string, ink ui.Color) {
	ui.Text(c, text).TextColor(ink).Wrap()
}

func (a *app) qualityToolbar(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	ui.Row(c).Wrap().Gap(t.Space(3)).GapY(t.Space(2)).AlignItems(ui.Center).Children(func() {
		if a.runRangePick(c) {
			a.runFetchQuality()
		}
		if a.ghostButton(c, "刷新") {
			a.runFetchQuality()
		}
		if p.quality.load.View(c.Now()).Refreshing {
			mutedText(c, "刷新中…")
		} else if !p.quality.at.IsZero() {
			mutedText(c, "更新于 "+p.quality.at.Format("15:04:05"))
		}
	})
}

// qualityTurns 是回合结局：一排徽标。
func (a *app) qualityTurns(c *ui.Context, turns ctlproto.QualityTurns) {
	t := c.Theme()
	ui.Row(c).Wrap().Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		mutedText(c, fmt.Sprintf("%s 轮，耗时 p50 %s、p95 %s", fmtCount(turns.Total), fmtMS(turns.P50Ms), fmtMS(turns.P95Ms)))
		for _, o := range turns.Outcomes {
			tone := winshell.BadgeNeutral
			switch o.Outcome {
			case "done":
				tone = winshell.BadgeGood
			case "error", "budget_exhausted":
				tone = winshell.BadgeWarn
			}
			a.badge(c, fmt.Sprintf("%s %s", turnOutcomeLabel(o.Outcome), fmtCount(o.Count)), tone)
		}
	})
}

func turnOutcomeLabel(s string) string {
	switch s {
	case "done":
		return "完成"
	case "stopped":
		return "被停下"
	case "error":
		return "出错"
	case "budget_exhausted":
		return "预算用完"
	}
	return s
}

// qualityTable 是这一页几张表共用的外框：标题加一张 OutlineTable，高度按行数，最多 qualityTableMaxRows 行。
func qualityTable(c *ui.Context, key, title string, st *ui.OutlineState[string], cols []ui.TableColumn, roots []string, kids func(string) []string, cell func(string, int)) {
	t := c.Theme()
	n := st.Rows()
	if n == 0 {
		n = len(roots)
	}
	h := float32(min(n, qualityTableMaxRows)+1)*(t.Space(8)+2) + 2 // 一行实际比 Space(8) 高一点（真窗口截图量得），按 Space(8) 算最后一行会被裁
	ui.Column(c.Key(key)).FillWidth().Gap(t.Space(2)).Children(func() {
		ui.Text(c, title).Bold()
		ui.Column(c).FillWidth().Border(1, t.Border).Radius(t.Radius).Clip().Children(func() {
			ui.OutlineTable(c, st, cols, roots, kids, cell).Height(h)
		})
	})
}

func noKids(string) []string { return nil }

// qualityMain 是总表：每类任务和模块一行，展开到失败原因。
func (a *app) qualityMain(c *ui.Context, rows []ctlproto.QualityRow) {
	type line struct {
		row    *ctlproto.QualityRow
		reason *ctlproto.QualityReason
	}
	lines := map[string]line{}
	kids := map[string][]string{}
	var roots []string
	for i := range rows {
		r := &rows[i]
		k := r.Kind + ":" + r.Key
		roots = append(roots, k)
		lines[k] = line{row: r}
		for j := range r.Reasons {
			child := fmt.Sprint(k, "#", j)
			lines[child] = line{reason: &r.Reasons[j]}
			kids[k] = append(kids[k], child)
		}
	}
	cols := []ui.TableColumn{
		{Title: "任务", ID: "name", MinWidth: 160},
		{Title: "次数", ID: "runs", Width: 70, Align: ui.End},
		{Title: "成功", ID: "ok", Width: 70, Align: ui.End},
		{Title: "失败", ID: "failed", Width: 70, Align: ui.End},
		{Title: "超时", ID: "timeout", Width: 70, Align: ui.End},
		{Title: "重试", ID: "retries", Width: 70, Align: ui.End},
		{Title: "失败率", ID: "rate", Width: 80, Align: ui.End},
		{Title: "p50", ID: "p50", Width: 84, Align: ui.End},
		{Title: "p95", ID: "p95", Width: 84, Align: ui.End},
	}
	qualityTable(c, "quality-main", "总表", &a.run.qualityTbl.main, cols, roots, func(k string) []string { return kids[k] }, func(k string, col int) {
		t := c.Theme()
		l := lines[k]
		if l.reason != nil {
			switch col {
			case 0:
				ui.Text(c, l.reason.Class).SingleLine().Tooltip(l.reason.Class)
			case 1:
				numText(c, fmtCount(l.reason.Count))
			case 6:
				numText(c, fmtPct(l.reason.Share))
			}
			return
		}
		r := l.row
		if !r.Wired && r.Runs == 0 {
			switch col {
			case 0:
				ui.Text(c, r.Label).SingleLine()
			case 1:
				mutedText(c, "未接入")
			}
			return
		}
		switch col {
		case 0:
			ui.Text(c, r.Label).SingleLine().Tooltip(r.Label)
		case 1:
			numText(c, fmtCount(r.Runs))
		case 2:
			numText(c, fmtCount(r.OK))
		case 3:
			if e := numText(c, fmtCount(r.Failed)); r.Failed > 0 {
				e.TextColor(t.Danger)
			}
		case 4:
			if e := numText(c, fmtCount(r.Timeout)); r.Timeout > 0 {
				e.TextColor(t.Danger)
			}
		case 5:
			numText(c, fmtCount(r.Retries))
		case 6:
			numText(c, fmtPct(r.FailRate))
		case 7:
			qualityMS(c, r.P50Ms, r.Samples)
		case 8:
			qualityMS(c, r.P95Ms, r.Samples)
		}
	})
}

// qualityMS 写耗时；一个成功的样本都没有时写横线，不写 0。
func qualityMS(c *ui.Context, ms, samples int64) {
	if samples == 0 {
		mutedText(c, "—")
		return
	}
	numText(c, fmtMS(ms))
}

// qualitySlowest 是最慢的环节：样本太少的灰显，别被个别请求误导。
func (a *app) qualitySlowest(c *ui.Context, rows []ctlproto.QualitySlow) {
	byKey := map[string]ctlproto.QualitySlow{}
	var roots []string
	for i, r := range rows {
		k := fmt.Sprint(i)
		byKey[k] = r
		roots = append(roots, k)
	}
	cols := []ui.TableColumn{
		{Title: "环节", ID: "name", MinWidth: 200},
		{Title: "样本", ID: "samples", Width: 80, Align: ui.End},
		{Title: "p50", ID: "p50", Width: 90, Align: ui.End},
		{Title: "p95", ID: "p95", Width: 90, Align: ui.End},
	}
	qualityTable(c, "quality-slow", "最慢的环节", &a.run.qualityTbl.slow, cols, roots, noKids, func(k string, col int) {
		t := c.Theme()
		r := byKey[k]
		var e ui.Element
		switch col {
		case 0:
			label := r.Label
			if r.Model != "" {
				label += " · " + r.Model
			}
			e = ui.Text(c, label).SingleLine().Tooltip(label)
		case 1:
			e = numText(c, fmtCount(r.Samples))
		case 2:
			e = numText(c, fmtMS(r.P50Ms))
		case 3:
			e = numText(c, fmtMS(r.P95Ms))
		}
		if r.Thin {
			e.TextColor(t.TextMuted)
		}
	})
}

// qualitySpans 是一轮里每一步花的时间，按合计排（规格-回合耗时时间线）。
func (a *app) qualitySpans(c *ui.Context, rep ctlproto.SpansReport) {
	byKey := map[string]ctlproto.SpanStat{}
	var roots []string
	for i, s := range rep.Stats {
		k := fmt.Sprint(i)
		byKey[k] = s
		roots = append(roots, k)
	}
	cols := []ui.TableColumn{
		{Title: "步骤", ID: "name", MinWidth: 220},
		{Title: "次数", ID: "count", Width: 80, Align: ui.End},
		{Title: "合计", ID: "total", Width: 90, Align: ui.End},
		{Title: "p50", ID: "p50", Width: 90, Align: ui.End},
		{Title: "p95", ID: "p95", Width: 90, Align: ui.End},
		{Title: "最长", ID: "max", Width: 90, Align: ui.End},
	}
	title := "每一步的耗时"
	if rep.Turns > 0 {
		title = fmt.Sprintf("每一步的耗时（%s 轮）", fmtCount(rep.Turns))
	}
	qualityTable(c, "quality-spans", title, &a.run.qualityTbl.span, cols, roots, noKids, func(k string, col int) {
		s := byKey[k]
		switch col {
		case 0:
			label := spanLabel(s.Kind, s.Name)
			ui.Text(c, label).SingleLine().Tooltip(label)
		case 1:
			numText(c, fmtCount(s.Count))
		case 2:
			numText(c, fmtMS(s.TotalMs))
		case 3:
			numText(c, fmtMS(s.P50Ms))
		case 4:
			numText(c, fmtMS(s.P95Ms))
		case 5:
			numText(c, fmtMS(s.MaxMs))
		}
	})
}

// qualityRebuild 是重建留痕（规格-提示词前缀稳定第八节）：每个场合一行，展开看每天；数据和 sumika-ctl stats rebuild 同一份。
func (a *app) qualityRebuild(c *ui.Context, rep ctlproto.RebuildReport) {
	byKey := map[string]ctlproto.RebuildRow{}
	kids := map[string][]string{}
	var roots []string
	for _, p := range rep.Places {
		k := "place:" + p.Place
		byKey[k] = p
		roots = append(roots, k)
	}
	for i, d := range rep.Rows {
		k := fmt.Sprint("day:", d.Place, ":", d.Date, "#", i)
		byKey[k] = d
		parent := "place:" + d.Place
		kids[parent] = append(kids[parent], k)
	}
	cols := []ui.TableColumn{
		{Title: "场合 / 日期", ID: "name", MinWidth: 140},
		{Title: "重建", ID: "n", Width: 70, Align: ui.End},
		{Title: "平均丢", ID: "avg", Width: 80, Align: ui.End},
		{Title: "最多丢", ID: "max", Width: 80, Align: ui.End},
		{Title: "摘要覆盖", ID: "covered", Width: 90, Align: ui.End},
		{Title: "原因", ID: "why", MinWidth: 220},
	}
	qualityTable(c, "quality-rebuild", "重建（上下文从库里重新拼）", &a.run.qualityTbl.rebuild, cols, roots, func(k string) []string { return kids[k] }, func(k string, col int) {
		r := byKey[k]
		switch col {
		case 0:
			label := r.Place
			if r.Date != "" {
				label = r.Date
			}
			ui.Text(c, label).SingleLine().Tooltip(label)
		case 1:
			numText(c, fmtCount(r.Rebuilds))
		case 2:
			if r.Counted == 0 {
				numText(c, "—")
			} else {
				numText(c, fmt.Sprintf("%.1f 条", r.DroppedAvg))
			}
		case 3:
			if r.Counted == 0 {
				numText(c, "—")
			} else {
				numText(c, fmtCount(r.DroppedMax)+" 条")
			}
		case 4:
			numText(c, fmtCount(r.Covered))
		case 5:
			why := rebuildWhy(r)
			ui.Text(c, why).SingleLine().Tooltip(why)
		}
	})
}

// rebuildWhy 是一行里最常见的几个原因（最多三个），加上旧记录、没数出来的说明
func rebuildWhy(r ctlproto.RebuildRow) string {
	var parts []string
	for i, c := range r.Reasons {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s", ctlproto.RebuildReasonLabel(c.Reason), fmtCount(c.Count)))
	}
	if r.Untraced > 0 {
		parts = append(parts, fmt.Sprintf("没记原因的旧记录 %s", fmtCount(r.Untraced)))
	}
	if r.NotCounted > 0 {
		parts = append(parts, fmt.Sprintf("没数出条数 %s", fmtCount(r.NotCounted)))
	}
	return strings.Join(parts, "，")
}

// qualityTools 是每个工具的成败。
func (a *app) qualityTools(c *ui.Context, rows []ctlproto.QualityTool) {
	byKey := map[string]ctlproto.QualityTool{}
	var roots []string
	for _, r := range rows {
		byKey[r.Name] = r
		roots = append(roots, r.Name)
	}
	cols := []ui.TableColumn{
		{Title: "工具", ID: "name", MinWidth: 180},
		{Title: "次数", ID: "runs", Width: 70, Align: ui.End},
		{Title: "成功", ID: "done", Width: 70, Align: ui.End},
		{Title: "失败", ID: "failed", Width: 70, Align: ui.End},
		{Title: "没做完", ID: "incomplete", Width: 80, Align: ui.End},
		{Title: "中位耗时", ID: "median", Width: 90, Align: ui.End},
	}
	qualityTable(c, "quality-tools", "工具", &a.run.qualityTbl.tool, cols, roots, noKids, func(k string, col int) {
		t := c.Theme()
		r := byKey[k]
		switch col {
		case 0:
			ui.Text(c, r.Name).SingleLine().Tooltip(r.Name)
		case 1:
			numText(c, fmtCount(r.Runs))
		case 2:
			numText(c, fmtCount(r.Done))
		case 3:
			if e := numText(c, fmtCount(r.Failed)); r.Failed > 0 {
				e.TextColor(t.Danger)
			}
		case 4:
			numText(c, fmtCount(r.Incomplete))
		case 5:
			qualityMS(c, r.MedianMs, r.Samples)
		}
	})
}

// qualityRecent 是最近的失败：选一行，详情写在表下面。
func (a *app) qualityRecent(c *ui.Context, rep ctlproto.QualityReport) {
	st := &a.run.qualityTbl
	if !st.inited {
		st.selected, st.inited = -1, true
	}
	st.fail.List.Selected = &st.selected
	roots := make([]string, len(rep.Recent))
	for i, f := range rep.Recent {
		roots[i] = fmt.Sprint(f.AtMS, "-", f.ID, "-", len(rep.Recent)-i) // 键稳定：刷新后选中的还是那一条
	}
	byKey := func(k string) ctlproto.QualityFailure {
		for i, r := range roots {
			if r == k {
				return rep.Recent[i]
			}
		}
		return ctlproto.QualityFailure{}
	}
	cols := []ui.TableColumn{
		{Title: "时间", ID: "at", Width: 170},
		{Title: "任务", ID: "task", MinWidth: 140},
		{Title: "原因", ID: "class", Width: 150},
		{Title: "场合", ID: "place", Width: 100},
		{Title: "耗时", ID: "ms", Width: 90, Align: ui.End},
	}
	title := fmt.Sprintf("最近的失败（共 %s 条）", fmtCount(rep.RecentOf))
	if k := a.settingsKit(); k != nil { // 选中行用中性底，不是整条强调色
		old := c.Theme()
		c.SetTheme(k.MenuTheme())
		defer c.SetTheme(old)
	}
	qualityTable(c, "quality-recent", title, &st.fail, cols, roots, noKids, func(k string, col int) {
		f := byKey(k)
		switch col {
		case 0:
			numText(c, time.UnixMilli(f.AtMS).Format("01-02 15:04:05"))
		case 1:
			label := f.Label
			if f.Model != "" {
				label += " · " + f.Model
			}
			ui.Text(c, label).SingleLine().Tooltip(label)
		case 2:
			ui.Text(c, f.Class).SingleLine().Tooltip(f.Class)
		case 3:
			ui.Text(c, f.Place).SingleLine()
		case 4:
			if f.Ms < 0 {
				mutedText(c, "—")
			} else {
				numText(c, fmtMS(f.Ms))
			}
		}
	})
	if i := st.selected; i >= 0 && i < len(rep.Recent) {
		a.qualityFailureDetail(c, rep.Recent[i])
	}
}

func (a *app) qualityFailureDetail(c *ui.Context, f ctlproto.QualityFailure) {
	items := []display.KeyValuePair{{Key: "结果", Value: f.Outcome}, {Key: "原因", Value: f.Class}}
	add := func(k, v string) {
		if v != "" {
			items = append(items, display.KeyValuePair{Key: k, Value: v})
		}
	}
	add("说明", f.Summary)
	add("模型", f.Model)
	add("实际线路", strings.TrimSpace(f.RouteProvider+" "+f.RouteModel))
	add("回合", f.TurnID)
	if f.Attempts > 1 {
		add("尝试次数", fmt.Sprint(f.Attempts))
	}
	a.keyValues(c, "quality-failure-detail", items)
}
