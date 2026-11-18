package main

import (
	"encoding/json"
	"testing"
)

// 每条规则给会影响消费者判定的反例和相邻正常形状；不按源码字串或调用次数验收。
func TestRules(t *testing.T) {
	base := func(ns ...node) state {
		return state{Window: window{ContentBounds: rect{Width: 200, Height: 100}}, Tree: tree{Nodes: ns}}
	}
	text := node{ID: "text", Role: "text", Label: "编造的说明", Bounds: rect{X: 190, Y: 10, Width: 30, Height: 20}}
	target := node{ID: "button", Role: "button", Label: "编的按钮", Actions: []string{"press"}, Bounds: rect{Width: 20, Height: 20}}
	tabs := []node{{ID: "a", Parent: "tabs", Role: "tab", Bounds: rect{Width: 50, Height: 32}}, {ID: "b", Parent: "tabs", Role: "tab", Bounds: rect{X: 50, Width: 50, Height: 20}}}
	keys := []node{{ID: "a", Parent: "p", Key: json.RawMessage(`"same"`)}, {ID: "b", Parent: "p", Key: json.RawMessage(`"same"`)}}
	empty := node{ID: "empty", Role: "group", Label: "空区域", Bounds: rect{Width: 100, Height: 100}}
	tests := []struct {
		name      string
		rule      func(state) []finding
		bad, good state
	}{
		{"overflow", overflow, base(text), base(node{ID: "in", Role: "text", Bounds: rect{Width: 200, Height: 100}})},
		{"clipped-text", clippedText, base(text), base(node{ID: "ellipsis", Role: "text", Label: "编造…", Bounds: text.Bounds})},
		{"small-target", smallTargets, base(target), base(node{ID: "ok", Actions: []string{"press"}, Bounds: rect{Width: 32, Height: 32}})},
		{"family-size", familySizes, base(tabs...), base(tabs[0], node{ID: "b", Parent: "tabs", Role: "tab", Bounds: rect{Width: 80, Height: 32}})},
		{"duplicate-key", duplicateKeys, base(keys...), base(keys[0], node{ID: "b", Parent: "q", Key: keys[0].Key})},
		{"empty-text", emptyText, state{EmptyRegion: "空区域", Tree: tree{Nodes: []node{empty}}}, state{EmptyRegion: "空区域", Tree: tree{Nodes: []node{empty, {ID: "hint", Parent: "empty", Role: "text", Label: "没有会话，试试其他关键词", Bounds: rect{Width: 90, Height: 20}}}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rule(tc.bad)
			wantNode := tc.bad.Tree.Nodes[len(tc.bad.Tree.Nodes)-1].ID
			if len(got) != 1 || got[0].Rule != tc.name || got[0].Node != wantNode {
				t.Fatalf("规则 %s 应定位到 %s: %+v", tc.name, wantNode, got)
			}
			if got := tc.rule(tc.good); len(got) != 0 {
				t.Fatalf("正常相邻形状误报: %+v", got)
			}
		})
	}
}

func TestOverflowFullyOutside(t *testing.T) {
	s := state{Window: window{ContentBounds: rect{Width: 200, Height: 100}}, Tree: tree{Nodes: []node{{ID: "lost", Role: "button", Bounds: rect{X: 210, Y: 10, Width: 40, Height: 32}, States: []string{"offscreen"}}}}}
	got := overflow(s)
	if len(got) != 1 || got[0].Node != "lost" || got[0].Rule != "overflow" {
		t.Fatalf("完全移出窗口的控件漏报: %+v", got)
	}
	s.Tree.Nodes[0].Parent = "scroll"
	s.Tree.Nodes = append(s.Tree.Nodes, node{ID: "scroll", Role: "scroll", Bounds: rect{Width: 200, Height: 100}})
	got = overflow(s)
	if len(got) != 1 || got[0].Node != "lost" {
		t.Fatalf("水平移出滚动视口的控件漏报: %+v", got)
	}
	s.Tree.Nodes[0].Bounds = rect{X: 10, Y: 110, Width: 40, Height: 32}
	if got = overflow(s); len(got) != 0 {
		t.Fatalf("正常可向下滚动的控件误报: %+v", got)
	}
}
