package linkreader

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestFromCardFields(t *testing.T) {
	paths := []struct {
		group  string
		fields []string
	}{
		{"detail_1", []string{"qqdocurl", "url", "jumpUrl", "sourceUrl", "shareUrl", "targetUrl"}},
		{"news", []string{"jumpUrl", "url", "sourceUrl", "shareUrl"}},
		{"music", []string{"musicUrl", "jumpUrl", "url"}},
		{"video", []string{"jumpUrl"}},
		{"miniapp", []string{"legacyUrl", "pcJumpUrl", "jumpUrl", "sourceUrl", "url"}},
	}
	for _, p := range paths {
		for _, field := range p.fields {
			t.Run(p.group+"/"+field, func(t *testing.T) {
				raw := fmt.Sprintf(`{"prompt":"提示","meta":{"%s":{"%s":"https://b23.tv/fake?share_source=qq","title":"标题","desc":"描述"}}}`, p.group, field)
				got, info := FromCard(raw)
				want := []Link{{"bilibili", "short", "", "https://b23.tv/fake", "https://b23.tv/fake?share_source=qq"}}
				if !reflect.DeepEqual(got, want) || info != (CardInfo{"标题", "描述", "提示"}) {
					t.Fatalf("got %#v %#v; want %#v", got, info, want)
				}
			})
		}
	}
}

func TestFromCardWrappersAndEncoding(t *testing.T) {
	card := `{"meta":{"news":{"jumpUrl":"https://b23.tv/fake?utm_source=qq"}}}`
	quoted, _ := json.Marshal(card)
	data, _ := json.Marshal(map[string]string{"data": card})
	segment, _ := json.Marshal(map[string]any{"type": "json", "data": map[string]string{"data": card}})
	for _, raw := range []string{card, string(quoted), string(data), string(segment), `{"data":{"meta":{"news":{"jumpUrl":"https://b23.tv/fake?utm_source=qq"}}}}`, `{"meta":{"news":{"jumpUrl":"https:\/\/b23.tv\/fake?utm_source=qq"}}}`, `{"meta":{"news":{"jumpUrl":"https%3A%2F%2Fb23.tv%2Ffake%3Futm_source%3Dqq"}}}`, `{"meta":{"news":{"jumpUrl":"https:\\/\\/b23.tv\\/fake?utm_source=qq"}}}`} {
		t.Run(raw, func(t *testing.T) {
			got, _ := FromCard(raw)
			if len(got) != 1 || got[0].Clean != "https://b23.tv/fake" {
				t.Fatalf("got %#v", got)
			}
		})
	}
}

func TestFromCardSiblingOrder(t *testing.T) {
	raw := `{"prompt":"分享","meta":{"detail_1":{"qqdocurl":23,"url":"https://twitter.com/a/status/1?s=20"},"news":{"jumpUrl":"https://x.com/a/status/1"},"music":{"jumpUrl":"b23.tv/fake"},"video":{"jumpUrl":"https://x.com/a/status/2"}}}`
	links, info := FromCard(raw)
	want := []Link{{"x", "post", "1", "https://x.com/a/status/1", "https://twitter.com/a/status/1?s=20"}, {"bilibili", "short", "", "https://b23.tv/fake", "b23.tv/fake"}, {"x", "post", "2", "https://x.com/a/status/2", "https://x.com/a/status/2"}}
	if !reflect.DeepEqual(links, want) || info != (CardInfo{"分享", "", "分享"}) {
		t.Fatalf("got %#v %#v", links, info)
	}
}

func TestFromCardFallback(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want CardInfo
	}{
		{`{"prompt":"提示","meta":{"unknown":{"title":"未知标题","desc":"未知描述","cover":"https://b23.tv/cover"}}}`, CardInfo{"未知标题", "未知描述", "提示"}},
		{`{"prompt":"仅提示","desc":"根描述"}`, CardInfo{"仅提示", "根描述", "仅提示"}},
		{`{"title":"根标题","desc":"根描述","prompt":"提示","meta":{"z":{"title":"后","desc":"后"},"a":{"title":"先","desc":"先"}}}`, CardInfo{"根标题", "根描述", "提示"}},
		{`{"meta":{"z":{"title":"后","desc":"后"},"a":{"title":"先","desc":"先"}}}`, CardInfo{"先", "先", ""}},
	} {
		t.Run(c.raw, func(t *testing.T) {
			links, info := FromCard(c.raw)
			if len(links) != 0 || info != c.want {
				t.Fatalf("got %#v %#v want %#v", links, info, c.want)
			}
		})
	}
}

func TestFromCardWrongShapes(t *testing.T) {
	// 字段全部入口：根、meta、分组、URL、标题和描述；坏兄弟不能吞掉合法 news。
	for _, bad := range []string{`null`, `false`, `0`, `[]`, `{}`, `""`} {
		for _, format := range []string{
			`{"meta":{"detail_1":%s,"news":{"jumpUrl":"b23.tv/fake"}}}`,
			`{"meta":{"detail_1":{"qqdocurl":%s},"news":{"jumpUrl":"b23.tv/fake"}}}`,
			`{"prompt":%s,"meta":{"news":{"jumpUrl":"b23.tv/fake"}}}`,
			`{"meta":{"detail_1":{"title":%s},"news":{"jumpUrl":"b23.tv/fake"}}}`,
			`{"meta":{"detail_1":{"desc":%s},"news":{"jumpUrl":"b23.tv/fake"}}}`,
		} {
			raw := fmt.Sprintf(format, bad)
			t.Run(raw, func(t *testing.T) {
				links, info := FromCard(raw)
				if len(links) != 1 || links[0].Clean != "https://b23.tv/fake" || info != (CardInfo{}) {
					t.Fatalf("got %#v %#v", links, info)
				}
			})
		}
		for _, raw := range []string{bad, fmt.Sprintf(`{"meta":%s}`, bad)} {
			if links, info := FromCard(raw); len(links) != 0 || info != (CardInfo{}) {
				t.Fatalf("%s: %#v %#v", raw, links, info)
			}
		}
	}
}

func TestFromCardHostileBounds(t *testing.T) {
	for _, raw := range []string{"", `{`, `{"prompt":"good","meta":`, `{"meta":{}} trailing`, strings.Repeat("[", 1000) + `{"prompt":"deep"}` + strings.Repeat("]", 1000), `{"prompt":"deep","other":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + "}", `{"prompt":"large","padding":"` + strings.Repeat("a", 1<<20) + `"}`} {
		if links, info := FromCard(raw); len(links) != 0 || info != (CardInfo{}) {
			t.Fatalf("accepted hostile card, bytes=%d: links=%d info=%#v", len(raw), len(links), info)
		}
	}
	// 正好 1 MiB 与深度 64 仍能读取正文；不是一律拒绝大卡片。
	prefix, suffix := `{"prompt":"edge","padding":"`, `"}`
	raw := prefix + strings.Repeat("a", (1<<20)-len(prefix)-len(suffix)) + suffix
	if links, info := FromCard(raw); len(links) != 0 || info != (CardInfo{"edge", "", "edge"}) {
		t.Fatalf("size boundary: %#v %#v", links, info)
	}
	raw = `{"prompt":"edge","other":` + strings.Repeat("[", 63) + "0" + strings.Repeat("]", 63) + "}"
	if links, info := FromCard(raw); len(links) != 0 || info != (CardInfo{"edge", "", "edge"}) {
		t.Fatalf("depth boundary: %#v %#v", links, info)
	}
	raw = `{"prompt":"braces [ { \\\"","meta":{"news":{"jumpUrl":"file://x.com/a/status/1"}}}`
	if links, info := FromCard(raw); len(links) != 0 || info.Prompt != "braces [ { \\\"" {
		t.Fatalf("quoted delimiters: %#v %#v", links, info)
	}
}

func TestFromCardQuotedDelimiters(t *testing.T) {
	prompt := strings.Repeat("[{\\\"", 1000)
	b, err := json.Marshal(map[string]string{"prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	links, info := FromCard(string(b))
	if len(links) != 0 || info != (CardInfo{prompt, "", prompt}) {
		t.Fatalf("braces inside string treated as JSON nesting: links=%d title bytes=%d", len(links), len(info.Title))
	}
}

func FuzzExtract(f *testing.F) {
	for _, seed := range []string{"", "看https://x.com/a/status/1。", "（b23.tv/fake）", "https://u:p@x.com/a/status/1", "https://x.com/%zz", "https://[::1]/", "javascript://x.com/a/status/1", "ｘ．ｃｏｍ／a／status／１", "x.com/a/status/1?s=20&utm_source=qq https://twitter.com/a/status/1", string([]byte{0xff, 0xfe})} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) { assertLinkInvariants(t, Extract(text)) })
}

func FuzzFromCard(f *testing.F) {
	for _, seed := range []string{"", `{`, `{"meta":{"detail_1":{"qqdocurl":"b23.tv/fake"}}}`, `{"meta":{"news":{"jumpUrl":[]}}}`, `{"meta":{"detail_1":{"qqdocurl":"b23.tv/fake?share_source=qq&utm_source=qq"},"news":{"jumpUrl":"https://b23.tv/fake"}}}`, strings.Repeat("[", 1000) + "0" + strings.Repeat("]", 1000)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		links, info := FromCard(raw)
		assertLinkInvariants(t, links)
		again, other := FromCard(raw)
		if !reflect.DeepEqual(links, again) || info != other {
			t.Fatal("non-deterministic card")
		}
	})
}

func assertLinkInvariants(t *testing.T, links []Link) {
	t.Helper()
	seen := make(map[string]bool)
	for _, l := range links {
		again, ok := Recognize(l.Clean)
		if !ok || again.Clean != l.Clean || again.Platform != l.Platform || again.Kind != l.Kind || again.ID != l.ID || seen[l.Clean] {
			t.Fatalf("invalid/non-idempotent/duplicate link: %#v", l)
		}
		seen[l.Clean] = true
	}
}
