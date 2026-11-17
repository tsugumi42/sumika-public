package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// ID、标题、作者都是编的；字段形状照 2026-11-09 的匿名实测（漫画系列 seriesId 是字符串，小说系列是数字）。

const pixivMangaSeries = `{"error":false,"body":{"illustSeries":[` +
	`{"id":"90020","userId":"90021","title":"纸船连载","description":"每周一话","caption":"每周一话","total":70,"url":"https://i.pximg.net/c/782x410_80_a2_g2/img-master/img/2026/11/01/00/00/00/90030_p0_master1200.jpg","firstIllustId":"90022","latestIllustId":"90030","createDate":"2026-08-07T23:30:52+09:00"},` +
	`{"id":"90099","userId":"90098","title":"别的系列","total":1}],` +
	`"users":[{"userId":"90021","name":"编造画师甲"}],"thumbnails":{"illust":[]},"page":{"series":[]}}}`

func TestPixivMangaSeries(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/ajax/series/90020" || req.URL.Query().Get("p") != "1" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return response(200, pixivMangaSeries), nil
	})
	l := link(t, "https://www.pixiv.net/user/90021/series/90020")
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "series" || r.Title.Value != "纸船连载" || r.Author.Name.Value != "编造画师甲" || r.Author.ID.Value != "90021" || r.Stats["total"].Value != 70 || r.Text.Value != "每周一话" {
		t.Fatalf("系列字段：%+v", r)
	}
	s := r.Series
	if s == nil || s.Type != "manga" || s.Total.Value != 70 || s.LatestID != "90030" || s.FirstID != "90022" || s.WorkURL(s.LatestID) != "https://www.pixiv.net/artworks/90030" {
		t.Fatalf("系列信息：%+v", s)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].Referer != "https://www.pixiv.net/" || r.Media.Value[0].URL.State != "available" {
		t.Fatalf("封面：%+v", r.Media)
	}
	// 只要元数据：不带简介和封面
	m, err := f.Metadata(context.Background(), l)
	if err != nil || m.Stage != "metadata" || m.Media.State == "available" {
		t.Fatalf("元数据阶段：%v %+v", err, m)
	}
}

func TestPixivMangaSeriesFailures(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		kind   string
	}{
		{"响应里没有这个系列", 200, `{"error":false,"body":{"illustSeries":[{"id":"90099","title":"别的系列"}],"users":[]}}`, "identity_mismatch"},
		{"系列不存在", 404, `{"error":true,"message":"","body":[]}`, "not_found"},
		{"匿名被拒", 403, `{}`, "not_returned"},
		{"业务拒绝", 200, `{"error":true,"message":"x","body":[]}`, "not_returned"},
		{"没有标题", 200, `{"error":false,"body":{"illustSeries":[{"id":"90020","total":3}],"users":[]}}`, "invalid_response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(c.status, c.body), nil })
			_, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/user/90021/series/90020"))
			wantError(t, err, c.kind)
		})
	}
}

const pixivNovelSeries = `{"error":false,"body":{"id":"90040","userId":"90041","userName":"编造作者乙","title":"纸灯物语","caption":"一段<br />简介","xRestrict":0,"aiType":1,` +
	`"createDate":"2026-06-09T15:20:08+09:00","total":87,"publishedContentCount":86,"publishedTotalCharacterCount":176729,"firstNovelId":"90042","latestNovelId":"90050",` +
	`"cover":{"urls":{"480mw":"https://i.pximg.net/c/480x960/novel-cover-master/img/a_master1200.jpg","1200x1200":"https://i.pximg.net/c/1200x1200/novel-cover-master/img/a_master1200.jpg"}}}}`

func TestPixivNovelSeries(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/ajax/novel/series/90040" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return response(200, pixivNovelSeries), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/novel/series/90040"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "novel_series" || r.Title.Value != "纸灯物语" || r.Author.Name.Value != "编造作者乙" || r.Stats["total"].Value != 87 || r.Stats["characters"].Value != 176729 || r.Text.Value != "一段\n简介" {
		t.Fatalf("字段：%+v", r)
	}
	if x, ok := r.Flags["ai_generated"]; !ok || x.State != "available" || x.Value != false || r.Flags["age_rating"].Value != "all_ages" {
		t.Fatalf("AI 和分级：%+v", r.Flags)
	}
	if r.Series.Type != "novel" || r.Series.WorkURL("90050") != "https://www.pixiv.net/novel/show.php?id=90050" {
		t.Fatalf("系列信息：%+v", r.Series)
	}
	if len(r.Media.Value) != 1 || !strings.Contains(r.Media.Value[0].URL.Value, "1200x1200") {
		t.Fatalf("封面应取 1200x1200：%+v", r.Media)
	}
	wrong := strings.Replace(pixivNovelSeries, `"id":"90040"`, `"id":"90099"`, 1)
	f = testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, wrong), nil })
	_, err = f.Fetch(context.Background(), link(t, "https://www.pixiv.net/novel/series/90040"))
	wantError(t, err, "identity_mismatch")
}

// 作品页自带的系列导航：漫画的 seriesId 是字符串，小说的是数字；上一话或下一话可以没有。
func TestPixivSeriesNavigation(t *testing.T) {
	work := strings.Replace(fixture(t, "more-pixiv-one"), `"illustType": 0,`, `"illustType": 0,
    "seriesNavData": {"seriesType":"manga","seriesId":"90020","title":"纸船连载","order":68,"isWatched":false,"prev":{"id":"90023","title":"上一话","order":67},"next":null},`, 1)
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, work), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/100000000"))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Series
	if s == nil || s.Type != "manga" || s.ID != "90020" || s.Title.Value != "纸船连载" || s.Order.Value != 68 || s.Prev == nil || s.Prev.ID != "90023" || s.Prev.Order.Value != 67 || s.Next != nil {
		t.Fatalf("漫画导航：%+v", s)
	}

	novel := strings.Replace(fixture(t, "pixiv-novel"), `"xRestrict": 0,`, `"xRestrict": 0,
    "coverUrl": "https://i.pximg.net/c/600x600/novel-cover-master/img/c_master1200.jpg",
    "seriesNavData": {"seriesType":"novel","seriesId":90040,"title":"纸灯物语","order":84,"prev":{"title":"上","order":83,"id":"90041","available":true},"next":{"title":"下","order":85,"available":false}},`, 1)
	f = testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, novel), nil })
	r, err = f.Fetch(context.Background(), link(t, "https://www.pixiv.net/novel/show.php?id=90010"))
	if err != nil {
		t.Fatal(err)
	}
	s = r.Series
	if s == nil || s.Type != "novel" || s.ID != "90040" || s.Order.Value != 84 || s.Prev == nil || s.Prev.ID != "90041" || s.Next != nil {
		t.Fatalf("小说导航（数字 ID、没有 ID 的下一话）：%+v", s)
	}
	if r.Novel.Cover.State != "available" || r.Novel.Cover.Value.Referer != "https://www.pixiv.net/" || !strings.Contains(r.Novel.Cover.Value.URL.Value, "c_master1200") {
		t.Fatalf("小说封面：%+v", r.Novel.Cover)
	}
	// 封面不混进正文插图
	for _, m := range r.Media.Value {
		if strings.Contains(m.URL.Value, "c_master1200") {
			t.Fatalf("封面混进了插图：%+v", r.Media)
		}
	}
}

// 导航是附带信息：为 null、缺、形状乱了都不能让作品取数失败，更不能记成有系列。
func TestPixivSeriesNavigationIsBestEffort(t *testing.T) {
	for _, nav := range []string{`null`, `"x"`, `[]`, `{}`, `{"seriesId":null,"title":"t"}`, `{"seriesId":"abc"}`, `{"seriesId":"90020","prev":"bad","order":"x"}`} {
		work := strings.Replace(fixture(t, "more-pixiv-one"), `"illustType": 0,`, `"illustType": 0, "seriesNavData": `+nav+`,`, 1)
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, work), nil })
		r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/artworks/100000000"))
		if err != nil {
			t.Errorf("%s：作品取数被导航拖垮：%v", nav, err)
			continue
		}
		if r.Title.Value == "" {
			t.Errorf("%s：作品主体丢了", nav)
		}
	}
	// 没有 coverUrl 的小说：封面是“没返回”，不是空地址
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fixture(t, "pixiv-novel")), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://www.pixiv.net/novel/show.php?id=90010"))
	if err != nil || r.Novel.Cover.State == "available" || r.Series != nil {
		t.Fatalf("无封面无系列：%v %+v %+v", err, r.Novel.Cover, r.Series)
	}
}
