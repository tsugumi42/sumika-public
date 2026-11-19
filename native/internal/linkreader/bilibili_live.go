package linkreader

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// B 站直播间和个人主页：只用不登录的公开接口，取到什么写什么，取不到的字段标 not_returned。
//   直播间：api.live.bilibili.com/room/v1/Room/get_info（标题、分区、状态、人气、封面）
//           + live_user/v1/Master/info（主播名、头像、粉丝数；这一步失败不影响直播间本身）
//   个人主页：api.bilibili.com/x/web-interface/card（名字、签名、粉丝、关注、投稿数、获赞数、认证）
// 遇到风控（-352、-412）或登录要求，原样报 risk_control / login_required，不绕过。

const (
	biliLiveSource  = "bili_live"
	biliSpaceSource = "bili_space"
)

type biliLiveRoom struct {
	UID        decimalID `json:"uid"`
	RoomID     decimalID `json:"room_id"`
	ShortID    decimalID `json:"short_id"`
	Title      *string   `json:"title"`
	Desc       *string   `json:"description"`
	Status     *int      `json:"live_status"`
	Online     *int64    `json:"online"`
	Attention  *int64    `json:"attention"`
	Cover      *string   `json:"user_cover"`
	Keyframe   *string   `json:"keyframe"`
	AreaName   *string   `json:"area_name"`
	ParentArea *string   `json:"parent_area_name"`
	LiveTime   *string   `json:"live_time"`
	Tags       *string   `json:"tags"`
}

var biliLiveZone = time.FixedZone("+08:00", 8*60*60)

func (f *Fetcher) fetchBiliLive(ctx context.Context, l Link) (Result, error) {
	var e struct {
		Code *int          `json:"code"`
		Data *biliLiveRoom `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.live.bilibili.com/room/v1/Room/get_info?room_id="+url.QueryEscape(l.ID), biliLiveSource, &e); err != nil {
		return Result{}, err
	}
	if e.Code == nil {
		return Result{}, failure("invalid_response", biliLiveSource)
	}
	if *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, biliLiveSource)
	}
	v := e.Data
	if v == nil || v.Title == nil || v.RoomID == "" {
		return Result{}, failure("invalid_response", biliLiveSource)
	}
	if string(v.RoomID) != trimLeadingZero(l.ID) && string(v.ShortID) != trimLeadingZero(l.ID) {
		return Result{}, failure("identity_mismatch", biliLiveSource)
	}
	r := newResult(l, biliLiveSource)
	r.Title = stringField(v.Title)
	r.Text = stringField(plainText(v.Desc))
	if v.UID != "" {
		r.Author.ID = available(string(v.UID))
	}
	if v.Status != nil {
		r.Flags["live_status"] = available[jsonValue](*v.Status) // 0 未开播，1 直播中，2 轮播
	}
	// 没在直播（未开播、轮播）时接口的人气是 0，不是真实人气，记成没有
	if v.Online != nil && *v.Online == 0 && (v.Status == nil || *v.Status != 1) {
		r.Stats["online"] = missing[int64]("not_live")
	} else {
		stat(&r, "online", v.Online)
	}
	// 直播间的 attention 就是关注这个直播间的人数（= 主播粉丝数），不是“主播关注了多少人”：
	// 记成粉丝；主播信息接口给了 follower_num 时以它为准。不另出一个“关注”，免得同一个数写两遍
	stat(&r, "followers", v.Attention)
	r.Stats["attention"] = missing[int64]("same_as_followers")
	area := strings.TrimSpace(strings.Join(nonEmpty(deref(v.ParentArea), deref(v.AreaName)), " / "))
	if area != "" {
		r.Category = available(area)
	}
	if v.LiveTime != nil && *v.LiveTime != "" && !strings.HasPrefix(*v.LiveTime, "0000") {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", *v.LiveTime, biliLiveZone); err == nil {
			r.Published = available(t.UTC())
		}
	}
	if v.Tags != nil && strings.TrimSpace(*v.Tags) != "" {
		var tags []string
		for _, t := range strings.Split(*v.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		r.Tags = available(tags)
	}
	cover := deref(v.Cover)
	if cover == "" {
		cover = deref(v.Keyframe)
	}
	if cover != "" {
		appendMedia(&r, imageMetadata(available(httpsURL(cover))))
	}
	appendBody(&r)
	if v.UID != "" {
		f.biliLiveMaster(ctx, &r, string(v.UID))
	}
	return r, nil
}

// biliLiveMaster 补主播名、头像和粉丝数；失败只留空字段，不让整个直播间取数失败。
func (f *Fetcher) biliLiveMaster(ctx context.Context, r *Result, uid string) {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Info struct {
				Name *string `json:"uname"`
				Face *string `json:"face"`
			} `json:"info"`
			Followers *int64 `json:"follower_num"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.live.bilibili.com/live_user/v1/Master/info?uid="+url.QueryEscape(uid), "bili_live_master", &e); err != nil || e.Code == nil || *e.Code != 0 || e.Data == nil {
		return
	}
	r.Author.Name = stringField(e.Data.Info.Name)
	if e.Data.Info.Face != nil {
		r.Author.Avatar = available(httpsURL(*e.Data.Info.Face))
	}
	if e.Data.Followers != nil {
		stat(r, "followers", e.Data.Followers)
	}
}

func (f *Fetcher) fetchBiliSpace(ctx context.Context, l Link) (Result, error) {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Card struct {
				Mid      decimalID `json:"mid"`
				Name     *string   `json:"name"`
				Face     *string   `json:"face"`
				Sign     *string   `json:"sign"`
				Fans     *int64    `json:"fans"`
				Attent   *int64    `json:"attention"`
				Official struct {
					Title *string `json:"title"`
				} `json:"Official"`
			} `json:"card"`
			Archive *int64 `json:"archive_count"`
			Article *int64 `json:"article_count"`
			Likes   *int64 `json:"like_num"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.bilibili.com/x/web-interface/card?photo=false&mid="+url.QueryEscape(l.ID), biliSpaceSource, &e); err != nil {
		return Result{}, err
	}
	if e.Code == nil {
		return Result{}, failure("invalid_response", biliSpaceSource)
	}
	if *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, biliSpaceSource)
	}
	if e.Data == nil || e.Data.Card.Name == nil || strings.TrimSpace(*e.Data.Card.Name) == "" {
		return Result{}, failure("invalid_response", biliSpaceSource)
	}
	c := e.Data.Card
	if string(c.Mid) != trimLeadingZero(l.ID) {
		return Result{}, failure("identity_mismatch", biliSpaceSource)
	}
	r := newResult(l, biliSpaceSource)
	r.Title = stringField(c.Name)
	r.Text = stringField(c.Sign)
	r.Author.Name = stringField(c.Name)
	r.Author.ID = available(string(c.Mid))
	if c.Face != nil && *c.Face != "" {
		r.Author.Avatar = available(httpsURL(*c.Face))
		appendMedia(&r, imageMetadata(available(httpsURL(*c.Face))))
	}
	if t := strings.TrimSpace(deref(c.Official.Title)); t != "" {
		r.Category = available(t)
	}
	stat(&r, "followers", c.Fans)
	stat(&r, "following", c.Attent)
	stat(&r, "videos", e.Data.Archive)
	stat(&r, "articles", e.Data.Article)
	stat(&r, "likes", e.Data.Likes)
	return r, nil
}

var liveHTMLTag = regexp.MustCompile(`<[^>]*>`)

// plainText 去掉直播间简介里的 HTML 标签（接口会回 <p>……</p>），转回普通字符。
func plainText(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(html.UnescapeString(liveHTMLTag.ReplaceAllString(*p, "")))
	return &s
}
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func nonEmpty(a ...string) []string {
	var out []string
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
func httpsURL(s string) string {
	if strings.HasPrefix(s, "//") {
		return "https:" + s
	}
	return strings.Replace(s, "http://", "https://", 1)
}
func trimLeadingZero(s string) string {
	if t := strings.TrimLeft(s, "0"); t != "" {
		return t
	}
	return s
}
