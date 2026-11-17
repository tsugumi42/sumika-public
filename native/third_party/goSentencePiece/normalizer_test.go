package sentencepiece

import (
	"errors"
	"testing"
)

func TestCollapseSpaces(t *testing.T) {
	for in, want := range map[string]string{
		"":               "",
		" ":              " ",
		"a b":            "a b",
		"a  b":           "a▁b",
		"a   b":          "a▁b",
		"  a":            "▁a",
		"a  ":            "a▁",
		"a b  c   d e":   "a b▁c▁d e",
		"a▁▁b":           "a▁▁b",   // 字面的 ▁ 不动
		"a\t\tb":         "a\t\tb", // 只合并 ASCII 空格
		"a\u3000\u3000b": "a\u3000\u3000b",
	} {
		if got := collapseSpaces(in); got != want {
			t.Errorf("collapseSpaces(%q) = %q，该是 %q", in, got, want)
		}
	}
}

func TestStripSpace(t *testing.T) {
	for _, c := range []struct {
		in          string
		left, right bool
		want        string
	}{
		{"  a b \t\n", false, true, "  a b"},
		{"  a b \t\n", true, false, "a b \t\n"},
		{"  a b \t\n", true, true, "a b"},
		{"a\u3000\u00a0\u2003", false, true, "a"}, // Unicode 的空白（Rust 的 is_whitespace）都算
		{"a\u200b", false, true, "a\u200b"},       // 零宽空格不是空白
		{"a\u001f", false, true, "a\u001f"},       // 控制字符 U+001F 不是空白（Python 的 str.strip 算，Rust 不算）
		{"", false, true, ""},
		{"   ", false, true, ""},
	} {
		if got := stripSpace(c.in, c.left, c.right); got != c.want {
			t.Errorf("stripSpace(%q, %v, %v) = %q，该是 %q", c.in, c.left, c.right, got, c.want)
		}
	}
}

func TestParseNormalizer(t *testing.T) {
	const charsmap = `"precompiled_charsmap": "AAAAAA=="`
	pre := `{"type": "Precompiled", ` + charsmap + `}`
	strip := `{"type": "Strip", "strip_left": false, "strip_right": true}`
	repl := `{"type": "Replace", "pattern": {"Regex": " {2,}"}, "content": "▁"}`
	seq := func(elems ...string) string {
		s := `{"type": "Sequence", "normalizers": [`
		for i, e := range elems {
			if i > 0 {
				s += ","
			}
			s += e
		}
		return s + "]}"
	}
	ok := map[string]int{ // 值是 Strip 加 Replace 的步数
		`null`:                     0,
		pre:                        0,
		seq(pre):                   0,
		seq(pre, strip, repl):      2,
		seq(pre, repl, strip):      2, // 顺序按文件里写的
		seq(pre, strip):            1,
		seq(pre, strip, strip):     2,
		seq(pre, repl):             1,
		seq(pre, strip, repl, pre): -1, // 下面单独列：第二个 Precompiled 不在开头
	}
	for js, steps := range ok {
		n, err := parseNormalizer([]byte(js))
		if steps < 0 {
			if !errors.Is(err, ErrUnsupportedModel) {
				t.Errorf("%s：该报 ErrUnsupportedModel，得到 %v", js, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s：%v", js, err)
			continue
		}
		if len(n.steps) != steps {
			t.Errorf("%s：该有 %d 步，得到 %d", js, steps, len(n.steps))
		}
	}
	bad := map[string]error{
		`{"type": "Lowercase"}`:  ErrUnsupportedModel,
		seq(strip):               nil, // 没有 Precompiled 也行
		seq(`{"type": "Strip"}`): ErrUnsupportedModel,
		seq(pre, `{"type": "Replace", "pattern": {"Regex": " +"}, "content": "▁"}`):    ErrUnsupportedModel,
		seq(pre, `{"type": "Replace", "pattern": {"String": " "}, "content": "▁"}`):    ErrUnsupportedModel,
		seq(pre, `{"type": "Replace", "pattern": {"Regex": " {2,}"}, "content": "_"}`): ErrUnsupportedModel,
		seq(`{"type": "NFKC"}`):                                       ErrUnsupportedModel,
		`{"type": "Precompiled", "precompiled_charsmap": "!!!"}`:      ErrInvalidModel,
		`{"type": "Precompiled", "precompiled_charsmap": ""}`:         ErrInvalidModel,
		`{"type": "Precompiled", "precompiled_charsmap": "AAAA"}`:     ErrInvalidModel, // 3 个字节，不够 4
		`{"type": "Precompiled", "precompiled_charsmap": "/////w=="}`: ErrInvalidModel, // 字典树大小超出
		`{`: ErrInvalidModel,
	}
	for js, want := range bad {
		_, err := parseNormalizer([]byte(js))
		if want == nil {
			if err != nil {
				t.Errorf("%s：该成功，得到 %v", js, err)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("%s：该报 %v，得到 %v", js, want, err)
		}
	}
}
