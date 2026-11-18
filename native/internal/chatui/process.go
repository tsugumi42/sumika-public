package chatui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 过程组件（docs/规格-过程组件.md）：角色这一轮回复前面的思考、读搜、命令、改文件、审批。
// 只在本地私聊、本地群聊显示（ADR-281）；状态机照规格-交互原型 3.4、3.5，这里只管长相。

// StepKind 是一步过程的种类。
type StepKind int

const (
	StepRead    StepKind = iota // 读文件
	StepSearch                  // 搜索
	StepThink                   // 思考
	StepCommand                 // 执行命令
	StepEdit                    // 写入、改文件
	StepTool                    // 其他工具：写清调用了哪个工具，别冒充“读文件”（主人 2026-11-09）
	StepRecall                  // 自动召回的记忆：不是工具调用，单独一种，图标和工具区分开（主人 2026-11-09）
)

// StepState 是一步的状态。
type StepState int

const (
	StepRunning StepState = iota
	StepDone
	StepFailed
	StepDenied  // 审批被拒、不允许
	StepStopped // 被停下
)

// Step 是一步过程；后台的数据结构由宿主转换过来。
type Step struct {
	ID     string
	Kind   StepKind
	State  StepState
	Target string // 路径、关键词、命令第一行
	Tool   string // 工具名
	// 思考
	Seconds int    // 用时（思考、命令）；0 表示不知道
	Thought string // 思考正文
	// 命令
	Output   string // 输出（宿主只留最后 20000 字）
	ExitCode int
	HasExit  bool
	// 改文件
	EditVerb       string // "改了""新建了""删了"；空当"改了"
	Added, Removed int
	Diff           string // 统一 diff 正文
	// 审批（挂在命令卡、改文件卡里）
	Approval *Approval
	// 自动召回：这一轮真正注入给模型的记忆，按注入的顺序
	Recall []RecallItem
}

// Segment 是排出来的一段：相邻的读和搜合成一段（Lorca agents.go:511-525），其余每步一段。
type Segment struct {
	Steps []*Step
}

// Merged 说明这一段是不是读搜合并行（两步以上的读、搜）。
func (s Segment) Merged() bool { return len(s.Steps) > 1 }

// Segments 按顺序把步骤分段：只合并相邻的读和搜，中间夹了别的种类就断开。
func Segments(steps []*Step) []Segment {
	var out []Segment
	for _, st := range steps {
		explore := st.Kind == StepRead || st.Kind == StepSearch
		if explore && len(out) > 0 {
			last := &out[len(out)-1]
			if k := last.Steps[0].Kind; k == StepRead || k == StepSearch {
				last.Steps = append(last.Steps, st)
				continue
			}
		}
		out = append(out, Segment{Steps: []*Step{st}})
	}
	return out
}

// exploreSummary 是读搜合并行的那句话：有一步还在跑就用进行时（"正在读 2 个文件、搜 1 次"），
// 否则过去时（"读了 3 个文件、搜了 2 次"）；只有一种就只写那一种（Lorca agents.go:540-557）。
func exploreSummary(steps []*Step) string {
	reads, searches, running := 0, 0, false
	for _, s := range steps {
		if s.Kind == StepRead {
			reads++
		} else {
			searches++
		}
		running = running || s.State == StepRunning
	}
	readVerb, searchVerb := "读了", "搜了"
	if running {
		readVerb, searchVerb = "正在读", "搜"
	}
	switch {
	case searches == 0:
		return fmt.Sprintf("%s %d 个文件", readVerb, reads)
	case reads == 0:
		if running {
			return fmt.Sprintf("正在搜 %d 次", searches)
		}
		return fmt.Sprintf("搜了 %d 次", searches)
	}
	return fmt.Sprintf("%s %d 个文件、%s %d 次", readVerb, reads, searchVerb, searches)
}

// stepLine 是单步那一行的动词和对象（进行时 / 过去时两套，Lorca agents.go:564-594）。
func stepLine(s *Step) (verb, object string) {
	if s.State == StepDenied {
		return "不允许：", s.Target
	}
	running := s.State == StepRunning
	switch s.Kind {
	case StepRead:
		if running {
			return "正在读 ", s.Target
		}
		return "读了 ", s.Target
	case StepSearch:
		if running {
			return "正在搜 ", s.Target
		}
		return "搜了 ", s.Target
	}
	return "", s.Target
}

func segmentFailed(steps []*Step) bool {
	for _, s := range steps {
		if s.State == StepFailed || s.State == StepDenied {
			return true
		}
	}
	return false
}

func segmentRunning(steps []*Step) bool {
	for _, s := range steps {
		if s.State == StepRunning {
			return true
		}
	}
	return false
}

// Process 单独画一轮过程的各段（不带气泡外壳，测试和以后别处复用）：段竖着排，段间隔 s1，宽度是过程最大宽。
// open 给每段的展开状态（按段第一步的 ID 记，宿主持有），nil 表示不能展开。聊天里过程是一个气泡，见 processbubble.go。
func (h Host) Process(c *ui.Context, key string, char OKLCH, steps []*Step, open func(id string) *bool) ui.Element {
	return h.processBody(c, key, char, steps, open).Width(h.processWidth()).Shrink(1).MinWidth(0)
}

// processBody 是过程的各段（思考行、读搜行、命令块、改文件块），段间隔 s1，宽度跟着外面。
func (h Host) processBody(c *ui.Context, key string, char OKLCH, steps []*Step, open func(id string) *bool) ui.Element {
	h.procChar = char // 这一轮的角色色，读搜、思考行的图标用它
	h.StepOpen = open // 终端块的"还有 N 行""展开全部"也记在同一处
	t := h.T
	col := ui.Column(c.Key(key)).Label("过程").Gap(t.S1).FillWidth().MinWidth(0)
	col.Children(func() {
		for _, seg := range Segments(steps) {
			first := seg.Steps[0]
			var o *bool
			if open != nil {
				o = open(first.ID)
			}
			switch {
			case first.Kind == StepRecall:
				h.recallCard(c, first, o, char)
			case first.Kind == StepTool:
				h.toolCard(c, first, o, char)
			case (first.Kind == StepRead || first.Kind == StepSearch) && !seg.Merged() && first.Output != "":
				h.toolCard(c, first, o, char) // 单独一次读或搜：展开能看它返回了什么
			case first.Kind == StepRead || first.Kind == StepSearch:
				h.exploreRow(c, seg, o)
			case first.Kind == StepThink:
				h.thinkRow(c, first, o)
			case first.Kind == StepCommand:
				h.commandCard(c, first, o, char)
			case first.Kind == StepEdit:
				h.editCard(c, first, o, char)
			}
		}
	})
	return col
}

// processWidth 是过程项的最大宽：气泡最大宽和 32 个正文字宽里较小的那个（Bun min(100%, 32em)）。
func (h Host) processWidth() float32 {
	t := h.T
	w := max(t.HLg*3, h.Column*t.BubbleShare)
	return min(w, 32*t.Body)
}

// leadCol 是过程里图标那一列，从气泡外沿量：图标格子的左缘就是气泡正文（和群聊名字）的左缘 BubblePadH，
// 图标中心在 center、后面的字从 text 开始。带图标的行一律用 lead 画开头，对齐是写法本身保证的（主人 2026-11-06：对齐写法要统一、要能自动查）。
func (h Host) leadCol() (center, text float32) {
	t := h.T
	return t.BubblePadH + h.rowLine()/2, t.BubblePadH + h.rowLine() + t.S2
}

// leadSlot 是内容从 start 起的那种行能用的图标格子宽：中心落在 leadCol 上，左边不越过自己的起点；最宽 rowLine。
// 起点比这条线靠里的行（凹块里的结果行；极端外观下的读搜行）格子窄一点，图标跟着缩（leadIcon）。
func (h Host) leadSlot(start float32) float32 {
	center, _ := h.leadCol()
	return max(0, min(h.rowLine(), 2*(center-start)))
}

// leadIcon 是起点为 start 的行里图标该画多大：本来的大小和格子宽取小的。
func (h Host) leadIcon(start, natural float32) float32 { return min(natural, h.leadSlot(start)) }

// lead 画一行开头的图标格子。start 是这一行内容从气泡外沿算起的起点，gap 是这一行自己子元素之间的间距；
// 格子宽、前后的外边距都按这两个算，让图标中心落在 leadCol 的 center、后面的字从 text 开始。
func (h Host) lead(c *ui.Context, start, gap float32, icon func()) {
	center, text := h.leadCol()
	w := h.leadSlot(start)
	ui.Row(c.Key("lead")).Size(w, h.rowLine()).Shrink(0).Center().Margin(0, max(0, text-center-w/2-gap), 0, max(0, center-w/2-start)).Children(icon)
}

// rowStart 是气泡里读搜行、思考行、命令和改文件头部、自动放行这些行的内容起点，从气泡外沿量。
func (h Host) rowStart() float32 { return h.T.BubblePadV + h.rowPad() }

func (h Host) rowLine() float32 { return round1(h.T.FS[2] * 1.45) }
func (h Host) rowPad() float32  { return round1((h.T.HSm - h.rowLine()) / 2) }

// processRow 是读搜行、思考行共用的一行：左边图标位（运行中转圈或三个点，完成后是这一类的图标，宽度不变不跳位）、
// 一句话、右边失败角标和展开箭头。能展开时是按钮（点、Enter、Space 切换）。
// expanded 是正文现在显示没有（箭头按它转，不只看 *o：思考边想边展开时 *o 没翻，正文也开着）。
func (h Host) processRow(c *ui.Context, key string, o *bool, expanded bool, lead func(), text func(), failed bool) ui.Element {
	t := h.T
	expandable := o != nil
	var row ui.Element
	if expandable {
		b := ui.ButtonBase(c.Key(key)).OnClick(func() { *o = !*o })
		bg := ui.Transparent
		if b.Pressed() {
			bg = t.C.Press
		} else if b.Hovered() {
			bg = t.C.Hover
		}
		row = b.Background(t.Fill(b, bg))
	} else {
		row = ui.Row(c.Key(key))
	}
	row.FillWidth().MinHeight(t.HSm).Padding(h.rowPad()).Gap(t.S2).AlignItems(ui.Center).Justify(ui.Start).Radius(min(t.RCtl, t.HSm/2)) // 圆角拉满时控件档会超过半高，按半高封顶
	row.Children(func() {
		h.lead(c, h.rowStart(), t.S2, lead)
		ui.Row(c.Key("text")).Grow(1).MinWidth(0).Children(text)
		if failed {
			ui.Icon(c.Key("badge"), Icon("alert", true)).Size(t.Badge, t.Badge).Shrink(0).TextColor(t.C.Danger).Label("出错了")
		}
		if expandable {
			deg := float32(0)
			if expanded {
				deg = 90
			}
			ui.Icon(c.Key("chevron"), Icon("right", false)).Size(t.FS[2], t.FS[2]).Shrink(0).TextColor(t.C.Text3).Rotate(row.AnimateTo("chevron", deg, ui.Animation{Duration: t.Fast}))
		}
	})
	return row
}

// spinner 是运行中的转圈（mygo 自带，900ms 一圈），大小填满图标位，第三级文字色。
func (h Host) spinner(c *ui.Context) {
	s := h.leadIcon(h.rowStart(), h.rowLine()*0.8)
	ui.Spinner(c).Size(s, s).TextColor(h.T.C.Text3)
}

// kindIcon 是读搜、思考行完成后那一类的图标：用这一轮角色的颜色（和命令、改文件的图标一致；群聊里多一层"这是谁的"）。
func (h Host) kindIcon(c *ui.Context, name string) {
	s := h.leadIcon(h.rowStart(), h.rowLine()*0.8)
	ui.Icon(c, Icon(name, false)).Size(s, s).TextColor(h.T.AccentOf(h.procChar).Text)
}

// rowText 是行里的一句话：FS2、第二级文字色、一行放不下截断。
func (h Host) rowText(c *ui.Context, spans ...ui.Span) ui.Element {
	t := h.T
	for i := range spans {
		if spans[i].Size == 0 {
			spans[i].Size = t.FS[2]
		}
		if spans[i].Color == (ui.Color{}) {
			spans[i].Color = t.C.Text2
		}
	}
	return ui.RichText(c, spans...).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(h.rowLine()).SingleLine().MinWidth(0)
}

// exploreRow 是读搜行：一步就是那一步的一行；相邻多步合成一行，展开后每步一行（缩进 s3），子行不能再展开。
func (h Host) exploreRow(c *ui.Context, seg Segment, o *bool) {
	t := h.T
	steps := seg.Steps
	if !seg.Merged() {
		o = nil
	}
	direct := h.soleSegment && seg.Merged() // 这一轮只有这一段：外层摘要已经写了“读了几个”，直接列出来
	running, failed := segmentRunning(steps), segmentFailed(steps)
	icon := "file" // 有搜索就用放大镜，只有读就用文件
	for _, s := range steps {
		if s.Kind == StepSearch {
			icon = "search"
		}
	}
	col := ui.Column(c.Key("explore-" + steps[0].ID)).FillWidth()
	ds := ui.Local(col, "disc", func() discState { return discState{} })
	col.Children(func() {
		if !direct {
			h.processRow(c, "row", o, o != nil && *o, func() {
				if running {
					h.spinner(c)
				} else {
					h.kindIcon(c, icon)
				}
			}, func() {
				if seg.Merged() {
					h.rowText(c, ui.Span{Text: exploreSummary(steps), Features: Tabular})
					return
				}
				verb, obj := stepLine(steps[0])
				h.rowText(c, ui.Span{Text: verb}, ui.Span{Text: obj, Font: h.Mono, Features: h.codeFeatures(), Size: t.FS[1]})
			}, failed)
		}
		h.disclose(c, col, "steps-box", ds, direct || (o != nil && *o), false, true, func() {
			ui.Column(c.Key("steps")).FillWidth().Padding(0, 0, 0, t.S3).Children(func() {
				for _, s := range steps {
					st := s
					verb, obj := stepLine(st)
					ui.Row(c.Key(st.ID)).FillWidth().MinHeight(t.HSm).Padding(h.rowPad()).Gap(t.S2).AlignItems(ui.Center).Children(func() {
						h.rowText(c, ui.Span{Text: verb}, ui.Span{Text: obj, Font: h.Mono, Features: h.codeFeatures(), Size: t.FS[1]}).Grow(1)
						if st.State == StepFailed || st.State == StepDenied {
							ui.Icon(c.Key("badge"), Icon("alert", true)).Size(t.Badge, t.Badge).Shrink(0).TextColor(t.C.Danger).Label("出错了")
						}
					})
				}
			})
		})
	})
}

// thinkRow 是思考行：进行中"正在想" + 三个点；完成"想了 N 秒"（没有秒数写"想了一下"）。
// 正文在展开时显示；进行中要不要边想边展开看 Host.LiveOpen（设置"进行中展开过程"，ADR-288）。
func (h Host) thinkRow(c *ui.Context, s *Step, o *bool) {
	running := s.State == StepRunning
	if s.Thought == "" {
		o = nil
	}
	// 设置"进行中展开过程"开着时，进行中默认展开；点了在默认状态上翻转（和卡片 cardDefaultOpen 一样）
	show := running && h.LiveOpen && s.Thought != ""
	if o != nil && *o {
		show = !show
	}
	if s.Thought == "" {
		show = false
	}
	col := ui.Column(c.Key("think-" + s.ID)).FillWidth()
	ds := ui.Local(col, "disc", func() discState { return discState{} })
	col.Children(func() {
		h.processRow(c, "row", o, show, func() {
			if running {
				// 三个点按图标格子的宽缩小（打字气泡里的点是 31×TS 宽，放进 rowLine 的格子会压到后面的字）
				dot := h.leadSlot(h.rowStart()) * typingDotBase / (3*typingDotBase + 2*typingGapBase)
				ui.Row(c.Key("dots")).Gap(dot * typingGapBase / typingDotBase).AlignItems(ui.Center).Children(func() { h.dotsSized(c, dot) })
			} else {
				h.kindIcon(c, "brain")
			}
		}, func() {
			switch {
			case running:
				h.rowText(c, ui.Span{Text: "正在想"})
			case s.Seconds > 0:
				h.rowText(c, ui.Span{Text: "想了 "}, ui.Span{Text: fmt.Sprint(s.Seconds), Features: Tabular}, ui.Span{Text: " 秒"})
			default:
				h.rowText(c, ui.Span{Text: "想了一下"})
			}
		}, false)
		if s.Thought != "" {
			h.disclose(c, col, "thought-box", ds, show, false, true, func() { h.thoughtBody(c, s) })
		}
	})
}

// thoughtBody 是思考正文：FS2、行高 1.45、第二级文字色，左缩进 s3；最高约 20 行，超出在里面滚动（Lorca agents.go:752-761、Bun 20lh）。
func (h Host) thoughtBody(c *ui.Context, s *Step) {
	t := h.T
	line := h.rowLine()
	ui.Scroll(c.Key("thought")).FillWidth().MaxHeight(20*line).Padding(0, h.rowPad(), h.rowPad(), t.S3).Children(func() {
		ui.Text(c, s.Thought).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(line).TextColor(t.C.Text2).Selectable()
	})
}
