package main

// 设置、记忆、运行这些还没按原型重做的页面用到的控件入口：新界面走 winshell 的标准控件（分段、筛选片、勾选、输入框、
// 搜索框、对话框、下拉、按钮、空态、徽标），不再用旧组件库的（盘点-组件复用-2026-11-09）。
// 旧界面（mainView=legacy）里仍然走各自原来的写法，旧界面退役时这个文件的旧分支一起删。
// 盘点见规格-新界面主线第十节“分段和筛选片的盘点”。

import (
	"fmt"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/button"
	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/ELY-Open-Source/CrychicUI/feedback"
	"github.com/ELY-Open-Source/CrychicUI/input"
	"github.com/ELY-Open-Source/CrychicUI/layout"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// segmentedChoice 是“几选一、固定几格”的分段控件。key 要在整窗里不重复。
func (a *app) segmentedChoice(c *ui.Context, key, label string, labels []string, sel *int) {
	if a.workbenchEnabled() {
		if k := a.workbench.shell.kit; k != nil {
			k.Segment(c, key, sel, labels)
			return
		}
	}
	ui.Segmented(c, sel, labels...).Label(label)
}

// settingsToggle 是“一行字加一个开关”的设置项，要提交到后台的那种：新界面用标准开关，旧界面走原来的。
func (a *app) settingsToggle(c *ui.Context, key any, title string, value, disabled bool, onChange func(bool)) {
	if k := a.settingsKit(); k != nil {
		id := fmt.Sprint("setting-", key)
		k.Row(c, id, title, "", func() { k.SwitchValue(c, id+"-switch", title, value, disabled, onChange) })
		return
	}
	settings.ToggleRow(c.Key(key), title, value, settings.ToggleOptions{Disabled: disabled, OnChange: onChange})
}

// settingsRow 是“名字 + 当前值 + 箭头”的一行：新界面用外壳的，旧界面走原来的。
func (a *app) settingsRow(c *ui.Context, title string, o settings.ButtonOptions) {
	if k := a.settingsKit(); k != nil {
		k.ValueRow(c, title, o.Value, o.Arrow, o.Disabled, o.OnActivate)
		return
	}
	settings.Button(c, title, o)
}

// settingsGroup 是一组设置：新界面小标题加内容，旧界面 layout.Section。
func (a *app) settingsGroup(c *ui.Context, title string, fn func()) {
	if k := a.settingsKit(); k != nil {
		k.SectionHeading(c, title)
		ui.Column(c).FillWidth().Gap(k.G.SpaceSmall).Children(fn)
		return
	}
	group(c, title, "", fn)
}

// settingsHeading 是一节的小标题。
func (a *app) settingsHeading(c *ui.Context, title string) {
	if k := a.settingsKit(); k != nil {
		k.SectionHeading(c, title)
		return
	}
	settings.SectionTitle(c, title)
}

// settingsOneOf 是“几选一”的一行：新界面标题加分段控件，旧界面一列单选。legacy 是旧界面单选上的字，nil 就用 labels。
// 值由外面管：点了调 onChange，等后台确认后才变。
func (a *app) settingsOneOf(c *ui.Context, key, title string, labels, legacy []string, sel int, disabled bool, onChange func(int)) {
	a.settingsOneOfHint(c, key, title, "", labels, legacy, sel, disabled, onChange)
}

// settingsOneOfHint 同 settingsOneOf，行名下面多一行说明（旧界面没有说明位，调用方把要紧的话写进 legacy 的选项里）。
func (a *app) settingsOneOfHint(c *ui.Context, key, title, hint string, labels, legacy []string, sel int, disabled bool, onChange func(int)) {
	if k := a.settingsKit(); k != nil {
		k.Row(c, key+"-row", title, hint, func() {
			v := max(sel, 0)
			if k.Segment(c, key, &v, labels) && !disabled {
				onChange(v)
			}
		})
		return
	}
	if legacy == nil {
		legacy = labels
	}
	for i, l := range legacy {
		settings.Radio(c.Key(fmt.Sprint(key, "-", i)), l, sel == i, settings.ChoiceOptions{Disabled: disabled, OnChange: func(bool) { onChange(i) }})
	}
}

// settingsLocalToggle 是只改界面状态的开关（不提交后台）：一行字加一个开关。
func (a *app) settingsLocalToggle(c *ui.Context, key, title, hint string, v *bool) {
	if k := a.settingsKit(); k != nil {
		k.Row(c, key+"-row", title, hint, func() { k.Switch(c, key, title, v) })
		return
	}
	choice.ToggleRow(c, title, hint, v)
}

// settingRowKit 是后台配置项的一行（新界面）：左边名字和说明，右边值；值的来源弱一档，完整路径悬停才出现。
func (a *app) settingRowKit(c *ui.Context, k *winshell.Kit, it api.Result[api.Setting], name, value, note, source string) {
	g, p := k.G, k.P
	s := it.Value
	hint := s.Help
	if a.realDetail {
		detail := s.Key
		if path := s.SourcePath(); path != "" {
			detail += " · " + path
		}
		hint = strings.TrimSpace(hint + "\n" + detail)
	}
	k.Row(c, "setting-"+s.Key, name, hint, func() {
		ui.Column(c).MaxWidth(g.ControlWide).Shrink(0).Gap(g.SpaceMicro).AlignItems(ui.End).Children(func() {
			v := ui.Text(c, value).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Ink).Selectable().Wrap()
			if it.State != api.Ready {
				v.TextColor(p.Muted)
			}
			small := func(text string) ui.Element {
				return ui.Text(c, text).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
			}
			if note != "" {
				small(note)
			}
			if source != "" {
				e := small(source)
				if path := s.SourcePath(); path != "" {
					e.Tooltip("写在这个配置文件里\n" + path)
				}
			}
		})
	})
}

// settingsCheck 是一行勾选，值由外面管。
func (a *app) settingsCheck(c *ui.Context, key, label string, checked, disabled bool, onChange func(bool)) {
	if k := a.settingsKit(); k != nil {
		k.CheckValue(c, key, label, checked, disabled, onChange)
		return
	}
	settings.Check(c.Key(key), label, checked, settings.ChoiceOptions{Disabled: disabled, OnChange: onChange})
}

// settingsField 是一行文字输入，onSubmit 可以是 nil。
func (a *app) settingsField(c *ui.Context, key string, value *string, label string, onSubmit func()) {
	var e ui.Element
	if k := a.settingsKit(); k != nil {
		e = k.TextField(c, key, value, label).FillWidth()
	} else {
		e = ui.TextInput(c.Key(key), value).Label(label).FillWidth()
	}
	if onSubmit != nil {
		e.OnSubmit(onSubmit)
	}
}

// settingsTextArea 是多行文字输入。
func (a *app) settingsTextArea(c *ui.Context, key string, value *string, label string, minLines, maxLines int) {
	if k := a.settingsKit(); k != nil {
		k.TextArea(c, key, value, label, minLines, maxLines)
		return
	}
	ui.TextArea(c.Key(key), value).Lines(minLines, maxLines).FillWidth().Label(label)
}

// settingsNumber 是只收数字的输入，默认带焦点。limit 为 0 不限上限；回车调 onSubmit。
func (a *app) settingsNumber(c *ui.Context, key string, st *settings.NumberState, label string, limit int, allowZero, failed bool, onSubmit func(string)) {
	if k := a.settingsKit(); k != nil {
		k.NumberField(c, key, &st.Text, label, limit, allowZero, func() { onSubmit(st.Text) })
		return
	}
	settings.NumberInput(c.Key(key), st, label, settings.NumberOptions{AutoFocus: true, Limit: int32(limit), AllowZero: allowZero, Error: failed, OnSubmit: onSubmit})
}

// settingsSearch 是搜索框。
func (a *app) settingsSearch(c *ui.Context, key string, value *string, hint string) {
	if k := a.settingsKit(); k != nil {
		k.SearchField(c, key, value, hint, nil)
		return
	}
	ui.TextInput(c.Key(key), value).Label(hint).FillWidth().Height(44)
}

// settingsButton 是按钮；primary 是强调色的主按钮。
func (a *app) settingsButton(c *ui.Context, key, label string, primary, disabled bool, fn func()) {
	if k := a.settingsKit(); k != nil {
		k.Button(c, key, label, primary, disabled, fn)
		return
	}
	if primary {
		button.Solid(c.Key(key), label).Disabled(disabled).OnClick(fn)
		return
	}
	ui.Button(c.Key(key), label).Disabled(disabled).OnClick(fn)
}

// ghostButton 是不抢眼的文字按钮，返回这一帧有没有被点。
func (a *app) ghostButton(c *ui.Context, label string) bool {
	if k := a.settingsKit(); k != nil {
		return k.Button(c, "ghost-"+label, label, false, false, func() {}).AlignSelf(ui.Start).Clicked()
	}
	return button.Ghost(c, label).Clicked()
}

// settingsDialogAction 是设置页对话框里的一个按钮。Danger 只有旧界面用（新界面的按钮还没有危险样式）。
type settingsDialogAction struct {
	ID, Label                                 string
	Default, Danger, Disabled, Left, KeepOpen bool
}

// settingsDialogSpec 是设置页对话框的标题、按钮和点了哪个之后的动作。页面用它描述对话框，不必 import 组件库。
// Locked 锁住：新、旧界面都不能用右上角关闭、Esc 或点遮罩关掉，只能点按钮；恢复码这种只显示一次的用。
type settingsDialogSpec struct {
	Title      string
	Wide       bool
	BodyHeight float32
	Locked     bool
	Actions    []settingsDialogAction
	OnAction   func(id string)
}

// settingsDialog 是带按钮行的对话框：新界面用外壳的，旧界面走原来的。
func (a *app) settingsDialog(c *ui.Context, key string, open *bool, sp settingsDialogSpec, body func()) {
	if k := a.settingsKit(); k != nil {
		actions := make([]winshell.DialogAction, len(sp.Actions))
		for i, x := range sp.Actions {
			actions[i] = winshell.DialogAction{ID: x.ID, Label: x.Label, Default: x.Default, Disabled: x.Disabled, Left: x.Left, KeepOpen: x.KeepOpen}
		}
		k.ActionDialog(c, key, open, winshell.DialogSpec{Title: sp.Title, Wide: sp.Wide, BodyHeight: sp.BodyHeight, Locked: sp.Locked, Actions: actions, OnAction: sp.OnAction}, body)
		return
	}
	actions := make([]overlay.BoxAction, len(sp.Actions))
	for i, x := range sp.Actions {
		actions[i] = overlay.BoxAction{ID: x.ID, Label: x.Label, Danger: x.Danger, Default: x.Default, Disabled: x.Disabled, Left: x.Left, KeepOpen: x.KeepOpen}
	}
	st := overlay.BoxState{Open: *open}
	overlay.Box(c.Key(key), &st, sp.Title, overlay.BoxOptions{Wide: sp.Wide, Locked: sp.Locked, FullBody: sp.BodyHeight > 0, BodyHeight: sp.BodyHeight, Actions: actions, OnAction: sp.OnAction}, body)
	*open = st.Open
}

// settingsBox 是带按钮行的对话框：新界面用外壳的，旧界面走原来的。
func (a *app) settingsBox(c *ui.Context, key string, st *overlay.BoxState, title string, o overlay.BoxOptions, body func()) {
	k := a.settingsKit()
	if k == nil {
		overlay.Box(c.Key(key), st, title, o, body)
		return
	}
	actions := make([]winshell.DialogAction, len(o.Actions))
	for i, x := range o.Actions {
		actions[i] = winshell.DialogAction{ID: x.ID, Label: x.Label, Default: x.Default, Disabled: x.Disabled, Left: x.Left, KeepOpen: x.KeepOpen}
	}
	sp := winshell.DialogSpec{Title: title, Wide: o.Wide, Locked: o.Locked, Actions: actions, OnAction: o.OnAction}
	if o.FullBody {
		sp.BodyHeight = o.BodyHeight
	}
	k.ActionDialog(c, key, &st.Open, sp, body)
}

// pickDrop 是键值下拉：显示 labels，选中的键写进 *key，选了别的返回 true。fill 时铺满这一栏，否则定宽（放在一排里）。
func (a *app) pickDrop(c *ui.Context, label string, values []string, labels []string, key *string, fill bool) bool {
	idx := 0
	for i, v := range values {
		if v == *key {
			idx = i
		}
	}
	k := a.settingsKit()
	if k == nil {
		sel := labels[idx]
		choice.Dropdown(c, label, labels, &sel)
		if sel != labels[idx] {
			for i, l := range labels {
				if l == sel {
					*key = values[i]
					return true
				}
			}
		}
		return false
	}
	if a.dropOpen == nil {
		a.dropOpen = map[string]*bool{}
	}
	open := a.dropOpen[label]
	if open == nil {
		open = new(bool)
		a.dropOpen[label] = open
	}
	changed := false
	trigger := k.Dropdown(c, "drop-"+label, label, open, idx, labels, func(i int) {
		*key = values[i]
		changed = true
	}, nil)
	if fill {
		trigger.FillWidth()
	} else {
		trigger.Width(k.G.ControlWide * .6)
	}
	return changed
}

// debouncedSearch 是停手一会儿才报告的搜索框。新界面不写“搜索”小标题和“稍等”行，框里的提示字够了。
func (a *app) debouncedSearch(c *ui.Context, key, label string, query, reported *string, hint, waiting string, delay time.Duration) {
	if k := a.settingsKit(); k != nil {
		k.DebouncedSearch(c, key, query, reported, hint, delay)
		return
	}
	input.DebouncedSearch(c, label, query, reported, input.DebouncedSearchOptions{Placeholder: hint, Searching: waiting, Delay: delay})
}

// emptyState 是“这里没东西”；icon 只有旧界面用。返回按钮这一帧有没有被点。
func (a *app) emptyState(c *ui.Context, icon, title, reason, action string) bool {
	if k := a.settingsKit(); k != nil {
		return k.EmptyState(c, "empty-"+title, title, reason, action)
	}
	_, act := feedback.EmptyState(c, icon, title, reason, action)
	return act.Valid() && act.Clicked()
}

// errorState 是“出错了”，带重试。返回重试这一帧有没有被点。
func (a *app) errorState(c *ui.Context, title, reason, retry string) bool {
	if k := a.settingsKit(); k != nil {
		return k.ErrorState(c, "error-"+title, title, reason, retry)
	}
	_, r := feedback.ErrorState(c, reason, feedback.ErrorStateOptions{Title: title, Retry: retry})
	return r.Valid() && r.Clicked()
}

// badge 是一小块状态字。
func (a *app) badge(c *ui.Context, text string, tone winshell.BadgeTone) {
	if k := a.settingsKit(); k != nil {
		k.Badge(c, "badge-"+text, text, tone)
		return
	}
	v := display.BadgeNeutral
	if tone == winshell.BadgeWarn {
		v = display.BadgeWarning
	}
	display.Badge(c, text, display.BadgeOptions{Variant: v})
}

// statusBadge 是“能 / 不能”的状态字。
func (a *app) statusBadge(c *ui.Context, text string, good bool) {
	if k := a.settingsKit(); k != nil {
		tone := winshell.BadgeNeutral
		if good {
			tone = winshell.BadgeGood
		}
		k.Badge(c, "status-"+text, text, tone)
		return
	}
	display.StatusBadge(c, text, good)
}

// keyValues 是详情里的“名字 + 值”列表。
func (a *app) keyValues(c *ui.Context, key string, items []display.KeyValuePair) {
	if k := a.settingsKit(); k != nil {
		out := make([]winshell.KeyValue, len(items))
		for i, it := range items {
			out[i] = winshell.KeyValue{Key: it.Key, Value: it.Value}
		}
		k.KeyValues(c, key, out)
		return
	}
	display.DescriptionList(c, items)
}

// settingsKit 是新界面的外壳组件；旧界面（或还没画过外壳）是 nil。
func (a *app) settingsKit() *winshell.Kit {
	if !a.workbenchEnabled() {
		return nil
	}
	return a.workbench.shell.kit
}

// pageTitle 是页面顶上的大标题：有自己画的窗口按钮时，顶行已经写了页名，不再写第二遍。
func (a *app) pageTitle(c *ui.Context, title string) {
	if a.workbenchEnabled() && a.winControls != nil {
		return
	}
	layout.PageHeader(c, title, layout.PageHeaderOptions{})
}

// chipChoice 是“选项多、会换行”的筛选片。选中的标签必须在 labels 里。
func (a *app) chipChoice(c *ui.Context, key, label string, labels []string, sel *string) {
	if a.workbenchEnabled() {
		if k := a.workbench.shell.kit; k != nil {
			idx := 0
			for i, l := range labels {
				if l == *sel {
					idx = i
				}
			}
			k.Chips(c, key, labels, &idx)
			*sel = labels[idx]
			return
		}
	}
	choice.ChipChoice(c, label, labels, sel)
}
