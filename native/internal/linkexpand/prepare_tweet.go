package linkexpand

// X 帖子的推特卡片（规格-链接发送改版 C1）：模板里的 $卡片 落在 X 帖子上时，照 X 网页画一张（linkcard.RenderTweet），
// 不用通用卡片。头像、媒体在这里下载、解码后交给渲染器；画上去的全部文字（正文、引用正文、名字）先交给 Guard 钩子；
// 正式装配里链接展开不过记忆守卫（ADR-312，app/linkexpand.go 的 Guard 是空函数），这一道只在装了守卫的调用方生效。
// 画不成就退回通用卡片，原因记进退化日志（“推特卡片”）。

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

// tweetCardMedia 是一张卡最多下几张图：正文四格加引用四格，再加两个头像
const tweetCardMedia = 4

// tweetCard 画 X 帖子的推特卡片。ok 为假表示不是 X 帖子或画不成（调用方接着画通用卡片）；err 只回守卫拦下、取消这类要停下的错误。
func (p *Preparer) tweetCard(ctx context.Context, r *linkreader.Result, caps types.MediaCaps, item Item) (loaded Loaded, semantic string, ok bool, err error) {
	if p.Cards == nil || r == nil || r.Platform != "x" || r.Kind != "post" {
		return Loaded{}, "", false, nil
	}
	tw := p.tweetData(ctx, r, true)
	if item.Translation != "" && len(tw.Flow) == 0 {
		// 有译文：卡片只画译文，不画原文，正文下面一行小字写翻译自哪种语言；长文（Flow）按原样排，不换译文
		tw.Text, tw.TranslationNote = item.Translation, item.TranslationNote
	}
	// 投票选项、引用框里的被引推文也只画译文（2026-11-14）；引用框没有“翻译自”那一行，主推文的小标记照旧
	if tw.Poll != nil && len(item.PollLabels) == len(tw.Poll.Choices) {
		for i, l := range item.PollLabels {
			if l != "" {
				tw.Poll.Choices[i].Label = l
			}
		}
	}
	if tw.Quote != nil && item.QuoteTranslation != "" {
		tw.Quote.Text = item.QuoteTranslation
	}
	if err := ctx.Err(); err != nil {
		return Loaded{}, "", false, err
	}
	semantic = tw.Semantic()
	if err := p.Guard(ctx, semantic); err != nil {
		return Loaded{}, "", false, err
	}
	out, e := p.Cards.RenderTweet(ctx, tw)
	if e != nil {
		if ctx.Err() != nil {
			return Loaded{}, "", false, ctx.Err()
		}
		p.degraded("推特卡片", e)
		return Loaded{}, "", false, nil
	}
	loaded, e = p.imageTransport(ctx, Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia(rand.Text(), out.MIME, out.Width, out.Height, out.Bytes)}, caps)
	if e != nil {
		p.degraded("推特卡片", e)
		return Loaded{}, "", false, nil
	}
	return loaded, semantic, true, nil
}

func avail[T any](f linkreader.Field[T]) (T, bool) { return f.Value, f.State == "available" }

func stat(r *linkreader.Result, key string) linkcard.Stat {
	if v, ok := avail(r.Stats[key]); ok {
		return linkcard.Stat{N: v, OK: true}
	}
	return linkcard.Stat{}
}

// tweetData 把取数结果换成卡片的内容；引用只画一层（top 为假时不再往下）
func (p *Preparer) tweetData(ctx context.Context, r *linkreader.Result, top bool) linkcard.Tweet {
	var tw linkcard.Tweet
	tw.Author.Name, _ = avail(r.Author.Name)
	tw.Author.Account, _ = avail(r.Author.Account)
	tw.Author.Verification, _ = avail(r.Author.Verification)
	tw.Text, _ = avail(r.Text)
	if t, ok := avail(r.Published); ok {
		tw.Published = t
	}
	if u, ok := avail(r.Author.Avatar); ok && u != "" {
		tw.Author.Avatar = p.cardBitmap(ctx, linkreader.Media{Type: "image", URL: linkreader.Field[string]{State: "available", Value: u}})
	}
	if top && p.Cards != nil && p.Cards.Uncapped() && flowWorthy(r) {
		tw.Flow = p.tweetFlow(ctx, r)
	}
	if media, ok := avail(r.Media); ok && len(tw.Flow) == 0 {
		for i, m := range media {
			if i == tweetCardMedia {
				// 第五张起不下载，只占一格让第四格写 +N
				tw.Media = append(tw.Media, make([]linkcard.TweetMedia, len(media)-i)...)
				break
			}
			tw.Media = append(tw.Media, p.cardMedia(ctx, m))
		}
	}
	tw.Replies, tw.Reposts, tw.Likes, tw.Views = stat(r, "replies"), stat(r, "reposts"), stat(r, "likes"), stat(r, "views")
	if !top {
		return tw
	}
	if r.Poll != nil {
		tw.Poll = cardPoll(r.Poll, nowFunc())
	}
	switch q := r.Quote; {
	case q.State == "available" && q.Value != nil:
		qt := p.tweetData(ctx, q.Value, false)
		tw.Quote = &qt
	case strings.HasPrefix(q.Reason, "quote_"):
		tw.QuoteGone = "引用的帖子不可用（可能已删除或不公开）"
	case q.State == "failed":
		tw.QuoteGone = "引用的帖子没取到"
	}
	return tw
}

// cardPoll 把投票换成卡片上的内容：每个选项的比例（接口没给比例就用票数除以总票数算），条下面一行“共 N 票 · 已结束”。
func cardPoll(p *linkreader.PollInfo, now time.Time) *linkcard.TweetPoll {
	if p == nil || len(p.Choices) == 0 {
		return nil
	}
	out := &linkcard.TweetPoll{}
	for _, c := range p.Choices {
		ch := linkcard.PollChoice{Label: c.Label}
		switch {
		case c.Percent.State == "available":
			ch.Percent, ch.OK = c.Percent.Value, true
		case c.Count.State == "available" && p.Total.State == "available" && p.Total.Value > 0:
			ch.Percent, ch.OK = float64(c.Count.Value)*100/float64(p.Total.Value), true
		}
		out.Choices = append(out.Choices, ch)
	}
	var foot []string
	if p.Total.State == "available" {
		foot = append(foot, fmt.Sprintf("共 %d 票", p.Total.Value))
	}
	if p.EndsAt.State == "available" {
		if p.EndsAt.Value.After(now) {
			foot = append(foot, "截止 "+stamp(p.EndsAt))
		} else {
			foot = append(foot, "已结束")
		}
	}
	out.Footer = strings.Join(foot, " · ")
	return out
}

// cardMedia 下载一张卡片用的图（视频、动图取缩略图并标播放钮）
func (p *Preparer) cardMedia(ctx context.Context, m linkreader.Media) linkcard.TweetMedia {
	tm := linkcard.TweetMedia{Video: m.Type == "video" || m.Type == "gif"}
	if w, ok := avail(m.Width); ok && w > 0 {
		if h, ok := avail(m.Height); ok && h > 0 {
			tm.Ratio = float64(h) / float64(w)
		}
	}
	src := m
	if tm.Video {
		src = linkreader.Media{Type: "image", URL: m.Thumbnail}
	}
	if u, ok := avail(src.URL); ok && u != "" {
		src.URL.Value = cardImageURL(u)
		tm.Image = p.cardBitmap(ctx, src)
	}
	return tm
}

// flowMediaMax 是长文卡片里最多下几张图；再多的不画，末尾写一句“还有 N 张图，看原文”
const flowMediaMax = 12

// flowWorthy 说这条帖子的正文该按“文字和图交替”的长文排：X 文章，或者有图夹在两段文字中间。
// 普通推文（文字在前、图在后）不算：它们仍用 X 的图片网格。
func flowWorthy(r *linkreader.Result) bool {
	if r.Article.State == "available" || r.Kind == "article" {
		return true
	}
	seenImage := false
	for _, b := range r.Blocks {
		switch b.Type {
		case "image", "photo", "article_cover":
			seenImage = true
		case "text":
			if seenImage && strings.TrimSpace(b.Text) != "" {
				return true
			}
		}
	}
	return false
}

// tweetFlow 把帖子的 Blocks 按顺序换成长文卡片的内容：文字是段落、文章标题是小标题、图按位置画、分隔线画线；
// 被引的帖子、投票这些另有位置的不放进来。
func (p *Preparer) tweetFlow(ctx context.Context, r *linkreader.Result) []linkcard.FlowBlock {
	var out []linkcard.FlowBlock
	images, skipped := 0, 0
	for _, b := range r.Blocks {
		switch b.Type {
		case "text", "link", "link_card":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, linkcard.FlowBlock{Kind: linkcard.FlowPara, Text: b.Text})
			}
		case "article_title":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, linkcard.FlowBlock{Kind: linkcard.FlowHeading, Text: b.Text})
			}
		case "divider":
			out = append(out, linkcard.FlowBlock{Kind: linkcard.FlowRule})
		case "image", "photo", "article_cover", "video", "gif":
			if b.Media == nil || !sendable(*b.Media) {
				continue
			}
			if images >= flowMediaMax {
				skipped++
				continue
			}
			images++
			out = append(out, linkcard.FlowBlock{Kind: linkcard.FlowImage, Media: p.cardMedia(ctx, *b.Media)})
		}
	}
	if skipped > 0 {
		out = append(out, linkcard.FlowBlock{Kind: linkcard.FlowPara, Text: fmt.Sprintf("还有 %d 张图，看原文", skipped)})
	}
	return out
}

// cardImageURL 把 X 图片换成 medium 档（最长边 1200）：卡片里一格最宽 673 像素，原图动辄几 MB，下载和解码都白费
func cardImageURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "pbs.twimg.com" || !u.Query().Has("name") {
		return raw
	}
	q := u.Query()
	q.Set("name", "medium")
	u.RawQuery = q.Encode()
	return u.String()
}

// cardBitmap 下载并解码一张卡片用的图；下不到、解不开返回 nil（卡片上画占位），原因记进退化日志
func (p *Preparer) cardBitmap(ctx context.Context, m linkreader.Media) *linkcard.Bitmap {
	if ctx.Err() != nil {
		return nil
	}
	l, err := p.loadImage(ctx, "x", m)
	if err != nil {
		if ctx.Err() == nil {
			p.degraded("推特卡片的图", err)
		}
		return nil
	}
	defer l.Resource.Release()
	var bmp *linkcard.Bitmap
	err = l.Resource.Read(func(r io.Reader) error {
		rs, ok := r.(io.ReadSeeker)
		if !ok {
			b, e := io.ReadAll(r)
			if e != nil {
				return e
			}
			bmp, e = linkcard.DecodeCover(b)
			return e
		}
		var e error
		bmp, e = linkcard.DecodeCoverReader(rs)
		return e
	})
	if err != nil {
		p.degraded("推特卡片的图", err)
		return nil
	}
	return bmp
}
