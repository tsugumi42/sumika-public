package main

// 设置 → 定期任务：列出所有本地私聊会话里没停的定期任务（/loop 建的），一行一个：
// 名字（任务文字的开头）、所在角色、间隔、下次时间、上次结果；可以暂停、继续、删除（先确认），
// 点“详情”展开最近的运行历史。数据来自 api.LocalStatus.LoopTasks（和输入框上方那行同一份订阅），
// 动作走 api.LoopControl（和输入框 /loop 同一套命令逻辑）。控件都取自 winshell.Kit，不另拼；
// 没有任务用 Kit.EmptyState。规格-目标和循环 第三节。

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// loopsCategoryID 是设置左栏里“定期任务”这一项的编号；其余分类的编号是 settingsTabs 的下标，它排在“通知”后面。
const loopsCategoryID = "loops"

// wbLoopsState 是定期任务页自己的界面状态：是否选中、哪几个展开了历史、要删哪一个、上一次动作的错误。
type wbLoopsState struct {
	page    bool            // 设置页当前选的是“定期任务”
	open    map[string]bool // 展开了运行历史的任务（键：会话#编号）
	delOpen bool            // 删除确认框开着
	del     loopRef
	err     string
	ed      loopEdit
}

// loopRef 指一个任务：会话加编号。
type loopRef struct {
	Key  api.ConversationKey
	ID   int
	Name string // 确认框里写的名字
}

func (r loopRef) id() string { return fmt.Sprintf("%s#%d", r.Key.Conversation, r.ID) }

// loopRow 是页上的一行：任务加它所在的会话。
type loopRow struct {
	key  api.ConversationKey
	info api.LoopInfo
}

// loopRows 汇总所有会话里的定期任务，按会话、编号排。
func (a *app) loopRows() []loopRow {
	var out []loopRow
	for _, s := range a.localStatus {
		for _, t := range s.LoopTasks {
			out = append(out, loopRow{key: s.Key, info: t})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].key.Conversation != out[j].key.Conversation {
			return out[i].key.Conversation < out[j].key.Conversation
		}
		return out[i].info.ID < out[j].info.ID
	})
	return out
}

func (a *app) wbLoops(c *ui.Context, kit *winshell.Kit) {
	st := &a.loopsUI
	rows := a.loopRows()
	if st.err != "" {
		kit.Note(c, st.err, true)
	}
	ui.Row(c.Key("loops-top")).FillWidth().Gap(kit.G.SpaceSmall).Children(func() {
		kit.Button(c, "loops-new", "新建定期任务", true, len(a.chars) == 0, func() { a.loopNew() })
	})
	if len(rows) == 0 {
		kit.EmptyState(c, "loops-empty", "还没有定期任务", "点上面的新建，或者在聊天里输入 /loop 30m 任务", "")
	}
	now := time.Now()
	for _, r := range rows {
		a.wbLoopRow(c, kit, r, now)
	}
	a.wbLoopEditor(c, kit)
	// 删除确认：任务只是停掉，不再按时运行，列表里也不再出现
	kit.ActionDialog(c, "loop-delete", &st.delOpen, winshell.DialogSpec{
		Title: "删除定期任务",
		Actions: []winshell.DialogAction{
			{ID: "cancel", Label: "取消", Default: true}, // 默认按钮放“取消”：回车不会误删
			{ID: "delete", Label: "删除"},
		},
		OnAction: func(id string) {
			if id == "delete" {
				a.loopAct("remove", st.del.Key, st.del.ID)
			}
		},
	}, func() {
		kit.Note(c, "“"+st.del.Name+"”会停止，不再按时运行。", false)
	})
}

func (a *app) wbLoopRow(c *ui.Context, kit *winshell.Kit, r loopRow, now time.Time) {
	st := &a.loopsUI
	t := r.info
	ref := loopRef{Key: r.key, ID: t.ID, Name: clipRunes(t.Prompt, 30)}
	key := "loop-" + ref.id()
	open := st.open[ref.id()]
	paused := t.Status == "paused"
	kit.Row(c, key, ref.Name, a.loopSummary(r, now), func() {
		ui.Row(c.Key(key + "-actions")).Shrink(0).Gap(kit.G.SpaceTight).Children(func() {
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
			kit.Button(c, key+"-edit", "编辑", false, false, func() { a.loopEditOpen(r) })
			if paused {
				kit.Button(c, key+"-resume", "继续", false, false, func() { a.loopAct("resume", ref.Key, ref.ID) })
			} else {
				kit.Button(c, key+"-pause", "暂停", false, false, func() { a.loopAct("pause", ref.Key, ref.ID) })
			}
			kit.Button(c, key+"-remove", "删除", false, false, func() { st.del, st.delOpen = ref, true })
		})
	})
	if !open {
		return
	}
	g, p := kit.G, kit.P
	ui.Column(c.Key(key + "-history")).FillWidth().Gap(g.SpaceSmall).Padding(g.ControlInset).Radius(g.RadiusControl).Background(p.Bubble).Children(func() {
		kit.KeyValues(c, key+"-facts", loopFacts(t, now))
		if len(t.History) == 0 {
			kit.Note(c, "还没有运行记录", false)
			return
		}
		kit.KeyValues(c, key+"-runs", loopRunLines(t.History, now))
	})
}

// loopAct 在后台线程里调 LoopControl；结果靠状态订阅回来（任务变了就是新的一帧），出错写在页顶。
func (a *app) loopAct(kind string, k api.ConversationKey, id int) {
	a.loopsUI.err = ""
	ctl, ok := a.reader.(api.LoopControl)
	if !ok {
		a.loopsUI.err = "后台没有开着，暂时不能改定期任务"
		return
	}
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var err error
		switch kind {
		case "pause":
			err = ctl.PauseLoop(ctx, k, id)
		case "resume":
			err = ctl.ResumeLoop(ctx, k, id)
		case "remove":
			err = ctl.RemoveLoop(ctx, k, id)
		}
		if err != nil && a.postFn != nil {
			a.post(func() { a.loopsUI.err = err.Error() })
		}
	}()
}

// loopSummary 是行下面的一句：角色 · 间隔 · 下次（暂停时写已暂停）· 上次结果。
func (a *app) loopSummary(r loopRow, now time.Time) string {
	t := r.info
	parts := []string{a.loopOwner(r.key), loopCadence(t)}
	if t.Status == "paused" {
		parts = append(parts, "已暂停")
	} else if t.NextRun != 0 {
		parts = append(parts, "下次 "+loopClock(t.NextRun, now))
	}
	return strings.Join(parts, " · ") + " · " + loopLast(t)
}

// loopOwner 是任务所在的角色名（话题里的任务也算这个角色）；查不到退回会话号。
func (a *app) loopOwner(k api.ConversationKey) string {
	id := charOfConv(k)
	for _, ch := range a.chars {
		if ch.ID == id && ch.Name != "" {
			return ch.Name
		}
	}
	if id != "" {
		return id
	}
	return k.Conversation
}

func loopCadence(t api.LoopInfo) string {
	switch t.Kind {
	case "daily":
		return "每天 " + t.At
	case "weekly":
		var b strings.Builder
		for _, d := range t.Days {
			b.WriteString(loopDayNames[(d+6)%7])
		}
		return "每周" + b.String() + " " + t.At
	}
	if t.IntervalSec <= 0 {
		return "自定节奏"
	}
	d := time.Duration(t.IntervalSec) * time.Second
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("每 %d 天", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("每 %d 小时", d/time.Hour)
	}
	return fmt.Sprintf("每 %d 分钟", int(d.Round(time.Minute)/time.Minute))
}

// loopLast 是上次结果：有内容、无事、出错（带原因）、还没跑过。
func loopLast(t api.LoopInfo) string {
	switch t.LastResult {
	case "content":
		return "上次：有内容"
	case "quiet":
		return "上次：无事"
	case "error":
		if t.LastNote != "" {
			return "上次：出错（" + t.LastNote + "）"
		}
		return "上次：出错"
	}
	return "还没跑过"
}

// loopClock 是时间：今天只写时分，别的天带月日。
func loopClock(ms int64, now time.Time) string {
	at := time.UnixMilli(ms).In(now.Location())
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return at.Format("15:04")
	}
	return at.Format("01-02 15:04")
}

func loopFacts(t api.LoopInfo, now time.Time) []winshell.KeyValue {
	facts := []winshell.KeyValue{{Key: "任务", Value: t.Prompt}, {Key: "已跑", Value: fmt.Sprintf("%d 次", t.Runs)},
		{Key: "聊天", Value: loopChatNames[loopBool(t.NewChat)]}, {Key: "通知", Value: loopNotifyNames[loopNotifyIndex(t.Notify)]}}
	if t.NextReason != "" {
		facts = append(facts, winshell.KeyValue{Key: "节奏", Value: t.NextReason})
	}
	if t.Expires == 0 {
		facts = append(facts, winshell.KeyValue{Key: "到期", Value: "不自动停"})
	} else {
		facts = append(facts, winshell.KeyValue{Key: "到期", Value: loopClock(t.Expires, now)})
	}
	return facts
}

// loopRunLines 是最近的运行记录，新的在前，最多 20 条；每条一行：时间、结果、原因。
func loopRunLines(h []api.LoopRun, now time.Time) []winshell.KeyValue {
	var out []winshell.KeyValue
	seen := map[string]bool{}
	for i := len(h) - 1; i >= 0 && len(out) < 20; i-- {
		e := h[i]
		text := map[string]string{"content": "有内容", "quiet": "无事", "error": "出错"}[e.Result]
		if text == "" {
			text = e.Result
		}
		if e.Missed > 0 {
			text += fmt.Sprintf("，错过了 %d 次", e.Missed)
		}
		if e.Note != "" {
			text += "：" + e.Note
		}
		// KeyValues 拿名字当元素键，同一分钟有两条时键会撞：后来的在时间后面补空格（看不出来，键不同）
		k := loopClock(e.At, now)
		for seen[k] {
			k += " "
		}
		seen[k] = true
		out = append(out, winshell.KeyValue{Key: k, Value: text})
	}
	return out
}

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
