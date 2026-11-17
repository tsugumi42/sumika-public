package main

import "testing"

func TestFindNestedAction(t *testing.T) {
	// 真窗口导航暴露 listItem 和同名 Row；合并祖先匹配后定位唯一命中框。
	outer := node{ID: "item", Role: "listItem", Label: "设置", Actions: []string{"press"}, Bounds: rect{Width: 72, Height: 62}}
	inner := node{ID: "row", Parent: "item", Role: "group", Label: "设置", Actions: outer.Actions, Bounds: outer.Bounds}
	got, err := findNode(tree{Nodes: []node{outer, inner}}, "设置", "press", "")
	if err != nil || got.ID != outer.ID {
		t.Fatalf("未定位外层导航：%+v %v", got, err)
	}
	peer := outer
	peer.ID = "other"
	if _, err = findNode(tree{Nodes: []node{outer, inner, peer}}, "设置", "press", ""); err == nil {
		t.Fatal("不同分支的同名入口不能盲选")
	}
}
