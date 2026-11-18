package main

// 右栏的“联系”标签（规格-主动联系 第十节第 2 条）：这个角色的主动联系现状——当前线（或最近一条）的状态、开口理由、去过哪、
// 下次醒的时间、最近 10 次导演决定，头上两个按钮：今天别找我、现在别再找了。
// 只在标签看得见时才读（经 api.ProactiveReader），5 秒看一次要不要刷新，靠帧里的定时，不起常驻协程；切走就不读。
// 读到的理由、摘要、结论是库里来的文字，只当数据显示，只在本机。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/proactive"
	"sumika-native/internal/winshell"
)

const sideContactRefresh = 5 * time.Second

// sideContactTab 是“联系”在右栏标签里的位置（资料 0、上下文 1、联系 2）；只有本地私聊有
const sideContactTab = 2

// sideContactState 是联系标签的状态，换角色整份清掉（代次加一，在途的旧结果对不上就丢）。
type sideContactState struct {
	char    string
	status  ctlproto.ProactiveStatus
	has     bool
	err     string
	loading bool
	due     time.Time
	gen     uint64
	open    map[int64]bool // 展开了快照的决定行
	acting  bool           // 点了按钮，正在做
	actErr  string
	reads   int // 读过几次（测试用：标签看不见时不该涨）
}

// contactReader 是这个后台的联系读写：没有就是后台没起来、或只看历史
func (a *app) contactReader() (api.ProactiveReader, api.ProactiveControl) {
	rd, _ := a.reader.(api.ProactiveReader)
	ctl, _ := a.reader.(api.ProactiveControl)
	return rd, ctl
}

func (a *app) sideContactView(c *ui.Context, k *winshell.Kit) {
	g, p := k.G, k.P
	st := &a.workbench.shell.sideContact
	char := charOfConv(a.keyOf(a.sel))
	if st.char != char {
		gen := st.gen + 1
		*st = sideContactState{char: char, gen: gen}
	}
	note := func(key, text string) {
		ui.Text(c.Key(key), text).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).Wrap()
	}
	rd, ctl := a.contactReader()
	if rd == nil || char == "" {
		note("contact-none", "后台还没起来，这里要等后台打开以后才能看")
		return
	}
	now := c.Now()
	if !st.loading && !now.Before(st.due) {
		a.sideContactFetch(rd, st, now)
	}
	if st.due.After(now) {
		c.After(st.due.Sub(now))
	}
	switch {
	case st.err != "" && !st.has:
		note("contact-error", "没读到主动联系的情况："+st.err)
		return
	case !st.has:
		return // 很快就到，不闪加载字
	case !st.status.Enabled:
		note("contact-off", "这个角色没有打开主动联系。想打开：配置里 proactive.enabled 设为 true，并把她的 id 列进 proactive.characters，重启后生效。")
		return
	}
	s := st.status
	th := s.Thread
	open := th != nil && th.Open
	ui.Row(c.Key("contact-actions")).FillWidth().Gap(g.SpaceSmall).Children(func() {
		k.Button(c, "contact-snooze", "今天别找我", false, ctl == nil || st.acting, func() {
			a.sideContactAct(ctl, st, func(ctx context.Context) error { return ctl.ProactiveSnooze(ctx, char) })
		})
		k.Button(c, "contact-close", "现在别再找了", false, ctl == nil || st.acting || !open, func() {
			a.sideContactAct(ctl, st, func(ctx context.Context) error { return ctl.ProactiveCloseThread(ctx, char) })
		})
	})
	if st.actErr != "" {
		k.Note(c, "没做成："+st.actErr, true)
	}
	k.KeyValues(c, "contact-facts", contactFacts(s, now))
	if len(s.Visits) > 0 {
		k.Break(c, "去过哪")
		ui.Column(c.Key("contact-visits")).FillWidth().Gap(g.SpaceTight).Children(func() {
			for i, v := range s.Visits {
				ui.Text(c.Key(fmt.Sprint("visit-", i)), contactVisitLine(v)).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
			}
		})
	}
	k.Break(c, "最近的决定")
	if len(s.Decisions) == 0 {
		note("contact-no-rows", "还没有决定记录：导演到点才会醒，第一次醒以前这里是空的。")
		return
	}
	for _, r := range s.Decisions {
		a.sideDecisionRow(c, k, st, r)
	}
}

// contactFacts 是当前线的几件事：现在怎样、为什么开口、下次醒、别烦到什么时候、模式
func contactFacts(s ctlproto.ProactiveStatus, now time.Time) []winshell.KeyValue {
	var out []winshell.KeyValue
	if th := s.Thread; th != nil {
		out = append(out, winshell.KeyValue{Key: "现在", Value: th.StateText})
		if th.OpenedReason != "" {
			out = append(out, winshell.KeyValue{Key: "为什么找你", Value: th.OpenedReason})
		}
		if !th.Open && th.Conclusion != "" {
			out = append(out, winshell.KeyValue{Key: "结论", Value: th.Conclusion})
		}
	} else {
		out = append(out, winshell.KeyValue{Key: "现在", Value: "没有开着的线"})
	}
	if s.NextWake > 0 {
		out = append(out, winshell.KeyValue{Key: "下次醒", Value: contactClock(time.UnixMilli(s.NextWake), now)})
	}
	if s.QuietUntil > 0 && time.UnixMilli(s.QuietUntil).After(now) {
		out = append(out, winshell.KeyValue{Key: "别烦到", Value: contactClock(time.UnixMilli(s.QuietUntil), now)})
	}
	if s.Send == "think" {
		out = append(out, winshell.KeyValue{Key: "模式", Value: "只想不说（不会真发）"})
	}
	return out
}

// contactClock 是今天的时间只写时分，别的天带日期
func contactClock(t, now time.Time) string {
	y1, m1, d1 := t.In(now.Location()).Date()
	y2, m2, d2 := now.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return t.In(now.Location()).Format("15:04")
	}
	return t.In(now.Location()).Format("01-02 15:04")
}

func contactVisitLine(v ctlproto.ProactiveVisit) string {
	line := time.UnixMilli(v.At).Format("15:04") + " · " + v.Label
	switch v.State {
	case proactive.SendFailed:
		line += " · 没发出去"
	case proactive.SendUnknown:
		line += " · 不知道发出去没有"
	case proactive.SendThought:
		line += " · 本想说（没发）"
	case proactive.SendPending:
		line += " · 发送中"
	}
	if v.Summary != "" {
		line += " · " + v.Summary
	}
	return line
}

// contactDecisionLine 是一条决定的一行：时间 · 结果 · 一句为什么
func contactDecisionLine(r ctlproto.ProactiveRow) string {
	at := time.UnixMilli(r.At).Format("01-02 15:04")
	line := at + " · " + contactResult(r)
	if r.Note != "" {
		line += " · " + r.Note
	}
	if r.Count > 1 {
		line += fmt.Sprintf("（%d 次）", r.Count)
	}
	return line
}

func contactResult(r ctlproto.ProactiveRow) string {
	switch r.Kind {
	case string(proactive.KindClose):
		return "收线：" + proactive.CloseReasonText(proactive.CloseReason(r.Reason))
	case string(proactive.KindFollowUp):
		return "续醒：" + contactReasonText(r.Reason)
	}
	return contactReasonText(r.Reason)
}

// contactReasonText 是日志原因码的中文（规格第七节；悬停说明在这里直接写成一句）
func contactReasonText(code string) string {
	switch code {
	case proactive.ReasonDue:
		return "放行，在想"
	case proactive.ReasonDueAsleepActive:
		return "睡觉时段，但你在活动，放行"
	case proactive.ReasonSnoozed:
		return "你说过别烦"
	case proactive.ReasonDailyCap:
		return "今天找够了"
	case proactive.ReasonAlreadyOpen:
		return "已经有开着的线"
	case proactive.ReasonOwnerChatting:
		return "你正在聊，先不打扰"
	case proactive.ReasonAsleepNoActive:
		return "睡觉时段，没有动静"
	case proactive.ReasonColdStreak:
		return "连着几次没理她，今天不找了"
	case proactive.ReasonMissedOffline:
		return "到点时程序没开，没醒到"
	case proactive.ReasonError:
		return "出错"
	case "model-spoke":
		return "开口了"
	case "model-quiet":
		return "想了没开口"
	case "model-no-stance":
		return "想了没表态，当没开口"
	case "think-only":
		return "本想说（只想不说档，没发）"
	case "send-failed":
		return "想说但没发出去"
	case "send-blocked-cap":
		return "这条线发满了"
	case "send-blocked-guard":
		return "这句被拦下了"
	}
	return code
}

func (a *app) sideDecisionRow(c *ui.Context, k *winshell.Kit, st *sideContactState, r ctlproto.ProactiveRow) {
	g, p := k.G, k.P
	open := st.open[r.ID]
	ui.Column(c.Key(fmt.Sprint("decision-", r.ID))).FillWidth().Gap(g.SpaceTight).Children(func() {
		row := ui.ButtonBase(c.Key("decision-row")).Label(contactDecisionLine(r)).FillWidth().Padding(g.SpaceSmall).Radius(g.RadiusSegment).OnClick(func() {
			if st.open == nil {
				st.open = map[int64]bool{}
			}
			st.open[r.ID] = !st.open[r.ID]
		})
		switch {
		case row.Pressed():
			row.Background(p.Press)
		case row.Hovered() || open:
			row.Background(p.Hover)
		}
		row.Children(func() {
			ui.Text(c, contactDecisionLine(r)).FillWidth().FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Ink).Wrap()
		})
		if open {
			ui.Text(c.Key("decision-inputs"), contactInputsText(r)).FillWidth().Padding(0, g.SpaceSmall).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap().Selectable()
		}
	})
}

// contactInputsText 把当时的快照（只有数字和布尔）写成一句话
func contactInputsText(r ctlproto.ProactiveRow) string {
	if len(r.Inputs) == 0 {
		return "这一行没有快照。"
	}
	var in proactive.Inputs
	if err := json.Unmarshal(r.Inputs, &in); err != nil {
		return "快照格式读不懂。"
	}
	yes := func(v bool) string {
		if v {
			return "是"
		}
		return "否"
	}
	mins := func(n int) string {
		if n < 0 {
			return "不知道"
		}
		return fmt.Sprintf("%d 分钟", n)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "距上次找你 %s · 系数 %.2f · 连冷 %d 次 · 今天已找 %d 次 · 有开着的线 %s · 睡觉时段 %s · 你最后活跃距今 %s · 你本地最后输入距今 %s · 窗口在前台 %s",
		mins(in.SinceContactMin), in.Factor, in.ColdStreak, in.ThreadsToday, yes(in.OpenThread), yes(in.InSleep), mins(in.OwnerLastActiveAgoMin), mins(in.LocalLastInputAgoMin), yes(in.WindowForeground))
	if in.MissedMin > 0 {
		fmt.Fprintf(&b, " · 晚了 %d 分钟", in.MissedMin)
	}
	return b.String()
}

func (a *app) sideContactFetch(rd api.ProactiveReader, st *sideContactState, now time.Time) {
	char, gen := st.char, st.gen
	st.due = now.Add(sideContactRefresh)
	st.loading = true
	st.reads++
	ctx := a.base()
	go func() {
		s, err := rd.ProactiveStatus(ctx, char)
		a.post(func() {
			cur := &a.workbench.shell.sideContact
			if cur.gen != gen || cur.char != char {
				return
			}
			cur.loading = false
			if err != nil {
				cur.err = err.Error()
				return
			}
			cur.status, cur.has, cur.err = s, true, ""
		})
	}()
}

// sideContactAct 点了按钮：后台做完以后马上重读一次，让状态行跟上
func (a *app) sideContactAct(ctl api.ProactiveControl, st *sideContactState, do func(context.Context) error) {
	if ctl == nil || st.acting {
		return
	}
	st.acting, st.actErr = true, ""
	gen, char := st.gen, st.char
	ctx := a.base()
	go func() {
		err := do(ctx)
		a.post(func() {
			cur := &a.workbench.shell.sideContact
			if cur.gen != gen || cur.char != char {
				return
			}
			cur.acting = false
			if err != nil {
				cur.actErr = err.Error()
			}
			cur.due = time.Time{} // 下一帧就重读
		})
	}()
}
