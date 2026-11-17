package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/segview"
)

func hasLinks(b segview.Block) bool {
	for _, r := range b.Runs {
		if r.Link != nil {
			return true
		}
	}
	return false
}

// 调用点在 DTO 转换中；绘制只读已算好的表，正文在前、卡片在后。
func linksOf(m *msg) []segview.LinkRef {
	var out []segview.LinkRef
	seen := map[string]bool{}
	add := func(l segview.LinkRef) {
		if !seen[l.Clean] && len(out) < 20 {
			seen[l.Clean] = true
			out = append(out, l)
		}
	}
	for _, b := range docOf(m) {
		for _, r := range b.Runs {
			if r.Link != nil {
				add(*r.Link)
			}
		}
	}
	for _, s := range m.segs {
		for _, raw := range s.Links {
			if clean, err := linkreader.CleanAny(raw); err == nil {
				add(segview.LinkRef{Raw: raw, Open: raw, Clean: clean})
			}
		}
	}
	return out
}

func clipURL(raw string) string {
	rs := []rune(raw)
	if len(rs) > 200 {
		return string(rs[:200]) + "…"
	}
	return raw
}

func (a *app) linkMenu(c *ui.Context, root ui.Element, links []segview.LinkRef, edit func(*ui.Menu)) {
	if len(links) == 0 {
		if edit != nil {
			root.ContextMenu(edit)
		}
		return
	}
	services := c.Services()
	root.PopoverMenu(func(m *ui.Menu) {
		labels := []string{"复制链接", "复制干净链接"}
		if len(links) > 1 {
			labels = []string{"复制所有链接", "复制所有干净链接"}
		}
		for i, label := range labels {
			if m.Item(label).Chosen() {
				values := make([]string, len(links))
				for j, l := range links {
					values[j] = l.Open
					if i == 1 {
						values[j] = l.Clean
					}
				}
				services.WriteClipboard(strings.Join(values, "\n"))
				playFeedback(feedbackSelect)
			}
		}
		m.Separator()
		if edit != nil {
			edit(m)
		} else {
			m.EditItems()
		}
	})
}
