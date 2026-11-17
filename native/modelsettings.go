package main

// 模型页照 MewTypeUI gallery/settings.go：宿主持有配置，组件只负责交互。
import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/loadui"
	"sumika-native/internal/winshell"
)

type modelTarget struct {
	field     config.ModelField
	character string
	// model 只给"模型的窗口和单次输出"那一节用：被查看或覆盖的模型 ID
	model string
}

// modelPickerState 是设置页里一个模型下拉的开合和搜索字。
type modelPickerState struct {
	open  bool
	query string
}

type modelPageState struct {
	pickers               map[modelTarget]*modelPickerState
	writer                api.ModelSettingsWriter
	view                  api.ModelSettingsView
	entered, loaded, busy bool
	load                  loadstate.Loader // 读取、保存的提示什么时候出现（延迟出现、出现后留够；见 loadstate）
	request               uint64
	pending               modelTarget
	notes                 map[modelTarget]string
	loadError             string
	items                 []settings.ChoiceItem
	chooser               settings.ChooseState
	choices               []settings.ChoiceItem
	target                modelTarget
	title, selected       string
	number                settings.NumberState
	numberBox             overlay.BoxState
	numberError           string
	limitOpen             map[string]bool // “模型的上下文和输出”里展开了哪几个模型（按模型 ID）
}

// 所有读写共用一个在途槽；保存版本来自最后一次接受的视图，不让并发写互相冲突。
func (a *app) modelJob(target modelTarget, change *api.ModelSettingsChange, refresh bool) {
	s := &a.models
	if s.busy {
		return
	}
	s.busy, s.pending = true, target
	s.load.Begin()
	s.request++
	request, generation, writer, version := s.request, a.srcGen, s.writer, s.view.Version
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var v api.ModelSettingsView
		var err error
		switch {
		case change != nil:
			v, err = writer.SetModelSettings(ctx, *change, version)
		case refresh:
			v, err = writer.RefreshModels(ctx)
		default:
			v, err = writer.ModelSettings(ctx)
		}
		a.post(func() {
			if a.srcGen != generation || a.models.request != request {
				return
			}
			s := &a.models
			s.busy = false
			defer func() { // 页面手里有数据就算读到了，没有才算没读成
				if s.loaded {
					s.load.Succeed()
				} else {
					s.load.Fail()
				}
			}()
			var saved *api.SavedModelSettingsError
			switch {
			case err == nil:
				s.accept(v)
				delete(s.notes, target)
				s.loadError = ""
			case errors.Is(err, api.ErrModelSettingsVersion):
				s.accept(v)
				s.notes[target] = "配置文件在外面改过，已重新读取"
			case errors.As(err, &saved):
				s.accept(v)
				s.notes[target] = "已保存，部分没生效：" + strings.Join(saved.Reasons, "；")
			case change != nil:
				s.notes[target] = err.Error()
			default:
				s.loadError = "读不到模型设置：" + err.Error()
			}
		})
	}()
}
func (s *modelPageState) accept(v api.ModelSettingsView) {
	s.view, s.loaded = v, true
	if s.notes == nil {
		s.notes = map[modelTarget]string{}
	}
	s.items = nil
	for _, g := range v.Groups {
		for _, m := range g.Models {
			s.items = append(s.items, settings.ChoiceItem{ID: m.ID, Title: m.Name, Subtitle: m.Subtitle, GroupID: g.Provider, GroupTitle: g.Provider})
		}
	}
}
func (a *app) enterModels() {
	s := &a.models
	if s.entered {
		return
	}
	s.entered = true
	if s.writer == nil && a.demoMode {
		s.writer = newDemoModelSettings(a.chars)
	}
	if s.writer == nil {
		s.writer, _ = a.reader.(api.ModelSettingsWriter)
	}
	if s.writer == nil {
		s.loadError = "当前后台没有提供模型设置"
		return
	}
	if s.notes == nil {
		s.notes = map[modelTarget]string{}
	}
	a.modelJob(modelTarget{}, nil, false)
}
func (s *modelPageState) leave() {
	s.entered = false
	s.load.Cancel()
	s.chooser.Open, s.numberBox.Open = false, false
}
func (s *modelPageState) name(id string) string {
	if id == "" {
		return "还没有选择"
	}
	for _, m := range s.view.Models {
		if m.ID == id {
			return m.Name
		}
	}
	return id
}
func (s *modelPageState) readOnly(key string) api.ModelReadOnly {
	for _, r := range s.view.ReadOnly {
		if r.Key == key {
			return r
		}
	}
	return api.ModelReadOnly{}
}
func (s *modelPageState) value(target modelTarget, v api.ModelSettingsValues) string {
	switch target.field {
	case config.CharacterModel:
		return v.Characters[target.character].Model
	case config.CharacterBackground:
		return v.Characters[target.character].Background
	case config.BackgroundModel:
		return v.BackgroundModel
	case config.BackgroundThinking:
		return string(v.BackgroundThinking)
	case config.ThinkingLocal:
		return string(v.Local)
	case config.ThinkingPrivate:
		return string(v.Private)
	case config.ThinkingGroup:
		return string(v.Group)
	case config.ContextWindow:
		return strconv.Itoa(v.Context.Window)
	case config.ContextReserve:
		return strconv.Itoa(v.Context.Reserve)
	case config.ContextKeepTokens:
		return strconv.Itoa(v.Context.KeepTokens)
	case config.ContextSoftRatio:
		return strconv.FormatFloat(v.Context.SoftRatio, 'f', -1, 64)
	case config.ContextHardRatio:
		return strconv.FormatFloat(v.Context.HardRatio, 'f', -1, 64)
	case config.ContextAutoCompact:
		return strconv.FormatBool(v.Context.AutoCompactChat)
	}
	return ""
}
func (s *modelPageState) reason(target modelTarget) string {
	v := s.view
	if !v.RunningKnown {
		return "开始接收以后生效"
	}
	configured, running := s.value(target, v.Configured), s.value(target, v.Running)
	if configured != running {
		return "实际在用：" + running + "；" + v.RunningReason
	}
	for _, ch := range v.Characters {
		if ch.ID == target.character && target.field == config.CharacterModel && ch.ConfiguredModel != ch.RunningModel {
			return "实际在用：" + s.name(ch.RunningModel) + "；" + v.RunningReason
		}
	}
	for _, active := range v.Active {
		if target.character != "" && active.Character != target.character {
			continue
		}
		different := false
		switch target.field {
		case config.CharacterModel:
			different = active.Model != configured
		case config.ThinkingLocal:
			different = strings.HasPrefix(active.Conversation, "console:") && active.Thinking != configured
		case config.ThinkingPrivate:
			different = strings.HasPrefix(active.Conversation, "qq:private:") && active.Thinking != configured
		case config.ThinkingGroup:
			different = strings.HasPrefix(active.Conversation, "qq:group:") && active.Thinking != configured
		case config.ContextWindow:
			different = v.Configured.Context.Window != 0 && active.Window != v.Configured.Context.Window
		case config.ContextReserve:
			different = v.Configured.Context.Reserve != 0 && active.Reserve != v.Configured.Context.Reserve
		case config.ContextSoftRatio:
			different = active.Soft != v.Configured.Context.SoftRatio
		case config.ContextHardRatio:
			different = active.Hard != v.Configured.Context.HardRatio
		}
		if different {
			return v.RunningReason
		}
	}
	if target.field == config.BackgroundModel || target.field == config.CharacterBackground {
		return strings.Join(v.ApplyErrors, "；")
	}
	return ""
}
func (a *app) modelNote(c *ui.Context, text string, failure bool) {
	if text == "" {
		return
	}
	if k := a.settingsKit(); k != nil {
		k.Note(c, text, failure)
		return
	}
	m := theme.T(c).Metrics().Settings()
	style := theme.T(c).Typography().Body()
	ui.Row(c).FillWidth().Justify(ui.End).Padding(0, m.ButtonNoIconPaddingRight(), c.Theme().Space(0.5), m.ButtonNoIconPaddingLeft()).Children(func() {
		// 状态和失败原因直接替换，避免文字过渡保留旧的加载提示。
		e := ui.Text(c, text).Font(theme.T(c).Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(c.Theme().TextMuted).SingleLine().MinWidth(0).Tooltip(text)
		if failure {
			e.TextColor(c.Theme().Danger)
		}
	})
}
func (a *app) modelRow(c *ui.Context, title, value string, target modelTarget, disabled bool, action func()) {
	s := &a.models
	lv := s.load.View(c.Now())
	loadui.Wake(c, lv)
	if s.busy && s.pending == target && loadui.Visible(lv) {
		value += " · 正在保存…"
	}
	a.settingsRow(c.Key(target), title, settings.ButtonOptions{Value: value, MutedValue: true, Arrow: true, Disabled: disabled || s.busy, OnActivate: action})
	if disabled {
		a.modelNote(c, s.view.ModelsDisabledReason, false)
	}
	a.modelNote(c, s.reason(target), false)
	a.modelNote(c, s.notes[target], true)
}
func (a *app) modelChoice(c *ui.Context, title string, target modelTarget) {
	s := &a.models
	id := s.value(target, s.view.Configured)
	value := s.name(id)
	if id == "" && target.field == config.CharacterBackground {
		value = "跟全局"
	}
	if id == "" && target.field == config.BackgroundModel {
		value = "不设置"
	}
	if k := a.settingsKit(); k != nil && s.view.ModelsEditable {
		a.modelPicker(c, k, title, value, target, id)
		return
	}
	a.modelRow(c, title, value, target, !s.view.ModelsEditable, func() {
		s.target, s.title, s.selected = target, title, id
		s.choices = s.items
		if target.field != config.CharacterModel {
			label := "不设置"
			if target.field == config.CharacterBackground {
				label = "跟全局"
			}
			s.choices = append([]settings.ChoiceItem{{ID: "@inherit", Title: label}}, s.items...)
			if id == "" {
				s.selected = "@inherit"
			}
		}
		s.chooser.Show()
	})
	missing := id != "" && !s.view.ModelsReadAt.IsZero()
	for _, m := range s.view.Models {
		if m.ID == id {
			missing = false
			break
		}
	}
	if missing {
		a.modelNote(c, "列表里没有了", true)
	}
}

// modelPicker 是新界面的选模型：一行里一个带搜索的分组下拉（主人 2026-11-09：不要整窗的单选列表）。
func (a *app) modelPicker(c *ui.Context, k *winshell.Kit, title, value string, target modelTarget, id string) {
	s := &a.models
	if s.pickers == nil {
		s.pickers = map[modelTarget]*modelPickerState{}
	}
	st := s.pickers[target]
	if st == nil {
		st = &modelPickerState{}
		s.pickers[target] = st
	}
	var items []winshell.MenuItem
	pick := func(newID string) func() {
		return func() {
			if newID != id {
				a.modelJob(target, &api.ModelSettingsChange{Field: target.field, Character: target.character, String: &newID}, false)
			}
		}
	}
	if target.field != config.CharacterModel {
		label := "不设置"
		if target.field == config.CharacterBackground {
			label = "跟全局"
		}
		items = append(items, winshell.MenuItem{Label: label, Checked: id == "", Action: pick("")})
	}
	items = append(items, s.modelMenuItems(id, pick)...)
	key := fmt.Sprint("model-picker-", target.field, "-", target.character)
	if s.busy && s.pending == target {
		value += " · 正在保存…"
	}
	k.Row(c, key+"-row", title, "", func() { k.Picker(c, key, title, value, &st.open, &st.query, "搜索名称或供应商", items) })
	a.modelNote(c, s.reason(target), false)
	a.modelNote(c, s.notes[target], true)
}

// modelMenuItems 是按供应商分组的模型菜单项；同一组里同名的（同一个模型的不同线路）带上 id 才分得清。输入框的模型胶囊也用它。
func (s *modelPageState) modelMenuItems(current string, pick func(id string) func()) []winshell.MenuItem {
	names := map[string]int{}
	for _, g := range s.view.Groups {
		for _, m := range g.Models {
			names[g.Provider+"\x00"+m.Name]++
		}
	}
	var items []winshell.MenuItem
	add := func(group string, m api.ModelOption) {
		label := m.Name
		if names[group+"\x00"+m.Name] > 1 {
			label = m.ID[strings.LastIndex(m.ID, "/")+1:]
		}
		items = append(items, winshell.MenuItem{Label: label, Group: group, Checked: m.ID == current, Action: pick(m.ID)})
	}
	for _, g := range s.view.Groups {
		for _, m := range g.Models {
			add(g.Provider, m)
		}
	}
	if len(s.view.Groups) == 0 {
		for _, m := range s.view.Models {
			add("", m)
		}
	}
	return items
}

// modelThinkingDropdown 是思考深度：选项少，普通下拉就够。
func (a *app) modelThinkingDropdown(c *ui.Context, k *winshell.Kit, title string, target modelTarget, id string) {
	s := &a.models
	if s.pickers == nil {
		s.pickers = map[modelTarget]*modelPickerState{}
	}
	st := s.pickers[target]
	if st == nil {
		st = &modelPickerState{}
		s.pickers[target] = st
	}
	current := -1
	names := make([]string, len(modelThinking))
	for i, level := range modelThinking {
		names[i] = level.Title
		if level.ID == id {
			current = i
		}
	}
	key := fmt.Sprint("model-thinking-", target.field)
	k.Row(c, key+"-row", title, "", func() {
		k.Dropdown(c, key, title, &st.open, current, names, func(i int) {
			value := modelThinking[i].ID
			a.modelJob(target, &api.ModelSettingsChange{Field: target.field, String: &value}, false)
		}, nil)
	})
	a.modelNote(c, s.reason(target), false)
	a.modelNote(c, s.notes[target], true)
}

var modelThinking = []settings.ChoiceItem{
	{ID: "off", Title: "关"}, {ID: "minimal", Title: "最低"}, {ID: "low", Title: "低"}, {ID: "medium", Title: "中"}, {ID: "high", Title: "高"}, {ID: "xhigh", Title: "很高"}, {ID: "max", Title: "最高"},
}

func (a *app) modelThinkingRow(c *ui.Context, title string, field config.ModelField) {
	s := &a.models
	target := modelTarget{field: field}
	id := s.value(target, s.view.Configured)
	label := id
	for _, level := range modelThinking {
		if level.ID == id {
			label = level.Title
		}
	}
	if k := a.settingsKit(); k != nil {
		a.modelThinkingDropdown(c, k, title, target, id)
		return
	}
	a.modelRow(c, title, label, target, false, func() {
		s.target, s.title, s.selected, s.choices = target, title, id, modelThinking
		s.chooser.Show()
	})
}
func (a *app) modelNumberRow(c *ui.Context, title string, field config.ModelField) {
	s := &a.models
	target := modelTarget{field: field}
	value := s.value(target, s.view.Configured)
	if k := a.settingsKit(); k != nil { // 新界面：一行里直接加减，改完即存
		a.modelNumberStepper(c, k, title, target, value)
		return
	}
	shown := value
	if field == config.ContextWindow && value == "0" {
		shown = "跟模型" // 0 = 跟着模型的上下文长度（规格-模型设置页 3.1）
	}
	a.modelRow(c, title, shown, target, false, func() {
		s.target, s.title, s.number.Text, s.numberError = target, title, value, ""
		s.numberBox.Open = true
	})
}

// modelNumberSave 是旧界面数字框的“保存”：合法就关框并交给后台，不合法把原因写在框里。
func (a *app) modelNumberSave(text string) {
	s := &a.models
	if msg := a.modelNumberApply(s.target, text); msg != "" {
		s.numberError = msg
		return
	}
	s.numberBox.Open = false
}

// modelNumberApply 校验一个数并交给后台保存，新旧界面共用。不合法就不写入，返回原因；合法返回空串。
// 校验规矩不变：模型窗口、单次输出覆盖是 ≥ 0 的整数（窗口非 0 时至少 1000，0 去掉覆盖）；
// 比例在 0.1 到 1 之间，开始压缩不能大于强制压缩；窗口 0 或 ≥ 1000，保留原文 ≥ 1000，预留 ≥ 0。
func (a *app) modelNumberApply(target modelTarget, text string) string {
	s := &a.models
	ch := api.ModelSettingsChange{Field: target.field, Model: target.model}
	if target.model != "" {
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 0 || n != 0 && target.field == config.ModelContextOverride && n < 1000 {
			return "请输入整数：0 去掉覆盖，窗口至少 1000"
		}
		ch.Int = &n
		delete(s.notes, target)
		a.modelJob(target, &ch, false)
		return ""
	}
	ratio := ch.Field == config.ContextSoftRatio || ch.Field == config.ContextHardRatio
	if ratio {
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(n) || n < .1 || n > 1 {
			return "请输入 0.1 到 1 之间的小数"
		}
		if ch.Field == config.ContextSoftRatio && n > s.view.Configured.Context.HardRatio || ch.Field == config.ContextHardRatio && n < s.view.Configured.Context.SoftRatio {
			return "开始压缩的比例不能大于强制压缩的比例"
		}
		ch.Float = &n
		// 两个比例互相限制：这一边改成功了，另一边留着的“不能大于/小于”说明就过时了
		delete(s.notes, modelTarget{field: config.ContextSoftRatio})
		delete(s.notes, modelTarget{field: config.ContextHardRatio})
	} else {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 || ch.Field == config.ContextWindow && n != 0 && n < 1000 || ch.Field == config.ContextKeepTokens && n < 1000 {
			return "请输入范围内的整数：" + modelNumberHint(target)
		}
		ch.Int = &n
	}
	delete(s.notes, target)
	a.modelJob(target, &ch, false)
	return ""
}

// modelNumberHint 是一项数字设置允许填什么，数字框下面和行下的拒绝说明共用。
func modelNumberHint(target modelTarget) string {
	switch {
	case target.field == config.ContextSoftRatio || target.field == config.ContextHardRatio:
		return "范围 0.1 到 1；开始压缩不能晚于强制压缩"
	case target.field == config.ContextKeepTokens:
		return "最少保留 1000 token"
	case target.field == config.ContextWindow:
		return "0 表示跟着模型，手动设置至少 1000 token"
	case target.model != "":
		return "写 0 去掉这项覆盖，回到服务商或资料库的值"
	}
	return "请输入大于或等于 0 的整数"
}

// modelNumberSpec 是一项数字设置在加减框里的取值规则；比例按整数百分比显示（0.85 写 85%），percent 为真。
func modelNumberSpec(field config.ModelField) (spec winshell.StepperSpec, percent bool) {
	switch field {
	case config.ContextWindow:
		return winshell.StepperSpec{Min: 1000, Step: 10000, AllowZero: true, ZeroLabel: "跟模型"}, false
	case config.ContextReserve:
		return winshell.StepperSpec{Step: 1000, AllowZero: true}, false
	case config.ContextKeepTokens:
		return winshell.StepperSpec{Min: 1000, Step: 5000}, false
	case config.ContextSoftRatio, config.ContextHardRatio:
		return winshell.StepperSpec{Min: 10, Max: 100, Step: 5, Unit: "%"}, true
	}
	return winshell.StepperSpec{Step: 1000, AllowZero: true}, false
}

// modelNumberStepper 是新界面的数字设置：一行里 [−] [数字] [+]，点一下或打完回车就存，不再弹窗。
// 保存中加减钮灰掉（后台一次只收一个写）；不合法的值不写入，原因写在这一行下面。
func (a *app) modelNumberStepper(c *ui.Context, k *winshell.Kit, title string, target modelTarget, value string) {
	s := &a.models
	spec, percent := modelNumberSpec(target.field)
	spec.Disabled = s.busy
	shown, _ := strconv.ParseFloat(value, 64)
	if percent {
		shown = math.Round(shown*1e8) / 1e6
	}
	key := fmt.Sprint("model-number-", target.field)
	k.Row(c, key+"-row", title, "", func() {
		k.Stepper(c, key, title, shown, spec, func(v float64) {
			if percent {
				v /= 100
			}
			if msg := a.modelNumberApply(target, strconv.FormatFloat(v, 'f', -1, 64)); msg != "" {
				if s.notes == nil {
					s.notes = map[modelTarget]string{}
				}
				s.notes[target] = msg
			}
		})
	})
	a.modelNote(c, s.reason(target), false)
	a.modelNote(c, s.notes[target], true)
	a.modelSavingNote(c, target)
}

// modelSavingNote 在保存拖得久了才写“正在保存…”（保存得快就不插这一行，免得行跳）。
func (a *app) modelSavingNote(c *ui.Context, target modelTarget) {
	s := &a.models
	if lv := s.load.View(c.Now()); s.busy && s.pending == target && loadui.Visible(lv) {
		a.modelNote(c, "正在保存…", false)
	}
}
func (a *app) modelDialogs(c *ui.Context) {
	s := &a.models
	settings.ChooseBox(c.Key("model-chooser"), &s.chooser, s.choices, s.selected, s.title, settings.ChooseOptions{
		SearchLabel: "搜索模型", SearchPlaceholder: "搜索名称或能力", ClearLabel: "清空搜索", EmptyLabel: "没有匹配的选项", CloseLabel: "取消", Wide: true, WideText: true,
		OnSelect: func(id string) {
			if s.target.model != "" { // 模型窗口和单次输出那一节的菜单
				s.chooser.Open = false
				a.modelLimitPick(id)
				return
			}
			if id == "@inherit" {
				id = ""
			}
			s.chooser.Open = false
			a.modelJob(s.target, &api.ModelSettingsChange{Field: s.target.field, Character: s.target.character, String: &id}, false)
		},
	})
	field := s.target.field
	ratio := field == config.ContextSoftRatio || field == config.ContextHardRatio
	hint := modelNumberHint(s.target)
	a.settingsBox(c, "model-number", &s.numberBox, s.title, overlay.BoxOptions{Actions: []overlay.BoxAction{{ID: "save", Label: "保存", Default: true, KeepOpen: true}, {ID: "cancel", Label: "取消"}}, OnAction: func(id string) {
		if id == "save" {
			a.modelNumberSave(s.number.Text)
		}
	}}, func() {
		if ratio {
			// NumberInput 只收整数，比例必须保留 config 允许的任意小数精度。
			a.settingsField(c, "model-ratio-input", &s.number.Text, "编辑数值", func() { a.modelNumberSave(s.number.Text) })
		} else {
			a.settingsNumber(c, "model-number-input", &s.number, "编辑数值", 0, true, s.numberError != "", a.modelNumberSave)
		}
		a.modelNote(c, hint, false)
		a.modelNote(c, s.numberError, true)
	})
}
func (a *app) pageModelSettings(c *ui.Context) {
	a.enterModels()
	s := &a.models
	ui.Column(c).FillWidth().MinWidth(0).Gap(0).Children(func() {
		a.settingsHeading(c, "服务商")
		address := s.readOnly("magpie.baseUrl").Value
		a.settingsRow(c, "Magpie 地址", settings.ButtonOptions{Value: address, MutedValue: true, Disabled: true})
		lv := s.load.View(c.Now())
		loadui.Wake(c, lv)
		status := "还没有读取模型列表"
		switch {
		case s.busy && !s.loaded:
			status = "\u3000" // 先留空，读得慢了才写提示：状态出现前后这一行不变
			if loadui.Visible(lv) {
				status = "正在读取模型设置…"
			}
		case s.view.ModelsError != "":
			status = "连接失败，点这里重试"
		case !s.view.ModelsReadAt.IsZero():
			status = fmt.Sprintf("已连接，%d 个 · %s 读取", len(s.view.Models), s.view.ModelsReadAt.Local().Format("15:04"))
		case s.loaded && !s.view.ModelsEditable:
			status = s.view.ModelsDisabledReason
		}
		// 状态放在"模型列表"这一行的右边，点这一行就是刷新（照 Telegram 设置行：右边是当前值）
		a.settingsRow(c, "模型列表", settings.ButtonOptions{Value: status, MutedValue: true, Disabled: s.busy || s.writer == nil, OnActivate: func() { a.modelJob(modelTarget{}, nil, true) }})
		if s.view.ModelsError != "" {
			note := "连接失败：" + s.view.ModelsError
			if !s.view.ModelsReadAt.IsZero() {
				note += "；用的是 " + s.view.ModelsReadAt.Local().Format("15:04") + " 的缓存"
			}
			a.modelNote(c, note, true)
		}
		a.modelNote(c, s.loadError, true)
		if !s.loaded {
			return
		}
		a.settingsBreak(c, "")
		a.settingsHeading(c, "角色模型")
		for _, ch := range s.view.Characters {
			name := ch.ID
			if character, ok := a.charOf(ch.ID); ok {
				name = character.Name
			}
			a.modelChoice(c, name+"的说话模型", modelTarget{field: config.CharacterModel, character: ch.ID})
			a.modelChoice(c, name+"的后台模型", modelTarget{field: config.CharacterBackground, character: ch.ID})
			a.settingsRow(c.Key("topic-"+ch.ID), name+"的话题模型", settings.ButtonOptions{Value: "话题还没做", MutedValue: true, Disabled: true})
		}
		a.settingsBreak(c, "")
		a.settingsHeading(c, "后台任务")
		a.modelChoice(c, "全局后台模型", modelTarget{field: config.BackgroundModel})
		a.modelThinkingRow(c, "后台思考", config.BackgroundThinking)
		a.settingsBreak(c, "")
		a.settingsHeading(c, "按场合思考")
		a.modelThinkingRow(c, "本地思考", config.ThinkingLocal)
		a.modelThinkingRow(c, "QQ 私聊思考", config.ThinkingPrivate)
		a.modelThinkingRow(c, "QQ 群聊思考", config.ThinkingGroup)
		a.settingsBreak(c, "")
		a.settingsHeading(c, "上下文")
		a.modelNumberRow(c, "上下文长度上限", config.ContextWindow)
		a.modelNumberRow(c, "留给回复和工具往返", config.ContextReserve)
		a.modelNumberRow(c, "压缩后保留的原文", config.ContextKeepTokens)
		a.modelNumberRow(c, "开始压缩的比例", config.ContextSoftRatio)
		a.modelNumberRow(c, "强制压缩的比例", config.ContextHardRatio)
		target := modelTarget{field: config.ContextAutoCompact}
		a.settingsToggle(c, target, "本地闲聊自动压缩", s.view.Configured.Context.AutoCompactChat, s.busy, func(value bool) {
			a.modelJob(target, &api.ModelSettingsChange{Field: target.field, Bool: &value}, false)
		})
		a.modelNote(c, s.reason(target), false)
		a.modelNote(c, s.notes[target], true)
		if lv := s.load.View(c.Now()); s.busy && s.pending == target && loadui.Visible(lv) { // 保存得快就不插这一行
			a.modelNote(c, "正在保存…", false)
		}
		a.settingsRow(c, "话题压缩门槛", settings.ButtonOptions{Value: "话题还没做", MutedValue: true, Disabled: true})
		a.settingsBreak(c, "")
		a.modelLimitsSection(c)
		a.settingsBreak(c, "")
		a.settingsHeading(c, "识图")
		a.settingsRow(c, "识图模型", settings.ButtonOptions{Value: "Go 还没做识图", MutedValue: true, Disabled: true})
	})
	a.modelDialogs(c)
}
