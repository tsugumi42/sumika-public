package linkreader

// X 的备用取数路线：官方嵌入接口 cdn.syndication.twimg.com/tweet-result（react-tweet 用的同一个，匿名、不要登录）。
// 只在 fx_v2、vx 都失败时取（twitter_routes.go）。它不给转发数、浏览数、引用数、书签数，这些照“没返回”记，不编。

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

// syndicationFeatures 照 react-tweet 的请求参数；接口按它决定回哪些字段（比如认证标）。
var syndicationFeatures = strings.Join([]string{
	"tfw_timeline_list:", "tfw_follower_count_sunset:true", "tfw_tweet_edit_backend:on", "tfw_refsrc_session:on",
	"tfw_fosnr_soft_interventions_enabled:on", "tfw_show_birdwatch_pivots_enabled:on", "tfw_show_business_verified_badge:on",
	"tfw_duplicate_scribes_to_settings:on", "tfw_use_profile_image_shape_enabled:on", "tfw_show_blue_verified_badge:on",
	"tfw_legacy_timeline_sunset:true", "tfw_show_gov_verified_badge:on", "tfw_show_business_affiliate_badge:on", "tfw_tweet_edit_frontend:on",
}, ";")

// syndicationToken 是接口要的 token：react-tweet 的 ((id / 1e15) * π).toString(36) 去掉所有 0 和小数点。
// toString(36) 照 V8 的 DoubleToRadixCString 逐位算（JS 引擎之间小数位数会差，以 V8 为准，测试里的样例由 node 生成）。
func syndicationToken(id string) string {
	if !digits(id) {
		return ""
	}
	n, err := strconv.ParseFloat(id, 64) // 和 JS 的 Number(id) 一样取最近的双精度数
	if err != nil {
		return ""
	}
	s := jsRadix36(n / 1e15 * math.Pi)
	return strings.Map(func(r rune) rune {
		if r == '0' || r == '.' {
			return -1
		}
		return r
	}, s)
}

// jsRadix36 是 V8 DoubleToRadixCString(value, 36) 的移植（只处理非负有限数，ID 换出来的值都是）。
func jsRadix36(value float64) string {
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	const radix = 36
	integer := math.Floor(value)
	fraction := value - integer
	delta := 0.5 * (math.Nextafter(value, math.Inf(1)) - value)
	delta = math.Max(math.Nextafter(0, 1), delta)
	var frac []byte
	if fraction >= delta {
		frac = append(frac, '.')
		for {
			fraction *= radix
			delta *= radix
			digit := int(fraction)
			frac = append(frac, chars[digit])
			fraction -= float64(digit)
			if fraction > 0.5 || (fraction == 0.5 && digit&1 == 1) {
				if fraction+delta > 1 {
					// 进位：往回改已经写下的位
					for {
						i := len(frac) - 1
						if i == 0 {
							integer++
							break
						}
						c := frac[i]
						d := int(c - '0')
						if c > '9' {
							d = int(c-'a') + 10
						}
						frac = frac[:i]
						if d+1 < radix {
							frac = append(frac, chars[d+1])
							break
						}
					}
					break
				}
			}
			if fraction < delta {
				break
			}
		}
	}
	var whole []byte
	for {
		rem := math.Mod(integer, radix)
		whole = append([]byte{chars[int(rem)]}, whole...)
		integer = (integer - rem) / radix
		if integer <= 0 {
			break
		}
	}
	if len(frac) == 1 { // 只剩一个小数点（全进位了）
		frac = nil
	}
	return string(whole) + string(frac)
}

type syndUser struct {
	ID           *string `json:"id_str"`
	Name         *string `json:"name"`
	ScreenName   *string `json:"screen_name"`
	Avatar       *string `json:"profile_image_url_https"`
	BlueVerified *bool   `json:"is_blue_verified"`
	VerifiedType *string `json:"verified_type"`
}
type syndMedia struct {
	Type     string  `json:"type"`
	URL      *string `json:"media_url_https"`
	Original *struct {
		Width  *int `json:"width"`
		Height *int `json:"height"`
	} `json:"original_info"`
	Video *struct {
		Duration *float64 `json:"duration_millis"`
		Variants []struct {
			Bitrate     *int64  `json:"bitrate"`
			ContentType string  `json:"content_type"`
			URL         *string `json:"url"`
		} `json:"variants"`
	} `json:"video_info"`
}
type syndTweet struct {
	Typename  string          `json:"__typename"`
	ID        string          `json:"id_str"`
	Text      *string         `json:"text"`
	Range     []int           `json:"display_text_range"`
	CreatedAt *string         `json:"created_at"`
	Likes     *int64          `json:"favorite_count"`
	Replies   *int64          `json:"conversation_count"`
	Sensitive *bool           `json:"possibly_sensitive"`
	ReplyTo   *string         `json:"in_reply_to_status_id_str"`
	User      *syndUser       `json:"user"`
	Media     []syndMedia     `json:"mediaDetails"`
	Quote     *syndTweet      `json:"quoted_tweet"`
	Tombstone *struct{}       `json:"tombstone"`
	NoteTweet json.RawMessage `json:"note_tweet"`
	Article   json.RawMessage `json:"article"`
	Entities  *struct {
		URLs []struct {
			URL      string `json:"url"`
			Expanded string `json:"expanded_url"`
		} `json:"urls"`
		Media []struct {
			Indices []int `json:"indices"`
		} `json:"media"`
	} `json:"entities"`
}

// syndTrim 去掉正文末尾媒体的 t.co 短链。正文是转义过的 HTML，先还原再截。
// display_text_range 按还原后的 UTF-16 单位算（一个 4 字节 emoji 占 2），entities.media 的 indices 却按码点算：
// 先把 range 换成码点下标截，再拿第一个媒体短链的起点兜一次取较小值，最后去掉尾部空白。
func syndTrim(p *syndTweet) string {
	runes := []rune(syndUnescape.Replace(*p.Text))
	start, end := 0, len(runes)
	if len(p.Range) == 2 && p.Range[0] >= 0 && p.Range[1] >= p.Range[0] {
		start, end = utf16Index(runes, p.Range[0]), utf16Index(runes, p.Range[1])
	}
	if p.Entities != nil {
		for _, m := range p.Entities.Media {
			if len(m.Indices) == 2 && m.Indices[0] >= start && m.Indices[0] < end {
				end = m.Indices[0]
			}
		}
	}
	return strings.TrimRightFunc(string(runes[start:end]), unicode.IsSpace)
}

// utf16Index 把 UTF-16 单位的位置换成码点下标；超出末尾的按末尾算。
func utf16Index(runes []rune, n int) int {
	units := 0
	for i, r := range runes {
		if units >= n {
			return i
		}
		units += utf16.RuneLen(r)
	}
	return len(runes)
}

// syndUnescape 还原接口正文里转义过的 &、<、>（推特的老接口只转这三个）；一次替换，原文里的“&amp;”不会被还原两次。
var syndUnescape = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")

func (f *Fetcher) fetchSyndication(ctx context.Context, l Link) (Result, error) {
	q := url.Values{}
	q.Set("id", l.ID)
	q.Set("lang", "en")
	q.Set("features", syndicationFeatures)
	q.Set("token", syndicationToken(l.ID))
	b, status, h, err := f.request(ctx, "https://cdn.syndication.twimg.com/tweet-result?"+q.Encode(), RouteSyndication)
	if err != nil {
		return Result{}, err
	}
	if err = httpFailure(status, h, RouteSyndication); err != nil {
		return Result{}, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		// 帖子不存在或不公开时接口回空内容；但它偶尔也无故回空，只算弱证据（fetchX 挑报错时不让它压过线路故障）
		return Result{}, &FetchError{Kind: "not_found", Source: RouteSyndication, Reason: syndEmptyReason}
	}
	var p syndTweet
	if json.Unmarshal(b, &p) != nil {
		return Result{}, failure("invalid_response", RouteSyndication)
	}
	if p.Typename == "TweetTombstone" || p.Tombstone != nil {
		return Result{}, &FetchError{Kind: "not_found", Source: RouteSyndication, Reason: "tombstone"}
	}
	if p.ID != l.ID {
		return Result{}, failure("identity_mismatch", RouteSyndication)
	}
	return mapSyndication(l, &p, 0)
}

func mapSyndication(l Link, p *syndTweet, depth int) (Result, error) {
	if p.ID == "" || p.Text == nil {
		return Result{}, failure("invalid_response", RouteSyndication)
	}
	r := newResult(l, RouteSyndication)
	r.ID = p.ID
	text := syndTrim(p)
	if p.Entities != nil {
		// 正文里的链接是 t.co 短链，换回原链接（fx、vx 给的正文本来就是原链接）
		for _, u := range p.Entities.URLs {
			if u.URL != "" && u.Expanded != "" {
				text = strings.ReplaceAll(text, u.URL, u.Expanded)
			}
		}
	}
	if len(p.NoteTweet) > 0 && string(p.NoteTweet) != "null" {
		// 长推文的全文在 note_tweet 里，这条接口给的正文可能只是开头
		r.Text = Field[string]{State: "available", Value: text, Truncated: true}
	} else {
		r.Text = available(text)
	}
	r.Blocks = append(r.Blocks, Block{Type: "text", Text: text})
	if u := p.User; u != nil {
		r.Author.Name, r.Author.Account, r.Author.ID, r.Author.Avatar = stringField(u.Name), stringField(u.ScreenName), stringField(u.ID), stringField(u.Avatar)
		if r.Author.Avatar.State == "available" {
			r.Author.Avatar.Value = strings.Replace(r.Author.Avatar.Value, "_normal.", "_200x200.", 1)
		}
		r.Author.Verification = syndVerification(u)
	}
	if p.CreatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *p.CreatedAt); err == nil {
			r.Published = available(t.UTC())
		} else {
			r.Published = missing[time.Time]("invalid_time")
		}
	}
	stat(&r, "likes", p.Likes)
	stat(&r, "replies", p.Replies)
	for _, k := range []string{"reposts", "quotes", "views", "bookmarks"} {
		stat(&r, k, nil)
	}
	flag(&r, "possibly_sensitive", p.Sensitive)
	r.Flags["ai_generated"] = missing[jsonValue]("interface_omitted")
	r.Flags["community_note"] = missing[jsonValue]("interface_omitted")
	r.ReplyTo = stringField(p.ReplyTo)
	media := make([]Media, 0, len(p.Media))
	for _, m := range p.Media {
		item := Media{URL: stringField(m.URL), Thumbnail: missing[string]("not_applicable"), Width: missing[int]("interface_omitted"), Height: missing[int]("interface_omitted"),
			Duration: missing[float64]("not_applicable"), Size: missing[int64]("interface_omitted"), Variants: missing[[]MediaVariant]("not_applicable")}
		if m.Original != nil {
			item.Width, item.Height = intField(m.Original.Width), intField(m.Original.Height)
		}
		switch m.Type {
		case "photo":
			item.Type = "image"
			if item.URL.State == "available" {
				item.URL.Value = originalImage(item.URL.Value)
			}
		case "video", "animated_gif":
			item.Type = "video"
			if m.Type == "animated_gif" {
				item.Type = "gif"
			}
			item.Thumbnail = stringField(m.URL)
			item.URL = missing[string]("interface_omitted")
			item.Duration = missing[float64]("interface_omitted")
			if v := m.Video; v != nil {
				if v.Duration != nil {
					item.Duration = available(*v.Duration / 1000)
				}
				best := int64(-1)
				variants := make([]MediaVariant, 0, len(v.Variants))
				for _, x := range v.Variants {
					variants = append(variants, MediaVariant{URL: stringField(x.URL), Container: strings.TrimPrefix(x.ContentType, "video/"), Bitrate: int64Field(x.Bitrate), Width: missing[int]("interface_omitted"), Height: missing[int]("interface_omitted"), Size: missing[int64]("interface_omitted")})
					if x.ContentType == "video/mp4" && x.URL != nil && *x.URL != "" {
						b := int64(0)
						if x.Bitrate != nil {
							b = *x.Bitrate
						}
						if b > best {
							item.URL, best = available(*x.URL), b
						}
					}
				}
				item.Variants = available(variants)
			}
		default:
			return Result{}, failure("invalid_response", RouteSyndication)
		}
		media = append(media, item)
	}
	r.Media = available(media)
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, Block{Type: r.Media.Value[i].Type, Media: &r.Media.Value[i]})
	}
	r.MoreQuotes = available(false)
	if p.Quote != nil {
		if depth >= 1 {
			r.MoreQuotes = available(true)
			r.Quote = missing[*Result]("deeper_quote_omitted")
		} else {
			ql := Link{Platform: "x", Kind: "post", ID: p.Quote.ID, Clean: syndStatusURL(p.Quote)}
			q, err := mapSyndication(ql, p.Quote, depth+1)
			if err != nil {
				r.Quote = Field[*Result]{State: "failed", Reason: err.(*FetchError).Kind}
			} else {
				r.Quote = available(&q)
				r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: &q})
			}
		}
	}
	if len(p.Article) > 0 && string(p.Article) != "null" {
		mapSyndArticle(&r, p.Article, depth)
	}
	classifyX(&r)
	return r, nil
}

// syndStatusURL 是推文的规范链接：有账号写 x.com/账号/status/ID，没有才退回 /i/status/。
func syndStatusURL(p *syndTweet) string {
	if p.User == nil {
		return xStatusURL(nil, p.ID)
	}
	return xStatusURL(p.User.ScreenName, p.ID)
}

// xStatusURL 是推文的规范链接 x.com/账号/status/ID；接口没给账号（或账号不像账号）才退回 /i/status/。
func xStatusURL(account *string, id string) string {
	if account != nil {
		if a := strings.TrimPrefix(*account, "@"); xAccountName.MatchString(a) {
			return "https://x.com/" + a + "/status/" + id
		}
	}
	return "https://x.com/i/status/" + id
}

var xAccountName = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// ShareURL 是发出去给人点的链接。有 DisplayURL（GitHub 保住用户写的大小写）就用它；推文按取到的作者账号写 x.com/账号/status/ID：用户发的是 /i/status/、/i/web/status/
// 这类没账号的形态，或者账号写错了（x.com/X/status/… 实际是别人的推），都改成真正的作者。没取到账号、账号不像账号，
// 或者不是推文，就是 CleanURL。CleanURL 照旧是缓存、去重、关联用的键（缓存校验要求它等于输入链接的 Clean），不改。
func (r *Result) ShareURL() string {
	if r == nil {
		return ""
	}
	if r.DisplayURL != "" {
		return r.DisplayURL
	}
	// 只改识别器给出的 x.com 规范链接（Clean 一律是 https://x.com/…）；别的形态（编的样本等）原样不动
	if r.Platform != "x" || r.Kind != "post" || !digits(r.ID) || r.Author.Account.State != "available" || !strings.HasPrefix(r.CleanURL, "https://x.com/") {
		return r.CleanURL
	}
	if a := strings.TrimPrefix(r.Author.Account.Value, "@"); xAccountName.MatchString(a) {
		return xStatusURL(&a, r.ID)
	}
	return r.CleanURL
}

// mapSyndArticle 映射嵌入接口里的长文（X Article）：它只给标题、封面和开头一段 preview_text，
// 不给正文块。照 fx 的长文结构出标题、封面，再把开头作为一段正文，长文记成没取全。
func mapSyndArticle(r *Result, raw json.RawMessage, depth int) {
	// 长文字段解析不了只把长文记成没取到，推文本身照常出（fx、vx 都挂时它是最后一条线路，不能整条丢掉）；
	// mapXArticle 可能改到一半，先留一份原样，失败时退回去
	saved := *r
	if err := mapXArticle(r, raw, depth); err != nil {
		*r = saved
		r.Article = missing[string]("article_invalid_response")
		return
	}
	var a struct {
		Preview *string `json:"preview_text"`
	}
	_ = json.Unmarshal(raw, &a)
	if r.Article.State == "available" || a.Preview == nil || strings.TrimSpace(*a.Preview) == "" {
		return
	}
	preview := strings.TrimSpace(*a.Preview)
	r.Blocks = append(r.Blocks, Block{Type: "text", Text: preview, SourceType: "unstyled"})
	r.Article = Field[string]{State: "available", Value: preview, Truncated: true}
}

// syndVerification 照嵌入接口的认证字段：verified_type 是 Business / Government 时是金标、灰标，is_blue_verified 是蓝标。
func syndVerification(u *syndUser) Field[string] {
	switch {
	case u.VerifiedType != nil && strings.EqualFold(*u.VerifiedType, "Business"):
		return available(VerifyBusiness)
	case u.VerifiedType != nil && strings.EqualFold(*u.VerifiedType, "Government"):
		return available(VerifyGovernment)
	case u.BlueVerified != nil && *u.BlueVerified:
		return available(VerifyBlue)
	case u.BlueVerified != nil:
		return available(VerifyNone)
	}
	return missing[string]("interface_omitted")
}
