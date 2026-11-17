package chatui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// 斜杠命令补全（清单 2.4"斜杠命令补全"，照 Hermes desktop、Codex、Claude 桌面版：输入 / 弹出命令列表，
// ↑↓ 选、Tab 采纳、Esc 关闭）。命令名单由宿主给（ComposerHost.Commands，取自后台的命令登记表，这里不存第二份）。
//
// 列表是否显示每帧从输入框文字现算：整段文字是 / 加还在打的命令名（没有空格、没有换行），
// 并且有命令名以它开头；用 Esc 关掉后，文字再变才重新出现。列表开着时 ↑↓ Tab Enter Esc 归它：
// ↑ 不触发回忆历史，Enter 不发送，Esc 不动文字也不删引用卡。没有匹配时列表不存在，这些键照旧。
// 列表画在输入框外框上面（画在流里，不用 mygo 的弹出菜单：弹出菜单会抢走输入框的焦点和键）。

// SlashItem 是列表里的一条命令。
type SlashItem struct {
	Name    string // 不带斜杠
	Summary string
}

// slashRows 是列表最多同时显示几行；命令更多时窗口随选中项滚动。
const slashRows = 8

// slashState 是列表的选择状态。forText 是 at 对应的那版文字：文字变了选中项回到第一条。
// off 是被 Esc 关掉时的文字：文字还是它就不显示。
type slashState struct {
	at      int
	forText string
	off     string
	// caretEnd：刚采纳了命令，下一帧把光标放到文字末尾
	caretEnd bool
}

// slashPrefix 取出正在打的命令名（小写）；整段文字不是"/ 加命令名"就返回 false。
func slashPrefix(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	name := strings.ToLower(text[1:])
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return "", false // 有空格、换行或别的字：已经在写参数或不是命令
		}
	}
	return name, true
}

// slashMatches 是现在该列出的命令（前缀筛选）；不该显示时返回 nil。
func (h ComposerHost) slashMatches(p *Composer, text string) []SlashItem {
	if h.Commands == nil || p.recall.on || p.slash.off == text {
		return nil
	}
	prefix, ok := slashPrefix(text)
	if !ok {
		return nil
	}
	var out []SlashItem
	for _, it := range h.Commands() {
		if strings.HasPrefix(strings.ToLower(it.Name), prefix) {
			out = append(out, it)
		}
	}
	return out
}

// slashAt 是选中第几条（n 是条数）：文字变了回到第一条。
func (p *Composer) slashAt(text string, n int) int {
	if p.slash.forText != text {
		p.slash.forText, p.slash.at = text, 0
	}
	p.slash.at = min(max(p.slash.at, 0), max(n-1, 0))
	return p.slash.at
}

// slashKey 处理输入框里的键；列表开着并收下了键返回 true。
// 用 k.Text() 判断：键来的时候构建时存下的文字可能已经落后了。
func (h ComposerHost) slashKey(p *Composer, k ui.TextKey) bool {
	if k.Mods != 0 || h.Commands == nil {
		return false
	}
	switch k.Key {
	case ui.KeyUp, ui.KeyDown, ui.KeyTab, ui.KeyEnter, ui.KeyEscape:
	default:
		return false
	}
	text := k.Text()
	items := h.slashMatches(p, text)
	if len(items) == 0 {
		return false
	}
	at := p.slashAt(text, len(items))
	switch k.Key {
	case ui.KeyUp:
		p.slash.at = (at + len(items) - 1) % len(items)
	case ui.KeyDown:
		p.slash.at = (at + 1) % len(items)
	case ui.KeyEscape:
		p.slash.off = text
	default: // Tab、Enter：采纳成"/命令 "，接着打参数
		k.SetText("/" + items[at].Name + " ")
		p.slash = slashState{caretEnd: true}
	}
	return true
}

// slashAccept 是鼠标点了一条：把文字换成"/命令 "，焦点回输入框。
func (p *Composer) slashAccept(name string) {
	if p.Main.Sending {
		return
	}
	p.Main.Text = "/" + name + " "
	p.slash = slashState{caretEnd: true}
	p.FocusMain()
}

// slashPanel 画命令列表：面板和下拉菜单同一套（浮层底色、细边、圆角 = 控件圆角，内距 S1，选项圆角同心）。
func (h ComposerHost) slashPanel(c *ui.Context, p *Composer) {
	items := h.slashMatches(p, p.Main.Text)
	if len(items) == 0 {
		return
	}
	t := h.T
	at := p.slashAt(p.Main.Text, len(items))
	first := min(max(at-slashRows+1, 0), max(len(items)-slashRows, 0)) // 选中项始终在窗口里
	last := min(first+slashRows, len(items))
	inner := t.Inner(t.RCtl, t.S1)
	// 进出场照菜单（motion.menu-in/out、travel.menu、scale.menu）；面板在输入框上方占位，所以高度也收放（Collapse），输入框跟着平滑长高、缩回，不跳。
	m := ui.Motion{Y: float32(t.D.Num.TravelMenu), Scale: float32(t.D.Num.ScaleMenu), Collapse: true}
	ui.Column(c.Key("slash")).Label("斜杠命令").FillWidth().Shrink(0).Padding(t.S1).Gap(t.S1).Radius(t.RCtl).Background(t.C.Float).Border(1, t.C.FloatRing).
		TransformOrigin(.5, 1).Transition(PopTransition(t.D.Motion.MotionMenuIn, t.D.Motion.MotionMenuOut, m)).Children(func() {
		for i := first; i < last; i++ {
			it := items[i]
			row := ui.ButtonBase(c.Key(it.Name)).Label("命令 /"+it.Name).FillWidth().Height(t.HSm).Shrink(0).Padding(0, t.S2).Gap(t.S2).AlignItems(ui.Center).Radius(inner).
				TextColor(h.C.Text).OnClick(func() { p.slashAccept(it.Name) })
			bg := ui.Transparent
			if i == at || row.Hovered() {
				bg = t.C.Hover
			}
			row.Background(t.Fill(row, bg)).Children(func() { // 键盘上下移和悬停的高亮都淡变
				ui.Text(c.Key("name"), "/"+it.Name).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[1]).TextColor(h.C.Text).Shrink(0).SingleLine()
				if it.Summary != "" {
					ui.Text(c.Key("summary"), it.Summary).Font(h.Font).FontSize(t.FS[1]).TextColor(h.C.Muted).Grow(1).MinWidth(0).SingleLine()
				}
			})
		}
	})
}
