package linkreader

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// B 站专栏（read/cv）。专栏页本身是客户端渲染的空壳，正文要通过它对应的 opus（图文）取：
//  1. x/polymer/web-dynamic/v1/detail?rid=<cv>&type=64：cv 号换 opus 号（data.item.id_str），同一响应还有标题、摘要、封面
//  2. 换不到时退路：请求 /read/cv<号>/ 不跟随跳转、读 Location 里的 opus 号（x/article/view 不再用，原因见 articleOpusFallback）
//  3. opus/detail?id=<opus>&features=…：结构化正文；basic.comment_id_str 必须等于 cv 号，否则当身份对不上
//  4. 正文取不到：用 detail 的摘要出卡片（正文标未完整）；detail 也没有就用 x/article/viewinfo（标题、作者、统计、封面）
//
// 统计（阅读、收藏、投币）来自 viewinfo，失败只缺统计。实测与取法见 data/dev/bilibili-link-coverage/专栏正文取法.md。
const articleSource = "bili_article"

var opusPath = regexp.MustCompile(`^/opus/([0-9]{1,20})/?$`)

type articleBrief struct {
	OpusID               string
	Title, Desc          string
	Covers               []string
	AuthorName, Face     string
	Mid                  int64
	Published            *int64
	Like, Comment, Share *int64
}

// articleJSON 只发一次请求。风控（412、-352）直接报 risk_control，平台已冷却（rate.go riskCool），
// 不再换标识重试：那样是风控之后紧接着的第二个请求，正是要避免的。
func (f *Fetcher) articleJSON(ctx context.Context, raw, source string, out any, code func() *int) error {
	err := f.getJSON(ctx, raw, source, out)
	if err == nil {
		if c := code(); c != nil && *c != 0 {
			err = biliFailure(*c, source)
		}
	}
	return err
}

func (f *Fetcher) articleDetail(ctx context.Context, cv string) (*articleBrief, error) {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Item *struct {
				ID    string `json:"id_str"`
				Basic struct {
					CommentID string `json:"comment_id_str"`
				} `json:"basic"`
				Modules struct {
					Author *struct {
						Name *string `json:"name"`
						Face *string `json:"face"`
						Mid  *int64  `json:"mid"`
						TS   *int64  `json:"pub_ts"`
					} `json:"module_author"`
					Dynamic *struct {
						Major *struct {
							Article *struct {
								Title  *string  `json:"title"`
								Desc   *string  `json:"desc"`
								Covers []string `json:"covers"`
							} `json:"article"`
						} `json:"major"`
					} `json:"module_dynamic"`
					Stat *struct {
						Like *struct {
							Count *int64 `json:"count"`
						} `json:"like"`
						Comment *struct {
							Count *int64 `json:"count"`
						} `json:"comment"`
						Forward *struct {
							Count *int64 `json:"count"`
						} `json:"forward"`
					} `json:"module_stat"`
				} `json:"modules"`
			} `json:"item"`
		} `json:"data"`
	}
	const source = "bili_article_detail"
	raw := "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?type=64&rid=" + url.QueryEscape(cv)
	if err := f.articleJSON(ctx, raw, source, &e, func() *int { return e.Code }); err != nil {
		return nil, err
	}
	if e.Code == nil || e.Data == nil || e.Data.Item == nil || !digitsOnly(e.Data.Item.ID) {
		return nil, failure("invalid_response", source)
	}
	it := e.Data.Item
	if it.Basic.CommentID != cv {
		return nil, failure("identity_mismatch", source)
	}
	b := &articleBrief{OpusID: it.ID}
	m := it.Modules
	if m.Author != nil {
		b.AuthorName, b.Face, b.Published = deref(m.Author.Name), deref(m.Author.Face), m.Author.TS
		if m.Author.Mid != nil {
			b.Mid = *m.Author.Mid
		}
	}
	if m.Dynamic != nil && m.Dynamic.Major != nil && m.Dynamic.Major.Article != nil {
		a := m.Dynamic.Major.Article
		b.Title, b.Desc, b.Covers = deref(a.Title), deref(a.Desc), a.Covers
	}
	if m.Stat != nil {
		if m.Stat.Like != nil {
			b.Like = m.Stat.Like.Count
		}
		if m.Stat.Comment != nil {
			b.Comment = m.Stat.Comment.Count
		}
		if m.Stat.Forward != nil {
			b.Share = m.Stat.Forward.Count
		}
	}
	return b, nil
}

func digitsOnly(s string) bool { return digits(s) && len(s) <= 20 }

// articleOpusFallback 是 detail 取不到时换 opus 号的退路：专栏页有时 301 到 /opus/<号>；失败返回空串。
// x/article/view 不再用：匿名请求它的记录是 3 次成功后 7 次全回 -352 或 -509（专栏正文取法.md），
// 这两个码会让整个 B 站平台冷却，为一条拿不稳的退路连累别的链接不值得。
func (f *Fetcher) articleOpusFallback(ctx context.Context, cv string) string {
	// 不跟随跳转，只读 Location（间歇性，所以只当退路）
	_, status, h, err := f.request(ctx, "https://www.bilibili.com/read/cv"+cv+"/", "bili_article_page")
	if err != nil || (status != 301 && status != 302 && status != 308) {
		return ""
	}
	loc, err := url.Parse(h.Get("Location"))
	if err != nil || (loc.Host != "" && loc.Host != "www.bilibili.com") {
		return ""
	}
	if m := opusPath.FindStringSubmatch(loc.Path); m != nil {
		return m[1]
	}
	return ""
}

func (f *Fetcher) articleBody(ctx context.Context, l Link, cv, opus string) (Result, error) {
	item, _, err := f.opusDetail(ctx, opus, true)
	if err != nil {
		return Result{}, err
	}
	if item == nil {
		return Result{}, failure("invalid_response", "bili_opus")
	}
	if item.ID != opus || item.Basic.CommentID != cv {
		return Result{}, failure("identity_mismatch", "bili_opus")
	}
	return opusResult(l, item)
}

type articleInfo struct {
	Title, Author string
	Mid           int64
	Stats         map[string]*int64
	Images        []string
}

func (f *Fetcher) articleInfo(ctx context.Context, cv string) (*articleInfo, error) {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Title  *string           `json:"title"`
			Mid    *int64            `json:"mid"`
			Author *string           `json:"author_name"`
			Stats  map[string]*int64 `json:"stats"`
			Images []string          `json:"image_urls"`
		} `json:"data"`
	}
	const source = "bili_article_info"
	if err := f.getJSON(ctx, "https://api.bilibili.com/x/article/viewinfo?id="+url.QueryEscape(cv), source, &e); err != nil {
		return nil, err
	}
	if e.Code == nil || e.Data == nil {
		return nil, failure("invalid_response", source)
	}
	if *e.Code != 0 {
		return nil, biliFailure(*e.Code, source)
	}
	i := &articleInfo{Title: deref(e.Data.Title), Author: deref(e.Data.Author), Stats: e.Data.Stats, Images: e.Data.Images}
	if e.Data.Mid != nil {
		i.Mid = *e.Data.Mid
	}
	return i, nil
}

func (f *Fetcher) fetchArticle(ctx context.Context, l Link) (Result, error) {
	cv := strings.TrimPrefix(l.ID, "cv")
	brief, derr := f.articleDetail(ctx, cv)
	var fe *FetchError
	if errors.As(derr, &fe) && (fe.Kind == "not_found" || fe.Kind == "identity_mismatch") {
		return Result{}, derr
	}
	opus, first := "", derr
	if brief != nil {
		opus = brief.OpusID
	} else {
		opus = f.articleOpusFallback(ctx, cv)
	}
	var r Result
	haveBody := false
	if opus != "" {
		var err error
		if r, err = f.articleBody(ctx, l, cv, opus); err == nil {
			haveBody = true
		} else if first == nil {
			first = err
		}
	}
	var info *articleInfo
	var ierr error
	// 统计只是锦上添花；正文已取到、预算又紧时不取，留给别的链接
	if !haveBody || f.rate.spare("bilibili", optionalKeep) {
		info, ierr = f.articleInfo(ctx, cv)
		if ierr != nil && first == nil {
			first = ierr
		}
	}
	if !haveBody {
		if brief == nil && info == nil {
			return Result{}, first
		}
		r = newResult(l, articleSource)
		for _, k := range []string{"like", "comment", "forward"} {
			stat(&r, k, nil)
		}
		r.Text = missing[string]("article_body_unavailable")
	}
	if brief != nil {
		fillArticleBrief(&r, brief, haveBody)
	}
	if info != nil {
		fillArticleInfo(&r, info)
	}
	if r.Title.State != "available" {
		return Result{}, failure("invalid_response", articleSource)
	}
	// 正文里没有图时，用封面当图（有正文图就不重复）
	if r.Media.State != "available" || len(r.Media.Value) == 0 {
		var covers []string
		if brief != nil {
			covers = brief.Covers
		}
		if len(covers) == 0 && info != nil {
			covers = info.Images
		}
		if len(covers) > 0 {
			r.Media = available([]Media{})
			appendMedia(&r, imageMetadata(available(httpsURL(covers[0]))))
		}
	}
	r.Comments = f.biliComments(ctx, cv, 12)
	return r, nil
}

// fillArticleBrief 用 detail 的内容补结果；正文已取到时只补缺的字段，没取到时摘要当正文开头并标未完整。
func fillArticleBrief(r *Result, b *articleBrief, haveBody bool) {
	if r.Title.State != "available" && b.Title != "" {
		r.Title = available(b.Title)
	}
	if r.Author.Name.State != "available" && b.AuthorName != "" {
		r.Author.Name = available(b.AuthorName)
	}
	if r.Author.Avatar.State != "available" && b.Face != "" {
		r.Author.Avatar = available(httpsURL(b.Face))
	}
	if r.Author.ID.State != "available" && b.Mid > 0 {
		r.Author.ID = available(strconv.FormatInt(b.Mid, 10))
	}
	if r.Published.State != "available" && b.Published != nil {
		r.Published = unixField(b.Published)
	}
	if !haveBody && b.Desc != "" {
		r.Text = Field[string]{State: "available", Value: b.Desc, Truncated: true}
		appendBody(r)
	}
	for k, v := range map[string]*int64{"like": b.Like, "comment": b.Comment, "forward": b.Share} {
		if r.Stats[k].State != "available" && v != nil {
			stat(r, k, v)
		}
	}
}

func fillArticleInfo(r *Result, i *articleInfo) {
	if r.Title.State != "available" && i.Title != "" {
		r.Title = available(i.Title)
	}
	if r.Author.Name.State != "available" && i.Author != "" {
		r.Author.Name = available(i.Author)
	}
	if r.Author.ID.State != "available" && i.Mid > 0 {
		r.Author.ID = available(strconv.FormatInt(i.Mid, 10))
	}
	for key, src := range map[string]string{"view": "view", "favorite": "favorite", "coin": "coin"} {
		if v, ok := i.Stats[src]; ok && v != nil {
			stat(r, key, v)
		}
	}
	for key, src := range map[string]string{"like": "like", "comment": "reply", "forward": "share"} {
		if v, ok := i.Stats[src]; ok && v != nil && r.Stats[key].State != "available" {
			stat(r, key, v)
		}
	}
}
