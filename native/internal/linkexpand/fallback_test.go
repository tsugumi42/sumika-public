package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// 文字版：文字和卡片语义都在，媒体段不在；链接补在最后；引用照带
func TestTextFallbackDropsMediaKeepsText(t *testing.T) {
	picture := types.MemoryMedia("pic", "image/png", 1, 1, []byte{1})
	p := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: "B站视频｜编的标题"},
		{Kind: types.BodyImage, Media: picture, Semantic: "卡片：编的标题"},
		{Kind: types.BodyText, Text: "编的说明"},
	}}
	got, ok := TextFallback(p, "-71", "https://example.invalid/v/1")
	if !ok {
		t.Fatal("有文字，应该能造文字版")
	}
	if got.Kind != types.PartMessage || got.Quote != "-71" {
		t.Fatalf("种类或引用不对：%+v", got)
	}
	for _, s := range got.Body {
		if s.Kind != types.BodyText || s.Media != nil {
			t.Fatalf("文字版里不能有媒体段：%+v", got.Body)
		}
	}
	text := got.Body[0].Text
	for _, want := range []string{"B站视频｜编的标题", "卡片：编的标题", "编的说明", "https://example.invalid/v/1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("文字版缺 %q：%q", want, text)
		}
	}
}

// 转发段：节点里的文字也要进文字版；链接已经在文字里就不重复补
func TestTextFallbackForwardNodesAndNoDuplicateLink(t *testing.T) {
	p := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "外部内容", Body: []types.BodySegment{{Kind: types.BodyText, Text: "编的正文 https://example.invalid/x"}}},
		{Name: "外部内容", Body: []types.BodySegment{{Kind: types.BodyVideo, Media: types.MemoryMedia("v", "video/mp4", 1, 1, []byte{1}), Semantic: "视频说明"}}},
	}}
	got, ok := TextFallback(p, "-72", "https://example.invalid/x")
	if !ok {
		t.Fatal("节点里有文字，应该能造")
	}
	text := got.Body[0].Text
	if strings.Count(text, "https://example.invalid/x") != 1 || !strings.Contains(text, "视频说明") {
		t.Fatalf("链接重复或说明丢了：%q", text)
	}
}

// 只有媒体、没有任何字：不造文字版（也没有链接可补时）
func TestTextFallbackNothingToSay(t *testing.T) {
	p := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage, Media: types.MemoryMedia("pic", "image/png", 1, 1, []byte{1})}}}
	if _, ok := TextFallback(p, "-71", ""); ok {
		t.Fatal("只有图、没有字，不该造文字版")
	}
}
