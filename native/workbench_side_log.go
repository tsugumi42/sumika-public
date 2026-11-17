package main

// 右栏的“对话日志”标签（规格-群聊引用和路由 第十五节）：这个会话里主人指给角色的悄悄话，和她们各自的处理过程——
// 引用卡、对象、主人的话，每位被指角色的安排和状态，她的思考、工具、记忆写入、唤起的子任务、返回、处理记录。
// 聊天区只显示真正发出去的对话，悄悄话和处理过程都不放在那里。
//
// 数据来自 api.WhisperLogReader：只在标签看得见时订阅最新一页，离开或隔几秒没再画就自己结束；更早的按“载入更早的”一页一页取。
// 全文（思考原文、工具参数）点开一条才读。悄悄话那一轮要主人批准的执行类审批卡就放在这个标签最上面，能批；
// 来源窗口的输入区只留一行提示指到这里。读到的字都是库里来的，只当数据显示，只在本机。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/winshell"
)

// sideLogTab 是“对话日志”在右栏标签里的位置（资料 0、上下文或活动 1、对话日志 2）；本地私聊没有悄悄话，没有这个标签。
const sideLogTab = 2

// sideLogIdle 是标签多久没画就结束订阅。
const sideLogIdle = 3 * time.Second

// sideLogState 是对话日志标签的状态，换会话整份清掉（代次加一，在途的旧结果对不上就丢）。
type sideLogState struct {
	conv     string
	gen      uint64
	batches  map[string][]ctlproto.LogEntry // 批号 → 这一批的条目（从早到晚）
	next     string                         // 还有更早的就非空：原样交回去取
	older    bool                           // 载入过更早的页（之后头一页的下一页游标不再覆盖 next）
	has      bool
	err      string
	watching bool
	stop     context.CancelFunc
	seen     *atomic.Int64 // 最后一次画的时间（纳秒）
	loading  bool          // 正在读更早的一页
	open     map[string]bool
	detail   map[string]ctlproto.LogDetail
	detailE  map[string]string
	busy     map[string]bool
	actErr   string
	acting   map[string]bool // 正在取消的批
	pending  []ctlproto.LogEntry
}

// reset 换会话：结束旧订阅，状态清空，代次加一。
func (st *sideLogState) reset(conv string) {
	if st.stop != nil {
		st.stop()
	}
	*st = sideLogState{conv: conv, gen: st.gen + 1, seen: new(atomic.Int64)}
}

// merge 把一页并进来。head 为真是订阅给的最新一页（整页替换它含的批）；否则是“载入更早的”读来的页。
// 掉出最新一页的批留在这里（不会因为有了新批就丢掉旧批），所以页与页之间不会漏。
func (st *sideLogState) merge(p ctlproto.LogPage, head bool) {
	byBatch := map[string][]ctlproto.LogEntry{}
	for _, e := range p.Entries {
		byBatch[e.Batch] = append(byBatch[e.Batch], e)
	}
	if st.batches == nil {
		st.batches = map[string][]ctlproto.LogEntry{}
	}
	for b, es := range byBatch {
		st.batches[b] = es
	}
	switch {
	case head && !st.older:
		st.next = p.Next
	case !head:
		st.next, st.older = p.Next, true
	}
	st.has = true
}

// order 是批号从新到旧：按这一批第一条的时间，同一时间按批号。
func (st *sideLogState) order() []string {
	ids := make([]string, 0, len(st.batches))
	for b := range st.batches {
		ids = append(ids, b)
	}
	sort.Slice(ids, func(i, j int) bool {
		ai, aj := st.batches[ids[i]][0].At, st.batches[ids[j]][0].At
		if ai != aj {
			return ai > aj
		}
		return ids[i] > ids[j]
	})
	return ids
}

func (a *app) logReader() api.WhisperLogReader {
	rd, _ := a.reader.(api.WhisperLogReader)
	return rd
}

// sideLogTabFor 说这个会话有没有对话日志：本地群聊、QQ 私聊、QQ 群（有悄悄话入口的场合）。
func (a *app) sideLogTabFor(k api.ConversationKey) bool {
	return a.whisperKey(k) && a.logReader() != nil
}

// sideLogView 画对话日志。
func (a *app) sideLogView(c *ui.Context, kit *winshell.Kit) {
	g, p := kit.G, kit.P
	st := &a.workbench.shell.sideLog
	k := a.keyOf(a.sel)
	if st.conv != a.sel || st.seen == nil {
		st.reset(a.sel)
	}
	st.seen.Store(time.Now().UnixNano()) // 墙上时间：订阅协程也按墙上时间判断多久没画了
	rd := a.logReader()
	note := func(key, text string) {
		ui.Text(c.Key(key), text).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).Wrap()
	}
	if rd == nil {
		note("log-none", "后台还没起来，这里要等后台打开以后才能看")
		return
	}
	a.sideLogWatch(st, rd, k)
	// 审批位置：悄悄话处理要主人批的卡放在最上面，能批
	if slices.ContainsFunc(a.approvals, func(v api.LocalApproval) bool { return a.whisperApprovalHere(v, k) }) {
		a.drawApprovals(c, func(v api.LocalApproval) bool { return a.whisperApprovalHere(v, k) })
	}
	switch {
	case st.err != "" && !st.has:
		note("log-error", "没读到对话日志："+st.err)
		return
	case !st.has:
		return // 很快就到，不闪加载字
	case len(st.batches) == 0:
		note("log-empty", "还没有悄悄话。在消息上点右键选“引用”，指给某位角色，她怎么处理的会记在这里。")
		return
	}
	if st.actErr != "" {
		kit.Note(c, "没做成："+st.actErr, true)
	}
	for _, b := range st.order() {
		a.sideLogBatch(c, kit, st, rd, k, b, st.batches[b])
	}
	if st.next != "" {
		kit.Button(c, "log-older", "载入更早的", false, st.loading, func() { a.sideLogOlder(st, rd, k) })
	}
}

// whisperApprovalHere 说这张审批卡是不是属于会话 k 的对话日志：必须是悄悄话那一轮的卡（Whisper 不空）、来源就是 k、k 有悄悄话入口。
// 别的会话的卡、普通的卡一律不在这里画。
func (a *app) whisperApprovalHere(v api.LocalApproval, k api.ConversationKey) bool {
	return v.Whisper != nil && v.Whisper.Source == k && a.whisperKey(k)
}

// sideLogWatch 开订阅（还没开的话）：先登记再读，之后后台给新的一页就更新状态；标签几秒没画就自己结束。
func (a *app) sideLogWatch(st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey) {
	if st.watching {
		return
	}
	st.watching = true
	ctx, cancel := context.WithCancel(a.base())
	st.stop = cancel
	gen, seen := st.gen, st.seen
	go func() {
		defer cancel()
		sub, err := rd.WatchWhisperLog(ctx, k)
		if err != nil {
			a.post(func() {
				if cur := &a.workbench.shell.sideLog; cur.gen == gen {
					cur.err, cur.watching = err.Error(), false
				}
			})
			return
		}
		defer sub.Close()
		idle := time.NewTicker(sideLogIdle / 2)
		defer idle.Stop()
		for {
			select {
			case snap, ok := <-sub.Updates():
				if !ok {
					a.post(func() {
						if cur := &a.workbench.shell.sideLog; cur.gen == gen {
							cur.watching = false
						}
					})
					return
				}
				a.post(func() {
					cur := &a.workbench.shell.sideLog
					if cur.gen != gen {
						return
					}
					if snap.State != api.Ready {
						if snap.Err != nil {
							cur.err = snap.Err.Error()
						}
						return
					}
					cur.err = ""
					cur.merge(snap.Value, true)
				})
			case <-idle.C:
				if time.Since(time.Unix(0, seen.Load())) > sideLogIdle {
					a.post(func() {
						if cur := &a.workbench.shell.sideLog; cur.gen == gen {
							cur.watching = false
						}
					})
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (a *app) sideLogOlder(st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey) {
	if st.loading || st.next == "" {
		return
	}
	st.loading = true
	gen, cursor, ctx := st.gen, st.next, a.base()
	go func() {
		p, err := rd.ReadWhisperLog(ctx, k, cursor, 0)
		a.post(func() {
			cur := &a.workbench.shell.sideLog
			if cur.gen != gen {
				return
			}
			cur.loading = false
			if err != nil {
				cur.actErr = "没读到更早的：" + err.Error()
				return
			}
			cur.actErr = ""
			cur.merge(p, false)
		})
	}()
}

func (a *app) sideLogCancel(st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey, batch, character string) {
	id := batch + "|" + character
	if st.acting[id] {
		return
	}
	if st.acting == nil {
		st.acting = map[string]bool{}
	}
	st.acting[id] = true
	gen, ctx := st.gen, a.base()
	go func() {
		_, err := rd.CancelWhisper(ctx, k, batch, character)
		a.post(func() {
			cur := &a.workbench.shell.sideLog
			if cur.gen != gen {
				return
			}
			delete(cur.acting, id)
			if err != nil {
				cur.actErr = "取消没做成：" + err.Error()
				return
			}
			cur.actErr = ""
		})
	}()
}

// sideLogDetail 读一条的全文（点开时）；已经读到或正在读就不再读。
func (a *app) sideLogDetail(st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey, id string) {
	if st.busy[id] {
		return
	}
	if _, ok := st.detail[id]; ok {
		return
	}
	if st.busy == nil {
		st.busy = map[string]bool{}
	}
	st.busy[id] = true
	delete(st.detailE, id)
	gen, ctx := st.gen, a.base()
	go func() {
		d, err := rd.ReadWhisperLogEntry(ctx, k, id, true)
		a.post(func() {
			cur := &a.workbench.shell.sideLog
			if cur.gen != gen {
				return
			}
			delete(cur.busy, id)
			if err != nil {
				if cur.detailE == nil {
					cur.detailE = map[string]string{}
				}
				cur.detailE[id] = "没读到全文：" + err.Error()
				return
			}
			if cur.detail == nil {
				cur.detail = map[string]ctlproto.LogDetail{}
			}
			cur.detail[id] = d
		})
	}()
}

var logStateName = map[string]string{
	ctlproto.WhisperQueued: "排队中", ctlproto.WhisperProcessing: "处理中", ctlproto.WhisperDone: "完成", ctlproto.WhisperFailed: "失败",
	ctlproto.WhisperCancelled: "已取消", ctlproto.WhisperTimedOut: "超时", ctlproto.WhisperInterrupted: "已中断，没处理",
}

var logKindLabel = map[string]string{
	ctlproto.LogThink: "思考", ctlproto.LogTool: "工具", ctlproto.LogMemory: "记忆", ctlproto.LogSpawn: "唤起", ctlproto.LogReturn: "返回", ctlproto.LogNote: "处理记录",
}

func logStateTone(state string) winshell.BadgeTone {
	switch state {
	case ctlproto.WhisperDone:
		return winshell.BadgeGood
	case ctlproto.WhisperFailed, ctlproto.WhisperTimedOut, ctlproto.WhisperInterrupted:
		return winshell.BadgeWarn
	}
	return winshell.BadgeNeutral
}

// logCharName 是角色在界面上的名字；认不出就用 id。
func (a *app) logCharName(id string) string {
	if ch, ok := a.charOf(id); ok && ch.Name != "" {
		return ch.Name
	}
	return id
}

// logEntryLine 是一条过程的一行字：种类、字数或状态、摘要。思考只有字数和第一行，工具有状态。
func logEntryLine(e ctlproto.LogEntry) string {
	line := logKindLabel[e.Kind]
	if line == "" {
		line = e.Kind
	}
	switch {
	case e.Kind == ctlproto.LogThink:
		line += fmt.Sprintf(" · %d 字", e.Runes)
	case e.Waiting:
		line += " · 等你批准"
	case e.Err:
		line += " · 出错"
	}
	if e.Egress != "" {
		line += " · 外发 " + e.Egress
		if e.EgressDanger > 0 {
			line += "（" + fmt.Sprint(e.EgressDanger) + " 处可疑）"
		}
	}
	if e.Summary != "" {
		line += " · " + e.Summary
	}
	return line
}

func (a *app) sideLogBatch(c *ui.Context, kit *winshell.Kit, st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey, batch string, es []ctlproto.LogEntry) {
	g := kit.G
	var say *ctlproto.LogEntry
	var arranges []ctlproto.LogEntry
	kids := map[string][]ctlproto.LogEntry{}
	for i, e := range es {
		switch e.Kind {
		case ctlproto.LogWhisper:
			say = &es[i]
		case ctlproto.LogArrange:
			arranges = append(arranges, e)
		default:
			kids[e.Character] = append(kids[e.Character], e)
		}
	}
	ui.Column(c.Key("log-batch-" + batch)).FillWidth().Gap(g.SpaceSmall).Children(func() {
		kit.Break(c, "")
		if say != nil {
			a.sideLogSay(c, kit, *say)
		}
		for _, ar := range arranges {
			a.sideLogArrange(c, kit, st, rd, k, ar, kids[ar.Character])
		}
	})
}

// sideLogSay 是悄悄话本身：谁在什么时候指给了谁，被引的话（最多三行），主人的话。
func (a *app) sideLogSay(c *ui.Context, kit *winshell.Kit, e ctlproto.LogEntry) {
	g, p := kit.G, kit.P
	var names []string
	for _, id := range e.Targets {
		names = append(names, a.logCharName(id))
	}
	who := strings.Join(names, "、")
	if len(names) > 3 { // 指给很多位（所有人）时只写前三位和总数，不让标题占满右栏
		who = strings.Join(names[:3], "、") + fmt.Sprintf(" 等 %d 位", len(names))
	}
	head := time.UnixMilli(e.At).Format("15:04") + " · 悄悄话 → " + who
	ui.Text(c.Key("log-say-head"), head).FillWidth().FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).FontWeight(g.MediumWeight).TextColor(p.Ink).Wrap()
	for i, q := range e.Quotes {
		line := q.Text
		if q.Name != "" {
			line = q.Name + "：" + line
		}
		ui.Text(c.Key(fmt.Sprint("log-quote-", i)), line).FillWidth().Padding(0, g.SpaceSmall).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap().Selectable()
	}
	if e.Summary != "" {
		ui.Text(c.Key("log-say-text"), e.Summary).FillWidth().Padding(0, g.SpaceSmall).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Ink).Wrap().Selectable()
	}
}

// sideLogArrange 是一位被指角色：名字、状态、取消，下面是她的过程。
func (a *app) sideLogArrange(c *ui.Context, kit *winshell.Kit, st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey, ar ctlproto.LogEntry, kids []ctlproto.LogEntry) {
	g, p := kit.G, kit.P
	live := ar.State == ctlproto.WhisperQueued || ar.State == ctlproto.WhisperProcessing
	ui.Column(c.Key("log-arr-" + ar.Character)).FillWidth().Gap(g.SpaceTight).Children(func() {
		ui.Row(c.Key("log-arr-head")).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.logCharName(ar.Character)).Grow(1).MinWidth(0).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).FontWeight(g.MediumWeight).TextColor(p.Ink).SingleLine()
			label := logStateName[ar.State]
			if label == "" {
				label = ar.State
			}
			if ar.State == ctlproto.WhisperQueued {
				label += fmt.Sprintf("，前面还有 %d 个", ar.Position)
			}
			kit.Badge(c, "log-state", label, logStateTone(ar.State))
			if live {
				kit.Button(c, "log-cancel", "取消", false, st.acting[ar.Batch+"|"+ar.Character], func() { a.sideLogCancel(st, rd, k, ar.Batch, ar.Character) })
			}
		})
		if ar.Busy {
			ui.Text(c.Key("log-busy"), "悄悄话到的时候她在这个会话里有回合在跑（没打断，后台处理）").FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
		}
		if ar.Summary != "" {
			ui.Text(c.Key("log-arr-detail"), ar.Summary).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
		}
		for _, e := range kids {
			a.sideLogEntry(c, kit, st, rd, k, e)
		}
	})
}

// sideLogEntry 是过程里的一条：一行摘要，点开读全文（思考原文、工具参数、子任务的任务说明和结果）。
func (a *app) sideLogEntry(c *ui.Context, kit *winshell.Kit, st *sideLogState, rd api.WhisperLogReader, k api.ConversationKey, e ctlproto.LogEntry) {
	g, p := kit.G, kit.P
	open := st.open[e.ID]
	line := logEntryLine(e)
	ui.Column(c.Key("log-e-" + e.ID)).FillWidth().Gap(g.SpaceTight).Children(func() {
		row := ui.ButtonBase(c.Key("log-row")).Label(line).FillWidth().Padding(g.SpaceTight, g.SpaceSmall).Radius(g.RadiusSegment).OnClick(func() {
			if st.open == nil {
				st.open = map[string]bool{}
			}
			st.open[e.ID] = !st.open[e.ID]
			if st.open[e.ID] {
				a.sideLogDetail(st, rd, k, e.ID)
			}
		})
		switch {
		case row.Pressed():
			row.Background(p.Press)
		case row.Hovered() || open:
			row.Background(p.Hover)
		}
		ink := p.Muted
		if e.Err || e.Waiting || e.EgressDanger > 0 {
			ink = kit.T.C.DangerText
		}
		row.Children(func() {
			ui.Text(c, line).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(ink).Wrap()
		})
		if !open {
			return
		}
		switch d, ok := st.detail[e.ID]; {
		case st.detailE[e.ID] != "":
			kit.Note(c, st.detailE[e.ID], true)
		case !ok:
			ui.Text(c.Key("log-loading"), "正在读").FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted)
		default:
			body := d.Text
			if d.Args != "" {
				body = "参数：" + d.Args + "\n" + body
			}
			if body == "" {
				body = "（没有更多内容）"
			}
			ui.Text(c.Key("log-detail"), body).FillWidth().Padding(0, g.SpaceSmall).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Ink).Wrap().Selectable()
		}
	})
}

// sideLogPendingHint 是来源窗口输入区里的一行提示：悄悄话处理有 n 张审批卡等着，卡在右栏的对话日志里批。
// 点“去批”打开右栏并切到对话日志。没有卡就不画。
func (a *app) sideLogPendingHint(c *ui.Context, k api.ConversationKey) {
	n := 0
	for _, v := range a.approvals {
		if a.whisperApprovalHere(v, k) {
			n++
		}
	}
	if n == 0 {
		return
	}
	kit := a.workbench.shell.kit
	if kit == nil {
		return
	}
	g, p := kit.G, kit.P
	ui.Row(c.Key("log-pending")).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).Padding(g.SpaceSmall).Children(func() {
		ui.Text(c, fmt.Sprintf("悄悄话处理有 %d 项等你批准，在右栏的对话日志里", n)).Grow(1).MinWidth(0).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Ink).Wrap()
		kit.Button(c, "log-pending-open", "去批", true, false, func() {
			a.workbench.shell.st.OpenSide()
			a.workbench.shell.sideTab = sideLogTab
		})
	})
}
