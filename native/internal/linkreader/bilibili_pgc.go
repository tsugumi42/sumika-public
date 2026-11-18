package linkreader

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// B 站番剧（ep、ss、md）：
//
//	pgc/view/web/season?ep_id=|season_id=：标题、简介、封面、评分、统计、分集、状态、风格、地区
//	md 页先用 pgc/review/user?media_id= 换 season_id（回包里就有），再取 season
//
// 会员专属集的元数据匿名也能取，只是正片要大会员；这里不请求播放地址。
const bangumiSource = "bili_bangumi"

// BangumiInfo 是番剧独有的字段；播放、弹幕、点赞等统计在 Result.Stats。
type BangumiInfo struct {
	Rating      Field[float64] `json:"rating"`
	RatingCount Field[int64]   `json:"rating_count"`
	Status      Field[string]  `json:"status"`  // 如“已完结，共12话”
	Areas       Field[string]  `json:"areas"`   // 如“日本”
	Episode     Field[string]  `json:"episode"` // 链接指向的那一集（只有 ep 链接才有）
}

type bangumiSeason struct {
	SeasonID decimalID `json:"season_id"`
	MediaID  decimalID `json:"media_id"`
	Title    *string   `json:"title"`
	Evaluate *string   `json:"evaluate"`
	Cover    *string   `json:"cover"`
	Rating   *struct {
		Score *float64 `json:"score"`
		Count *int64   `json:"count"`
	} `json:"rating"`
	Stat *struct {
		Views    *int64 `json:"views"`
		Danmakus *int64 `json:"danmakus"`
		Likes    *int64 `json:"likes"`
		Follows  *int64 `json:"favorites"` // 追番人数
		Coins    *int64 `json:"coins"`
		Reply    *int64 `json:"reply"`
		Share    *int64 `json:"share"`
	} `json:"stat"`
	NewEp *struct {
		Desc *string `json:"desc"`
	} `json:"new_ep"`
	Publish *struct {
		PubTime *string `json:"pub_time"`
	} `json:"publish"`
	Styles []string `json:"styles"`
	Areas  []struct {
		Name string `json:"name"`
	} `json:"areas"`
	Episodes []struct {
		ID        decimalID `json:"id"`
		Title     *string   `json:"title"`
		LongTitle *string   `json:"long_title"`
		ShowTitle *string   `json:"show_title"`
	} `json:"episodes"`
}

func (f *Fetcher) bangumiSeason(ctx context.Context, q url.Values) (*bangumiSeason, error) {
	var e struct {
		Code   *int           `json:"code"`
		Result *bangumiSeason `json:"result"`
	}
	if err := f.getJSON(ctx, "https://api.bilibili.com/pgc/view/web/season?"+q.Encode(), bangumiSource, &e); err != nil {
		return nil, err
	}
	if e.Code == nil {
		return nil, failure("invalid_response", bangumiSource)
	}
	if *e.Code != 0 {
		return nil, biliFailure(*e.Code, bangumiSource)
	}
	if e.Result == nil || e.Result.SeasonID == "" || e.Result.Title == nil || strings.TrimSpace(*e.Result.Title) == "" {
		return nil, failure("invalid_response", bangumiSource)
	}
	return e.Result, nil
}

// bangumiMediaSeason 用 md 号换 season_id，同时核对 media_id。
func (f *Fetcher) bangumiMediaSeason(ctx context.Context, md string) (string, error) {
	var e struct {
		Code   *int `json:"code"`
		Result *struct {
			Media struct {
				MediaID  decimalID `json:"media_id"`
				SeasonID decimalID `json:"season_id"`
			} `json:"media"`
		} `json:"result"`
	}
	const source = "bili_bangumi_media"
	if err := f.getJSON(ctx, "https://api.bilibili.com/pgc/review/user?media_id="+url.QueryEscape(md), source, &e); err != nil {
		return "", err
	}
	if e.Code == nil {
		return "", failure("invalid_response", source)
	}
	if *e.Code != 0 {
		return "", biliFailure(*e.Code, source)
	}
	if e.Result == nil || e.Result.Media.SeasonID == "" {
		return "", failure("invalid_response", source)
	}
	if string(e.Result.Media.MediaID) != md {
		return "", failure("identity_mismatch", source)
	}
	return string(e.Result.Media.SeasonID), nil
}

func (f *Fetcher) fetchBangumi(ctx context.Context, l Link) (Result, error) {
	q := url.Values{}
	var wantSeason, wantMedia, wantEp string
	switch {
	case strings.HasPrefix(l.ID, "ep"):
		wantEp = strings.TrimPrefix(l.ID, "ep")
		q.Set("ep_id", wantEp)
	case strings.HasPrefix(l.ID, "ss"):
		wantSeason = strings.TrimPrefix(l.ID, "ss")
		q.Set("season_id", wantSeason)
	case strings.HasPrefix(l.ID, "md"):
		wantMedia = strings.TrimPrefix(l.ID, "md")
		ss, err := f.bangumiMediaSeason(ctx, wantMedia)
		if err != nil {
			return Result{}, err
		}
		wantSeason = ss
		q.Set("season_id", ss)
	default:
		return Result{}, failure("invalid_target", bangumiSource)
	}
	v, err := f.bangumiSeason(ctx, q)
	if err != nil {
		return Result{}, err
	}
	if (wantSeason != "" && string(v.SeasonID) != wantSeason) || (wantMedia != "" && v.MediaID != "" && string(v.MediaID) != wantMedia) {
		return Result{}, failure("identity_mismatch", bangumiSource)
	}
	r := newResult(l, bangumiSource)
	r.Title = stringField(v.Title)
	r.Text = stringField(v.Evaluate)
	if len(v.Styles) > 0 {
		r.Tags = available(v.Styles)
	}
	if v.Publish != nil && v.Publish.PubTime != nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", *v.Publish.PubTime, biliLiveZone); err == nil {
			r.Published = available(t.UTC())
		}
	}
	b := &BangumiInfo{Rating: missing[float64]("interface_omitted"), RatingCount: missing[int64]("interface_omitted"), Status: missing[string]("interface_omitted"), Areas: missing[string]("interface_omitted"), Episode: missing[string]("not_applicable")}
	if v.Rating != nil {
		b.Rating, b.RatingCount = optFloat(v.Rating.Score), optInt64(v.Rating.Count)
	}
	if v.NewEp != nil && v.NewEp.Desc != nil && strings.TrimSpace(*v.NewEp.Desc) != "" {
		b.Status = available(*v.NewEp.Desc)
	}
	var areas []string
	for _, a := range v.Areas {
		if a.Name != "" {
			areas = append(areas, a.Name)
		}
	}
	if len(areas) > 0 {
		b.Areas = available(strings.Join(areas, "、"))
	}
	if wantEp != "" {
		for _, ep := range v.Episodes {
			if string(ep.ID) != wantEp {
				continue
			}
			label := strings.TrimSpace(deref(ep.ShowTitle))
			if label == "" {
				label = strings.TrimSpace(deref(ep.Title) + " " + deref(ep.LongTitle))
			}
			if label != "" {
				b.Episode = available(label)
			}
			break
		}
		if b.Episode.State != "available" {
			b.Episode = missing[string]("episode_not_in_list")
		}
	}
	r.Bangumi = b
	var s struct{ views, danmaku, like, follow, coin, reply, share *int64 }
	if v.Stat != nil {
		s.views, s.danmaku, s.like, s.follow, s.coin, s.reply, s.share = v.Stat.Views, v.Stat.Danmakus, v.Stat.Likes, v.Stat.Follows, v.Stat.Coins, v.Stat.Reply, v.Stat.Share
	}
	stat(&r, "view", s.views)
	stat(&r, "danmaku", s.danmaku)
	stat(&r, "like", s.like)
	stat(&r, "follow", s.follow)
	stat(&r, "coin", s.coin)
	stat(&r, "reply", s.reply)
	stat(&r, "share", s.share)
	appendBody(&r)
	if v.Cover != nil && *v.Cover != "" {
		appendMedia(&r, imageMetadata(available(httpsURL(*v.Cover))))
	}
	return r, nil
}

func optFloat(p *float64) Field[float64] {
	if p == nil {
		return missing[float64]("interface_omitted")
	}
	return available(*p)
}
func optInt64(p *int64) Field[int64] {
	if p == nil {
		return missing[int64]("interface_omitted")
	}
	return available(*p)
}
