package linkreader

import (
	"context"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	afdianSlug = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	afdianHex  = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

	afdianBlockScript = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	afdianLineBreak   = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|h[1-6]|li|tr)>`)
	afdianAnyTag      = regexp.MustCompile(`<[^>]+>`)
)

// recognizeAfdian 设置平台、类型、ID 和规范路径；查询参数和片段由调用方丢弃。
// 旧域名 afdian.net、ifdian.net 统一规范为 afdian.com，slug 保留大小写。
func recognizeAfdian(parts []string, l *Link, host, path *string) bool {
	switch {
	case len(parts) == 2 && parts[0] == "a" && afdianSlug.MatchString(parts[1]):
		l.Kind, l.ID = "creator", parts[1]
		*path = "/a/" + parts[1]
	case len(parts) == 2 && parts[0] == "p" && afdianHex.MatchString(parts[1]):
		l.Kind, l.ID = "post", strings.ToLower(parts[1])
		*path = "/p/" + l.ID
	case len(parts) == 2 && parts[0] == "album" && afdianHex.MatchString(parts[1]):
		l.Kind, l.ID = "album", strings.ToLower(parts[1])
		*path = "/album/" + l.ID
	case len(parts) == 3 && parts[0] == "album" && afdianHex.MatchString(parts[1]) && afdianHex.MatchString(parts[2]):
		l.Kind, l.ID = "album_post", strings.ToLower(parts[1])+"/"+strings.ToLower(parts[2])
		*path = "/album/" + l.ID
	case len(parts) == 2 && parts[0] == "item" && afdianHex.MatchString(parts[1]):
		l.Kind, l.ID = "item", strings.ToLower(parts[1])
		*path = "/item/" + l.ID
	default:
		return false
	}
	l.Platform = "afdian"
	*host = "afdian.com"
	return true
}

// afdianCount 接受 JSON 数字或纯数字字符串：列表返回数字，详情常返回字符串；其他类型整条视为响应错误。
type afdianCount int64

func (c *afdianCount) UnmarshalJSON(b []byte) error {
	raw := string(b)
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	if !digits(raw) {
		return errors.New("afdian count")
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	*c = afdianCount(v)
	return nil
}

type afdianCreator struct {
	Detail *string `json:"detail"`
}
type afdianUser struct {
	UserID   *string        `json:"user_id"`
	Name     *string        `json:"name"`
	Avatar   *string        `json:"avatar"`
	Cover    *string        `json:"cover"`
	Slug     *string        `json:"url_slug"`
	Verified *afdianCount   `json:"is_verified"`
	Creator  *afdianCreator `json:"creator"`
}
type afdianPost struct {
	PostID     *string      `json:"post_id"`
	Title      *string      `json:"title"`
	Content    *string      `json:"content"`
	Type       *afdianCount `json:"type"`
	Pics       *[]string    `json:"pics"`
	Video      *string      `json:"video"`
	VideoThumb *string      `json:"video_thumb"`
	HasRight   *afdianCount `json:"has_right"`
	RightNote  *string      `json:"has_right_errMsg"`
	MinPrice   *string      `json:"min_price"`
	Likes      *afdianCount `json:"like_count"`
	Comments   *afdianCount `json:"comment_count"`
	Published  *afdianCount `json:"publish_time"`
	User       *afdianUser  `json:"user"`
}
type afdianAlbum struct {
	AlbumID   *string      `json:"album_id"`
	Title     *string      `json:"title"`
	Content   *string      `json:"content"`
	Cover     *string      `json:"cover"`
	PostCount *afdianCount `json:"post_count"`
	OrderBy   *string      `json:"order_by"`
	User      *afdianUser  `json:"user"`
}

func (f *Fetcher) fetchAfdian(ctx context.Context, l Link) (Result, error) {
	switch l.Kind {
	case "creator":
		return f.fetchAfdianCreator(ctx, l)
	case "album":
		return f.fetchAfdianAlbum(ctx, l)
	case "post", "album_post":
		return f.fetchAfdianPost(ctx, l)
	}
	// 商品没有已验证的匿名详情接口；列表摘要不冒充单个商品。
	return Result{}, &FetchError{Kind: "unsupported", Source: "afdian_item", Reason: "anonymous_detail_not_found"}
}

// afdianStatus 按业务码判断：爱发电的 HTTP 状态恒为 200，错误只在 ec 里。
func afdianStatus(ec *int, source string) error {
	if ec == nil {
		return failure("invalid_response", source)
	}
	if *ec == 200 {
		return nil
	}
	kind := "interface_error"
	switch *ec {
	case 400, 404:
		kind = "not_found"
	case 401, 403:
		kind = "login_required"
	case 429:
		kind = "rate_limited"
	}
	return &FetchError{Kind: kind, Code: *ec, Source: source}
}

func (f *Fetcher) fetchAfdianCreator(ctx context.Context, l Link) (Result, error) {
	const source = "afdian_profile"
	var p struct {
		EC   *int `json:"ec"`
		Data struct {
			User *afdianUser `json:"user"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://afdian.com/api/user/get-profile-by-slug?url_slug="+url.QueryEscape(l.ID), source, &p); err != nil {
		return Result{}, err
	}
	if err := afdianStatus(p.EC, source); err != nil {
		return Result{}, err
	}
	u := p.Data.User
	if u == nil {
		return Result{}, failure("not_returned", source)
	}
	if u.Slug == nil || !strings.EqualFold(*u.Slug, l.ID) {
		return Result{}, failure("identity_mismatch", source)
	}
	if u.Name == nil {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = afdianString(u.Name)
	r.Author = afdianAuthor(u)
	if u.Creator != nil && u.Creator.Detail != nil && strings.TrimSpace(*u.Creator.Detail) != "" {
		r.Text = previewField(u.Creator.Detail)
	} else {
		r.Text = Field[string]{State: "not_returned", Reason: "empty_content"}
	}
	if u.Verified != nil {
		r.Flags["verified"] = available(jsonValue(*u.Verified == 1))
	}
	media := []Media{}
	if cover := afdianString(u.Cover); cover.State == "available" {
		media = append(media, imageMetadata(cover))
	}
	afdianSetMedia(&r, media)
	appendBody(&r)
	return r, nil
}

func (f *Fetcher) fetchAfdianAlbum(ctx context.Context, l Link) (Result, error) {
	const source = "afdian_album"
	var p struct {
		EC   *int `json:"ec"`
		Data struct {
			Album *afdianAlbum `json:"album"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://afdian.com/api/user/get-album-info?album_id="+url.QueryEscape(l.ID), source, &p); err != nil {
		return Result{}, err
	}
	if err := afdianStatus(p.EC, source); err != nil {
		return Result{}, err
	}
	a := p.Data.Album
	if a == nil {
		return Result{}, failure("not_returned", source)
	}
	if a.AlbumID == nil || !strings.EqualFold(*a.AlbumID, l.ID) {
		return Result{}, failure("identity_mismatch", source)
	}
	if a.Title == nil {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = afdianString(a.Title)
	r.Author = afdianAuthor(a.User)
	r.Text = afdianBody(a.Content, false)
	stat(&r, "posts", afdianInt(a.PostCount))
	media := []Media{}
	if cover := afdianString(a.Cover); cover.State == "available" {
		media = append(media, imageMetadata(cover))
	}
	afdianSetMedia(&r, media)
	appendBody(&r)
	// 相册列表失败只标在 Links 上，不拖垮相册主体。
	r.Links = f.afdianAlbumLinks(ctx, l.ID, a)
	return r, nil
}

// afdianAlbumLinks 只取第一页；has_more 不是 0 或缺失时标 Truncated，不声称完整。
func (f *Fetcher) afdianAlbumLinks(ctx context.Context, albumID string, a *afdianAlbum) Field[[]ExternalLink] {
	const source = "afdian_album_posts"
	order := "asc"
	if a.OrderBy != nil && strings.Contains(strings.ToLower(*a.OrderBy), "desc") {
		order = "desc"
	}
	var p struct {
		EC   *int `json:"ec"`
		Data struct {
			List []struct {
				PostID *string `json:"post_id"`
				Title  *string `json:"title"`
			} `json:"list"`
			HasMore *afdianCount `json:"has_more"`
		} `json:"data"`
	}
	query := url.Values{"album_id": {albumID}, "rankField": {"rank"}, "rankOrder": {order}}
	if err := f.getJSON(ctx, "https://afdian.com/api/user/get-album-post?"+query.Encode(), source, &p); err != nil {
		var fe *FetchError
		kind := "network"
		if errors.As(err, &fe) {
			kind = fe.Kind
		}
		return Field[[]ExternalLink]{State: "failed", Reason: kind}
	}
	if err := afdianStatus(p.EC, source); err != nil {
		var fe *FetchError
		kind := "invalid_response"
		if errors.As(err, &fe) {
			kind = fe.Kind
		}
		return Field[[]ExternalLink]{State: "failed", Reason: kind}
	}
	links := []ExternalLink{}
	for _, item := range p.Data.List {
		if item.PostID == nil || !afdianHex.MatchString(*item.PostID) {
			continue
		}
		id := strings.ToLower(*item.PostID)
		links = append(links, ExternalLink{
			URL:      available("https://afdian.com/album/" + albumID + "/" + id),
			Original: missing[string]("not_applicable"),
			Display:  afdianString(item.Title),
		})
	}
	field := available(links)
	field.Truncated = p.Data.HasMore == nil || *p.Data.HasMore != 0
	return field
}

func (f *Fetcher) fetchAfdianPost(ctx context.Context, l Link) (Result, error) {
	const source = "afdian_post"
	postID, albumID := l.ID, ""
	query := url.Values{}
	if l.Kind == "album_post" {
		albumID, postID, _ = strings.Cut(l.ID, "/")
		query.Set("album_id", albumID)
	}
	query.Set("post_id", postID)
	var p struct {
		EC   *int `json:"ec"`
		Data struct {
			Post *afdianPost `json:"post"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://afdian.com/api/post/get-detail?"+query.Encode(), source, &p); err != nil {
		return Result{}, err
	}
	if err := afdianStatus(p.EC, source); err != nil {
		return Result{}, err
	}
	post := p.Data.Post
	if post == nil {
		return Result{}, failure("not_returned", source)
	}
	if post.PostID == nil || !strings.EqualFold(*post.PostID, postID) {
		return Result{}, failure("identity_mismatch", source)
	}
	if post.HasRight == nil || (*post.HasRight != 0 && *post.HasRight != 1) || post.Title == nil {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = afdianString(post.Title)
	r.Author = afdianAuthor(post.User)
	r.Published = unixField(afdianInt(post.Published))
	stat(&r, "likes", afdianInt(post.Likes))
	stat(&r, "comments", afdianInt(post.Comments))
	if *post.HasRight == 0 {
		// 付费内容不尝试解锁：正文和媒体只标锁定状态，平台的提示原样作为数据保留。
		r.Text = Field[string]{State: "login_required", Reason: "sponsor_required"}
		r.Media = Field[[]Media]{State: "login_required", Reason: "sponsor_required"}
		r.Flags["sponsor_required"] = available(jsonValue(true))
		if post.MinPrice != nil && *post.MinPrice != "" {
			r.Flags["sponsor_min_price"] = available(jsonValue(*post.MinPrice))
		}
		if post.RightNote != nil && *post.RightNote != "" {
			r.Flags["sponsor_notice"] = available(jsonValue(*post.RightNote))
		}
		return r, nil
	}
	r.Text = afdianBody(post.Content, post.Type != nil && *post.Type == 1)
	appendBody(&r)
	media := []Media{}
	if post.Pics != nil {
		for _, u := range *post.Pics {
			if u != "" {
				media = append(media, imageMetadata(available(u)))
			}
		}
	}
	if post.Video != nil && *post.Video != "" {
		media = append(media, Media{
			Type:      "video",
			URL:       available(*post.Video),
			Thumbnail: afdianString(post.VideoThumb),
			Width:     missing[int]("interface_omitted"),
			Height:    missing[int]("interface_omitted"),
			Duration:  missing[float64]("interface_omitted"),
			Size:      missing[int64]("interface_omitted"),
		})
	}
	afdianSetMedia(&r, media)
	return r, nil
}

// afdianSetMedia 先定稿媒体切片再建块，避免后续追加导致块里的指针指向旧数组。
func afdianSetMedia(r *Result, media []Media) {
	r.Media = available(media)
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, Block{Type: r.Media.Value[i].Type, Media: &r.Media.Value[i]})
	}
}

// afdianBody 正文开头 800 字（截取标 truncated）；长文 HTML 先转纯文字，不保留标签和脚本。
func afdianBody(content *string, isHTML bool) Field[string] {
	if content == nil || strings.TrimSpace(*content) == "" {
		return Field[string]{State: "not_returned", Reason: "empty_content"}
	}
	text := *content
	if isHTML {
		text = afdianPlainText(text)
	}
	if strings.TrimSpace(text) == "" {
		return Field[string]{State: "not_returned", Reason: "empty_content"}
	}
	return previewField(&text)
}

// afdianPlainText 只做标签剥离和实体解码，不执行或解释脚本；结果仍是不可信的外部资料。
func afdianPlainText(s string) string {
	s = afdianBlockScript.ReplaceAllString(s, "")
	s = afdianLineBreak.ReplaceAllString(s, "\n")
	s = afdianAnyTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

func afdianString(p *string) Field[string] {
	if p == nil || *p == "" {
		return missing[string]("interface_omitted")
	}
	return available(*p)
}

func afdianInt(p *afdianCount) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

func afdianAuthor(u *afdianUser) Author {
	a := Author{Name: missing[string]("interface_omitted"), Account: missing[string]("interface_omitted"), ID: missing[string]("interface_omitted"), Avatar: missing[string]("interface_omitted")}
	if u == nil {
		return a
	}
	a.Name = afdianString(u.Name)
	a.Account = afdianString(u.Slug)
	a.Avatar = afdianString(u.Avatar)
	if u.UserID != nil && *u.UserID != "" {
		a.ID = available(*u.UserID)
	}
	return a
}
