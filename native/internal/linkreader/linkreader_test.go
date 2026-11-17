package linkreader

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestRecognizeShapes(t *testing.T) {
	cases := []struct{ raw, platform, kind, id, clean string }{
		{"https://x.com/example_lab/status/101", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"http://twitter.com/example_lab/status/101?s=20", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"https://mobile.twitter.com/example_lab/status/101/photo/1?t=token", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"https://fxtwitter.com/example_lab/status/101/video/1", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"https://vxtwitter.com/example_lab/status/101/photo/2", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"fixupx.com/example_lab/status/101/", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"https://WWW.X.COM:443/example_lab/status/101", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"http://www.twitter.com:80/example_lab/status/102", "x", "post", "102", "https://x.com/example_lab/status/102"},
		{"mobile.x.com/example_lab/status/103", "x", "post", "103", "https://x.com/example_lab/status/103"},
		{" ｘ．ｃｏｍ／example_lab／status／１０１ ", "x", "post", "101", "https://x.com/example_lab/status/101"},
		{"https://t.co/fake123?s=20&next=keep", "x", "short", "", "https://t.co/fake123?next=keep"},
		{"https://www.bilibili.com/video/BV1Ab2Cd3Ef4/?p=2&t=90&spm_id_from=share", "bilibili", "video", "BV1Ab2Cd3Ef4", "https://www.bilibili.com/video/BV1Ab2Cd3Ef4?p=2&t=90"},
		{"https://m.bilibili.com/video/av123456?p=3&t=12", "bilibili", "video", "av123456", "https://www.bilibili.com/video/av123456?p=3&t=12"},
		{"bilibili.com/bv1Ab2Cd3Ef4", "bilibili", "video", "BV1Ab2Cd3Ef4", "https://www.bilibili.com/video/BV1Ab2Cd3Ef4"},
		{"https://bilibili.com/AV123456", "bilibili", "video", "av123456", "https://www.bilibili.com/video/av123456"},
		{"b23.tv/fake123?share_source=qq&t=9", "bilibili", "short", "", "https://b23.tv/fake123?t=9"},
		{"https://bili2233.cn/fake456", "bilibili", "short", "", "https://bili2233.cn/fake456"},
		{"https://t.bilibili.com/123456/", "bilibili", "dynamic", "123456", "https://t.bilibili.com/123456"},
		{"https://m.bilibili.com/dynamic/123456", "bilibili", "dynamic", "123456", "https://t.bilibili.com/123456"},
		{"https://www.bilibili.com/opus/123456/", "bilibili", "opus", "123456", "https://www.bilibili.com/opus/123456"},
		{"https://www.bilibili.com/read/cv123456/", "bilibili", "article", "cv123456", "https://www.bilibili.com/read/cv123456"},
		{"https://live.bilibili.com/123456/?share_source=qq", "bilibili", "live", "123456", "https://live.bilibili.com/123456"},
		{"https://www.bilibili.com/bangumi/play/ep123456", "bilibili", "episode", "ep123456", "https://www.bilibili.com/bangumi/play/ep123456"},
		{"https://www.bilibili.com/bangumi/play/ss123456", "bilibili", "season", "ss123456", "https://www.bilibili.com/bangumi/play/ss123456"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, ok := Recognize(c.raw)
			want := Link{Platform: c.platform, Kind: c.kind, ID: c.id, Clean: c.clean, Raw: c.raw}
			if !ok || got != want {
				t.Fatalf("Recognize = %#v, %v; want %#v", got, ok, want)
			}
		})
	}
}

func TestRejectURLs(t *testing.T) {
	for _, raw := range []string{
		"", "http://", "https://x.com/%zz", "https://[::1]/a/status/1", "https://u:p@x.com/a/status/1", "https://u@b23.tv/fake", "javascript://x.com/a/status/1", "javascript:https://x.com/a/status/1", "file://x.com/a/status/1",
		"https://x.com.evil.test/a/status/1", "https://evilx.com/a/status/1", "https://x.com:8443/a/status/1", "https://x.com:/a/status/1", "https://x.com/a/status/1/extra", "https://x.com/a/status/not-id", "https://x.com/a/status/1?x=%zz", "https://x.com/a/status/1?%zz=x", "https://x.com/a\n/status/1", "https://www.bilibili.com/video/BVshort", "https://www.bilibili.com/opus/no", "https://www.bilibili.com/read/cv", "https://www.bilibili.com/bangumi/play/ep", "https://b23.tv/", "https://b23.tv/fake/other", "https://x.com/a/status/1\\evil",
	} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			if got, ok := Recognize(raw); ok || got != (Link{}) {
				t.Fatalf("accepted %#v", got)
			}
			if got := Clean(raw); got != "" {
				t.Fatalf("Clean = %q", got)
			}
		})
	}
}

// 列表与生产表独立；删生产表任意一项都必须报出对应参数。
func TestTrackingTables(t *testing.T) {
	common := []string{"utm_source", "utm_medium", "utm_campaign", "utm_custom", "spm", "fbclid", "gclid"}
	x := []string{"s", "t", "ref_src", "ref_url"}
	bili := []string{"spm_id_from", "share_source", "share_medium", "share_plat", "share_session_id", "share_tag", "share_from", "vd_source", "bbid", "ts", "timestamp", "unique_k", "from_spmid", "plat_id", "buvid", "up_id"}
	for _, platform := range []string{"x", "bilibili"} {
		base, want := "https://x.com/a/status/1?", "https://x.com/a/status/1?unknown=a%2fb&unknown=&bare"
		keys := append([]string(nil), common...)
		if platform == "x" {
			keys = append(keys, x...)
		} else {
			base = "https://www.bilibili.com/video/av123456?"
			want = "https://www.bilibili.com/video/av123456?unknown=a%2fb&unknown=&bare"
			keys = append(keys, bili...)
		}
		for _, key := range keys {
			t.Run(platform+"/"+key, func(t *testing.T) {
				for _, k := range []string{key, strings.ToUpper(key), fmt.Sprintf("%%%02x%s", key[0], key[1:])} {
					raw := base + k + "=fake&unknown=a%2fb&unknown=&bare&" + k + "=duplicate"
					if got := Clean(raw); got != want {
						t.Errorf("%s: got %q; want %q", k, got, want)
					}
				}
			})
		}
	}
	for _, c := range []struct{ raw, want string }{
		{"https://www.bilibili.com/video/av123456?p=2&p=3&t=12&t=90&s=keep&ref_src=keep", "https://www.bilibili.com/video/av123456?p=2&p=3&t=12&t=90&s=keep&ref_src=keep"},
		{"https://x.com/a/status/1?p=2&share_source=keep&%75tm_source=drop&next=a%26b%3Dc#keep", "https://x.com/a/status/1?p=2&share_source=keep&next=a%26b%3Dc#keep"},
		{"https://x.com/a/status/1?bare&&empty=&utm_source=x&last=+", "https://x.com/a/status/1?bare&&empty=&last=+"},
		{"https://x.com/a/status/1?token=Ａ%2f&sig=．．", "https://x.com/a/status/1?token=Ａ%2f&sig=．．"},
		{"https://ｘ．ｃｏｍ／a／status／１？token=Ａ%2f", "https://x.com/a/status/1?token=Ａ%2f"},
	} {
		if got := Clean(c.raw); got != c.want {
			t.Errorf("got %q; want %q", got, c.want)
		}
	}
}

func TestCleanIdempotentCombinations(t *testing.T) {
	n := 0
	for _, base := range []string{"https://twitter.com/a/status/1/photo/1", "https://m.bilibili.com/video/BV1Ab2Cd3Ef4", "https://b23.tv/fake"} {
		for i := range 2048 {
			raw := fmt.Sprintf("%s?utm_source=%d&spm=x&unknown=%%2f&unknown=&bare&p=%d&p=3", base, i, i%9+1)
			if strings.Contains(base, "twitter") {
				raw += "&s=20&t=secret&ref_src=x&ref_url=x"
			} else {
				raw += "&t=12&t=90&vd_source=x&share_source=qq"
			}
			one := Clean(raw)
			if one == "" || Clean(one) != one {
				t.Fatalf("not idempotent: %q -> %q -> %q", raw, one, Clean(one))
			}
			wantQuery := fmt.Sprintf("unknown=%%2f&unknown=&bare&p=%d&p=3", i%9+1)
			if !strings.Contains(base, "twitter") {
				wantQuery += "&t=12&t=90"
			}
			u, err := url.Parse(one)
			if err != nil || u.RawQuery != wantQuery {
				t.Fatalf("lost content/unknown params: %q want %q", one, wantQuery)
			}
			n++
		}
	}
	t.Logf("checked %d combinations", n)
}

func TestExtractBoundariesAndOrder(t *testing.T) {
	cases := []struct {
		text string
		want []Link
	}{
		{"看这个https://x.com/a/status/1。", []Link{{"x", "post", "1", "https://x.com/a/status/1", "https://x.com/a/status/1"}}},
		{"（https://b23.tv/xxx）", []Link{{"bilibili", "short", "", "https://b23.tv/xxx", "https://b23.tv/xxx"}}},
		{"【ｈｔｔｐｓ：／／ｘ．ｃｏｍ／a／status／１】", []Link{{"x", "post", "1", "https://x.com/a/status/1", "ｈｔｔｐｓ：／／ｘ．ｃｏｍ／a／status／１"}}},
		{"https://x.com/a/status/1?label=你好。", []Link{{"x", "post", "1", "https://x.com/a/status/1?label=你好", "https://x.com/a/status/1?label=你好"}}},
		{"（ｘ．ｃｏｍ／a／status／１？token=Ａ%2f）", []Link{{"x", "post", "1", "https://x.com/a/status/1?token=Ａ%2f", "ｘ．ｃｏｍ／a／status／１？token=Ａ%2f"}}},
		{"https://x.com/a/status/1?unknown=end?!,.", []Link{{"x", "post", "1", "https://x.com/a/status/1?unknown=end?!,.", "https://x.com/a/status/1?unknown=end?!,."}}},
		{"x.com/a/status/1，b23.tv/xxx! https://twitter.com/a/status/1?s=20 https://b23.tv/xxx?utm_source=qq", []Link{{"x", "post", "1", "https://x.com/a/status/1", "x.com/a/status/1"}, {"bilibili", "short", "", "https://b23.tv/xxx", "b23.tv/xxx"}}},
		{"(https://x.com/a/status/1),[https://x.com/a/status/2];\r\nb23.tv/xxx", []Link{{"x", "post", "1", "https://x.com/a/status/1", "https://x.com/a/status/1"}, {"x", "post", "2", "https://x.com/a/status/2", "https://x.com/a/status/2"}, {"bilibili", "short", "", "https://b23.tv/xxx", "b23.tv/xxx"}}},
		{"https://u:p@x.com/a/status/1 file://x.com/a/status/2 javascript:https://x.com/a/status/3 https://evil.test/x.com/a/status/4 evilx.com/a/status/5 a" + "@" + "x.com/a/status/6", nil},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			if got := Extract(c.text); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v; want %#v", got, c.want)
			}
		})
	}
}

func TestExtractHostileScale(t *testing.T) {
	text := strings.Repeat("敌", (1<<20)/3) + " https://x.com/a/status/1"
	want := []Link{{"x", "post", "1", "https://x.com/a/status/1", "https://x.com/a/status/1"}}
	if got := Extract(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("1 MB text: %#v", got)
	}
	var b strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&b, "x.com/a/status/%d?utm_source=qq ", i)
	}
	links := Extract(b.String())
	if len(links) != 4000 {
		t.Fatalf("got %d links", len(links))
	}
	for i, l := range links {
		if want := fmt.Sprintf("https://x.com/a/status/%d", i); l.Clean != want {
			t.Fatalf("row %d = %q want %q", i, l.Clean, want)
		}
	}
	t.Logf("text bytes=%d; links=%d", len(text), len(links))
}
