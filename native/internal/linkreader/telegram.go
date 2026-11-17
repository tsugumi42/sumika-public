package linkreader

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Telegram 公开频道帖子：识别 t.me/<频道>/<号>、t.me/s/<频道>/<号>（网页预览写法）和 t.me/c/<数字>/<号>（私有频道）。
// 取数只读 embed 页（?embed=1&mode=tme），不登录、不带 Cookie；私有频道一律要登录，识别出来但不请求。

const telegramSource = "telegram_embed"

var tgChannel = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)

// recognizeTelegram 在主机精确匹配（t.me、telegram.me）后调用。查询串整条丢弃：?single、?embed、?comment、?t 都不影响帖子本身。
// 私有频道的身份写成 c/<频道数字>/<号>，fetch 看到 c/ 前缀直接按需要登录处理，不发请求。
func recognizeTelegram(u *url.URL, raw string) (Link, bool) {
	if strings.Contains(u.EscapedPath(), "%") {
		return Link{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 && parts[0] == "s" {
		parts = parts[1:]
	}
	l := Link{Platform: "telegram", Kind: "post", Raw: raw}
	switch {
	case len(parts) == 3 && parts[0] == "c" && positiveID(parts[1]) && positiveID(parts[2]):
		l.ID = "c/" + parts[1] + "/" + parts[2]
	case len(parts) == 2 && tgChannel.MatchString(parts[0]) && positiveID(parts[1]):
		l.ID = strings.ToLower(parts[0]) + "/" + parts[1]
	default:
		return Link{}, false
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", "t.me", "/"+l.ID, ""
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	l.Clean = u.String()
	return l, true
}

func (f *Fetcher) fetchTelegram(ctx context.Context, l Link) (Result, error) {
	if strings.HasPrefix(l.ID, "c/") {
		// 私有频道的帖子必须登录才能看：不请求，按需要登录处理
		return Result{}, &FetchError{Kind: "login_required", Reason: "private_channel", Source: telegramSource}
	}
	page, status, h, err := f.request(ctx, "https://t.me/"+l.ID+"?embed=1&mode=tme", telegramSource)
	if err != nil {
		return Result{}, err
	}
	if err = httpFailure(status, h, telegramSource); err != nil {
		return Result{}, err
	}
	return parseTelegram(l, page)
}

func tgHasClass(n *html.Node, class string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}
func tgAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// tgWalk 按文档顺序遍历；fn 返回 false 表示不再进入这个节点的子树。
func tgWalk(n *html.Node, fn func(*html.Node) bool) {
	if !fn(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		tgWalk(c, fn)
	}
}
func tgFind(n *html.Node, class string) *html.Node {
	var found *html.Node
	tgWalk(n, func(x *html.Node) bool {
		if found != nil {
			return false
		}
		if tgHasClass(x, class) {
			found = x
			return false
		}
		return true
	})
	return found
}

var tgBackground = regexp.MustCompile(`background-image:\s*url\(\s*['"]?([^'")]+)['"]?\s*\)`)

// tgMediaURL 只收 https 的 Telegram 图床地址；别的主机不进结果。
func tgMediaURL(raw string) (string, bool) {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return "", false
	}
	if !mediaHost("telegram", u.Hostname()) {
		return "", false
	}
	return u.String(), true
}
func tgStyleURL(n *html.Node) (string, bool) {
	m := tgBackground.FindStringSubmatch(tgAttr(n, "style"))
	if m == nil {
		return "", false
	}
	return tgMediaURL(m[1])
}

// tgText 把消息正文节点转成纯文字：<br> 是换行；带不同显示文字的外链在后面补一个括号里的完整地址。
func tgText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		switch {
		case x.Type == html.TextNode:
			b.WriteString(x.Data)
			return
		case x.Type != html.ElementNode:
		case x.Data == "br":
			b.WriteByte('\n')
			return
		case x.Data == "a":
			start := b.Len()
			for c := x.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			shown := b.String()[start:]
			href := tgAttr(x, "href")
			switch tgLinkText(shown, href) {
			case tgReplace: // 显示文字是被缩短的地址（带省略号）：换成完整地址
				cur := b.String()[:start]
				b.Reset()
				b.WriteString(cur + href)
			case tgAppend:
				b.WriteString("（" + href + "）")
			}
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

const (
	tgKeep    = iota // 显示文字照旧
	tgReplace        // 换成完整地址
	tgAppend         // 在后面补一个括号里的完整地址
)

// tgLinkText 判断一个链接的显示文字要怎么处理。话题、@用户、站内相对链接不动；显示的就是地址本身也不动；
// 显示的是被省略号缩短的地址，换成完整地址（不然转发出去的就是半条链接）；显示成别的文字（“点这里”），在后面补完整地址。
func tgLinkText(shown, href string) int {
	if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
		return tgKeep
	}
	s := strings.TrimSpace(shown)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "@") || strings.HasPrefix(s, "$") {
		return tgKeep
	}
	norm := func(x string) string {
		x = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(x), "https://"), "http://")
		return strings.TrimSuffix(x, "/")
	}
	short := strings.TrimRight(s, "….")
	if short != s && strings.HasPrefix(norm(href), norm(short)) {
		return tgReplace
	}
	if norm(s) == norm(href) {
		return tgKeep
	}
	return tgAppend
}

// tgViews 解析“1.2M”“12.3K”“845”这种浏览数缩写。
func tgViews(s string) (int64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "views"))
	s = strings.ReplaceAll(s, ",", "")
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "B"), strings.HasSuffix(s, "b"):
		mult, s = 1e9, s[:len(s)-1]
	case strings.HasSuffix(s, "K"), strings.HasSuffix(s, "k"):
		mult, s = 1e3, s[:len(s)-1]
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "m"):
		mult, s = 1e6, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, " ", ""), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return int64(v*mult + 0.5), true
}

// tgDuration 解析“0:15”“1:02:03”。
func tgDuration(s string) (float64, bool) {
	total := 0
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		total = total*60 + n
	}
	return float64(total), true
}

func tgTextOf(n *html.Node) string {
	var b strings.Builder
	tgWalk(n, func(x *html.Node) bool {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		return true
	})
	return strings.TrimSpace(b.String())
}

// parseTelegram 把 embed 页变成 Result。页面里没有帖子节点时：提示“can't be displayed”是网页版不显示这个频道（版权或内容限制），
// 错误节点是帖子不存在；这两种都不当成空帖子。
func parseTelegram(l Link, page []byte) (Result, error) {
	doc, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return Result{}, failure("invalid_response", telegramSource)
	}
	var msg *html.Node
	tgWalk(doc, func(x *html.Node) bool {
		if msg == nil && tgHasClass(x, "tgme_widget_message") && tgAttr(x, "data-post") != "" {
			msg = x
		}
		return msg == nil
	})
	if msg == nil {
		// 先看提示文字：被屏蔽的频道在错误节点（tgme_widget_message_error）里写 “can't be displayed”，
		// 那是网页版不显示这个频道，不是帖子不存在；错误节点里是别的话才是 not_found。
		lower := strings.ToLower(strings.ReplaceAll(tgTextOf(doc), "’", "'"))
		if strings.Contains(lower, "can't be displayed") {
			return Result{}, &FetchError{Kind: "not_returned", Reason: "channel_hidden", Source: telegramSource}
		}
		if tgFind(doc, "tgme_widget_message_error") != nil {
			return Result{}, &FetchError{Kind: "not_found", Source: telegramSource}
		}
		return Result{}, failure("invalid_response", telegramSource)
	}
	if !strings.EqualFold(tgAttr(msg, "data-post"), l.ID) {
		return Result{}, failure("identity_mismatch", telegramSource)
	}
	r := newResult(l, telegramSource)
	channel := l.ID[:strings.Index(l.ID, "/")]
	r.Author.Account = available(channel)
	r.Author.ID = missing[string]("not_returned")
	r.Author.Name = missing[string]("not_returned")
	if n := tgFind(msg, "tgme_widget_message_owner_name"); n != nil {
		if name := tgTextOf(n); name != "" {
			r.Author.Name = available(name)
		}
	}
	r.Author.Avatar = missing[string]("not_returned")
	if n := tgFind(msg, "tgme_widget_message_user_photo"); n != nil {
		tgWalk(n, func(x *html.Node) bool {
			if x.Type == html.ElementNode && x.Data == "img" {
				if u, ok := tgMediaURL(tgAttr(x, "src")); ok {
					r.Author.Avatar = available(u)
				}
			}
			return true
		})
	}
	// 正文：跳过回复引用和外链预览里的同名节点，只取这条帖子自己的
	var body *html.Node
	tgWalk(msg, func(x *html.Node) bool {
		if tgHasClass(x, "tgme_widget_message_reply") || tgHasClass(x, "tgme_widget_message_link_preview") {
			return false
		}
		if body == nil && tgHasClass(x, "tgme_widget_message_text") {
			body = x
		}
		return body == nil
	})
	text := ""
	if body != nil {
		text = tgText(body)
	}
	if text != "" {
		r.Text = available(text)
	} else {
		r.Text = missing[string]("empty_text")
	}
	// 标题总要有：第一行；没有文字的帖子（纯图、纯视频）用“频道 #号”
	post := l.ID[strings.Index(l.ID, "/")+1:]
	title := channel + " #" + post
	if name := r.Author.Name; name.State == "available" {
		title = name.Value + " #" + post
	}
	if line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]); line != "" {
		head, _ := CutRunes(line, 72)
		title = head
	}
	r.Title = available(title)
	var dt string
	if n := tgFind(msg, "tgme_widget_message_date"); n != nil {
		if t := tgFindTag(n, "time"); t != nil {
			dt = tgAttr(t, "datetime")
		}
	}
	if dt == "" {
		r.Published = missing[time.Time]("not_returned")
	} else if r.Published, err = timestampField(&dt); err != nil {
		return Result{}, err
	}
	// 浏览数：embed 页里每条帖子底部的 tgme_widget_message_views（“2.82M”“9.35K”“845”）。
	// 老帖子页面里根本没有这个节点，如实记“没返回”，不留一个空的统计表。
	r.Stats["views"] = missing[int64]("not_returned")
	if n := tgFind(msg, "tgme_widget_message_views"); n != nil {
		if v, ok := tgViews(tgTextOf(n)); ok {
			stat(&r, "views", &v)
		}
	}
	r.Media = available([]Media{})
	appendBody(&r)
	tgMedia(&r, msg)
	return r, nil
}

func tgFindTag(n *html.Node, tag string) *html.Node {
	var found *html.Node
	tgWalk(n, func(x *html.Node) bool {
		if found != nil {
			return false
		}
		if x.Type == html.ElementNode && x.Data == tag {
			found = x
			return false
		}
		return true
	})
	return found
}

// tgMedia 按文档顺序收这条帖子的图和视频。视频播放器节点里有视频地址和封面；没有地址的（太大，网页版不给）退成封面图，并标记媒体不完整。
func tgMedia(r *Result, msg *html.Node) {
	omitted := func() (Field[int], Field[int], Field[int64]) {
		return missing[int]("interface_omitted"), missing[int]("interface_omitted"), missing[int64]("interface_omitted")
	}
	tgWalk(msg, func(x *html.Node) bool {
		if tgHasClass(x, "tgme_widget_message_reply") || tgHasClass(x, "tgme_widget_message_link_preview") {
			return false
		}
		switch {
		case tgHasClass(x, "tgme_widget_message_photo_wrap"):
			if u, ok := tgStyleURL(x); ok {
				w, h, size := omitted()
				appendMedia(r, Media{Type: "image", URL: available(u), Thumbnail: missing[string]("not_applicable"), Width: w, Height: h, Duration: missing[float64]("not_applicable"), Size: size})
			}
			return false
		case tgHasClass(x, "tgme_widget_message_video_player"), tgHasClass(x, "tgme_widget_message_roundvideo_player"):
			var src, thumb string
			dur := missing[float64]("interface_omitted")
			tgWalk(x, func(y *html.Node) bool {
				if y.Type != html.ElementNode {
					return true
				}
				switch {
				case y.Data == "video":
					if u, ok := tgMediaURL(tgAttr(y, "src")); ok {
						src = u
					}
				case tgHasClass(y, "tgme_widget_message_video_thumb"), tgHasClass(y, "tgme_widget_message_roundvideo_thumb"):
					if u, ok := tgStyleURL(y); ok {
						thumb = u
					}
				case y.Data == "time":
					if d, ok := tgDuration(tgTextOf(y)); ok {
						dur = available(d)
					}
				}
				return true
			})
			w, h, size := omitted()
			switch {
			case src != "":
				m := Media{Type: "video", URL: available(src), Thumbnail: missing[string]("interface_omitted"), Width: w, Height: h, Duration: dur, Size: size}
				if thumb != "" {
					m.Thumbnail = available(thumb)
				}
				appendMedia(r, m)
			case thumb != "":
				// 网页版不给视频文件：发封面，并标记媒体不完整
				appendMedia(r, Media{Type: "image", URL: available(thumb), Thumbnail: missing[string]("not_applicable"), Width: w, Height: h, Duration: missing[float64]("not_applicable"), Size: size})
				r.Media.Truncated, r.Media.Reason = true, "video_not_in_embed"
			}
			return false
		}
		return true
	})
}
