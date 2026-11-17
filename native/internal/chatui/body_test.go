package chatui

import (
	"strings"
	"testing"
)

func bodySource(s string, streaming bool) string { return hardBreaks(stripThink(s, streaming)) }

func TestStripThink(t *testing.T) {
	for _, c := range []struct {
		in        string
		streaming bool
		want      string
	}{
		{"<think>想想</think>\n好的。", false, "好的。"},
		{"先说一句。<think>想想</think>再说一句。", false, "先说一句。再说一句。"},
		{"<think>还在想", true, ""},
		{"开头<think>还在想", true, "开头"},
		{"开头<thi", true, "开头"},
		{"开头<", true, "开头"},
		{"a < b", true, "a < b"},
		{"a <", false, "a <"},
		{"<think>一</think><think>二</think>三", false, "三"},
		{"没有思考", true, "没有思考"},
	} {
		if got := stripThink(c.in, c.streaming); got != c.want {
			t.Errorf("stripThink(%q, %v) = %q，期望 %q", c.in, c.streaming, got, c.want)
		}
	}
}

func TestHardBreaks(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"第一行\n第二行", "第一行  \n第二行"},
		{"一段\n\n二段", "一段  \n\n二段"},
		{"```go\na := 1\nb := 2\n```\n后面\n还有", "```go\na := 1\nb := 2\n```\n后面  \n还有"},
		{"~~~\nx\n~~~~\ny\nz", "~~~\nx\n~~~~\ny  \nz"},
		{"    缩进代码\n下一行", "    缩进代码\n下一行"},
		{"已经有两个空格  \n下一行", "已经有两个空格  \n下一行"},
		{"还没换行", "还没换行"},
	} {
		if got := hardBreaks(c.in); got != c.want {
			t.Errorf("hardBreaks(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 前缀稳定：逐字往后送，每一步显示的文字都是下一步（和最后完成时）的前缀——否则 mygo 会把它当成改写，整段一下子出完。
func TestBodySourcePrefixStable(t *testing.T) {
	for _, full := range []string{
		"<think>先想一下\n她说了什么</think>\n好呀，\n我们接着聊。\n\n```go\nx := 1\n```\n完了 < 吗",
		"第一行\n第二行<think>中间想想</think>\n第三行\n- 列表\n- 第二项",
		"a <b>粗</b> 和 <thinking> 不是思考段\n下一行",
	} {
		runes := []rune(full)
		prev := ""
		final := bodySource(full, false)
		for k := 1; k <= len(runes); k++ {
			cur := bodySource(string(runes[:k]), true)
			if !strings.HasPrefix(cur, prev) {
				t.Fatalf("送到第 %d 个字时显示 %q，不是上一步 %q 的延续", k, cur, prev)
			}
			if !strings.HasPrefix(final, cur) {
				t.Fatalf("送到第 %d 个字时显示 %q，不是完成后 %q 的前缀", k, cur, final)
			}
			prev = cur
		}
	}
}
