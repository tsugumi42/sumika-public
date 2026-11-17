package linkreader

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// B 站小游戏页（www.bilibili.com/toy/<slug>/index.html）：外壳页里的 window.__TOY_META__={toy_id:<数>} 给出编号，
// 再取 api.bilibili.com/x/toy/detail?id=<编号>（带 /toy/<slug>/ 的 Referer，匿名，不用 WBI）。
// 外壳页对 /toy/ 后面随便接的路径也回 200 和别的 toy_id（实测 /toy/api/ 会回另一个小游戏），所以必须核对详情里 url 的 slug
// 和链接一致，不一致当不存在。
const toySource = "bili_toy"

var toyMeta = regexp.MustCompile(`__TOY_META__\s*=\s*\{[^{}]*?["']?toy_id["']?\s*:\s*["']?(\d{1,20})`)

// toySlug 是 /toy/ 后的那一段：字母数字开头，后面可有下划线和横线。
var toySlug = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type toyDetail struct {
	ID         decimalID `json:"id"`
	Title      *string   `json:"title"`
	PosterURL  *string   `json:"poster_url"`
	PosterW    *int      `json:"poster_width"`
	PosterH    *int      `json:"poster_height"`
	URL        *string   `json:"url"`
	DynID      *string   `json:"dyn_id"`
	Category   *string   `json:"category"`
	CreateTime *int64    `json:"ctime"`
	ModifyTime *int64    `json:"mtime"`
	User       *struct {
		Mid  decimalID `json:"mid"`
		Name *string   `json:"name"`
		Face *string   `json:"face"`
	} `json:"user_info"`
	DynStat *struct {
		Like     *int64 `json:"like"`
		Favorite *int64 `json:"favorite"`
		Comment  *int64 `json:"comment"`
		Share    *int64 `json:"share"`
	} `json:"dyn_stat"`
	Stat *struct {
		PV *int64 `json:"pv"`
	} `json:"stat"`
}

// toyURLSlug 取详情里 url 的 slug；host 不是 www.bilibili.com 或路径不是 /toy/<slug>/… 返回空。
func toyURLSlug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "www.bilibili.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "toy" {
		return ""
	}
	return parts[1]
}

func (f *Fetcher) fetchBiliToy(ctx context.Context, l Link) (Result, error) {
	if !toySlug.MatchString(l.ID) {
		return Result{}, failure("invalid_target", toySource)
	}
	shellCtx := withHeader(ctx, "Accept", "text/html")
	page, status, h, err := f.request(shellCtx, l.Clean, toySource+"_page")
	if err != nil {
		return Result{}, err
	}
	if err = httpFailure(status, h, toySource+"_page"); err != nil {
		return Result{}, err
	}
	m := toyMeta.FindSubmatch(page)
	if m == nil {
		return Result{}, failure("not_found", toySource)
	}
	id := string(m[1])
	var e struct {
		Code *int       `json:"code"`
		Data *toyDetail `json:"data"`
	}
	detailCtx := withHeader(ctx, "Referer", "https://www.bilibili.com/toy/"+l.ID+"/")
	if err = f.getJSON(detailCtx, "https://api.bilibili.com/x/toy/detail?id="+url.QueryEscape(id), toySource, &e); err != nil {
		return Result{}, err
	}
	if e.Code == nil {
		return Result{}, failure("invalid_response", toySource)
	}
	if *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, toySource)
	}
	v := e.Data
	if v == nil || v.Title == nil || strings.TrimSpace(*v.Title) == "" {
		return Result{}, failure("invalid_response", toySource)
	}
	// 编号对不上，或详情给的页面不是这条链接（外壳对乱接的路径也会回别的小游戏）：当不存在
	if string(v.ID) != id || v.URL == nil || toyURLSlug(*v.URL) != l.ID {
		return Result{}, failure("not_found", toySource)
	}
	r := newResult(l, toySource)
	r.Title = stringField(v.Title)
	r.Category = stringField(v.Category)
	if v.CreateTime != nil {
		r.Published = unixField(v.CreateTime)
	}
	if v.User != nil {
		r.Author.Name = stringField(v.User.Name)
		if v.User.Mid != "" {
			r.Author.ID = available(string(v.User.Mid))
		}
		if v.User.Face != nil && *v.User.Face != "" {
			r.Author.Avatar = available(httpsURL(*v.User.Face))
		}
	}
	var like, fav, comment, share, pv *int64
	if v.DynStat != nil {
		like, fav, comment, share = v.DynStat.Like, v.DynStat.Favorite, v.DynStat.Comment, v.DynStat.Share
	}
	if v.Stat != nil {
		pv = v.Stat.PV
	}
	stat(&r, "play", pv)
	stat(&r, "like", like)
	stat(&r, "favorite", fav)
	stat(&r, "comment", comment)
	stat(&r, "share", share)
	if v.ModifyTime != nil {
		r.Flags["modified_at"] = available[jsonValue](*v.ModifyTime)
	}
	if v.DynID != nil && digits(*v.DynID) {
		r.Flags["dyn_id"] = available[jsonValue](*v.DynID)
	}
	appendBody(&r)
	if v.PosterURL != nil && *v.PosterURL != "" {
		m := imageMetadata(available(httpsURL(*v.PosterURL)))
		if v.PosterW != nil && *v.PosterW > 0 {
			m.Width = available(*v.PosterW)
		}
		if v.PosterH != nil && *v.PosterH > 0 {
			m.Height = available(*v.PosterH)
		}
		appendMedia(&r, m)
	}
	return r, nil
}
