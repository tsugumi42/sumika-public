package chatui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// 审批块（docs/规格-过程组件.md 3.5）：挂在命令卡、改文件卡的最下面。状态机照规格-交互原型 3.5。

// ApprovalState 是审批的状态。
type ApprovalState int

const (
	ApprovalPending    ApprovalState = iota // 等主人决定
	ApprovalSubmitting                      // 决定已发出、等后台确认
	ApprovalAllowed
	ApprovalDenied
	ApprovalExpired
	ApprovalRevoked
	ApprovalAuto // 没问主人就放行（或主人批过），只是记一句
)

// Approval 是挂在一步上的审批请求。
type Approval struct {
	State      ApprovalState
	Cause      string        // 本地群聊里是谁的话引起的（ADR-269）；空就不写
	Reason     string        // 她给的原因
	Remaining  time.Duration // 剩余时间，宿主每秒更新；0 表示不显示
	AutoReason string        // 这一步是哪条规则放行的，一句话（ApprovalAuto）
	Shown      time.Time     // 审批块第一次出现的时刻（出现后 500ms 内不认 Enter）
	// Scopes 是"允许一次"以外还能选的范围（这个对话、记住），后台给什么才摆什么；KindLabel 是它们管的那一类。
	Scopes    []ApprovalScope
	KindLabel string
}

// ApprovalScope 是"允许"的范围。
type ApprovalScope int

const (
	AllowOnce ApprovalScope = iota
	AllowConversation
	AllowAlways
)

// approvalEnterGuard：审批块出现后这么久之内不认 Enter（防正在按回车发消息时块一出现就被批，Bun keys.ts）。
const approvalEnterGuard = 500 * time.Millisecond

// approvalKeys 接审批块的键盘：焦点在块本身时 Enter = 允许一次，块里任何地方 Esc = 拒绝；出现后 500ms 内不认 Enter。
// 返回这一帧在块里按了 Enter 没有（按的是哪个按钮由焦点在谁身上决定，见 approvalButtons）。只在等决定时有效。
func (h Host) approvalKeys(c *ui.Context, blk ui.Element, s *Step) (enter bool) {
	a := s.Approval
	if a.State != ApprovalPending {
		return false
	}
	blk.Focusable()
	ready := a.Shown.IsZero() || c.Now().Sub(a.Shown) >= approvalEnterGuard
	enter = blk.Shortcut(0, ui.KeyEnter) && ready
	if enter && blk.Focused() && h.OnApprove != nil { // 焦点在块本身：允许一次
		h.OnApprove(s.ID, AllowOnce)
	}
	if blk.Shortcut(0, ui.KeyEscape) && h.OnDeny != nil { // 块里任何地方 Esc：拒绝
		h.OnDeny(s.ID, "")
	}
	return enter
}

// approvalResult 是审批决定后行尾写的结果：图标、两三个字、要不要用危险色（只有拒绝用）。还在等、放行记录没有。
func approvalResult(a *Approval) (icon, text string, danger bool) {
	if a == nil {
		return "", "", false
	}
	switch a.State {
	case ApprovalAllowed:
		return "check", "已允许", false
	case ApprovalDenied:
		return "close", "已拒绝", true
	case ApprovalExpired:
		return "time", "超时", false
	case ApprovalRevoked:
		return "close", "已撤回", false
	}
	return "", "", false
}

// approvalResultChip 画结果：一个小图标加两三个字，和头部行尾的别的状态（用时、停止）同一个字号、同一档灰；只有拒绝用危险色。
func (h Host) approvalResultChip(c *ui.Context, a *Approval) {
	t := h.T
	icon, text, danger := approvalResult(a)
	if text == "" {
		return
	}
	ink := t.C.Text3
	if danger {
		ink = t.C.DangerText
	}
	ui.Row(c.Key("result")).Shrink(0).Gap(t.S1).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, Icon(icon, false)).Size(t.FS[2], t.FS[2]).TextColor(ink)
		ui.Text(c, text).Font(h.Font).FontSize(t.FS[1]).TextColor(ink).SingleLine()
	})
}

// askCard 是过程气泡里"只有一步、而且在等批准"时画的审批卡（主人 2026-11-15："叠了很多层，图标和命令各出现两次"）：
// 第一行（图标、想做什么、倒计时）是气泡的摘要行，这里从下面接着画——要运行的命令或改动的差异（一块凹块，命令只在这里出现一次）、
// 一句理由、右对齐的按钮。按钮直接在气泡上，不再套一块灰底；键盘、提交中、"⋯"的行为照旧。
//
// frozen 为真是决定之后这张卡正在收起（processbubble.go 的 askState）：画的是等批准时最后的样子，淡出、高度收到一行；
// 这时它不收键盘也不收点击，决定已经做完了，不能再批一次。
func (h Host) askCard(c *ui.Context, s *Step, frozen bool) {
	t := h.T
	a := s.Approval
	col := ui.Column(c.Key("approval")).FillWidth().Gap(t.S2).Label("审批")
	enter := false
	if !frozen {
		enter = h.approvalKeys(c, col, s)
	}
	col.Children(func() {
		if s.Kind == StepEdit {
			if s.Diff != "" {
				h.diffBlock(c, s)
			}
		} else {
			h.console(c, s)
		}
		// 理由、谁引起的：字和凹块里的字同一条左边线
		if a.Reason != "" {
			ui.Text(c.Key("reason"), a.Reason).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(h.rowLine()).TextColor(t.C.Text2).MaxLines(3).FillWidth().Padding(0, t.S2)
		}
		if a.Cause != "" {
			ui.Text(c.Key("cause"), "是 "+a.Cause+" 的话引起的").Font(h.Font).FontSize(t.FS[1]).FixedLineHeight(h.rowLine()).TextColor(t.C.Text3).MaxLines(2).FillWidth().Padding(0, t.S2)
		}
		h.approvalButtons(c, s, enter, true, frozen)
	})
}

// approvalBlock 画审批块（输入区上方的审批卡、多步过程里的那一步用）。等决定时：上面是谁引起的、原因；下面右边"拒绝""允许一次""⋯"。
// 键盘只在焦点在块里时有效：焦点在块本身时 Enter = 允许一次；焦点在按钮上时 Enter 按那个按钮；块里任何地方 Esc = 拒绝。
// 不画键帽，新审批不抢焦点。决定后只剩一行结果（图标 + 两三个字），不再画一块灰底。
func (h Host) approvalBlock(c *ui.Context, s *Step) {
	t := h.T
	a := s.Approval
	if a.State == ApprovalAuto {
		// 放行记录：只剩一行，没有凹块（Bun chat.tsx:818）
		// 图标放进和卡片头部一样的格子：图标、字和头部对成一列
		ui.Row(c.Key("approval")).FillWidth().MinHeight(t.HSm).Padding(h.rowPad()).Gap(t.S2).AlignItems(ui.Center).Children(func() {
			h.lead(c, t.BubblePadV+h.rowPad(), t.S2, func() {
				ui.Icon(c, Icon("lightning", false)).Size(h.rowLine()*0.8, h.rowLine()*0.8).TextColor(t.AccentOf(h.SelfChar).Text)
			})
			h.rowText(c, ui.Span{Text: a.AutoReason}).Grow(1)
		})
		return
	}
	if a.State != ApprovalPending && a.State != ApprovalSubmitting {
		ui.Row(c.Key("approval")).Label("审批结果").FillWidth().MinHeight(t.HSm).AlignItems(ui.Center).Justify(ui.End).Children(func() { h.approvalResultChip(c, a) })
		return
	}
	blk := h.block(c, "approval").Gap(t.S1).Label("审批")
	enter := h.approvalKeys(c, blk, s)
	blk.Children(func() {
		if a.Cause != "" {
			h.rowText(c, ui.Span{Text: "是 " + a.Cause + " 的话引起的", Color: t.C.Text3, Size: t.FS[1]})
		}
		if a.Reason != "" {
			ui.Text(c.Key("reason"), a.Reason).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(h.rowLine()).TextColor(t.C.Text2).MaxLines(3).FillWidth()
		}
		h.approvalButtons(c, s, enter, false, false)
	})
}

// approvalButtons 是等决定时的按钮行：左边剩余时间（时钟 + 等宽"4:32"），右边次要"拒绝"、主按钮"允许一次"、"⋯"更多。
// 提交中按钮全部禁用，主按钮里转圈。按钮圆角和审批块同心。
func (h Host) approvalButtons(c *ui.Context, s *Step, enter, pill, frozen bool) {
	t := h.T
	a := s.Approval
	busy := a.State == ApprovalSubmitting
	blockR := max(t.Inner(t.R1, t.BubblePadV), t.RInnerMin)
	r := max(t.Inner(blockR, t.S2), t.RInnerMin)
	if pill {
		r = t.HSm / 2 // 直接放在气泡上的按钮：单行控件的胶囊
	}
	fs := t.FS[2]
	acc := t.AccentOf(h.SelfChar)
	// 按钮四边内距一样（字行高放进去刚好 hsm），左右多出来的一截补在字行自己身上（和引用块同一个做法）
	line := h.rowLine()
	pad := max(0, (t.HSm-line)/2)
	label := func(text string) {
		ui.Text(c, text).Font(h.Font).FontSize(fs).FixedLineHeight(line).Padding(0, max(0, t.S3-pad)).SingleLine()
	}
	// 窄窗口、大字号时按钮换到下一行，不挤出块外
	ui.Row(c.Key("buttons")).FillWidth().Gap(t.S2).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Spacer(c) // 剩余时间在过程气泡的摘要行（ADR-287），这一行只有按钮、靠右
		btn := func(key, label string, primary bool, onClick func()) ui.Element {
			if frozen {
				onClick = nil // 收起中的卡：决定已经做完，点了不再算
			}
			b := ui.ButtonBase(c.Key(key)).Label(label).Height(t.HSm).Padding(pad).Center().Radius(r).Disabled(busy).OnClick(onClick)
			bg, ink := t.C.Surface, t.C.Text
			if pill {
				bg = t.C.Hover // 直接在气泡上：次要按钮的底和气泡同色，用凹块那一档的底才看得出是按钮
			}
			if primary {
				bg, ink = acc.Strong, acc.OnStrong
			}
			if !busy && b.Pressed() {
				bg = bg.Mix(t.C.Text, 0.11)
			} else if !busy && b.Hovered() {
				bg = bg.Mix(t.C.Text, 0.06)
			}
			b.Background(t.Fill(b, bg)).TextColor(ink)
			return b
		}
		deny := func() {
			if h.OnDeny != nil {
				h.OnDeny(s.ID, "")
			}
		}
		allow := func() {
			if h.OnApprove != nil {
				h.OnApprove(s.ID, AllowOnce)
			}
		}
		// mygo 的按钮自己不接 Enter（冒到审批块），所以焦点在哪个按钮上，Enter 就按哪个
		d := btn("deny", "拒绝", false, deny)
		if enter && d.Focused() {
			deny()
		}
		d.Children(func() { label("拒绝") })
		al := btn("allow", "允许一次", true, allow)
		if enter && al.Focused() {
			allow()
		}
		al.Children(func() {
			if busy { // 字留着占位（透明），转圈叠在正中：按钮宽度不变，旁边的按钮不跳（规格 3.5"主按钮里转圈"）
				ui.Text(c, "允许一次").Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(line).Padding(0, max(0, t.S3-pad)).SingleLine().TextColor(ui.Transparent)
				ui.Row(c.Key("spin")).Absolute().Left(0).Right(0).Top(0).Bottom(0).Center().Children(func() {
					ui.Spinner(c).Size(t.FS[2], t.FS[2]).TextColor(acc.OnStrong)
				})
				return
			}
			label("允许一次")
		})
		if len(a.Scopes) == 0 && !h.DenyReason {
			return // 后台没给别的范围，不摆做不到的选择
		}
		more := btn("more", "更多选择", false, nil).Width(t.HSm)
		more.Children(func() { ui.Icon(c, Icon("more", false)).Size(t.FS[2], t.FS[2]) })
		if !busy && !frozen {
			// 窗口里画的菜单（和消息右键同一套主题）：系统菜单不认 Danger，“拒绝并说明原因…”红不了
			restore := h.withMenuTheme(c)
			defer restore()
			more.PopoverMenuButton(func(m *ui.Menu) {
				for _, scope := range a.Scopes {
					word := "这个对话里都允许"
					if scope == AllowAlways {
						word = "记住，以后都允许"
					}
					if m.Item(word+a.KindLabel).Chosen() && h.OnApprove != nil {
						h.OnApprove(s.ID, scope)
					}
				}
				if h.DenyReason {
					if len(a.Scopes) > 0 {
						m.Separator()
					}
					if m.Item("拒绝并说明原因…").Danger().Chosen() && h.OnDeny != nil {
						h.OnDeny(s.ID, "?") // "?" 表示宿主要弹出写原因的输入框
					}
				}
			}, h.T.MenuButton(c))
		}
	})
}

// ApprovalInfo 是一张本地审批卡的内容（宿主从 api.LocalApproval 转来，chatui 不认后台的类型）。
type ApprovalInfo struct {
	ID, Title, Summary, Detail string
	State                      ApprovalState
	Shown                      time.Time
	Error                      string // 上一次决定没成功的原因；有就在按钮上面写出来，按钮可以再点
	Scopes                     []ApprovalScope
	KindLabel                  string
	// Egress 不空：这次调用会往外发的地址（悄悄话那一轮才有）。卡上先写“会往外发 N 处地址 · 编号”，危险的排前面、整行标红；
	// 只是给主人看的信息，不改审批怎么放行（规格-群聊引用和路由 8.2）。
	Egress *ApprovalEgress
}

// ApprovalEgress 是审批卡上的外发地址块：编号、危险的有几处、要直接露出的几行（其余在详情里）。
type ApprovalEgress struct {
	ID     string
	Total  int // 一共几处
	Danger int // 其中几处危险
	// Unchecked：参数太多或嵌套太深，后面的没检查
	Unchecked bool
	Lines     []EgressLine
}

// EgressLine 是露出的一行：地址（危险的后面跟原因），Danger 为真整行用危险色。
type EgressLine struct {
	Text   string
	Danger bool
}

// EgressShown 是卡上直接露出几行地址；再多的写“还有 N 处”，全部在详情里
const EgressShown = 3

// EgressHeader 是外发块的标题行：会往外发几处地址、编号、有危险的说几处
func EgressHeader(e *ApprovalEgress) string {
	if e == nil {
		return ""
	}
	s := fmt.Sprintf("会往外发 %d 处地址 · 编号 %s", e.Total, e.ID)
	if e.Total == 0 {
		s = "没找到外发地址 · 编号 " + e.ID
	}
	if e.Danger > 0 {
		s += fmt.Sprintf(" · %d 处写法可疑", e.Danger)
	}
	if e.Unchecked {
		s += " · 参数太多或太深，后面的没检查"
	}
	return s
}

// ApprovalCard 画输入区上方的一张审批卡：标题、她给的原因、可展开的详情，下面是审批按钮。
// 外框圆角和内距按"气泡里的块"那一套，里面的审批块同心。detail 是展开状态（宿主持有）。
func (h Host) ApprovalCard(c *ui.Context, a ApprovalInfo, detail *bool, line, fill ui.Color) {
	t := h.T
	step := &Step{ID: a.ID, Approval: &Approval{State: a.State, Reason: a.Summary, Shown: a.Shown, Scopes: a.Scopes, KindLabel: a.KindLabel}}
	// 外框圆角 R1、内距 BubblePadV：里面的审批块圆角正好是外框减内距（同心，见 block）
	ui.Column(c.Key(a.ID)).Label("审批："+a.Title).FillWidth().Shrink(0).Padding(t.BubblePadV).Gap(t.S2).Radius(t.R1).Background(fill).Border(1, line).Children(func() {
		ui.Text(c.Key("title"), a.Title).Font(h.Font).FontSize(t.FS[3]).FontWeight(t.Weight[2]).TextColor(t.C.Text).Wrap().FillWidth()
		if e := a.Egress; e != nil && (e.Total > 0 || e.Unchecked) {
			h.egressBlock(c, e)
		}
		if a.Detail != "" {
			more := ui.ButtonBase(c.Key("detail")).Label("查看详情").Height(t.HSm).Padding(0, t.S2).Radius(t.HSm / 2).OnClick(func() { *detail = !*detail })
			word := "查看详情"
			if *detail {
				word = "收起详情"
			}
			more.TextColor(t.C.Text2).Children(func() { ui.Text(c, word).Font(h.Font).FontSize(t.FS[2]) })
			if *detail {
				ui.Scroll(c.Key("detail-text")).MaxHeight(t.BodyLine * 8).FillWidth().Children(func() {
					ui.Text(c, a.Detail).Font(h.Font).FontSize(t.FS[2]).TextColor(t.C.Text2).Wrap().Selectable()
				})
			}
		}
		if a.Error != "" {
			ui.Text(c.Key("error"), a.Error).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Danger).Wrap().FillWidth()
		}
		h.approvalBlock(c, step)
	})
}

// egressBlock 画外发地址块：标题一行（有危险就整行标红），下面最多 EgressShown 行地址，危险的在前、同样标红。
func (h Host) egressBlock(c *ui.Context, e *ApprovalEgress) {
	t := h.T
	ui.Column(c.Key("egress")).Label("外发地址 " + e.ID).FillWidth().Gap(t.S1).Children(func() {
		head := t.C.Text2
		if e.Danger > 0 {
			head = t.C.DangerText
		}
		ui.Text(c.Key("egress-head"), EgressHeader(e)).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).TextColor(head).Wrap().FillWidth()
		for i, ln := range e.Lines {
			if i >= EgressShown {
				break
			}
			col := t.C.Text2
			if ln.Danger {
				col = t.C.DangerText
			}
			ui.Text(c.Key(fmt.Sprintf("egress-%d", i)), ln.Text).Font(h.Font).FontSize(t.FS[2]).TextColor(col).Wrap().FillWidth().Selectable()
		}
		if n := e.Total - min(len(e.Lines), EgressShown); n > 0 {
			ui.Text(c.Key("egress-more"), fmt.Sprintf("还有 %d 处，在详情里", n)).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).Wrap().FillWidth()
		}
	})
}
