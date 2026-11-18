package jsdiff

import (
	"strings"
	"testing"
)

func joinParts(parts []Part, skip func(Part) bool) string {
	var b strings.Builder
	for _, p := range parts {
		if !skip(p) {
			b.WriteString(p.Value)
		}
	}
	return b.String()
}

// 整体替换大文件：不再算最短改法（Myers 的耗时是改动行数的平方），公共的头尾留着，中间先删后加。
func TestDiffLinesHugeEditIsWholesale(t *testing.T) {
	const lines = 6000
	head, tail := "头一行\n头二行\n", "尾一行\n尾二行\n尾三行\n"
	before := head + strings.Repeat("旧\n", lines) + tail
	after := head + strings.Repeat("新\n", lines) + tail
	oldT, newT := tokenize(before), tokenize(after)
	if _, ok := myers(oldT, newT, maxEditLength); ok {
		t.Fatalf("%d 行整段替换要 %d 步，超过上限 %d，不该算到底", lines, 2*lines, maxEditLength)
	}
	parts := wholesale(oldT, newT) // DiffLines 接上这条路由 turn 的 TestLargeEditDetailsBoundedWithoutChangingModelText 验
	if len(parts) != 4 {
		t.Fatalf("应该是 头、删、加、尾 四段：%d 段", len(parts))
	}
	if p := parts[0]; p.Added || p.Removed || p.Value != head || p.Count != 2 {
		t.Fatalf("公共开头：%+v", p)
	}
	if p := parts[1]; !p.Removed || p.Count != lines || p.Value != strings.Repeat("旧\n", lines) {
		t.Fatalf("先删：%d 行，%v", p.Count, p.Removed)
	}
	if p := parts[2]; !p.Added || p.Count != lines || p.Value != strings.Repeat("新\n", lines) {
		t.Fatalf("后加：%d 行，%v", p.Count, p.Added)
	}
	if p := parts[3]; p.Added || p.Removed || p.Value != tail || p.Count != 3 {
		t.Fatalf("公共结尾：%+v", p)
	}
	if got := joinParts(parts, func(p Part) bool { return p.Added }); got != before {
		t.Fatal("去掉加的应该还原旧文本")
	}
	if got := joinParts(parts, func(p Part) bool { return p.Removed }); got != after {
		t.Fatal("去掉删的应该还原新文本")
	}
}

// 头尾只能算一次：旧文本整个是新文本的开头时，公共部分不能重叠。
func TestWholesaleOverlap(t *testing.T) {
	for _, c := range []struct{ before, after string }{
		{"甲\n甲\n", "甲\n甲\n甲\n"},
		{"甲\n甲\n甲\n", "甲\n"},
		{"甲\n乙\n", ""},
		{"", "甲\n乙\n"},
		{"甲\n", "甲\n"},
	} {
		parts := wholesale(tokenize(c.before), tokenize(c.after))
		if got := joinParts(parts, func(p Part) bool { return p.Added }); got != c.before {
			t.Errorf("%q → %q：还原旧文本 %q", c.before, c.after, got)
		}
		if got := joinParts(parts, func(p Part) bool { return p.Removed }); got != c.after {
			t.Errorf("%q → %q：还原新文本 %q", c.before, c.after, got)
		}
	}
}

// 没到上限的照旧算最短改法（和 jsdiff 逐段相同由 TestSameAsBun 钉住）：隔行改 1000 处，只改到的行算改动。
func TestDiffLinesUnderLimitStaysMinimal(t *testing.T) {
	var before, after strings.Builder
	for range 1000 {
		before.WriteString("同\n旧\n")
		after.WriteString("同\n新\n")
	}
	parts, ok := myers(tokenize(before.String()), tokenize(after.String()), maxEditLength)
	if !ok {
		t.Fatal("2000 步没到上限")
	}
	removed := 0
	for _, p := range parts {
		if p.Removed {
			removed += p.Count
		}
	}
	if removed != 1000 {
		t.Fatalf("只有 1000 行改了，算出来删了 %d 行", removed)
	}
}
