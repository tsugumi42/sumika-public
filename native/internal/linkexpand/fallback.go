package linkexpand

import (
	"strings"

	"sumika-native/internal/channel/types"
)

// TextFallback 把一段里的文字和媒体的说明文字拼成一条不带媒体的文字版（ADR-310）。
func TextFallback(p types.Part, quote, link string) (types.Part, bool) {
	return TextFallbacks([]types.Part{p}, nil, quote, link)
}

// TextFallbacks 把同一计划里要补发的几段合成一条不带媒体的文字版：媒体本身不带，说明文字（卡片语义）保留，
// 行去重；链接只补一次，已经出现在这些段或 known（同计划里已确认的段）的正文里就不再补。
// quote 为空则不引用。没有任何字时 ok 为假。notes 是说明没发出去的原因，放在内容之后、链接之前。
// 文字都来自 Prepare 已经过守卫的内容，这里只是它的子集，不另做守卫。
func TextFallbacks(parts, known []types.Part, quote, link string, notes ...string) (types.Part, bool) {
	var lines []string
	seen := map[string]bool{}
	add := func(body []types.BodySegment) {
		for _, s := range body {
			t := s.Text
			if s.Kind != types.BodyText {
				t = s.Semantic
			}
			if strings.TrimSpace(t) != "" && !seen[t] {
				seen[t] = true
				lines = append(lines, t)
			}
		}
	}
	for _, p := range parts {
		add(p.Body)
		for _, n := range p.Nodes {
			add(n.Body)
		}
	}
	for _, n := range notes {
		if strings.TrimSpace(n) != "" && !seen[n] {
			seen[n] = true
			lines = append(lines, n)
		}
	}
	have := strings.Join(lines, "\n")
	for _, p := range known {
		have += "\n" + plainText(p)
	}
	if link != "" && !strings.Contains(have, link) {
		lines = append(lines, link)
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		return types.Part{}, false
	}
	// 敏感许可照原段的标记带过去：文字版也要过发送前的敏感检查，不能因为去掉了媒体就放宽
	return types.Part{Kind: types.PartMessage, Quote: quote, Body: types.TextBody(text)}, true
}

// plainText 只取段里的文字（不含媒体的说明），用来判断链接是不是已经在文字里
func plainText(p types.Part) string {
	var b strings.Builder
	for _, s := range p.Body {
		if s.Kind == types.BodyText {
			b.WriteString(s.Text)
			b.WriteString("\n")
		}
	}
	for _, n := range p.Nodes {
		for _, s := range n.Body {
			if s.Kind == types.BodyText {
				b.WriteString(s.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}
