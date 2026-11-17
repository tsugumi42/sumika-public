package main

// 右栏的“上下文 / 活动”标签（规格-右栏上下文）：这个会话最近的模型请求，一轮一行，点开看每次请求和每一步的耗时。
// 只有标签看得见时才读库；刷新靠帧里的定时，不起常驻协程。

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/winshell"
)

const sideContextRefresh = 5 * time.Second

// sideContextState 是上下文标签的状态，换会话整份清掉。
type sideContextState struct {
	conv     string
	report   ctlproto.ConvTurnsReport
	has      bool
	err      string
	loading  bool
	due      time.Time
	gen      uint64
	open     map[string]bool
	spans    map[string]ctlproto.SpansReport
	spanBusy map[string]bool
	// 检查器：点开的请求、读到的详情、读失败的原因、正在读的、每个请求当前在哪一页。只在点开时读。
	openReq    map[string]bool
	detail     map[string]ctlproto.RequestDetail
	detailErr  map[string]string
	detailBusy map[string]bool
	page       map[string]*int
	// 聊天里“本轮用量”钮：消息对应哪一轮（assignTurnUsage 算的，报告或消息变了才重算）
	reportVer uint64
	assign    map[string]string
	assignKey string
}

// sideTabsFor 是这个会话右栏有哪几个标签：本地“上下文”，QQ“活动”（ADR-293），都有“资料”；
// 本地私聊再多一个“联系”（主动联系，规格-主动联系 第十节），本地群聊没有；
// 有悄悄话入口的场合（本地群聊、QQ 私聊、QQ 群）多一个“对话日志”（规格-群聊引用和路由 第十五节）。
func sideTabsFor(k api.ConversationKey) []string {
	if k.Platform == "local" {
		if charOfConv(k) != "" {
			return []string{"资料", "上下文", "联系"}
		}
		return []string{"资料", "上下文", "对话日志"}
	}
	return []string{"资料", "活动", "对话日志"}
}

// wbSideTabs 把标签放进右栏顶行的小胶囊，内容跟着换。第 0 个是默认的资料，第 1 个是轮次列表。
func (a *app) wbSideTabs(c *ui.Context, kit *winshell.Kit, sp *winshell.SideSpec) {
	w := &a.workbench.shell
	names := sideTabsFor(a.keyOf(a.sel))
	if w.sideTab >= len(names) {
		w.sideTab = 0
	}
	sp.Tabs = func() { kit.SideTabs(c, "side-tab", &w.sideTab, names) }
	sp.TabKey = names[w.sideTab]
	switch {
	case w.sideTab == 1:
		sp.Content = func() {
			if a.keyOf(a.sel).Platform == "local" {
				a.sideConvSummary(c, kit) // “这个对话”：用量、模型、工作区、审批方式（ADR-351）
			}
			a.sideContextView(c, kit)
		}
	case w.sideTab == sideContactTab && len(names) > sideContactTab && names[sideContactTab] == "联系":
		sp.Content = func() { a.sideContactView(c, kit) }
	case w.sideTab == sideLogTab && len(names) > sideLogTab && names[sideLogTab] == "对话日志":
		sp.Content = func() { a.sideLogView(c, kit) }
	}
}

// sceneName 是场合的叫法（词汇表：本地私聊、本地群聊、QQ 私聊、QQ 群）。
func sceneName(k api.ConversationKey) string {
	switch {
	case k.Platform == "local" && k.Scene == "group":
		return "本地群聊"
	case k.Platform == "local":
		return "本地私聊"
	case k.Scene == "group":
		return "QQ 群"
	}
	return "QQ 私聊"
}

// wbSideFacts 是资料里“这个会话是什么”：名称、场合；本地私聊再写她用的模型。只写后台真有的。
func (a *app) wbSideFacts(cv *conv) []winshell.KeyValue {
	facts := []winshell.KeyValue{{Key: "名称", Value: convName(*cv)}, {Key: "场合", Value: sceneName(cv.Key)}}
	if id := charOfConv(cv.Key); id != "" {
		if ch, ok := a.charOf(id); ok && ch.Model != "" {
			facts = append(facts, winshell.KeyValue{Key: "模型", Value: ch.Model})
		}
	}
	return facts
}

// sideContextView 画轮次列表；顺手看要不要刷新。
func (a *app) sideContextView(c *ui.Context, k *winshell.Kit) {
	g, p := k.G, k.P
	st := a.ensureConvTurns(c)
	if a.keyOf(a.sel).Platform != "local" { // QQ 场合的“活动”也读同一份（原来不分场合都读）
		now := c.Now()
		if !st.loading && !now.Before(st.due) {
			a.sideContextFetch(st, now)
		}
	}
	note := func(key, text string) {
		ui.Text(c.Key(key), text).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).Wrap()
	}
	switch {
	case a.run.src == nil:
		note("context-none", "后台还没起来，这里要等后台打开以后才能看")
		return
	case st.err != "" && !st.has:
		note("context-error", "没读到这个会话的请求记录："+st.err)
		return
	case !st.has:
		return // 很快就到，不闪加载字
	case len(st.report.Turns) == 0:
		note("context-empty", "这个会话还没有请求记录")
		return
	}
	for _, t := range st.report.Turns {
		a.sideTurnRow(c, k, st, t)
	}
	if st.report.Truncated {
		note("context-truncated", "只列最近的，更早的看运行页")
	}
}

func (a *app) sideContextFetch(st *sideContextState, now time.Time) {
	src, conv, gen := a.run.src, st.conv, st.gen
	st.due = now.Add(sideContextRefresh)
	if src == nil || conv == "" {
		return
	}
	st.loading = true
	ctx := a.base()
	go func() {
		rep, err := src.ConvTurns(ctx, conv, ctlproto.ConvTurnsMaxRuns)
		a.post(func() {
			cur := &a.workbench.shell.sideContext
			if cur.gen != gen || cur.conv != conv {
				return
			}
			cur.loading = false
			if err != nil {
				cur.err = err.Error()
				return
			}
			cur.report, cur.has, cur.err = rep, true, ""
			cur.reportVer++
		})
	}()
}

// sideTurnSummary 是一轮的第二行：模型、输入（命中）、输出。
func sideTurnSummary(t ctlproto.ConvTurn) string {
	var in, read, out int64
	model, failed := "", ""
	for _, r := range t.Requests {
		in += r.Input
		read += r.CacheRead
		out += r.Output
		if r.Model != "" {
			model = r.Model
		}
		if r.Failed != "" {
			failed = r.Failed
		}
	}
	text := model
	if in > 0 {
		text += fmt.Sprintf(" · 输入 %s（命中 %s）· 输出 %s", fmtTokens(in), fmtPct(float64(read)/float64(in)), fmtTokens(out))
	}
	if failed != "" {
		text += " · " + failed
	}
	return text
}

// sideTurnTime 是一轮的第一行：时间和用时。进行中不编用时。
func sideTurnTime(t ctlproto.ConvTurn) string {
	at := time.UnixMilli(t.StartedAt).Format("01-02 15:04:05")
	if t.EndedAt == 0 {
		return at + " · 进行中"
	}
	return at + " · " + fmtMS(t.EndedAt-t.StartedAt)
}

func (a *app) sideTurnRow(c *ui.Context, k *winshell.Kit, st *sideContextState, t ctlproto.ConvTurn) {
	g, p := k.G, k.P
	open := st.open[t.ID]
	ui.Column(c.Key("turn-" + t.ID)).FillWidth().Gap(g.SpaceSmall).Children(func() {
		name := ""
		if a.group {
			name = t.Character
			if ch, ok := a.charOf(t.Character); ok && ch.Name != "" {
				name = ch.Name
			}
		}
		row := ui.ButtonBase(c.Key("turn-row")).Label(sideTurnTime(t)).FillWidth().Padding(g.SpaceSmall).Gap(g.SpaceMicro).Radius(g.RadiusSegment).OnClick(func() {
			if st.open == nil {
				st.open = map[string]bool{}
			}
			st.open[t.ID] = !st.open[t.ID]
			if st.open[t.ID] {
				a.sideSpansFetch(st, t.ID)
			}
		})
		switch {
		case row.Pressed():
			row.Background(p.Press)
		case row.Hovered() || open:
			row.Background(p.Hover)
		}
		row.Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(g.SpaceMicro).Children(func() {
				ui.Row(c).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
					ui.Text(c, sideTurnTime(t)).Grow(1).MinWidth(0).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Ink).SingleLine()
					if name != "" {
						ui.Text(c, name).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine()
					}
					switch t.Outcome {
					case "error", "budget_exhausted":
						k.Badge(c, "outcome", turnOutcomeLabel(t.Outcome), winshell.BadgeWarn)
					case "stopped":
						k.Badge(c, "outcome", turnOutcomeLabel(t.Outcome), winshell.BadgeNeutral)
					}
				})
				ui.Text(c, sideTurnSummary(t)).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
			})
		})
		if open {
			a.sideTurnDetail(c, k, st, t)
		}
	})
}

// sideTurnDetail 是点开的一轮：每次请求一行，下面是每一步的耗时横条。
func (a *app) sideTurnDetail(c *ui.Context, k *winshell.Kit, st *sideContextState, t ctlproto.ConvTurn) {
	g, p := k.G, k.P
	ui.Column(c.Key("turn-detail")).FillWidth().Padding(0, g.SpaceSmall).Gap(g.SpaceTight).Children(func() {
		for i, r := range t.Requests {
			a.sideRequestRow(c, k, st, i, r)
		}
		rep, ok := st.spans[t.ID]
		switch {
		case !ok:
		case len(rep.Rows) == 0:
			ui.Text(c.Key("spans-empty"), "这一轮没有耗时记录").FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted)
		default:
			sideSpanBars(c, k, rep.Rows)
		}
	})
}

func (a *app) sideSpansFetch(st *sideContextState, id string) {
	src := a.run.src
	if src == nil || st.spanBusy[id] {
		return
	}
	if st.spanBusy == nil {
		st.spanBusy = map[string]bool{}
	}
	st.spanBusy[id] = true
	gen := st.gen
	ctx := a.base()
	go func() {
		rep, err := src.Spans(ctx, ctlproto.UsageRange{}, ctlproto.StatsArgs{Turn: id})
		a.post(func() {
			cur := &a.workbench.shell.sideContext
			if cur.gen != gen {
				return
			}
			delete(cur.spanBusy, id)
			if err != nil {
				return // 耗时读不到不影响上面的请求列表
			}
			if cur.spans == nil {
				cur.spans = map[string]ctlproto.SpansReport{}
			}
			cur.spans[id] = rep
		})
	}()
}

// sideSpanBars 画一轮的时间线：一步一行，横条按这一轮的时长摆位置。
func sideSpanBars(c *ui.Context, k *winshell.Kit, rows []ctlproto.SpanRow) {
	g, p := k.G, k.P
	start, end := spanRange(rows)
	span := float64(max(end-start, 1))
	for i, r := range rows {
		label := spanLabel(r.Kind, r.Name)
		ui.Column(c.Key(fmt.Sprint("span-", i))).FillWidth().Gap(g.SpaceMicro).Children(func() {
			ui.Row(c).FillWidth().Gap(g.SpaceSmall).Children(func() {
				ui.Text(c, label).Grow(1).MinWidth(0).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine().Tooltip(label)
				ui.Text(c, fmtMS(r.Ms)).Shrink(0).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).FontFeatures("tnum")
			})
			ink := p.Accent
			if r.Outcome == "failed" {
				ink = k.T.C.Danger
			}
			from, width := float32(float64(r.StartedAt-start)/span), float32(float64(r.Ms)/span)
			ui.Box(c.Key("bar")).FillWidth().Height(g.SpaceTight).Radius(g.SpaceTight / 2).Background(p.Bubble).Draw(func(pt *ui.Painter, b ui.Rect) {
				w := max(b.W*width, g.SpaceTight)
				x := min(b.X+b.W*from, b.X+b.W-w)
				pt.Fill(ui.Rect{X: x, Y: b.Y, W: w, H: b.H}, ink, b.H/2)
			})
		})
	}
}

func spanRange(rows []ctlproto.SpanRow) (start, end int64) {
	for i, r := range rows {
		if i == 0 || r.StartedAt < start {
			start = r.StartedAt
		}
		if e := r.StartedAt + r.Ms; e > end {
			end = e
		}
	}
	return start, end
}
