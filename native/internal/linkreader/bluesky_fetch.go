package linkreader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bluesky 取数走公开 AppView，不发 Cookie、不登录、不解析 PDS。
const bskySource = "bluesky_appview"
const bskyAPI = "https://public.api.bsky.app/xrpc/"

type bskyAuthor struct {
	DID         string  `json:"did"`
	Handle      string  `json:"handle"`
	DisplayName *string `json:"displayName"`
	Avatar      *string `json:"avatar"`
}
type bskyLabel struct {
	Val string `json:"val"`
}
type bskyAspect struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}
type bskyImage struct {
	Thumb     *string     `json:"thumb"`
	Thumbnail *string     `json:"thumbnail"`
	Fullsize  *string     `json:"fullsize"`
	Aspect    *bskyAspect `json:"aspectRatio"`
}
type bskyExternal struct {
	URI         string  `json:"uri"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Thumb       *string `json:"thumb"`
}

// bskyEmbed 是嵌入的 view 形态；record 与 media 只在 recordWithMedia 时各有一层。
type bskyEmbed struct {
	Type      string          `json:"$type"`
	Images    []bskyImage     `json:"images"`
	Items     []bskyImage     `json:"items"`
	Playlist  *string         `json:"playlist"`
	Thumbnail *string         `json:"thumbnail"`
	Aspect    *bskyAspect     `json:"aspectRatio"`
	External  *bskyExternal   `json:"external"`
	Record    json.RawMessage `json:"record"`
	Media     json.RawMessage `json:"media"`
}
type bskyFacet struct {
	Index struct {
		Start int `json:"byteStart"`
		End   int `json:"byteEnd"`
	} `json:"index"`
	Features []struct {
		Type string `json:"$type"`
		URI  string `json:"uri"`
	} `json:"features"`
}
type bskyRecord struct {
	Text      string          `json:"text"`
	CreatedAt *string         `json:"createdAt"`
	Facets    []bskyFacet     `json:"facets"`
	Tags      []string        `json:"tags"`
	Labels    json.RawMessage `json:"labels"`
	Reply     *struct {
		Parent struct {
			URI string `json:"uri"`
		} `json:"parent"`
	} `json:"reply"`
}

// bskyPostView 同时覆盖主帖的 postView（record、embed）和被引用的 viewRecord（value、embeds）。
type bskyPostView struct {
	URI     string          `json:"uri"`
	Author  *bskyAuthor     `json:"author"`
	Record  json.RawMessage `json:"record"`
	Value   json.RawMessage `json:"value"`
	Embed   *bskyEmbed      `json:"embed"`
	Embeds  []bskyEmbed     `json:"embeds"`
	Labels  []bskyLabel     `json:"labels"`
	Likes   *int64          `json:"likeCount"`
	Reposts *int64          `json:"repostCount"`
	Replies *int64          `json:"replyCount"`
	Quotes  *int64          `json:"quoteCount"`
}

func (v bskyPostView) recordRaw() json.RawMessage {
	if len(v.Record) > 0 {
		return v.Record
	}
	return v.Value
}

type bskyProfile struct {
	DID         string  `json:"did"`
	Handle      string  `json:"handle"`
	DisplayName *string `json:"displayName"`
	Description *string `json:"description"`
	Avatar      *string `json:"avatar"`
	Followers   *int64  `json:"followersCount"`
	Follows     *int64  `json:"followsCount"`
	Posts       *int64  `json:"postsCount"`
}

func (f *Fetcher) fetchBluesky(ctx context.Context, l Link) (Result, error) {
	if l.Kind == "profile" {
		return f.fetchBlueskyProfile(ctx, l)
	}
	actor, rkey, ok := strings.Cut(l.ID, "/")
	if !ok {
		return Result{}, failure("invalid_target", bskySource)
	}
	endpoint := bskyAPI + "app.bsky.feed.getPostThread?uri=" + url.QueryEscape("at://"+actor+"/app.bsky.feed.post/"+rkey) + "&depth=0&parentHeight=0"
	var p struct {
		Thread struct {
			Type string        `json:"$type"`
			Post *bskyPostView `json:"post"`
		} `json:"thread"`
	}
	if err := f.bskyGet(ctx, endpoint, &p); err != nil {
		return Result{}, err
	}
	switch p.Thread.Type {
	case "app.bsky.feed.defs#threadViewPost":
	case "app.bsky.feed.defs#notFoundPost":
		return Result{}, &FetchError{Kind: "not_found", Source: bskySource}
	case "app.bsky.feed.defs#blockedPost":
		return Result{}, &FetchError{Kind: "not_found", Source: bskySource, Reason: "blocked_post"}
	default:
		return Result{}, failure("invalid_response", bskySource)
	}
	post := p.Thread.Post
	if post == nil || post.Author == nil {
		return Result{}, failure("invalid_response", bskySource)
	}
	if !bskyPostMatches(*post, actor, rkey) {
		return Result{}, failure("identity_mismatch", bskySource)
	}
	return bskyPost(l, *post, 0)
}

// bskyPostMatches 要求 URI 的 DID 与作者 DID 一致；请求用 handle 时才退回到大小写不敏感的 handle 比较。
func bskyPostMatches(v bskyPostView, actor, rkey string) bool {
	parts := strings.Split(strings.TrimPrefix(v.URI, "at://"), "/")
	if len(parts) != 3 || parts[0] != v.Author.DID || parts[1] != "app.bsky.feed.post" || parts[2] != rkey {
		return false
	}
	if strings.HasPrefix(actor, "did:") {
		return v.Author.DID == actor
	}
	return strings.EqualFold(v.Author.Handle, actor)
}

func (f *Fetcher) fetchBlueskyProfile(ctx context.Context, l Link) (Result, error) {
	var p bskyProfile
	if err := f.bskyGet(ctx, bskyAPI+"app.bsky.actor.getProfile?actor="+url.QueryEscape(l.ID), &p); err != nil {
		return Result{}, err
	}
	if p.DID == "" || (p.DID != l.ID && !strings.EqualFold(p.Handle, l.ID)) {
		return Result{}, failure("identity_mismatch", bskySource)
	}
	r := newResult(l, bskySource)
	r.Author = Author{Name: bskyDisplayName(p.DisplayName), Account: available(p.Handle), ID: available(p.DID), Avatar: stringField(p.Avatar)}
	r.Title = available(p.Handle)
	if n := bskyDisplayName(p.DisplayName); n.State == "available" {
		r.Title = n
	}
	r.Text = bskyText(p.Description)
	stat(&r, "followers", p.Followers)
	stat(&r, "follows", p.Follows)
	stat(&r, "posts", p.Posts)
	r.Media = available([]Media{})
	appendBody(&r)
	return r, nil
}

// bskyGet 处理 Bluesky 特有的 400 NotFound；其余状态沿用通用映射。
func (f *Fetcher) bskyGet(ctx context.Context, endpoint string, out any) error {
	b, status, h, err := f.request(ctx, endpoint, bskySource)
	if err != nil {
		return err
	}
	var e struct {
		Error string `json:"error"`
	}
	if status == 400 && json.Unmarshal(b, &e) == nil && e.Error == "NotFound" {
		return &FetchError{Kind: "not_found", Source: bskySource, Status: status}
	}
	if err := httpFailure(status, h, bskySource); err != nil {
		// RateLimit-Reset 是 Unix 时间戳，换成 HTTP 日期只作展示；冷却仍由 request 读 Retry-After 决定。
		if fe, ok := err.(*FetchError); ok && fe.Kind == "rate_limited" && fe.RetryAfter == "" {
			if reset, perr := strconv.ParseInt(strings.TrimSpace(h.Get("RateLimit-Reset")), 10, 64); perr == nil {
				fe.RetryAfter = time.Unix(reset, 0).UTC().Format(http.TimeFormat)
			}
		}
		return err
	}
	if json.Unmarshal(b, out) != nil {
		return failure("invalid_response", bskySource)
	}
	return nil
}

// bskyPost 映射一条帖子；depth 为 0 表示主帖，1 表示被引用的帖子（它再引用的记录不展开）。
func bskyPost(l Link, v bskyPostView, depth int) (Result, error) {
	r := newResult(l, bskySource)
	r.Author = Author{Name: bskyDisplayName(v.Author.DisplayName), Account: available(v.Author.Handle), ID: available(v.Author.DID), Avatar: stringField(v.Author.Avatar)}
	if v.Author.DID == "" {
		r.Author.ID = missing[string]("not_returned")
	}
	var rec bskyRecord
	if len(v.recordRaw()) == 0 || json.Unmarshal(v.recordRaw(), &rec) != nil {
		return Result{}, failure("invalid_response", bskySource)
	}
	var err error
	r.Published, err = timestampField(rec.CreatedAt)
	if err != nil {
		return Result{}, err
	}
	body := bskyExpandLinks(rec)
	r.Text = bskyText(&body)
	r.Title = missing[string]("empty_text")
	if line := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0]); line != "" {
		r.Title = available(bskyHead(line, 72))
	}
	stat(&r, "likes", v.Likes)
	stat(&r, "reposts", v.Reposts)
	stat(&r, "replies", v.Replies)
	stat(&r, "quotes", v.Quotes)
	sensitive := bskySensitive(v.Labels, rec.Labels)
	flag(&r, "possibly_sensitive", &sensitive)
	if rec.Tags != nil {
		r.Tags = available(rec.Tags)
	}
	r.Links = available(bskyLinks(rec))
	if replyURL, ok := bskyReplyURL(rec); ok {
		r.ReplyTo = available(replyURL)
	}
	r.Media = available([]Media{})
	appendBody(&r)
	if v.Embed != nil {
		if err := bskyEmbedInto(&r, v.Embed, depth); err != nil {
			return Result{}, err
		}
	} else if depth > 0 && len(v.Embeds) > 0 {
		if err := bskyEmbedInto(&r, &v.Embeds[0], depth); err != nil {
			return Result{}, err
		}
	}
	return r, nil
}

func bskyEmbedInto(r *Result, e *bskyEmbed, depth int) error {
	switch e.Type {
	case "app.bsky.embed.images#view":
		bskyImages(r, e.Images)
	case "app.bsky.embed.gallery#view":
		bskyImages(r, e.Items)
	case "app.bsky.embed.video#view":
		bskyVideo(r, e)
	case "app.bsky.embed.external#view":
		bskyLinkCard(r, e.External)
	case "app.bsky.embed.record#view":
		return bskyQuote(r, e.Record, depth)
	case "app.bsky.embed.recordWithMedia#view":
		if err := bskyQuote(r, e.Record, depth); err != nil {
			return err
		}
		var m bskyEmbed
		if json.Unmarshal(e.Media, &m) != nil {
			return failure("invalid_response", bskySource)
		}
		return bskyEmbedInto(r, &m, depth)
	default:
		r.Media = Field[[]Media]{State: "unsupported", Reason: "unsupported_embed"}
	}
	return nil
}

func bskyImages(r *Result, items []bskyImage) {
	for _, it := range items {
		thumb := it.Thumbnail
		if thumb == nil {
			thumb = it.Thumb
		}
		w, h := bskyDims(it.Aspect)
		appendMedia(r, Media{Type: "image", URL: stringField(it.Fullsize), Thumbnail: stringField(thumb), Width: w, Height: h, Duration: missing[float64]("not_applicable"), Size: missing[int64]("interface_omitted")})
	}
}

// 视频只给 HLS 主清单，不给 mp4；Duration 和 Size 在嵌入视图里没有，显式标 not_returned。
func bskyVideo(r *Result, e *bskyEmbed) {
	if e.Playlist == nil || *e.Playlist == "" {
		r.Media = missing[[]Media]("no_playlist")
		return
	}
	w, h := bskyDims(e.Aspect)
	appendMedia(r, Media{Type: "video", URL: available(*e.Playlist), Thumbnail: stringField(e.Thumbnail), Width: w, Height: h,
		Duration: missing[float64]("not_returned"), Size: missing[int64]("not_returned"),
		Variants: available([]MediaVariant{{URL: available(*e.Playlist), Container: "hls", Bitrate: missing[int64]("not_returned"), Width: w, Height: h, Size: missing[int64]("not_returned")}})})
}

func bskyLinkCard(r *Result, x *bskyExternal) {
	if x == nil || x.URI == "" {
		return
	}
	if !bskyWebURL(x.URI) {
		r.LinkCard = Field[LinkPreview]{State: "failed", Reason: "unsafe_target"}
		return
	}
	site := missing[string]("not_returned")
	if u, err := url.Parse(x.URI); err == nil && u.Hostname() != "" {
		site = available(strings.ToLower(u.Hostname()))
	}
	card := LinkPreview{URL: available(x.URI), Title: stringField(x.Title), Description: stringField(x.Description), Site: site, Image: missing[Media]("not_returned")}
	if x.Thumb != nil && *x.Thumb != "" {
		card.Image = available(imageMetadata(available(*x.Thumb)))
	}
	r.LinkCard = available(card)
	appendLinkCardBlock(r)
}

// bskyQuote 只展开一层；更深的引用和 Starter Pack、Feed、List 等记录标 unsupported，不当作空引用。
func bskyQuote(r *Result, raw json.RawMessage, depth int) error {
	if len(raw) == 0 {
		return nil
	}
	if depth > 0 {
		r.Quote = Field[*Result]{State: "unsupported", Reason: "nested_quote_not_expanded"}
		return nil
	}
	v, typ, ok, err := bskyViewRecord(raw)
	if err != nil {
		return err
	}
	if !ok {
		state := "unsupported"
		reason := typ
		switch typ {
		case "app.bsky.embed.record#viewBlocked", "app.bsky.embed.record#viewNotFound", "app.bsky.embed.record#viewDetached":
			state = "failed"
			reason = typ[strings.LastIndex(typ, "#")+1:]
		}
		r.Quote = Field[*Result]{State: state, Reason: reason}
		return nil
	}
	if v.Author == nil {
		return failure("invalid_response", bskySource)
	}
	_, rkey, ok := bskyRKeyOf(v.URI)
	if !ok {
		return failure("invalid_response", bskySource)
	}
	ql := Link{Platform: "bluesky", Kind: "post", ID: v.Author.DID + "/" + rkey, Clean: "https://bsky.app/profile/" + v.Author.DID + "/post/" + rkey}
	q, err := bskyPost(ql, v, 1)
	if err != nil {
		return err
	}
	r.Quote = available(&q)
	r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: &q})
	return nil
}

// bskyViewRecord 穿过 record#view 外壳，直到 viewRecord 或某个不展开的记录类型。
func bskyViewRecord(raw json.RawMessage) (bskyPostView, string, bool, error) {
	var probe struct {
		Type   string          `json:"$type"`
		Record json.RawMessage `json:"record"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return bskyPostView{}, "", false, failure("invalid_response", bskySource)
	}
	// recordWithMedia 里的外壳对象不带 $type，只有 record 字段；两种写法都穿过去。
	if probe.Type == "" && len(probe.Record) > 0 {
		return bskyViewRecord(probe.Record)
	}
	switch probe.Type {
	case "app.bsky.embed.record#view":
		return bskyViewRecord(probe.Record)
	case "app.bsky.embed.record#viewRecord":
		var v bskyPostView
		if json.Unmarshal(raw, &v) != nil {
			return bskyPostView{}, "", false, failure("invalid_response", bskySource)
		}
		return v, probe.Type, true, nil
	}
	return bskyPostView{}, probe.Type, false, nil
}

func bskyRKeyOf(uri string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(uri, "at://"), "/")
	if len(parts) != 3 || parts[1] != "app.bsky.feed.post" || !bskyRKey.MatchString(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// bskyReplyURL 只把父帖换成网页地址，不取父帖内容；depth=0 时父帖也不取。
func bskyReplyURL(rec bskyRecord) (string, bool) {
	if rec.Reply == nil {
		return "", false
	}
	did, rkey, ok := bskyRKeyOf(rec.Reply.Parent.URI)
	if !ok {
		return "", false
	}
	return "https://bsky.app/profile/" + did + "/post/" + rkey, true
}

// bskyWebURL 只放行 http、https 的绝对地址；链接由发帖人的记录写入，不能原样交给下游。
func bskyWebURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

// bskyLinks 只取链接类 facet；偏移按 UTF-8 字节切片，越界或非法的 facet 直接丢弃，地址不是 http、https 的也丢弃。
func bskyLinks(rec bskyRecord) []ExternalLink {
	out := []ExternalLink{}
	for _, facet := range rec.Facets {
		start, end := facet.Index.Start, facet.Index.End
		if start < 0 || end < start || end > len(rec.Text) || !utf8.ValidString(rec.Text[start:end]) {
			continue
		}
		for _, feat := range facet.Features {
			if feat.Type == "app.bsky.richtext.facet#link" && feat.URI != "" && bskyWebURL(feat.URI) {
				out = append(out, ExternalLink{URL: available(feat.URI), Original: available(rec.Text[start:end]), Display: missing[string]("not_applicable")})
			}
		}
	}
	return out
}

// bskyExpandLinks 把正文里被缩短显示的链接（“github.com/a/b…”）换回 facet 里的完整地址。
// Bluesky 发帖时正文里写的是缩短后的显示文字，真地址只在 facet；不换回来，转发出去的就是半条链接。
// 只换“显示文字去掉省略号和协议头后是地址前缀”的那种；作者给任意文字挂的链接（“点这里”）不动。
func bskyExpandLinks(rec bskyRecord) string {
	type swap struct {
		start, end int
		uri        string
	}
	var swaps []swap
	for _, facet := range rec.Facets {
		start, end := facet.Index.Start, facet.Index.End
		if start < 0 || end <= start || end > len(rec.Text) || !utf8.ValidString(rec.Text[start:end]) {
			continue
		}
		for _, feat := range facet.Features {
			if feat.Type != "app.bsky.richtext.facet#link" || feat.URI == "" || !bskyWebURL(feat.URI) {
				continue
			}
			shown := rec.Text[start:end]
			if shown == feat.URI || !bskyShortOf(shown, feat.URI) {
				continue
			}
			swaps = append(swaps, swap{start, end, feat.URI})
			break
		}
	}
	slices.SortFunc(swaps, func(a, b swap) int { return b.start - a.start })
	text, limit := rec.Text, len(rec.Text)
	for _, s := range swaps {
		if s.end > limit { // 和位置更靠后的替换重叠，跳过
			continue
		}
		text = text[:s.start] + s.uri + text[s.end:]
		limit = s.start
	}
	return text
}

// bskyShortOf 说 shown 是不是 uri 的缩短显示：去掉结尾的省略号、两边去掉协议头和 www. 后，是 uri 的开头部分。
func bskyShortOf(shown, uri string) bool {
	norm := func(s string) string {
		s = strings.ToLower(s)
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		return strings.TrimPrefix(s, "www.")
	}
	shown = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(shown), "…."))
	return shown != "" && strings.HasPrefix(norm(uri), norm(shown))
}

// bskySensitive 读自标签（record.labels）和 AppView 标签（post.labels）；任一命中成人或血腥类即为真。
func bskySensitive(post []bskyLabel, raw json.RawMessage) bool {
	var self struct {
		Values []bskyLabel `json:"values"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &self)
	}
	for _, l := range append(post, self.Values...) {
		switch l.Val {
		case "porn", "sexual", "nudity", "graphic-media":
			return true
		}
	}
	return false
}

func bskyDisplayName(p *string) Field[string] {
	if p == nil || *p == "" {
		return missing[string]("not_returned")
	}
	return available(*p)
}

func bskyText(p *string) Field[string] {
	if p == nil || *p == "" {
		return missing[string]("empty_text")
	}
	return previewField(p)
}

func bskyDims(a *bskyAspect) (Field[int], Field[int]) {
	if a == nil {
		return missing[int]("interface_omitted"), missing[int]("interface_omitted")
	}
	w, h := a.Width, a.Height
	return available(w), available(h)
}

// bskyHead 是标题用的正文开头：超过 n 个字时截断并加“…”；英文这类用空格分词的，退到最近的空格，不截在词中间
// （往回最多退 n/4 个字，找不到空格就照字数截）。
func bskyHead(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := n
	if !unicode.IsSpace(runes[n]) {
		for i := n - 1; i > n-n/4 && i > 0; i-- {
			if unicode.IsSpace(runes[i]) {
				cut = i
				break
			}
		}
	}
	return strings.TrimRightFunc(string(runes[:cut]), unicode.IsSpace) + "…"
}
