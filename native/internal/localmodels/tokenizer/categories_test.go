package tokenizer

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"
)

func TestCategories(t *testing.T) {
	cats, err := ParseCategories([]byte(`{
		"B": {"name": "B", "models": ["gte", "bge"], "why": "甲", "remove": {"ranges": [[97, 98]], "strings": []}},
		"A": {"name": "A", "models": ["gte"], "why": "乙", "remove": {"ranges": [], "strings": ["<x>"]}},
		"C": {"name": "C", "models": ["bge"], "why": "丙", "remove": {"ranges": [[122, 122]], "strings": []}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cats["B"].Strip("abcab"); got != "c" {
		t.Errorf("区间去字符：得到 %q", got)
	}
	if got := cats["A"].Strip("a<x>b<x>"); got != "ab" {
		t.Errorf("字符串去字符：得到 %q", got)
	}
	cases := []struct {
		name  string
		model string
		texts []string
		names []string
		rest  []string
	}{
		{"没有类别字符", "gte", []string{"cd"}, nil, []string{"cd"}},
		{"按名字排序，全部去掉", "gte", []string{"a<x>c"}, []string{"A", "B"}, []string{"c"}},
		{"只对生效的模型", "gte", []string{"zc"}, nil, []string{"zc"}},
		{"bge 看 C 和 B", "bge", []string{"zbc"}, []string{"B", "C"}, []string{"c"}},
		{"一对里只要一边含有就算", "bge", []string{"c", "zc"}, []string{"C"}, []string{"c", "c"}},
	}
	for _, c := range cases {
		names, rest := PresentCategories(cats, c.model, c.texts...)
		if !slices.Equal(names, c.names) || !slices.Equal(rest, c.rest) {
			t.Errorf("%s：得到 %v %q，想要 %v %q", c.name, names, rest, c.names, c.rest)
		}
	}
	if _, err := ParseCategories([]byte(`[`)); err == nil {
		t.Error("坏 JSON 没报错")
	}
}

// S1 的字面值也可能由 normalizer 造出来：全角 ＜s＞ 经 NFKC 变成 <s>，Python 版同样认成真的特殊符号。
// nfkc 的类别在 NFKC 之后的文本里找，找到了才算命中；没找到原样返回，不因为做了 NFKC 就算改动（不然每个含兼容字符的文本都像命中了 S1）。
func TestStripNFKC(t *testing.T) {
	lt, gt, fw := string(rune(0xff1c)), string(rune(0xff1e)), string(rune(0xff53)) // ＜ ＞ ｓ
	cat := Category{}
	cat.Remove.NFKC = true
	cat.Remove.Strings = []string{"<s>", "</s>"}
	for _, c := range []struct{ name, in, want string }{
		{"半角照旧", "a<s>b</s>c", "abc"},
		{"全角的括号", lt + "s" + gt, ""},
		{"全角的括号和斜杠夹在话里", "n" + lt + "/s" + gt + " orz", "n orz"},
		{"全角的字母", lt + fw + gt, ""},
		{"半角全角混着写", "<s" + gt + "x" + lt + "/s>", "x"},
		{"去掉一个又拼出新的", "<<s>s>", ""},
		{"全角的去掉一个又拼出新的", lt + lt + "s" + gt + "s" + gt, ""},
		{"没有字面值：原样返回，不做 NFKC", "ｶﾞ ABC " + lt, "ｶﾞ ABC " + lt},
		{"字面值不全：原样返回", "<s " + lt + "/s", "<s " + lt + "/s"},
	} {
		if got := cat.Strip(c.in); got != c.want {
			t.Errorf("%s：Strip(%q) = %q，想要 %q", c.name, c.in, got, c.want)
		}
	}
	// 不带 nfkc 的类别（编的）照旧只认原样的字符串，也去到不再出现为止
	plain := Category{}
	plain.Remove.Strings = []string{"<s>"}
	if got := plain.Strip(lt + "s" + gt + "<<s>s>"); got != lt+"s"+gt {
		t.Errorf("不带 nfkc 不该认全角：%q", got)
	}
}

// 真实的类别表里：S1 带 nfkc，PresentCategories 对全角字面值报 S1，对只含兼容字符的普通话不报
func TestShippedS1CatchesFullwidthLiterals(t *testing.T) {
	var cats map[string]Category
	readJSONFile(t, "categories.json", &cats)
	if !cats["S1"].Remove.NFKC {
		t.Fatal("S1 该在 NFKC 之后的文本里找字面值")
	}
	lt, gt := string(rune(0xff1c)), string(rune(0xff1e))
	names, rest := PresentCategories(cats, "gte", "文档里写着 "+lt+"/s"+gt+" 和 "+lt+"pad"+gt)
	if !slices.Equal(names, []string{"S1"}) || rest[0] != "文档里写着  和 " {
		t.Errorf("全角字面值该归 S1 并去掉：%v %q", names, rest)
	}
	if names, _ := PresentCategories(cats, "gte", "ｶﾞ "+lt+"s"+" 普通的话 ＜"); slices.Contains(names, "S1") {
		t.Errorf("没有完整字面值不该归 S1：%v", names)
	}
}

// T5 是 Grapheme_Extend（= Mn ∪ Me ∪ Other_Grapheme_Extend），不是手工拼的范围：原来只有 Mn、Me 加三个字符，
// 漏了 Mc 里的 Other_Grapheme_Extend（印度系两段式元音符号的第二个字符，U+09BE 等），这类差异归不到任何类别。
// 区间对着 Go 的 unicode 表核：表里的 Grapheme_Extend 一个不许缺；多出来的只许是 categories.json 的 why 里写明的
// （Unicode 15 时是 Mn、16 起改成 Mc 的 U+1171E，靠旧表留着）。Go 升级 Unicode 表后这条会红：重新生成区间（README 里有生成的办法）
func TestT5IsGraphemeExtend(t *testing.T) {
	var cats map[string]Category
	readJSONFile(t, "categories.json", &cats)
	t5, ok := cats["T5"]
	if !ok {
		t.Fatal("categories.json 里没有 T5")
	}
	in := func(r rune) bool {
		for _, span := range t5.Remove.Ranges {
			if r >= span[0] && r <= span[1] {
				return true
			}
		}
		return false
	}
	for i, span := range t5.Remove.Ranges {
		if span[0] > span[1] || (i > 0 && span[0] <= t5.Remove.Ranges[i-1][1]+1) {
			t.Fatalf("T5 的区间要升序、不相接不重叠：第 %d 个 %v", i, span)
		}
	}
	extra := map[rune]string{0x1171E: "Unicode 15 的 Mn，16 起改成 Mc"}
	var missing, surplus []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		ge := unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Other_Grapheme_Extend, r)
		switch {
		case ge && !in(r):
			missing = append(missing, r)
		case !ge && in(r) && extra[r] == "":
			surplus = append(surplus, r)
		}
	}
	if len(missing) > 0 || len(surplus) > 0 {
		t.Errorf("T5 和 Go 的 Grapheme_Extend（Unicode %s）对不上：缺 %d 个 %s，多 %d 个 %s", unicode.Version, len(missing), hexList(missing), len(surplus), hexList(surplus))
	}
	// 当时漏掉的那几个，点名钉住：Mc 里的 Other_Grapheme_Extend
	for _, r := range []rune{0x09BE, 0x09D7, 0x0B3E, 0x0B57, 0x0BBE, 0x0BD7, 0x0D3E, 0x0D57, 0x0DCF, 0x0DDF, 0x200C, 0xFF9E, 0xFF9F, 0xE0020} {
		if !in(r) {
			t.Errorf("U+%04X 该在 T5 里", r)
		}
	}
}

func hexList(rs []rune) string {
	var sb strings.Builder
	for i, r := range rs {
		if i == 8 {
			sb.WriteString(" …")
			break
		}
		fmt.Fprintf(&sb, " U+%04X", r)
	}
	return strings.TrimSpace(sb.String())
}
