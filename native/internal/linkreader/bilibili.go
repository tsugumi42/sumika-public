package linkreader

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type biliVideo struct {
	Source    string    `json:"-"`
	BVID      string    `json:"bvid"`
	AID       decimalID `json:"aid"`
	CID       decimalID `json:"cid"`
	Title     *string   `json:"title"`
	Desc      *string   `json:"desc"`
	Pic       *string   `json:"pic"`
	Timestamp *int64    `json:"pubdate"`
	Category  *string   `json:"tname"`
	Owner     struct {
		Name *string   `json:"name"`
		Face *string   `json:"face"`
		ID   decimalID `json:"mid"`
	} `json:"owner"`
	Pages []struct {
		CID       decimalID `json:"cid"`
		Page      int       `json:"page"`
		Title     *string   `json:"part"`
		Duration  *int      `json:"duration"`
		Dimension struct {
			Width  *int `json:"width"`
			Height *int `json:"height"`
		} `json:"dimension"`
	} `json:"pages"`
	Stats         map[string]json.RawMessage `json:"stat"`
	Copyright     json.RawMessage            `json:"copyright"`
	Argue         json.RawMessage            `json:"argue_info"`
	Rights        json.RawMessage            `json:"rights"`
	Honor         json.RawMessage            `json:"honor_reply"`
	UPExclusive   *bool                      `json:"is_upower_exclusive"`
	Chargeable    *bool                      `json:"is_chargeable_season"`
	UPowerPlay    *bool                      `json:"is_upower_play"`
	UPowerPreview *bool                      `json:"is_upower_preview"`
	Teenage       json.RawMessage            `json:"teenage_mode"`
}
type biliEnvelope struct {
	Code *int       `json:"code"`
	Data *biliVideo `json:"data"`
}

func biliFailure(code int, source string) error {
	kind := "interface_error"
	switch code {
	case -404, 62002, 81100001, 81102094, 4101152, 147002:
		// 81100001、81102094：会员购、小店商品不存在；4101152：动态（专栏）不可见或已删除；147002：系列已失效
		kind = "not_found"
	case -101, -403, 62004:
		kind = "login_required"
	case -412, -352:
		kind = "risk_control"
	case -509, -799:
		// -509 超出限制、-799 请求过于频繁：频率码，平台短冷却（见 rate.go freqCool）
		kind = "rate_limited"
	}
	return &FetchError{Kind: kind, Code: code, Source: source}
}

func biliQuery(l Link) url.Values {
	q := url.Values{}
	if strings.HasPrefix(l.ID, "BV") {
		q.Set("bvid", l.ID)
	} else {
		q.Set("aid", trimID(l.ID))
	}
	return q
}
func (f *Fetcher) biliView(ctx context.Context, l Link) (*biliVideo, error) {
	// 只发这一次请求，不签名、不换标识重试：风控（HTTP 412、-352、-412）报 risk_control，平台整体冷却（rate.go riskCool）。
	// 风控之后再发的请求只会加重标记；视频接口本身不要求 WBI 签名，所以原先 412 后的签名回退已去掉。
	var e biliEnvelope
	err := f.getJSON(ctx, "https://api.bilibili.com/x/web-interface/view?"+biliQuery(l).Encode(), "bili_view", &e)
	if err != nil {
		return nil, err
	}
	if e.Code == nil {
		return nil, failure("invalid_response", "bili_view")
	}
	if *e.Code != 0 {
		return nil, biliFailure(*e.Code, "bili_view")
	}
	if e.Data == nil {
		return nil, failure("invalid_response", "bili_view")
	}
	e.Data.Source = "bili_view"
	return e.Data, nil
}
func (f *Fetcher) fetchBili(ctx context.Context, l Link) (Result, error) {
	v, err := f.biliView(ctx, l)
	if err != nil {
		return Result{}, err
	}
	if (strings.HasPrefix(l.ID, "BV") && v.BVID != l.ID) || (strings.HasPrefix(l.ID, "av") && string(v.AID) != trimID(l.ID)) {
		return Result{}, failure("identity_mismatch", "bili_view")
	}
	if v.Title == nil || v.Desc == nil || len(v.Pages) == 0 || v.CID == "" {
		return Result{}, failure("invalid_response", "bili_view")
	}
	u, _ := url.Parse(l.Clean)
	part := 1
	if p, ok := u.Query()["p"]; ok {
		if len(p) != 1 {
			return Result{}, failure("invalid_part", "bili_view")
		}
		part, err = strconv.Atoi(p[0])
		if err != nil {
			return Result{}, failure("invalid_part", "bili_view")
		}
	}
	if part < 1 || part > len(v.Pages) {
		return Result{}, failure("invalid_part", "bili_view")
	}
	parts := make([]VideoPart, 0, len(v.Pages))
	seen := map[decimalID]bool{}
	for i, p := range v.Pages {
		if p.Page != i+1 || p.CID == "" || seen[p.CID] {
			return Result{}, failure("identity_mismatch", "bili_view")
		}
		seen[p.CID] = true
		parts = append(parts, VideoPart{Number: p.Page, CID: string(p.CID), Title: stringField(p.Title), Duration: intField(p.Duration)})
	}
	if v.Pages[0].CID != v.CID {
		return Result{}, failure("identity_mismatch", "bili_view")
	}
	r := newResult(l, v.Source)
	r.Title = stringField(v.Title)
	r.Text = stringField(v.Desc)
	r.Author.Name = stringField(v.Owner.Name)
	r.Author.Avatar = stringField(v.Owner.Face)
	if v.Owner.ID != "" {
		r.Author.ID = available(string(v.Owner.ID))
	}
	r.Published = unixField(v.Timestamp)
	r.Category = stringField(v.Category)
	r.Parts = available(parts)
	r.SelectedPart = available(parts[part-1])
	r.Blocks = append(r.Blocks, Block{Type: "text", Text: *v.Title + "\n" + *v.Desc})
	p := v.Pages[part-1]
	// view 的 dimension 属于视频，不能拿它冒充封面的像素尺寸。
	cover := Media{Type: "image", URL: stringField(v.Pic), Thumbnail: missing[string]("not_applicable"), Width: missing[int]("interface_omitted"), Height: missing[int]("interface_omitted"), Duration: missing[float64]("not_applicable"), Size: missing[int64]("interface_omitted")}
	video := Media{Type: "video", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: stringField(v.Pic), Width: intField(p.Dimension.Width), Height: intField(p.Dimension.Height), Duration: secondsField(p.Duration), Size: missing[int64]("interface_omitted")}
	r.Media = available([]Media{cover, video})
	r.Blocks = append(r.Blocks, Block{Type: "image", Media: &r.Media.Value[0]}, Block{Type: "video", Media: &r.Media.Value[1]})
	for _, key := range []string{"view", "like", "coin", "favorite", "share", "danmaku", "reply"} {
		var value *int64
		if raw, ok := v.Stats[key]; ok {
			if json.Unmarshal(raw, &value) != nil {
				return Result{}, failure("invalid_response", "bili_view")
			}
		}
		stat(&r, key, value)
	}
	rawFlag(&r, "copyright", v.Copyright)
	rawFlag(&r, "argue_info", v.Argue)
	rawFlag(&r, "rights", v.Rights)
	rawFlag(&r, "honor_reply", v.Honor)
	flag(&r, "is_upower_exclusive", v.UPExclusive)
	flag(&r, "is_chargeable_season", v.Chargeable)
	flag(&r, "is_upower_play", v.UPowerPlay)
	flag(&r, "is_upower_preview", v.UPowerPreview)
	rawFlag(&r, "teenage_mode", v.Teenage)
	r.Flags["ai_generated"] = missing[jsonValue]("interface_omitted")
	r.Flags["creation_statement"] = missing[jsonValue]("interface_omitted")
	r.Flags["possibly_sensitive"] = missing[jsonValue]("interface_omitted")
	r.Tags = f.biliTags(ctx, l)
	r.Comments = f.biliComments(ctx, string(v.AID), 1)
	return r, nil
}
func (f *Fetcher) biliTags(ctx context.Context, l Link) Field[[]string] {
	var e struct {
		Code *int `json:"code"`
		Data *[]struct {
			Name string `json:"tag_name"`
		} `json:"data"`
	}
	err := f.getJSON(ctx, "https://api.bilibili.com/x/tag/archive/tags?"+biliQuery(l).Encode(), "bili_tags", &e)
	if err == nil && e.Code != nil && *e.Code != 0 {
		err = biliFailure(*e.Code, "bili_tags")
	}
	if err == nil && (e.Code == nil || e.Data == nil) {
		err = failure("invalid_response", "bili_tags")
	}
	if err != nil {
		var fe *FetchError
		state := "failed"
		reason := "interface_error"
		if errors.As(err, &fe) {
			reason = fe.Kind
			if reason == "login_required" || reason == "rate_limited" {
				state = reason
			}
		}
		return Field[[]string]{State: state, Reason: reason}
	}
	tags := make([]string, 0, len(*e.Data))
	for _, tag := range *e.Data {
		tags = append(tags, tag.Name)
	}
	return available(tags)
}

func secondsField(p *int) Field[float64] {
	if p == nil {
		return missing[float64]("interface_omitted")
	}
	return available(float64(*p))
}
