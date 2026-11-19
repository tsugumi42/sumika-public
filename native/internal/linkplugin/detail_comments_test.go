package linkplugin

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// 给她看的详情里带热评和充电专属标注；评论是外部文字，会被转义；没有评论时写明确说法。
func TestRenderShowsCommentsAndUpower(t *testing.T) {
	r := linkreader.Result{
		Platform: "bilibili", Kind: "video", CleanURL: "https://www.bilibili.com/video/BV1xx411c7mD", FetchedAt: time.Unix(1, 0),
		Stats: map[string]linkreader.Field[int64]{},
		Flags: map[string]linkreader.Field[any]{
			"is_upower_exclusive": {State: "available", Value: true},
			"is_upower_preview":   {State: "available", Value: true},
		},
		Comments: linkreader.Field[[]linkreader.Comment]{State: "available", Value: []linkreader.Comment{{Author: "用户甲", Text: "编的评论", Likes: 12}}},
	}
	got := Render(r, 0)
	for _, want := range []string{"标注：充电专属（有试看）", "热评：", "1. 用户甲：编的评论（赞 12）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	r.Comments = linkreader.Field[[]linkreader.Comment]{State: "not_returned", Reason: "comments_selected_only"}
	if got := Render(r, 0); !strings.Contains(got, "评论不公开（UP 主精选后才可见）") {
		t.Fatalf("\n%s", got)
	}
	r.Comments = linkreader.Field[[]linkreader.Comment]{}
	r.Flags = nil
	if got := Render(r, 0); strings.Contains(got, "评论") || strings.Contains(got, "标注") {
		t.Fatalf("旧缓存不该多出这两行：\n%s", got)
	}
}
