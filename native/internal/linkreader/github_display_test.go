package linkreader

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// #67 和复核：GitHub 链接发出去保住用户写的大小写，但缓存、去重按小写的 Clean，
// 大小写不同的同一个仓库只取一次、只缓存一份；每次发出去的链接按这一次用户写的。
func TestGitHubDisplayKeepsCase(t *testing.T) {
	a, ok := Recognize("https://github.com/Example-Lab/Widget?utm_source=qq")
	b, ok2 := Recognize("https://github.com/example-lab/WIDGET")
	if !ok || !ok2 || a.Clean != b.Clean || a.Clean != "https://github.com/example-lab/widget" {
		t.Fatalf("干净链接应当一样且小写：%q %q", a.Clean, b.Clean)
	}
	if d := githubDisplay("https://github.com/Example-Lab/Widget?utm_source=qq", a); d != "https://github.com/Example-Lab/Widget" {
		t.Fatalf("展示链接：%q", d)
	}
	if c, _ := Recognize("https://github.com/example-lab/widget"); githubDisplay("https://github.com/example-lab/widget", c) != "" {
		t.Fatal("本来就是小写不该另记")
	}
	// 没写协议、带 www、带 .git、issue 路径：只换 owner/repo 那一段
	if c, _ := Recognize("www.github.com/Example-Lab/Widget.git"); githubDisplay("www.github.com/Example-Lab/Widget.git", c) != "https://github.com/Example-Lab/Widget" {
		t.Fatalf("带 .git：%q", githubDisplay("www.github.com/Example-Lab/Widget.git", c))
	}
	raw := "看看 ÄÖ https://github.com/Example-Lab/Widget/issues/12"
	if c, ok := Recognize("https://github.com/Example-Lab/Widget/issues/12"); !ok || githubDisplay(raw, c) != "https://github.com/Example-Lab/Widget/issues/12" {
		t.Fatalf("issue：%q", githubDisplay(raw, c))
	}
	st, _ := cacheStores(t)
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	f := cacheFetcher{fetch: func(_ context.Context, l Link) (Result, error) {
		calls.Add(1)
		r := newResult(l, "offline")
		r.Title, r.FetchedAt = available("Example-Lab/Widget"), at
		r.DisplayURL = githubDisplay(l.Raw, l) // 和真的 Fetcher 一样，第一次那人写的大小写会跟着进缓存
		return r, nil
	}}
	s := cacheService(t, st, f, func() time.Time { return at })
	ra, err := s.Get(context.Background(), "https://github.com/Example-Lab/Widget")
	if err != nil {
		t.Fatal(err)
	}
	rb, err := s.Get(context.Background(), "https://github.com/example-lab/WIDGET")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("大小写不同的同一个仓库取了 %d 次", calls.Load())
	}
	if ra.ShareURL() != "https://github.com/Example-Lab/Widget" || rb.ShareURL() != "https://github.com/example-lab/WIDGET" {
		t.Fatalf("发出去的链接：%q %q", ra.ShareURL(), rb.ShareURL())
	}
	if ra.CleanURL != rb.CleanURL {
		t.Fatalf("键不一样：%q %q", ra.CleanURL, rb.CleanURL)
	}
	// 这次用户写的是全小写：不沿用缓存里上一个人写的大小写
	if rc, err := s.Get(context.Background(), "https://github.com/example-lab/widget"); err != nil || rc.ShareURL() != "https://github.com/example-lab/widget" {
		t.Fatalf("全小写：%v %q", err, rc.ShareURL())
	}
}

// 样张、link-probe 这类直接调 Fetcher.Fetch 的入口（不经 Service.Get）也拿到保住大小写的展示链接。
func TestGitHubDisplayOnDirectFetch(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		return response(200, fixtureObject(t, "more-github", "repository")), nil
	})
	l, _ := Recognize("https://github.com/Example-Lab/Widget")
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if r.CleanURL != "https://github.com/example-lab/widget" || r.ShareURL() != "https://github.com/Example-Lab/Widget" {
		t.Fatalf("干净 %q 发出 %q", r.CleanURL, r.ShareURL())
	}
}
