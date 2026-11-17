package linkreader

import "testing"

// 复核 BS-05：Bluesky、Mastodon 补出来的外链写进正文前清掉追踪参数（utm_* 任何站都删）；不是 http(s) 的地址只写标题。
func TestLinkCardBlockCleansTracking(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://example.invalid/post?utm_source=bsky&utm_medium=social&id=7", "编的标题\nhttps://example.invalid/post?id=7"},
		{"https://example.invalid/plain", "编的标题\nhttps://example.invalid/plain"},
		{"javascript:alert(1)", "编的标题"},
	} {
		r := newResult(Link{Platform: "bluesky", Kind: "post", ID: "1", Clean: "https://bsky.app/profile/a/post/1"}, "fixture")
		r.Text = available("看这个：")
		r.LinkCard = available(LinkPreview{URL: available(c.url), Title: available("编的标题")})
		appendLinkCardBlock(&r)
		if len(r.Blocks) == 0 || r.Blocks[len(r.Blocks)-1].Text != c.want {
			t.Errorf("%s：%+v", c.url, r.Blocks)
		}
	}
	// 正文里已经有原链接（带追踪参数的那种）：不再重复写链接
	r := newResult(Link{Platform: "bluesky", Kind: "post", ID: "1", Clean: "https://bsky.app/profile/a/post/1"}, "fixture")
	r.Text = available("看这个 https://example.invalid/post?utm_source=bsky")
	r.LinkCard = available(LinkPreview{URL: available("https://example.invalid/post?utm_source=bsky"), Title: available("编的标题")})
	appendLinkCardBlock(&r)
	if got := r.Blocks[len(r.Blocks)-1].Text; got != "编的标题" {
		t.Fatalf("正文里已有链接：%q", got)
	}
}
