package linkreader

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// steamEnvelope 是商店接口的外层：{"<请求的 key>": {"success": …, "data": …}}。
type steamEnvelope[T any] struct {
	Success *bool `json:"success"`
	Data    *T    `json:"data"`
}

type steamPriceData struct {
	Currency        *string `json:"currency"`
	Initial         *int64  `json:"initial"`
	Final           *int64  `json:"final"`
	DiscountPercent *int64  `json:"discount_percent"`
	FinalFormatted  *string `json:"final_formatted"`
}
type steamGenre struct {
	Description *string `json:"description"`
}
type steamNamed struct {
	ID   decimalID `json:"id"`
	Name *string   `json:"name"`
}
type steamReleaseDate struct {
	ComingSoon *bool   `json:"coming_soon"`
	Date       *string `json:"date"`
}

// 字段都用指针：缺失、null 与 0 分开。未读取的字段不在这里声明。
type steamAppData struct {
	Type        *string         `json:"type"`
	Name        *string         `json:"name"`
	AppID       decimalID       `json:"steam_appid"`
	RequiredAge json.RawMessage `json:"required_age"`
	IsFree      *bool           `json:"is_free"`
	ShortDesc   *string         `json:"short_description"`
	Header      *string         `json:"header_image"`
	Developers  *[]string       `json:"developers"`
	Publishers  *[]string       `json:"publishers"`
	Price       *steamPriceData `json:"price_overview"`
	Platforms   map[string]bool `json:"platforms"`
	Genres      *[]steamGenre   `json:"genres"`
	Screenshots *[]struct {
		Full *string `json:"path_full"`
	} `json:"screenshots"`
	Movies *[]struct {
		Thumbnail *string `json:"thumbnail"`
	} `json:"movies"`
	Recommendations *struct {
		Total *int64 `json:"total"`
	} `json:"recommendations"`
	Achievements *struct {
		Total *int64 `json:"total"`
	} `json:"achievements"`
	ReleaseDate *steamReleaseDate `json:"release_date"`
	Descriptors *struct {
		IDs *[]int64 `json:"ids"`
	} `json:"content_descriptors"`
}

type steamPackageData struct {
	Name        *string           `json:"name"`
	PurchaseTxt *string           `json:"purchase_text"`
	Header      *string           `json:"header_image"`
	Apps        *[]steamNamed     `json:"apps"`
	Price       *steamPriceData   `json:"price"`
	Platforms   map[string]bool   `json:"platforms"`
	ReleaseDate *steamReleaseDate `json:"release_date"`
}

type steamFileDetail struct {
	PublishedFileID string  `json:"publishedfileid"`
	Result          *int    `json:"result"`
	Creator         string  `json:"creator"`
	ConsumerAppID   *int64  `json:"consumer_app_id"`
	FileSize        string  `json:"file_size"`
	Preview         *string `json:"preview_url"`
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	TimeCreated     *int64  `json:"time_created"`
	TimeUpdated     *int64  `json:"time_updated"`
	Visibility      *int64  `json:"visibility"`
	Banned          *int64  `json:"banned"`
	Subscriptions   *int64  `json:"subscriptions"`
	Favorited       *int64  `json:"favorited"`
	Views           *int64  `json:"views"`
	Tags            *[]struct {
		Tag string `json:"tag"`
	} `json:"tags"`
}

// steamProfileXML 只读取主页 XML 的这些元素；summary 等个人简介不取。
type steamProfileXML struct {
	XMLName    xml.Name `xml:""`
	Error      string   `xml:"error"`
	SteamID64  string   `xml:"steamID64"`
	Persona    string   `xml:"steamID"`
	Online     string   `xml:"onlineState"`
	Privacy    string   `xml:"privacyState"`
	Visibility string   `xml:"visibilityState"`
	Avatar     string   `xml:"avatarFull"`
}

const (
	steamStoreSource  = "steam_store"
	steamRemoteSource = "steam_remote_storage"
	steamCommunitySrc = "steam_community"
)

// fetchSteam 按 Kind 分派；应用的入口 fetchSteamApp 接收 Link，AppID 来自 l.ID，供其他平台复用。
func (f *Fetcher) fetchSteam(ctx context.Context, l Link) (Result, error) {
	switch l.Kind {
	case "app":
		return f.fetchSteamApp(ctx, l)
	case "package":
		return f.fetchSteamPackage(ctx, l)
	case "sharedfile":
		return f.fetchSteamSharedfile(ctx, l)
	case "profile":
		return f.fetchSteamProfile(ctx, l)
	}
	return Result{}, failure("unsupported", "steam")
}

// steamHTTP 把 3xx 记为 not_returned：不跟随到年龄确认或地区页，也不当成功。
func steamHTTP(status int, h http.Header, source string) error {
	if status >= 300 && status < 400 {
		return &FetchError{Kind: "not_returned", Source: source, Status: status, Reason: "redirect_not_followed"}
	}
	return httpFailure(status, h, source)
}

func decodeSteamEnvelope[T any](body []byte, key, source, reason string) (*T, error) {
	// 外层先只当原始 JSON 收下，先看 success，data 只在 success:true 时按 T 解码，
	// 避免失败条目里 data 的形状（例如 []）拖垮整段解码。
	var env map[string]json.RawMessage
	if json.Unmarshal(body, &env) != nil {
		return nil, failure("invalid_response", source)
	}
	raw, ok := env[key]
	if !ok {
		return nil, failure("invalid_response", source)
	}
	var head steamEnvelope[json.RawMessage]
	if json.Unmarshal(raw, &head) != nil || head.Success == nil {
		return nil, failure("invalid_response", source)
	}
	if !*head.Success {
		// success:false 可能是地区限制或内容缺失，不能说成删除或空结果。
		return nil, &FetchError{Kind: "not_returned", Source: source, Reason: reason}
	}
	var item steamEnvelope[T]
	if json.Unmarshal(raw, &item) != nil {
		return nil, failure("invalid_response", source)
	}
	if item.Data == nil {
		return nil, &FetchError{Kind: "not_returned", Source: source, Reason: reason}
	}
	return item.Data, nil
}

// steamAge 接受数字或字符串；其他形状视为结构错误。
func steamAge(raw json.RawMessage) (Field[int], error) {
	if len(raw) == 0 || string(raw) == "null" {
		return missing[int]("interface_omitted"), nil
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return available(n), nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			return available(v), nil
		}
	}
	return Field[int]{}, failure("invalid_response", steamStoreSource)
}

func steamPlatforms(p map[string]bool) Field[[]string] {
	if p == nil {
		return missing[[]string]("interface_omitted")
	}
	out := []string{}
	for _, name := range []string{"windows", "mac", "linux"} {
		if p[name] {
			out = append(out, name)
		}
	}
	return available(out)
}

func steamPrice(p *steamPriceData) Field[SteamPrice] {
	if p == nil {
		return missing[SteamPrice]("interface_omitted")
	}
	return available(SteamPrice{
		Currency:        stringField(p.Currency),
		Initial:         steamInt64(p.Initial),
		Final:           steamInt64(p.Final),
		DiscountPercent: steamInt64(p.DiscountPercent),
		Formatted:       stringField(p.FinalFormatted),
	})
}

func steamInt64(p *int64) Field[int64] {
	if p == nil {
		return missing[int64]("interface_omitted")
	}
	return available(*p)
}
func boolValue(p *bool) Field[bool] {
	if p == nil {
		return missing[bool]("interface_omitted")
	}
	return available(*p)
}

// steamReleaseText 保存本地化的发行日期文字；Steam 不保证是 RFC3339，因此不解析成时间。
func steamReleaseText(rd *steamReleaseDate) Field[string] {
	if rd == nil || rd.Date == nil || *rd.Date == "" {
		return missing[string]("interface_omitted")
	}
	return available(isoDateText(*rd.Date))
}

func (f *Fetcher) fetchSteamApp(ctx context.Context, l Link) (Result, error) {
	endpoint := "https://store.steampowered.com/api/appdetails?appids=" + url.QueryEscape(l.ID) + "&cc=cn&l=schinese"
	body, status, h, err := f.request(ctx, endpoint, steamStoreSource)
	if err != nil {
		return Result{}, err
	}
	if err := steamHTTP(status, h, steamStoreSource); err != nil {
		return Result{}, err
	}
	d, err := decodeSteamEnvelope[steamAppData](body, l.ID, steamStoreSource, "app_unavailable_or_missing")
	if err != nil {
		return Result{}, err
	}
	if string(d.AppID) != l.ID {
		return Result{}, failure("identity_mismatch", steamStoreSource)
	}
	if d.Name == nil || *d.Name == "" {
		return Result{}, failure("invalid_response", steamStoreSource)
	}
	age, err := steamAge(d.RequiredAge)
	if err != nil {
		return Result{}, err
	}
	r := newResult(l, steamStoreSource)
	r.Title = stringField(d.Name)
	r.Text = previewField(d.ShortDesc)
	r.Author.Name = missing[string]("interface_omitted")
	if d.Developers != nil {
		r.Author.Name = available(strings.Join(*d.Developers, " / "))
	}
	r.Author.ID = missing[string]("not_applicable")
	r.Author.Account = missing[string]("not_applicable")
	r.Published = missing[time.Time]("localized_text_only")
	r.PublishedText = steamReleaseText(d.ReleaseDate)
	if d.Genres != nil {
		tags := []string{}
		for _, g := range *d.Genres {
			if g.Description != nil {
				tags = append(tags, *g.Description)
			}
		}
		r.Tags = available(tags)
	}
	if d.Recommendations != nil {
		stat(&r, "recommendations", d.Recommendations.Total)
	}
	if d.Achievements != nil {
		stat(&r, "achievements", d.Achievements.Total)
	}
	if d.Price != nil {
		stat(&r, "price_initial", d.Price.Initial)
		stat(&r, "price_final", d.Price.Final)
		stat(&r, "discount_percent", d.Price.DiscountPercent)
	}
	// 免费游戏没有 price_overview，这不是 0 元，不写 Stats。
	if d.Header != nil || d.Screenshots != nil || d.Movies != nil {
		r.Media = available([]Media{})
	}
	if d.Header != nil {
		appendMedia(&r, imageMetadata(available(*d.Header)))
	}
	if d.Screenshots != nil {
		for _, s := range *d.Screenshots {
			if s.Full != nil {
				appendMedia(&r, imageMetadata(available(*s.Full)))
			}
		}
	}
	if d.Movies != nil {
		for _, m := range *d.Movies {
			appendMedia(&r, Media{Type: "video", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: stringField(m.Thumbnail),
				Width: missing[int]("interface_omitted"), Height: missing[int]("interface_omitted"), Duration: missing[float64]("interface_omitted"),
				Size: missing[int64]("interface_omitted"), Variants: missing[[]MediaVariant]("interface_omitted")})
		}
	}
	info := &SteamInfo{
		Type:          stringField(d.Type),
		Price:         steamPrice(d.Price),
		IsFree:        boolValue(d.IsFree),
		ComingSoon:    missing[bool]("interface_omitted"),
		RequiredAge:   age,
		Platforms:     steamPlatforms(d.Platforms),
		Developers:    missing[[]string]("interface_omitted"),
		Publishers:    missing[[]string]("interface_omitted"),
		DescriptorIDs: missing[[]int64]("interface_omitted"),
		Apps:          missing[[]SteamAppRef]("not_applicable"),
		ConsumerAppID: missing[string]("not_applicable"),
		FileSize:      missing[int64]("not_applicable"),
		Updated:       missing[time.Time]("not_applicable"),
	}
	if d.Developers != nil {
		info.Developers = available(*d.Developers)
	}
	if d.Publishers != nil {
		info.Publishers = available(*d.Publishers)
	}
	if d.ReleaseDate != nil && d.ReleaseDate.ComingSoon != nil {
		info.ComingSoon = available(*d.ReleaseDate.ComingSoon)
	}
	if d.Descriptors != nil && d.Descriptors.IDs != nil {
		info.DescriptorIDs = available(*d.Descriptors.IDs)
	}
	r.Steam = info
	appendBody(&r)
	return r, nil
}

func (f *Fetcher) fetchSteamPackage(ctx context.Context, l Link) (Result, error) {
	endpoint := "https://store.steampowered.com/api/packagedetails?packageids=" + url.QueryEscape(l.ID) + "&cc=cn&l=schinese"
	body, status, h, err := f.request(ctx, endpoint, steamStoreSource)
	if err != nil {
		return Result{}, err
	}
	if err := steamHTTP(status, h, steamStoreSource); err != nil {
		return Result{}, err
	}
	d, err := decodeSteamEnvelope[steamPackageData](body, l.ID, steamStoreSource, "package_unavailable_or_missing")
	if err != nil {
		return Result{}, err
	}
	if d.Name == nil || *d.Name == "" {
		return Result{}, failure("invalid_response", steamStoreSource)
	}
	r := newResult(l, steamStoreSource)
	r.Title = stringField(d.Name)
	r.Text = previewField(d.PurchaseTxt)
	r.Author.Name = missing[string]("not_applicable")
	r.Author.ID = missing[string]("not_applicable")
	r.Author.Account = missing[string]("not_applicable")
	r.Published = missing[time.Time]("localized_text_only")
	r.PublishedText = steamReleaseText(d.ReleaseDate)
	if d.Header != nil {
		r.Media = available([]Media{})
		appendMedia(&r, imageMetadata(available(*d.Header)))
	}
	apps := missing[[]SteamAppRef]("interface_omitted")
	if d.Apps != nil {
		list := []SteamAppRef{}
		for _, a := range *d.Apps {
			list = append(list, SteamAppRef{ID: available(string(a.ID)), Name: stringField(a.Name)})
		}
		apps = available(list)
	}
	r.Steam = &SteamInfo{
		Type: available("package"), Price: steamPrice(d.Price), IsFree: missing[bool]("not_applicable"),
		ComingSoon: missing[bool]("interface_omitted"), RequiredAge: missing[int]("not_applicable"),
		Platforms: steamPlatforms(d.Platforms), Developers: missing[[]string]("not_applicable"), Publishers: missing[[]string]("not_applicable"),
		DescriptorIDs: missing[[]int64]("not_applicable"), Apps: apps, ConsumerAppID: missing[string]("not_applicable"),
		FileSize: missing[int64]("not_applicable"), Updated: missing[time.Time]("not_applicable"),
	}
	if d.ReleaseDate != nil && d.ReleaseDate.ComingSoon != nil {
		r.Steam.ComingSoon = available(*d.ReleaseDate.ComingSoon)
	}
	appendBody(&r)
	return r, nil
}

func (f *Fetcher) fetchSteamSharedfile(ctx context.Context, l Link) (Result, error) {
	form := url.Values{}
	form.Set("itemcount", "1")
	form.Set("publishedfileids[0]", l.ID)
	body, status, h, err := f.send(ctx, http.MethodPost, "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/", steamRemoteSource, []byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return Result{}, err
	}
	if err := steamHTTP(status, h, steamRemoteSource); err != nil {
		return Result{}, err
	}
	var p struct {
		Response struct {
			Result  *int              `json:"result"`
			Details []steamFileDetail `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &p) != nil || p.Response.Result == nil {
		return Result{}, failure("invalid_response", steamRemoteSource)
	}
	if *p.Response.Result != 1 {
		return Result{}, &FetchError{Kind: "not_returned", Source: steamRemoteSource, Code: *p.Response.Result, Reason: "api_result"}
	}
	if len(p.Response.Details) != 1 {
		return Result{}, failure("invalid_response", steamRemoteSource)
	}
	d := p.Response.Details[0]
	if d.Result == nil {
		return Result{}, failure("invalid_response", steamRemoteSource)
	}
	if *d.Result != 1 {
		return Result{}, &FetchError{Kind: "not_returned", Source: steamRemoteSource, Code: *d.Result, Reason: "api_result"}
	}
	if d.PublishedFileID != l.ID {
		return Result{}, failure("identity_mismatch", steamRemoteSource)
	}
	if d.Banned != nil && *d.Banned != 0 {
		return Result{}, &FetchError{Kind: "not_returned", Source: steamRemoteSource, Reason: "banned"}
	}
	if d.Title == nil || *d.Title == "" || d.Creator == "" {
		return Result{}, failure("invalid_response", steamRemoteSource)
	}
	size, err := strconv.ParseInt(d.FileSize, 10, 64)
	if err != nil || size < 0 {
		return Result{}, failure("invalid_response", steamRemoteSource)
	}
	r := newResult(l, steamRemoteSource)
	r.Title = stringField(d.Title)
	// 创意工坊的说明是 BBCode：先换成纯文字再截开头，不让截断落在标签中间
	if d.Description != nil {
		desc := bbcodePlain(*d.Description)
		r.Text = previewField(&desc)
	} else {
		r.Text = previewField(nil)
	}
	r.Flags["text_format"] = available(jsonValue("bbcode"))
	if d.Visibility != nil {
		r.Flags["visibility"] = available(jsonValue(*d.Visibility))
	}
	r.Author.ID = available(d.Creator)
	r.Author.Name = missing[string]("needs_profile_lookup")
	r.Author.Account = missing[string]("not_applicable")
	r.Published = unixField(d.TimeCreated)
	stat(&r, "subscriptions", d.Subscriptions)
	stat(&r, "favorited", d.Favorited)
	stat(&r, "views", d.Views)
	if d.Tags != nil {
		tags := []string{}
		for _, t := range *d.Tags {
			if steamLanguageTags[t.Tag] {
				continue // Steam 自己打的语言标记（english、schinese 这类），不是作者写的标签
			}
			tags = append(tags, t.Tag)
		}
		r.Tags = available(tags)
	}
	r.Media = available([]Media{})
	if d.Preview != nil {
		appendMedia(&r, imageMetadata(available(*d.Preview)))
	}
	consumer := missing[string]("interface_omitted")
	if d.ConsumerAppID != nil {
		consumer = available(strconv.FormatInt(*d.ConsumerAppID, 10))
	}
	r.Steam = &SteamInfo{
		Type: missing[string]("not_applicable"), Price: missing[SteamPrice]("not_applicable"), IsFree: missing[bool]("not_applicable"),
		ComingSoon: missing[bool]("not_applicable"), RequiredAge: missing[int]("not_applicable"), Platforms: missing[[]string]("not_applicable"),
		Developers: missing[[]string]("not_applicable"), Publishers: missing[[]string]("not_applicable"), DescriptorIDs: missing[[]int64]("not_applicable"),
		Apps: missing[[]SteamAppRef]("not_applicable"), ConsumerAppID: consumer, FileSize: available(size),
		Updated: unixField(d.TimeUpdated),
	}
	appendBody(&r)
	return r, nil
}

func (f *Fetcher) fetchSteamProfile(ctx context.Context, l Link) (Result, error) {
	body, status, h, err := f.request(ctx, "https://steamcommunity.com/"+l.ID+"/?xml=1", steamCommunitySrc)
	if err != nil {
		return Result{}, err
	}
	if err := steamHTTP(status, h, steamCommunitySrc); err != nil {
		return Result{}, err
	}
	var p steamProfileXML
	if xml.Unmarshal(body, &p) != nil {
		return Result{}, failure("invalid_response", steamCommunitySrc)
	}
	// 个人主页找不到时仍是 HTTP 200，错误放在 <response><error> 里。
	if p.XMLName.Local == "response" || p.Error != "" {
		return Result{}, failure("not_found", steamCommunitySrc)
	}
	if p.XMLName.Local != "profile" || !steamID64.MatchString(p.SteamID64) {
		return Result{}, failure("invalid_response", steamCommunitySrc)
	}
	// profiles 路径可以用 SteamID64 核对；id 路径的 XML 不带自定义名（调研样本无此元素），只能核对格式。
	if rest, ok := strings.CutPrefix(l.ID, "profiles/"); ok && rest != p.SteamID64 {
		return Result{}, failure("identity_mismatch", steamCommunitySrc)
	}
	if p.Persona == "" {
		return Result{}, failure("invalid_response", steamCommunitySrc)
	}
	r := newResult(l, steamCommunitySrc)
	r.Title = available(p.Persona)
	r.Text = missing[string]("summary_not_read")
	r.Author.Name = available(p.Persona)
	r.Author.ID = available(p.SteamID64)
	r.Author.Account = missing[string]("not_in_xml")
	if p.Avatar != "" {
		r.Author.Avatar = available(p.Avatar)
	}
	r.Flags["privacy_state"] = available(jsonValue(p.Privacy))
	r.Flags["visibility_state"] = available(jsonValue(p.Visibility))
	r.Flags["online_state"] = available(jsonValue(p.Online))
	appendBody(&r)
	return r, nil
}

// steamLanguageTags 是 Steam 创意工坊给每件物品自动带的语言标记，值就是 Steam 的语言接口名，全小写。
// 作者自己写的标签（Characters、mod、skin 这类）不会和它们重名；大小写不同的不算。
var steamLanguageTags = map[string]bool{
	"arabic": true, "bulgarian": true, "schinese": true, "tchinese": true, "czech": true, "danish": true, "dutch": true,
	"english": true, "finnish": true, "french": true, "german": true, "greek": true, "hungarian": true, "indonesian": true,
	"italian": true, "japanese": true, "koreana": true, "norwegian": true, "polish": true, "portuguese": true, "brazilian": true,
	"romanian": true, "russian": true, "latam": true, "spanish": true, "swedish": true, "thai": true, "turkish": true,
	"ukrainian": true, "vietnamese": true,
}
