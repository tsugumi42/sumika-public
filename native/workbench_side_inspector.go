package main

// 右栏“上下文 / 活动”里的请求检查器（规格-右栏上下文 第二刀）：点开一轮的详情，每次请求再点开，
// 分页看系统提示词、注入的上下文、工具表、用量。详情只在点开时按请求 id 读一次，列表查询不碰这些大文本。
// 这个视图只在主窗口里，不往任何平台发东西；QQ 场合的“活动”标签走同一个视图。

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/runstats"
	"sumika-native/internal/winshell"
)

// inspectorPages 是检查器的四页。
var inspectorPages = []string{"系统提示词", "注入", "工具表", "用量"}

// sideRequestRow 是一次请求：一行摘要，点开在下面（同级，不嵌在可点的行里）显示检查器。
// 没有请求 id 的旧行不能点开，只写摘要。
func (a *app) sideRequestRow(c *ui.Context, k *winshell.Kit, st *sideContextState, i int, r ctlproto.ConvRequest) {
	g, p := k.G, k.P
	text := fmt.Sprintf("请求 %d · %s · %s", i+1, r.Model, fmtMS(r.TotalMs))
	if r.TTFTMs > 0 {
		text += " · 首字 " + fmtMS(r.TTFTMs)
	}
	if r.Failed != "" {
		text += " · " + r.Failed
	}
	key := fmt.Sprint("request-", i)
	if r.ID == "" {
		ui.Text(c.Key(key), text).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
		return
	}
	open := st.openReq[r.ID]
	ui.Column(c.Key(key)).FillWidth().Gap(g.SpaceTight).Children(func() {
		row := ui.ButtonBase(c.Key("request-row")).Label(text).FillWidth().Padding(g.SpaceTight, g.SpaceSmall).Radius(g.RadiusSegment).OnClick(func() {
			if st.openReq == nil {
				st.openReq = map[string]bool{}
			}
			st.openReq[r.ID] = !st.openReq[r.ID]
			if st.openReq[r.ID] {
				a.sideDetailFetch(st, r.ID)
			}
		})
		switch {
		case row.Pressed():
			row.Background(p.Press)
		case row.Hovered() || open:
			row.Background(p.Hover)
		}
		row.Children(func() {
			ui.Text(c, text).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
		})
		if open {
			a.sideRequestDetail(c, k, st, r.ID)
		}
	})
}

// sideDetailFetch 读一次请求的详情；已经读到或正在读就不再读。收起再点开不重读（记录不会变）。
func (a *app) sideDetailFetch(st *sideContextState, id string) {
	src := a.run.src
	if src == nil || st.detailBusy[id] {
		return
	}
	if _, ok := st.detail[id]; ok {
		return
	}
	if st.detailBusy == nil {
		st.detailBusy = map[string]bool{}
	}
	st.detailBusy[id] = true
	delete(st.detailErr, id)
	gen := st.gen
	ctx := a.base()
	go func() {
		d, err := src.RequestDetail(ctx, id)
		a.post(func() {
			cur := &a.workbench.shell.sideContext
			if cur.gen != gen {
				return
			}
			delete(cur.detailBusy, id)
			if err != nil {
				if cur.detailErr == nil {
					cur.detailErr = map[string]string{}
				}
				if errors.Is(err, runstats.ErrNoRequest) {
					cur.detailErr[id] = "这次请求的记录已经不在了"
				} else {
					cur.detailErr[id] = "没读到这次请求的详情：" + err.Error()
				}
				return
			}
			if cur.detail == nil {
				cur.detail = map[string]ctlproto.RequestDetail{}
			}
			cur.detail[id] = d
		})
	}()
}

// sideRequestDetail 画检查器：四页的分段选择，下面是当前页。内容在右栏的滚动区里一起滚。
func (a *app) sideRequestDetail(c *ui.Context, k *winshell.Kit, st *sideContextState, id string) {
	g, p := k.G, k.P
	ui.Column(c.Key("inspector")).FillWidth().Padding(g.SpaceSmall, 0).Gap(g.SpaceSmall).Children(func() {
		d, ok := st.detail[id]
		switch {
		case st.detailErr[id] != "":
			k.Note(c, st.detailErr[id], true)
			return
		case !ok:
			ui.Text(c.Key("loading"), "正在读").FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted)
			return
		}
		if st.page == nil {
			st.page = map[string]*int{}
		}
		page := st.page[id]
		if page == nil {
			page = new(int)
			st.page[id] = page
		}
		k.Segment(c, "inspector-page-"+id, page, inspectorPages)
		switch *page {
		case 0:
			sideSections(c, k, d.System, d.SectionsRaw, "这次请求没有系统提示词记录")
		case 1:
			sideSections(c, k, d.Injected, d.InjectedRaw, "这次请求没有注入的上下文")
		case 2:
			sideTools(c, k, d)
		default:
			k.KeyValues(c, "usage", requestUsageFacts(d))
			for i, e := range d.ParseErr {
				ui.Text(c.Key(fmt.Sprint("parse-", i)), e).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(k.T.C.DangerText).Wrap()
			}
		}
	})
}

// sideSections 画一页提示词段：段头一行（名字、来源、字数），下面是能选中复制的全文。
// raw 不空说明这一列解析不了，原样显示原文。
func sideSections(c *ui.Context, k *winshell.Kit, secs []ctlproto.RequestSection, raw, empty string) {
	g, p := k.G, k.P
	if raw != "" {
		k.Note(c.Key("raw-note"), "这一列的格式解析不了，原文如下", false)
		sideLongText(c, "raw", raw, k)
	}
	if len(secs) == 0 && raw == "" {
		k.Note(c.Key("empty"), empty, false)
		return
	}
	for i, s := range secs {
		ui.Column(c.Key(fmt.Sprint("section-", i))).FillWidth().Gap(g.SpaceTight).Children(func() {
			head := s.ID
			if s.Source != "" {
				head += " · " + s.Source
			}
			head += fmt.Sprintf(" · %d 字", s.Chars)
			ui.Text(c.Key("head"), head).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).FontWeight(g.MediumWeight).TextColor(p.Muted).Wrap()
			if s.Missing {
				k.Note(c.Key("missing"), "这段的正文没存下", false)
				return
			}
			sideLongText(c, "text", s.Text, k)
		})
	}
}

// sideLongText 把长文按空行分成段，每段一个文本，整块连着能选中复制。
// 一整段几万字放进一个文本框，每帧排版都重来；分段后排版缓存按段命中。
func sideLongText(c *ui.Context, key, text string, k *winshell.Kit) {
	g, p := k.G, k.P
	ui.Column(c.Key(key)).FillWidth().Gap(g.SpaceSmall).Selectable().Children(func() {
		for i, para := range splitParas(text) {
			ui.Text(c.Key(i), para).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Ink).Wrap()
		}
	})
}

// splitParas 按空行切段，保留每段内部的换行；全空返回一个空段，不丢内容。
func splitParas(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, para := range strings.Split(text, "\n\n") {
		if strings.TrimSpace(para) != "" {
			out = append(out, para)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

// sideTools 是工具表页：每个工具一行（名字、估算 token），总数在最上面。
func sideTools(c *ui.Context, k *winshell.Kit, d ctlproto.RequestDetail) {
	g, p := k.G, k.P
	if d.ContextRaw != "" {
		k.Note(c.Key("raw-note"), "这一列的格式解析不了，原文如下", false)
		sideLongText(c, "context-raw", d.ContextRaw, k)
	}
	if len(d.Tools) == 0 && d.ContextRaw == "" {
		k.Note(c.Key("empty"), "这次请求没记工具表", false)
	}
	if len(d.Tools) > 0 {
		k.Note(c.Key("total"), fmt.Sprintf("%d 个工具 · 约 %s token", len(d.Tools), fmtTokens(int64(d.ToolTokens))), false)
	}
	for i, t := range d.Tools {
		ui.Row(c.Key(fmt.Sprint("tool-", i))).FillWidth().Gap(g.SpaceSmall).Children(func() {
			ui.Text(c, t.Name).Grow(1).MinWidth(0).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Ink).SingleLine().Tooltip(t.Name)
			ui.Text(c, fmtTokens(int64(t.Tokens))).Shrink(0).FontSize(g.MetaSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).FontFeatures("tnum")
		})
	}
	// 后台目前不存完整请求正文；以后存了就原样放在这里
	if d.RequestRaw != "" {
		k.Note(c.Key("request-note"), "完整请求", false)
		sideLongText(c, "request-raw", d.RequestRaw, k)
	}
}

// requestUsageFacts 是用量页的键值：只写记录里有的，没有的不写。
func requestUsageFacts(d ctlproto.RequestDetail) []winshell.KeyValue {
	kv := []winshell.KeyValue{{Key: "模型", Value: d.Model}}
	if d.Route != "" {
		kv = append(kv, winshell.KeyValue{Key: "线路", Value: d.Route})
	}
	total := d.Input + d.CacheRead + d.CacheWrite
	if total > 0 {
		kv = append(kv,
			winshell.KeyValue{Key: "输入", Value: fmtTokens(total)},
			winshell.KeyValue{Key: "命中缓存", Value: fmt.Sprintf("%s（%s）", fmtTokens(d.CacheRead), fmtPct(float64(d.CacheRead)/float64(total)))})
		if d.CacheWrite > 0 {
			kv = append(kv, winshell.KeyValue{Key: "写入缓存", Value: fmtTokens(d.CacheWrite)})
		}
	}
	kv = append(kv, winshell.KeyValue{Key: "输出", Value: fmtTokens(d.Output)})
	if d.Reasoning > 0 {
		kv = append(kv, winshell.KeyValue{Key: "思考", Value: fmtTokens(d.Reasoning)})
	}
	if d.TTFTMs > 0 {
		kv = append(kv, winshell.KeyValue{Key: "首字", Value: fmtMS(d.TTFTMs)})
	}
	kv = append(kv, winshell.KeyValue{Key: "用时", Value: fmtMS(d.TotalMs)})
	if d.Window > 0 {
		kv = append(kv, winshell.KeyValue{Key: "窗口", Value: fmtTokens(int64(d.Window))})
	}
	if d.Error != "" {
		kv = append(kv, winshell.KeyValue{Key: "失败", Value: d.Error})
	}
	return kv
}

// turnUsageSlack 是一轮结束以后，她的最后一句话落库时间最多可以晚多久（落库在回合结束时，两个时钟读数有微小先后）。
const turnUsageSlack = 2000 // 毫秒

// assignTurnUsage 算聊天里哪条消息对应哪一轮（消息 id → 轮 id）。
// 后台的消息表没有轮号，只能靠时间和角色对：只给已经结束的轮、只给它时间窗里她的最后一句；
// 一句话同时落在两轮的窗口里（对不准）就都不给；进行中的轮没有结束时间，也不给。宁可没有，不乱挂。
// msgs 按时间先后排。
func assignTurnUsage(msgs []*msg, turns []ctlproto.ConvTurn) map[string]string {
	out := map[string]string{}
	taken := map[string]string{} // 消息 → 先认领它的轮
	conflict := map[string]bool{}
	for _, t := range turns {
		if t.EndedAt == 0 || len(t.Requests) == 0 {
			continue
		}
		var last *msg
		for _, m := range msgs {
			if !m.isChar || m.service != "" || m.liveKind != liveNone || m.id == "" || m.actor != t.Character {
				continue
			}
			if m.at >= t.StartedAt && m.at <= t.EndedAt+turnUsageSlack {
				last = m
			}
		}
		if last == nil {
			continue
		}
		if prev, ok := taken[last.id]; ok && prev != t.ID {
			conflict[last.id] = true
			continue
		}
		taken[last.id] = t.ID
		out[last.id] = t.ID
	}
	for id := range conflict {
		delete(out, id)
	}
	return out
}

// turnUsageText 是一轮的用量说明（悬停提示）；没有用量记录（失败的、还没回的）返回空，不编。
func turnUsageText(t ctlproto.ConvTurn) string {
	var in, read, out int64
	for _, r := range t.Requests {
		in += r.Input
		read += r.CacheRead
		out += r.Output
	}
	if in == 0 && out == 0 {
		return ""
	}
	text := "本轮用量 · 输入 " + fmtTokens(in)
	if in > 0 {
		text += "（命中 " + fmtPct(float64(read)/float64(in)) + "）"
	}
	return text + " · 输出 " + fmtTokens(out)
}

// turnUsageFor 是这条消息所在轮的用量说明，没有就空。只用右栏已经读到的报告，不为它另读库：
// 右栏没打开过上下文（或换了会话）时没有数据，钮就不摆。只在本地私聊、本地群聊。
func (a *app) turnUsageFor(messageID string) string {
	if a.keyOf(a.sel).Platform != "local" {
		return ""
	}
	st := &a.workbench.shell.sideContext
	if st.conv != a.sel || !st.has {
		return ""
	}
	last := ""
	if n := len(a.msgs); n > 0 {
		last = a.msgs[n-1].id
	}
	key := fmt.Sprint(st.reportVer, "|", len(a.msgs), "|", last)
	if st.assign == nil || st.assignKey != key {
		st.assign, st.assignKey = assignTurnUsage(a.msgs, st.report.Turns), key
	}
	turnID := st.assign[messageID]
	if turnID == "" {
		return ""
	}
	for _, t := range st.report.Turns {
		if t.ID == turnID {
			return turnUsageText(t)
		}
	}
	return ""
}

// openSideTurn 点“本轮用量”：打开右栏，切到上下文，展开这一轮。
func (a *app) openSideTurn(messageID string) {
	st := &a.workbench.shell.sideContext
	turnID := st.assign[messageID]
	if turnID == "" || st.conv != a.sel {
		return
	}
	a.workbench.shell.st.OpenSide()
	a.workbench.shell.sideTab = 1
	if st.open == nil {
		st.open = map[string]bool{}
	}
	st.open[turnID] = true
	a.sideSpansFetch(st, turnID)
}
