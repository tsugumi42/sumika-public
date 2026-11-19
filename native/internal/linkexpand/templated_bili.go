package linkexpand

import (
	"fmt"
	"strconv"
	"strings"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktrunc"
)

// B 站后来补的几种页面（小店商品、专栏、番剧、课程、音频、合集）的字段和卡片。
// templated.go 里按站点键的几处 switch 只在对应 case 里转到这里，排法本身在这个文件。

// biliMoreSite 说这个模板站点是不是本文件负责的。
func biliMoreSite(key string) bool {
	switch key {
	case "bilibili-toy", "bilibili-workshop", "bilibili-article", "bilibili-bangumi", "bilibili-course", "bilibili-audio", "bilibili-collection":
		return true
	}
	return false
}

// yuanText 给价格补上“¥”；取不到返回 none。
func yuanText(f linkreader.Field[string]) val {
	if f.State == "available" && f.Value != "" {
		return some("¥" + f.Value)
	}
	return none
}

// toyCategory 是小游戏分类的中文说法：GAME 写成“游戏”，别的原样（接口给什么写什么）。
func toyCategory(f linkreader.Field[string]) val {
	v := optText(f)
	if v.ok && strings.EqualFold(v.text, "GAME") {
		return some("游戏")
	}
	return v
}

// ratingText 是“9.8（217261 人评分）”；没有评分人数只写分数，没有评分返回 none。
func ratingText(b *linkreader.BangumiInfo) val {
	if b == nil || b.Rating.State != "available" {
		return none
	}
	s := strconv.FormatFloat(b.Rating.Value, 'f', 1, 64)
	if b.RatingCount.State == "available" && b.RatingCount.Value > 0 {
		s += fmt.Sprintf("（%s 人评分）", compact(b.RatingCount.Value))
	}
	return some(s)
}

// itemsText 把合集前几个视频的标题写成《甲》《乙》《丙》。
func itemsText(c *linkreader.CollectionInfo) val {
	if c == nil || c.Items.State != "available" || len(c.Items.Value) == 0 {
		return none
	}
	var b strings.Builder
	for _, t := range c.Items.Value {
		b.WriteString("《" + linktrunc.Inline(t, 30) + "》")
	}
	return some(b.String())
}

func (b *builder) biliMoreFields(v map[string]val) {
	r := b.r
	set := func(id string, x val) { v[id] = x }
	stat := func(id, key string) { set(id, optCompact(r.Stats[key])) }
	switch b.site.Key {
	case "bilibili-article":
		set("site", some("B站专栏"))
		set("title", optText(r.Title))
		set("author", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		set("desc", optText(r.Text))
		set("hot", commentsVal(r))
		for id, key := range map[string]string{"views": "view", "likes": "like", "comments": "comment", "favorites": "favorite", "coins": "coin", "shares": "forward"} {
			stat(id, key)
		}
	case "bilibili-toy":
		set("site", some("B站小游戏"))
		set("title", optText(r.Title))
		set("author", optText(r.Author.Name))
		set("category", toyCategory(r.Category))
		set("published", optStamp(r.Published))
		set("when", shortStamp(r.Published))
		for id, key := range map[string]string{"plays": "play", "likes": "like", "favorites": "favorite", "comments": "comment", "shares": "share"} {
			stat(id, key)
		}
	case "bilibili-workshop":
		set("site", some("B站小店"))
		set("title", optText(r.Title))
		set("shop", optText(r.Author.Name))
		set("desc", optText(r.Text))
		set("tags", tagsVal(r, 10))
		if w := r.Workshop; w != nil {
			set("price", yuanText(w.Price))
			set("original", yuanText(w.OriginalPrice))
			set("sold", optText(w.Sold))
			set("delivery", optText(w.Delivery))
			if w.Status.State == "available" {
				set("status", some(w.Status.Value))
			}
		}
	case "bilibili-bangumi":
		set("site", some("B站番剧"))
		set("title", optText(r.Title))
		set("desc", optText(r.Text))
		set("styles", tagsVal(r, 6))
		set("published", dateVal(r.Published))
		for id, key := range map[string]string{"views": "view", "danmaku": "danmaku", "likes": "like", "coins": "coin", "follows": "follow"} {
			stat(id, key)
		}
		if bg := r.Bangumi; bg != nil {
			set("episode", optText(bg.Episode))
			set("status", optText(bg.Status))
			set("areas", optText(bg.Areas))
			if bg.Rating.State == "available" {
				set("rating", some(strconv.FormatFloat(bg.Rating.Value, 'f', 1, 64)))
			}
			set("ratingcount", optNumber(bg.RatingCount))
		}
	case "bilibili-course":
		set("site", some("B站课程"))
		set("title", optText(r.Title))
		set("desc", optText(r.Text))
		set("author", optText(r.Author.Name))
		stat("views", "view")
		if c := r.Course; c != nil {
			set("price", optText(c.Price))
			set("discount", optText(c.Discount))
			set("lessons", optText(c.Release))
			set("trial", optText(c.Trial))
		}
	case "bilibili-audio":
		set("site", some("B站音频"))
		set("title", optText(r.Title))
		set("desc", optText(r.Text))
		set("author", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		set("duration", durationVal(r))
		for id, key := range map[string]string{"views": "view", "favorites": "favorite", "comments": "reply", "shares": "share", "coins": "coin"} {
			stat(id, key)
		}
	case "bilibili-collection":
		set("title", optText(r.Title))
		set("desc", optText(r.Text))
		set("author", optText(r.Author.Name))
		stat("videos", "videos")
		set("site", some("B站合集"))
		if c := r.Collection; c != nil {
			set("kind", optText(c.Type))
			if c.Type.State == "available" {
				set("site", some("B站"+c.Type.Value))
			}
			set("items", itemsText(c))
		}
	}
}

// biliMoreCard 给 genericCard 补这几种页面的作者行、日期行和统计行。
func (b *builder) biliMoreCard(data *cardParts) {
	r := b.r
	data.date = nil // 日期行由各页面自己排（发布时间、价格、评分……），不沿用 genericCard 先放进来的发布时间
	who := func(label string) {
		if s := optText(r.Author.Name); s.ok {
			data.author = label + s.text
		}
	}
	line := func(keys, labels []string) {
		if s := stats(r, keys, labels, cardSep); s != "" {
			data.lines = append(data.lines, s)
		}
	}
	switch b.site.Key {
	case "bilibili-article":
		who("UP主：")
		if p := optStamp(r.Published); p.ok {
			data.date = append(data.date, p.text)
		}
		line([]string{"view", "like", "comment"}, []string{"阅读", "点赞", "评论"})
		line([]string{"favorite", "coin", "forward"}, []string{"收藏", "投币", "转发"})
	case "bilibili-toy":
		who("作者：")
		if c := toyCategory(r.Category); c.ok {
			data.date = append(data.date, c.text)
		}
		if p := shortStamp(r.Published); p.ok {
			data.date = append(data.date, p.text)
		}
		line([]string{"play", "like", "favorite"}, []string{"游玩", "点赞", "收藏"})
		line([]string{"comment", "share"}, []string{"评论", "分享"})
	case "bilibili-workshop":
		who("店铺：")
		if w := r.Workshop; w != nil {
			if p := yuanText(w.Price); p.ok {
				price := "价格：" + p.text
				if o := yuanText(w.OriginalPrice); o.ok {
					price += "（原价 " + o.text + "）"
				}
				data.date = append(data.date, price)
			}
			if w.Status.State == "available" {
				data.date = append(data.date, "状态："+w.Status.Value)
			}
			if s := optText(w.Sold); s.ok {
				data.lines = append(data.lines, "已售 "+s.text)
			}
		}
	case "bilibili-bangumi":
		if bg := r.Bangumi; bg != nil {
			if s := optText(bg.Status); s.ok {
				data.author = s.text
			}
			if e := optText(bg.Episode); e.ok {
				data.title = e.text // 接在卡片标题后面另起一行
			}
		}
		if s := ratingText(r.Bangumi); s.ok {
			data.date = append(data.date, "评分："+s.text)
		}
		if p := dateVal(r.Published); p.ok {
			data.date = append(data.date, "首播："+p.text)
		}
		line([]string{"view", "danmaku", "like"}, []string{"播放", "弹幕", "点赞"})
		line([]string{"coin", "follow", "share"}, []string{"投币", "追番", "转发"})
	case "bilibili-course":
		who("UP主：")
		if c := r.Course; c != nil {
			if p := optText(c.Price); p.ok {
				price := "价格：" + p.text
				if d := optText(c.Discount); d.ok {
					price += "（" + d.text + "）"
				}
				data.date = append(data.date, price)
			}
			var info []string
			for _, f := range []linkreader.Field[string]{c.Release, c.Trial} {
				if s := optText(f); s.ok {
					info = append(info, s.text)
				}
			}
			if len(info) > 0 {
				data.lines = append(data.lines, strings.Join(info, "｜"))
			}
		}
		line([]string{"view"}, []string{"播放"})
	case "bilibili-audio":
		who("UP主：")
		if d := durationVal(r); d.ok {
			data.date = append(data.date, "时长："+d.text)
		}
		if p := optStamp(r.Published); p.ok {
			data.date = append(data.date, "发布："+p.text)
		}
		line([]string{"view", "favorite", "reply"}, []string{"播放", "收藏", "评论"})
		line([]string{"share", "coin"}, []string{"分享", "投币"})
	case "bilibili-collection":
		who("UP主：")
		if c := r.Collection; c != nil {
			if t := optText(c.Type); t.ok {
				data.date = append(data.date, t.text)
			}
			if n := optNumber(r.Stats["videos"]); n.ok {
				data.date = append(data.date, "共 "+n.text+" 个视频")
			}
			if it := itemsText(c); it.ok {
				data.lines = append(data.lines, linktrunc.Inline(it.text, 60))
			}
		}
	}
}

// cardParts 是 genericCard 里正在拼的几块：标题的补充行、作者行、日期行（用 ｜ 连）、统计行。
type cardParts struct {
	title  string // 非空时接在卡片标题后面（番剧的“第几集”）
	author string
	date   []string
	lines  []string
}

// upowerVal 是充电专属视频的标注（linkreader 的 UpowerLabel：只给能确认的，有试看时一并写出）。
func upowerVal(r *linkreader.Result) val {
	if s := r.UpowerLabel(); s != "" {
		return some(s)
	}
	return none
}

// commentsVal 是前几条热评，或没有评论的明确说明（评论不公开、评论区关闭；暂无评论、这次没取到）；旧缓存没有这个字段就不写。
func commentsVal(r *linkreader.Result) val {
	if r.Comments.Reason == "no_comments" {
		return none // 没人评论不用占一行
	}
	if s := linkreader.CommentsText(r.Comments, 80); s != "" {
		return some(s)
	}
	return none
}
