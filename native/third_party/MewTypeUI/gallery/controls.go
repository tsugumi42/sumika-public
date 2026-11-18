package gallery

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/control"
	"github.com/tsugumi42/MewTypeUI/settings"
	"github.com/tsugumi42/MewTypeUI/sound"
	"github.com/tsugumi42/MewTypeUI/theme"
	"strconv"
)

func init() {
	for _, p := range []Page{
		{ID: "controls/round", Area: "controls", Title: "圆角按钮", View: controlsRound},
		{ID: "controls/link", Area: "controls", Title: "文字链接", View: controlsLink},
		{ID: "controls/icon", Area: "controls", Title: "图标按钮", View: controlsIcon},
		{ID: "controls/input", Area: "controls", Title: "通用输入", View: controlsInput},
		{ID: "controls/dropdown", Area: "controls", Title: "下拉选择", View: controlsDropdown},
		{ID: "controls/segments", Area: "controls", Title: "分段选择", View: controlsSegments},
		{ID: "controls/tabs", Area: "controls", Title: "横向标签", View: controlsTabs},
		{ID: "controls/categories", Area: "controls", Title: "侧边分类", View: controlsCategories},
		{ID: "controls/title", Area: "controls", Title: "页面标题", View: controlsTitle},
		{ID: "controls/badge", Area: "controls", Title: "状态标签", View: controlsBadge},
		{ID: "controls/collapse", Area: "controls", Title: "折叠区域", View: controlsCollapse},
		{ID: "controls/approval", Area: "controls", Title: "审批卡", View: controlsApproval},
		{ID: "controls/forward", Area: "controls", Title: "合并转发摘要", View: controlsForward},
		{ID: "controls/stop", Area: "controls", Title: "停止生成", View: controlsStop},
		{ID: "controls/sound", Area: "controls", Title: "提示音接口", View: controlsSound},
	} {
		Register(p)
	}
}
func controlsPage(c *ui.Context, content func()) {
	m := theme.T(c).Metrics()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().MaxWidth(m.Layout().ListMaxWidth()).Gap(m.Settings().SectionSkip()).Padding(m.Dialog().PaddingTop(), m.Dialog().PaddingRight(), m.Dialog().PaddingBottom(), m.Dialog().PaddingLeft()).Children(content)
	})
}
func controlsEvent(c *ui.Context) *string {
	return ui.Local(c.Root(), "controls-event", func() string { return "尚未激活" })
}
func controlsRound(c *ui.Context) {
	event := controlsEvent(c)
	n := ui.Local(c.Root(), "controls-number", func() int { return 8 })
	controlsPage(c, func() {
		settings.SectionTitle(c, "自然宽、定宽、全圆角与数字动画")
		for _, v := range []struct {
			id, label string
			o         control.RoundButtonOptions
		}{
			{"primary", "主要操作", control.RoundButtonOptions{}}, {"light", "轻操作", control.RoundButtonOptions{Tone: control.Light}},
			{"ghost", "透明轻操作", control.RoundButtonOptions{Tone: control.Light, Transparent: true}},
			{"disabled", "禁用", control.RoundButtonOptions{Disabled: true}},
			{"radius", "全圆角", control.RoundButtonOptions{FullRadius: true}},
			{"width", "固定宽度的长标签会省略", control.RoundButtonOptions{FullWidth: theme.T(c).Metrics().Dialog().Width()}},
			{"icon", "图标在文字前", control.RoundButtonOptions{Icon: galleryCheck}},
			{"suffix", "计数", control.RoundButtonOptions{Numbers: control.NumberSuffix{Text: strconv.Itoa(*n), Value: int64(*n)}}},
			{"empty", "", control.RoundButtonOptions{Numbers: control.NumberSuffix{Text: strconv.Itoa(*n), Value: int64(*n)}}},
		} {
			o := v.o
			o.OnActivate = func() { *event = "激活 " + v.id; *n++ }
			control.RoundButton(c.Key(v.id), v.label, o)
		}
		control.LinkButton(c, "增加数字", control.LinkButtonOptions{OnActivate: func() { *n++ }})
		control.LinkButton(c, "减少数字", control.LinkButtonOptions{OnActivate: func() { *n-- }})
		ui.Text(c, *event)
	})
}
func controlsLink(c *ui.Context) {
	event := controlsEvent(c)
	controlsPage(c, func() {
		for _, v := range []struct {
			id, label string
			disabled  bool
		}{{"normal", "悬停出现下划线；Enter / Space 激活", false}, {"disabled", "禁用链接", true}, {"empty", "", false}} {
			control.LinkButton(c.Key(v.id), v.label, control.LinkButtonOptions{Disabled: v.disabled, OnActivate: func() { *event = "链接意图 " + v.id }})
		}
		ui.Box(c).Width(theme.T(c).Metrics().Message().MinWidth()).Children(func() {
			control.LinkButton(c, "长链接在窄宽中单行省略，不改变行高", control.LinkButtonOptions{})
		})
		ui.Text(c, *event)
	})
}
func controlsIcon(c *ui.Context) {
	event := controlsEvent(c)
	controlsPage(c, func() {
		for _, v := range []struct {
			id string
			o  control.IconButtonOptions
		}{{"独立", control.IconButtonOptions{}}, {"返回", control.IconButtonOptions{Kind: control.BackIcon}}, {"关闭", control.IconButtonOptions{Kind: control.CloseIcon}}, {"保持菜单反馈", control.IconButtonOptions{ForceRippled: true}}, {"禁用图标", control.IconButtonOptions{Disabled: true}}} {
			o := v.o
			o.OnActivate = func() { *event = v.id }
			control.IconButton(c.Key(v.id), v.id, galleryCheck, o)
		}
		ui.Text(c, *event)
	})
}
func controlsInput(c *ui.Context) {
	values := ui.Local(c.Root(), "controls-fields", func() [7]string {
		return [7]string{"", "已有正文", "一行\n第二行\n第三行\n第四行\n第五行\n第六行\n第七行\n第八行", "校验由宿主提供", "只读，可以选择复制", "禁用编辑", ""}
	})
	event := controlsEvent(c)
	controlsPage(c, func() {
		for i, label := range []string{"空单行", "普通单行", "自增高多行", "错误字段", "只读字段", "禁用字段", "中文输入法"} {
			control.InputField(c.Key(label), &values[i], label, control.InputOptions{Placeholder: label, Multiline: i == 2 || i == 6, SubmitOnControlEnter: true, Error: i == 3, ReadOnly: i == 4, Disabled: i == 5, OnSubmit: func(v string) { *event = "提交意图：" + v }, OnCancel: func() { *event = "取消意图" }})
		}
		ui.Text(c, *event)
	})
}
func controlsDropdown(c *ui.Context) {
	type model struct {
		states   [5]control.DropdownState
		selected [5]string
		items    []settings.ChoiceItem
	}
	s := ui.Local(c.Root(), "controls-dropdown", func() model {
		return model{items: []settings.ChoiceItem{{ID: "a", Title: "相同名称"}, {ID: "b", Title: "相同名称"}, {ID: "c", Title: "长名称用于窄宽省略和工作区边缘"}, {ID: "blocked", Title: "不可选项", Disabled: true}}}
	})
	controlsPage(c, func() {
		for i, label := range []string{"短原生菜单", "设置行入口", "可搜索长列表", "禁用选择", "空列表"} {
			items := s.items
			if i == 4 {
				items = nil
			}
			mode := control.Popup
			if i == 2 {
				mode = control.SearchBox
			}
			control.Dropdown(c.Key(label), &s.states[i], items, s.selected[i], label, control.DropdownOptions{Placeholder: "请选择", SettingsRow: i == 1, Disabled: i == 3, Mode: mode, Choose: settings.ChooseOptions{SearchLabel: "搜索选项", SearchPlaceholder: "搜索", EmptyLabel: "没有匹配", CloseLabel: "关闭", ClearLabel: "清空"}, OnSelect: func(id string) { s.selected[i] = id }})
		}
		for i, id := range s.selected {
			ui.Text(c, fmt.Sprintf("入口%d 当前ID=%s", i, id))
		}
	})
}
func controlsItems() []control.TabItem {
	return []control.TabItem{{ID: "all", Label: "全部"}, {ID: "media", Label: "媒体"}, {ID: "files", Label: "文件"}, {ID: "blocked", Label: "禁用", Disabled: true}, {ID: "links", Label: "链接"}}
}
func controlsSegments(c *ui.Context) {
	selected := ui.Local(c.Root(), "controls-segments", func() [3]string { return [3]string{"all", "media", "all"} })
	controlsPage(c, func() {
		for i, label := range []string{"普通分段：按下预览、横拖、释放提交", "设置配色与外边距", "禁用分段"} {
			ui.Text(c, label)
			control.SettingsSlider(c.Key(label), controlsItems(), selected[i], control.SegmentsOptions{Settings: i == 1, Disabled: i == 2, OnSelect: func(id string) { selected[i] = id }})
		}
		control.SettingsSlider(c.Key("long"), []control.TabItem{{ID: "a", Label: "很长的标签会保持自然宽度"}, {ID: "b", Label: "窄宽时横向滚动，不互相覆盖"}}, "a", control.SegmentsOptions{})
		control.SettingsSlider(c.Key("empty"), nil, "", control.SegmentsOptions{})
		ui.Text(c, fmt.Sprint(selected))
	})
}
func controlsTabs(c *ui.Context) {
	selected := ui.Local(c.Root(), "controls-tabs", func() [3]string { return [3]string{"all", "media", "all"} })
	event := controlsEvent(c)
	controlsPage(c, func() {
		for i, label := range []string{"下划线标签", "Info 岛状标签", "禁用标签"} {
			ui.Text(c, label)
			variant := control.UnderlineTabs
			if i == 1 {
				variant = control.ProfileTabs
			}
			control.Tabs(c.Key(label), controlsItems(), selected[i], control.TabsOptions{Variant: variant, Disabled: i == 2, OnSelect: func(id string) { selected[i] = id; *event = "选择 " + id }, OnContext: func(id string) { *event = "右键 " + id }})
		}
		control.Tabs(c.Key("overflow"), []control.TabItem{{ID: "a", Label: "超宽岛状标签保持自然字宽"}, {ID: "b", Label: "可以横拖或横向滚轮"}, {ID: "c", Label: "选中项滚到可见区"}}, "c", control.TabsOptions{Variant: control.ProfileTabs})
		control.Tabs(c.Key("empty"), nil, "", control.TabsOptions{})
		ui.Text(c, *event)
	})
}
func controlsCategories(c *ui.Context) {
	selected := ui.Local(c.Root(), "controls-category", func() string { return "general" })
	drafts := ui.Local(c.Root(), "controls-category-drafts", func() map[string]*string {
		return map[string]*string{"general": new(string), "privacy": new(string), "advanced": new(string)}
	})
	m := theme.T(c).Metrics()
	ui.Row(c).Fill().Children(func() {
		control.SideCategories(c, []control.Category{{ID: "general", Label: "常规", Icon: galleryCheck, GroupID: "one", GroupTitle: "偏好"}, {ID: "privacy", Label: "隐私", Icon: galleryCheck, GroupID: "one"}, {ID: "blocked", Label: "禁用类别", Disabled: true, GroupID: "two", GroupTitle: "其他"}, {ID: "advanced", Label: "高级", GroupID: "two"}}, *selected, control.CategoriesOptions{Label: "设置分类", Arrow: true, OnSelect: func(id string) { *selected = id }}).Width(m.Layout().ListMinWidth())
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			control.PageTitle(c, *selected, control.PageTitleOptions{})
			control.InputField(c.Key(*selected), (*drafts)[*selected], "分类草稿", control.InputOptions{Placeholder: "切换类别保留本页草稿"})
		})
	})
}
func controlsTitle(c *ui.Context) {
	hidden := ui.Local(c.Root(), "controls-title-hidden", func() bool { return false })
	event := controlsEvent(c)
	controlsPage(c, func() {
		for _, v := range []struct {
			id, title string
			o         control.PageTitleOptions
		}{{"single", "单标题14 semibold", control.PageTitleOptions{}}, {"double", "双标题13 semibold", control.PageTitleOptions{Subtitle: "副标题13 regular"}}, {"actions", "返回与尾部动作", control.PageTitleOptions{OnBack: func() { *event = "返回" }, BackLabel: "返回", OnClose: func() { *event = "关闭" }, CloseLabel: "关闭", Badge: "状态", Actions: []control.TitleAction{{ID: "one", Label: "动作一", Icon: galleryCheck, OnActivate: func() { *event = "动作一" }}}}}, {"layer", "Layer 标题", control.PageTitleOptions{Layer: true, Subtitle: "56 DIP 基高"}}, {"long", "长标题按真实可用宽省略：标题不会覆盖右侧关闭动作", control.PageTitleOptions{OnClose: func() {}, CloseLabel: "关闭长标题"}}, {"inline", "内嵌标题自然换行", control.PageTitleOptions{Inline: true, Subtitle: "内嵌说明按完整文字排版，宽度变化不会挤进操作区域。"}}} {
			control.PageTitle(c.Key(v.id), v.title, v.o)
		}
		control.LinkButton(c, "切换标题模式显隐", control.LinkButtonOptions{OnActivate: func() { *hidden = !*hidden }})
		control.PageTitle(c.Key("fade"), "模式显隐150ms", control.PageTitleOptions{Hidden: *hidden})
		ui.Text(c, *event)
	})
}
func controlsBadge(c *ui.Context) {
	controlsPage(c, func() {
		for _, v := range []struct {
			label string
			tone  control.BadgeTone
		}{{"普通", control.Neutral}, {"活动", control.Active}, {"成功", control.Success}, {"警告", control.Warning}, {"错误", control.Error}, {"等待", control.Pending}} {
			control.StatusBadge(c.Key(v.label), v.label, control.BadgeOptions{Tone: v.tone})
		}
		control.StatusBadge(c, "很长的标签在给定最大宽度时省略，完整名称仍可读", control.BadgeOptions{MaxWidth: theme.T(c).Metrics().Message().MinWidth()})
		for _, v := range []struct {
			id, label string
			o         control.CountBadgeOptions
		}{{"one", "1", control.CountBadgeOptions{}}, {"many", "12.3K", control.CountBadgeOptions{}}, {"muted", "8", control.CountBadgeOptions{Muted: true}}, {"active", "8", control.CountBadgeOptions{Active: true}}} {
			control.CountBadge(c.Key(v.id), v.label, v.o)
		}
	})
}
func controlsCollapse(c *ui.Context) {
	type model struct {
		shown     [3]bool
		draft     [3]string
		trigger   [3]ui.Handle
		completed int
	}
	s := ui.Local(c.Root(), "controls-collapse", func() model { return model{shown: [3]bool{true, false, false}} })
	controlsPage(c, func() {
		for i, label := range []string{"向下展开", "向上展开", "保留最小预览"} {
			control.LinkButton(c.Key(label), label, control.LinkButtonOptions{OnActivate: func() { s.shown[i] = !s.shown[i] }}).Bind(&s.trigger[i]).Expanded(s.shown[i])
			minimum := float32(0)
			if i == 2 {
				minimum = theme.T(c).Metrics().Button().Height()
			}
			control.Collapse(c.Key(struct{ Body string }{label}), control.CollapseOptions{Expanded: s.shown[i], Up: i == 1, MinimumHeight: minimum, Trigger: &s.trigger[i], OnComplete: func(bool) { s.completed++ }}, func() {
				ui.Text(c, "文字不缩放，只改变占高和裁切。可以在中途反向。")
				control.InputField(c, &s.draft[i], "折叠内草稿"+strconv.Itoa(i), control.InputOptions{Placeholder: "收起后移出焦点，草稿保留"})
			})
		}
		ui.Text(c, fmt.Sprintf("完成回调 %d", s.completed))
	})
}
func controlsApproval(c *ui.Context) {
	type model struct {
		expanded [6]bool
		phase    control.ApprovalStatus
		event    string
	}
	s := ui.Local(c.Root(), "controls-approval", func() model { return model{} })
	controlsPage(c, func() {
		for i, label := range []string{"等待审批", "提交中", "失败可重试", "已经处理", "已经过期", "交互审批"} {
			status := control.ApprovalStatus(i)
			if i == 5 {
				status = s.phase
			}
			data := control.Approval{ID: strconv.Itoa(i), Title: label, Summary: "宿主提供准备好的操作说明。组件只报告意图，不执行操作。", Status: status, Expanded: s.expanded[i]}
			if status == control.Submitting {
				data.StatusText = "正在处理…"
			}
			if status == control.ApprovalFailed {
				data.Error = "宿主报告失败，可明确重试"
			}
			if status == control.Resolved {
				data.StatusText = "已经由宿主确认"
			}
			if status == control.Expired {
				data.StatusText = "请求已失效"
			}
			control.ApprovalCard(c.Key(label), data, control.ApprovalOptions{DetailsLabel: "查看详情", AllowLabel: "允许一次", RejectLabel: "拒绝", CanRetry: true, OnExpand: func(v bool) { s.expanded[i] = v }, OnAction: func(id string, action control.ApprovalAction) {
				s.event = fmt.Sprintf("审批 %s 动作 %d", id, action)
				if i == 5 {
					s.phase = control.Submitting
				}
			}}, func() { ui.Text(c, "编造的详情，可随父宽自然测高；安全说明不被强制截断。") })
		}
		control.LinkButton(c, "模拟交互审批失败", control.LinkButtonOptions{OnActivate: func() { s.phase = control.ApprovalFailed }})
		control.LinkButton(c, "模拟交互审批成功", control.LinkButtonOptions{OnActivate: func() { s.phase = control.Resolved }})
		ui.Text(c, s.event)
	})
}
func controlsForward(c *ui.Context) {
	event := controlsEvent(c)
	controlsPage(c, func() {
		for i, label := range []string{"可打开摘要", "只读摘要", "加载中保留旧节点", "失败可重试", "禁用摘要", "出站选中摘要"} {
			status := content.SummaryReady
			if i == 2 {
				status = content.SummaryLoading
			}
			if i == 3 {
				status = content.SummaryFailed
			}
			data := content.ForwardSummaryData{ID: strconv.Itoa(i), Title: label, CountLabel: "共 24 条", Nodes: []content.SummaryNode{{ID: "one", Text: "第一份摘要，可以自然换行占用总行预算。"}, {ID: "two", Text: "第二份摘要"}, {ID: "three", Text: "第三份摘要"}, {ID: "four", Text: "超出总预算的内容将省略"}}, Status: status, StatusText: "加载中…", Error: "加载失败"}
			o := content.ForwardSummaryOptions{Appearance: content.Appearance{Disabled: i == 4, Outgoing: i == 5, Selected: i == 5}, RetryLabel: "重试摘要", CanRetry: true, OnRetry: func(id string) { *event = "重试 " + id }}
			if i != 1 {
				o.OnOpen = func(id string) { *event = "打开 " + id }
			}
			content.ForwardSummary(c.Key(label), data, o)
		}
		ui.Text(c, *event)
	})
}
func controlsStop(c *ui.Context) {
	type model struct {
		draft string
		run   compose.Generation
		event string
	}
	s := ui.Local(c.Root(), "controls-stop", func() model {
		return model{draft: "生成期间保留的草稿", run: compose.Generation{RunID: "invented-run", Phase: compose.Idle}}
	})
	controlsPage(c, func() {
		compose.Editor(c, &s.draft, compose.EditorOptions{Generation: &s.run, OnSubmit: func(string) { s.run.Phase = compose.Generating; s.event = "生成中；草稿由宿主保留" }, OnStop: func(id string) { s.run.Phase = compose.Stopping; s.event = "停止请求 " + id }})
		for _, v := range []struct {
			label string
			phase compose.GenerationPhase
		}{{"模拟开始生成", compose.Generating}, {"模拟停止失败", compose.StopFailed}, {"模拟停止确认", compose.Stopped}, {"模拟自然完成", compose.Completed}} {
			control.LinkButton(c.Key(v.label), v.label, control.LinkButtonOptions{OnActivate: func() { s.run.Phase = v.phase }})
		}
		for _, v := range []struct {
			label string
			phase compose.GenerationPhase
		}{{"生成中", compose.Generating}, {"正在停止", compose.Stopping}, {"停止失败后仍生成", compose.StopFailed}, {"已经停止", compose.Stopped}, {"自然完成", compose.Completed}} {
			ui.Row(c.Key(v.label)).Children(func() {
				ui.Text(c, v.label)
				compose.StopGeneration(c, compose.Generation{RunID: v.label, Phase: v.phase}, compose.StopOptions{Label: v.label, OnStop: func(id string) { s.event = id }})
			})
		}
		ui.Text(c, s.event)
	})
}
func controlsSound(c *ui.Context) {
	type preparedCue struct{ ID string }
	type model struct {
		enabled, muted, os bool
		volume             string
		calls              int
		event              string
		resource           *preparedCue
	}
	s := ui.Local(c.Root(), "controls-sound", func() model {
		return model{enabled: true, volume: "75", resource: &preparedCue{ID: "host-prepared-demo"}}
	})
	controlsPage(c, func() {
		settings.ToggleRow(c, "全局声音", s.enabled, settings.ToggleOptions{OnChange: func(v bool) { s.enabled = v }})
		settings.ToggleRow(c, "会话静音", s.muted, settings.ToggleOptions{OnChange: func(v bool) { s.muted = v }})
		settings.ToggleRow(c, "OS 拥有通知声音", s.os, settings.ToggleOptions{OnChange: func(v bool) { s.os = v }})
		control.InputField(c, &s.volume, "最终音量百分比", control.InputOptions{Placeholder: "0…100，0为静音"})
		for _, preview := range []bool{false, true} {
			label := "显式通知事件"
			if preview {
				label = "显式试听事件"
			}
			control.RoundButton(c.Key(label), label, control.RoundButtonOptions{OnActivate: func() {
				volume, err := strconv.ParseFloat(s.volume, 32)
				if err != nil {
					s.event = "音量必须是数字"
					return
				}
				percent := float32(volume)
				s.calls++
				sent := sound.Dispatch(sound.Request{EventID: strconv.Itoa(s.calls), ScopeID: "demo", CueID: "host-cue", Resource: s.resource}, sound.Policy{Enabled: s.enabled, Muted: s.muted, Eligible: true, OSOwnsSound: s.os, VolumePercent: &percent}, sound.Options{Preview: preview, OnPlay: func(r sound.Request) {
					s.event = fmt.Sprintf("播放意图 event=%s gain=%.2f resource=%s", r.EventID, r.Gain, r.Resource.(*preparedCue).ID)
				}})
				if !sent {
					s.event = "本次资格未产生播放意图"
				}
			}})
		}
		ui.Text(c, s.event)
		ui.Text(c, "此页展示宿主回调；声音资源准备、设备输出与播放失败属于宿主。控件点击本身没有默认提示音。")
	})
}
