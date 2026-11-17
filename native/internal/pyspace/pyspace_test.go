package pyspace

import "testing"

// 手写的几个成员：不依赖 engine 的 Python 导出数据，包自己的单测也能钉住 U+001C～U+001F 这类 Go 的 TrimSpace 不认的字符。
// 对着 Python 全部 0x110000 个码点的逐个比较在 engine 的 TestPySpaceMatchesPython。
func TestIsSpaceMembers(t *testing.T) {
	for _, r := range []rune{'\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x2028, 0x2029, 0x3000} {
		if !IsSpace(r) {
			t.Errorf("U+%04X 该是空白", r)
		}
	}
	for _, r := range []rune{0, 0x1b, 0x20 + 1, 'a', '。', 0x200b, 0x200c, 0x200d, 0xfeff, 0x180e} {
		if IsSpace(r) {
			t.Errorf("U+%04X 不该是空白（零宽字符和 U+180E Python 都不算）", r)
		}
	}
}

func TestBlankAndTrim(t *testing.T) {
	for _, s := range []string{"", " ", "\x1f", "\x1c\x1d\x1e\x1f", " \u3000\t\x1f\u00a0"} {
		if !IsBlank(s) || Trim(s) != "" {
			t.Errorf("%q 该是空白：IsBlank=%v Trim=%q", s, IsBlank(s), Trim(s))
		}
	}
	for _, s := range []string{"a", "\x1fa", "\u200b", "。"} {
		if IsBlank(s) {
			t.Errorf("%q 不该是空白", s)
		}
	}
	if got := Trim("\x1f 甲。\x1c\n"); got != "甲。" {
		t.Errorf("Trim 该去掉首尾的 U+001F、空格、U+001C、换行：%q", got)
	}
	if got := Trim("乙\x1c\x1d。"); got != "乙\x1c\x1d。" {
		t.Errorf("Trim 只去首尾：%q", got)
	}
}
