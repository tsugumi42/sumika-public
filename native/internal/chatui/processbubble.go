package chatui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 过程气泡（ADR-287，docs/规格-过程组件.md 第二节）：她这一轮在两段回复之间的思考、读搜、命令、改文件折进一个气泡。
// 收起时只有摘要一行；展开后是 processBody 的各段。它就是她的一个气泡：底色、内距、圆角、相贴、名字、头像都照气泡。

// Summary 是过程气泡摘要行要写的东西。
type Summary struct {
	Text      string        // 一句话：进行中写在干什么，做完写总结
	Icon      string        // 左边的图标：做完是这一轮最主要的那一类，进行中是正在做的那一类（展开时摘要行不转圈，放它）
	Running   bool          // 还有没结束的步骤
	Thinking  bool          // 正在进行的是思考（左边用三个点，不用转圈）
	Failed    bool          // 有失败的步骤（挂失败角标）
	Pending   bool          // 有等批准的步骤（自动展开、不能收起）
	Remaining time.Duration // 等批准时的剩余时间（0 不显示）
	// Elapsed 是这一轮各步用时加起来的秒数（只在做完、且比"想了 N 秒"多出别的步骤用时的时候才有；0 表示不写）。
	// 各步是依次做的，加起来就是过程占的时间；缺数据的步骤（Seconds 为 0）不猜，所以可能比实际少。
	Elapsed int
}

// Summarize 给一轮过程写摘要。
func Summarize(steps []*Step) Summary {
	var s Summary
	for _, st := range steps {
		if st.State == StepFailed {
			s.Failed = true
		}
	}
	// 等批准的优先：只要有一步在等，就写那一步（气泡也因此展开），不管后面还有别的在跑
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		if a := st.Approval; st.State == StepRunning && a != nil && (a.State == ApprovalPending || a.State == ApprovalSubmitting) {
			// 等批准时气泡是展开的，下面那块头部写着"想运行 / 想改 什么"；摘要只说在等你，不重复
			s.Running, s.Pending, s.Remaining = true, true, a.Remaining
			s.Text, s.Icon = "等你批准", "terminal"
			if st.Kind == StepEdit {
				s.Icon = "pen"
			}
			return s
		}
	}
	// 进行中：取最后一个还没结束的步骤
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		if st.State != StepRunning {
			continue
		}
		s.Running = true
		switch st.Kind {
		case StepThink:
			s.Text, s.Thinking, s.Icon = "正在想", true, "brain"
		case StepRead:
			s.Text, s.Icon = "正在读 "+st.Target, "file"
		case StepSearch:
			s.Text, s.Icon = "正在搜 "+st.Target, "search"
		case StepCommand:
			s.Text, s.Icon = "正在运行 "+firstLine(st.Target), "terminal"
		case StepEdit:
			s.Text, s.Icon = "正在改 "+st.Target, "pen"
		case StepTool:
			s.Text, s.Icon = "正在调用 "+st.Tool, "tool"
		}
		return s
	}
	// 做完：按出现过的种类写总结，用"·"连起来
	var secs, reads, searches, ran, notRan, edited, notEdited, recalled int
	var called []string
	thought := false
	for _, st := range steps {
		switch st.Kind {
		case StepThink:
			thought = true
			secs += st.Seconds
		case StepRead:
			reads++
		case StepSearch:
			searches++
		case StepCommand:
			if notRun(st) {
				notRan++
			} else {
				ran++
			}
		case StepEdit:
			if notRun(st) {
				notEdited++
			} else {
				edited++
			}
		case StepTool:
			called = append(called, st.Tool)
		case StepRecall: // 自动召回不是工具，不进“调用了”的数
			recalled += len(st.Recall)
		}
	}
	total := 0
	for _, st := range steps {
		total += max(st.Seconds, 0)
	}
	if total > secs {
		s.Elapsed = total
	}
	var parts []string
	if recalled > 0 {
		parts = append(parts, fmt.Sprintf("召回 %d 条记忆", recalled))
		s.Icon = "bookmark" // 先放：后面有别的种类会盖掉它，图标留给更主要的那一类
	}
	if thought {
		if secs > 0 {
			parts = append(parts, fmt.Sprintf("想了 %d 秒", secs))
		} else {
			parts = append(parts, "想了一下")
		}
		s.Icon = "brain"
	}
	var explore []string
	if reads > 0 {
		explore = append(explore, fmt.Sprintf("读了 %d 个文件", reads))
		s.Icon = "file"
	}
	if searches > 0 {
		explore = append(explore, fmt.Sprintf("搜了 %d 次", searches))
		s.Icon = "search"
	}
	if len(explore) > 0 {
		parts = append(parts, strings.Join(explore, "、"))
	}
	if ran > 0 {
		parts = append(parts, fmt.Sprintf("运行了 %d 条命令", ran))
		s.Icon = "terminal"
	}
	if notRan > 0 {
		parts = append(parts, fmt.Sprintf("%d 条命令没运行", notRan))
		s.Icon = "terminal"
	}
	if edited > 0 {
		parts = append(parts, fmt.Sprintf("改了 %d 个文件", edited))
		s.Icon = "pen"
	}
	if notEdited > 0 {
		parts = append(parts, fmt.Sprintf("%d 个文件没改", notEdited))
		s.Icon = "pen"
	}
	switch len(called) {
	case 0:
	case 1:
		parts = append(parts, "调用了 "+called[0]) // 就一个就写它的名字，一眼看出调了什么
		s.Icon = "tool"
	default:
		parts = append(parts, fmt.Sprintf("调用了 %d 个工具", len(called)))
		s.Icon = "tool"
	}
	s.Text = strings.Join(parts, " · ")
	return s
}

// elapsedText 把秒数写成"45 秒""1 分 5 秒"。
func elapsedText(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%d 秒", sec)
	}
	if sec%60 == 0 {
		return fmt.Sprintf("%d 分", sec/60)
	}
	return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
}

// processOpen 是过程气泡现在展开没有（ADR-287、288）：等批准时展开、点了也不收（审批不能藏）；
// 设置"进行中展开过程"开着、这一轮还在进行时展开——进行中手动收起的记在 dismissed，做完以后不会自己再展开（照 Alma 把"手动收起"单独记）；
// 其余时候默认收起，*o 记手动展开了没有。
func (h Host) processOpen(sum Summary, o, dismissed *bool) bool {
	switch {
	case sum.Pending:
		return true
	case h.LiveOpen && sum.Running:
		return dismissed == nil || !*dismissed
	default:
		return o != nil && *o
	}
}

// processBubble 画过程气泡。
func (h Host) processBubble(c *ui.Context, m *Msg, l layout, acc Accent) {
	t := h.T
	h.procChar = m.Char
	pv := t.BubblePadV
	side := t.BubblePadH - pv
	sum := Summarize(m.Steps)
	var o, dismissed *bool
	if h.StepOpen != nil {
		o, dismissed = h.StepOpen(m.ID), h.StepOpen(m.ID+"#进行中收起")
	}
	open := h.processOpen(sum, o, dismissed)
	live := h.LiveOpen && sum.Running && !sum.Pending // 这时候点摘要记的是"进行中收起"
	sole := len(Segments(m.Steps)) == 1
	soleThink := sole && m.Steps[0].Kind == StepThink // 只有一段思考：摘要就是它的标题，展开直接是正文

	// 圆角照气泡：收起且没有名字时是一行，按单行气泡（胶囊）；展开后按多行
	height := pv*2 + t.BodyLine
	if l.ShowName {
		height += t.NameLine
	}
	single := !open && !l.ShowName
	if open {
		height = 1 << 20
	}
	corners := BubbleCorners(t, false, l.JoinPrev, l.JoinNext, single, height)

	// 宽度按内容：收起时是摘要那一行，展开时是里面最宽的一行或块，最宽到过程最大宽（再长的折行）。
	// 展开且还在进行时只变宽、不变窄：输出滚动、"正在运行"换成"运行了"时不来回跳；做完或收起后重新按内容。
	maxW := min(h.processWidth(), l.MaxW)
	// 气泡自己不留内距：摘要行铺满气泡，悬停底和点击区到气泡边（主人 2026-11-09）；内距各部分自己留
	e := ui.Column(c.Key("bubble")).Label("过程").MinWidth(0).MaxWidth(maxW).Background(t.C.Bubble).Radius(corners[:]...)
	grow := ui.Local(e, "grow", func() procGrow { return procGrow{} })
	e.Bind(&grow.h)
	if open && sum.Running {
		grow.w = min(max(grow.w, grow.h.Bounds(c).W), maxW)
		e.MinWidth(grow.w)
	} else {
		grow.w = 0
	}
	if t.S.Wallpaper {
		e.Shadow(0, 0.5, 1, 0, t.C.ShadowA).Shadow(0, 1, 3, 0, t.C.ShadowB)
	}
	e.Children(func() {
		if l.ShowName {
			ui.Text(c.Key("name"), m.Name).Font(h.Font).FontSize(t.FS[3]).FontWeight(t.Weight[2]).FixedLineHeight(t.NameLine).
				TextColor(acc.Text).Padding(pv, side+pv, 0, side+pv).SingleLine().Translate(0, textNudge(t.FS[3]))
		}
		row := ui.ButtonBase(c.Key("summary")).Label("过程摘要") // 等批准时点了不收（不设禁用：禁用会把整行调淡，看着像坏了）
		switch {
		case sum.Pending: // 等批准时点了不收
		case live && dismissed != nil:
			row.OnClick(func() { *dismissed = !*dismissed })
		case o != nil:
			row.OnClick(func() { *o = !*o })
		}
		bg := ui.Transparent
		if !sum.Pending && row.Pressed() {
			bg = t.C.Press
		} else if !sum.Pending && row.Hovered() {
			bg = t.C.Hover
		}
		// 有名字时上面贴着名字不再留一层；展开时下面接内容，下边不圆
		rowTop, rowBottom, rowCorners := pv, pv, corners
		if l.ShowName {
			rowTop, rowCorners[0], rowCorners[1] = 0, 0, 0
		}
		if open {
			rowCorners[2], rowCorners[3] = 0, 0
		}
		for i := range rowCorners { // 圆角不超过半高：大圆角外观下一行高的摘要不能画成比胶囊还圆
			rowCorners[i] = min(rowCorners[i], (t.BodyLine+rowTop+rowBottom)/2)
		}
		row.Background(t.Fill(row, bg)).MinHeight(t.BodyLine+rowTop+rowBottom).Padding(rowTop, pv, rowBottom, pv).Gap(t.S2).AlignItems(ui.Center).Justify(ui.Start).Radius(rowCorners[:]...)
		if open {
			row.FillWidth()
		}
		row.Children(func() {
			// 图标格子从气泡内距 pv 起，左缘对齐气泡正文（leadCol）
			h.lead(c, pv, t.S2, func() {
				size := h.leadIcon(pv, h.rowLine()*0.8)
				switch {
				case sum.Pending || (open && sum.Running && !soleThink): // 在等你、或展开着进行中（下面那一行在转）：不转圈，放这一步的图标
					ui.Icon(c, Icon(sum.Icon, false)).Size(size, size).TextColor(t.AccentOf(m.Char).Text)
				case sum.Running && sum.Thinking:
					dot := h.leadSlot(pv) * typingDotBase / (3*typingDotBase + 2*typingGapBase)
					ui.Row(c.Key("dots")).Gap(dot * typingGapBase / typingDotBase).AlignItems(ui.Center).Children(func() { h.dotsSized(c, dot) })
				case sum.Running:
					ui.Spinner(c.Key("spin")).Size(size, size).TextColor(t.C.Text3)
				default:
					ui.Icon(c, Icon(sum.Icon, false)).Size(size, size).TextColor(t.AccentOf(m.Char).Text)
				}
			})
			text := sum.Text
			if open && sum.Running && !sum.Pending && !soleThink {
				text = "进行中" // 展开时在做哪一步看下面那一行，摘要不重复
			}
			h.rowText(c, ui.Span{Text: text, Features: Tabular}).Grow(1)
			// 右边：失败角标、等批准的剩余时间、箭头；最右边离气泡边和左边一样（side）
			ui.Row(c.Key("meta")).Shrink(0).Gap(t.S1).AlignItems(ui.Center).Margin(0, side, 0, 0).Children(func() {
				if sum.Failed && !open { // 展开后哪一步失败由那一行自己挂角标，摘要行不再挂一个（主人 2026-11-07："那俩感叹号只留一个"）
					ui.Icon(c.Key("badge"), Icon("alert", true)).Size(t.Badge, t.Badge).TextColor(t.C.Danger).Label("出错了")
				}
				if sum.Elapsed > 0 && !sum.Running { // 做完了才写用时；进行中、等批准时不写
					ui.Text(c.Key("elapsed"), "用时 "+elapsedText(sum.Elapsed)).Font(h.Font).FontSize(t.FS[1]).FontFeatures(Tabular).TextColor(t.C.Text3).SingleLine()
				}
				if sum.Pending && sum.Remaining > 0 {
					ui.Icon(c, Icon("time", false)).Size(t.FS[1], t.FS[1]).TextColor(t.C.Text3)
					sec := int((sum.Remaining + time.Second - 1) / time.Second) // 向上取整：到 0 才是 0:00
					ui.Text(c, fmt.Sprintf("%d:%02d", sec/60, sec%60)).Font(h.Font).FontSize(t.FS[1]).FontFeatures(Tabular).TextColor(t.C.Text3)
				}
				if !sum.Pending {
					deg := float32(0)
					if open {
						deg = 90
					}
					ui.Icon(c.Key("chevron"), Icon("right", false)).Size(t.FS[2], t.FS[2]).TextColor(t.C.Text3).Rotate(row.AnimateTo("chevron", deg, ui.Animation{Duration: t.Fast}))
				}
			})
		})
		switch {
		case !open:
		case soleThink:
			if m.Steps[0].Thought != "" {
				ui.Column(c.Key("steps")).MinWidth(0).Margin(0, pv, pv, pv).Children(func() { h.thoughtBody(c, m.Steps[0]) })
			}
		default:
			h.soleSegment = sole
			ui.Column(c.Key("steps-pad")).MinWidth(0).Margin(0, pv, pv, pv).Children(func() {
				h.processBody(c, "steps", m.Char, m.Steps, h.StepOpen)
			})
		}
	})
}

// procGrow 是过程气泡进行中记下的最大宽（Bounds 读上一帧定下的框）。
type procGrow struct {
	h ui.Handle
	w float32
}
