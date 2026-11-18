package linkexpand

import (
	"fmt"
	"slices"
	"strings"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linktemplate"
)

// 发送形状（规格-链接发送改版 B3，2026-11-12 复核修订）：模板拼完以后由代码保证、所有站点一样（自定义模板也生效）的几条：
//  1. 直接发的一条消息里图（静态图、卡片、动图）最多 directImageMax 张。超过时：站点不是 never 就整条改成合并转发
//     （第一个节点是这条的全部文字，后面一图一节点）；站点设成 never（有没有转发模板都能设）就只发前 directImageMax 张，
//     文字末尾标“（发出/总数）”，不拆成几条（用户 2026-11-13，R-217）。
//  2. 直接发的消息里视频不和文字挤一条（QQ 的视频消息不能带文字）：视频标 Standalone，Prepare 把它单独发成一条。
//  3. X 拆图候选组在 Build 时还没有像素，按拼回后的一张算；Prepare 拼不成、图数超过上限时按第 1 条再定一次（imageCap、capForward）。
//  4. 只因为图多才进的合并转发（fitDirect）：Prepare 后实际发得出的图不超过上限、也没有视频，就收回成一条消息
//     （图没下下来时不发只剩一个文字节点的转发）。

// visualItem 说这一项在消息里算不算一张图。
func visualItem(it Item) bool {
	switch it.Kind {
	case types.BodyImage, "photo", "card", types.BodyGIF, "ugoira":
		return true
	}
	return false
}

func visualSeg(s types.BodySegment) bool { return s.Kind == types.BodyImage || s.Kind == types.BodyGIF }

func countVisual(body []types.BodySegment) (n int) {
	for _, s := range body {
		if visualSeg(s) {
			n++
		}
	}
	return n
}

// itemImages 数一条消息里的图：n 把拆图候选组算成一张，all 是没拼成时的张数。
func itemImages(items []Item) (n, all int) {
	skip := 0
	for _, it := range items {
		if !visualItem(it) {
			continue
		}
		all++
		if skip > 0 {
			skip--
			continue
		}
		n++
		if it.PanoramaN > 1 {
			skip = it.PanoramaN - 1
		}
	}
	return n, all
}

// shapeDirect 对草稿里每条直接发的消息执行上面第 1～3 条。
func shapeDirect(d *Draft, limit int, forwardOK bool) {
	var parts []Part
	for _, p := range d.Parts {
		if p.Kind != types.PartMessage {
			parts = append(parts, p)
			continue
		}
		p.Body = append([]Item(nil), p.Body...)
		for i := range p.Body {
			if p.Body[i].Kind == types.BodyVideo {
				p.Body[i].Standalone = true
			}
		}
		n, all := itemImages(p.Body)
		switch {
		case n <= limit:
			if all > limit {
				p.imageCap, p.capForward = limit, forwardOK
			}
			parts = append(parts, p)
		case forwardOK:
			// 只因为图多才进的转发：Prepare 后图少了就收回（第 4 条），没有转发模板的站点也一样
			parts = append(parts, Part{Kind: types.PartForward, Quote: p.Quote, Nodes: itemsToNodes(p.Body, d.nodeBytes), fitDirect: true, imageCap: limit, nodeBytes: d.nodeBytes})
		default:
			parts = append(parts, truncateImages(p, limit))
		}
	}
	d.Parts = parts
}

// splitText 把一段文字切成每段最多 maxBytes 个 UTF-8 字节（切点不落在链接或多字节字符中间，见 nodeHead）；
// 切处的空行去掉，不留在节点末尾。
func splitText(s string, maxBytes int) []string { return splitTextCost(s, maxBytes, nil) }

// splitTextCost 是 splitText 的可计数版本；cost 非空时累计链接区间扫描和候选切点检查的次数（测试用，不靠墙钟）。
func splitTextCost(s string, maxBytes int, cost *linkCost) []string {
	if maxBytes <= 0 {
		maxBytes = linktemplate.DefaultForwardOverBytes
	}
	var out []string
	for rest := s; rest != ""; {
		head := nodeHeadCost(rest, maxBytes, cost)
		rest = strings.TrimLeft(rest[len(head):], "\n")
		if head = strings.TrimRight(head, "\n"); head != "" {
			out = append(out, head)
		}
	}
	return out
}

// splitLongMessage 把一条普通消息里超过 maxBytes 的文字按 splitText 的切法拆成几条依次发（ADR-391）：
// 切点照节点规则（空行、换行、句末，不切 URL 和多字节字符）；后面的消息不重复前面的头部；
// 图、视频等按原顺序留在它们所在的那一条里，引用只挂在第一条。没超限原样返回。
func splitLongMessage(p types.Part, maxBytes int) []types.Part {
	if p.Kind != types.PartMessage {
		return []types.Part{p}
	}
	if maxBytes <= 0 {
		maxBytes = linktemplate.DefaultForwardOverBytes
	}
	var batches [][]types.BodySegment
	var cur []types.BodySegment
	textBytes := 0
	flush := func() {
		if len(cur) > 0 {
			batches = append(batches, cur)
			cur, textBytes = nil, 0
		}
	}
	appendText := func(s string) {
		if s == "" {
			return
		}
		add := len(s)
		if len(cur) > 0 && cur[len(cur)-1].Kind == types.BodyText {
			add++ // 和上一段文字之间会补一个换行
		}
		if textBytes > 0 && textBytes+add > maxBytes {
			flush()
		}
		if len(cur) > 0 && cur[len(cur)-1].Kind == types.BodyText {
			cur[len(cur)-1].Text += "\n" + s
			textBytes += 1 + len(s)
			return
		}
		cur = append(cur, types.BodySegment{Kind: types.BodyText, Text: s})
		textBytes += len(s)
	}
	for _, s := range p.Body {
		if s.Kind != types.BodyText {
			cur = append(cur, s)
			continue
		}
		if len(s.Text) <= maxBytes {
			appendText(s.Text)
			continue
		}
		for _, piece := range splitText(s.Text, maxBytes) {
			appendText(piece)
		}
	}
	flush()
	if len(batches) <= 1 {
		return []types.Part{p}
	}
	out := make([]types.Part, 0, len(batches))
	for i, b := range batches {
		q := ""
		if i == 0 {
			q = p.Quote
		}
		out = append(out, types.Part{Kind: types.PartMessage, Quote: q, Body: b})
	}
	return out
}

// markSplitPieces 给同一条原文拆出来的几条打上同一组标记：发送时某条被拒，后面的不再发（B16）。
// 只有真的拆成多条时才有标记；没拆的原样。
func markSplitPieces(parts []types.Part) {
	if len(parts) < 2 {
		return
	}
	g := &types.SplitGroup{}
	for i := range parts {
		parts[i].Split = &types.SplitPart{Group: g, Index: i + 1, Total: len(parts)}
	}
}

// joinTexts 把几段文字接成一段（一段一行），空的跳过。
func joinTexts(texts []string) string {
	var keep []string
	for _, s := range texts {
		if strings.TrimSpace(s) != "" {
			keep = append(keep, s)
		}
	}
	return strings.Join(keep, "\n")
}

// itemsToNodes 把一条直接发的消息改成合并转发的节点：全部文字（按原顺序接起来，干净链接也在里面）排在最前，图跟在文字后面放同一个节点（packNodes），
// 视频、卡片等其它项各自一节点。maxBytes 和转发门槛是同一个配置值（ADR-391）。
func itemsToNodes(items []Item, maxBytes int) []Node {
	var texts []string
	var rest []Item
	for _, it := range items {
		if it.Kind == types.BodyText {
			texts = append(texts, it.Text)
			continue
		}
		it.PanoramaN, it.PanoramaKeep, it.Standalone = 0, false, false
		rest = append(rest, it)
	}
	nodes := toNodes(append([]Item{text(joinTexts(texts))}, rest...), maxBytes)
	markNoteEnd(nodes)
	return nodes
}

// truncateImages 是站点设成从不合并转发、图又多于上限时的排法（用户 2026-11-13，R-217）：只发前 limit 张图，其余丢掉，不拆成几条；
// 文字、视频、引用都留着。图原来一共几张记在 imageTotal，Prepare 按真正发出去几张在文字末尾标“（发出/总数）”。
func truncateImages(p Part, limit int) Part {
	out := Part{Kind: types.PartMessage, Quote: p.Quote}
	seen := 0
	for _, it := range p.Body {
		it.PanoramaN, it.PanoramaKeep = 0, false
		if visualItem(it) {
			seen++
			if seen > limit {
				continue
			}
		}
		out.Body = append(out.Body, it)
	}
	out.imageTotal = seen
	return out
}

// markTruncated 在文字末尾标“（发出/总数）”，说明图被截断了；一段文字都没有就在第一张图前加一段。一张都没发出去就不标。
func markTruncated(body []types.BodySegment, sent, total int) []types.BodySegment {
	if sent <= 0 || total <= sent {
		return body
	}
	mark := fmt.Sprintf("（%d/%d）", sent, total)
	for i := len(body) - 1; i >= 0; i-- {
		if body[i].Kind == types.BodyText {
			body[i].Text += "\n" + mark
			return body
		}
	}
	for i, s := range body {
		if visualSeg(s) {
			return slices.Insert(body, i, types.BodySegment{Kind: types.BodyText, Text: mark})
		}
	}
	return body
}

// keepFirstImages 只留 body 里前 limit 张图（静态图、动图），其余丢掉；total 是原来有几张。
func keepFirstImages(body []types.BodySegment, limit int) (kept []types.BodySegment, total int) {
	for _, s := range body {
		if visualSeg(s) {
			total++
			if total > limit {
				continue
			}
		}
		kept = append(kept, s)
	}
	return kept, total
}

// overCap 是 Prepare 的第 3 条：拆图没拼成、准备好的一条消息里图超过上限时，改成合并转发或拆成几条。
func overCap(p Part, got types.Part) []types.Part {
	if p.capForward {
		var texts []string
		var rest []types.BodySegment
		for _, s := range got.Body {
			if s.Kind == types.BodyText {
				texts = append(texts, s.Text)
			} else {
				rest = append(rest, s)
			}
		}
		out := types.Part{Kind: types.PartForward, Quote: got.Quote, Card: p.capCard}
		var items []types.BodySegment
		for _, s := range got.Body {
			if s.Kind != types.BodyText {
				items = append(items, s)
			}
		}
		items = append([]types.BodySegment{{Kind: types.BodyText, Text: joinTexts(texts)}}, items...)
		isText := func(s types.BodySegment) bool { return s.Kind == types.BodyText }
		for _, body := range packNodes(items, isText, func(s types.BodySegment) string { return s.Text }, func(t string) types.BodySegment { return types.BodySegment{Kind: types.BodyText, Text: t} }, visualSeg, p.nodeBytes) {
			out.Nodes = append(out.Nodes, types.ForwardNode{Name: p.capNode.Name, Self: !p.capNode.Fixed, Body: body})
		}
		return []types.Part{out}
	}
	kept, total := keepFirstImages(got.Body, p.imageCap)
	return []types.Part{{Kind: types.PartMessage, Quote: got.Quote, Body: markTruncated(kept, p.imageCap, total)}}
}

// collapseForward 是 Prepare 的第 4 条：转发里发得出的图不超过 limit 张、没有视频，就把各节点按顺序接成一条消息（相邻文字并成一段）。
// 不合适时原样返回 false。
func collapseForward(got types.Part, quote string, limit int) (types.Part, bool) {
	images := 0
	var body []types.BodySegment
	for _, n := range got.Nodes {
		for _, s := range n.Body {
			switch {
			case s.Kind == types.BodyVideo:
				return got, false
			case visualSeg(s):
				images++
			}
			if s.Kind == types.BodyText && len(body) > 0 && body[len(body)-1].Kind == types.BodyText {
				body[len(body)-1].Text += "\n" + s.Text
				continue
			}
			body = append(body, s)
		}
	}
	if images > limit || len(body) == 0 {
		return got, false
	}
	return types.Part{Kind: types.PartMessage, Quote: quote, Body: body}, true
}
