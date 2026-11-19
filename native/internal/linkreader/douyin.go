package linkreader

import (
	"context"
	"encoding/json"
	"net/url"
)

type douyinAddr struct {
	URLs   []string `json:"url_list"`
	Width  *int     `json:"width"`
	Height *int     `json:"height"`
}
type douyinItem struct {
	ID          decimalID `json:"aweme_id"`
	Description *string   `json:"desc"`
	Created     *int64    `json:"create_time"`
	Author      *struct {
		Name *string   `json:"nickname"`
		ID   decimalID `json:"uid"`
	} `json:"author"`
	Statistics *struct {
		Likes    *int64 `json:"digg_count"`
		Comments *int64 `json:"comment_count"`
		Shares   *int64 `json:"share_count"`
	} `json:"statistics"`
	Video *struct {
		Cover    douyinAddr `json:"cover"`
		Duration *int64     `json:"duration"`
		Width    *int       `json:"width"`
		Height   *int       `json:"height"`
	} `json:"video"`
	Images *[]douyinAddr `json:"images"`
}

func (f *Fetcher) fetchDouyin(ctx context.Context, l Link) (Result, error) {
	source := "douyin_detail"
	endpoint := "https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=" + l.ID + "&aid=6383"
	if l.Kind == "note" {
		source = "douyin_slides"
		endpoint = "https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/?aweme_ids=" + url.QueryEscape("["+l.ID+"]") + "&request_source=200"
	}
	b, status, h, err := f.request(ctx, endpoint, source)
	if err != nil {
		return Result{}, err
	}
	// 无登录证据的 403 是匿名接口没返回，不能断言作品已删或必须登录。
	if status == 403 {
		return Result{}, &FetchError{Kind: "not_returned", Source: source, Status: status}
	}
	if err := httpFailure(status, h, source); err != nil {
		return Result{}, err
	}
	if len(b) == 0 {
		return Result{}, failure("not_returned", source)
	}
	var e struct {
		Code  *int         `json:"status_code"`
		Item  *douyinItem  `json:"aweme_detail"`
		Items []douyinItem `json:"aweme_details"`
	}
	if json.Unmarshal(b, &e) != nil {
		return Result{}, failure("invalid_response", source)
	}
	if e.Code == nil {
		return Result{}, failure("invalid_response", source)
	}
	if *e.Code != 0 {
		return Result{}, &FetchError{Kind: "not_returned", Source: source, Code: *e.Code}
	}
	p := e.Item
	if l.Kind == "note" {
		if len(e.Items) == 0 {
			return Result{}, failure("not_returned", source)
		}
		if len(e.Items) != 1 {
			return Result{}, failure("identity_mismatch", source)
		}
		p = &e.Items[0]
	}
	if p == nil {
		return Result{}, failure("not_returned", source)
	}
	if string(p.ID) != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	if p.Description == nil {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Text, r.Published = previewField(p.Description), unixField(p.Created)
	if p.Author != nil {
		r.Author.Name = stringField(p.Author.Name)
		if p.Author.ID != "" {
			r.Author.ID = available(string(p.Author.ID))
		}
	}
	stat(&r, "likes", nil)
	stat(&r, "comments", nil)
	stat(&r, "shares", nil)
	if p.Statistics != nil {
		stat(&r, "likes", p.Statistics.Likes)
		stat(&r, "comments", p.Statistics.Comments)
		stat(&r, "shares", p.Statistics.Shares)
	}
	appendBody(&r)
	if p.Images != nil {
		r.Media = available([]Media{})
		for _, image := range *p.Images {
			m := imageMetadata(lastURL(image))
			m.Width, m.Height = intField(image.Width), intField(image.Height)
			appendMedia(&r, m)
		}
	} else if p.Video != nil {
		v := p.Video
		m := Media{Type: "video", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: lastURL(v.Cover), Width: intField(v.Width), Height: intField(v.Height), Duration: missing[float64]("interface_omitted"), Size: missing[int64]("interface_omitted")}
		if v.Duration != nil {
			if *v.Duration < 0 {
				return Result{}, failure("invalid_response", source)
			}
			m.Duration = available(float64(*v.Duration) / 1000)
		}
		appendMedia(&r, m)
	}
	return r, nil
}
func lastURL(a douyinAddr) Field[string] {
	if len(a.URLs) == 0 {
		return missing[string]("interface_omitted")
	}
	return available(a.URLs[len(a.URLs)-1])
}
