package linkexpand

import (
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// 合并转发里每个节点最多 2000 字：切点落在链接中间时，整条链接挪到下一个节点。
func TestToNodesKeepsLinkInOneNode(t *testing.T) {
	link := "https://example.com/Claude-Fable-5.2-HTML-Files/006-" + strings.Repeat("x", 40) + ".html"
	for _, before := range []int{1960, 1990, 1999, 2000 - len("https://exa")} {
		text := strings.Repeat("字", before) + link + strings.Repeat("尾", 30)
		nodes := toNodes([]Item{{Kind: types.BodyText, Text: text}})
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
			if n := len([]rune(s)); n > 2000 {
				t.Fatalf("before=%d: 节点超过 2000 字: %d", before, n)
			}
		}
		if holders != 1 || all.String() != text {
			t.Fatalf("before=%d: 链接要整条在一个节点里且内容不丢: holders=%d", before, holders)
		}
	}
	// 没有链接时仍按 2000 字切
	nodes := toNodes([]Item{{Kind: types.BodyText, Text: strings.Repeat("字", 4500)}})
	if len(nodes) != 3 || len([]rune(nodes[0].Body[0].Text)) != 2000 {
		t.Fatalf("无链接按 2000 字切: %d 个节点", len(nodes))
	}
}

// 截图里的形状：多段“标题 + 长链接”的长文，原先按 2000 字硬切，上一个节点结尾是半条链接。
// 现在优先在空行切，节点里的每条链接都完整，且 @用户名、#话题 也不被切开。
func TestToNodesLongArticleSplitsAtBlankLines(t *testing.T) {
	var b strings.Builder
	var links []string
	for i := 1; i <= 60; i++ {
		link := fmt.Sprintf("https://example-lab.github.io/Fable-Files/%03d-jelly-alphabet-%s.html", i, strings.Repeat("k", 20))
		links = append(links, link)
		fmt.Fprintf(&b, "%03d 编造的标题 @某用户 #某话题 说明文字说明文字\n%s\n\n", i, link)
	}
	text := strings.TrimSpace(b.String())
	nodes := toNodes([]Item{{Kind: types.BodyText, Text: text}})
	if len(nodes) < 2 {
		t.Fatalf("应切成多个节点，得到 %d", len(nodes))
	}
	var joined []string
	for i, n := range nodes {
		s := n.Body[0].Text
		joined = append(joined, s)
		if len([]rune(s)) > 2000 {
			t.Fatalf("节点 %d 超过 2000 字", i)
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
	text := strings.Repeat("字", 1995) + "@abcdefghijk" + strings.Repeat("字", 100)
	nodes := toNodes([]Item{{Kind: types.BodyText, Text: text}})
	for _, n := range nodes {
		s := n.Body[0].Text
		if strings.HasSuffix(s, "@abc") || strings.HasPrefix(s, "defghijk") {
			t.Fatalf("@用户名被切开: %q", s[:12])
		}
	}
	huge := "https://example.com/" + strings.Repeat("p", 2030)
	nodes = toNodes([]Item{{Kind: types.BodyText, Text: huge + "尾巴"}})
	if !strings.Contains(nodes[0].Body[0].Text, huge) {
		t.Fatal("超长链接应整条放进一个节点")
	}
}
