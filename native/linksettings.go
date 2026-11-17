package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/loadui"
)

type linkRow struct {
	index  int
	header string
}
type linkPageState struct {
	writer                          api.LinkSettingsWriter
	view                            api.LinkSettingsView
	entered, loaded, busy, expanded bool
	load                            loadstate.Loader // 读取、保存的提示什么时候出现（延迟出现、出现后留够；见 loadstate）
	request                         uint64
	note                            string
	chooser                         settings.ChooseState
	siteOpen                        bool   // 新界面的站点下拉
	siteQuery                       string // 下拉里的搜索字
	choosing                        string
	listBox, confirmBox, editBox    overlay.BoxState
	list                            ui.ListState
	highlight                       int
	query, filteredQuery            string
	dirty                           bool
	rows                            []linkRow
	editKey                         config.LinkKey
	editTitle, text, editError      string
	number                          settings.NumberState
	numeric                         bool
	// 模板编辑：当前站点、正在编辑的模板键（站点键或 站点键-forward）、文本、框
	site, editSite, tplKey, tpl string
	tplBox                      overlay.BoxState
	// 新界面的“网站与页面类型”：展开的网站、展开的页面类型（同一时间一个，共用一个模板缓冲区）、编辑的是不是合并转发里的模板
	platOpen   map[string]bool
	kindOpen   string
	tplForward bool
	free       freeAPIPageState // “免费接口”一组（freeapisettings.go）
	fwd        linkForwardState // “合并转发”分组：节点名和卡片文字的草稿
	tplOpen    bool             // 展开面板里的“通配符模板”折叠区
	styleAsk   string           // 当前是自定义模板时，点了哪个样式（站点键/样式 ID），等第二次点击确认
}

func (a *app) linkJob(change *api.LinkSettingsChange) {
	s := &a.linkSettings
	if s.busy {
		return
	}
	s.busy = true
	s.load.Begin()
	s.request++
	request, generation, writer, version := s.request, a.srcGen, s.writer, s.view.Version
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var v api.LinkSettingsView
		var err error
		if change == nil {
			v, err = writer.LinkSettings(ctx)
		} else {
			v, err = writer.SetLinkSettings(ctx, *change, version)
		}
		a.post(func() {
			if a.srcGen != generation || a.linkSettings.request != request {
				return
			}
			s := &a.linkSettings
			s.busy = false
			if err == nil || errors.Is(err, config.ErrConfigVersion) {
				s.accept(v)
			}
			if err == nil && change != nil && change.Key == config.LinkSiteStyle {
				a.linkLoadTemplate(v.Configured.Expand, change.Site) // 选了样式，模板变了，编辑框跟着换，免得保存时把样式冲掉
			}
			if s.loaded {
				s.load.Succeed()
			} else {
				s.load.Fail()
			}
			if err != nil {
				s.note = err.Error()
			} else {
				s.note = ""
			}
		})
	}()
}
func (s *linkPageState) accept(v api.LinkSettingsView) {
	s.view = v
	s.loaded = true
	s.dirty = true
}
func (a *app) enterLinks() {
	s := &a.linkSettings
	if s.entered {
		return
	}
	s.entered = true
	if s.writer == nil && a.demoMode {
		s.writer = newDemoLinkSettings()
	}
	if s.writer == nil {
		s.writer, _ = a.reader.(api.LinkSettingsWriter)
	}
	if s.writer == nil {
		s.note = "当前后台没有提供插件设置"
		return
	}
	a.linkJob(nil)
}
func (s *linkPageState) leave() {
	s.entered = false
	s.load.Cancel()
	s.chooser.Open = false
	s.listBox.Open = false
	s.confirmBox.Open = false
	s.editBox.Open = false
	s.tplBox.Open = false
	s.free.leave()
}
func (a *app) linkToggle(c *ui.Context, title string, key config.LinkKey, value bool) {
	s := &a.linkSettings
	a.settingsToggle(c, key, title, value, s.busy, func(v bool) { a.linkJob(&api.LinkSettingsChange{Key: key, Bool: v}) })
}
func (a *app) linkEdit(c *ui.Context, title string, key config.LinkKey, value string, numeric bool) {
	a.linkEditShown(c, title, key, value, value, numeric)
}

// linkEditShown 的 shown 是行上显示的字，value 是点开后输入框里的初值（留空时行上写“跟后台模型”这类说明，输入框仍是空的）
func (a *app) linkEditShown(c *ui.Context, title string, key config.LinkKey, value, shown string, numeric bool) {
	s := &a.linkSettings
	a.settingsRow(c.Key(key), title, settings.ButtonOptions{Value: shown, Arrow: true, Disabled: s.busy, OnActivate: func() {
		s.editKey, s.editTitle, s.editSite = key, title, ""
		s.text = value
		s.number.Text = value
		s.numeric = numeric
		s.editError = ""
		s.editBox.Open = true
	}})
}
func (a *app) linkEditSave() {
	s := &a.linkSettings
	ch := api.LinkSettingsChange{Key: s.editKey, Text: s.text}
	ch.Site = s.editSite
	if s.numeric {
		n, e := strconv.Atoi(s.number.Text)
		if e != nil || n < 0 || (n < 1 && s.editSite == "") {
			s.editError = "请输入范围内的整数"
			return
		}
		ch.Int = n
	}
	s.editBox.Open = false
	a.linkJob(&ch)
}

// pageLinkSettings 是“插件”分类的内容：链接解析加日历和天气
func (a *app) pageLinkSettings(c *ui.Context) {
	ui.Column(c.Key("plugin-settings")).FillWidth().MinWidth(0).Gap(0).Children(func() {
		a.pageLinkSettingsBody(c)
		a.pageLifeSettings(c)
	})
}
func (a *app) pageLinkSettingsBody(c *ui.Context) {
	a.enterLinks()
	s := &a.linkSettings
	lv := s.load.View(c.Now())
	loadui.Wake(c, lv)
	ui.Column(c).FillWidth().MinWidth(0).Gap(0).Children(func() {
		a.settingsHeading(c, "内置插件")
		a.pageFreeAPI(c)
		a.settingsRow(c, "链接解析（link-reader）", settings.ButtonOptions{Value: func() string {
			if s.expanded {
				return "收起"
			}
			return "设置"
		}(), Arrow: true, OnActivate: func() { s.expanded = !s.expanded }})
		for _, id := range s.view.Plugins {
			if id != "link-reader" {
				a.settingsRow(c.Key(id), id, settings.ButtonOptions{Value: "已注册 · 只读", Disabled: true, MutedValue: true})
			}
		}
		if !s.expanded {
			a.modelNote(c, s.note, true)
			return
		}
		if !s.loaded {
			message := "\u3000" // 先留空，读得慢了才写提示
			if loadui.Visible(lv) {
				message = "正在读取插件设置…"
			}
			if s.note != "" {
				message = s.note
			}
			a.modelNote(c, message, s.note != "")
			return
		}
		e := s.view.Configured.Expand
		a.linkToggle(c, "给她看", config.LinkContextEnabled, s.view.Configured.ContextEnabled)
		a.linkToggle(c, "自动展开", config.LinkEnabled, e.Enabled)
		a.settingsBreak(c, "自动展开使用自己的名单，角色接话档位独立。")
		a.settingsHeading(c, "展开会话")
		listMode := 0
		if e.Mode == "blacklist" {
			listMode = 1
		}
		a.settingsOneOf(c, "link-list-mode", "名单方式", []string{"白名单", "黑名单"}, nil, listMode, s.busy, func(i int) {
			if i == 1 {
				s.confirmBox.Open = true // 切到黑名单影响面大，先确认
				return
			}
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkList, Mode: "whitelist", Conversations: slices.Clone(e.Conversations)})
		})
		hint := "只在所选会话展开"
		if e.Mode == "blacklist" {
			hint = "所选会话不展开，其他合格QQ会话会展开"
		}
		a.modelNote(c, hint, false)
		a.settingsRow(c, "选择会话", settings.ButtonOptions{Value: fmt.Sprintf("已选 %d 个", len(e.Conversations)), Arrow: true, Disabled: s.busy, OnActivate: func() { s.listBox.Open = true; s.query = ""; s.dirty = true; s.highlight = -1; a.linkJob(nil) }})
		a.settingsRow(c, "刷新本机会话目录", settings.ButtonOptions{Disabled: s.busy, OnActivate: func() { a.linkJob(nil) }})
		a.linkEdit(c, "每条消息最多几个链接", config.LinkMaxLinks, strconv.Itoa(e.MaxLinks), true)
		a.linkToggle(c, "识别裸 BV / av 号", config.LinkBareBiliID, e.BareBiliID)
		a.linkToggle(c, "图被拒收后旋转重发一次", config.LinkRotateOnReject, e.RotateOnReject)
		a.linkToggle(c, "拼接后另附原图", config.LinkPanoramaOriginals, e.PanoramaOriginals)
		a.linkToggle(c, "翻译外文正文", config.LinkTranslateEnabled, e.Translate.Enabled)
		a.linkEdit(c, "翻译成什么语言", config.LinkTranslateTarget, e.Translate.Target, false)
		a.linkEdit(c, "翻译时限（秒，1～60）", config.LinkTranslateTimeout, strconv.Itoa(e.Translate.TimeoutSeconds), true)
		shown := e.Translate.Model
		if shown == "" {
			shown = "跟后台模型"
		}
		a.linkEditShown(c, "翻译用的模型（留空跟后台模型）", config.LinkTranslateModel, e.Translate.Model, shown, false)
		if k := a.settingsKit(); k != nil {
			a.settingsHeading(c, "网站与页面类型")
			a.linkPlatformsPage(c, k, e)
		} else {
			a.settingsHeading(c, "消息格式")
			a.linkSitePage(c, e)
		}
		a.linkForwardPage(c, e)
		a.linkToggle(c, "下载视频", config.LinkVideoEnabled, e.Video.Enabled)
		a.linkEdit(c, "单个视频超时（秒，1～600）", config.LinkVideoTimeout, strconv.Itoa(e.Video.TimeoutSeconds), true)
		a.linkEdit(c, "单张图片上限（MB，1～20，超过的不发）", config.LinkImageMaxMB, strconv.Itoa(e.ImageMaxMB), true)
		a.linkEdit(c, "单个视频上限（MB，1～1000）", config.LinkVideoMaxMB, strconv.Itoa(e.Video.MaxMB), true)
		a.linkEdit(c, "私聊开始状态", config.LinkPrivateStatus, e.PrivateStatus, false)
		a.linkEdit(c, "群开始表情编号（留空不贴）", config.LinkStartEmoji, e.StartEmoji, false)
		a.linkEdit(c, "群完成表情编号（留空不贴）", config.LinkDoneEmoji, e.DoneEmoji, false)
		a.linkEdit(c, "群失败表情编号（留空不贴）", config.LinkFailEmoji, e.FailEmoji, false)
		a.modelNote(c, s.view.RunningReason, false)
		a.modelNote(c, s.view.DisabledReason, false)
		a.modelNote(c, s.note, true)
		if s.busy && loadui.Visible(lv) { // 保存得快就不插这一行
			a.modelNote(c, "正在保存或读取…", false)
		}
	})
	a.linkDialogs(c)
}
func (s *linkPageState) filter() {
	query := strings.ToLower(strings.TrimSpace(s.query))
	if !s.dirty && query == s.filteredQuery {
		return
	}
	s.dirty = false
	s.filteredQuery = query
	s.rows = s.rows[:0]
	for _, kind := range []string{"group", "private"} {
		added := false
		for i, k := range s.view.KnownConversations {
			if k.Kind != kind {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(k.Name+" "+k.Account), query) {
				continue
			}
			if !added {
				header := "QQ群"
				if kind == "private" {
					header = "QQ私聊"
				}
				s.rows = append(s.rows, linkRow{index: -1, header: header})
				added = true
			}
			s.rows = append(s.rows, linkRow{index: i})
		}
	}
}
func (a *app) linkCheck(id string, value bool) {
	s := &a.linkSettings
	ids := slices.Clone(s.view.Configured.Expand.Conversations)
	if value {
		ids = append(ids, id)
	} else {
		ids = slices.DeleteFunc(ids, func(k string) bool { return k == id })
	}
	a.linkJob(&api.LinkSettingsChange{Key: config.LinkList, Mode: s.view.Configured.Expand.Mode, Conversations: ids})
}
func (a *app) linkDialogs(c *ui.Context) {
	s := &a.linkSettings
	a.linkSiteDialogs(c)
	a.settingsBox(c, "link-blacklist-confirm", &s.confirmBox, "切换为黑名单", overlay.BoxOptions{Actions: []overlay.BoxAction{{ID: "confirm", Label: "确认切换", Default: true, Disabled: s.busy}, {ID: "cancel", Label: "取消"}}, OnAction: func(id string) {
		if id == "confirm" {
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkList, Mode: "blacklist", Conversations: slices.Clone(s.view.Configured.Expand.Conversations)})
		}
	}}, func() { ui.Text(c, "所选会话不展开，其他合格QQ会话会展开") })
	a.settingsBox(c, "link-editor", &s.editBox, s.editTitle, overlay.BoxOptions{Actions: []overlay.BoxAction{{ID: "save", Label: "保存", Default: true, KeepOpen: true, Disabled: s.busy}, {ID: "cancel", Label: "取消"}}, OnAction: func(id string) {
		if id == "save" {
			a.linkEditSave()
		}
	}}, func() {
		if s.numeric {
			limit := 1000
			if s.editKey == config.LinkMaxLinks {
				limit = 20
			} else if s.editKey == config.LinkVideoTimeout {
				limit = 600
			} else if s.editKey == config.LinkTranslateTimeout {
				limit = 60
			} else if s.editSite != "" {
				limit = 100000
			}
			a.settingsNumber(c, "link-number-input", &s.number, "编辑插件数值", limit, false, false, func(value string) { s.number.Text = value; a.linkEditSave() })
		} else {
			a.settingsField(c, "link-text-input", &s.text, "编辑插件文字", a.linkEditSave)
		}
		a.modelNote(c, s.editError, true)
	})
	a.settingsBox(c, "link-multiselect", &s.listBox, "选择展开会话", overlay.BoxOptions{Wide: true, FullBody: true, BodyHeight: 450, Actions: []overlay.BoxAction{{ID: "close", Label: "完成"}}}, func() {
		a.settingsSearch(c, "link-search", &s.query, "搜索会话")
		s.filter()
		s.list.Selected = &s.highlight
		s.list.Header = func(i int) bool { return s.rows[i].header != "" }
		list := ui.List(c, &s.list, len(s.rows)).Grow(1).MinHeight(0).ItemKey(func(i int) any {
			r := s.rows[i]
			if r.header != "" {
				return "header-" + r.header
			}
			return s.view.KnownConversations[r.index].ID
		}).Rows(func(row ui.ListRow) {
			r := s.rows[row.Index]
			if r.header != "" {
				a.settingsHeading(c, r.header)
				return
			}
			k := s.view.KnownConversations[r.index]
			checked := slices.Contains(s.view.Configured.Expand.Conversations, k.ID)
			label := k.Name + " · " + k.Account
			if k.Unavailable {
				label += " · 当前目录不可用"
			}
			a.settingsCheck(c, k.ID, label, checked, s.busy || (k.Unavailable && !checked), func(v bool) { a.linkCheck(k.ID, v) })
		})
		if list.Submitted() && s.highlight >= 0 && s.highlight < len(s.rows) && !s.busy {
			r := s.rows[s.highlight]
			if r.index >= 0 {
				k := s.view.KnownConversations[r.index]
				checked := slices.Contains(s.view.Configured.Expand.Conversations, k.ID)
				if !k.Unavailable || checked {
					a.linkCheck(k.ID, !checked)
				}
			}
		}
		if len(s.rows) == 0 {
			a.modelNote(c, "没有匹配的本机会话", false)
		}
	})
}
