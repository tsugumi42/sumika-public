package linkreader

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

var pixivUploadedImage = regexp.MustCompile(`\[uploadedimage:([^\]]+)\]`)

type pixivNovel struct {
	ID             decimalID       `json:"id"`
	Title          *string         `json:"title"`
	UserID         decimalID       `json:"userId"`
	UserName       *string         `json:"userName"`
	Created        *string         `json:"createDate"`
	Restrict       *int            `json:"xRestrict"`
	AIType         *int            `json:"aiType"`
	Description    *string         `json:"description"`
	Content        *string         `json:"content"`
	CharacterCount *int64          `json:"characterCount"`
	CoverURL       *string         `json:"coverUrl"`
	SeriesNav      json.RawMessage `json:"seriesNavData"`
	Tags           *struct {
		Tags *[]struct {
			Tag string `json:"tag"`
		} `json:"tags"`
	} `json:"tags"`
	Images *map[string]struct {
		URLs struct {
			Original *string `json:"original"`
			Regular  *string `json:"1200x1200"`
		} `json:"urls"`
		Width  *int `json:"width"`
		Height *int `json:"height"`
	} `json:"textEmbeddedImages"`
}

func (f *Fetcher) fetchPixivNovel(ctx context.Context, l Link, metadata bool) (Result, error) {
	const source = "pixiv_novel"
	var p pixivNovel
	if err := f.pixivJSON(ctx, "https://www.pixiv.net/ajax/novel/"+l.ID, source, &p); err != nil {
		return Result{}, err
	}
	if string(p.ID) != l.ID {
		return Result{}, failure("identity_mismatch", source)
	}
	if p.Title == nil || *p.Title == "" || (p.CharacterCount != nil && *p.CharacterCount < 0) {
		return Result{}, failure("invalid_response", source)
	}
	r := newResult(l, source)
	r.Title = stringField(p.Title)
	r.Author.Name = stringField(p.UserName)
	if p.UserID != "" {
		r.Author.ID = available(string(p.UserID))
	}
	var err error
	r.Published, err = timestampField(p.Created)
	if err != nil {
		return Result{}, err
	}
	pixivFlags(&r, p.Restrict, p.AIType)
	r.Series = mapPixivNav(p.SeriesNav, "novel")
	if metadata {
		r.Stage = "metadata"
		return r, nil
	}
	r.Novel = &NovelInfo{Description: stringField(p.Description), CharacterCount: missing[int64]("interface_omitted"), Cover: missing[Media]("interface_omitted")}
	if p.CoverURL != nil && *p.CoverURL != "" {
		// 封面和正文里的插图分开放：它不进 Media，免得被当成第一张正文插图
		r.Novel.Cover = available(pixivImage(pixivURLs{Original: p.CoverURL}, nil, nil))
	}
	if p.Description != nil {
		description := pixivDescription(*p.Description)
		r.Novel.Description = previewField(&description)
	}
	if p.CharacterCount != nil {
		r.Novel.CharacterCount = available(*p.CharacterCount)
	}
	if p.Tags != nil && p.Tags.Tags != nil {
		tags := make([]string, 0, len(*p.Tags.Tags))
		for _, tag := range *p.Tags.Tags {
			tags = append(tags, tag.Tag)
		}
		r.Tags = available(tags)
	}
	r.Text = previewField(p.Content)
	if p.Images != nil {
		r.Blocks = make([]Block, 0, 1+len(*p.Images))
	}
	appendBody(&r)
	if p.Images != nil {
		r.Media = available(make([]Media, 0, len(*p.Images)))
		seen := make(map[string]bool, len(*p.Images))
		add := func(id string) {
			if seen[id] {
				return
			}
			image, ok := (*p.Images)[id]
			if !ok {
				r.Media.Truncated = true
				r.Media.Reason = "embedded_image_not_returned"
				return
			}
			seen[id] = true
			appendMedia(&r, pixivImage(pixivURLs{Original: image.URLs.Original, Regular: image.URLs.Regular}, image.Width, image.Height))
		}
		// JSON 对象没有顺序；先按全文中的插图标记排序，不能受正文预览截断影响。
		if p.Content != nil {
			for _, match := range pixivUploadedImage.FindAllStringSubmatch(*p.Content, -1) {
				add(match[1])
			}
		}
		rest := make([]string, 0, len(*p.Images)-len(seen))
		for id := range *p.Images {
			if !seen[id] {
				rest = append(rest, id)
			}
		}
		sort.Strings(rest)
		for _, id := range rest {
			add(id)
		}
	}
	return r, nil
}

func pixivDescription(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	var out strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(out.String())
		case html.TextToken:
			out.Write(z.Text())
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "br", "p", "div":
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
			}
		}
	}
}
