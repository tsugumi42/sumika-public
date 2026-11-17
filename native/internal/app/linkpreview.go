package app

import (
	"context"
	"errors"
	"strings"

	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 链接解析模板的只读入口（sumika-ctl link preview / link template，规格-链接解析模板第十节）：
// 取数走共享 Service 和缓存，只渲染文字，不发消息、不下载媒体、不改配置。

// ErrLinkInvalid 是请求本身不对（链接不认识、站点键不认识、模板写错），不是后台出了问题。
var ErrLinkInvalid = errors.New("链接预览的参数不合法")

type LinkPreviewRequest struct {
	URL, Template, Site string
	Sample              bool
}
type LinkPreviewOut struct {
	Site, Template, Source string
	Forward                bool
	Messages, Notes        []string
}
type LinkSiteState struct {
	Key, Label, Direct, Forward, ForwardMode string
	ForwardOver, DescLimit                   int
	Fields                                   []linktemplate.Field
}

func invalidLink(msg string) error { return errors.Join(ErrLinkInvalid, errors.New(msg)) }

// effectiveTemplate 是该站点该形态现在生效的模板原文：配置里的（已展开 @预设），写错或没写用内置默认。
func effectiveTemplate(site linktemplate.Site, expand config.LinkExpand, forward bool) string {
	key := site.Key
	desc := expand.Sites[site.Key].ShowDesc // 开着时内置默认带简介那一行
	if forward {
		key += "-forward"
	}
	if src := expand.Templates[key]; strings.TrimSpace(src) != "" {
		if _, err := site.Parse(src); err == nil {
			out, _ := site.ResolveWith(src, desc)
			return out
		}
	}
	return site.SourceWith(forward, desc)
}

func (r *DesktopReader) LinkPreview(ctx context.Context, req LinkPreviewRequest) (LinkPreviewOut, error) {
	a := r.desk.app
	expand := a.currentLinks().Values.Expand
	if req.Sample {
		site, forward, ok := linktemplate.SiteByKey(req.Site)
		if !ok {
			return LinkPreviewOut{}, invalidLink("不认识的站点键 " + req.Site)
		}
		src := req.Template
		if strings.TrimSpace(src) == "" {
			src = effectiveTemplate(site, expand, forward)
		}
		text, err := site.Preview(src)
		if err != nil {
			return LinkPreviewOut{}, invalidLink("模板有错：" + err.Error())
		}
		resolved, _ := site.Resolve(src)
		return LinkPreviewOut{Site: req.Site, Template: resolved, Forward: forward, Source: "sample", Messages: []string{text}}, nil
	}
	if a.linkSvc == nil {
		return LinkPreviewOut{}, errors.New("现在没有链接解析（只看历史模式或没有接收），请先开始接收")
	}
	link, ok := linkreader.Recognize(req.URL)
	if !ok {
		return LinkPreviewOut{}, invalidLink("不是认识的链接")
	}
	res, err := a.linkSvc.Get(ctx, link.Raw)
	if err != nil {
		return LinkPreviewOut{}, errors.New("取数失败：" + err.Error())
	}
	if !linkexpand.Supports(res.Platform, res.Kind) {
		return LinkPreviewOut{}, invalidLink("这种链接还没有发送格式（" + res.Platform + "/" + res.Kind + "）")
	}
	key := linktemplate.SiteKey(res.Platform, res.Kind)
	site, _, _ := linktemplate.SiteByKey(key)
	opt := linkplugin.BuildOptions(expand) // 和插件真发时同一套排版设置；模板表、站点表是复制的，下面可以改
	var notes []string
	if req.Site != "" { // --site <键> 强制直发形态，<键>-forward 强制转发形态
		s2, forward, ok := linktemplate.SiteByKey(req.Site)
		if !ok || s2.Key != key {
			return LinkPreviewOut{}, invalidLink("--site 要写这条链接对应的站点键：" + key)
		}
		set := opt.Sites[key]
		set.Forward = linktemplate.ForwardNever
		if forward {
			set.Forward = linktemplate.ForwardAlways
		}
		opt.Sites[key] = set
	}
	if strings.TrimSpace(req.Template) != "" {
		if _, err := site.Parse(req.Template); err != nil {
			return LinkPreviewOut{}, invalidLink("模板有错：" + err.Error())
		}
		cfgKey := key
		if opt.Sites[key].Forward == linktemplate.ForwardAlways {
			cfgKey += "-forward"
		}
		if site.HasForward() && opt.Sites[key].Forward != linktemplate.ForwardAlways && opt.Sites[key].Forward != linktemplate.ForwardNever {
			notes = append(notes, "没有用 --site 指定形态：模板只替换这条链接实际走的那一形态（自动判断）")
			opt.Templates[key], opt.Templates[key+"-forward"] = req.Template, req.Template
		} else {
			opt.Templates[cfgKey] = req.Template
		}
	}
	if mode := linkplugin.ArtistMode(a.artistBook(), &res, expand.Artist); mode != "" { // 预览和真发一样；预览不记待确认
		opt.Artist = mode
		notes = append(notes, "这条推文的作者是确认过的画师（"+mode+"）：自定义模板和合并转发设置不生效，按画师的发法渲染")
	}
	draft := linkexpand.Build(&res, "preview", opt, 0)
	forward := false
	for _, p := range draft.Parts {
		if p.Kind == "forward" {
			forward = true
		}
	}
	used := effectiveTemplate(site, config.LinkExpand{Templates: opt.Templates}, forward)
	return LinkPreviewOut{Site: key, Template: used, Forward: forward, Source: "fetched", Messages: draft.Describe(), Notes: notes}, nil
}

func (r *DesktopReader) LinkTemplates(ctx context.Context, siteKey string) ([]LinkSiteState, error) {
	expand := r.desk.app.currentLinks().Values.Expand
	var out []LinkSiteState
	for _, s := range linktemplate.Sites() {
		if siteKey != "" && s.Key != siteKey {
			continue
		}
		st := LinkSiteState{Key: s.Key, Label: s.Label, Direct: effectiveTemplate(s, expand, false), DescLimit: s.DescLimit, ForwardOver: s.ForwardOver}
		set := expand.Sites[s.Key]
		if set.ForwardOver != nil {
			st.ForwardOver = *set.ForwardOver
		}
		if set.DescLimit != nil {
			st.DescLimit = *set.DescLimit
		}
		if s.HasForward() {
			st.Forward = effectiveTemplate(s, expand, true)
			st.ForwardMode = set.Forward
			if st.ForwardMode == "" {
				st.ForwardMode = linktemplate.ForwardAuto
			}
		}
		if siteKey != "" {
			st.Fields = s.Set.Fields()
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, invalidLink("不认识的站点键 " + siteKey)
	}
	return out, nil
}
