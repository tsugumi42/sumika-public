package linkexpand

import (
	"regexp"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// 正文为空的帖子（只有图片、视频，或正文只剩空白）：文字里不出现空的「」、不留空行（用户 2026-11-13，复核 #32，通则）。
// “名字[@ID]：”后面直接接时间；通则覆盖 X、被引推文、转发里的节点、Bluesky / Misskey / Telegram 帖子。

var (
	emptyQuoteRe = regexp.MustCompile(`「\s*」|「\s*$|^\s*」`)
	quoteInnerRe = regexp.MustCompile(`「([^」]*)」`)
)

// noBlankOrEmptyQuote 查一段发出去的文字：没有空的「」、没有空行、没有以“「”结尾的行。
func noBlankOrEmptyQuote(t *testing.T, label, s string) {
	t.Helper()
	if emptyQuoteRe.MatchString(s) || strings.Contains(s, "「」") {
		t.Errorf("%s：有空的「」：%q", label, s)
	}
	for _, m := range quoteInnerRe.FindAllStringSubmatch(s, -1) {
		if blankText(m[1]) {
			t.Errorf("%s：「」里看不见字：%q", label, s)
		}
	}
	for i, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("%s：第 %d 行是空行：%q", label, i+1, s)
			return
		}
	}
}

// emptyBodies 是“正文为空”的几种写法：没写、只有空白、只有零宽字符。
var emptyBodies = map[string]string{"没有正文": "", "只有空白": " \n\t ", "零宽字符": "\u200b\u200b", "填充符": "\u3164\u3164", "盲文空白": "\u2800 \u2800"}

func withBody(r linkreader.Result, body string) linkreader.Result {
	r.Text = av(body)
	var blocks []linkreader.Block
	if body != "" {
		blocks = append(blocks, linkreader.Block{Type: "text", Text: body})
	}
	for _, b := range r.Blocks {
		if b.Type != "text" {
			blocks = append(blocks, b)
		}
	}
	r.Blocks = blocks
	return r
}

// 所有出口的文字都过一遍：直发的每个文字项、转发的每个节点的每个文字项。
func allTexts(d Draft) []string {
	var out []string
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == "text" {
				out = append(out, it.Text)
			}
		}
		for _, n := range p.Nodes {
			for _, it := range n.Body {
				if it.Kind == "text" {
					out = append(out, it.Text)
				}
			}
		}
	}
	return out
}

func TestEmptyBodyTweetHasNoEmptyQuoteOrBlankLine(t *testing.T) {
	for name, body := range emptyBodies {
		for _, trans := range []string{"", "不会有的译文"} {
			r := withBody(xImages(2), body)
			d := build(r, Options{Translation: trans})
			texts := allTexts(d)
			if len(texts) == 0 {
				t.Fatalf("%s：没有文字", name)
			}
			for _, s := range texts {
				noBlankOrEmptyQuote(t, name, s)
				if !strings.Contains(s, "纸云[@paper_cloud]\n11-07 23:28") || strings.Contains(s, "]：") { // 没有正文时作者行也不挂冒号（用户 2026-11-13）
					t.Errorf("%s：作者行后面应直接是时间：%q", name, s)
				}
			}
		}
	}
}

func TestEmptyBodyQuotedTweetHasNoEmptyQuoteOrBlankLine(t *testing.T) {
	for name, body := range emptyBodies {
		q := goldenBase("x", "post", "90009")
		q.CleanURL = "https://x.com/i/status/90009"
		q.Author.Name, q.Author.Account = av("被引的人"), av("quoted_one")
		q = withMedia(withBody(q, body), goldenImage(9))
		main := xImages(1)
		main.Quote = av(&q)
		main.Blocks = append(main.Blocks, linkreader.Block{Type: "quote", Quote: &q})
		for _, s := range allTexts(build(main, Options{})) {
			noBlankOrEmptyQuote(t, "被引推文·"+name, s)
		}
		// 主推文和被引推文都没有正文
		both := withBody(main, body)
		both.Quote = av(&q)
		for _, s := range allTexts(build(both, Options{})) {
			noBlankOrEmptyQuote(t, "都没有正文·"+name, s)
		}
	}
}

func TestEmptyBodyPostSitesHaveNoEmptyQuoteOrBlankLine(t *testing.T) {
	for name, body := range emptyBodies {
		bs := withBody(blueskyImages(1), body)
		mk := siteResult("misskey", "note", "misskey.io/90050")
		mk.Author.Name, mk.Author.Account = av("纸云"), av("paper")
		mk = withBody(withMedia(mk, goldenImage(1)), body)
		tg := tgResult("", 1)
		tg = withBody(tg, body)
		for site, r := range map[string]linkreader.Result{"bluesky": bs, "misskey": mk, "telegram": tg} {
			d := build(r, Options{})
			for _, s := range allTexts(d) {
				noBlankOrEmptyQuote(t, site+"·"+name, s)
				if strings.Contains(s, "]：") { // 没有正文时作者行不挂冒号（用户 2026-11-13，#32）
					t.Errorf("%s·%s：没有正文却留了冒号：%q", site, name, s)
				}
			}
			// 五张图进合并转发：头部节点同样不留空行
			many := r
			for i := 2; i <= 5; i++ {
				many = withMedia(many, goldenImage(i))
			}
			for _, s := range allTexts(build(many, Options{})) {
				noBlankOrEmptyQuote(t, site+"·转发·"+name, s)
			}
		}
	}
}
