package main

// 设置 → 提醒（规格-提醒 第九节）：列出所有角色的提醒，她建的和主人建的一视同仁，来源用徽标标出。
// 一行：内容开头；下面一句“角色 · 下次 11-10 09:00 · 每天 · 发到本地 QQ”。按钮：详情、改、暂停或继续、取消、删除（先确认）。
// 结束的（响过了、取消了）收在“已结束”下面，只留详情和删除。顶上“新建提醒”和每行“改”共用一个编辑框，
// 错的时间在框里写原因、不关框。数据来自 api.LocalStatus.Reminders（和定期任务同一个订阅），动作走 api.ReminderControl。
// 控件都取自 winshell.Kit，不另拼；没有提醒用 Kit.EmptyState。

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// remindCategoryID 是设置左栏里“提醒”这一项的编号；排在“通知”后面、“定期任务”前面。
const remindCategoryID = "reminders"

// 编辑框里“重复”的几个固定选项；已有的其他周期多一个选项，显示原样。
var remindRepeatNames = []string{"不重复", "每天", "工作日", "每周", "每月"}

const remindRepeatCustom = 5

// wbRemindState 是提醒页自己的界面状态。
type wbRemindState struct {
	page      bool            // 设置页当前选的是“提醒”
	open      map[string]bool // 展开了详情的提醒
	endedOpen bool            // “已结束”展开了
	delOpen   bool            // 删除确认框开着
	del       remindRef
	err       string
	ed        remindEdit
	// 提醒开没开在后台线程里问，问到了存这里；构建只读这一份（以前每帧同步问，后台卡住时每帧等 2 秒，2026-11-12）
	opts        api.ReminderOptions
	optsAt      time.Time // 上次问到的时间，零值是还没问到
	optsLoading bool
}

// remindOptsRefresh 是多久重问一次提醒开没开（配置改了，过一会儿页面就跟上）
const remindOptsRefresh = 10 * time.Second

// remindEdit 是编辑框：id 为 0 是新建。
type remindEdit struct {
	open    bool
	id      int
	key     api.ConversationKey // 改：提醒所在的会话；新建：由 charSel 选
	charSel int
	text    string
	at      string
	rep     int
	custom  api.ReminderRepeat // 选项“原样”时用的周期
	qq      bool
	baseAt  string // 打开时的时间文字：没改就不传，免得把到点的秒数改掉
	err     string
	busy    bool
}

// remindRef 指一条提醒：会话加编号。
type remindRef struct {
	Key  api.ConversationKey
	ID   int
	Name string
}

func (r remindRef) id() string { return fmt.Sprintf("%s#%d", r.Key.Conversation, r.ID) }

type remindRow struct {
	key  api.ConversationKey
	info api.ReminderInfo
}

func endedStatus(s string) bool { return s == "done" || s == "cancelled" }

// remindRows 汇总所有角色的提醒：没结束的按下次时间排，结束的按结束时间倒排。
func (a *app) remindRows() (live, ended []remindRow) {
	for _, s := range a.localStatus {
		for _, r := range s.Reminders {
			row := remindRow{key: s.Key, info: r}
			if endedStatus(r.Status) {
				ended = append(ended, row)
			} else {
				live = append(live, row)
			}
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].info.NextAt != live[j].info.NextAt {
			return live[i].info.NextAt < live[j].info.NextAt
		}
		return live[i].info.ID < live[j].info.ID
	})
	sort.Slice(ended, func(i, j int) bool { return ended[i].info.EndedAt > ended[j].info.EndedAt })
	return live, ended
}

// remindOptions 返回上次问到的选项；过期了、又没在问，就在后台线程里问一次，问到了经 a.post 写回、重画。
// 界面线程上不调 ReminderOptions：它会走到后台，后台卡住时要等满超时。
func (a *app) remindOptions(c *ui.Context) api.ReminderOptions {
	st := &a.remindUI
	ctl, ok := a.reader.(api.ReminderControl)
	if !ok {
		return api.ReminderOptions{}
	}
	if !st.optsLoading && (st.optsAt.IsZero() || c.Now().Sub(st.optsAt) >= remindOptsRefresh) {
		st.optsLoading = true
		at, gen := c.Now(), a.srcGen // 和上面判过期用同一个钟；换了数据源，旧后台的结果不要（同 loadSettings）
		go func() {
			o := ctl.ReminderOptions()
			a.post(func() {
				if a.srcGen != gen {
					return
				}
				st.opts, st.optsAt, st.optsLoading = o, at, false
			})
		}()
	}
	c.After(remindOptsRefresh)
	return st.opts
}

func (a *app) wbReminders(c *ui.Context, kit *winshell.Kit) {
	st := &a.remindUI
	live, ended := a.remindRows()
	opts := a.remindOptions(c)
	_, hasCtl := a.reader.(api.ReminderControl)
	asked := !st.optsAt.IsZero() || !hasCtl // 没有后台入口时直接说没开，和以前一样
	if st.err != "" {
		kit.Note(c, st.err, true)
	}
	ui.Row(c.Key("remind-top")).FillWidth().Gap(kit.G.SpaceSmall).Children(func() {
		kit.Button(c, "remind-new", "新建提醒", true, !opts.Enabled || len(a.chars) == 0, func() { a.remindNew() })
		if asked && !opts.Enabled { // 还没问到时不说“没开”，免得一打开页面先闪一下
			kit.Note(c, "提醒功能没开（reminders.enabled），已有的不会响。", false)
		}
	})
	if len(live) == 0 && len(ended) == 0 {
		kit.EmptyState(c, "remind-empty", "还没有提醒", "点上面的新建，或者在聊天里让她记一个", "")
	}
	now := time.Now()
	for _, r := range live {
		a.wbRemindRow(c, kit, r, now)
	}
	if len(ended) > 0 {
		kit.DisclosureRow(c, "remind-ended", fmt.Sprintf("已结束（%d）", len(ended)), "", st.endedOpen, false, func() { st.endedOpen = !st.endedOpen }, func() {})
		kit.Disclose(c, "remind-ended-rows", st.endedOpen, kit.G.PaneInset, func() { // 展开收起：高度弹簧加淡变（设置页的内容列间距是 PaneInset）
			for _, r := range ended {
				a.wbRemindRow(c, kit, r, now)
			}
		})
	}
	a.wbRemindEditor(c, kit, opts)
	kit.ActionDialog(c, "remind-delete", &st.delOpen, winshell.DialogSpec{
		Title: "删除提醒",
		Actions: []winshell.DialogAction{
			{ID: "cancel", Label: "取消", Default: true}, // 默认按钮放“取消”：回车不会误删
			{ID: "delete", Label: "删除"},
		},
		OnAction: func(id string) {
			if id == "delete" {
				a.remindAct("delete", st.del.Key, st.del.ID)
			}
		},
	}, func() {
		kit.Note(c, "“"+st.del.Name+"”会从列表里拿掉，不能恢复。", false)
	})
}

func (a *app) wbRemindRow(c *ui.Context, kit *winshell.Kit, r remindRow, now time.Time) {
	st := &a.remindUI
	t := r.info
	ref := remindRef{Key: r.key, ID: t.ID, Name: clipRunes(t.Text, 30)}
	key := "remind-" + ref.id()
	open := st.open[ref.id()]
	ended := endedStatus(t.Status)
	paused := t.Status == "paused"
	kit.Row(c, key, ref.Name, a.remindSummary(r, now), func() {
		ui.Row(c.Key(key + "-actions")).Shrink(0).Gap(kit.G.SpaceTight).AlignItems(ui.Center).Children(func() {
			kit.Badge(c, key+"-src", remindSourceText(t.Source), winshell.BadgeNeutral)
			label := "详情"
			if open {
				label = "收起"
			}
			kit.Button(c, key+"-detail", label, false, false, func() {
				if st.open == nil {
					st.open = map[string]bool{}
				}
				st.open[ref.id()] = !open
			})
			if !ended {
				kit.Button(c, key+"-edit", "改", false, false, func() { a.remindEdit(r) })
				if paused {
					kit.Button(c, key+"-resume", "继续", false, false, func() { a.remindAct("resume", ref.Key, ref.ID) })
				} else {
					kit.Button(c, key+"-pause", "暂停", false, false, func() { a.remindAct("pause", ref.Key, ref.ID) })
				}
				kit.Button(c, key+"-cancel", "取消", false, false, func() { a.remindAct("cancel", ref.Key, ref.ID) })
			}
			kit.Button(c, key+"-remove", "删除", false, false, func() { st.del, st.delOpen = ref, true })
		})
	})
	if !open {
		return
	}
	g, p := kit.G, kit.P
	ui.Column(c.Key(key + "-history")).FillWidth().Gap(g.SpaceSmall).Padding(g.ControlInset).Radius(g.RadiusControl).Background(p.Bubble).Children(func() {
		kit.KeyValues(c, key+"-facts", remindFacts(t, now))
		if len(t.History) == 0 {
			kit.Note(c, "还没有记录", false)
			return
		}
		kit.KeyValues(c, key+"-runs", remindEventLines(t.History, now))
	})
}

func remindSourceText(s string) string {
	switch s {
	case "owner":
		return "你建的"
	case "thread":
		return "她主动建的"
	}
	return "她建的"
}

// remindSummary 是行下面的一句：角色 · 下次（暂停、结束时写状态）· 重复 · 发到哪。
func (a *app) remindSummary(r remindRow, now time.Time) string {
	t := r.info
	parts := []string{a.loopOwner(r.key)}
	switch t.Status {
	case "paused":
		parts = append(parts, "已暂停")
	case "done":
		parts = append(parts, "响过了")
	case "cancelled":
		parts = append(parts, "已取消")
	default:
		parts = append(parts, "下次 "+loopClock(t.NextAt, now))
	}
	if t.RepeatText != "" {
		parts = append(parts, t.RepeatText)
	}
	return strings.Join(parts, " · ") + " · " + remindTargetsText(t.EffectiveTargets)
}

func remindTargetsText(ts []string) string {
	var names []string
	for _, t := range ts {
		switch t {
		case "local":
			names = append(names, "本地")
		case "qq":
			names = append(names, "QQ")
		}
	}
	if len(names) == 0 {
		names = []string{"本地"}
	}
	return "发到" + strings.Join(names, " ")
}

func remindFacts(t api.ReminderInfo, now time.Time) []winshell.KeyValue {
	facts := []winshell.KeyValue{{Key: "内容", Value: t.Text}, {Key: "已响", Value: fmt.Sprintf("%d 次", t.Rings)}}
	if t.LastNote != "" {
		facts = append(facts, winshell.KeyValue{Key: "上次", Value: t.LastNote})
	}
	if t.EndNote != "" {
		facts = append(facts, winshell.KeyValue{Key: "结束", Value: t.EndNote})
	}
	return facts
}

var remindKinds = map[string]string{"create": "建立", "edit": "修改", "pause": "暂停", "resume": "继续", "ring": "响了", "done": "完成", "cancel": "取消", "defer": "顺延"}

// remindEventLines 是最近的记录，新的在前；每条一行：时间、做了什么、谁做的、备注。
func remindEventLines(h []api.ReminderEvent, now time.Time) []winshell.KeyValue {
	var out []winshell.KeyValue
	seen := map[string]bool{}
	for i := len(h) - 1; i >= 0 && len(out) < 20; i-- {
		e := h[i]
		text := remindKinds[e.Kind]
		if text == "" {
			text = e.Kind
		}
		if e.Note != "" {
			text += "：" + e.Note
		}
		k := loopClock(e.At, now)
		for seen[k] { // KeyValues 拿名字当元素键，同一分钟有两条时补空格错开
			k += " "
		}
		seen[k] = true
		out = append(out, winshell.KeyValue{Key: k, Value: text})
	}
	return out
}

// ───────────── 编辑框 ─────────────

func remindRepeatIndex(r api.ReminderRepeat) int {
	switch {
	case r.Unit == "":
		return 0
	case r.Unit == "day" && r.Every <= 1:
		return 1
	case r.Unit == "week" && r.Every <= 1 && slices.Equal(r.Weekdays, []int{1, 2, 3, 4, 5}):
		return 2
	case r.Unit == "week" && r.Every <= 1 && len(r.Weekdays) == 0:
		return 3
	case r.Unit == "month" && r.Every <= 1:
		return 4
	}
	return remindRepeatCustom
}

func remindRepeatOf(i int, custom api.ReminderRepeat) api.ReminderRepeat {
	switch i {
	case 1:
		return api.ReminderRepeat{Unit: "day", Every: 1}
	case 2:
		return api.ReminderRepeat{Unit: "week", Every: 1, Weekdays: []int{1, 2, 3, 4, 5}}
	case 3:
		return api.ReminderRepeat{Unit: "week", Every: 1}
	case 4:
		return api.ReminderRepeat{Unit: "month", Every: 1}
	case remindRepeatCustom:
		return custom
	}
	return api.ReminderRepeat{}
}

func remindLocalKey(char string) api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + char}
}

const remindTimeLayout = "2006-01-02 15:04"

// remindNew 打开空白的编辑框：时间预填下一个整点。
func (a *app) remindNew() {
	next := time.Now().Truncate(time.Hour).Add(time.Hour)
	a.remindUI.ed = remindEdit{open: true, at: next.Format(remindTimeLayout)}
}

// remindEdit 打开这条提醒的编辑框。
func (a *app) remindEdit(r remindRow) {
	t := r.info
	at := time.UnixMilli(t.NextAt).In(time.Local).Format(remindTimeLayout)
	ed := remindEdit{open: true, id: t.ID, key: r.key, text: t.Text, at: at, baseAt: at, rep: remindRepeatIndex(t.Repeat), custom: t.Repeat,
		qq: slices.Contains(t.EffectiveTargets, "qq")}
	a.remindUI.ed = ed
}

func (a *app) wbRemindEditor(c *ui.Context, kit *winshell.Kit, opts api.ReminderOptions) {
	st := &a.remindUI
	ed := &st.ed
	title := "新建提醒"
	if ed.id != 0 {
		title = "修改提醒"
	}
	kit.ActionDialog(c, "remind-edit", &ed.open, winshell.DialogSpec{
		Title: title,
		Actions: []winshell.DialogAction{
			{ID: "save", Label: "保存", Default: true, KeepOpen: true, Disabled: ed.busy},
			{ID: "cancel", Label: "取消"},
		},
		OnAction: func(id string) {
			if id == "save" {
				a.remindSave(opts)
			}
		},
	}, func() {
		g := kit.G
		ui.Column(c.Key("remind-form")).FillWidth().Gap(g.SpaceSmall).Children(func() {
			if ed.id == 0 && len(a.chars) > 1 {
				names := make([]string, len(a.chars))
				for i, ch := range a.chars {
					names[i] = ch.Name
				}
				kit.Chips(c, "remind-char", names, &ed.charSel)
			}
			kit.TextArea(c, "remind-text", &ed.text, "提醒内容", 2, 5)
			kit.TextField(c, "remind-at", &ed.at, "时间（2026-11-10 09:00）")
			labels := slices.Clone(remindRepeatNames)
			if ed.custom.Unit != "" && remindRepeatIndex(ed.custom) == remindRepeatCustom {
				labels = append(labels, remindCustomText(ed.custom))
			}
			kit.Chips(c, "remind-rep", labels, &ed.rep)
			local := true
			kit.Check(c, "remind-to-local", "发到本地私聊", &local, true)
			kit.Check(c, "remind-to-qq", "发到 QQ 私聊", &ed.qq, !opts.AllowQQ)
			if !opts.AllowQQ {
				kit.Note(c, "QQ 投递没开（reminders.qq）", false)
			}
			if ed.err != "" {
				kit.Note(c, ed.err, true)
			}
		})
	})
}

func remindCustomText(r api.ReminderRepeat) string {
	unit := map[string]string{"minute": "分钟", "hour": "小时", "day": "天", "week": "周", "month": "个月"}[r.Unit]
	every := max(r.Every, 1)
	s := fmt.Sprintf("每 %d %s", every, unit)
	if len(r.Weekdays) > 0 {
		s += fmt.Sprint(r.Weekdays)
	}
	return s
}

// remindSave 校验后在后台线程里调 ReminderControl；成功才关框，出错写在框里。
func (a *app) remindSave(opts api.ReminderOptions) {
	st := &a.remindUI
	ed := &st.ed
	ed.err = ""
	text := strings.TrimSpace(ed.text)
	if text == "" {
		ed.err = "写一下要提醒什么"
		ed.open = true
		return
	}
	ctl, ok := a.reader.(api.ReminderControl)
	if !ok {
		ed.err = "后台没有开着，暂时不能改提醒"
		ed.open = true
		return
	}
	var targets []string
	targets = append(targets, "local")
	if ed.qq && opts.AllowQQ {
		targets = append(targets, "qq")
	}
	rep := remindRepeatOf(ed.rep, ed.custom)
	at, key, id, baseAt := strings.TrimSpace(ed.at), ed.key, ed.id, ed.baseAt
	if id == 0 {
		if len(a.chars) == 0 {
			ed.err = "没有角色，不能建提醒"
			ed.open = true
			return
		}
		key = remindLocalKey(a.chars[min(ed.charSel, len(a.chars)-1)].ID)
	}
	ed.busy, ed.open = true, true
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var err error
		if id == 0 {
			err = ctl.CreateReminder(ctx, key, api.ReminderDraft{Text: text, At: at, Repeat: rep, Targets: targets})
		} else {
			p := api.ReminderPatch{Text: &text, Repeat: &rep, Targets: &targets}
			if at != baseAt {
				p.At = &at
			}
			err = ctl.UpdateReminder(ctx, key, id, p)
		}
		a.post(func() {
			ed.busy = false
			if err != nil {
				ed.err = err.Error()
				return
			}
			ed.open = false
		})
	}()
}

// remindAct 在后台线程里调 ReminderControl；结果靠状态订阅回来，出错写在页顶。
func (a *app) remindAct(kind string, k api.ConversationKey, id int) {
	a.remindUI.err = ""
	ctl, ok := a.reader.(api.ReminderControl)
	if !ok {
		a.remindUI.err = "后台没有开着，暂时不能改提醒"
		return
	}
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var err error
		yes, no := true, false
		switch kind {
		case "pause":
			err = ctl.UpdateReminder(ctx, k, id, api.ReminderPatch{Paused: &yes})
		case "resume":
			err = ctl.UpdateReminder(ctx, k, id, api.ReminderPatch{Paused: &no})
		case "cancel":
			err = ctl.FinishReminder(ctx, k, id, false)
		case "delete":
			err = ctl.DeleteReminder(ctx, k, id)
		}
		if err != nil && a.postFn != nil {
			a.post(func() { a.remindUI.err = err.Error() })
		}
	}()
}
