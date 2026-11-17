package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// 只放译文时，原文的各种形状都要被换掉、不能留一份在旁边：正文带首尾空白换行、拆成多个文字块、只有 Text 没有块、
// 后面跟着外链卡（卡的文字留在原位）、末尾是链接。
func TestTranslatedBodyReplacesEveryOriginalShape(t *testing.T) {
	const tr = "你好，火箭和月亮"
	never := map[string]SiteSetting{"x": {Forward: "never"}}
	cases := []struct {
		name string
		mk   func() linkreader.Result
		keep string // 要留在原位的别的文字；空表示没有
	}{
		{"首尾空白换行", func() linkreader.Result { return xPost("  Hello rockets and moons \n\n") }, ""},
		{"多个文字块", func() linkreader.Result {
			r := xPost("Hello rockets")
			r.Blocks = append(r.Blocks, linkreader.Block{Type: "text", Text: "and moons"})
			return r
		}, ""},
		{"只有 Text 没有块", func() linkreader.Result {
			r := goldenBase("x", "post", "80003")
			r.Text = av("Hello rockets and moons")
			return r
		}, ""},
		{"正文后面跟外链卡", func() linkreader.Result {
			r := xPost("Hello rockets and moons")
			r.Blocks = append(r.Blocks, linkreader.Block{Type: "link_card", Text: "纸船博客：试航记录"})
			return r
		}, "纸船博客：试航记录"},
		{"末尾是链接", func() linkreader.Result {
			return xPost("Hello rockets and moons\nhttps://example.invalid/blog/1")
		}, "https://example.invalid/blog/1"},
	}
	for _, c := range cases {
		r := c.mk()
		got := descTr(&r, Options{Sites: never, Translation: tr})
		if strings.Contains(got, "Hello rockets") || strings.Contains(got, "and moons") {
			t.Errorf("%s：原文还在：%q", c.name, got)
		}
		if !strings.Contains(got, tr) || !strings.Contains(got, "（翻译自") {
			t.Errorf("%s：没有译文或翻译自：%q", c.name, got)
		}
		if c.keep != "" && strings.Count(got, c.keep) != 1 {
			t.Errorf("%s：%q 应留在原位一份：%q", c.name, c.keep, got)
		}
	}
}

// 链接后面紧跟中日文（没有空格）：链接只算到 ASCII 结束；译文里丢了链接补回时，补的只是链接本身，不带原文的尾巴。
func TestLinkFollowedByCJKIsNotSwallowed(t *testing.T) {
	long := "见https://example.invalid/abc，" + strings.Repeat("字", 5600) // 16800 多字节，超过默认门槛
	if got := asciiURLRe.FindAllString(long, -1); len(got) != 1 || got[0] != "https://example.invalid/abc" {
		t.Fatalf("链接后的汉字被吞进链接了：%q", got)
	}
	for _, key := range []string{"x", "telegram"} {
		var r linkreader.Result
		if key == "telegram" {
			r = tgResult(long, 0)
		} else {
			r = xImages(0)
			r.Text = av(long)
			r.Blocks = []linkreader.Block{{Type: "text", Text: long}}
		}
		if got := strings.Join(kinds(build(r, Options{})), ","); got != "forward" {
			t.Errorf("%s：链接加 400 个汉字应是长：%s", key, got)
		}
	}
	// 补回链接：日文原文里链接后面紧跟假名
	const jp = "詳しくはhttps://example.invalid/abcをご覧ください"
	if got := keepOriginalLinks("详情请看这里", jp); got != "详情请看这里\nhttps://example.invalid/abc" {
		t.Fatalf("补回的应只是链接：%q", got)
	}
	r := xPost(jp)
	out := descTr(&r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Translation: "详情请看 https://example.invalid/abc 这里"})
	if strings.Contains(out, "をご覧") || strings.Count(out, "https://example.invalid/abc") != 1 {
		t.Fatalf("译文里已有链接不补，也不带原文尾巴：%q", out)
	}
	out = descTr(&r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Translation: "详情请看这里"})
	if strings.Contains(out, "をご覧") || strings.Count(out, "https://example.invalid/abc") != 1 {
		t.Fatalf("译文丢了链接要补回，且不带原文尾巴：%q", out)
	}
}
