package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// 小游戏页的数据都是编的：slug、编号、作者名不对应任何真实小游戏。
const toyShell = `<!DOCTYPE html><html><head><title>纸舟小游戏</title><script>window.__TOY_META__={toy_id:700000000000001};</script></head><body></body></html>`

const toyBody = `{"code":0,"message":"OK","data":{"id":700000000000001,"title":"纸舟小游戏","poster_url":"https://i0.hdslb.com/bfs/static/toy/app/zhizhou-youxi/poster.jpg",
"poster_width":1200,"poster_height":900,"icon_url":"https://i0.hdslb.com/bfs/static/toy/app/zhizhou-youxi/icon.png","url":"https://www.bilibili.com/toy/zhizhou-youxi/index.html",
"dyn_id":"1200000000000000001","rid":"1","category":"GAME","user_info":{"mid":770002,"name":"纸灯实验室","face":"https://i1.hdslb.com/bfs/face/fake.jpg"},
"dyn_stat":{"like":523,"favorite":667,"comment":59,"share":0},"stat":{"pv":51149},"ctime":1793035469,"mtime":1794225105}}`

func toyFetcher(t *testing.T, shell, detail string, seen *[]string) *Fetcher {
	t.Helper()
	return testFetcher(t, func(req *http.Request) (*http.Response, error) {
		*seen = append(*seen, req.URL.String()+" | "+req.Header.Get("Referer"))
		switch {
		case req.URL.Host == "www.bilibili.com" && strings.HasPrefix(req.URL.Path, "/toy/"):
			return response(200, shell), nil
		case req.URL.Host == "api.bilibili.com" && req.URL.Path == "/x/toy/detail":
			return response(200, detail), nil
		}
		t.Fatalf("不该请求：%s", req.URL)
		return nil, nil
	})
}

func TestBiliToyRecognize(t *testing.T) {
	const clean = "https://www.bilibili.com/toy/zhizhou-youxi/index.html"
	for _, raw := range []string{
		"https://www.bilibili.com/toy/zhizhou-youxi/index.html",
		"https://www.bilibili.com/toy/zhizhou-youxi/",
		"https://www.bilibili.com/toy/zhizhou-youxi",
		"https://www.bilibili.com/toy/zhizhou-youxi/index.html?spm_id_from=333.788.specialRecommendByOp.0",
		"https://m.bilibili.com/toy/zhizhou-youxi/index.html?share_source=copy_web",
	} {
		got, ok := Recognize(raw)
		if !ok || got.Platform != "bilibili" || got.Kind != "toy" || got.ID != "zhizhou-youxi" || got.Clean != clean {
			t.Fatalf("%s → %#v %v", raw, got, ok)
		}
		again, ok := Recognize(got.Clean)
		if !ok || again.Clean != got.Clean || again.ID != got.ID {
			t.Fatalf("Clean 再认一遍变了：%#v", again)
		}
	}
	for _, raw := range []string{
		"https://www.bilibili.com/toy/",
		"https://www.bilibili.com/toy/zhizhou-youxi/other.html",
		"https://www.bilibili.com/toy/zhizhou-youxi/index.html/x",
		"https://www.bilibili.com/toy/-bad/index.html",
		"https://www.bilibili.com/toy/a%2Fb/index.html",
	} {
		if got, ok := Recognize(raw); ok {
			t.Fatalf("%s 不该认出：%#v", raw, got)
		}
	}
}

func TestBiliToyFetch(t *testing.T) {
	var seen []string
	f := toyFetcher(t, toyShell, toyBody, &seen)
	l := link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html?spm_id_from=333.788.x.0")
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || strings.Contains(seen[0], "spm_id_from") || !strings.HasSuffix(seen[1], "| https://www.bilibili.com/toy/zhizhou-youxi/") || !strings.Contains(seen[1], "id=700000000000001") {
		t.Fatalf("请求：%v", seen)
	}
	if r.Kind != "toy" || r.Title.Value != "纸舟小游戏" || r.Category.Value != "GAME" || r.Author.Name.Value != "纸灯实验室" || r.Author.ID.Value != "770002" {
		t.Fatalf("%+v", r)
	}
	for key, want := range map[string]int64{"play": 51149, "like": 523, "favorite": 667, "comment": 59, "share": 0} {
		if s := r.Stats[key]; s.State != "available" || s.Value != want {
			t.Fatalf("%s = %+v，应为 %d", key, s, want)
		}
	}
	if r.Published.State != "available" || r.Published.Value.Unix() != 1793035469 {
		t.Fatalf("发布时间：%+v", r.Published)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/static/toy/app/zhizhou-youxi/poster.jpg" || r.Media.Value[0].Width.Value != 1200 || r.Media.Value[0].Height.Value != 900 {
		t.Fatalf("封面：%+v", r.Media)
	}
}

// 外壳页对乱接的路径也回 200 和别的 toy_id：详情里的 slug 对不上，就当不存在。
func TestBiliToySlugMismatchIsNotFound(t *testing.T) {
	var seen []string
	other := strings.Replace(toyBody, "/toy/zhizhou-youxi/", "/toy/other-youxi/", 1)
	f := toyFetcher(t, toyShell, other, &seen)
	_, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/api/index.html"))
	wantError(t, err, "not_found")
	// 详情的 url 指向别的主机也不认
	seen = nil
	wrongHost := strings.Replace(toyBody, "https://www.bilibili.com/toy/", "https://evil.example.invalid/toy/", 1)
	f = toyFetcher(t, toyShell, wrongHost, &seen)
	_, err = f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html"))
	wantError(t, err, "not_found")
	// 详情的编号和外壳给的不一致
	seen = nil
	wrongID := strings.Replace(toyBody, `"id":700000000000001`, `"id":700000000000009`, 1)
	f = toyFetcher(t, toyShell, wrongID, &seen)
	_, err = f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html"))
	wantError(t, err, "not_found")
}

func TestBiliToyFailures(t *testing.T) {
	var seen []string
	// 外壳里没有 toy_id
	f := toyFetcher(t, `<html>没有元数据</html>`, toyBody, &seen)
	_, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html"))
	wantError(t, err, "not_found")
	// 接口回错误码
	f = toyFetcher(t, toyShell, `{"code":-404,"message":"啥都木有"}`, &seen)
	if _, err = f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html")); err == nil {
		t.Fatal("接口报错应当失败")
	}
	// 没有标题
	f = toyFetcher(t, toyShell, `{"code":0,"data":{"id":700000000000001,"url":"https://www.bilibili.com/toy/zhizhou-youxi/index.html"}}`, &seen)
	wantError(t, func() error {
		_, e := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html"))
		return e
	}(), "invalid_response")
	// 缺统计、封面和作者：字段标明没取到，不当失败
	f = toyFetcher(t, toyShell, `{"code":0,"data":{"id":700000000000001,"title":"只有标题","url":"https://www.bilibili.com/toy/zhizhou-youxi/index.html"}}`, &seen)
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/toy/zhizhou-youxi/index.html"))
	if err != nil || r.Author.Name.State == "available" || r.Stats["play"].State == "available" || len(r.Media.Value) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestToyMetaPatterns(t *testing.T) {
	for page, want := range map[string]string{
		`window.__TOY_META__={toy_id:123};`:        "123",
		`window.__TOY_META__ = {"toy_id":"456"}`:   "456",
		`window.__TOY_META__={a:1,toy_id:789,b:2}`: "789",
	} {
		if m := toyMeta.FindStringSubmatch(page); m == nil || m[1] != want {
			t.Fatalf("%s → %v", page, m)
		}
	}
	if toyMeta.MatchString(`window.__TOY_META__={toy_id:abc}`) {
		t.Fatal("非数字编号不该认")
	}
}
