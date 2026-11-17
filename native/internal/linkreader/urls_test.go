package linkreader

import (
	"strings"
	"testing"
)

func TestFindAndCleanURLs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"看https://example.com/p?x=1看这个", "https://example.com/p?x=1"},
		{"(https://example.com/x)", "https://example.com/x"},
		{"https://en.wikipedia.org/wiki/Foo_(bar)", "https://en.wikipedia.org/wiki/Foo_(bar)"},
		{"ｈｔｔｐｓ：／／ｅｘａｍｐｌｅ．ｃｏｍ／ａ？看", "https://example.com/a"},
		{"https://example.com/x?q=1.,;:!", "https://example.com/x?q=1"},
		{"https://example.com/?q=%zz", "https://example.com/?q=%zz"},
		{"https://a.com@evil.com/x", ""}, {"javascript:alert(1)", ""}, {"file:///c:/x", ""}, {"xhttps://example.com/x", ""}, {"github.com/x", ""}, {"https://", ""},
		{"https://example.com/" + strings.Repeat("x", 2048), ""},
	} {
		got := FindURLs(tc.in)
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("应拒绝 %q", tc.in)
			}
			continue
		}
		if len(got) != 1 || got[0].Open != tc.want || got[0].Raw != tc.in[got[0].Start:got[0].End] {
			t.Errorf("%q -> %+v，要 %q", tc.in, got, tc.want)
		}
	}
	if len(FindURLs(strings.Repeat("https://example.com/x ", 21))) != 20 {
		t.Fatal("链接数量上限失效")
	}
	for _, tc := range []struct{ in, want string }{
		{"https://example.com/a?s=1&t=2&ts=3&spm=4&share_source=5", "https://example.com/a?s=1&t=2&ts=3&spm=4&share_source=5"},
		{"https://bilibili.com/video/BV1?p=2&t=3&spm_id_from=x&ts=1", "https://bilibili.com/video/BV1?p=2&t=3"},
		{"https://bilibili.com.evil.cn/a?spm_id_from=x", "https://bilibili.com.evil.cn/a?spm_id_from=x"},
		{"https://notbilibili.com/a?spm_id_from=x", "https://notbilibili.com/a?spm_id_from=x"},
		{"https://x.com/a/status/123?s=1&t=2&token=3", "https://x.com/a/status/123?token=3"},
		{"https://x.com/search?s=1&t=2", "https://x.com/search?s=1&t=2"},
		{"https://youtube.com/watch?v=x&t=3&si=y&pp=z", "https://youtube.com/watch?v=x&t=3"},
		{"https://xiaohongshu.com/a?ShareRedId=x&xsec_token=y&xsec_source=z", "https://xiaohongshu.com/a?xsec_token=y&xsec_source=z"},
		{"https://taobao.com/item?id=1&spm=x", "https://taobao.com/item?id=1"},
		{"https://example.com/a?a=%2F&a=&UTM_SOURCE=x&z=1", "https://example.com/a?a=%2F&a=&z=1"},
	} {
		got, err := CleanAny(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("清理 %q -> %q %v，要 %q", tc.in, got, err, tc.want)
		}
		twice, err := CleanAny(got)
		if err != nil || twice != got {
			t.Errorf("不幂等 %q -> %q", got, twice)
		}
	}
}
