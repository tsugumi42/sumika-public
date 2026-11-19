package linkreader

import (
	"context"
	"encoding/json"
)

// pixiv 的系列：作品页自带的导航（seriesNavData）和两种系列页（漫画系列、小说系列）。
// 系列页匿名可取（2026-11-09 实测）：漫画 /ajax/series/<ID>?p=1，小说 /ajax/novel/series/<ID>。

// lenientID 是纯数字编号，缺、为 null、形状不对都当成空：这些是附带信息，不能让主体取数失败。
type lenientID string

func (d *lenientID) UnmarshalJSON(b []byte) error {
	var x decimalID
	if x.UnmarshalJSON(b) == nil {
		*d = lenientID(x)
	}
	return nil
}

type pixivNavLink struct {
	ID    lenientID `json:"id"`
	Title *string   `json:"title"`
	Order *int64    `json:"order"`
}

// mapPixivNav 把作品响应里的 seriesNavData 映射成 SeriesInfo。
// 这是附带信息：缺、为 null 或形状不对都返回 nil，不能让作品主体因此取数失败。
func mapPixivNav(raw json.RawMessage, fallbackType string) *SeriesInfo {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var nav struct {
		Type  string        `json:"seriesType"`
		ID    lenientID     `json:"seriesId"`
		Title *string       `json:"title"`
		Order *int64        `json:"order"`
		Prev  *pixivNavLink `json:"prev"`
		Next  *pixivNavLink `json:"next"`
	}
	if json.Unmarshal(raw, &nav) != nil || nav.ID == "" {
		return nil
	}
	info := &SeriesInfo{Type: nav.Type, ID: string(nav.ID), Title: stringField(nav.Title), Order: int64Field(nav.Order), Total: missing[int64]("not_applicable")}
	if info.Type == "" {
		info.Type = fallbackType
	}
	link := func(n *pixivNavLink) *SeriesLink {
		// 没有 ID 的（已删或不可见的上下话）不当作可点的导航
		if n == nil || n.ID == "" {
			return nil
		}
		return &SeriesLink{ID: string(n.ID), Title: stringField(n.Title), Order: int64Field(n.Order)}
	}
	info.Prev, info.Next = link(nav.Prev), link(nav.Next)
	return info
}

func (f *Fetcher) fetchPixivSeries(ctx context.Context, l Link, metadata bool) (Result, error) {
	if l.Kind == "novel_series" {
		return f.fetchPixivNovelSeries(ctx, l, metadata)
	}
	const source = "pixiv_series"
	var p struct {
		Series []struct {
			ID          lenientID `json:"id"`
			UserID      lenientID `json:"userId"`
			Title       *string   `json:"title"`
			Description *string   `json:"description"`
			Caption     *string   `json:"caption"`
			Total       *int64    `json:"total"`
			Cover       *string   `json:"url"`
			First       lenientID `json:"firstIllustId"`
			Latest      lenientID `json:"latestIllustId"`
			Created     *string   `json:"createDate"`
		} `json:"illustSeries"`
		Users []struct {
			ID   lenientID `json:"userId"`
			Name *string   `json:"name"`
		} `json:"users"`
	}
	if err := f.pixivJSON(ctx, "https://www.pixiv.net/ajax/series/"+l.ID+"?p=1", source, &p); err != nil {
		return Result{}, err
	}
	// 响应里的系列必须是请求的那一个，不拿别的系列兜底
	idx := -1
	for i := range p.Series {
		if string(p.Series[i].ID) == l.ID {
			idx = i
		}
	}
	if idx < 0 {
		return Result{}, failure("identity_mismatch", source)
	}
	s := p.Series[idx]
	if s.Title == nil || *s.Title == "" || (s.Total != nil && *s.Total < 0) {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = stringField(s.Title)
	if s.UserID != "" {
		r.Author.ID = available(string(s.UserID))
		for _, u := range p.Users {
			if u.ID == s.UserID {
				r.Author.Name = stringField(u.Name)
			}
		}
	}
	var err error
	if r.Published, err = timestampField(s.Created); err != nil {
		return Result{}, err
	}
	pixivFlags(&r, nil, nil)
	stat(&r, "total", s.Total)
	r.Series = &SeriesInfo{Type: "manga", ID: l.ID, Title: r.Title, Order: missing[int64]("not_applicable"), Total: int64Field(s.Total), FirstID: string(s.First), LatestID: string(s.Latest)}
	if metadata {
		r.Stage = "metadata"
		return r, nil
	}
	text := s.Description
	if text == nil || *text == "" {
		text = s.Caption
	}
	if text != nil {
		desc := pixivDescription(*text)
		r.Text = previewField(&desc)
	}
	if s.Cover != nil && *s.Cover != "" {
		cover := imageMetadata(pixivURL(s.Cover))
		cover.Referer = "https://www.pixiv.net/"
		r.Media = available([]Media{cover})
	}
	return r, nil
}

func (f *Fetcher) fetchPixivNovelSeries(ctx context.Context, l Link, metadata bool) (Result, error) {
	const source = "pixiv_novel_series"
	var p struct {
		ID         lenientID `json:"id"`
		UserID     lenientID `json:"userId"`
		UserName   *string   `json:"userName"`
		Title      *string   `json:"title"`
		Caption    *string   `json:"caption"`
		Restrict   *int      `json:"xRestrict"`
		AIType     *int      `json:"aiType"`
		Created    *string   `json:"createDate"`
		Total      *int64    `json:"total"`
		Published  *int64    `json:"publishedContentCount"`
		Characters *int64    `json:"publishedTotalCharacterCount"`
		First      lenientID `json:"firstNovelId"`
		Latest     lenientID `json:"latestNovelId"`
		Cover      *struct {
			URLs map[string]string `json:"urls"`
		} `json:"cover"`
	}
	if err := f.pixivJSON(ctx, "https://www.pixiv.net/ajax/novel/series/"+l.ID, source, &p); err != nil {
		return Result{}, err
	}
	if string(p.ID) != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	if p.Title == nil || *p.Title == "" {
		return Result{}, failure("invalid_response", source)
	}
	total := p.Total
	if total == nil {
		total = p.Published
	}
	if total != nil && *total < 0 {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = stringField(p.Title)
	r.Author.Name = stringField(p.UserName)
	if p.UserID != "" {
		r.Author.ID = available(string(p.UserID))
	}
	var err error
	if r.Published, err = timestampField(p.Created); err != nil {
		return Result{}, err
	}
	pixivFlags(&r, p.Restrict, p.AIType)
	stat(&r, "total", total)
	stat(&r, "characters", p.Characters)
	r.Series = &SeriesInfo{Type: "novel", ID: l.ID, Title: r.Title, Order: missing[int64]("not_applicable"), Total: int64Field(total), FirstID: string(p.First), LatestID: string(p.Latest)}
	if metadata {
		r.Stage = "metadata"
		return r, nil
	}
	if p.Caption != nil {
		desc := pixivDescription(*p.Caption)
		r.Text = previewField(&desc)
	}
	if p.Cover != nil {
		for _, size := range []string{"1200x1200", "480mw", "original"} {
			if u := p.Cover.URLs[size]; u != "" {
				cover := imageMetadata(available(u))
				cover.Referer = "https://www.pixiv.net/"
				r.Media = available([]Media{cover})
				break
			}
		}
	}
	return r, nil
}

// WorkURL 是系列里某一话的链接（系列卡里的“最新话”“首话”）；没有 ID 返回空串。
func (s *SeriesInfo) WorkURL(id string) string {
	if s == nil || id == "" {
		return ""
	}
	if s.Type == "novel" {
		return "https://www.pixiv.net/novel/show.php?id=" + id
	}
	return "https://www.pixiv.net/artworks/" + id
}
