package linkexpand

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 合并转发里每个节点的文字最多 maxBytes 个 UTF-8 字节（ADR-391）：切点落在链接中间时，整条链接挪到下一个节点。
func TestToNodesKeepsLinkInOneNode(t *testing.T) {
	const maxBytes = 300
	link := "https://example.com/Claude-Fable-5.2-HTML-Files/006-" + strings.Repeat("x", 40) + ".html"
	for _, before := range []int{95, 97, 99} { // 每个汉字 3 字节
		text := strings.Repeat("字", before) + link + strings.Repeat("尾", 30)
		nodes := toNodes([]Item{{Kind: types.BodyText, Text: text}}, maxBytes)
		if len(nodes) != 2 {
			t.Fatalf("before=%d: 应分成 2 个节点，得到 %d", before, len(nodes))
		}
		holders := 0
		var all strings.Builder
		for _, n := range nodes {
			s := n.Body[0].Text
			all.WriteString(s)
			if strings.Contains(s, link) {
				holders++
			} else if strings.Contains(s, "https://") || strings.Contains(s, ".html") {
				t.Fatalf("before=%d: 链接被切开: %q", before, s)
			}
			if n := len(s); n > maxBytes {
				t.Fatalf("before=%d: 节点超过 %d 字节: %d", before, maxBytes, n)
			}
		}
		if holders != 1 || all.String() != text {
			t.Fatalf("before=%d: 链接要整条在一个节点里且内容不丢: holders=%d", before, holders)
		}
	}
	// 没有链接时按字节硬切，多字节字符不切坏
	nodes := toNodes([]Item{{Kind: types.BodyText, Text: strings.Repeat("字", 1000)}}, maxBytes)
	if len(nodes) != 10 || len(nodes[0].Body[0].Text) != maxBytes {
		t.Fatalf("无链接按 %d 字节切: %d 个节点", maxBytes, len(nodes))
	}
	for i, n := range nodes {
		if s := n.Body[0].Text; !utf8.ValidString(s) || len(s) > maxBytes {
			t.Fatalf("节点 %d 切坏了: %q", i, s)
		}
	}
}

// 截图里的形状：多段“标题 + 长链接”的长文，原先按固定字数硬切，上一个节点结尾是半条链接。
// 现在优先在空行切，节点里的每条链接都完整，且 @用户名、#话题 也不被切开。
func TestToNodesLongArticleSplitsAtBlankLines(t *testing.T) {
	const maxBytes = 1200
	var b strings.Builder
	var links []string
	for i := 1; i <= 60; i++ {
		link := fmt.Sprintf("https://example-lab.github.io/Fable-Files/%03d-jelly-alphabet-%s.html", i, strings.Repeat("k", 20))
		links = append(links, link)
		fmt.Fprintf(&b, "%03d 编造的标题 @某用户 #某话题 说明文字说明文字\n%s\n\n", i, link)
	}
	text := strings.TrimSpace(b.String())
	nodes := toNodes([]Item{{Kind: types.BodyText, Text: text}}, maxBytes)
	if len(nodes) < 2 {
		t.Fatalf("应切成多个节点，得到 %d", len(nodes))
	}
	var joined []string
	for i, n := range nodes {
		s := n.Body[0].Text
		joined = append(joined, s)
		if len(s) > maxBytes {
			t.Fatalf("节点 %d 超过 %d 字节", i, maxBytes)
		}
		// 在空行或换行处切，切处的空行不留在节点末尾（X-15 第一个节点末尾留过两个空行）
		if i < len(nodes)-1 && (strings.HasSuffix(s, "\n") || !strings.Contains(text, s+"\n")) {
			t.Fatalf("节点 %d 应在空行或换行处切、末尾不留空行: %q", i, s[len(s)-30:])
		}
		if strings.HasSuffix(strings.TrimSpace(s), "-") || strings.HasSuffix(strings.TrimSpace(s), "Files") {
			t.Fatalf("节点 %d 结尾像半条链接: %q", i, s[len(s)-30:])
		}
	}
	all := strings.Join(joined, "")
	for _, l := range links {
		if strings.Count(all, l) != 1 {
			t.Fatalf("链接不完整或丢了: %s", l)
		}
		in := 0
		for _, s := range joined {
			if strings.Contains(s, l) {
				in++
			}
		}
		if in != 1 {
			t.Fatalf("链接要整条在一个节点里: %s", l)
		}
	}
	if strings.ReplaceAll(strings.ReplaceAll(all, "\n", ""), " ", "") != strings.ReplaceAll(strings.ReplaceAll(text, "\n", ""), " ", "") {
		t.Fatal("切分后内容不能丢")
	}
}

// 没有换行、没有标点的一整段里，@用户名 和超长链接也不被切开。
func TestToNodesProtectsMentionAndHugeLink(t *testing.T) {
	const maxBytes = 300
	text := strings.Repeat("字", 665) + "@abcdefghijk" + strings.Repeat("字", 34) // 1995 字节 + @用户名 + 102 字节
	nodes := toNodes([]Item{{Kind: types.BodyText, Text: text}}, maxBytes)
	for _, n := range nodes {
		s := n.Body[0].Text
		if strings.HasSuffix(s, "@abc") || strings.HasPrefix(s, "defghijk") {
			t.Fatalf("@用户名被切开: %q", s[:12])
		}
	}
	huge := "https://example.com/" + strings.Repeat("p", 400)
	nodes = toNodes([]Item{{Kind: types.BodyText, Text: huge + "尾巴"}}, maxBytes)
	if !strings.Contains(nodes[0].Body[0].Text, huge) {
		t.Fatal("超长链接应整条放进一个节点")
	}
}

// 长段没有换行时，切节点不能每个候选切点都从头扫链接：30 KB 英文、2 万汉字单段各只该整理一次链接区间、
// 候选切点数与文字长度同阶；旧代码每轮调 SafeSplit 扫整段剩余文字（30 KB 要几十秒），这里不靠墙钟。
func TestNodeSplitLongParagraphFast(t *testing.T) {
	cases := []struct{ name, text string }{
		{"30KB 英文单段", strings.Repeat("The quick brown fox jumps over the lazy dog. ", 683)[:30*1024]},
		{"2 万汉字单段", strings.Repeat("纸船在河面上慢慢漂。", 2000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cost linkCost
			nodes := splitTextCost(c.text, linktemplate.DefaultForwardOverBytes, &cost)
			if len(nodes) < 2 {
				t.Fatalf("长段应拆成多个节点，得到 %d", len(nodes))
			}
			// 每个超过上限的节点只整理一次链接区间（最后一段不超上限，不扫）
			if cost.scans != len(nodes)-1 {
				t.Fatalf("每个超上限的节点只该扫一次链接区间：%d 个节点扫了 %d 次", len(nodes), cost.scans)
			}
			// 每次只扫这个节点剩下的部分，总量不会超过原文的几倍
			if cost.scanBytes > 3*len(c.text) {
				t.Fatalf("链接区间扫了 %d 字节，原文 %d：不该每个候选从头扫", cost.scanBytes, len(c.text))
			}
			// 候选切点只在上限到一半之间试，和文字长度同阶
			if cost.candidates > 2*len(c.text)+1024 {
				t.Fatalf("候选切点检查了 %d 次，原文 %d 字节：不该随剩余文字反复扫", cost.candidates, len(c.text))
			}

			r := xImages(0)
			r.Text = av(c.text)
			r.Blocks = []linkreader.Block{{Type: "text", Text: c.text}}
			d := build(r, Options{})
			if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward || len(d.Parts[0].Nodes) < 2 {
				t.Fatalf("长段应拆进合并转发的多个节点：%v", kinds(d))
			}
		})
	}
}
