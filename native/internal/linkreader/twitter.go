package linkreader

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type xAuthor struct {
	Name    *string `json:"name"`
	Account *string `json:"screen_name"`
	ID      *string `json:"id"`
	Avatar  *string `json:"avatar_url"`
	// 下面这些是账号资料（规格-画师原图 第七节，按需判断是不是画师用）：fx 给了才有，字段缺就当没资料
	Description    *string `json:"description"`
	RawDescription *struct {
		Facets *[]xFacet `json:"facets"`
	} `json:"raw_description"`
	Location *string `json:"location"`
	Website  *struct {
		URL *string `json:"url"`
	} `json:"website"`
	Followers    *int64 `json:"followers"`
	MediaCount   *int64 `json:"media_count"`
	Statuses     *int64 `json:"statuses"`
	Verification *struct {
		Verified *bool   `json:"verified"`
		Type     *string `json:"type"`
	} `json:"verification"`
}

// profile 是作者资料：fx 没给简介字段（作者对象很瘦）就是 nil，调用方不去判断；数字没给记 -1。
// 简介里的 t.co 在 fx 的 description 里已经换成真实地址，facets 里的 replacement 一并收进 Links。
func (a xAuthor) profile() *AuthorProfile {
	if a.Description == nil {
		return nil
	}
	p := &AuthorProfile{Bio: *a.Description, Followers: -1, MediaCount: -1, Statuses: -1}
	if a.Location != nil {
		p.Location = *a.Location
	}
	if a.Website != nil && a.Website.URL != nil {
		p.Links = append(p.Links, *a.Website.URL)
	}
	if a.RawDescription != nil && a.RawDescription.Facets != nil {
		for _, f := range *a.RawDescription.Facets {
			if f.Type == "url" && f.Replacement != nil {
				p.Links = append(p.Links, *f.Replacement)
			}
		}
	}
	if a.Followers != nil {
		p.Followers = *a.Followers
	}
	if a.MediaCount != nil {
		p.MediaCount = *a.MediaCount
	}
	if a.Statuses != nil {
		p.Statuses = *a.Statuses
	}
	return p
}

// verification 把 fx 的认证对象换成 none / blue / business / government；没给、或给了不认识的种类都算不知道（卡片上不画）。
// fx 实际给机构金标的种类是 organization（录的真实夹具里都是它），business 照旧也认。
func (a xAuthor) verification() Field[string] {
	v := a.Verification
	if v == nil || v.Verified == nil {
		return missing[string]("interface_omitted")
	}
	if !*v.Verified {
		return available(VerifyNone)
	}
	if v.Type == nil {
		return missing[string]("verification_type_omitted")
	}
	switch *v.Type {
	case "individual", "blue":
		return available(VerifyBlue)
	case "organization", "business":
		return available(VerifyBusiness)
	case "government":
		return available(VerifyGovernment)
	}
	return missing[string]("unknown_verification_type")
}

type xMedia struct {
	Type      string     `json:"type"`
	URL       *string    `json:"url"`
	Thumbnail *string    `json:"thumbnail_url"`
	Width     *int       `json:"width"`
	Height    *int       `json:"height"`
	Duration  *float64   `json:"duration"`
	Size      *int64     `json:"size"`
	Formats   *[]xFormat `json:"formats"`
}
type xFormat struct {
	URL       *string `json:"url"`
	Container string  `json:"container"`
	Bitrate   *int64  `json:"bitrate"`
	Width     *int    `json:"width"`
	Height    *int    `json:"height"`
	Size      *int64  `json:"size"`
}
type xFacet struct {
	Type        string  `json:"type"`
	Original    *string `json:"original"`
	Replacement *string `json:"replacement"`
	Display     *string `json:"display"`
}
type xCard struct {
	URL         *string `json:"url"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Domain      *string `json:"domain"`
	Image       *struct {
		URL    *string `json:"url"`
		Width  *int    `json:"width"`
		Height *int    `json:"height"`
	} `json:"image"`
}

// xPoll 是 fx 的投票对象；字段都可能缺。
type xPoll struct {
	Choices *[]struct {
		Label      *string  `json:"label"`
		Count      *int64   `json:"count"`
		Percentage *float64 `json:"percentage"`
	} `json:"choices"`
	Total  *int64  `json:"total_votes"`
	EndsAt *string `json:"ends_at"`
}
type xPost struct {
	// Type 为 tombstone 时这条帖子不可用（已删除、停用、受保护……），没有正文；Reason 是原因。
	Type    *string `json:"type"`
	Reason  *string `json:"reason"`
	ID      string  `json:"id"`
	Text    *string `json:"text"`
	RawText *struct {
		Facets *[]xFacet `json:"facets"`
	} `json:"raw_text"`
	Card          *xCard          `json:"card"`
	Author        xAuthor         `json:"author"`
	Timestamp     *int64          `json:"created_timestamp"`
	Likes         *int64          `json:"likes"`
	Reposts       *int64          `json:"reposts"`
	Replies       *int64          `json:"replies"`
	Quotes        *int64          `json:"quotes"`
	Views         *int64          `json:"views"`
	Bookmarks     *int64          `json:"bookmarks"`
	Sensitive     *bool           `json:"possibly_sensitive"`
	Note          *bool           `json:"is_note_tweet"`
	CommunityNote json.RawMessage `json:"community_note"`
	Article       json.RawMessage `json:"article"`
	Media         *struct {
		All    json.RawMessage `json:"all"`
		Mosaic *struct {
			Formats *struct {
				JPEG *string `json:"jpeg"`
			} `json:"formats"`
		} `json:"mosaic"`
	} `json:"media"`
	Poll    *xPoll `json:"poll"`
	Quote   *xPost `json:"quote"`
	ReplyTo *struct {
		Status *string `json:"status"`
	} `json:"replying_to"`
}

func (p *xPost) tombstone() (reason string, ok bool) {
	if p == nil || p.Type == nil || *p.Type != "tombstone" {
		return "", false
	}
	reason = "unavailable"
	if p.Reason != nil && *p.Reason != "" {
		reason = *p.Reason
	}
	return reason, true
}

// mapXPoll 把 fx 的投票映射成 PollInfo；没有选项就当没有投票。
func mapXPoll(p *xPoll) *PollInfo {
	if p == nil || p.Choices == nil || len(*p.Choices) == 0 {
		return nil
	}
	poll := &PollInfo{Total: int64Field(p.Total), EndsAt: missing[time.Time]("interface_omitted")}
	for _, c := range *p.Choices {
		label := ""
		if c.Label != nil {
			label = *c.Label
		}
		choice := PollChoice{Label: label, Count: int64Field(c.Count), Percent: missing[float64]("interface_omitted")}
		if c.Percentage != nil {
			choice.Percent = available(*c.Percentage)
		}
		poll.Choices = append(poll.Choices, choice)
	}
	if t, err := timestampField(p.EndsAt); err == nil {
		poll.EndsAt = t
	} else {
		poll.EndsAt = missing[time.Time]("invalid_time")
	}
	return poll
}

func stat(r *Result, key string, p *int64) {
	if p == nil {
		r.Stats[key] = missing[int64]("interface_omitted")
	} else {
		r.Stats[key] = available(*p)
	}
}
func flag(r *Result, key string, p *bool) {
	if p == nil {
		r.Flags[key] = missing[jsonValue]("interface_omitted")
	} else {
		r.Flags[key] = available[jsonValue](*p)
	}
}
func rawFlag(r *Result, key string, v json.RawMessage) {
	if len(v) == 0 || string(v) == "null" {
		r.Flags[key] = missing[jsonValue]("interface_omitted")
		return
	}
	var x any
	if json.Unmarshal(v, &x) == nil {
		r.Flags[key] = available[jsonValue](x)
	}
}

// fetchFX 是 fx_v2 这一条线路：只取这一路，不退到别的线路（并发和退路在 twitter_routes.go）。
func (f *Fetcher) fetchFX(ctx context.Context, l Link) (Result, error) {
	var envelope struct {
		Code   *int   `json:"code"`
		Status *xPost `json:"status"`
	}
	// fx_v2 单独的时限（linkReader.x.fxTimeoutSeconds）只管主请求；后面补引用、补文章推文用外层的时限。
	main, cancel := context.WithTimeout(ctx, f.xFXTimeout())
	err := f.getJSON(main, "https://api.fxtwitter.com/2/status/"+l.ID, "fx_v2", &envelope)
	cancel()
	if err != nil {
		return Result{}, err
	}
	if envelope.Code == nil {
		return Result{}, failure("invalid_response", "fx_v2")
	}
	if *envelope.Code != 200 {
		return Result{}, xBusiness(*envelope.Code, "fx_v2")
	}
	if reason, gone := envelope.Status.tombstone(); gone {
		// 帖子本身不可用：按原因报，不当成功回空结果。
		kind := "not_found"
		if reason == "private" || reason == "blocked" {
			kind = "login_required"
		}
		return Result{}, &FetchError{Kind: kind, Source: "fx_v2", Reason: "tombstone_" + reason}
	}
	if envelope.Status == nil || envelope.Status.ID != l.ID {
		return Result{}, failure("identity_mismatch", "fx_v2")
	}
	r, err := mapX(l, envelope.Status, 0, map[string]bool{})
	if err == nil {
		f.fillArticleTweets(ctx, &r)
		f.fillQuoteDepth(ctx, &r)
	}
	return r, err
}

// fillQuoteDepth 补“引用里还有引用”：fx 的 status 只带一层引用，被引的那条自己有没有再引用别人，
// 要单独取它一次才知道。只多取这一次（不再往下追）；取不到记成“没查到”，不当成“没有更深引用”。
func (f *Fetcher) fillQuoteDepth(ctx context.Context, r *Result) {
	q := r.Quote.Value
	if r.Quote.State != "available" || q == nil || q.MoreQuotes.Value || q.Quote.State == "available" {
		return
	}
	var envelope struct {
		Code   *int   `json:"code"`
		Status *xPost `json:"status"`
	}
	if err := f.getJSON(ctx, "https://api.fxtwitter.com/2/status/"+q.ID, "fx_v2", &envelope); err != nil ||
		envelope.Code == nil || *envelope.Code != 200 || envelope.Status == nil || envelope.Status.ID != q.ID {
		q.MoreQuotes = missing[bool]("quote_depth_not_returned")
		return
	}
	if envelope.Status.Quote != nil {
		q.MoreQuotes = available(true)
		q.Quote = missing[*Result]("deeper_quote_omitted")
	}
}
func xBusiness(code int, source string) error {
	kind := "interface_error"
	switch code {
	case 404:
		kind = "not_found"
	case 401, 403:
		kind = "login_required"
	case 429:
		kind = "rate_limited"
	}
	return &FetchError{Kind: kind, Source: source, Code: code}
}
func mapX(l Link, p *xPost, depth int, seen map[string]bool) (Result, error) {
	if p.ID == "" || p.Text == nil {
		return Result{}, failure("invalid_response", "fx_v2")
	}
	if seen[p.ID] {
		return Result{}, failure("quote_cycle", "fx_v2")
	}
	seen[p.ID] = true
	defer delete(seen, p.ID)
	r := newResult(l, "fx_v2")
	r.ID = p.ID
	r.Author.Name = stringField(p.Author.Name)
	r.Author.Account = stringField(p.Author.Account)
	r.Author.ID = stringField(p.Author.ID)
	r.Author.Avatar = stringField(p.Author.Avatar)
	r.Author.Verification = p.Author.verification()
	r.Author.Profile = p.Author.profile()
	r.Text = stringField(p.Text)
	r.Published = unixField(p.Timestamp)
	r.Blocks = append(r.Blocks, Block{Type: "text", Text: *p.Text})
	if poll := mapXPoll(p.Poll); poll != nil {
		r.Poll = poll
		r.Blocks = append(r.Blocks, Block{Type: "poll"})
	}
	stat(&r, "likes", p.Likes)
	stat(&r, "reposts", p.Reposts)
	stat(&r, "replies", p.Replies)
	stat(&r, "quotes", p.Quotes)
	stat(&r, "views", p.Views)
	stat(&r, "bookmarks", p.Bookmarks)
	flag(&r, "possibly_sensitive", p.Sensitive)
	flag(&r, "is_note_tweet", p.Note)
	rawFlag(&r, "community_note", p.CommunityNote)
	r.Flags["ai_generated"] = missing[jsonValue]("interface_omitted")
	if p.ReplyTo != nil {
		r.ReplyTo = stringField(p.ReplyTo.Status)
	}
	if p.RawText != nil && p.RawText.Facets != nil {
		links := make([]ExternalLink, 0)
		for _, facet := range *p.RawText.Facets {
			if facet.Type != "url" {
				continue
			}
			media := false
			for _, other := range *p.RawText.Facets {
				if other.Type == "media" && other.Original != nil && facet.Original != nil && *other.Original == *facet.Original {
					media = true
					break
				}
			}
			if !media {
				links = append(links, ExternalLink{URL: stringField(facet.Replacement), Original: stringField(facet.Original), Display: stringField(facet.Display)})
			}
		}
		r.Links = available(links)
	}
	if p.Card != nil && p.Card.URL != nil && *p.Card.URL != "" && !bskyWebURL(*p.Card.URL) {
		// 外链卡的地址只认 http、https（照 Bluesky）；别的协议不出卡，也不拿图当封面。
		r.LinkCard = Field[LinkPreview]{State: "failed", Reason: "unsafe_target"}
	} else if p.Card != nil {
		c := p.Card
		card := LinkPreview{URL: stringField(c.URL), Title: stringField(c.Title), Description: stringField(c.Description), Site: stringField(c.Domain), Image: missing[Media]("interface_omitted")}
		if c.Image != nil {
			card.Image = available(xImage(c.Image.URL, c.Image.Width, c.Image.Height))
		}
		r.LinkCard = available(card)
	}
	// FX 明确的空 media 对象表示没有附件；all:null 仍是未返回清单。
	if p.Media != nil && len(p.Media.All) == 0 {
		r.Media = available([]Media{})
	}
	if p.Media != nil && len(p.Media.All) > 0 && string(p.Media.All) != "null" {
		var all []xMedia
		if json.Unmarshal(p.Media.All, &all) != nil {
			return Result{}, failure("invalid_response", "fx_v2")
		}
		media := make([]Media, 0, len(all))
		for _, m := range all {
			if m.Type != "photo" && m.Type != "image" && m.Type != "video" && m.Type != "gif" {
				return Result{}, failure("invalid_response", "fx_v2")
			}
			item := mapXMedia(m)
			media = append(media, item)
			r.Blocks = append(r.Blocks, Block{Type: item.Type, Media: &media[len(media)-1]})
		}
		r.Media = available(media)
	}
	// 三张及以上图时 fx 会给一张拼好的 jpeg；只认 jpeg（QQ 一定能收），主机在 inspectMedia 里检查。
	if p.Media != nil && p.Media.Mosaic != nil && p.Media.Mosaic.Formats != nil && p.Media.Mosaic.Formats.JPEG != nil && *p.Media.Mosaic.Formats.JPEG != "" {
		r.Mosaic = available(xImage(p.Media.Mosaic.Formats.JPEG, nil, nil))
	}
	r.MoreQuotes = available(false)
	if reason, gone := p.Quote.tombstone(); gone {
		// 被引的帖子已删或不可见：写明原因，不静默丢掉引用。
		r.Quote = Field[*Result]{State: "not_returned", Reason: "quote_" + reason}
		r.Blocks = append(r.Blocks, Block{Type: "quote_unavailable", Reason: reason})
	} else if p.Quote != nil {
		if depth >= 1 {
			r.MoreQuotes = available(true)
			r.Quote = missing[*Result]("deeper_quote_omitted")
		} else {
			ql := Link{Platform: "x", Kind: "post", ID: p.Quote.ID, Clean: xStatusURL(p.Quote.Author.Account, p.Quote.ID)}
			q, err := mapX(ql, p.Quote, depth+1, seen)
			if err != nil {
				r.Quote = Field[*Result]{State: "failed", Reason: err.(*FetchError).Kind}
			} else {
				r.Quote = available(&q)
				r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: &q})
			}
		}
	}
	if len(p.Article) > 0 && string(p.Article) != "null" {
		if err := mapXArticle(&r, p.Article, depth); err != nil {
			return Result{}, err
		}
	}
	return r, nil
}
func originalImage(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.Hostname() == "pbs.twimg.com" {
		q := u.Query()
		q.Set("name", "orig")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return raw
}

func int64Field(p *int64) Field[int64] {
	if p == nil {
		return missing[int64]("interface_omitted")
	}
	return available(*p)
}
func xImage(raw *string, width, height *int) Media {
	return Media{Type: "image", URL: stringField(raw), Width: intField(width), Height: intField(height), Thumbnail: missing[string]("not_applicable"), Duration: missing[float64]("not_applicable"), Size: missing[int64]("interface_omitted"), Variants: missing[[]MediaVariant]("not_applicable")}
}
func mapXMedia(m xMedia) Media {
	item := Media{Type: m.Type, URL: stringField(m.URL), Thumbnail: stringField(m.Thumbnail), Width: intField(m.Width), Height: intField(m.Height), Duration: floatField(m.Duration), Size: int64Field(m.Size), Variants: missing[[]MediaVariant]("interface_omitted")}
	if item.Type == "photo" {
		item.Type = "image"
	}
	if item.Type == "image" && item.URL.State == "available" {
		item.URL.Value = originalImage(item.URL.Value)
	}
	if m.Formats != nil {
		variants := make([]MediaVariant, 0, len(*m.Formats))
		best := int64(-1)
		for _, v := range *m.Formats {
			variants = append(variants, MediaVariant{URL: stringField(v.URL), Container: v.Container, Bitrate: int64Field(v.Bitrate), Width: intField(v.Width), Height: intField(v.Height), Size: int64Field(v.Size)})
			// CDN 路径声明的是编码变体尺寸，不拿原视频尺寸充当每档清晰度。
			if v.URL != nil && v.Width == nil && v.Height == nil {
				for part := range strings.SplitSeq(*v.URL, "/") {
					w, h, ok := strings.Cut(part, "x")
					if ok && digits(w) && digits(h) {
						width, e1 := strconv.Atoi(w)
						height, e2 := strconv.Atoi(h)
						if e1 == nil && e2 == nil {
							variants[len(variants)-1].Width = available(width)
							variants[len(variants)-1].Height = available(height)
						}
					}
				}
			}
			if v.Container == "mp4" && v.URL != nil && *v.URL != "" && v.Bitrate != nil && *v.Bitrate > best {
				item.URL, item.Size, best = available(*v.URL), int64Field(v.Size), *v.Bitrate
			}
		}
		item.Variants = available(variants)
	}
	return item
}

// 数字标识允许平台 JSON 的整数或字符串；不经过 float64。
type vxPost struct {
	ID        string  `json:"tweetID"`
	Text      *string `json:"text"`
	Name      *string `json:"user_name"`
	Account   *string `json:"user_screen_name"`
	Avatar    *string `json:"user_profile_image_url"`
	Timestamp *int64  `json:"date_epoch"`
	Likes     *int64  `json:"likes"`
	Reposts   *int64  `json:"retweets"`
	Replies   *int64  `json:"replies"`
	Sensitive *bool   `json:"possibly_sensitive"`
	Quote     *vxPost `json:"qrt"`
	PollData  *struct {
		Options *[]struct {
			Name    *string  `json:"name"`
			Percent *float64 `json:"percent"`
			Votes   *int64   `json:"votes"`
		} `json:"options"`
	} `json:"pollData"`
	Reply   *string         `json:"replyingToID"`
	Article json.RawMessage `json:"article"`
	Media   *[]struct {
		Type      string   `json:"type"`
		URL       *string  `json:"url"`
		Thumbnail *string  `json:"thumbnail_url"`
		Duration  *float64 `json:"duration_millis"`
		Size      struct {
			Width  *int `json:"width"`
			Height *int `json:"height"`
		} `json:"size"`
	} `json:"media_extended"`
}

func (f *Fetcher) fetchVX(ctx context.Context, l Link) (Result, error) {
	var p vxPost
	u, _ := url.Parse(l.Clean)
	account := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")[0]
	if err := f.getJSON(ctx, "https://api.vxtwitter.com/"+url.PathEscape(account)+"/status/"+l.ID, "vx", &p); err != nil {
		return Result{}, err
	}
	if p.ID != l.ID {
		return Result{}, failure("identity_mismatch", "vx")
	}
	return mapVX(l, &p, 0, map[string]bool{})
}
func mapVX(l Link, p *vxPost, depth int, seen map[string]bool) (Result, error) {
	if p.Text == nil || p.ID == "" {
		return Result{}, failure("invalid_response", "vx")
	}
	if seen[p.ID] {
		return Result{}, failure("quote_cycle", "vx")
	}
	seen[p.ID] = true
	defer delete(seen, p.ID)
	r := newResult(l, "vx")
	r.ID = p.ID
	r.Author.Name = stringField(p.Name)
	r.Author.Account = stringField(p.Account)
	r.Author.Avatar = stringField(p.Avatar)
	r.Published = unixField(p.Timestamp)
	r.Text = stringField(p.Text)
	r.Blocks = append(r.Blocks, Block{Type: "text", Text: *p.Text})
	if p.PollData != nil && p.PollData.Options != nil && len(*p.PollData.Options) > 0 {
		// VX 只给选项、票数和百分比：总票数是各项相加，结束时间没有，不写“已结束”。
		poll := &PollInfo{Total: missing[int64]("interface_omitted"), EndsAt: missing[time.Time]("interface_omitted")}
		var sum int64
		known := true
		for _, o := range *p.PollData.Options {
			label := ""
			if o.Name != nil {
				label = *o.Name
			}
			choice := PollChoice{Label: label, Count: int64Field(o.Votes), Percent: missing[float64]("interface_omitted")}
			if o.Percent != nil {
				choice.Percent = available(*o.Percent)
			}
			if o.Votes == nil {
				known = false
			} else {
				sum += *o.Votes
			}
			poll.Choices = append(poll.Choices, choice)
		}
		if known {
			poll.Total = available(sum)
		}
		r.Poll = poll
		r.Blocks = append(r.Blocks, Block{Type: "poll"})
	}
	stat(&r, "likes", p.Likes)
	stat(&r, "reposts", p.Reposts)
	stat(&r, "replies", p.Replies)
	stat(&r, "views", nil)
	stat(&r, "quotes", nil)
	stat(&r, "bookmarks", nil)
	flag(&r, "possibly_sensitive", p.Sensitive)
	r.Flags["ai_generated"] = missing[jsonValue]("interface_omitted")
	r.Flags["community_note"] = missing[jsonValue]("interface_omitted")
	r.ReplyTo = stringField(p.Reply)
	if len(p.Article) > 0 && string(p.Article) != "null" {
		r.Article = missing[string]("vx_article_body_omitted")
	}
	if p.Media != nil {
		items := make([]Media, 0, len(*p.Media))
		for _, m := range *p.Media {
			if m.Type != "image" && m.Type != "video" && m.Type != "gif" {
				return Result{}, failure("invalid_response", "vx")
			}
			duration := missing[float64]("interface_omitted")
			if m.Duration != nil {
				duration = available(*m.Duration / 1000)
			}
			item := Media{Type: m.Type, URL: stringField(m.URL), Thumbnail: stringField(m.Thumbnail), Width: intField(m.Size.Width), Height: intField(m.Size.Height), Duration: duration, Size: missing[int64]("interface_omitted"), Variants: missing[[]MediaVariant]("vx_omits_variants")}
			if item.Type == "image" && item.URL.State == "available" {
				item.URL.Value = originalImage(item.URL.Value)
			}
			items = append(items, item)
			r.Blocks = append(r.Blocks, Block{Type: item.Type, Media: &items[len(items)-1]})
		}
		r.Media = available(items)
	}
	r.MoreQuotes = available(false)
	if p.Quote != nil {
		if depth >= 1 {
			r.MoreQuotes = available(true)
			r.Quote = missing[*Result]("deeper_quote_omitted")
		} else {
			ql := Link{Platform: "x", Kind: "post", ID: p.Quote.ID, Clean: xStatusURL(p.Quote.Account, p.Quote.ID)}
			q, err := mapVX(ql, p.Quote, depth+1, seen)
			if err != nil {
				r.Quote = Field[*Result]{State: "failed", Reason: err.(*FetchError).Kind}
			} else {
				r.Quote = available(&q)
				r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: &q})
			}
		}
	}
	return r, nil
}

// 数字标识允许平台 JSON 的整数或字符串；不经过 float64。
type decimalID string

func (d *decimalID) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	if !digits(s) {
		return strconv.ErrSyntax
	}
	*d = decimalID(s)
	return nil
}
