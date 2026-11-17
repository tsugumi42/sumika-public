package linkreader

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

type xArticleMedia struct {
	ID   string `json:"media_id"`
	Info struct {
		Type   string  `json:"__typename"`
		URL    *string `json:"original_img_url"`
		Width  *int    `json:"original_img_width"`
		Height *int    `json:"original_img_height"`
	} `json:"media_info"`
}
type xArticleEntity struct {
	Type string `json:"type"`
	Data struct {
		URL      *string `json:"url"`
		Markdown *string `json:"markdown"`
		TweetID  string  `json:"tweetId"`
		Items    []struct {
			ID string `json:"mediaId"`
		} `json:"mediaItems"`
	} `json:"data"`
}
type xArticle struct {
	Title   *string        `json:"title"`
	Cover   *xArticleMedia `json:"cover_media"`
	Content *struct {
		Blocks *[]struct {
			Type   string  `json:"type"`
			Text   *string `json:"text"`
			Ranges []struct {
				Key int `json:"key"`
			} `json:"entityRanges"`
		} `json:"blocks"`
		Entities []struct {
			Key   string         `json:"key"`
			Value xArticleEntity `json:"value"`
		} `json:"entityMap"`
	} `json:"content"`
	Media []xArticleMedia `json:"media_entities"`
}

// Draft.js 的 entityMap 是乱序键值数组；原文位置只由 blocks 和 entityRanges 决定。
func mapXArticle(r *Result, raw json.RawMessage, depth int) error {
	var a xArticle
	if json.Unmarshal(raw, &a) != nil {
		return failure("invalid_response", "fx_article")
	}
	r.Title = stringField(a.Title)
	if a.Title != nil {
		r.Blocks = append(r.Blocks, Block{Type: "article_title", Text: *a.Title})
	}
	if a.Cover != nil {
		m := articleImage(*a.Cover)
		r.Blocks = append(r.Blocks, Block{Type: "article_cover", Media: &m})
		r.Media.Value = append(r.Media.Value, m)
	}
	if a.Content == nil || a.Content.Blocks == nil {
		r.Article = missing[string]("article_body_omitted")
		return nil
	}
	entities := make(map[string]xArticleEntity, len(a.Content.Entities))
	for _, e := range a.Content.Entities {
		entities[e.Key] = e.Value
	}
	media := make(map[string]xArticleMedia, len(a.Media))
	for _, m := range a.Media {
		media[m.ID] = m
	}
	var body strings.Builder
	complete := true
	for i, b := range *a.Content.Blocks {
		if b.Text == nil {
			return failure("invalid_response", "fx_article")
		}
		if b.Type != "atomic" {
			block := Block{Type: "text", Text: *b.Text, SourceType: b.Type}
			switch b.Type {
			case "unstyled", "blockquote", "header-one", "header-two", "header-three", "unordered-list-item", "ordered-list-item", "code-block":
			default:
				block.State, block.Reason, complete = "unsupported", "unknown_article_block", false
			}
			for _, ref := range b.Ranges {
				e, found := entities[strconv.Itoa(ref.Key)]
				if !found {
					block.State, block.Reason, complete = "not_returned", "article_entity_omitted", false
					continue
				}
				if e.Type == "LINK" {
					block.Links = append(block.Links, ExternalLink{URL: stringField(e.Data.URL), Original: missing[string]("not_applicable"), Display: missing[string]("interface_omitted")})
				}
			}
			r.Blocks = append(r.Blocks, block)
			if i > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(*b.Text)
			continue
		}
		if len(b.Ranges) == 0 {
			r.Blocks = append(r.Blocks, Block{Type: "unsupported", State: "not_returned", Reason: "article_entity_omitted"})
			complete = false
		}
		for _, ref := range b.Ranges {
			e, found := entities[strconv.Itoa(ref.Key)]
			if !found {
				r.Blocks = append(r.Blocks, Block{Type: "unsupported", State: "not_returned", Reason: "article_entity_omitted"})
				complete = false
				continue
			}
			switch e.Type {
			case "MEDIA":
				if len(e.Data.Items) == 0 {
					r.Blocks = append(r.Blocks, Block{Type: "unsupported", State: "not_returned", Reason: "article_media_omitted"})
					complete = false
				}
				for _, item := range e.Data.Items {
					m, found := media[item.ID]
					if !found {
						r.Blocks = append(r.Blocks, Block{Type: "image", State: "not_returned", Reason: "article_media_omitted"})
						complete = false
						continue
					}
					image := articleImage(m)
					if m.Info.Type != "ApiImage" {
						r.Blocks = append(r.Blocks, Block{Type: "unsupported", State: "unsupported", Reason: "unknown_article_media"})
						complete = false
						continue
					}
					r.Blocks = append(r.Blocks, Block{Type: "image", Media: &image})
					r.Media.Value = append(r.Media.Value, image)
				}
			case "TWEET":
				if !digits(e.Data.TweetID) {
					return failure("invalid_response", "fx_article")
				}
				if depth >= 1 {
					r.MoreQuotes = available(true)
					r.Blocks = append(r.Blocks, Block{Type: "quote", State: "not_returned", Reason: "deeper_quote_omitted"})
					complete = false
					continue
				}
				q := newResult(Link{Platform: "x", Kind: "post", ID: e.Data.TweetID, Clean: xStatusURL(nil, e.Data.TweetID)}, "fx_v2") // 长文实体只给推文 ID、没有作者，先写 /i/status/；后面补取到这条推文时有了作者账号，发出去按 ShareURL 写成作者的链接，补取不到才留 /i/status/（x.com 会跳到作者那里）
				q.Text = missing[string]("inline_tweet_not_fetched")
				r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: &q, State: "not_returned", Reason: "inline_tweet_not_fetched"})
			case "MARKDOWN":
				if e.Data.Markdown == nil {
					r.Blocks = append(r.Blocks, Block{Type: "text", State: "not_returned", Reason: "article_markdown_omitted"})
					complete = false
					continue
				}
				r.Blocks = append(r.Blocks, Block{Type: "text", Text: *e.Data.Markdown, SourceType: "markdown"})
				if body.Len() > 0 {
					body.WriteByte('\n')
				}
				body.WriteString(*e.Data.Markdown)
			case "DIVIDER":
				r.Blocks = append(r.Blocks, Block{Type: "divider"})
			default:
				r.Blocks = append(r.Blocks, Block{Type: "unsupported", SourceType: e.Type, State: "unsupported", Reason: "unknown_article_entity"})
				complete = false
			}
		}
	}
	r.Article = available(body.String())
	r.Article.Truncated = !complete
	return nil
}
func articleImage(m xArticleMedia) Media {
	image := xImage(m.Info.URL, m.Info.Width, m.Info.Height)
	if image.URL.State == "available" {
		image.URL.Value = originalImage(image.URL.Value)
	}
	return image
}

// 只补文章明确引用的 status ID，不把正文链接或未知实体当成联网目标。
func (f *Fetcher) fillArticleTweets(ctx context.Context, r *Result) {
	results := map[string]*Result{}
	roots := []*Result{r}
	if r.Quote.Value != nil {
		roots = append(roots, r.Quote.Value)
	}
	for _, root := range roots {
		for i := range root.Blocks {
			b := &root.Blocks[i]
			if b.Reason != "inline_tweet_not_fetched" {
				continue
			}
			id := b.Quote.ID
			if id == r.ID || id == root.ID {
				// 同文回指不建指针环；序列化和下游遍历必须有界。
				b.State, b.Reason = "not_returned", "inline_tweet_cycle"
				root.Article.Truncated = true
				continue
			}
			if previous := results[id]; previous != nil {
				b.Quote = previous
				b.State, b.Reason = "available", ""
				continue
			}
			var envelope struct {
				Code   *int   `json:"code"`
				Status *xPost `json:"status"`
			}
			err := f.getJSON(ctx, "https://api.fxtwitter.com/2/status/"+id, "fx_v2", &envelope)
			if err == nil {
				switch {
				case envelope.Code == nil:
					err = failure("invalid_response", "fx_v2")
				case *envelope.Code != 200:
					err = xBusiness(*envelope.Code, "fx_v2")
				case envelope.Status == nil || envelope.Status.ID != id:
					err = failure("identity_mismatch", "fx_v2")
				default:
					q, mapErr := mapX(Link{Platform: "x", Kind: "post", ID: id, Clean: b.Quote.CleanURL}, envelope.Status, 1, map[string]bool{})
					err = mapErr
					if err == nil {
						*b.Quote = q
						results[id] = b.Quote
						b.State, b.Reason = "available", ""
					}
				}
			}
			if err != nil {
				e := err.(*FetchError)
				b.State, b.Reason = "not_returned", e.Kind
				b.Quote.Text = missing[string]("inline_tweet_" + e.Kind)
				root.Article.Truncated = true
			}
		}
	}
}
