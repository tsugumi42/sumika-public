package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/winshell"
)

// 链接解析设置里的“网站与页面类型”（规格-链接页面类型和样式）：
// 一张网站列表，每站一个开关；点开看它的页面类型，每种一行：状态（能展开 / 只能取数 / 不支持）和单独的开关，
// 不支持的也列出来写明原因。能展开的页面类型再点开，是它的展开面板：样式（卡片 / 图文 / 混合）、合并转发、
// 通配符模板编辑（占位符、预设、实时预览、出错提示写在框下面）。只在新外壳里用，控件都来自 winshell。

func kindID(platform, kind string) string { return platform + "/" + kind }

// kindSwitchOn 是这一种页面类型自己的开关（不看整站的）：没写就是开。
func kindSwitchOn(e config.LinkExpand, platform, kind string) bool {
	v, set := e.Platforms[platform].Kinds[kind]
	return !set || v
}

func platformSummary(p linktemplate.CatalogPlatform) string {
	expand, fetch := 0, 0
	for _, k := range p.Kinds {
		switch k.Support {
		case linktemplate.SupportExpand:
			expand++
		case linktemplate.SupportFetch:
			fetch++
		}
	}
	switch {
	case expand == len(p.Kinds):
		return fmt.Sprintf("%d 种页面，都能展开", len(p.Kinds))
	case expand > 0:
		return fmt.Sprintf("%d 种页面，%d 种能展开", len(p.Kinds), expand)
	case fetch > 0:
		return fmt.Sprintf("%d 种页面，只能取数", len(p.Kinds))
	}
	return fmt.Sprintf("%d 种页面，都不支持", len(p.Kinds))
}

func (a *app) linkPlatformsPage(c *ui.Context, k *winshell.Kit, e config.LinkExpand) {
	s := &a.linkSettings
	if s.platOpen == nil {
		s.platOpen = map[string]bool{}
	}
	a.modelNote(c, "关掉的网站或页面类型不会自动展开，不影响给她看。", false)
	for _, p := range linktemplate.Catalog() {
		open := s.platOpen[p.ID]
		on := e.PlatformEnabled(p.ID)
		k.DisclosureRow(c, "plat-"+p.ID, p.Label, platformSummary(p), open, false, func() { s.platOpen[p.ID] = !open }, func() {
			k.SwitchValue(c, "plat-switch-"+p.ID, p.Label+"整站展开", on, s.busy, func(v bool) {
				a.linkJob(&api.LinkSettingsChange{Key: config.LinkPlatformEnabled, Site: p.ID, Bool: v})
			})
		})
		k.Disclose(c, "plat-body-"+p.ID, open, k.G.PaneInset, func() { // 展开收起：高度弹簧加淡变（设置页内容列的间距）
			a.linkImageModeRow(c, e, p)
			a.linkTranslateRow(c, k, e, p)
			for _, kind := range p.Kinds {
				a.linkKindRow(c, k, e, p, kind, on)
			}
		})
	}
}

// linkImageModeRow 是网站的发图方式（ADR-333）：普通优先 / 转图优先 / 合并转发转图优先。
// 对容易被 QQ 卡图的网站，选后两种，第一次就发转过的图，不先让被卡的原图失败一次。
func (a *app) linkImageModeRow(c *ui.Context, e config.LinkExpand, p linktemplate.CatalogPlatform) {
	s := &a.linkSettings
	expandable := slices.ContainsFunc(p.Kinds, func(k linktemplate.CatalogKind) bool { return k.Support == linktemplate.SupportExpand })
	if !expandable {
		return
	}
	modes := []string{config.ImageModeAuto, config.ImageModeRotate, config.ImageModeForward}
	a.settingsOneOf(c, "img-"+p.ID, "发图方式", []string{"普通优先", "转图优先", "合并转发转图优先"}, nil, slices.Index(modes, e.ImageModeOf(p.ID)), s.busy, func(i int) {
		a.linkJob(&api.LinkSettingsChange{Key: config.LinkPlatformImage, Site: p.ID, Text: modes[i]})
	})
	a.modelNote(c, imageModeHint(e.ImageModeOf(p.ID)), false)
}

// linkTranslateRow 是接了 $译文 的网站的翻译开关；总开关关着时置灰并说明，不显示成“开”。
func (a *app) linkTranslateRow(c *ui.Context, k *winshell.Kit, e config.LinkExpand, p linktemplate.CatalogPlatform) {
	if !linktemplate.PlatformTranslatable(p) {
		return
	}
	s := &a.linkSettings
	k.Row(c, "translate-"+p.ID, "翻译外文正文", "", func() {
		k.SwitchValue(c, "translate-switch-"+p.ID, p.Label+"翻译", e.TranslateOn(p.ID), s.busy || !e.Translate.Enabled, func(v bool) {
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkPlatformTranslate, Site: p.ID, Bool: v})
		})
	})
	if !e.Translate.Enabled {
		a.modelNote(c, "翻译总开关关着。", false)
	}
}

func imageModeHint(mode string) string {
	switch mode {
	case config.ImageModeRotate:
		return "图先转 180° 直接发；不成再试合并转发里转图，都不行才发文字。动图、视频不转。"
	case config.ImageModeForward:
		return "图先放进合并转发并转 180° 再发；不成才发文字。动图、视频不转。"
	}
	return "图先照原样发；被拒或查实没发出去，再依次试转 180° 直接发、合并转发里转图，最后才发文字。"
}

func (a *app) linkKindRow(c *ui.Context, k *winshell.Kit, e config.LinkExpand, p linktemplate.CatalogPlatform, kind linktemplate.CatalogKind, platformOn bool) {
	s := &a.linkSettings
	id := kindID(p.ID, kind.ID)
	expandable := kind.Support == linktemplate.SupportExpand && kind.Site != ""
	summary := kind.Note
	tone := winshell.BadgeNeutral
	if expandable {
		summary, tone = "", winshell.BadgeGood
	}
	control := func() {
		ui.Row(c.Key("ctl-" + id)).Gap(k.G.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
			k.Badge(c, "state-"+id, kind.Support.Label(), tone)
			if expandable {
				k.SwitchValue(c, "kind-switch-"+id, kind.Label+"展开", kindSwitchOn(e, p.ID, kind.ID) && platformOn, s.busy || !platformOn, func(v bool) {
					a.linkJob(&api.LinkSettingsChange{Key: config.LinkKindEnabled, Site: p.ID, Text: kind.ID, Bool: v})
				})
			}
		})
	}
	if !expandable {
		k.DisclosureRow(c, "kind-"+id, kind.Label, summary, false, true, nil, control)
		return
	}
	open := s.kindOpen == id
	k.DisclosureRow(c, "kind-"+id, kind.Label, summary, open, true, func() {
		if open {
			s.kindOpen = ""
			return
		}
		s.kindOpen = id
		s.styleAsk = ""
		s.tplForward, s.tplOpen = false, false
		a.linkLoadTemplate(e, kind.Site)
	}, control)
	k.Disclose(c, "kind-body-"+id, open, k.G.PaneInset, func() {
		site, _, _ := linktemplate.SiteByKey(kind.Site)
		a.linkKindPanel(c, k, e, site)
	})
}

// linkLoadTemplate 把这个站点现在生效的模板放进编辑框（没写过的放内置默认原文，方便在它上面改）。
func (a *app) linkLoadTemplate(e config.LinkExpand, siteKey string) {
	s := &a.linkSettings
	site, _, _ := linktemplate.SiteByKey(siteKey)
	key := siteKey
	if s.tplForward && site.HasForward() {
		key += "-forward"
	}
	s.tplKey, s.tpl = key, e.Templates[key]
	if strings.TrimSpace(s.tpl) == "" {
		s.tpl = site.Source(s.tplForward && site.HasForward())
	}
}

func (a *app) linkKindPanel(c *ui.Context, k *winshell.Kit, e config.LinkExpand, site linktemplate.Site) {
	s := &a.linkSettings
	g := k.G
	ui.Column(c.Key("panel-"+site.Key)).FillWidth().Gap(g.SpaceSmall).Padding(g.SpaceTight, 0, g.SpaceSmall, 2*(g.IconSmall+g.SpaceSmall)).Children(func() {
		// 样式：选一种就是写入 @预设名和合并转发模式（高级用户下面仍可手写通配符）
		styles := site.Styles()
		cfg := e.Sites[site.Key]
		cur := site.CurrentStyle(e.Templates[site.Key], cfg.Forward)
		sel, labels := -1, make([]string, len(styles), len(styles)+1)
		for i, st := range styles {
			labels[i] = st.Label
			if st.ID == cur {
				sel = i
			}
		}
		if sel < 0 {
			// 手写过的模板不属于任何样式：多放一格“自定义”表示现在的状态。分段控件会把越界的选中项夹到第一格，
			// 那会被当成点了“默认”，一打开面板就把自定义模板冲掉。
			labels = append(labels, "自定义")
		}
		k.Row(c, "style-row-"+site.Key, "发出去的样式", "", func() {
			v := sel
			if sel < 0 {
				v = len(styles)
			}
			if k.Segment(c, "style-"+site.Key, &v, labels) && v >= 0 && v < len(styles) && !s.busy {
				ask := site.Key + "/" + styles[v].ID
				if sel < 0 && s.styleAsk != ask {
					s.styleAsk = ask // 现在是自定义模板：第一次点只提醒，再点一次才替换
					return
				}
				s.styleAsk = ""
				a.linkJob(&api.LinkSettingsChange{Key: config.LinkSiteStyle, Site: site.Key, Text: styles[v].ID})
			}
		})
		hint := "现在是自定义模板"
		if sel >= 0 {
			hint = styles[sel].Hint
		} else if strings.HasPrefix(s.styleAsk, site.Key+"/") {
			hint = "会替换你的自定义模板，再点一次确认"
		}
		a.modelNote(c, hint, false)
		a.linkForwardModeRow(c, e, site)
		if site.HasForward() {
			a.linkForwardLimitRow(c, e, site)
		}
		if hasDescription(site) {
			a.linkShowDescRow(c, e, site)
			limit := site.DescLimit
			if v := cfg.DescLimit; v != nil {
				limit = *v
			}
			a.linkSiteNumber(c, site, "简介最多多少字（0 不截断）", config.LinkSiteDescLimit, limit)
		}
		a.linkTemplateEditor(c, k, e, site)
	})
}

// linkTemplateEditor 是展开面板里的通配符编辑：文本框、错误写在框下面、插入占位符、套用预设、实时预览。
func (a *app) linkTemplateEditor(c *ui.Context, k *winshell.Kit, e config.LinkExpand, site linktemplate.Site) {
	s := &a.linkSettings
	if s.tplKey != site.Key && s.tplKey != site.Key+"-forward" {
		a.linkLoadTemplate(e, site.Key) // 面板是别处打开的，缓冲区还是上一个站点的
	}
	k.DisclosureRow(c, "tpl-adv-"+site.Key, "通配符模板", "高级：自己排版，改完实时预览", s.tplOpen, false, func() { s.tplOpen = !s.tplOpen }, nil)
	// 展开收起：高度弹簧加淡变；外面是 linkKindPanel 那一列，间距 SpaceSmall
	k.Disclose(c, "tpl-adv-body-"+site.Key, s.tplOpen, k.G.SpaceSmall, func() { a.linkTemplateBody(c, k, e, site) })
}

// linkTemplateBody 是“通配符模板”展开后的内容：选哪份模板、编辑框、插入、套用、预览、保存。
func (a *app) linkTemplateBody(c *ui.Context, k *winshell.Kit, e config.LinkExpand, site linktemplate.Site) {
	s := &a.linkSettings
	if site.HasForward() {
		which := 0
		if s.tplForward {
			which = 1
		}
		if k.Segment(c, "tpl-which-"+site.Key, &which, []string{"消息模板", "合并转发里的模板"}) {
			s.tplForward = which == 1
			a.linkLoadTemplate(e, site.Key)
		}
	}
	forward := strings.HasSuffix(s.tplKey, "-forward")
	a.settingsTextArea(c, "link-template-text-"+s.tplKey, &s.tpl, "消息模板", 4, 9)
	_, perr := site.Parse(s.tpl)
	if perr != nil {
		a.modelNote(c, perr.Error(), true)
	}
	a.settingsHeading(c, "插入")
	ui.Row(c.Key("ins-" + site.Key)).Wrap().Gap(6).Children(func() {
		for _, f := range site.Set.Fields() {
			name := f.Names[0]
			a.settingsButton(c, "ins-"+site.Key+"-"+f.ID, "$"+name, false, false, func() { s.tpl += "$" + name })
		}
	})
	if len(site.Presets) > 0 {
		a.settingsHeading(c, "套用内置模板")
		ui.Row(c.Key("presets-" + site.Key)).Wrap().Gap(6).Children(func() {
			for _, p := range site.Presets {
				a.settingsButton(c, "preset-"+site.Key+"-"+p.Name, "@"+p.Name, false, false, func() { s.tpl = "@" + p.Name })
			}
		})
	}
	a.settingsHeading(c, "预览（示例数据）")
	if perr == nil {
		if text, err := site.Preview(s.tpl); err == nil {
			ui.Text(c.Key("preview-"+site.Key), text).Wrap().Selectable()
		}
	}
	a.modelNote(c, "图片类写成 [封面] 之类的方块；$分段 另起一条消息，$合并转发 之后进合并转发，$节点 换下一个节点。", false)
	ui.Row(c.Key("tpl-actions-" + site.Key)).Gap(8).Children(func() {
		a.settingsButton(c, "tpl-save-"+site.Key, "保存模板", true, s.busy || perr != nil, func() {
			text := s.tpl
			// 内容和内置默认一样就不写进配置，免得以后内置默认改进了也用不上
			if strings.TrimSpace(text) == strings.TrimSpace(site.Source(forward)) {
				text = ""
			}
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkTemplate, Site: s.tplKey, Text: text})
		})
		a.settingsButton(c, "tpl-reset-"+site.Key, "恢复内置默认", false, s.busy, func() {
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkTemplate, Site: s.tplKey, Text: ""})
			s.tpl = site.Source(forward)
		})
	})
}
