package linkreader

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// B 站课程（课堂）、音频、合集和系列。都只用匿名公开接口，只取展示用的元数据，不碰播放地址。
//   课程：pugv/view/web/season?season_id=|ep_id=（课程名、UP、价格、课时数），pugv/view/web/ep/list 数试看课时
//   音频：audio/music-service-c/web/song/info（挂在 api.bilibili.com 下也能取）
//   合集：x/polymer/web-space/seasons_archives_list；系列：x/series/series 加 x/series/archives；UP 主名字另取名片，失败只缺名字

const (
	courseSource     = "bili_course"
	audioSource      = "bili_audio"
	collectionSource = "bili_collection"
)

// CourseInfo 是课程独有的字段。价格按接口给的单位写（课程用 B币），不换算成人民币。
type CourseInfo struct {
	Price    Field[string] `json:"price"`    // 如“256 B币”，0 写“免费”
	Discount Field[string] `json:"discount"` // 券后价说法，如“券后188 B币”
	Release  Field[string] `json:"release"`  // 如“已完结 | 共20课时”
	Trial    Field[string] `json:"trial"`    // 如“6 课时可试看”
}

type courseSeason struct {
	SeasonID decimalID `json:"season_id"`
	Title    *string   `json:"title"`
	Subtitle *string   `json:"subtitle"`
	Cover    *string   `json:"cover"`
	Release  *string   `json:"release_info"`
	EpCount  *int64    `json:"ep_count"`
	UpInfo   *struct {
		Mid      decimalID `json:"mid"`
		Name     *string   `json:"uname"`
		Avatar   *string   `json:"avatar"`
		Follower *int64    `json:"follower"`
	} `json:"up_info"`
	Stat *struct {
		Play *int64 `json:"play"`
	} `json:"stat"`
	Payment *struct {
		Price    *float64 `json:"price"`
		Format   *string  `json:"price_format"`
		Unit     *string  `json:"price_unit"`
		Discount *string  `json:"discount_desc"`
	} `json:"payment"`
}

func (f *Fetcher) fetchCourse(ctx context.Context, l Link) (Result, error) {
	q := url.Values{}
	wantSeason := ""
	if n, ok := strings.CutPrefix(l.ID, "ss"); ok {
		wantSeason = n
		q.Set("season_id", n)
	} else if n, ok := strings.CutPrefix(l.ID, "ep"); ok {
		q.Set("ep_id", n)
	} else {
		return Result{}, failure("invalid_target", courseSource)
	}
	var e struct {
		Code *int          `json:"code"`
		Data *courseSeason `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.bilibili.com/pugv/view/web/season?"+q.Encode(), courseSource, &e); err != nil {
		return Result{}, err
	}
	if e.Code == nil {
		return Result{}, failure("invalid_response", courseSource)
	}
	if *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, courseSource)
	}
	v := e.Data
	if v == nil || v.SeasonID == "" || v.Title == nil || strings.TrimSpace(*v.Title) == "" {
		return Result{}, failure("invalid_response", courseSource)
	}
	if wantSeason != "" && string(v.SeasonID) != wantSeason {
		return Result{}, failure("identity_mismatch", courseSource)
	}
	r := newResult(l, courseSource)
	r.Title = stringField(v.Title)
	r.Text = stringField(v.Subtitle)
	c := &CourseInfo{Price: missing[string]("interface_omitted"), Discount: missing[string]("not_applicable"), Release: stringField(v.Release), Trial: missing[string]("interface_omitted")}
	if v.Payment != nil && v.Payment.Price != nil {
		switch {
		case *v.Payment.Price == 0:
			c.Price = available("免费")
		default:
			num := strconv.FormatFloat(*v.Payment.Price, 'f', -1, 64)
			if v.Payment.Format != nil && *v.Payment.Format != "" {
				num = *v.Payment.Format
			}
			c.Price = available(strings.TrimSpace(num + " " + deref(v.Payment.Unit)))
		}
		if d := cleanSpaces(deref(v.Payment.Discount)); d != "" {
			c.Discount = available(d)
		}
	}
	if v.EpCount != nil {
		stat(&r, "lessons", v.EpCount)
	} else {
		stat(&r, "lessons", nil)
	}
	if v.Stat != nil {
		stat(&r, "view", v.Stat.Play)
	} else {
		stat(&r, "view", nil)
	}
	if u := v.UpInfo; u != nil {
		r.Author.Name = stringField(u.Name)
		if u.Mid != "" {
			r.Author.ID = available(string(u.Mid))
		}
		if u.Avatar != nil && *u.Avatar != "" {
			r.Author.Avatar = available(httpsURL(*u.Avatar))
		}
		stat(&r, "followers", u.Follower)
	}
	c.Trial = f.courseTrial(ctx, string(v.SeasonID))
	r.Course = c
	appendBody(&r)
	if v.Cover != nil && *v.Cover != "" {
		appendMedia(&r, imageMetadata(available(httpsURL(*v.Cover))))
	}
	return r, nil
}

// cleanSpaces 把接口里夹的细空格（U+2009、U+2006、U+00A0）换成普通空格，并去掉两头空白。
func cleanSpaces(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\u2009' || r == '\u2006' || r == '\u00a0' {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// courseTrial 数课时列表里带“试看”标签的课时。取不到只缺这一项，不让整个课程失败。
func (f *Fetcher) courseTrial(ctx context.Context, season string) Field[string] {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Items []struct {
				Label *string `json:"label"`
			} `json:"items"`
		} `json:"data"`
	}
	err := f.getJSON(ctx, "https://api.bilibili.com/pugv/view/web/ep/list?pn=1&season_id="+url.QueryEscape(season), "bili_course_eps", &e)
	if err != nil || e.Code == nil || *e.Code != 0 || e.Data == nil {
		reason := "interface_error"
		if fe, ok := err.(*FetchError); ok {
			reason = fe.Kind
		}
		return Field[string]{State: "failed", Reason: reason}
	}
	n := 0
	for _, it := range e.Data.Items {
		if strings.Contains(deref(it.Label), "试看") {
			n++
		}
	}
	if n == 0 {
		return available("没有试看")
	}
	return available(fmt.Sprintf("%d 课时可试看", n))
}

func (f *Fetcher) fetchAudio(ctx context.Context, l Link) (Result, error) {
	id := strings.TrimPrefix(l.ID, "au")
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			ID       decimalID `json:"id"`
			Uid      decimalID `json:"uid"`
			Uname    *string   `json:"uname"`
			Author   *string   `json:"author"`
			Title    *string   `json:"title"`
			Cover    *string   `json:"cover"`
			Intro    *string   `json:"intro"`
			Duration *int      `json:"duration"`
			PassTime *int64    `json:"passtime"`
			Stat     *struct {
				Play    *int64 `json:"play"`
				Collect *int64 `json:"collect"`
				Comment *int64 `json:"comment"`
				Share   *int64 `json:"share"`
			} `json:"statistic"`
			Coin *int64 `json:"coin_num"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.bilibili.com/audio/music-service-c/web/song/info?sid="+url.QueryEscape(id), audioSource, &e); err != nil {
		return Result{}, err
	}
	if e.Code == nil {
		return Result{}, failure("invalid_response", audioSource)
	}
	if *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, audioSource)
	}
	v := e.Data
	if v == nil || v.ID == "" || v.Title == nil || strings.TrimSpace(*v.Title) == "" {
		return Result{}, failure("invalid_response", audioSource)
	}
	if string(v.ID) != id {
		return Result{}, failure("identity_mismatch", audioSource)
	}
	r := newResult(l, audioSource)
	r.Title = stringField(v.Title)
	r.Text = stringField(v.Intro)
	r.Author.Name = stringField(v.Uname)
	if r.Author.Name.State != "available" || r.Author.Name.Value == "" {
		r.Author.Name = stringField(v.Author)
	}
	if v.Uid != "" {
		r.Author.ID = available(string(v.Uid))
	}
	if v.PassTime != nil && *v.PassTime > 0 {
		r.Published = unixField(v.PassTime)
	}
	var play, collect, comment, share *int64
	if v.Stat != nil {
		play, collect, comment, share = v.Stat.Play, v.Stat.Collect, v.Stat.Comment, v.Stat.Share
	}
	stat(&r, "view", play)
	stat(&r, "favorite", collect)
	stat(&r, "reply", comment)
	stat(&r, "share", share)
	stat(&r, "coin", v.Coin)
	appendBody(&r)
	if v.Cover != nil && *v.Cover != "" {
		appendMedia(&r, imageMetadata(available(httpsURL(*v.Cover))))
	}
	// 时长放在一个不下载的音频项里（模板的“时长”从这里读）；不请求播放地址。
	audio := Media{Type: "audio", URL: Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: missing[string]("not_applicable"), Width: missing[int]("not_applicable"), Height: missing[int]("not_applicable"), Duration: secondsField(v.Duration), Size: missing[int64]("interface_omitted")}
	appendMedia(&r, audio)
	return r, nil
}

// CollectionInfo 是合集和系列共有的字段：种类、视频总数、前几条视频的标题。
type CollectionInfo struct {
	Type  Field[string]   `json:"type"` // 合集或系列
	Total Field[int64]    `json:"total"`
	Items Field[[]string] `json:"items"`
}

type collectionVideo struct {
	Title *string `json:"title"`
	Pic   *string `json:"pic"`
}

func (f *Fetcher) fetchCollection(ctx context.Context, l Link) (Result, error) {
	parts := strings.Split(l.ID, ":")
	if len(parts) != 3 || (parts[0] != "season" && parts[0] != "series") {
		return Result{}, failure("invalid_target", collectionSource)
	}
	typ, mid, sid := parts[0], parts[1], parts[2]
	var (
		name, desc, cover *string
		total             *int64
		published         *int64
		videos            []collectionVideo
	)
	if typ == "season" {
		var e struct {
			Code *int `json:"code"`
			Data *struct {
				Meta struct {
					ID    decimalID `json:"season_id"`
					Mid   decimalID `json:"mid"`
					Name  *string   `json:"name"`
					Title *string   `json:"title"`
					Desc  *string   `json:"description"`
					Cover *string   `json:"cover"`
					Total *int64    `json:"total"`
					Ptime *int64    `json:"ptime"`
				} `json:"meta"`
				Archives []collectionVideo `json:"archives"`
			} `json:"data"`
		}
		raw := "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?page_num=1&page_size=3&mid=" + url.QueryEscape(mid) + "&season_id=" + url.QueryEscape(sid)
		// 这个接口认 User-Agent：只写 Mozilla/5.0 时回 -352（实测）；B 站请求现在统一用完整的浏览器写法（setBiliHeaders）。
		if err := f.getJSON(ctx, raw, collectionSource, &e); err != nil {
			return Result{}, err
		}
		if e.Code == nil {
			return Result{}, failure("invalid_response", collectionSource)
		}
		if *e.Code != 0 {
			return Result{}, biliFailure(*e.Code, collectionSource)
		}
		if e.Data == nil || (e.Data.Meta.Name == nil && e.Data.Meta.Title == nil) {
			return Result{}, failure("invalid_response", collectionSource)
		}
		m := e.Data.Meta
		if string(m.ID) != sid || (m.Mid != "" && string(m.Mid) != mid) {
			return Result{}, failure("identity_mismatch", collectionSource)
		}
		name = m.Title
		if name == nil || *name == "" {
			name = m.Name
		}
		desc, cover, total, published, videos = m.Desc, m.Cover, m.Total, m.Ptime, e.Data.Archives
	} else {
		var e struct {
			Code *int `json:"code"`
			Data *struct {
				Meta struct {
					ID    decimalID `json:"series_id"`
					Mid   decimalID `json:"mid"`
					Name  *string   `json:"name"`
					Desc  *string   `json:"description"`
					Total *int64    `json:"total"`
					Ctime *int64    `json:"ctime"`
				} `json:"meta"`
			} `json:"data"`
		}
		if err := f.getJSON(ctx, "https://api.bilibili.com/x/series/series?series_id="+url.QueryEscape(sid), collectionSource, &e); err != nil {
			return Result{}, err
		}
		if e.Code == nil {
			return Result{}, failure("invalid_response", collectionSource)
		}
		if *e.Code != 0 {
			return Result{}, biliFailure(*e.Code, collectionSource)
		}
		if e.Data == nil || e.Data.Meta.Name == nil || strings.TrimSpace(*e.Data.Meta.Name) == "" {
			return Result{}, failure("invalid_response", collectionSource)
		}
		m := e.Data.Meta
		if string(m.ID) != sid || string(m.Mid) != mid {
			return Result{}, failure("identity_mismatch", collectionSource)
		}
		name, desc, total, published = m.Name, m.Desc, m.Total, m.Ctime
		videos = f.seriesVideos(ctx, mid, sid)
	}
	r := newResult(l, collectionSource)
	r.Title = stringField(name)
	r.Text = stringField(desc)
	r.Author.ID = available(mid)
	if published != nil && *published > 0 {
		r.Published = unixField(published)
	}
	c := &CollectionInfo{Type: available(map[string]string{"season": "合集", "series": "系列"}[typ]), Total: optInt64(total), Items: missing[[]string]("interface_omitted")}
	if len(videos) > 0 {
		var titles []string
		for _, v := range videos {
			if t := strings.TrimSpace(deref(v.Title)); t != "" {
				titles = append(titles, t)
			}
		}
		c.Items = available(titles)
	}
	r.Collection = c
	stat(&r, "videos", total)
	f.collectionOwner(ctx, &r, mid)
	appendBody(&r)
	cv := deref(cover)
	if cv == "" && len(videos) > 0 {
		cv = deref(videos[0].Pic) // 系列没有自己的封面，用第一个视频的
	}
	if cv != "" {
		appendMedia(&r, imageMetadata(available(httpsURL(cv))))
	}
	return r, nil
}

// seriesVideos 取系列最前面的三个视频；失败只缺这一项。
func (f *Fetcher) seriesVideos(ctx context.Context, mid, sid string) []collectionVideo {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Archives []collectionVideo `json:"archives"`
		} `json:"data"`
	}
	raw := "https://api.bilibili.com/x/series/archives?only_normal=true&sort=asc&pn=1&ps=3&mid=" + url.QueryEscape(mid) + "&series_id=" + url.QueryEscape(sid)
	if err := f.getJSON(ctx, raw, "bili_series_archives", &e); err != nil || e.Code == nil || *e.Code != 0 || e.Data == nil {
		return nil
	}
	return e.Data.Archives
}

// collectionOwner 补 UP 主名字和头像（合集接口只给 mid）；失败只缺名字。
func (f *Fetcher) collectionOwner(ctx context.Context, r *Result, mid string) {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			Card struct {
				Mid  decimalID `json:"mid"`
				Name *string   `json:"name"`
				Face *string   `json:"face"`
			} `json:"card"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.bilibili.com/x/web-interface/card?photo=false&mid="+url.QueryEscape(mid), "bili_space", &e); err != nil || e.Code == nil || *e.Code != 0 || e.Data == nil || string(e.Data.Card.Mid) != mid {
		return
	}
	r.Author.Name = stringField(e.Data.Card.Name)
	if e.Data.Card.Face != nil && *e.Data.Card.Face != "" {
		r.Author.Avatar = available(httpsURL(*e.Data.Card.Face))
	}
}
