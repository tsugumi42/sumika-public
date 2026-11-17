package wiki

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	xhtml "golang.org/x/net/html"
)

var htmlUnescape = html.UnescapeString

// 词条 HTML 里要整块丢掉的 class。信息框、导航框、目录、参考文献、编辑链接、不打印的东西、
// 萌百的提示框和嵌入的视频播放器。按“整个 class 词”或前缀匹配。
var (
	dropClassExact = map[string]bool{
		"toc": true, "toctitle": true, "mw-editsection": true, "mw-editsection-bracket": true,
		"reference": true, "references": true, "reflist": true, "mw-references-wrap": true, "mw-cite-backlink": true,
		"noprint": true, "mobileonly": true, "metadata": true, "mw-empty-elt": true, "catlinks": true,
		"hatnote": false, "navbar": true, "sistersitebox": true, "template-ruby-hidden": true,
		"ambox": true, "tmbox": true, "ombox": true, "mw-authority-control": true, "printfooter": true,
		"thumb": true, "gallery": true, "mw-jump-link": true, "mw-collapsible-toggle": true,
	}
	// infobox、infoBox（萌百的提示框）、navbox、vertical-navbox、bilibili-*（视频播放器）
	dropClassPrefix = []string{"infobox", "navbox", "vertical-navbox", "bilibili", "mw-ext-", "ext-"}
)

func shouldDrop(n *xhtml.Node) bool {
	if n.Type != xhtml.ElementNode {
		return false
	}
	switch n.Data {
	case "script", "style", "template", "iframe", "object", "embed", "noscript", "img", "figure", "audio", "video", "svg", "link", "meta", "input", "button":
		return true
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if a.Val == "true" {
				return true
			}
		case "style":
			v := strings.ReplaceAll(strings.ToLower(a.Val), " ", "")
			if strings.Contains(v, "display:none") || strings.Contains(v, "visibility:hidden") {
				return true
			}
		case "id":
			if a.Val == "toc" || a.Val == "catlinks" || a.Val == "mw-navigation" {
				return true
			}
		case "class":
			for _, c := range strings.Fields(a.Val) {
				c = strings.ToLower(c)
				if dropClassExact[c] {
					return true
				}
				for _, p := range dropClassPrefix {
					if strings.HasPrefix(c, p) {
						return true
					}
				}
			}
		}
	}
	return false
}

// prune 删掉要丢的子树，并把链接拆成纯文字（链接地址对模型没用，还费 token）。
func prune(n *xhtml.Node, depth int) error {
	if depth > 512 {
		return fmt.Errorf("词条层级过深")
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case shouldDrop(c):
			n.RemoveChild(c)
		case c.Type == xhtml.ElementNode && c.Data == "a":
			if err := prune(c, depth+1); err != nil {
				return err
			}
			// 把 a 的子节点提到它的位置
			for k := c.FirstChild; k != nil; {
				kn := k.NextSibling
				c.RemoveChild(k)
				n.InsertBefore(k, c)
				k = kn
			}
			n.RemoveChild(c)
		default:
			if err := prune(c, depth+1); err != nil {
				return err
			}
		}
		c = next
	}
	return nil
}

var (
	blankRunRE   = regexp.MustCompile(`\n{3,}`)
	trailingWSRE = regexp.MustCompile(`[ \t]+\n`)
)

// ToMarkdown 把 MediaWiki 的 parse 输出（HTML）清理后转成 Markdown：去信息框、导航框、目录、参考文献、
// 编辑链接、隐藏元素、图片，链接拆成文字。
// emptyTableHeadRE 是一行全空的表头加它下面的分隔线。
var emptyTableHeadRE = regexp.MustCompile(`(?m)^\|(?:[ \t]*\|)+[ \t]*\r?\n\|(?:[ \t]*:?-+:?[ \t]*\|)+[ \t]*\r?\n`)

func ToMarkdown(htmlText, base string) (string, error) {
	doc, err := xhtml.Parse(strings.NewReader(htmlText))
	if err != nil {
		return "", err
	}
	if err := prune(doc, 0); err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := xhtml.Render(&b, doc); err != nil {
		return "", err
	}
	conv := htmltomarkdown.NewConverter("", true, nil)
	conv.Use(plugin.Table())
	md, err := conv.ConvertString(b.String())
	if err != nil {
		return "", err
	}
	// 没有 <th> 的表（萌百拿表格排版的引言、两栏信息表）会被补一行空表头，读起来像一张空表：连同分隔线一起删
	md = emptyTableHeadRE.ReplaceAllString(md, "")
	md = trailingWSRE.ReplaceAllString(md, "\n")
	md = blankRunRE.ReplaceAllString(md, "\n\n")
	return strings.TrimSpace(md), nil
}

// Truncate 按段落边界截到约 max 个字；返回正文和被截掉的字数。
func Truncate(md string, max int) (string, int) {
	total := utf8.RuneCountInString(md)
	if total <= max {
		return md, 0
	}
	rs := []rune(md)
	cut := max
	// 尽量在最后 20% 里找一个空行断开，不拦腰截断段落
	floor := max - max/5
	for i := max; i > floor; i-- {
		if rs[i-1] == '\n' && i-2 >= 0 && rs[i-2] == '\n' {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(rs[:cut])), total - cut
}
