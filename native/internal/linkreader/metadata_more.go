package linkreader

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func fetchableMore(l Link) bool {
	switch l.Platform {
	case "github":
		return l.Kind == "repository" || l.Kind == "issue" || l.Kind == "pull_request" || l.Kind == "release" || l.Kind == "gist"
	case "youtube":
		return l.Kind == "video"
	case "douyin":
		return l.Kind == "video" || l.Kind == "note"
	case "netease":
		return l.Kind == "song" || l.Kind == "playlist" || l.Kind == "album"
	case "pixiv":
		return l.Kind == "artwork" || l.Kind == "novel" || l.Kind == "series" || l.Kind == "novel_series"
	case "booth":
		return l.Kind == "item"
	case "bluesky":
		return l.Kind == "post" || l.Kind == "profile"
	case "telegram":
		return l.Kind == "post"
	case "steam":
		return l.Kind == "app" || l.Kind == "package" || l.Kind == "sharedfile" || l.Kind == "profile"
	case "afdian":
		return l.Kind == "creator" || l.Kind == "post" || l.Kind == "album" || l.Kind == "album_post"
	case "spotify":
		return l.Kind == "track" || l.Kind == "album" || l.Kind == "playlist" || l.Kind == "artist" || l.Kind == "episode"
	case "tiktok":
		return l.Kind == "video" || l.Kind == "user"
	}
	return false
}

func setMoreHeaders(req *http.Request, platform string) {
	switch platform {
	case "github":
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Del("Referer")
	case "youtube":
		req.Header.Set("Referer", "https://www.youtube.com/")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	case "douyin":
		req.Header.Set("Referer", "https://www.douyin.com/")
	case "netease":
		req.Header.Set("Referer", "https://music.163.com/")
	case "pixiv":
		req.Header.Set("Referer", "https://www.pixiv.net/")
	case "booth":
		req.Header.Set("Referer", "https://booth.pm/")
		if strings.HasSuffix(req.URL.Path, ".json") {
			req.Header.Set("Accept", "application/json")
		} else {
			req.Header.Set("Accept", "text/html")
		}
	case "telegram":
		req.Header.Del("Referer")
		req.Header.Set("Accept", "text/html")
	case "bluesky", "misskey", "mastodon", "steam":
		// 这几家的公开接口不需要 Referer，也不能沿用默认的 bilibili 来源（几个分支合并时空 case 不会落到下一条，所以写在同一行）。
		req.Header.Del("Referer")
	case "afdian":
		req.Header.Set("Referer", "https://afdian.com/")
	case "spotify":
		// embed 页是 HTML，oEmbed 是 JSON；两种都接受，不发 Cookie。
		// request() 默认写的是 B 站 Referer，这里删掉，不带错位来源。
		req.Header.Set("Accept", "text/html,application/json;q=0.9")
		req.Header.Del("Referer")
	case "tiktok":
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", "https://www.tiktok.com/")
	case "xiaoheihe":
		req.Header.Set("Referer", "https://www.xiaoheihe.cn/")
	case "xiaohongshu":
		req.Header.Del("Referer")
	}
}

// GitHub 匿名额度按出口 IP 共用；剩余耗尽后等 reset，不能换会话继续刷。
func (f *Fetcher) githubBudget(resp *http.Response) {
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return
	}
	reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err == nil {
		f.rate.cooldownCapped("github", time.Unix(reset, 0).UTC().Format(http.TimeFormat), githubResetCap)
		return
	}
	f.rate.cooldown("github", resp.Header.Get("Retry-After"))
}

func timestampField(p *string) (Field[time.Time], error) {
	if p == nil {
		return missing[time.Time]("interface_omitted"), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if v, err := time.Parse(layout, *p); err == nil {
			return available(v.UTC()), nil
		}
	}
	return Field[time.Time]{}, failure("invalid_response", "date")
}
func previewField(p *string) Field[string] {
	f := stringField(p)
	// 元数据接口的正文只保留开头；标记截取，不能冒充完整文章。
	if p != nil && f.State == "available" {
		// \r\n、单独的 \r 换成 \n（Steam 礼包等简介带 Windows 换行，原样发会多出空行或乱码）
		f.Value = normalizeNewlines(f.Value)
		// 切点落在链接中间时退到链接开头，不留半条链接
		if head, cut := CutRunes(f.Value, 800); cut {
			f.Value, f.Truncated = head, true
		}
	}
	return f
}
func imageMetadata(raw Field[string]) Media {
	return Media{Type: "image", URL: raw, Thumbnail: missing[string]("not_applicable"), Width: missing[int]("interface_omitted"), Height: missing[int]("interface_omitted"), Duration: missing[float64]("not_applicable"), Size: missing[int64]("interface_omitted")}
}
func appendMedia(r *Result, m Media) {
	if r.Media.State != "available" {
		r.Media = available([]Media{})
	}
	r.Media.Value = append(r.Media.Value, m)
	r.Blocks = append(r.Blocks, Block{Type: m.Type, Media: &r.Media.Value[len(r.Media.Value)-1]})
}
func appendBody(r *Result) {
	if r.Text.State == "available" {
		r.Blocks = append(r.Blocks, Block{Type: "text", Text: r.Text.Value})
	}
}

// JSON Decoder 理解字符串里的括号和转义；不自己数大括号，也不执行页面脚本。
func pageJSON(page []byte, marker string, out any) bool {
	start := bytes.Index(page, []byte(marker))
	if start < 0 {
		return false
	}
	rest := bytes.TrimSpace(page[start+len(marker):])
	if len(rest) == 0 || rest[0] != '=' {
		return false
	}
	return json.NewDecoder(bytes.NewReader(bytes.TrimSpace(rest[1:]))).Decode(out) == nil
}

// appendLinkCardBlock 把帖子附带的外链卡写成正文后面的一段：卡片标题一行，正文里没有这条链接时再写链接一行。
// Bluesky 的外链只放在 embed 里、不在正文中，不写出来群里就只看到一句以冒号结尾的话。
func appendLinkCardBlock(r *Result) {
	if r.LinkCard.State != "available" || r.LinkCard.Value.URL.State != "available" {
		return
	}
	c := r.LinkCard.Value
	orig := strings.TrimSpace(c.URL.Value)
	// 写出去前过一遍通用追踪表（utm_* 等任何站都删）；不是 http(s) 的地址不写
	link, err := CleanAny(orig)
	if err != nil {
		link = ""
	}
	var lines []string
	if t := strings.TrimSpace(c.Title.Value); c.Title.State == "available" && t != "" && t != link && t != orig {
		lines = append(lines, t)
	}
	if link != "" && !strings.Contains(r.Text.Value, link) && !strings.Contains(r.Text.Value, orig) {
		lines = append(lines, link)
	}
	if len(lines) > 0 {
		r.Blocks = append(r.Blocks, Block{Type: "link_card", Text: strings.Join(lines, "\n")})
	}
}
