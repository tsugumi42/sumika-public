package main

import (
	"fmt"
	"math"
	"strings"
)

type finding struct {
	Rule      string `json:"rule"`
	Node      string `json:"node"`
	Label     string `json:"label"`
	Detail    string `json:"detail"`
	Candidate bool   `json:"candidate,omitempty"`
}
type state struct {
	Name            string    `json:"name"`
	EmptyRegion     string    `json:"emptyRegion,omitempty"`
	Window          window    `json:"window"`
	Tree            tree      `json:"tree"`
	Issues          []string  `json:"issues"`
	Inspector       tree      `json:"inspector"`
	Frames          []frame   `json:"frames"`
	InspectorFrames []frame   `json:"inspectorFrames"`
	Width           int       `json:"pixelWidth"`
	Height          int       `json:"pixelHeight"`
	Millis          float64   `json:"captureMillis"`
	Findings        []finding `json:"findings"`
	IdleErrors      []string  `json:"idleErrors,omitempty"`
}

func check(s state) []finding {
	var out []finding
	for _, rule := range []func(state) []finding{overflow, clippedText, smallTargets, familySizes, duplicateKeys, emptyText} {
		out = append(out, rule(s)...)
	}
	return out
}
func issue(rule string, n node, detail string, candidate bool) finding {
	return finding{rule, n.ID, n.Label, detail, candidate}
}
func visible(n node) bool {
	return !has(n.States, "offscreen") && n.Bounds.Width > 0 && n.Bounds.Height > 0
}
func contains(a, b rect) bool {
	const e = 0.8
	return b.X >= a.X-e && b.Y >= a.Y-e && b.X+b.Width <= a.X+a.Width+e && b.Y+b.Height <= a.Y+a.Height+e
}
func intersects(a, b rect) bool {
	return a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
}
func index(t tree) map[string]node {
	byID := make(map[string]node, len(t.Nodes))
	for _, n := range t.Nodes {
		byID[n.ID] = n
	}
	return byID
}
func under(n node, root string, byID map[string]node) bool {
	for p := n.Parent; p != ""; p = byID[p].Parent {
		if p == root {
			return true
		}
	}
	return false
}
func viewport(s state, n node, byID map[string]node) (rect, bool) {
	// AX 省略普通布局容器；只拿有明确裁剪语义的 scroll/list 祖先判边界。
	for p := n.Parent; p != ""; {
		v, ok := byID[p]
		if !ok {
			break
		}
		if v.Role == "scroll" || v.Role == "list" {
			return v.Bounds, true
		}
		p = v.Parent
	}
	return rect{Width: s.Window.ContentBounds.Width, Height: s.Window.ContentBounds.Height}, false
}
func overflow(s state) []finding {
	var out []finding
	byID := index(s.Tree)
	for _, n := range s.Tree.Nodes {
		if n.Bounds.Width <= 0 || n.Bounds.Height <= 0 {
			continue
		}
		v, scroll := viewport(s, n, byID)
		if !contains(v, n.Bounds) {
			// 可正常滚动的首尾行不是静态布局缺陷，仍保留完整坐标在树内。
			if scroll && (n.Bounds.Y+n.Bounds.Height <= v.Y || n.Bounds.Y >= v.Y+v.Height || n.Bounds.X >= v.X-0.8 && n.Bounds.X+n.Bounds.Width <= v.X+v.Width+0.8) {
				continue
			}
			out = append(out, issue("overflow", n, fmt.Sprintf("bounds=%+v viewport=%+v", n.Bounds, v), false))
		}
	}
	return out
}
func clippedText(s state) []finding {
	var out []finding
	byID := index(s.Tree)
	for _, n := range s.Tree.Nodes {
		if !visible(n) || n.Role != "text" || strings.TrimSpace(n.Label) == "" {
			continue
		}
		v, scroll := viewport(s, n, byID)
		if !scroll && !contains(v, n.Bounds) && intersects(v, n.Bounds) && !strings.HasSuffix(n.Label, "…") && !strings.HasSuffix(n.Label, "...") {
			out = append(out, issue("clipped-text", n, "文字框被客户区裁剪；AX 不提供实际字形和省略号，需对照 PNG", true))
		}
	}
	return out
}
func smallTargets(s state) []finding {
	var out []finding
	for _, n := range s.Tree.Nodes {
		if visible(n) && has(n.Actions, "press") && !has(n.States, "disabled") && (n.Bounds.Width < 32 || n.Bounds.Height < 32) {
			out = append(out, issue("small-target", n, fmt.Sprintf("%.2f×%.2f DIP < 32×32", n.Bounds.Width, n.Bounds.Height), false))
		}
	}
	return out
}
func familySizes(s state) []finding {
	var out []finding
	groups := map[string][]node{}
	for _, n := range s.Tree.Nodes {
		if !visible(n) || (n.Role != "tab" && n.Role != "radio") {
			continue
		}
		groups[n.Parent+"/"+n.Role] = append(groups[n.Parent+"/"+n.Role], n)
	}
	for _, ns := range groups {
		if len(ns) < 2 {
			continue
		}
		first := ns[0]
		for _, n := range ns[1:] {
			if math.Abs(n.Bounds.Height-first.Bounds.Height) > 0.8 {
				out = append(out, issue("family-size", n, fmt.Sprintf("同父 %s 高 %.2f，%s 高 %.2f DIP", n.Role, n.Bounds.Height, first.ID, first.Bounds.Height), false))
			}
		}
	}
	return out
}
func duplicateKeys(s state) []finding {
	var out []finding
	seen := map[struct{ parent, key string }]string{}
	for _, n := range s.Tree.Nodes {
		if len(n.Key) == 0 || string(n.Key) == "null" {
			continue
		}
		// 官方 JSON 编码保留标量 Key 类型，不能解成 float64 再编码（大整数会丢精度）。
		key := strings.TrimSpace(string(n.Key))
		id := struct{ parent, key string }{n.Parent, key}
		if prev, ok := seen[id]; ok {
			out = append(out, issue("duplicate-key", n, "AX 同父 Key="+key+" 与 "+prev+" 重复；需对照 Issues 确认布局父级", true))
		} else {
			seen[id] = n.ID
		}
	}
	for _, v := range s.Issues {
		if strings.Contains(v, "key") {
			out = append(out, finding{Rule: "duplicate-key", Detail: v})
		}
	}
	return out
}
func emptyText(s state) []finding {
	var out []finding
	byID := index(s.Tree)
	for _, n := range s.Tree.Nodes {
		if !visible(n) || !(s.EmptyRegion != "" && n.Label == s.EmptyRegion || n.Role == "list" && n.SetSize == 0) {
			continue
		}
		text := false
		for _, ch := range s.Tree.Nodes {
			if !visible(ch) || (ch.Role != "text" && ch.Role != "status" && ch.Role != "heading") || strings.TrimSpace(ch.Label) == "" || ch.ID == n.ID {
				continue
			}
			for p := ch.Parent; p != ""; {
				if p == n.ID {
					text = true
					break
				}
				p = byID[p].Parent
			}
		}
		// 空列表的说明常是旁边的兄弟，只要在同一视口可见就算有解释。
		if !text && n.Role == "list" {
			for _, ch := range s.Tree.Nodes {
				if visible(ch) && ch.Role == "text" && ch.Parent == n.Parent && strings.TrimSpace(ch.Label) != "" {
					text = true
					break
				}
			}
		}
		if !text {
			out = append(out, issue("empty-text", n, "空区域没有可见说明文字", false))
		}
	}
	return out
}
