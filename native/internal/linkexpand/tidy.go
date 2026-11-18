package linkexpand

import (
	"strings"
	"unicode"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 正文清理（用户 2026-11-12）：作者正文里连续多个空行压成一个空行（保留分段），行尾空格、\r 去掉，首尾的空行去掉；
// 结尾的 hashtag 块各平台都去（开关同推文的 stripTrailingHashtags）。只动取数结果里的正文，不动渲染结果：
// 模板里写的换行（含用户自定义模板的空行）不经过这里。发出去的、autoForward 数字数的、送去翻译的都是 cleanResult 这一份。

// tidyText 清理一段作者正文：统一换行、去行尾空白、两个以上的空行压成一个、去首尾空行。清两遍 = 清一遍。
func tidyText(s string) string {
	if s == "" {
		return s
	}
	if blankText(s) {
		return "" // 看不见的字（零宽字符、韩文填充符、盲文空白）不算正文：否则会写出一对夹着空气的「」
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			if len(out) > 0 && !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, line)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// blankText 说这段文字看不见任何字：只有空白、格式字符（零宽空格、零宽连接符、方向标记、BOM），
// 或发帖的人拿来占位的填充符（韩文填充符 U+3164、U+115F、U+1160、盲文空白 U+2800）。
func blankText(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		switch {
		case unicode.IsSpace(r), unicode.Is(unicode.Cf, r):
			return false
		case r == 0x3164 || r == 0x115F || r == 0x1160 || r == 0x2800:
			return false
		}
		return true
	})
}

// cleanResult 返回清理过正文的副本（不改原结果）：Text、各个 text 块、被引帖子都清；没关开关时再去结尾 hashtag。
func cleanResult(r *linkreader.Result, o Options) *linkreader.Result {
	c := *r
	if c.Text.State == "available" {
		c.Text.Value = tidyText(c.Text.Value)
	}
	// 相邻的 text 块先按换行拼成一块再清（发出去时它们本来就按换行连在一起）：专栏、opus 一段一块，
	// 空段落是单独的空块，逐块清会把段落之间的空行全清掉；拼起来再压，段落之间正好留一个空行。
	c.Blocks = nil
	for _, b := range r.Blocks {
		if b.Type == "text" && len(c.Blocks) > 0 && c.Blocks[len(c.Blocks)-1].Type == "text" {
			last := &c.Blocks[len(c.Blocks)-1]
			last.Text += "\n" + b.Text
			continue
		}
		c.Blocks = append(c.Blocks, b)
	}
	for i, b := range c.Blocks {
		if b.Type == "text" {
			c.Blocks[i].Text = tidyText(b.Text)
		}
		if b.Type == "quote" && b.Quote != nil {
			c.Blocks[i].Quote = cleanResult(b.Quote, o)
		}
	}
	if c.Quote.State == "available" && c.Quote.Value != nil {
		c.Quote.Value = cleanResult(c.Quote.Value, o)
	}
	dropSharePlaceholder(&c)
	if !o.KeepTrailingHashtags {
		return stripTweetHashtags(&c)
	}
	return &c
}

// sharePlaceholder 是 B 站纯图动态没写字时接口填的正文。
const sharePlaceholder = "分享图片"

// dropSharePlaceholder：B 站动态有图时，正文和 text 块里只有“分享图片”的那份去掉（#18）。
// 直发的 $正文文字、转发的 $正文、送去翻译的都读这一份，所以在清理这里统一去；没有图时它就是作者写的正文，留着。
func dropSharePlaceholder(c *linkreader.Result) {
	if linktemplate.SiteKey(c.Platform, c.Kind) != "bilibili-dynamic" || !hasImage(c) {
		return
	}
	if strings.TrimSpace(c.Text.Value) == sharePlaceholder {
		c.Text.Value = ""
	}
	kept := c.Blocks[:0]
	for _, b := range c.Blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) == sharePlaceholder {
			continue
		}
		kept = append(kept, b)
	}
	c.Blocks = kept
}

func hasImage(r *linkreader.Result) bool {
	for _, b := range r.Blocks {
		if (b.Type == "image" || b.Type == "photo") && b.Media != nil {
			return true
		}
	}
	if len(r.Blocks) == 0 {
		for _, m := range r.Media.Value {
			if m.Type == "image" || m.Type == "photo" {
				return true
			}
		}
	}
	return false
}
