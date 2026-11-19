package linkreader

import (
	"context"
	"net/url"
	"strings"
)

// 普通动态详情 x/polymer/web-dynamic/v1/detail：opus/detail 渲染不了的动态（转发、视频分享、抽奖等）走这里。
// opus/detail 对这类动态回 data.item=null、fallback={type:1,id:<动态号>}（2026-11-13 实测一条转发动态；
// 文档只写了“已知 2 为专栏”，type 1 是实测结论，不是文档）。加 features=itemOpusStyle 后图文原动态是 MAJOR_TYPE_OPUS，
// 不加则是 MAJOR_TYPE_DRAW；两种、以及视频、专栏、番剧等卡片都按下面的分支认，认不出的不整条失败。

const (
	dynamicSource   = "bili_dynamic"
	dynamicFeatures = "itemOpusStyle"
)

type dynNode struct {
	Type     lv[string] `json:"type"`
	Text     lv[string] `json:"text"`
	OrigText lv[string] `json:"orig_text"`
}
type dynRich struct {
	Text  lv[string]    `json:"text"`
	Nodes lv[[]dynNode] `json:"rich_text_nodes"`
}
type dynPic struct {
	URL    lv[string] `json:"url"`
	Src    lv[string] `json:"src"`
	Width  lv[int]    `json:"width"`
	Height lv[int]    `json:"height"`
}

// dynCard 是视频、专栏、番剧、课程、直播、通用卡片这些“主体是一张卡”的共同字段，各类型取到哪个用哪个。
type dynCard struct {
	Title    lv[string]   `json:"title"`
	SubTitle lv[string]   `json:"sub_title"`
	Desc     lv[string]   `json:"desc"`
	Label    lv[string]   `json:"label"`
	Cover    lv[string]   `json:"cover"`
	Covers   lv[[]string] `json:"covers"`
	JumpURL  lv[string]   `json:"jump_url"`
	Bvid     lv[string]   `json:"bvid"`
	Duration lv[string]   `json:"duration_text"`
}
type dynMajor struct {
	Type lv[string] `json:"type"`
	Opus lv[struct {
		Title   lv[string]   `json:"title"`
		Summary lv[dynRich]  `json:"summary"`
		Pics    lv[[]dynPic] `json:"pics"`
	}] `json:"opus"`
	Draw lv[struct {
		Items lv[[]dynPic] `json:"items"`
	}] `json:"draw"`
	Archive   lv[dynCard] `json:"archive"`
	Article   lv[dynCard] `json:"article"`
	Common    lv[dynCard] `json:"common"`
	PGC       lv[dynCard] `json:"pgc"`
	Music     lv[dynCard] `json:"music"`
	Courses   lv[dynCard] `json:"courses"`
	UGCSeason lv[dynCard] `json:"ugc_season"`
	Live      lv[dynCard] `json:"live"`
	Medialist lv[dynCard] `json:"medialist"`
	None      lv[struct {
		Tips lv[string] `json:"tips"`
	}] `json:"none"`
	Blocked lv[struct {
		Hint lv[string] `json:"hint_message"`
	}] `json:"blocked"`
}
type dynExtra struct { // 动态下面挂的预约、抽奖、投票、商品等
	Type    lv[string] `json:"type"`
	Reserve lv[struct {
		Title lv[string]  `json:"title"`
		Desc1 lv[dynRich] `json:"desc1"`
		Desc2 lv[dynRich] `json:"desc2"`
	}] `json:"reserve"`
	Vote lv[struct {
		Desc lv[string] `json:"desc"`
	}] `json:"vote"`
	Common lv[struct {
		Title lv[string] `json:"title"`
		Desc1 lv[string] `json:"desc1"`
		Desc2 lv[string] `json:"desc2"`
	}] `json:"common"`
	UGC lv[struct {
		Title lv[string] `json:"title"`
	}] `json:"ugc"`
	Goods lv[struct {
		Head lv[string] `json:"head_text"`
	}] `json:"goods"`
}
type dynItem struct {
	ID      lv[string] `json:"id_str"`
	Type    lv[string] `json:"type"`
	Visible lv[bool]   `json:"visible"`
	Basic   lv[struct {
		JumpURL lv[string] `json:"jump_url"`
	}] `json:"basic"`
	Modules lv[struct {
		Author lv[struct {
			Name    lv[string]    `json:"name"`
			Mid     lv[decimalID] `json:"mid"`
			Face    lv[string]    `json:"face"`
			PubTime lv[string]    `json:"pub_time"`
			PubTS   lv[int64]     `json:"pub_ts"`
		}] `json:"module_author"`
		Dynamic lv[struct {
			Desc       lv[dynRich]  `json:"desc"`
			Major      lv[dynMajor] `json:"major"`
			Additional lv[dynExtra] `json:"additional"`
		}] `json:"module_dynamic"`
		Stat lv[struct {
			Like lv[struct {
				Count lv[int64] `json:"count"`
			}] `json:"like"`
			Comment lv[struct {
				Count lv[int64] `json:"count"`
			}] `json:"comment"`
			Forward lv[struct {
				Count lv[int64] `json:"count"`
			}] `json:"forward"`
		}] `json:"module_stat"`
	}] `json:"modules"`
	Orig lv[dynItem] `json:"orig"`
}

// dynamicDetail 请求普通动态详情。item 为空（接口没给）返回 invalid_response。
func (f *Fetcher) dynamicDetail(ctx context.Context, id string) (*dynItem, error) {
	var envelope struct {
		Code *int `json:"code"`
		Data lv[struct {
			Item lv[dynItem] `json:"item"`
		}] `json:"data"`
	}
	q := url.Values{"id": {id}, "timezone_offset": {"-480"}, "features": {dynamicFeatures}}
	if err := f.articleJSON(ctx, "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?"+q.Encode(), dynamicSource, &envelope, func() *int { return envelope.Code }); err != nil {
		return nil, err
	}
	if envelope.Code == nil {
		return nil, failure("invalid_response", dynamicSource)
	}
	if envelope.Data.V == nil || envelope.Data.V.Item.V == nil {
		return nil, failure("invalid_response", dynamicSource)
	}
	return envelope.Data.V.Item.V, nil
}

func (f *Fetcher) fetchDynamic(ctx context.Context, l Link) (Result, error) {
	item, err := f.dynamicDetail(ctx, l.ID)
	if err != nil {
		return Result{}, err
	}
	if sv(item.ID) != l.ID {
		return Result{}, failure("identity_mismatch", dynamicSource)
	}
	return dynamicResult(l, item)
}

func sv(s lv[string]) string {
	if s.V == nil {
		return ""
	}
	return *s.V
}

// dynText 把富文本节点拼成一段文字：优先 text，缺了拼节点，节点取 text 或 orig_text（表情是 [名字]、@ 带 @）。
func dynText(d lv[dynRich]) string {
	if d.V == nil {
		return ""
	}
	if t := sv(d.V.Text); t != "" {
		return t
	}
	var b strings.Builder
	if d.V.Nodes.V != nil {
		for _, n := range *d.V.Nodes.V {
			if t := sv(n.Text); t != "" {
				b.WriteString(t)
			} else {
				b.WriteString(sv(n.OrigText))
			}
		}
	}
	return b.String()
}

// dynamicResult 把一条普通动态排成统一结果。转发的原动态放进 Quote（只展开一层，原动态再转发的不再往下取）。
// 动态类型（DYNAMIC_TYPE_*）只记在 Flags["dynamic_type"]；主体类型认不出时不失败：作者和正文照出，类型进 Flags["unrendered_parts"]，
// 作者和正文一样都没有才报错，错误的 Detail 写清动态类型和主体类型。
func dynamicResult(l Link, item *dynItem) (Result, error) {
	return dynamicResultAt(l, item, 0)
}

func dynamicResultAt(l Link, item *dynItem, depth int) (Result, error) {
	r := newResult(l, dynamicSource)
	for _, key := range []string{"like", "comment", "forward"} {
		stat(&r, key, nil)
	}
	typ := sv(item.Type)
	if typ != "" {
		r.Flags["dynamic_type"] = available[jsonValue](typ)
	}
	m := item.Modules.V
	if m == nil {
		return Result{}, dynamicFailure(typ, "", "no_modules")
	}
	if a := m.Author.V; a != nil {
		r.Author.Name = optString(a.Name)
		if a.Mid.V != nil {
			r.Author.ID = available(string(*a.Mid.V))
		}
		if face := sv(a.Face); face != "" {
			r.Author.Avatar = available(httpsURL(face))
		}
		r.PublishedText = optString(a.PubTime)
		if a.PubTS.V != nil {
			r.Published = unixField(a.PubTS.V)
		}
	}
	if s := m.Stat.V; s != nil {
		if s.Like.V != nil {
			stat(&r, "like", s.Like.V.Count.V)
		}
		if s.Comment.V != nil {
			stat(&r, "comment", s.Comment.V.Count.V)
		}
		if s.Forward.V != nil {
			stat(&r, "forward", s.Forward.V.Count.V)
		}
	}
	var body strings.Builder
	media := make([]Media, 0)
	addText := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		if len(r.Blocks) > 0 && body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(t)
		r.Blocks = append(r.Blocks, Block{Type: "text", Text: t})
	}
	addImage := func(p dynPic) {
		u := sv(p.URL)
		if u == "" {
			u = sv(p.Src)
		}
		if u == "" {
			return
		}
		u = httpsURL(u)
		appendOpusImage(&r, &media, opusPicture{URL: &u, Width: p.Width.V, Height: p.Height.V}, nil)
	}
	var unrendered []string
	majorType := ""
	d := m.Dynamic.V
	if d != nil {
		addText(dynText(d.Desc))
		if mj := d.Major.V; mj != nil {
			majorType = sv(mj.Type)
			if !dynamicMajor(&r, mj, addText, addImage) {
				unrendered = append(unrendered, "major:"+majorType)
			}
		}
		if ex := d.Additional.V; ex != nil {
			if !dynamicExtra(ex, addText) && sv(ex.Type) != "" {
				unrendered = append(unrendered, "additional:"+sv(ex.Type))
			}
		}
	}
	// 被转发的原动态在展开里只出 Blocks：标题放最前面一块（和专栏一样《标题》），不然原动态的标题就丢了
	if depth > 0 && r.Title.State == "available" && r.Title.Value != "" {
		r.Blocks = append([]Block{{Type: "article_title", Text: r.Title.Value}}, r.Blocks...)
	}
	if item.Orig.V != nil && typ == "DYNAMIC_TYPE_FORWARD" {
		dynamicQuote(&r, item.Orig.V, depth, addText)
	}
	if len(unrendered) > 0 {
		r.Flags["unrendered_parts"] = available[jsonValue](unrendered)
	}
	// 作者、正文、图一样都没拿到：这条没东西可发，报错并把类型写进日志，别返回一条空的成功结果
	if body.Len() == 0 && len(media) == 0 && r.Quote.State != "available" && r.Author.Name.State != "available" {
		return Result{}, dynamicFailure(typ, majorType, "empty")
	}
	r.Media = available(media)
	r.Text.State, r.Text.Value, r.Text.Reason = "available", body.String(), ""
	return r, nil
}

func dynamicFailure(typ, major, why string) *FetchError {
	e := failure("invalid_response", dynamicSource)
	e.Detail = "动态类型 " + typ + "，主体类型 " + major + "，" + why
	return e
}

func optString(p lv[string]) Field[string] {
	if p.V == nil {
		return missing[string]("interface_omitted")
	}
	return available(*p.V)
}

// dynamicQuote 把转发的原动态展开成 Quote。原动态被删（类型 NONE、不可见）写一句平台给的提示，Quote 标 deleted。
// 原动态再转发别的动态时只写它自己的文字，不再往下展开（和 X 的引用一样只一层）。
func dynamicQuote(r *Result, orig *dynItem, depth int, addText func(string)) {
	origID := sv(orig.ID)
	deleted := sv(orig.Type) == "DYNAMIC_TYPE_NONE" || (orig.Visible.V != nil && !*orig.Visible.V)
	if deleted || origID == "" || depth > 0 {
		if d := orig.Modules.V; deleted && d != nil && d.Dynamic.V != nil && d.Dynamic.V.Major.V != nil && d.Dynamic.V.Major.V.None.V != nil {
			if tips := sv(d.Dynamic.V.Major.V.None.V.Tips); tips != "" {
				addText(tips)
			}
		}
		if deleted {
			r.Quote = missing[*Result]("deleted")
		} else {
			r.Quote = missing[*Result]("nested_quote_not_expanded")
			addText("还转发了另一条动态，看原文")
		}
		return
	}
	ql, ok := Recognize("https://www.bilibili.com/opus/" + origID)
	if !ok {
		r.Quote = missing[*Result]("interface_omitted")
		return
	}
	q, err := dynamicResultAt(ql, orig, depth+1)
	if err != nil {
		r.Quote = Field[*Result]{State: "failed", Reason: "invalid_response"}
		return
	}
	q.Source = dynamicSource
	r.Quote = available(&q)
	r.Blocks = append(r.Blocks, Block{Type: "quote", Quote: &q})
}

// dynamicMajor 把动态主体写进正文和图片，返回是不是认识这个主体类型（没有主体也算认识）。
func dynamicMajor(r *Result, mj *dynMajor, addText func(string), addImage func(dynPic)) bool {
	switch sv(mj.Type) {
	case "", "MAJOR_TYPE_NONE":
		if t := mj.None.V; t != nil && sv(t.Tips) != "" {
			addText(sv(t.Tips))
		}
		return true
	case "MAJOR_TYPE_OPUS":
		o := mj.Opus.V
		if o == nil {
			return true
		}
		if t := sv(o.Title); t != "" {
			r.Title = available(t)
		}
		addText(dynText(o.Summary))
		if o.Pics.V != nil {
			for _, p := range *o.Pics.V {
				addImage(p)
			}
		}
	case "MAJOR_TYPE_DRAW":
		if o := mj.Draw.V; o != nil && o.Items.V != nil {
			for _, p := range *o.Items.V {
				addImage(p)
			}
		}
	case "MAJOR_TYPE_BLOCKED":
		if b := mj.Blocked.V; b != nil {
			addText(sv(b.Hint))
		}
	case "MAJOR_TYPE_ARCHIVE":
		dynamicCard(mj.Archive, "视频", addText, addImage)
	case "MAJOR_TYPE_ARTICLE":
		dynamicCard(mj.Article, "专栏", addText, addImage)
	case "MAJOR_TYPE_COMMON":
		dynamicCard(mj.Common, "", addText, addImage)
	case "MAJOR_TYPE_PGC":
		dynamicCard(mj.PGC, "番剧", addText, addImage)
	case "MAJOR_TYPE_MUSIC":
		dynamicCard(mj.Music, "音频", addText, addImage)
	case "MAJOR_TYPE_COURSES":
		dynamicCard(mj.Courses, "课程", addText, addImage)
	case "MAJOR_TYPE_UGC_SEASON":
		dynamicCard(mj.UGCSeason, "合集", addText, addImage)
	case "MAJOR_TYPE_LIVE":
		dynamicCard(mj.Live, "直播", addText, addImage)
	case "MAJOR_TYPE_MEDIALIST":
		dynamicCard(mj.Medialist, "收藏夹", addText, addImage)
	default:
		return false
	}
	return true
}

// dynamicCard 写一张卡：“[类别] 标题”、简介（专栏、视频才有）、链接，封面作为图。
func dynamicCard(c lv[dynCard], kind string, addText func(string), addImage func(dynPic)) {
	if c.V == nil {
		return
	}
	title := sv(c.V.Title)
	var lines []string
	head := title
	if kind != "" && title != "" {
		head = "[" + kind + "] " + title
	}
	if head != "" {
		lines = append(lines, head)
	}
	if s := sv(c.V.SubTitle); s != "" {
		lines = append(lines, s)
	}
	if s := sv(c.V.Desc); s != "" && s != title {
		lines = append(lines, s)
	}
	if bv := sv(c.V.Bvid); bv != "" {
		lines = append(lines, "https://www.bilibili.com/video/"+bv)
	} else if u := sv(c.V.JumpURL); u != "" {
		lines = append(lines, httpsURL(u))
	}
	addText(strings.Join(lines, "\n"))
	cover := sv(c.V.Cover)
	if cover == "" && c.V.Covers.V != nil && len(*c.V.Covers.V) > 0 {
		cover = (*c.V.Covers.V)[0]
	}
	if cover != "" {
		addImage(dynPic{URL: lv[string]{V: &cover}})
	}
}

// dynamicExtra 写动态下面挂的预约、投票、商品等，返回是不是认识。
func dynamicExtra(ex *dynExtra, addText func(string)) bool {
	switch sv(ex.Type) {
	case "":
		return true
	case "ADDITIONAL_TYPE_RESERVE":
		if v := ex.Reserve.V; v != nil {
			addText(strings.Join(dynNonEmpty(sv(v.Title), dynText(v.Desc1), dynText(v.Desc2)), "\n"))
		}
	case "ADDITIONAL_TYPE_VOTE":
		if v := ex.Vote.V; v != nil {
			addText("[投票] " + sv(v.Desc))
		}
	case "ADDITIONAL_TYPE_COMMON":
		if v := ex.Common.V; v != nil {
			addText(strings.Join(dynNonEmpty(sv(v.Title), sv(v.Desc1), sv(v.Desc2)), "\n"))
		}
	case "ADDITIONAL_TYPE_UGC":
		if v := ex.UGC.V; v != nil {
			addText(sv(v.Title))
		}
	case "ADDITIONAL_TYPE_GOODS":
		if v := ex.Goods.V; v != nil {
			addText(sv(v.Head))
		}
	default:
		return false
	}
	return true
}

func dynNonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
