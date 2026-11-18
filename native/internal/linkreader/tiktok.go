package linkreader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 页面里的 JSON 位于 id 属性所在开始标签之后；属性顺序不固定，只认 id。
const tiktokRehydrationMarker = `id="__UNIVERSAL_DATA_FOR_REHYDRATION__"`

// tiktokNum 同时接受 JSON 字符串和数字：统计字段是字符串，原样保留，解析在映射时做。
type tiktokNum string

func (n *tiktokNum) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else if t := string(bytes.TrimSpace(b)); t != "null" {
		s = t
	}
	*n = tiktokNum(s)
	return nil
}

type tiktokStats struct {
	PlayCount      *tiktokNum `json:"playCount"`
	DiggCount      *tiktokNum `json:"diggCount"`
	CommentCount   *tiktokNum `json:"commentCount"`
	ShareCount     *tiktokNum `json:"shareCount"`
	CollectCount   *tiktokNum `json:"collectCount"`
	FollowerCount  *tiktokNum `json:"followerCount"`
	FollowingCount *tiktokNum `json:"followingCount"`
	HeartCount     *tiktokNum `json:"heartCount"`
	VideoCount     *tiktokNum `json:"videoCount"`
}
type tiktokAuthor struct {
	ID       tiktokNum  `json:"id"`
	UniqueID lv[string] `json:"uniqueId"`
	Nickname lv[string] `json:"nickname"`
}
type tiktokVideo struct {
	Cover    lv[string] `json:"cover"`
	Width    lv[int]    `json:"width"`
	Height   lv[int]    `json:"height"`
	Duration lv[int64]  `json:"duration"`
}
type tiktokImage struct {
	ImageURL struct {
		URLList []string `json:"urlList"`
	} `json:"imageURL"`
	ImageWidth  lv[int] `json:"imageWidth"`
	ImageHeight lv[int] `json:"imageHeight"`
}
type tiktokItem struct {
	ID         tiktokNum        `json:"id"`
	Desc       lv[string]       `json:"desc"`
	CreateTime *tiktokNum       `json:"createTime"`
	Author     lv[tiktokAuthor] `json:"author"`
	StatsV2    lv[tiktokStats]  `json:"statsV2"`
	Video      lv[tiktokVideo]  `json:"video"`
	ImagePost  lv[struct {
		Images []tiktokImage `json:"images"`
	}] `json:"imagePost"`
}
type tiktokUser struct {
	ID           tiktokNum  `json:"id"`
	UniqueID     lv[string] `json:"uniqueId"`
	Nickname     lv[string] `json:"nickname"`
	Signature    lv[string] `json:"signature"`
	AvatarLarger lv[string] `json:"avatarLarger"`
	Verified     lv[bool]   `json:"verified"`
}
type tiktokScope struct {
	VideoDetail *struct {
		StatusCode lv[int] `json:"statusCode"`
		ItemInfo   *struct {
			ItemStruct *tiktokItem `json:"itemStruct"`
		} `json:"itemInfo"`
	} `json:"webapp.video-detail"`
	UserDetail *struct {
		StatusCode lv[int] `json:"statusCode"`
		UserInfo   *struct {
			User    lv[tiktokUser]  `json:"user"`
			StatsV2 lv[tiktokStats] `json:"statsV2"`
		} `json:"userInfo"`
	} `json:"webapp.user-detail"`
}

// 统一为 https://www.tiktok.com 的无查询串页面；视频、图文和用户页都取同一条路径。
func tiktokPageURL(clean string) (string, error) {
	u, err := url.Parse(clean)
	if err != nil || u.Host == "" {
		return "", failure("invalid_target", "tiktok_page")
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// tiktokHomeRedirect 识别失效短链落到首页（实测跳到 ?_r=1）；只认带 _r=1 的首页，普通首页不算。
func tiktokHomeRedirect(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return hostSuffix(host, "tiktok.com") && (u.Path == "" || u.Path == "/") && u.Query().Get("_r") == "1"
}

// tiktokDomainTarget 终点在 TikTok 域内、但不是支持的内容页（标签页、直播等），报 unsupported，见规格 TikTok 小节的错误映射。
func tiktokDomainTarget(u *url.URL) bool {
	return hostSuffix(strings.ToLower(u.Hostname()), "tiktok.com")
}

// tiktokScopeFromPage 找不到 rehydration 标记时返回 found=false，由调用方判断挑战页或结构错误。
func tiktokScopeFromPage(page []byte) (scope *tiktokScope, found bool, err error) {
	i := bytes.Index(page, []byte(tiktokRehydrationMarker))
	if i < 0 {
		return nil, false, nil
	}
	rest := page[i+len(tiktokRehydrationMarker):]
	gt := bytes.IndexByte(rest, '>')
	if gt < 0 {
		return nil, true, failure("invalid_response", "tiktok_page")
	}
	body := rest[gt+1:]
	end := bytes.Index(body, []byte("</script>"))
	if end < 0 {
		return nil, true, failure("invalid_response", "tiktok_page")
	}
	var doc struct {
		Scope *tiktokScope `json:"__DEFAULT_SCOPE__"`
	}
	// 只有语法错误才算页面结构坏了；个别字段类型不对时 encoding/json 会继续解其余字段，那一个字段留空、如实当缺失。
	if err := json.Unmarshal(body[:end], &doc); err != nil {
		var ute *json.UnmarshalTypeError
		if !errors.As(err, &ute) {
			return nil, true, failure("invalid_response", "tiktok_page")
		}
	}
	if doc.Scope == nil {
		return nil, true, failure("invalid_response", "tiktok_page")
	}
	return doc.Scope, true, nil
}

// tiktokStatus 只映射已确认的业务码；0 以外的未知码不冒充成功或已删除。
func tiktokStatus(code *int) error {
	if code == nil {
		return failure("invalid_response", "tiktok_page")
	}
	switch *code {
	case 0:
		return nil
	case 10216, 10222:
		return &FetchError{Kind: "login_required", Source: "tiktok_page", Code: *code}
	case 10204:
		return &FetchError{Kind: "rate_limited", Source: "tiktok_page", Code: *code}
	}
	return &FetchError{Kind: "not_returned", Source: "tiktok_page", Code: *code}
}

// tiktokCount 只接受非负十进制字符串或数字；缺失为 nil，解析失败不当作 0。
func tiktokCount(n *tiktokNum) (*int64, error) {
	if n == nil {
		return nil, nil
	}
	s := string(*n)
	if !digits(s) {
		return nil, failure("invalid_response", "tiktok_page")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, failure("invalid_response", "tiktok_page")
	}
	return &v, nil
}

type tiktokStat struct {
	key string
	get func(*tiktokStats) *tiktokNum
}

var tiktokVideoStats = []tiktokStat{
	{"views", func(s *tiktokStats) *tiktokNum { return s.PlayCount }},
	{"likes", func(s *tiktokStats) *tiktokNum { return s.DiggCount }},
	{"comments", func(s *tiktokStats) *tiktokNum { return s.CommentCount }},
	{"shares", func(s *tiktokStats) *tiktokNum { return s.ShareCount }},
	{"collects", func(s *tiktokStats) *tiktokNum { return s.CollectCount }},
}

var tiktokUserStats = []tiktokStat{
	{"followers", func(s *tiktokStats) *tiktokNum { return s.FollowerCount }},
	{"following", func(s *tiktokStats) *tiktokNum { return s.FollowingCount }},
	{"heart", func(s *tiktokStats) *tiktokNum { return s.HeartCount }},
	{"videos", func(s *tiktokStats) *tiktokNum { return s.VideoCount }},
}

// tiktokPutStats 逐项写统计；某一项的值不是非负整数时只把这一项记为缺失（invalid_value），不让整页失败，也不当 0。
func tiktokPutStats(r *Result, s *tiktokStats, stats []tiktokStat) {
	for _, st := range stats {
		var n *tiktokNum
		if s != nil {
			n = st.get(s)
		}
		v, err := tiktokCount(n)
		if err != nil {
			r.Stats[st.key] = missing[int64]("invalid_value")
			continue
		}
		stat(r, st.key, v)
	}
}

// tiktokText 把空白描述标为缺失：空串在平台上不等于“没有正文”的确认。
func tiktokText(p *string) Field[string] {
	if p == nil {
		return missing[string]("interface_omitted")
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return missing[string]("empty_text")
	}
	return previewField(&s)
}

func (f *Fetcher) fetchTikTok(ctx context.Context, l Link) (Result, error) {
	const source = "tiktok_page"
	endpoint, err := tiktokPageURL(l.Clean)
	if err != nil {
		return Result{}, err
	}
	b, status, h, err := f.request(ctx, endpoint, source)
	if err != nil {
		return Result{}, err
	}
	// 3xx 不跟随；跳去登录页才是登录墙，其余跳转不当作成功或已删除。
	if status >= 300 && status <= 399 {
		if strings.Contains(h.Get("Location"), "/login") {
			return Result{}, &FetchError{Kind: "login_required", Source: source, Status: status}
		}
		return Result{}, &FetchError{Kind: "not_returned", Source: source, Status: status}
	}
	// 无登录证据的 403 是匿名页没返回，不断言作品已删或必须登录。
	if status == 403 {
		return Result{}, &FetchError{Kind: "not_returned", Source: source, Status: status}
	}
	if err := httpFailure(status, h, source); err != nil {
		return Result{}, err
	}
	scope, found, err := tiktokScopeFromPage(b)
	if err != nil {
		return Result{}, err
	}
	if !found {
		// 挑战页不解题、不发 Cookie，只报告这一次没拿到。
		if bytes.Contains(b, []byte(`id="cs"`)) {
			return Result{}, &FetchError{Kind: "not_returned", Source: source, Status: status}
		}
		return Result{}, failure("invalid_response", source)
	}
	if l.Kind == "user" {
		return tiktokUserResult(l, scope, source)
	}
	return tiktokVideoResult(l, scope, source)
}

func tiktokVideoResult(l Link, scope *tiktokScope, source string) (Result, error) {
	d := scope.VideoDetail
	if d == nil {
		return Result{}, failure("not_returned", source)
	}
	if err := tiktokStatus(d.StatusCode.V); err != nil {
		return Result{}, err
	}
	if d.ItemInfo == nil || d.ItemInfo.ItemStruct == nil {
		return Result{}, failure("not_returned", source)
	}
	item := d.ItemInfo.ItemStruct
	if string(item.ID) != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	r := newResult(l, source)
	if a := item.Author.V; a != nil {
		r.Author.Name = stringField(a.Nickname.V)
		r.Author.Account = stringField(a.UniqueID.V)
		if a.ID != "" {
			r.Author.ID = available(string(a.ID))
		}
	}
	// 实测 createTime 是十进制字符串（秒）；0 或缺失都不当作发布时间。
	if created, err := tiktokCount(item.CreateTime); err != nil {
		r.Published = missing[time.Time]("invalid_value")
	} else if created != nil && *created > 0 {
		r.Published = unixField(created)
	}
	r.Text = tiktokText(item.Desc.V)
	tiktokPutStats(&r, item.StatsV2.V, tiktokVideoStats)
	appendBody(&r)
	switch {
	case item.ImagePost.V != nil:
		// 图文只产生图片；video 字段在图文里全为 0，不能当视频。
		r.Media = available([]Media{})
		for _, img := range item.ImagePost.V.Images {
			raw := missing[string]("interface_omitted")
			if len(img.ImageURL.URLList) > 0 {
				raw = available(img.ImageURL.URLList[0])
			}
			m := imageMetadata(raw)
			m.Width, m.Height = intField(img.ImageWidth.V), intField(img.ImageHeight.V)
			appendMedia(&r, m)
		}
	case item.Video.V != nil:
		v := item.Video.V
		m := Media{Type: "video", URL: Field[string]{State: "unsupported", Reason: "session_cookie_required"}, Thumbnail: stringField(v.Cover.V), Width: intField(v.Width.V), Height: intField(v.Height.V), Duration: missing[float64]("interface_omitted"), Size: missing[int64]("interface_omitted")}
		if v.Duration.V != nil {
			if *v.Duration.V < 0 {
				m.Duration = missing[float64]("invalid_value")
			} else {
				m.Duration = available(float64(*v.Duration.V))
			}
		}
		appendMedia(&r, m)
	}
	return r, nil
}

func tiktokUserResult(l Link, scope *tiktokScope, source string) (Result, error) {
	d := scope.UserDetail
	if d == nil {
		return Result{}, failure("not_returned", source)
	}
	if err := tiktokStatus(d.StatusCode.V); err != nil {
		return Result{}, err
	}
	if d.UserInfo == nil || d.UserInfo.User.V == nil {
		return Result{}, failure("not_returned", source)
	}
	u := d.UserInfo.User.V
	if u.UniqueID.V == nil {
		return Result{}, failure("invalid_response", source)
	}
	if !strings.EqualFold(*u.UniqueID.V, l.ID) {
		return Result{}, failure("identity_mismatch", source)
	}
	r := newResult(l, source)
	r.Title = stringField(u.Nickname.V)
	r.Author.Name = stringField(u.Nickname.V)
	r.Author.Account = stringField(u.UniqueID.V)
	if u.ID != "" {
		r.Author.ID = available(string(u.ID))
	}
	r.Author.Avatar = stringField(u.AvatarLarger.V)
	r.Text = tiktokText(u.Signature.V)
	tiktokPutStats(&r, d.UserInfo.StatsV2.V, tiktokUserStats)
	flag(&r, "verified", u.Verified.V)
	appendBody(&r)
	return r, nil
}
