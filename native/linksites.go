package main

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/winshell"
)

// 链接解析设置里的“消息格式”：每个站点一份模板（规格-链接解析模板第十节）。
// 一个文本框、一排插入占位符的按钮、一排套用内置模板的按钮、用示例数据实时渲染的预览；
// 解析错误显示在文本框下面，有错时不能保存。点按钮把 $名字 追加到末尾（要插到中间请直接手写）。

const defaultSite = "bilibili-video"

func (s *linkPageState) currentSite() linktemplate.Site {
	key := s.site
	if key == "" {
		key = defaultSite
	}
	site, _, _ := linktemplate.SiteByKey(key)
	return site
}

// templateSummary 说这份模板现在用的是什么：内置默认、套用的内置预设，还是自己写的。
func templateSummary(raw string) string {
	switch t := strings.TrimSpace(raw); {
	case t == "":
		return "内置默认"
	case strings.HasPrefix(t, "@"):
		return "内置 " + t
	}
	return "已自定义"
}

func (a *app) linkSitePage(c *ui.Context, e config.LinkExpand) {
	s := &a.linkSettings
	site := s.currentSite()
	if k := a.settingsKit(); k != nil {
		var items []winshell.MenuItem
		for _, one := range linktemplate.Sites() {
			items = append(items, winshell.MenuItem{Label: one.Label, Checked: one.Key == site.Key, Action: func() { s.site = one.Key }})
		}
		k.Row(c, "link-site", "站点", "", func() {
			k.Picker(c, "link-site-picker", "站点", site.Label, &s.siteOpen, &s.siteQuery, "搜索站点", items)
		})
	} else {
		a.settingsRow(c, "站点", settings.ButtonOptions{Value: site.Label, Arrow: true, Disabled: s.busy, OnActivate: func() { s.choosing = "site"; s.chooser.Show() }})
	}
	a.linkTemplateRow(c, e, site, false)
	if site.HasForward() {
		a.linkTemplateRow(c, e, site, true)
	}
	a.linkForwardModeRow(c, e, site)
	if site.HasForward() {
		a.linkForwardLimitRow(c, e, site)
	}
	if hasDescription(site) {
		a.linkShowDescRow(c, e, site)
		limit := site.DescLimit
		if v := e.Sites[site.Key].DescLimit; v != nil {
			limit = *v
		}
		a.linkSiteNumber(c, site, "简介最多多少字（0 不截断）", config.LinkSiteDescLimit, limit)
	}
}

// linkShowDescRow 是“显示简介”开关（R-219）：只有内置默认去掉过简介的站点有；开了以后内置默认模板多一行简介，按下面的字数截断。
func (a *app) linkShowDescRow(c *ui.Context, e config.LinkExpand, site linktemplate.Site) {
	if !site.HasDescOption() {
		return
	}
	a.settingsToggle(c, "show-desc-"+site.Key, "显示简介", e.Sites[site.Key].ShowDesc, a.linkSettings.busy, func(v bool) {
		a.linkJob(&api.LinkSettingsChange{Key: config.LinkSiteShowDesc, Site: site.Key, Bool: v})
	})
}

// neverForwardHint 是“合并转发：从不”旁边的说明：这个选项会让多图被截断（用户 2026-11-13，R-217）。
const neverForwardHint = "选“从不”时，图多于直发图数上限就只发前几张，文字末尾标出发了几张、共几张，不拆成多条。"

// linkForwardModeRow 是站点的“合并转发”几选一：有转发模板的站点自动、总是、从不；没有的（Bluesky 等）只有自动、从不（R-218）。
func (a *app) linkForwardModeRow(c *ui.Context, e config.LinkExpand, site linktemplate.Site) {
	s := &a.linkSettings
	mode := e.Sites[site.Key].Forward
	if mode == "" {
		mode = linktemplate.ForwardAuto
	}
	modes := []string{linktemplate.ForwardAuto, linktemplate.ForwardAlways, linktemplate.ForwardNever}
	labels := []string{"按规则自动", "总是", "从不"}
	legacy := []string{"合并转发：按规则自动", "合并转发：总是", "合并转发：从不（图多于上限时只发前几张）"}
	if !site.HasForward() {
		modes, labels, legacy = []string{modes[0], modes[2]}, []string{labels[0], labels[2]}, []string{legacy[0], legacy[2]}
	}
	a.settingsOneOfHint(c, "fwd-"+site.Key, "合并转发", neverForwardHint, labels, legacy, slices.Index(modes, mode), s.busy, func(i int) {
		a.linkJob(&api.LinkSettingsChange{Key: config.LinkSiteForward, Site: site.Key, Text: modes[i]})
	})
}

func hasDescription(site linktemplate.Site) bool {
	for _, f := range site.Set.Fields() {
		if f.Kind == linktemplate.KindText && (f.ID == "desc" || f.ID == "text") && f.Names[0] == "简介" {
			return true
		}
	}
	return false
}

func (a *app) linkSiteNumber(c *ui.Context, site linktemplate.Site, title string, key config.LinkKey, value int) {
	s := &a.linkSettings
	a.settingsRow(c.Key(string(key)+site.Key), title, settings.ButtonOptions{Value: strconv.Itoa(value), Arrow: true, Disabled: s.busy, OnActivate: func() {
		s.editKey, s.editTitle, s.editSite = key, title, site.Key
		s.number.Text = strconv.Itoa(value)
		s.numeric = true
		s.editError = ""
		s.editBox.Open = true
	}})
}

// forwardBytesTitle 是 x、telegram 的“超过多少字节折成合并转发”一行：这两个站自 ADR-391 起按实发字节判断，
// 不再显示已失效的 forwardOver 字数。三个英文字节左右等于一个汉字，标签里写清楚。
const forwardBytesTitle = "超过多少字节折成合并转发（约三个字节一个汉字）"

// forwardBytesHint 是这一行下面的常驻说明（设计语言第十一节：常驻最多一行）。
const forwardBytesHint = "按实际会发出去的文字算，链接、作者、时间也算；到点就折成合并转发。"

// linkForwardLimitRow 按站点的判断方式放“超过多少字/字节折成合并转发”：x、telegram 编辑 forwardOverBytes，
// 其余站（B 站等）照旧编辑 forwardOver。
func (a *app) linkForwardLimitRow(c *ui.Context, e config.LinkExpand, site linktemplate.Site) {
	if site.ForwardOverBytes > 0 {
		v := site.ForwardOverBytes
		if got := e.Sites[site.Key].ForwardOverBytes; got != nil {
			v = *got
		}
		a.linkSiteNumber(c, site, forwardBytesTitle, config.LinkSiteForwardBytes, v)
		a.modelNote(c, forwardBytesHint, false)
		return
	}
	over := site.ForwardOver
	if v := e.Sites[site.Key].ForwardOver; v != nil {
		over = *v
	}
	a.linkSiteNumber(c, site, "超过多少字折成合并转发（0 不按字数）", config.LinkSiteForwardOver, over)
}

func (a *app) linkTemplateRow(c *ui.Context, e config.LinkExpand, site linktemplate.Site, forward bool) {
	s := &a.linkSettings
	key, title := site.Key, "消息模板"
	if forward {
		key, title = site.Key+"-forward", "合并转发里的模板"
	}
	a.settingsRow(c.Key("tpl-"+key), title, settings.ButtonOptions{Value: templateSummary(e.Templates[key]), Arrow: true, Disabled: s.busy, OnActivate: func() {
		s.tplKey, s.tpl = key, e.Templates[key]
		if strings.TrimSpace(s.tpl) == "" {
			s.tpl = site.Source(forward) // 没写过：把内置默认原文放进文本框，方便在它上面改
		}
		s.tplBox.Open = true
	}})
}

func (a *app) linkSiteDialogs(c *ui.Context) {
	s := &a.linkSettings
	var items []settings.ChoiceItem
	for _, site := range linktemplate.Sites() {
		items = append(items, settings.ChoiceItem{ID: site.Key, Title: site.Label})
	}
	settings.ChooseBox(c.Key("link-site-choice"), &s.chooser, items, s.currentSite().Key, "选择站点", settings.ChooseOptions{SearchLabel: "搜索站点", SearchPlaceholder: "搜索名称", ClearLabel: "清空搜索", EmptyLabel: "没有匹配的站点", CloseLabel: "取消", OnSelect: func(id string) {
		s.chooser.Open = false
		s.site = id
	}})
	site, forward, ok := linktemplate.SiteByKey(s.tplKey)
	if !ok {
		site, forward = s.currentSite(), false
	}
	title := "编辑模板：" + site.Label
	if forward {
		title += "（合并转发）"
	}
	_, perr := site.Parse(s.tpl)
	a.settingsBox(c, "link-template-editor", &s.tplBox, title, overlay.BoxOptions{Wide: true, Actions: []overlay.BoxAction{
		{ID: "save", Label: "保存", Default: true, Disabled: s.busy || perr != nil},
		{ID: "reset", Label: "恢复内置默认", Disabled: s.busy},
		{ID: "cancel", Label: "取消"},
	}, OnAction: func(id string) {
		switch id {
		case "save":
			// 内容和内置默认一样就不写进配置，免得以后内置默认改进了也用不上
			text := s.tpl
			if strings.TrimSpace(text) == strings.TrimSpace(site.Source(forward)) {
				text = ""
			}
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkTemplate, Site: s.tplKey, Text: text})
		case "reset":
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkTemplate, Site: s.tplKey, Text: ""})
		}
	}}, func() {
		a.settingsTextArea(c, "link-template-text", &s.tpl, "消息模板", 4, 9)
		if perr != nil {
			a.modelNote(c, perr.Error(), true)
		}
		a.settingsHeading(c, "插入")
		ui.Row(c).Wrap().Gap(6).Children(func() {
			for _, f := range site.Set.Fields() {
				name := f.Names[0]
				a.settingsButton(c, "ins-"+f.ID, "$"+name, false, false, func() { s.tpl += "$" + name })
			}
		})
		if len(site.Presets) > 0 {
			a.settingsHeading(c, "套用内置模板")
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, p := range site.Presets {
					a.settingsButton(c, "preset-"+p.Name, "@"+p.Name, false, false, func() { s.tpl = "@" + p.Name })
				}
			})
		}
		a.settingsHeading(c, "预览（示例数据）")
		if perr == nil {
			if text, err := site.Preview(s.tpl); err == nil {
				ui.Text(c, text)
			}
		}
		a.modelNote(c, "图片类写成 [封面] 之类的方块；$分段 另起一条消息，$合并转发 之后进合并转发，$节点 换下一个节点。", false)
	})
}
