package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body []byte, header ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body))}
}

// 1x1 的 PNG
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

const samplePost = `{"thread":{"$type":"app.bsky.feed.defs#threadViewPost","post":{
 "uri":"at://did:plc:aaaaaaaaaaaaaaaaaaaaaaaa/app.bsky.feed.post/3aaaaaaaaaaab","cid":"x",
 "author":{"did":"did:plc:aaaaaaaaaaaaaaaaaaaaaaaa","handle":"paper.example.invalid","displayName":"纸云"},
 "record":{"$type":"app.bsky.feed.post","text":"这是编的帖子","createdAt":"2026-11-01T10:30:00Z"},
 "embed":{"$type":"app.bsky.embed.images#view","images":[{"thumb":"https://cdn.bsky.app/img/feed_thumbnail/plain/did:plc:aaaaaaaaaaaaaaaaaaaaaaaa/bafkreiaaaa@jpeg?sig=secret-sig","fullsize":"https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:aaaaaaaaaaaaaaaaaaaaaaaa/bafkreiaaaa@jpeg?sig=secret-sig","alt":"","aspectRatio":{"width":1,"height":1}}]},
 "replyCount":1,"repostCount":2,"likeCount":3,"quoteCount":0,"indexedAt":"2026-11-01T10:30:01Z"}}}`

func TestSampleURLsPicksLinksAndDedupes(t *testing.T) {
	md := "| https://bsky.app/profile/paper.example.invalid/post/3aaaaaaaaaaab | 图文 |\n| https://bsky.app/profile/paper.example.invalid/post/3aaaaaaaaaaab?utm_source=x | 重复 |\n" +
		"| https://example.invalid/whatever/x?token=secret-token | 不认识 |\n| https://bsky.app/profile/paper.example.invalid/feed/whats-hot | feed |\n"
	links, unknown := sampleURLs(md)
	if len(links) != 1 || links[0].Platform != "bluesky" {
		t.Fatalf("链接：%+v", links)
	}
	if len(unknown) != 2 {
		t.Fatalf("认不出的形状：%v", unknown)
	}
	for _, u := range unknown {
		if strings.Contains(u, "secret") || strings.Contains(u, "token") {
			t.Fatalf("认不出的形状不该带查询串：%q", u)
		}
	}
}

// 真实取数、渲染、写目录的整条路径：文字、图片、JSON 都在，JSON 里没有签名；失败的如实写原因；
// 个人账号形状（小黑盒帖子）只写类别，不写链接；全程只发 GET，不连任何聊天平台。
func TestRunSamplesWritesTextImagesAndFailures(t *testing.T) {
	var methods []string
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		methods = append(methods, r.Method)
		switch {
		case strings.Contains(r.URL.Host, "public.api.bsky.app"):
			return reply(200, []byte(samplePost)), nil
		case r.URL.Host == "cdn.bsky.app":
			return reply(200, tinyPNG, "Content-Type", "image/png"), nil
		case r.URL.Host == "booth.pm":
			return reply(404, nil), nil
		}
		t.Fatalf("不该请求的地址：%s", r.URL)
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	md := "https://bsky.app/profile/paper.example.invalid/post/3aaaaaaaaaaab\nhttps://booth.pm/ja/items/90001\nhttps://www.xiaoheihe.cn/app/bbs/link/90099\nhttps://example.invalid/x/y?token=secret-token\n"
	links, unknown := sampleURLs(md)
	out := t.TempDir()
	err = runSamples(context.Background(), f, links, unknown, sampleOptions{Out: out, MaxPerSite: 5, Now: func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	bsky := read("bluesky.md")
	if strings.Contains(bsky, "Bluesky 帖子") {
		t.Fatalf("bluesky.md 不该写平台名：\n%s", bsky)
	}
	for _, want := range []string{"> 纸云[@paper.example.invalid]：\n", "> 「这是编的帖子」", "![图片](bluesky/01/image-1.png)", "点赞 3 | 转发 2 | 回复 1 | 引用 0"} {
		if !strings.Contains(bsky, want) {
			t.Fatalf("bluesky.md 缺 %q：\n%s", want, bsky)
		}
	}
	if _, err = os.Stat(filepath.Join(out, "bluesky", "01", "image-1.png")); err != nil {
		t.Fatal("图没下到：", err)
	}
	for _, name := range []string{"result.json", "draft.json"} {
		if j := read(filepath.Join("bluesky", "01", name)); strings.Contains(j, "secret-sig") || !strings.Contains(j, "?redacted") {
			t.Fatalf("%s 里的签名没打掉：\n%s", name, j)
		}
	}
	if booth := read("booth.md"); !strings.Contains(booth, "取数失败：not_found") {
		t.Fatalf("失败原因没写：\n%s", booth)
	}
	hey := read("xiaoheihe.md")
	if strings.Contains(hey, "90099") || !strings.Contains(hey, "不写进样张") || !strings.Contains(hey, "不进自动展开") {
		t.Fatalf("小黑盒帖子：\n%s", hey)
	}
	idx := read("README.md")
	if strings.Contains(idx, "secret") || strings.Contains(idx, "90099") || !strings.Contains(idx, "认不出的形状") {
		t.Fatalf("README：\n%s", idx)
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Fatalf("只许 GET：%v", methods)
		}
	}
}
