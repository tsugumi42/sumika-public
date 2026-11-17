package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// 番剧、课程、音频、合集和系列：数据都是编的，编号、名字不对应任何真实内容。

const bangumiBody = `{"code":0,"result":{"season_id":830001,"media_id":840001,"title":"纸船物语","evaluate":"这是编的番剧简介。","cover":"http://i0.hdslb.com/bfs/bangumi/image/fake.png",
"rating":{"score":9.8,"count":21726},"stat":{"views":2500000,"danmakus":39000,"likes":19000,"favorites":80000,"coins":23000,"reply":4400,"share":2700},
"new_ep":{"desc":"已完结，共12话"},"publish":{"pub_time":"2020-04-12 00:00:00"},"styles":["冒险","校园"],"areas":[{"id":2,"name":"日本"}],
"episodes":[{"id":850001,"title":"1","long_title":"纸船出航","show_title":"第1话 纸船出航"},{"id":850002,"title":"2","long_title":"遇到风浪","show_title":"第2话 遇到风浪"}]}}`

func TestBangumiFetchEpisode(t *testing.T) {
	var query string
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/pgc/view/web/season" {
			t.Fatalf("不该请求：%s", req.URL)
		}
		query = req.URL.RawQuery
		return response(200, bangumiBody), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/bangumi/play/ep850002"))
	if err != nil {
		t.Fatal(err)
	}
	if query != "ep_id=850002" {
		t.Fatalf("query = %s", query)
	}
	b := r.Bangumi
	if r.Kind != "episode" || r.Title.Value != "纸船物语" || b == nil || b.Episode.Value != "第2话 遇到风浪" || b.Rating.Value != 9.8 || b.RatingCount.Value != 21726 || b.Status.Value != "已完结，共12话" || b.Areas.Value != "日本" {
		t.Fatalf("%+v %+v", r, b)
	}
	if r.Stats["view"].Value != 2500000 || r.Stats["follow"].Value != 80000 || r.Stats["danmaku"].Value != 39000 {
		t.Fatalf("%+v", r.Stats)
	}
	if len(r.Tags.Value) != 2 || r.Published.State != "available" || r.Published.Value.UTC().Format("2006-01-02 15:04") != "2020-04-11 16:00" {
		t.Fatalf("%+v %+v", r.Tags, r.Published)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/bangumi/image/fake.png" {
		t.Fatalf("%+v", r.Media)
	}
}

// ss 链接没有“哪一集”；md 页先换 season_id，再取 season。
func TestBangumiSeasonAndMedia(t *testing.T) {
	var paths []string
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path+"?"+req.URL.RawQuery)
		if req.URL.Path == "/pgc/review/user" {
			return response(200, `{"code":0,"result":{"media":{"media_id":840001,"season_id":830001}}}`), nil
		}
		return response(200, bangumiBody), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/bangumi/play/ss830001"))
	if err != nil || r.Bangumi.Episode.State != "not_returned" || r.Bangumi.Episode.Reason != "not_applicable" {
		t.Fatalf("%v %+v", err, r.Bangumi)
	}
	r, err = f.Fetch(context.Background(), link(t, "https://www.bilibili.com/bangumi/media/md840001"))
	if err != nil || r.Kind != "media" || r.Title.Value != "纸船物语" {
		t.Fatalf("%v %+v", err, r.Title)
	}
	if got := strings.Join(paths, " "); got != "/pgc/view/web/season?season_id=830001 /pgc/review/user?media_id=840001 /pgc/view/web/season?season_id=830001" {
		t.Fatalf("请求：%s", got)
	}
}

func TestBangumiErrors(t *testing.T) {
	cases := map[string]struct {
		body, url, kind string
	}{
		"不存在":        {`{"code":-404,"message":"啥都木有"}`, "https://www.bilibili.com/bangumi/play/ep850001", "not_found"},
		"风控":         {`{"code":-352}`, "https://www.bilibili.com/bangumi/play/ep850001", "risk_control"},
		"season 对不上": {bangumiBody, "https://www.bilibili.com/bangumi/play/ss999999", "identity_mismatch"},
		"没有标题":       {`{"code":0,"result":{"season_id":830001}}`, "https://www.bilibili.com/bangumi/play/ss830001", "invalid_response"},
		"没有结果":       {`{"code":0}`, "https://www.bilibili.com/bangumi/play/ss830001", "invalid_response"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, c.body), nil })
			_, err := f.Fetch(context.Background(), link(t, c.url))
			wantError(t, err, c.kind)
		})
	}
	t.Run("md 的 media_id 对不上", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, `{"code":0,"result":{"media":{"media_id":1,"season_id":830001}}}`), nil
		})
		_, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/bangumi/media/md840001"))
		wantError(t, err, "identity_mismatch")
	})
}

const courseBody = `{"code":0,"data":{"season_id":860001,"title":"纸艺入门课","subtitle":"从零开始学折纸","cover":"https://archive.biliimg.com/bfs/archive/fake.jpg","release_info":"已完结 | 共20课时","ep_count":20,
"up_info":{"mid":770003,"uname":"纸灯实验室","avatar":"https://i0.hdslb.com/bfs/face/fake.jpg","follower":2100},"stat":{"play":1790000},
"payment":{"price":256.0,"price_format":"256","price_unit":"B币","discount_desc":"券后188 B币"}}}`

func TestCourseFetch(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/pugv/view/web/season":
			return response(200, courseBody), nil
		case "/pugv/view/web/ep/list":
			return response(200, `{"code":0,"data":{"items":[{"label":"全集试看"},{"label":"部分试看"},{},{}]}}`), nil
		}
		t.Fatalf("不该请求：%s", req.URL)
		return nil, nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/cheese/play/ss860001"))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Course
	if r.Kind != "course" || c == nil || c.Price.Value != "256 B币" || c.Discount.Value != "券后188 B币" || c.Release.Value != "已完结 | 共20课时" || c.Trial.Value != "2 课时可试看" {
		t.Fatalf("%+v", c)
	}
	if r.Author.Name.Value != "纸灯实验室" || r.Stats["lessons"].Value != 20 || r.Stats["view"].Value != 1790000 || r.Text.Value != "从零开始学折纸" {
		t.Fatalf("%+v", r)
	}
	if len(r.Media.Value) != 1 || !strings.HasPrefix(r.Media.Value[0].URL.Value, "https://archive.biliimg.com/") {
		t.Fatalf("%+v", r.Media)
	}
}

// 课时列表取不到只缺“试看”，课程本身照出；免费课程价格写免费。
func TestCourseTrialFailureAndFree(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/pugv/view/web/ep/list" {
			return response(200, `{"code":-352}`), nil
		}
		return response(200, strings.Replace(courseBody, `"price":256.0`, `"price":0`, 1)), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/cheese/play/ss860001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Course.Trial.State != "failed" || r.Course.Price.Value != "免费" {
		t.Fatalf("%+v", r.Course)
	}
}

func TestCourseErrors(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, courseBody), nil })
	_, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/cheese/play/ss999999"))
	wantError(t, err, "identity_mismatch")
	f = testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, `{"code":-404}`), nil })
	_, err = f.Fetch(context.Background(), link(t, "https://www.bilibili.com/cheese/play/ep860001"))
	wantError(t, err, "not_found")
}

const audioBody = `{"code":0,"msg":"success","data":{"id":870001,"uid":770004,"uname":"纸灯实验室","author":"纸灯","title":"纸船谣","cover":"http://i0.hdslb.com/bfs/music/fake.jpg","intro":"这是编的音频简介。","duration":239,"passtime":1501640683,
"coin_num":23,"statistic":{"sid":870001,"play":13446,"collect":375,"comment":50,"share":44}}}`

func TestAudioFetch(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.bilibili.com" || req.URL.Path != "/audio/music-service-c/web/song/info" || req.URL.Query().Get("sid") != "870001" {
			t.Fatalf("不该请求：%s", req.URL)
		}
		return response(200, audioBody), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/audio/au870001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "audio" || r.Title.Value != "纸船谣" || r.Author.Name.Value != "纸灯实验室" || r.Stats["view"].Value != 13446 || r.Stats["favorite"].Value != 375 || r.Stats["coin"].Value != 23 {
		t.Fatalf("%+v", r)
	}
	var audio *Media
	for i := range r.Media.Value {
		if r.Media.Value[i].Type == "audio" {
			audio = &r.Media.Value[i]
		}
	}
	if audio == nil || audio.Duration.Value != 239 || audio.URL.State != "unsupported" {
		t.Fatalf("%+v", r.Media)
	}
}

func TestAudioErrors(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, audioBody), nil })
	_, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/audio/au999999"))
	wantError(t, err, "identity_mismatch")
	f = testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"code":7201006,"msg":"歌曲不存在"}`), nil
	})
	_, err = f.Fetch(context.Background(), link(t, "https://www.bilibili.com/audio/au870001"))
	if err == nil {
		t.Fatal("不存在的歌曲应当失败")
	}
}

func TestCollectionSeason(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/x/polymer/web-space/seasons_archives_list":
			if !strings.Contains(req.Header.Get("User-Agent"), "Chrome/") {
				return response(200, `{"code":-352}`), nil // 真接口只认完整的浏览器 UA（实测）
			}
			return response(200, `{"code":0,"data":{"meta":{"season_id":880001,"mid":770005,"name":"合集·折纸教程合集","title":"折纸教程合集","description":"这是编的合集简介。","cover":"https://archive.biliimg.com/bfs/archive/fake.jpg","total":48,"ptime":1700000000},
"archives":[{"title":"第一课","pic":"http://i0.hdslb.com/bfs/archive/a.jpg"},{"title":"第二课"},{"title":"第三课"}]}}`), nil
		case "/x/web-interface/card":
			return response(200, `{"code":0,"data":{"card":{"mid":770005,"name":"纸灯实验室","face":"http://i0.hdslb.com/bfs/face/fake.jpg"}}}`), nil
		}
		t.Fatalf("不该请求：%s", req.URL)
		return nil, nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://space.bilibili.com/770005/lists/880001?type=season"))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Collection
	if r.Kind != "collection" || r.Title.Value != "折纸教程合集" || c == nil || c.Type.Value != "合集" || c.Total.Value != 48 || len(c.Items.Value) != 3 || c.Items.Value[0] != "第一课" {
		t.Fatalf("%+v %+v", r.Title, c)
	}
	if r.Author.Name.Value != "纸灯实验室" || r.Author.ID.Value != "770005" || r.Stats["videos"].Value != 48 {
		t.Fatalf("%+v", r.Author)
	}
	if len(r.Media.Value) != 1 || !strings.HasPrefix(r.Media.Value[0].URL.Value, "https://archive.biliimg.com/") {
		t.Fatalf("%+v", r.Media)
	}
}

func TestCollectionSeries(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/x/series/series":
			return response(200, `{"code":0,"data":{"meta":{"series_id":880002,"mid":770005,"name":"折纸系列","description":"","total":8,"ctime":1700000000}}}`), nil
		case "/x/series/archives":
			return response(200, `{"code":0,"data":{"archives":[{"title":"系列第一集","pic":"http://i0.hdslb.com/bfs/archive/s1.jpg"},{"title":"系列第二集"}]}}`), nil
		case "/x/web-interface/card":
			return response(200, `{"code":-352}`), nil // 名字取不到只缺名字
		}
		t.Fatalf("不该请求：%s", req.URL)
		return nil, nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://space.bilibili.com/770005/channel/seriesdetail?sid=880002"))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Collection
	if c == nil || c.Type.Value != "系列" || c.Total.Value != 8 || len(c.Items.Value) != 2 || r.Author.Name.State != "not_returned" {
		t.Fatalf("%+v %+v", c, r.Author)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/archive/s1.jpg" {
		t.Fatalf("系列用第一个视频的封面：%+v", r.Media)
	}
}

func TestCollectionErrors(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"code":0,"data":{"meta":{"season_id":1,"mid":770005,"name":"别的合集"}}}`), nil
	})
	_, err := f.Fetch(context.Background(), link(t, "https://space.bilibili.com/770005/lists/880001?type=season"))
	wantError(t, err, "identity_mismatch")
	f = testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"code":147002,"message":"视频列表已失效"}`), nil
	})
	_, err = f.Fetch(context.Background(), link(t, "https://space.bilibili.com/770005/lists/880002?type=series"))
	wantError(t, err, "not_found")
}

// 短链跳到课程、专栏、番剧、合集、小店商品时也能取数（以前只放行视频、动态、直播间、主页、会员购）。
func TestShortLinkResolvesToNewPages(t *testing.T) {
	for target, kind := range map[string]string{
		"https://www.bilibili.com/cheese/play/ss860001":                            "course",
		"https://www.bilibili.com/read/cv810001":                                   "article",
		"https://www.bilibili.com/bangumi/play/ep850001":                           "episode",
		"https://space.bilibili.com/770005/lists/880001?type=season&spm_id_from=1": "collection",
		"https://www.bilibili.com/audio/au870001":                                  "audio",
	} {
		t.Run(kind, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				resp := response(302, "")
				resp.Header.Set("Location", target)
				return resp, nil
			})
			l, err := f.Resolve(context.Background(), link(t, "https://b23.tv/fake123"))
			if err != nil || l.Kind != kind {
				t.Fatalf("%v %+v", err, l)
			}
		})
	}
}
