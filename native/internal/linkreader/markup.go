package linkreader

// 各站正文里的原始标记换成纯文字：Steam 的 BBCode、Misskey 的 MFM、简介里的 Markdown 粗体和链接。
// 群里看到的是纯文字，标记原样发出去就是一堆方括号和星号。每个函数都满足“清两遍 = 清一遍”，循环有固定上限。

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/idna"
)

// normalizeNewlines 把 \r\n 和单独的 \r 都换成 \n（Steam 等接口的简介带 Windows 换行）。
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

var (
	// [img]地址[/img]、[previewyoutube=…][/previewyoutube] 这类整段只是嵌入物的，连内容一起去掉
	bbEmbed = regexp.MustCompile(`(?is)\[(img|previewyoutube|previewicon|video)(=[^\]]*)?\].*?\[/(img|previewyoutube|previewicon|video)\]`)
	// [url=地址]文字[/url] → 文字 地址；文字就是地址时只留一份
	bbLinkNamed = regexp.MustCompile(`(?is)\[url=([^\]]+)\](.*?)\[/url\]`)
	bbLinkBare  = regexp.MustCompile(`(?is)\[url\](.*?)\[/url\]`)
	// 列表项写成“· ”
	bbItem = regexp.MustCompile(`(?i)\[\*\]\s*`)
	// 标题、段落、引用、表格行的收尾换成换行，再去掉其余标签；收尾后面原文本来就有换行时一起吃掉，不多出一个空行
	bbBreak = regexp.MustCompile(`(?i)(?:\[/(h[1-6]|p|quote|tr|list|olist|hr)\]|\[(hr|br)\])\n?`)
	bbTag   = regexp.MustCompile(`(?i)\[/?(h[1-6]|b|i|u|s|strike|spoiler|noparse|code|quote|p|list|olist|table|tr|th|td|hr|br|url|img|previewyoutube|video|emoticon|expand)(=[^\]]*)?\]`)
	// 正文被截在 [img] 这类嵌入标签之后：末尾孤零零的开标签和它后面的半截地址一起去掉
	bbTailEmbed = regexp.MustCompile(`(?is)\[(img|previewyoutube|video)(=[^\]]*)?\][^\[]*$`)
	blankRun    = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)
	trailSpace  = regexp.MustCompile(`[ \t]+\n`)
)

// bbcodePlain 把 Steam 创意工坊、公告用的 BBCode 换成纯文字：图片和视频嵌入去掉，链接留文字和地址，列表写成“· ”。
func bbcodePlain(s string) string {
	s = normalizeNewlines(s)
	s = bbEmbed.ReplaceAllString(s, "")
	s = bbTailEmbed.ReplaceAllString(s, "")
	s = bbLinkNamed.ReplaceAllStringFunc(s, func(m string) string {
		p := bbLinkNamed.FindStringSubmatch(m)
		return linkWithLabel(strings.TrimSpace(p[2]), strings.TrimSpace(p[1]))
	})
	s = bbLinkBare.ReplaceAllString(s, "$1")
	s = bbItem.ReplaceAllString(s, "\n· ")
	s = bbBreak.ReplaceAllString(s, "\n")
	s = bbTag.ReplaceAllString(s, "")
	return tidyBlank(s)
}

// tidyBlank 去掉行尾空白，三个及以上的连续换行压成一个空行（保留分段）。
func tidyBlank(s string) string {
	s = trailSpace.ReplaceAllString(s, "\n")
	s = blankRun.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

var (
	// MFM 函数 $[名字.参数 内容]：只留内容。从最里层往外剥，嵌套层数有上限
	mfmFunc = regexp.MustCompile(`\$\[[A-Za-z0-9_]+(?:\.[^\s\]]*)?\s([^\[\]]*)\]`)
	// MFM 的 <center> <small> <plain> <i> <b> <s> 标签
	mfmTag = regexp.MustCompile(`(?i)</?(center|small|plain|i|b|s)>`)
	// Markdown 链接 [文字](地址)，Misskey 的静默链接 ?[文字](地址) 一样处理
	mdLink = regexp.MustCompile(`\??\[([^\[\]\n]+)\]\((https?://[^\s()]+)\)`)
	// Markdown 粗体、删除线：**文字**、~~文字~~（__文字__ 不动：容易误伤 __init__ 这类代码名）
	mdBold   = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	mdStrike = regexp.MustCompile(`~~([^~\n]+?)~~`)
)

// mfmMaxDepth 是 MFM 函数最多剥几层；正常帖子不超过三四层，再多的留原样，不无限循环。
const mfmMaxDepth = 8

// misskeyPlain 把 Misskey 的 MFM 换成纯文字：函数只留内容、去掉排版标签，粗体、链接照 Markdown 处理。
func misskeyPlain(s string) string {
	for range mfmMaxDepth {
		next := mfmFunc.ReplaceAllString(s, "$1")
		if next == s {
			break
		}
		s = next
	}
	s = mfmTag.ReplaceAllString(s, "")
	return markdownPlain(s)
}

// markdownPlain 去掉 Markdown 粗体、删除线，链接写成“文字 地址”（文字就是地址时只留一份）。
func markdownPlain(s string) string {
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		return linkWithLabel(p[1], p[2])
	})
	s = mdBold.ReplaceAllString(s, "$1")
	s = mdStrike.ReplaceAllString(s, "$1")
	return s
}

// linkWithLabel 是“文字 地址”：地址里的 punycode 域名（xn-- 开头）换回原来的字，文字和地址一样时只留地址。
func linkWithLabel(label, raw string) string {
	shown := displayURL(raw)
	if label == "" || label == raw || label == shown {
		return shown
	}
	return label + " " + shown
}

// displayURL 把链接里 punycode 的域名换成原来的字；换不了的原样返回。
func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.Contains(u.Host, "xn--") {
		return raw
	}
	host, err := idna.Display.ToUnicode(u.Hostname())
	if err != nil {
		return raw
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return strings.Replace(raw, u.Host, host, 1)
}
