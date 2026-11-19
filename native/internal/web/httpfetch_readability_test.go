package web_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"sumika-native/internal/web"
)

func htmlPage(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, body)
}

// articleHTML 是一篇有导航、侧栏、页脚的文章页：正文该被挑出来，周围的杂项该被丢掉。
func articleHTML() string {
	para := strings.Repeat("莫比乌斯环是一种只有一个面和一条边界的曲面，把纸带扭转半圈再首尾相接就能做出来。", 6)
	return `<!doctype html><html><head><title>莫比乌斯环 - 示例百科</title></head><body>
<nav><ul><li><a href="/a">首页导航甲</a></li><li><a href="/b">首页导航乙</a></li></ul></nav>
<div class="sidebar"><p>订阅我们的周刊获得更多优惠</p></div>
<article><h1>莫比乌斯环</h1><p>` + para + `</p><p>` + para + `</p><p>` + para + `</p></article>
<footer><p>版权所有 页脚杂项文字</p></footer></body></html>`
}

func TestFetchPicksArticleBodyAndTitle(t *testing.T) {
	r := hostResolver{"example.com": {publicAddr}}
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) { htmlPage(w, articleHTML()) }, nil)
	res, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/a", Format: "markdown"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "只有一个面和一条边界") {
		t.Fatalf("正文没了：%q", res.Content)
	}
	for _, noise := range []string{"首页导航甲", "订阅我们的周刊", "页脚杂项"} {
		if strings.Contains(res.Content, noise) {
			t.Errorf("杂项 %q 还在正文里：%q", noise, res.Content)
		}
	}
	if !strings.Contains(res.Title, "莫比乌斯环") {
		t.Errorf("标题：%q", res.Title)
	}
	text, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/b", Format: "text"})
	if err != nil || strings.Contains(text.Content, "首页导航甲") || !strings.Contains(text.Content, "只有一个面") {
		t.Fatalf("text 格式也该只有正文：%q %v", text.Content, err)
	}
}

func TestFetchFallsBackToWholePageWhenNoArticle(t *testing.T) {
	r := hostResolver{"example.com": {publicAddr}}
	short := `<html><head><title>小页面</title></head><body><nav><a href="/x">导航链接</a></nav><p>只有一句短话。</p></body></html>`
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) { htmlPage(w, short) }, nil)
	res, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/s", Format: "markdown"})
	if err != nil || !strings.Contains(res.Content, "只有一句短话") || !strings.Contains(res.Content, "导航链接") {
		t.Fatalf("挑不出正文应退回整页：%q %v", res.Content, err)
	}
	if res.Title != "小页面" {
		t.Errorf("退回整页时标题取 <title>：%q", res.Title)
	}
}

func TestFetchKeepsWholePageForIndexLikePages(t *testing.T) {
	r := hostResolver{"example.com": {publicAddr}}
	var b strings.Builder
	b.WriteString(`<html><head><title>列表</title></head><body><ul>`)
	for i := 0; i < 200; i++ {
		b.WriteString(`<li><a href="/p/` + strings.Repeat("x", i%7) + `">条目标题第` + strings.Repeat("甲", 8) + `</a></li>`)
	}
	b.WriteString(`</ul></body></html>`)
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) { htmlPage(w, b.String()) }, nil)
	res, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/l", Format: "markdown"})
	if err != nil || strings.Count(res.Content, "条目标题第") < 150 {
		t.Fatalf("列表页不该只剩一小块：%d %v", strings.Count(res.Content, "条目标题第"), err)
	}
}
