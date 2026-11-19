package main

import (
	"regexp"
	"strings"
	"testing"
)

func fakeGallery() galleryOut {
	return galleryOut{Run: runInfo{At: "2026-11-12 20:00:00", Theme: "auto", Video: "开", Translate: "开", Deviations: []string{"假的偏离"}}, Samples: []sampleOut{
		{Code: "X-01", URL: "https://x.com/a/status/1", ListPlat: "x", ListKind: "post", Variant: "引用 <b>推文</b>", Platform: "x", Kind: "post", PlatLabel: "X / 推特", KindLabel: "推文", Title: "标题", Route: "fx_v2", FetchMs: 1234,
			Styles: []styleOut{
				{ID: "default", Label: "默认", Hint: "原来的排法", Messages: []msgOut{{Kind: "forward", Nodes: []nodeOut{
					{Name: "link-reader", Self: true, ReplyTo: 2, Items: []itemOut{{Type: "text", Text: "第一行\n第二行"}}},
					{Name: "link-reader", Self: true, Items: []itemOut{{Type: "image", File: "files/a.jpg", W: 1200, H: 800}}},
					{Name: "link-reader", Self: true, Items: []itemOut{{Type: "video", Cover: "files/v-cover.jpg", W: 1280, H: 720, Size: 3 << 20, Seconds: 75}}},
				}}}},
				{ID: "text", Label: "图文", Hint: "一条消息", Messages: []msgOut{{Kind: "message", Quote: true, Items: []itemOut{
					{Type: "text", Text: "链接\n作者："}, {Type: "image", File: "files/a.jpg", W: 1200, H: 800}, {Type: "image", File: "files/b.jpg", W: 100, H: 100}}}}},
				{ID: "card", Label: "卡片", NotSent: "取到了但没内容", Hint: "卡片"},
			}},
		{Code: "XHS-01", URL: "https://www.xiaohongshu.com/explore/1", ListPlat: "xiaohongshu", ListKind: "note", PlatLabel: "小红书", KindLabel: "笔记", Error: "login_required", NotSent: "取数失败：群里不发文字"},
	}}
}

func TestPageAnchorsShapesAndNoInlineData(t *testing.T) {
	h := page(fakeGallery())
	if strings.Contains(h, "data:") {
		t.Fatal("页面不能内嵌 data: 图片，一律用 files/ 相对路径")
	}
	if strings.Contains(h, "prefers-color-scheme") {
		t.Fatal("要浅色主题，不跟系统深色")
	}
	ids := map[string]int{}
	for _, m := range regexp.MustCompile(`<section class="sample[^"]*" id="([^"]+)"`).FindAllStringSubmatch(h, -1) {
		ids[m[1]]++
	}
	if len(ids) != 2 || ids["X-01"] != 1 || ids["XHS-01"] != 1 {
		t.Fatalf("每条一个唯一锚点：%v", ids)
	}
	nav := regexp.MustCompile(`(?s)<nav>.*</nav>`).FindString(h)
	links := regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(nav, -1)
	if len(links) != 2 {
		t.Fatalf("目录每条一个链接：%v", links)
	}
	for _, a := range links {
		if ids[a[1]] != 1 {
			t.Fatalf("目录链到不存在的区块 %s", a[1])
		}
	}
	for _, want := range []string{
		"查看3条转发消息", "展开的 3 个节点", `class="reply">引用第 2 个节点`, `class="reply">群友：https://x.com/a/status/1`,
		"发 1 条：合并转发（3 个节点）", "发 1 条：文字+图×2（回复原消息）", "取到了但没内容", "取不到：login_required", "取数失败：群里不发文字",
		`loading="lazy"`, `src="files/a.jpg" width="260" height="173"`, `src="files/b.jpg" width="100" height="100"`, "1:15 · 3.0 MB · 1280×720",
		"第一行\n第二行", "引用 &lt;b&gt;推文&lt;/b&gt;", "和线上不同：假的偏离", "线路 fx_v2", "发送号的 QQ 昵称",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("页面里缺 %q", want)
		}
	}
	if strings.Contains(h, "<b>推文</b>") {
		t.Error("清单里的文字要转义")
	}
}

func TestThumbKeepsRatio(t *testing.T) {
	if w, h := thumb(1000, 500, 250); w != 250 || h != 125 {
		t.Fatalf("%d×%d", w, h)
	}
	if w, h := thumb(80, 60, 250); w != 80 || h != 60 {
		t.Fatalf("小图不放大：%d×%d", w, h)
	}
}
