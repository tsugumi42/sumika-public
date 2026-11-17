package ctlproto

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 穷举：每一个码点单独成串，清完不能剩控制字符，清两遍等于清一遍
func TestCleanExhaustive(t *testing.T) {
	for r := rune(0); r <= 0x10ffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue // 代理区不是合法的码点
		}
		in := "甲" + string(r) + "乙"
		got := Clean(in)
		for _, c := range got {
			if (c != '\t' && isControl(c)) || c == '\n' || c == '\r' || c == 0x2028 || c == 0x2029 {
				t.Fatalf("U+%04X 清完还剩控制字符 U+%04X：%q", r, c, got)
			}
		}
		if !utf8.ValidString(got) {
			t.Fatalf("U+%04X 清完不是合法 UTF-8：%q", r, got)
		}
		if Clean(got) != got {
			t.Fatalf("U+%04X 清两遍和清一遍不一样：%q → %q", r, got, Clean(got))
		}
	}
}

func TestCleanKeepsOrdinaryText(t *testing.T) {
	for _, s := range []string{"", "好群", "a\tb", "混合 mixed 123 ！？「」", "👨‍👩‍👧 家人群", "ｆｕｌｌ"} {
		if got := Clean(s); got != s {
			t.Errorf("正常文字被改了：%q → %q", s, got)
		}
	}
}

func TestCleanReplaces(t *testing.T) {
	r := func(c rune) string { return string(c) }
	cases := []struct{ in, want string }{
		{"好群\x1b[2J", "好群?[2J"},
		{"\x1b]0;被改的标题\x07", "?]0;被改的标题?"},
		{"一行\r二行\n三行\r\n四行", "一行 二行 三行  四行"},
		{r(0x2028) + r(0x2029), "  "},
		{"a" + r(0x9b) + "b", "a?b"},      // C1 的 CSI
		{"a\x9bb", "a" + r(0xfffd) + "b"}, // 单个字节的 0x9b 不是合法 UTF-8
		{"坏\xff字节", "坏" + r(0xfffd) + "字节"},
		{r(0x202e) + "反向" + r(0x2069), "?反向?"},
		{strings.Repeat("\x1b", 3), "???"},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q，应是 %q", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	r20 := strings.Repeat("群", 20)
	for in, want := range map[string]string{
		"":                       "",
		"短":                      "短",
		r20:                      r20,
		r20 + "多":                r20 + "…",
		strings.Repeat("甲", 500): strings.Repeat("甲", 20) + "…",
		"👨‍👩‍👧" + r20:            "👨‍👩‍👧" + strings.Repeat("群", 15) + "…", // 按码点算，一个带连接符的 emoji 占好几个
	} {
		if got := Truncate(in, MaxGroupNameRunes); got != want {
			t.Errorf("Truncate(%q) = %q，应是 %q", in, got, want)
		}
	}
}
