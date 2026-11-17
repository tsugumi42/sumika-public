package linkexpand

// 性质测试：按上限切节点、切长文。上限写成参数，不写死 splitText 现在的 2000，
// fix/link-length-translate 改成字节上限后并过来仍成立。

import (
	"strings"
	"testing"
	"unicode/utf8"

	"sumika-native/internal/linkreader"
)

// FuzzNodeHead：返回值是原文的前缀；不超过 max 个字，唯一的例外是开头一条自己就超长的链接；
// 合法 UTF-8 不切坏。上限按字还是按字节算都成立：字节数不少于字数，宽字符只会让“没超上限”的判断更保守。
func FuzzNodeHead(f *testing.F) {
	seeds := []string{
		"",
		"短句。",
		strings.Repeat("中英日 mixed 😀 text，", 200),
		"https://example.com/" + strings.Repeat("a", 3000) + " 后面还有字",
		"第一段\n\n第二段\n第三段。最后一句！",
		"@某个名字 #话题 " + strings.Repeat("尾巴", 100),
		"控制字符\x00\x01和零宽\u200b\u200d",
	}
	for _, s := range seeds {
		for _, max := range []int{1, 5, 10, 100, 2000} {
			f.Add(s, max)
		}
	}
	f.Fuzz(func(t *testing.T, rest string, max int) {
		if max < 1 {
			t.Skip()
		}
		head := nodeHead(rest, max)
		if !strings.HasPrefix(rest, head) {
			t.Fatalf("切出来的不是原文前缀：\n%q\n%q", rest, head)
		}
		if utf8.ValidString(rest) && !utf8.ValidString(head) {
			t.Fatalf("合法 UTF-8 被切坏：\n%q\n%q", rest, head)
		}
		if len(rest) <= max {
			if head != rest {
				t.Fatalf("字节数没超上限却切了：%d 字节，上限 %d\n%q\n%q", len(rest), max, rest, head)
			}
			return
		}
		if utf8.RuneCountInString(head) <= max {
			return
		}
		// 超上限只能是开头一条链接自己就超过上限：从上限处退到这条链接结尾（按字、按字节两种口径都在链接里）。
		end := linkreader.SafeSplit(rest, max)
		if end <= max || head != rest[:end] {
			t.Fatalf("超上限又不是开头的超长链接：上限 %d，切出 %d 个字\n%q\n%q（SafeSplit %d）", max, utf8.RuneCountInString(head), rest, head, end)
		}
	})
}

// FuzzSplitText：splitText 切完拼回来，除了切点两侧被去掉的换行，内容一字不少、顺序不变、不重复；
// 合法 UTF-8 的每段仍是合法 UTF-8。
func FuzzSplitText(f *testing.F) {
	seeds := []string{
		"",
		"\n\n\n",
		"短句。",
		strings.Repeat("很长的一段话😀 mixed日本語。\n", 150),
		strings.Repeat("https://example.com/"+strings.Repeat("a", 100)+" ", 50),
		strings.Repeat("第一段。\n\n第二段！", 400),
		"控制字符\x00\x01和零宽\u200b\u200d",
	}
	for _, s := range seeds {
		for _, max := range []int{4, 100, 2000, 16500} {
			f.Add(s, max)
		}
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 4 { // 上限至少要放得下一个字符（UTF-8 最长 4 字节）；配置下限是 1000
			t.Skip()
		}
		parts := splitText(s, max)
		noNL := func(x string) string { return strings.ReplaceAll(x, "\n", "") }
		if got, want := noNL(strings.Join(parts, "\n")), noNL(s); got != want {
			t.Fatalf("拼回来少字 / 乱序 / 重复：\n原文（去换行）%q\n拼回（去换行）%q\n段 %q", want, got, parts)
		}
		if utf8.ValidString(s) {
			for i, part := range parts {
				if !utf8.ValidString(part) {
					t.Fatalf("第 %d 段不是合法 UTF-8：%q（原文 %q）", i, part, s)
				}
				if part == "" {
					t.Fatalf("第 %d 段是空的：%q", i, s)
				}
			}
		}
	})
}
