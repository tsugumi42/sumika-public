package main

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// 可替换浏览器入口让 Tester 不启动真实浏览器；打开失败只记录主机，不记录可能带令牌的地址或错误原文。
var openBrowser = func(c *ui.Context, raw string, done func(error)) { c.OpenURLThen(raw, done) }

func (a *app) openLink(c *ui.Context, raw string) {
	if strings.ContainsAny(raw, `\`) || strings.ContainsFunc(raw, unicode.IsControl) {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || !(strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) {
		return
	}
	openBrowser(c, raw, func(err error) {
		if err != nil {
			a.rm.log("打开链接失败，主机 %s", u.Hostname())
		}
	})
}

// 文字预览和封面共用一个导航动作；封面独立占位，异步到图不改变卡片高度。
func (a *app) cardBlock(c *ui.Context, s seg, mine bool) {
	card := s.Card
	width := imgMaxW(c)
	appearance := content.Appearance{Outgoing: mine, Disabled: card.URL == ""}
	root := content.LinkPreview(c, content.PreviewData{Title: card.Title, Description: card.Description, Site: card.Source, URL: card.URL}, content.PreviewOptions{Appearance: appearance, Width: width, OnOpen: func(raw string) { a.openLink(c, raw) }}).Label(card.Title)
	root.Children(func() {
		if s.mediaHash() != "" {
			metrics := theme.T(c).Metrics()
			inner := width - metrics.Preview().PaddingLeft() - metrics.Preview().PaddingRight()
			e := a.is().getRef(keyOf(s, width, false, a.is().scale), refOf(s))
			w, h := float32(s.Width), float32(s.Height)
			if w <= 0 || h <= 0 {
				w, h = inner, inner*3/4
			}
			media := content.Media{ID: s.mediaHash(), Label: "卡片封面", Bitmap: e.bmp, Width: w, Height: h, Status: content.MediaLoading}
			switch e.state {
			case imgOK:
				media.Status = content.MediaReady
			case imgFailed, imgBad:
				media.Status = content.MediaFailed
			}
			// 封面属于整张卡片的链接目标，不额外提供下载或取消操作。
			radius := metrics.Quote().Radius()
			corners := content.Corners{TopLeft: radius, TopRight: radius, BottomRight: radius, BottomLeft: radius}
			content.Photo(c, media, content.MediaOptions{Appearance: appearance, Width: inner, Corners: &corners}).Margin(metrics.Preview().MediaGap(), 0, 0).PassThrough()
		}
	})
}
