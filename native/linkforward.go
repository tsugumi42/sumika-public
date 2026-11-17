package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/linktrunc"
)

// 链接解析设置里的“合并转发”分组（规格-链接解析模板第十四节）：节点名和卡片外面的四项文字。
// 五个文本框、一排插入占位符的按钮（插进选中的那一项）、画成卡片样子的实时预览，错误写在各自的框下面，有错时不能保存。

// 卡片各项的名字、预览里没填时显示的 QQ 默认样子。
var forwardFields = []struct{ key, label, fallback string }{
	{"source", "卡片标题", "群聊的聊天记录"},
	{"news1", "预览第一行", ""},
	{"news2", "预览第二行", ""},
	{"summary", "底部摘要", "查看3条转发消息"},
	{"prompt", "外显（聊天列表里那一行）", "[聊天记录]"},
}

type linkForwardState struct {
	open, dirty bool
	name        string
	text        [5]string // 按 forwardFields 的顺序
	target      int       // 点“插入”按钮时写进哪一项
	// 上一帧结束时的草稿：文本框直接改 name、text，下一帧开头比一比就知道有没有人在打字。
	lastName   string
	lastText   [5]string
	seeded     bool
	seededFrom string // 上次从哪份已保存设置填的草稿
}

// seed 用已保存的设置填草稿；改到一半（dirty）不覆盖，已保存的设置没变也不重填
// （点保存后、设置刷新前的几帧里草稿不会闪回旧值）。
func (f *linkForwardState) seed(c config.LinkForward) {
	if f.name != f.lastName || f.text != f.lastText {
		f.dirty = true // 文本框被改过
	}
	defer func() { f.lastName, f.lastText = f.name, f.text }()
	src := fmt.Sprintf("%#v", c)
	if f.dirty || (f.seeded && src == f.seededFrom) {
		return
	}
	f.seeded, f.seededFrom = true, src
	f.name = c.NodeName
	f.text = [5]string{c.Source, "", "", c.Summary, c.Prompt}
	for i := 0; i < linktemplate.CardNewsLines && i < len(c.News); i++ {
		f.text[1+i] = c.News[i]
	}
}

// clean 说草稿已保存或已重置：不再算改到一半。
func (f *linkForwardState) clean() {
	f.dirty = false
	f.lastName, f.lastText = f.name, f.text
}

// card 是草稿拼成的配置写法：预览没写的行不留空位。
func (f *linkForwardState) card() config.LinkForward {
	c := config.LinkForward{NodeName: strings.TrimSpace(f.name), Source: f.text[0], Summary: f.text[3], Prompt: f.text[4]}
	for _, t := range f.text[1:3] {
		if strings.TrimSpace(t) != "" {
			c.News = append(c.News, t)
		}
	}
	return c
}

// errors 逐项校验，返回每个框下面要写的错误（没错是空串），第 0 项是节点名。
func (f *linkForwardState) errors() (errs [6]string, any bool) {
	if bad := (config.LinkForward{NodeName: strings.TrimSpace(f.name)}).Check(); len(bad) > 0 {
		errs[0] = strings.TrimPrefix(bad[0], "linkReader.expand.forward.nodeName：")
	}
	for i, t := range f.text {
		if _, err := linktemplate.ParseCard(t); err != nil {
			errs[1+i] = err.Error()
		}
	}
	for _, e := range errs {
		if e != "" {
			any = true
		}
	}
	return
}

// forwardPreview 用示例数据渲染卡片四项；某项没写或缺字段就是 QQ 默认（返回 ok=false）。
func forwardPreview(src string, limit int) (string, bool) {
	tpl, err := linktemplate.ParseCard(src)
	if err != nil {
		return "", false
	}
	s := linktrunc.Display(linktemplate.RenderCard(tpl, linktemplate.SampleCardEnv()), limit)
	return s, s != ""
}

func (a *app) linkForwardPage(c *ui.Context, e config.LinkExpand) {
	s := &a.linkSettings
	f := &s.fwd
	f.seed(e.Forward)
	if k := a.settingsKit(); k != nil {
		summary := "QQ 默认"
		if e.Forward.NodeName != "" || e.Forward.Source != "" || len(e.Forward.News) > 0 || e.Forward.Summary != "" || e.Forward.Prompt != "" {
			summary = "已自定义"
		}
		a.settingsHeading(c, "合并转发")
		k.DisclosureRow(c, "fwd-card", "节点名和卡片文字", summary, f.open, false, func() { f.open = !f.open }, nil)
	} else {
		a.settingsHeading(c, "合并转发")
		value := "展开"
		if f.open {
			value = "收起"
		}
		a.settingsRow(c.Key("fwd-card"), "节点名和卡片文字", settings.ButtonOptions{Value: value, Arrow: true, OnActivate: func() { f.open = !f.open }})
	}
	if !f.open {
		return
	}
	errs, bad := f.errors()
	a.modelNote(c, "节点名留空用她的 QQ 名字，头像永远是她自己的。卡片文字留空用 QQ 默认；字段取不到整项退回默认，太长自动加省略号。", false)
	a.modelNote(c, "节点名（留空用她的 QQ 名字）", false)
	a.settingsTextArea(c, "fwd-name", &f.name, "节点名（留空用她的 QQ 名字）", 1, 1)
	if errs[0] != "" {
		a.modelNote(c, errs[0], true)
	}
	for i, fl := range forwardFields {
		a.modelNote(c, fl.label, false)
		a.settingsTextArea(c, "fwd-"+fl.key, &f.text[i], fl.label, 1, 2)
		if errs[1+i] != "" {
			a.modelNote(c, errs[1+i], true)
		}
	}
	a.settingsHeading(c, "插入占位符")
	labels := []string{"标题", "首行", "次行", "摘要", "外显"}
	a.settingsOneOf(c, "fwd-target", "插入到", labels, nil, f.target, false, func(i int) { f.target = i })
	ui.Row(c.Key("fwd-ins")).Wrap().Gap(6).Children(func() {
		for _, fd := range linktemplate.CardSet().Fields() {
			name := fd.Names[0]
			a.settingsButton(c, "fwd-ins-"+fd.ID, "$"+name, false, false, func() {
				f.text[f.target] += "$" + name
				f.dirty = true
			})
		}
	})
	a.settingsHeading(c, "预览（示例数据）")
	a.linkForwardCard(c, f)
	a.modelNote(c, "这是示例内容，节点名按设置；留空时发出去是她的 QQ 名字。卡片文字超出 QQ 的显示宽度会被截断。", false)
	ui.Row(c.Key("fwd-actions")).Gap(8).Children(func() {
		a.settingsButton(c, "fwd-save", "保存合并转发", true, s.busy || bad, func() {
			f.dirty = false
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkForwardCard, Card: f.card()})
		})
		a.settingsButton(c, "fwd-reset", "恢复 QQ 默认", false, s.busy, func() {
			f.dirty = false
			f.name, f.text = "", [5]string{}
			a.linkJob(&api.LinkSettingsChange{Key: config.LinkForwardCard, Card: config.LinkForward{}})
		})
	})
}

// linkForwardCard 把草稿画成合并转发卡片的样子：标题、两行预览、分隔线、摘要；外显写在卡片下面。
func (a *app) linkForwardCard(c *ui.Context, f *linkForwardState) {
	limits := []int{linktemplate.CardSourceLimit, linktemplate.CardNewsLimit, linktemplate.CardNewsLimit, linktemplate.CardSummaryLimit, linktemplate.CardPromptLimit}
	shown := func(i int) (string, bool) {
		if s, ok := forwardPreview(f.text[i], limits[i]); ok {
			return s, true
		}
		return forwardFields[i].fallback, false
	}
	name := strings.TrimSpace(f.name)
	if name == "" {
		name = "她的 QQ 名字"
	}
	var line, ink, muted ui.Color
	var pad, radius float32 = 12, 12
	var bg ui.Color
	if k := a.settingsKit(); k != nil {
		line, ink, muted, bg = k.P.Line, k.P.Ink, k.P.Muted, k.P.Bubble
		pad, radius = k.G.SpaceMedium, k.G.RadiusControl
	} else {
		line, ink, muted, bg = c.Theme().Border, c.Theme().Text, c.Theme().Text, c.Theme().Surface
	}
	ui.Column(c.Key("fwd-preview")).FillWidth().Padding(pad).Gap(6).Radius(radius).Background(bg).Border(1, line).Label("合并转发卡片预览").Children(func() {
		title, _ := shown(0)
		ui.Text(c.Key("fwd-pv-source"), title).TextColor(ink).Wrap()
		for i := 1; i <= 2; i++ {
			if t, ok := shown(i); ok && t != "" {
				ui.Text(c.Key("fwd-pv-news"+string(rune('0'+i))), t).TextColor(muted).Wrap()
			}
		}
		if f.text[1] == "" && f.text[2] == "" {
			ui.Text(c.Key("fwd-pv-news-default"), name+"：（节点内容的前几行）").TextColor(muted).Wrap()
		}
		ui.Box(c.Key("fwd-pv-line")).FillWidth().Height(1).Background(line)
		sum, _ := shown(3)
		ui.Text(c.Key("fwd-pv-summary"), sum).TextColor(muted).Wrap()
	})
	prompt, _ := shown(4)
	ui.Text(c.Key("fwd-pv-prompt"), "外显："+prompt).TextColor(muted).Wrap()
}
