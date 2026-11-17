package linkexpand

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/linktrunc"
)

// 合并转发的节点名和卡片文字（规格-链接解析模板第十四节）。

// ForwardOptions 是配置里 linkReader.expand.forward 的副本（两边类型独立，linkexpand 不依赖配置包）。全空是 QQ 的默认样子。
type ForwardOptions struct {
	NodeName string
	Source   string
	News     []string
	Summary  string
	Prompt   string
}

// ForwardNodeName 决定合并转发节点的显示名：配置里写了就用配置的（去掉控制字符、截到 40 个字），没写用她自己的 QQ 昵称，
// 取不到昵称才用 fallback（这条路径原来的写法）。返回的 self 为真表示发送时换成 QQ 昵称。头像和号永远是连接自己的。
func ForwardNodeName(configured, fallback string) (name string, self bool) {
	if n := cleanNodeName(configured); n != "" {
		return n, false
	}
	return fallback, true
}

func cleanNodeName(s string) string {
	rs := make([]rune, 0, linktemplate.NodeNameRunes)
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\ufffc' {
			continue
		}
		if len(rs) == linktemplate.NodeNameRunes {
			break
		}
		rs = append(rs, r)
	}
	return strings.TrimSpace(string(rs))
}

// applyForward 给草稿里的每个合并转发套上节点名和卡片文字。
func applyForward(d *Draft, r *linkreader.Result, f ForwardOptions) {
	var card *types.ForwardCard
	for i := range d.Parts {
		p := &d.Parts[i]
		if p.Kind != types.PartForward {
			if p.capForward { // Prepare 后图超上限要改成合并转发（shape.go）：节点名和卡片文字照常先备好
				var self bool
				p.capNode.Name, self = ForwardNodeName(f.NodeName, NodeName)
				p.capNode.Fixed = !self
				if card == nil {
					card = BuildCard(r, f)
				}
				p.capCard = card
			}
			continue
		}
		for j := range p.Nodes {
			n := &p.Nodes[j]
			var self bool
			n.Name, self = ForwardNodeName(f.NodeName, n.Name)
			n.Fixed = !self
		}
		if card == nil {
			card = BuildCard(r, f)
		}
		p.Card = card
	}
}

// BuildCard 按配置的四项模板和这条链接的内容渲染卡片文字；一项都没有返回 nil（全用 QQ 默认）。
func BuildCard(r *linkreader.Result, f ForwardOptions) *types.ForwardCard {
	if r == nil {
		return nil
	}
	env := cardEnv{r: r}
	one := func(src string, limit int) string {
		tpl, err := linktemplate.ParseCard(src)
		if err != nil {
			return "" // 配置校验会拦住，这里再错就用默认，不发空壳
		}
		return linktrunc.Display(linktemplate.RenderCard(tpl, env), limit)
	}
	c := &types.ForwardCard{
		Source:  one(f.Source, linktemplate.CardSourceLimit),
		Summary: one(f.Summary, linktemplate.CardSummaryLimit),
		Prompt:  one(f.Prompt, linktemplate.CardPromptLimit),
	}
	for i, src := range f.News {
		if i >= linktemplate.CardNewsLines {
			break
		}
		if line := one(src, linktemplate.CardNewsLimit); line != "" {
			c.News = append(c.News, line)
		}
	}
	if c.Empty() {
		return nil
	}
	return c
}

// cardEnv 是卡片模板的取值：字段缺或为空返回 ok=false，整项就退回 QQ 默认。
type cardEnv struct{ r *linkreader.Result }

func (cardEnv) Slot(string) bool { return false }

func (e cardEnv) Text(id string) (string, bool) {
	r := e.r
	switch id {
	case "title":
		if v := optText(r.Title); v.ok && strings.TrimSpace(v.text) != "" {
			return strings.TrimSpace(v.text), true
		}
		// 没有标题的帖子（动态、推文）用正文第一行
		if v := optText(r.Text); v.ok {
			for _, line := range strings.Split(v.text, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					return line, true
				}
			}
		}
	case "chars":
		n := 0
		if r.Novel != nil && r.Novel.CharacterCount.State == "available" {
			n = int(r.Novel.CharacterCount.Value)
		}
		if n == 0 && r.Text.State == "available" {
			n = utf8.RuneCountInString(strings.TrimSpace(r.Text.Value))
		}
		if n > 0 {
			return "共 " + strconv.Itoa(n) + " 字", true
		}
	case "images":
		if n := imageCount(r); n > 0 {
			return strconv.Itoa(n) + " 张图", true
		}
	case "author":
		if v := optText(r.Author.Name); v.ok && strings.TrimSpace(v.text) != "" {
			return strings.TrimSpace(v.text), true
		}
	case "site":
		key := linktemplate.SiteKey(r.Platform, r.Kind)
		if site, _, ok := linktemplate.SiteByKey(key); ok {
			b := &builder{r: r, site: site}
			if v, ok := b.Text("site"); ok && v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// imageCount 数这条内容里的图（动图和视频不算）；pixiv 以作品页数为准。
func imageCount(r *linkreader.Result) int {
	n := 0
	if len(r.Blocks) > 0 {
		for _, b := range r.Blocks {
			if b.Type == "image" || b.Type == "photo" {
				n++
			}
		}
	} else {
		for _, m := range r.Media.Value {
			if m.Type == "image" || m.Type == "photo" {
				n++
			}
		}
	}
	if p := r.Stats["pages"]; p.State == "available" && int(p.Value) > n {
		n = int(p.Value)
	}
	return n
}
