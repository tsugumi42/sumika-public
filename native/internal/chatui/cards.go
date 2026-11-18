package chatui

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 命令卡、改文件卡（docs/规格-过程组件.md 3.3、3.4）。

// cardDefaultOpen：运行中、失败时默认展开，成功、停下后收起；等批准时也展开（审批块挂在里面，见 approval.go）。
func cardDefaultOpen(s *Step) bool {
	return s.State == StepRunning || s.State == StepFailed || (s.Approval != nil && s.Approval.State == ApprovalPending)
}

// card 是命令、改文件这一段在过程气泡里的样子（ADR-287：不再是独立的卡片）：一个头部（按钮，点了在默认展开状态上翻转），
// 展开时下面是几块凹块（离气泡边 pv、圆角同心，见 block）。没有自己的底色和外壳，相贴、圆角、投影都归外面的气泡管。
func (h Host) card(c *ui.Context, key string, s *Step, o *bool, lead func(), title func(), body func(add func(ui.Element) ui.Element)) {
	t := h.T
	inner := max(t.Inner(t.R1, t.BubblePadV), t.RInnerMin)
	open := cardDefaultOpen(s)
	if o != nil && *o {
		open = !open
	}
	if h.soleSegment { // 这一轮只有这一段：外层展开就是看它，不再折一层
		open, o = true, nil
	}
	col := ui.Column(c.Key(key)).FillWidth().Gap(t.BubblePadV)
	ds := ui.Local(col, "disc", func() discState { return discState{} })
	col.Children(func() {
		b := ui.ButtonBase(c.Key("head")).Label("展开")
		if o != nil {
			b.OnClick(func() { *o = !*o })
		}
		bg := ui.Transparent
		if b.Pressed() {
			bg = t.C.Press
		} else if b.Hovered() && o != nil { // 点不了的不给悬停底，不然看着像能点
			bg = t.C.Hover
		}
		// 头部的悬停底和气泡同心 = r-1 − pv；圆角拉满时这个值会超过 hsm 的一半，头部就长高到放得下（同心优先，不把圆角压扁）
		b.Background(t.Fill(b, bg)).FillWidth().MinHeight(max(t.HSm, 2*inner)).Padding(h.rowPad()).Gap(t.S2).AlignItems(ui.Center).Justify(ui.Start).Radius(inner)
		b.Children(func() {
			h.lead(c, h.rowStart(), t.S2, lead) // 图标进统一的那一列（leadCol）
			ui.Row(c.Key("title")).Grow(1).MinWidth(0).Gap(t.S2).AlignItems(ui.Center).Children(title)
		})
		add := func(el ui.Element) ui.Element { return el }
		if body != nil { // 没有内容可展开的（body 为 nil）不留盒子，免得多一段间距
			h.disclose(c, col, "body", ds, open, false, true, func() { body(add) })
		}
		// 放行规则那一行只在展开时出现：平时一眼看的是做了什么，查"凭什么没问我"才展开
		// 决定过的（允许、拒绝、超时、撤回）结果写在头部行尾，下面不再画一条
		if a := s.Approval; a != nil && (a.State == ApprovalPending || a.State == ApprovalSubmitting || (a.State == ApprovalAuto && open)) {
			h.approvalBlock(c, s)
		}
	})
}

// block 是卡片里的一块凹底（叠色 Hover，和引用块一样），离卡边 pv、圆角同心，四边内距 s2。
func (h Host) block(c *ui.Context, key string) ui.Element {
	t := h.T
	// 和气泡同心；圆角令牌不封顶，大圆角档下一行的块会比两倍圆角还矮，所以块至少两倍圆角高，同心和不超过半高同时成立
	r := max(t.Inner(t.R1, t.BubblePadV), t.RInnerMin)
	return ui.Column(c.Key(key)).FillWidth().MinHeight(2 * r).Padding(t.S2).Background(t.C.Hover).Radius(r)
}

// noLigatures 关掉等宽字的连字（Maple Mono 用 calt 做连字，=== 会画成三横）。
const noLigatures = "liga=0,calt=0"

// codeFeatures 是命令、路径、diff 这类代码的 OpenType 特性：照设置"等宽字连字"（默认开）。
// 命令输出不走这里：输出是程序原样吐出来的字，一律不连写（=== RUN 要看得出是三个等号）。
func (h Host) codeFeatures() string {
	if h.T.S.Ligatures {
		return ""
	}
	return noLigatures
}

func (h Host) monoLine() float32 { return round1(h.T.FS[1] * 1.4) }

func (h Host) cardIcon(c *ui.Context, name string, char OKLCH) {
	s := h.leadIcon(h.rowStart(), h.rowLine()*0.8)
	ui.Icon(c, Icon(name, false)).Size(s, s).TextColor(h.T.AccentOf(char).Text)
}

// commandCard 是执行命令的卡：头部"正在运行 / 运行了 / 停了 + 命令第一行"，右边用时；失败是角标 + 退出码；
// 展开后是完整命令和输出（运行中只显示最后 6 行，跑完是 6 行高、能滚的块）。
func (h Host) commandCard(c *ui.Context, s *Step, o *bool, char OKLCH) {
	t := h.T
	verb := "运行了 "
	switch s.State {
	case StepRunning:
		verb = "正在运行 "
	case StepStopped:
		verb = "停了 "
	case StepDenied:
		verb = "不允许："
	}
	if s.Approval != nil && s.Approval.State == ApprovalPending {
		verb = "想运行 "
	}
	if notRun(s) {
		verb = "没运行 " // 过期、撤回：根本没跑，不写"停了"
		if s.Approval.State == ApprovalDenied {
			verb = "不允许："
		}
	}
	var body func(add func(ui.Element) ui.Element)
	if !notRun(s) || strings.Contains(strings.TrimRight(s.Target, "\n"), "\n") { // 没执行、命令就一行：头部已经写着它，不在下面再画一遍
		body = func(add func(ui.Element) ui.Element) { h.console(c, s) }
	}
	h.card(c, "cmd-"+s.ID, s, o, func() { h.cardIcon(c, "terminal", char) }, func() {
		h.rowText(c, ui.Span{Text: verb}, ui.Span{Text: firstLine(s.Target), Font: h.Mono, Features: h.codeFeatures(), Size: t.FS[1]}).Grow(1)
		h.cardMeta(c, s)
	}, body)
}

// toolCard 是一次工具调用的卡：头部写调用了什么、参数是什么，展开是它返回的内容。没有返回内容就不能展开。
func (h Host) toolCard(c *ui.Context, s *Step, o *bool, char OKLCH) {
	t := h.T
	verb, name, icon := "调用了 ", s.Tool, "tool"
	switch s.Kind {
	case StepRead:
		verb, name, icon = "读了 ", "", "file"
	case StepSearch:
		verb, name, icon = "搜了 ", "", "search"
	}
	switch s.State {
	case StepRunning:
		switch s.Kind {
		case StepRead:
			verb = "正在读 "
		case StepSearch:
			verb = "正在搜 "
		default:
			verb = "正在调用 "
		}
	case StepStopped:
		verb = "停了 "
	case StepDenied:
		verb = "不允许："
	}
	if s.Output == "" {
		o = nil // 没有内容可看，头部不能点
	}
	var body func(add func(ui.Element) ui.Element)
	if s.Output != "" {
		body = func(add func(ui.Element) ui.Element) {
			line := h.monoLine()
			h.block(c, "tool-output").Label("工具返回的内容").Children(func() {
				ui.Scroll(c.Key("tool-output-scroll")).FillWidth().MaxHeight(line * consoleOutLines).Children(func() {
					ui.Text(c, s.Output).Font(h.Mono).FontFeatures(noLigatures).FontSize(t.FS[1]).FixedLineHeight(line).TextColor(t.C.Text2).Wrap().Selectable()
				})
			})
		}
	}
	h.card(c, "tool-"+s.ID, s, o, func() { h.cardIcon(c, icon, char) }, func() {
		spans := []ui.Span{{Text: verb}}
		if name != "" {
			spans = append(spans, ui.Span{Text: name, Font: h.Mono, Features: h.codeFeatures(), Size: t.FS[1]})
		}
		if target := firstLine(s.Target); target != "" {
			if name != "" {
				target = "  " + target
			}
			spans = append(spans, ui.Span{Text: target, Font: h.Mono, Features: h.codeFeatures(), Size: t.FS[1]})
		}
		h.rowText(c, spans...).Grow(1).MinWidth(0).SingleLine()
		h.cardMeta(c, s)
	}, body)
}

// 终端块的几个上限（行数）：命令最多显示几行；输出运行中看最后几行、跑完的块多高、"展开全部"以后多高。
const (
	consoleCmdLines  = 4
	consoleLiveLines = 6
	consoleOutLines  = 12
	consoleAllLines  = 30
)

// console 是命令块（规格 3.3；主人 2026-11-07：看着像普通文本块，要更像终端）：一块凹底里第一行"$ 命令"
// （$ 淡色，命令正文色，折行的接续行对齐在 $ 后面），下面接输出（第二级色、一律不连写、剥掉颜色控制码），
// 失败时最后一行是危险色"退出码 N"。命令超过 4 行只显示前 4 行 +"还有 N 行"；输出运行中只看最后 6 行，
// 跑完最高 12 行、能滚、默认停在最底下（错误一般在最后），超过时左下"共 N 行"+"展开全部"（最高 30 行）。
func (h Host) console(c *ui.Context, s *Step) {
	t := h.T
	line := h.monoLine()
	mono := func(key string, text string, ink ui.Color, feat string) ui.Element {
		return ui.Text(c.Key(key), text).Font(h.Mono).FontFeatures(feat).FontSize(t.FS[1]).FixedLineHeight(line).TextColor(ink)
	}
	small := func(key, text string, ink ui.Color) ui.Element {
		return ui.Text(c.Key(key), text).Font(h.Font).FontSize(t.FS[1]).FontFeatures(Tabular).FixedLineHeight(line).TextColor(ink).SingleLine()
	}
	link := func(key, text string, flip *bool) {
		b := ui.ButtonBase(c.Key(key)).Label(text).Radius(t.RInnerMin).Padding(0, t.S1)
		if flip != nil {
			b.OnClick(func() { *flip = !*flip })
		}
		ink := t.AccentOf(h.procChar).Text // 能点的字用角色色，和旁边的说明分得开
		bg := ui.Transparent
		if b.Hovered() {
			bg = t.C.Hover
		}
		b.Background(t.Fill(b, bg))
		b.Children(func() { small("label", text, ink) })
	}
	var cmdAll, outAll *bool
	if h.StepOpen != nil {
		cmdAll, outAll = h.StepOpen(s.ID+"#命令全部"), h.StepOpen(s.ID+"#输出全部")
	}
	gap := round1(t.FS[1] * 0.6) // 一个等宽空格：$ 和命令之间
	h.block(c, "command").Label("终端").Gap(t.S1).Children(func() {
		// 命令：$ 单独一列，命令折行时接续行对齐在 $ 后面
		cmd := strings.TrimRight(s.Target, "\n")
		lines := strings.Split(cmd, "\n")
		hidden := 0
		if len(lines) > consoleCmdLines && (cmdAll == nil || !*cmdAll) {
			hidden = len(lines) - consoleCmdLines
			cmd = strings.Join(lines[:consoleCmdLines], "\n")
		}
		ui.Row(c.Key("cmd")).FillWidth().Gap(gap).AlignItems(ui.Start).Children(func() {
			mono("prompt", "$", t.C.Text3, noLigatures).Shrink(0).Unselectable()
			mono("text", cmd, t.C.Text, h.codeFeatures()).Grow(1).MinWidth(0).Selectable()
		})
		if hidden > 0 || (cmdAll != nil && *cmdAll && len(lines) > consoleCmdLines) {
			text := fmt.Sprintf("还有 %d 行", hidden)
			if hidden == 0 {
				text = "收起命令"
			}
			ui.Row(c.Key("cmd-more")).Gap(gap).Children(func() {
				mono("pad", "$", ui.Transparent, noLigatures).Shrink(0).Unselectable() // 占住 $ 那一列
				link("more", text, cmdAll)
			})
		}
		if s.Approval != nil && s.Approval.State == ApprovalPending || notRun(s) {
			return // 还没执行：只有命令
		}
		// 输出
		out := stripANSI(strings.TrimRight(s.Output, "\n")) // 末尾的换行不画成空行
		total := 0
		if out != "" {
			total = strings.Count(out, "\n") + 1
		}
		switch {
		case out == "" && s.State == StepRunning:
		case out == "":
			small("output", "没有输出", t.C.Text3)
		case s.State == StepRunning: // 运行中只看最后几行（Lorca cards.go:59-72）
			mono("output", lastLines(out, consoleLiveLines), t.C.Text2, noLigatures).FillWidth().Selectable()
		default:
			rows := consoleOutLines
			if outAll != nil && *outAll {
				rows = consoleAllLines
			}
			sc := ui.Scroll(c.Key("output")).FillWidth().MaxHeight(float32(rows) * line)
			pos := ui.Local(sc, "pos", func() ui.ScrollState { return ui.ScrollState{Y: math.MaxFloat32} }) // 默认停在最底下
			sc.TrackScroll(pos).Children(func() {
				mono("text", out, t.C.Text2, noLigatures).FillWidth().Selectable()
			})
		}
		// 最后一行：左边"共 N 行 · 展开全部"，右边失败的退出码
		long := s.State != StepRunning && total > consoleOutLines
		failed := s.State == StepFailed && s.HasExit
		if long || failed {
			ui.Row(c.Key("foot")).FillWidth().Gap(t.S1).AlignItems(ui.Center).Children(func() {
				if long {
					small("total", fmt.Sprintf("共 %d 行", total), t.C.Text3)
					text := "展开全部"
					if outAll != nil && *outAll {
						text = "收起"
					}
					link("all", text, outAll)
				}
				ui.Spacer(c)
				if failed {
					small("exit", fmt.Sprintf("退出码 %d", s.ExitCode), t.C.Danger)
				}
			})
		}
	})
}

// stripANSI 剥掉终端的颜色、光标控制码（ESC [ … 字母、ESC ] … BEL）：现在不画颜色，留着会显示成乱码。
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI：到 0x40–0x7e 的结束字节为止
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
		case ']': // OSC：到 BEL 或 ESC \ 为止
			j := i + 2
			for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
				j++
			}
			if j < len(s) && s[j] == 0x1b {
				j++
			}
			i = j
		default: // 其余：ESC + 若干中间字节（0x20–0x2f，如字符集切换 ESC ( B）+ 一个结束字节
			j := i + 1
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			i = j
		}
	}
	return b.String()
}

// cardMeta 是卡片头部右边：运行中转圈 + 秒数；完成只有秒数；失败角标；停下是停止图标；审批决定过的先写结果（小图标 + 两三个字）。都用等宽数字、第三级色。
func (h Host) cardMeta(c *ui.Context, s *Step) {
	t := h.T
	if a := s.Approval; a != nil && (a.State == ApprovalPending || a.State == ApprovalSubmitting) {
		return // 等批准的：右边不放东西（剩余时间在摘要行）
	}
	ui.Row(c.Key("meta")).Shrink(0).Gap(t.S2).AlignItems(ui.Center).Children(func() {
		h.approvalResultChip(c, s.Approval)
		if notRun(s) {
			return // 没执行：结果就是全部（停止图标只给跑起来又被停下的）
		}
		if s.State == StepDone && s.Seconds <= 0 {
			return // 没有别的要写：不留一个空行占间距（结果行才对得齐）
		}
		ui.Row(c.Key("state")).Gap(t.S1).AlignItems(ui.Center).Children(func() {
			switch s.State {
			case StepRunning:
				h.spinner(c)
			case StepFailed:
				ui.Icon(c.Key("badge"), Icon("alert", true)).Size(t.Badge, t.Badge).TextColor(t.C.Danger).Label("出错了") // 退出码写在终端块最后一行
				return
			case StepStopped, StepDenied:
				ui.Icon(c, Icon("stop", false)).Size(t.FS[2], t.FS[2]).TextColor(t.C.Text3).Label("停了")
				return
			}
			if s.Seconds > 0 {
				ui.Text(c, fmt.Sprintf("%d 秒", s.Seconds)).Font(h.Font).FontSize(t.FS[1]).FontFeatures(Tabular).TextColor(t.C.Text3)
			}
		})
	})
}

// editCard 是写入、改文件的卡（ADR-281 定写入是卡片）：头部"正在改 / 改了 / 新建了 / 删了 + 路径"，行末 +A（成功色）−B（危险色）；
// 展开后是 diff：加的行成功色、减的行危险色，行底各叠同色一层；最多显示 16 行，超出滚动。
func (h Host) editCard(c *ui.Context, s *Step, o *bool, char OKLCH) {
	t := h.T
	verb := s.EditVerb
	if verb == "" {
		verb = "改了"
	}
	switch {
	case s.Approval != nil && s.Approval.State == ApprovalPending:
		verb = "想改"
	case notRun(s):
		verb = "没改" // 被拒、过期、撤回：改动没落地
	case s.State == StepRunning:
		verb = "正在改"
	}
	var body func(add func(ui.Element) ui.Element)
	if s.Diff != "" {
		body = func(add func(ui.Element) ui.Element) { h.diffBlock(c, s) }
	}
	h.card(c, "edit-"+s.ID, s, o, func() { h.cardIcon(c, "pen", char) }, func() {
		h.rowText(c, ui.Span{Text: verb + " "}, ui.Span{Text: s.Target, Font: h.Mono, Features: h.codeFeatures(), Size: t.FS[1]}).Grow(1)
		if s.Added > 0 || s.Removed > 0 {
			ui.RichText(c, ui.Span{Text: fmt.Sprintf("+%d", s.Added), Color: t.C.OkText, Weight: t.Weight[1], Features: Tabular},
				ui.Span{Text: " "}, ui.Span{Text: fmt.Sprintf("−%d", s.Removed), Color: t.C.DangerText, Weight: t.Weight[1], Features: Tabular}).
				Font(h.Font).FontSize(t.FS[1]).Shrink(0).SingleLine()
		}
		if _, text, _ := approvalResult(s.Approval); s.State != StepDone || text != "" {
			h.cardMeta(c, s)
		}
	}, body)
}

// diffBlock 是改动的差异块：加的行成功色、减的行危险色，行底各叠同色一层；最多显示 16 行，超出滚动。
func (h Host) diffBlock(c *ui.Context, s *Step) {
	t := h.T
	line := round1(t.FS[1] * 1.6)
	// 加、减的行底：连续同类的几行算一段，一段的四角做圆角（块里离边 s2 的同心值），不贴块边；字左右留 s1
	r := min(max(t.Inner(max(t.Inner(t.R1, t.BubblePadV), t.RInnerMin), t.S2), t.RInnerMin), line/2) // 单独一行时按半行高封顶（小字号、大圆角时会超）
	lines := splitLines(s.Diff)
	kind := func(i int) byte {
		if i < 0 || i >= len(lines) || lines[i] == "" || (lines[i][0] != '+' && lines[i][0] != '-') {
			return 0
		}
		return lines[i][0]
	}
	h.block(c, "diff").Children(func() {
		ui.Scroll(c.Key("scroll")).FillWidth().MaxHeight(16 * line).Children(func() {
			ui.Column(c.Key("lines")).FillWidth().Children(func() {
				for i, l := range lines {
					ink, bg := t.C.Text2, ui.Transparent
					k := kind(i)
					switch k {
					case '+':
						ink, bg = t.C.OkText, t.C.Ok.Alpha(t.C.DiffTint)
					case '-':
						ink, bg = t.C.DangerText, t.C.Danger.Alpha(t.C.DiffTint)
					}
					var top, bottom float32
					if k != 0 && kind(i-1) != k {
						top = r
					}
					if k != 0 && kind(i+1) != k {
						bottom = r
					}
					ui.Text(c.Key(i), l).Font(h.Mono).FontFeatures(h.codeFeatures()).FontSize(t.FS[1]).FixedLineHeight(line).TextColor(ink).Background(bg).FillWidth().
						Padding(0, t.S1).Radius(top, top, bottom, bottom)
				}
			})
		})
	})
}

// notRun：要批准的这一步最后没执行（被拒、过期、撤回）。
func notRun(s *Step) bool {
	if s.Approval == nil {
		return false
	}
	switch s.Approval.State {
	case ApprovalDenied, ApprovalExpired, ApprovalRevoked:
		return true
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func splitLines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

// lastLines 是最后 n 行。
func lastLines(s string, n int) string {
	ls := splitLines(s)
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}
