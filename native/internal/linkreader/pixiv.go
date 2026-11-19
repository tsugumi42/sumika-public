package linkreader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type pixivURLs struct {
	Original *string `json:"original"`
	Regular  *string `json:"regular"`
}
type pixivWork struct {
	ID        decimalID `json:"illustId"`
	Title     *string   `json:"illustTitle"`
	UserID    decimalID `json:"userId"`
	UserName  *string   `json:"userName"`
	Account   *string   `json:"userAccount"`
	Created   *string   `json:"createDate"`
	URLs      pixivURLs `json:"urls"`
	Width     *int      `json:"width"`
	Height    *int      `json:"height"`
	Pages     *int      `json:"pageCount"`
	Restrict  *int      `json:"xRestrict"`
	Type      *int      `json:"illustType"`
	AIType    *int      `json:"aiType"`
	Bookmarks *int64    `json:"bookmarkCount"`
	Views     *int64    `json:"viewCount"`
	// 系列导航是附带信息，用原始 JSON 接，形状不对也不影响作品本身
	SeriesNav json.RawMessage `json:"seriesNavData"`
	Tags      *struct {
		Tags *[]struct {
			Tag string `json:"tag"`
		} `json:"tags"`
	} `json:"tags"`
}

func (f *Fetcher) fetchPixiv(ctx context.Context, l Link, metadata bool) (Result, error) {
	switch l.Kind {
	case "novel":
		return f.fetchPixivNovel(ctx, l, metadata)
	case "series", "novel_series":
		return f.fetchPixivSeries(ctx, l, metadata)
	}
	const source = "pixiv_ajax"
	var p pixivWork
	base := "https://www.pixiv.net/ajax/illust/" + l.ID
	if err := f.pixivJSON(ctx, base, source, &p); err != nil {
		return Result{}, err
	}
	if string(p.ID) != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	if p.Title == nil || *p.Title == "" {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = stringField(p.Title)
	r.Author.Name, r.Author.Account = stringField(p.UserName), stringField(p.Account)
	if p.UserID != "" {
		r.Author.ID = available(string(p.UserID))
	}
	var err error
	r.Published, err = timestampField(p.Created)
	if err != nil {
		return Result{}, err
	}
	stat(&r, "bookmarks", p.Bookmarks)
	stat(&r, "views", p.Views)
	stat(&r, "pages", nil)
	if p.Pages != nil {
		if *p.Pages < 1 {
			return Result{}, failure("invalid_response", source)
		}
		r.Stats["pages"] = available(int64(*p.Pages))
	}
	pixivFlags(&r, p.Restrict, p.AIType)
	if p.Type != nil {
		r.Flags["illust_type"] = available[jsonValue](*p.Type)
	}
	r.Series = mapPixivNav(p.SeriesNav, "manga")
	if p.Tags != nil && p.Tags.Tags != nil {
		tags := make([]string, 0, len(*p.Tags.Tags))
		for _, tag := range *p.Tags.Tags {
			tags = append(tags, tag.Tag)
		}
		r.Tags = available(tags)
	}
	if metadata {
		r.Stage = "metadata"
		return r, nil
	}
	// 图片下载另步做；只把固定 Referer 和返回地址一起保存，不能携带 Cookie。
	// 首页多图 URL 可能全空；原图是否可取，以正常 pages 响应为准。
	originalMissing := p.URLs.Original == nil || *p.URLs.Original == ""
	firstMissing := originalMissing && (p.URLs.Regular == nil || *p.URLs.Regular == "")
	if originalMissing || (p.Pages != nil && *p.Pages > 1) {
		var pages []struct {
			URLs   pixivURLs `json:"urls"`
			Width  *int      `json:"width"`
			Height *int      `json:"height"`
		}
		err := f.pixivJSON(ctx, base+"/pages", "pixiv_pages", &pages)
		if err == nil && (len(pages) == 0 || (p.Pages != nil && len(pages) != *p.Pages)) {
			err = failure("invalid_response", "pixiv_pages")
		}
		if err != nil {
			reason := pixivFailureReason(err)
			if firstMissing {
				r.Media = missing[[]Media](reason)
			} else {
				// 多页成功时一次分配完整页表；失败才保留可用首图。
				appendMedia(&r, pixivImage(p.URLs, p.Width, p.Height))
				r.Media.Truncated, r.Media.Reason = true, reason
			}
		} else {
			r.Media = available(make([]Media, 0, len(pages)))
			r.Blocks = make([]Block, 0, len(pages))
			for _, page := range pages {
				appendMedia(&r, pixivImage(page.URLs, page.Width, page.Height))
			}
		}
	} else {
		appendMedia(&r, pixivImage(p.URLs, p.Width, p.Height))
	}
	if p.Pages == nil && r.Media.State == "available" {
		r.Media.Truncated = true
		r.Media.Reason = "page_count_not_returned"
	}
	if p.Type != nil && *p.Type == 2 {
		r.Ugoira = f.fetchUgoira(ctx, base+"/ugoira_meta")
	}
	return r, nil
}
func pixivImage(urls pixivURLs, width, height *int) Media {
	image := imageMetadata(pixivURL(urls.Original))
	image.Thumbnail = pixivURL(urls.Regular)
	if image.URL.State != "available" && image.Thumbnail.State == "available" {
		image.URL = image.Thumbnail
		image.URL.Reason = "original_not_returned_using_regular"
	} else if image.URL.State == "available" && image.Thumbnail.State == "available" && image.URL.Value != image.Thumbnail.Value {
		image.Fallback = &MediaFallback{URL: image.Thumbnail, AboveBytes: 100 * 1024 * 1024, Reason: "original_too_large", Notice: "原图超过单张上限，使用 regular 图片"}
	}
	image.Width, image.Height = intField(width), intField(height)
	image.Referer = "https://www.pixiv.net/"
	return image
}

func pixivURL(p *string) Field[string] {
	if p == nil || *p == "" {
		return missing[string]("interface_omitted")
	}
	return available(*p)
}

func pixivFlags(r *Result, restrict, ai *int) {
	r.Flags["x_restrict"] = missing[jsonValue]("interface_omitted")
	r.Flags["possibly_sensitive"] = missing[jsonValue]("interface_omitted")
	r.Flags["age_rating"] = missing[jsonValue]("interface_omitted")
	if restrict != nil {
		r.Flags["x_restrict"] = available[jsonValue](*restrict)
		if *restrict >= 0 && *restrict <= 2 {
			r.Flags["possibly_sensitive"] = available[jsonValue](*restrict != 0)
			r.Flags["age_rating"] = available[jsonValue]([]string{"all_ages", "R18", "R18-G"}[*restrict])
		} else {
			r.Flags["age_rating"] = missing[jsonValue]("unknown_x_restrict")
			r.Flags["possibly_sensitive"] = missing[jsonValue]("unknown_x_restrict")
		}
	}
	r.Flags["ai_type"] = missing[jsonValue]("interface_omitted")
	r.Flags["ai_generated"] = missing[jsonValue]("interface_omitted")
	if ai != nil {
		r.Flags["ai_type"] = available[jsonValue](*ai)
		if *ai == 1 || *ai == 2 {
			r.Flags["ai_generated"] = available[jsonValue](*ai == 2)
		} else {
			r.Flags["ai_generated"] = missing[jsonValue]("unknown_ai_type")
		}
	}
}

func (f *Fetcher) fetchUgoira(ctx context.Context, raw string) *UgoiraInfo {
	const source = "pixiv_ugoira"
	info := &UgoiraInfo{ZIP: missing[string]("interface_omitted"), Frames: missing[[]UgoiraFrame]("interface_omitted"), MIMEType: missing[string]("interface_omitted"), Referer: "https://www.pixiv.net/"}
	var p struct {
		Original *string `json:"originalSrc"`
		Src      *string `json:"src"`
		MIMEType *string `json:"mime_type"`
		Frames   *[]struct {
			File  *string `json:"file"`
			Delay *int    `json:"delay"`
		} `json:"frames"`
	}
	err := f.pixivJSON(ctx, raw, source, &p)
	if err != nil {
		reason := pixivFailureReason(err)
		info.ZIP = missing[string](reason)
		info.Frames = missing[[]UgoiraFrame](reason)
		return info
	}
	info.ZIP = pixivURL(p.Original)
	if info.ZIP.State != "available" {
		info.ZIP = pixivURL(p.Src)
		if info.ZIP.State == "available" {
			info.ZIP.Reason = "original_zip_not_returned_using_src"
		}
	}
	info.MIMEType = stringField(p.MIMEType)
	if p.Frames != nil && len(*p.Frames) != 0 {
		frames := make([]UgoiraFrame, 0, len(*p.Frames))
		for _, frame := range *p.Frames {
			// 文件名会用于 ZIP 取帧，不能让外部路径跨目录；延迟保持毫秒原值。
			if frame.File == nil || *frame.File == "" || strings.ContainsAny(*frame.File, `/\:`) || *frame.File == "." || *frame.File == ".." || frame.Delay == nil || *frame.Delay < 0 {
				info.Frames = missing[[]UgoiraFrame]("invalid_response")
				return info
			}
			frames = append(frames, UgoiraFrame{File: *frame.File, DelayMS: *frame.Delay})
		}
		info.Frames = available(frames)
	}
	return info
}

func pixivFailureReason(err error) string {
	var fe *FetchError
	if errors.As(err, &fe) {
		if fe.Reason != "" {
			return fe.Reason
		}
		return fe.Kind
	}
	return "failed"
}

func (f *Fetcher) pixivJSON(ctx context.Context, raw, source string, out any) error {
	// 拒绝响应的 body 可能是 []，先看 error 再解主体，保留真正的拒绝原因。
	var envelope struct {
		Error *bool           `json:"error"`
		Body  json.RawMessage `json:"body"`
	}
	if err := f.getJSON(ctx, raw, source, &envelope); err != nil {
		var fe *FetchError
		if errors.As(err, &fe) && (fe.Status == 401 || fe.Status == 403) {
			return &FetchError{Kind: "not_returned", Source: source, Status: fe.Status, Reason: "anonymous_access_denied"}
		}
		return err
	}
	if envelope.Error == nil {
		return failure("invalid_response", source)
	}
	if *envelope.Error {
		return &FetchError{Kind: "not_returned", Source: source, Reason: "api_rejected_anonymous_request"}
	}
	if len(envelope.Body) == 0 || string(envelope.Body) == "null" || json.Unmarshal(envelope.Body, out) != nil {
		return failure("invalid_response", source)
	}
	return nil
}
