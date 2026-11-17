package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// 文字 + 图 + 视频混排的长消息拆条：图、视频按原顺序留在它们所在的那一条，每条都有文字，
// 不出现空消息或只有媒体的条；引用只挂第一条。
func TestSplitLongMessageKeepsMediaPositions(t *testing.T) {
	image := types.MemoryMedia("i", "image/png", 1, 1, []byte("png"))
	video := types.MemoryMedia("v", "video/mp4", 1, 1, []byte("mp4"))
	texts := []string{strings.Repeat("甲", 70), strings.Repeat("乙", 70), strings.Repeat("丙", 70)}
	body := []types.BodySegment{
		{Kind: types.BodyText, Text: texts[0]},
		{Kind: types.BodyImage, Media: image},
		{Kind: types.BodyText, Text: texts[1]},
		{Kind: types.BodyVideo, Media: video},
		{Kind: types.BodyText, Text: texts[2]},
	}
	parts := splitLongMessage(types.Part{Kind: types.PartMessage, Quote: "-71", Body: body}, 300)
	if len(parts) != 3 {
		t.Fatalf("应拆成 3 条，得到 %d：%+v", len(parts), parts)
	}
	wantKinds := []string{"text,image", "text,video", "text"}
	var gotMedia []string
	var gotText strings.Builder
	for i, p := range parts {
		if len(p.Body) == 0 {
			t.Fatalf("第 %d 条是空的", i+1)
		}
		if got := segKinds(p.Body); got != wantKinds[i] {
			t.Fatalf("第 %d 条的段序是 %s，应该是 %s", i+1, got, wantKinds[i])
		}
		hasText := false
		for _, s := range p.Body {
			switch s.Kind {
			case types.BodyText:
				hasText = true
				gotText.WriteString(s.Text)
			case types.BodyImage:
				gotMedia = append(gotMedia, "image")
			case types.BodyVideo:
				gotMedia = append(gotMedia, "video")
			}
			if s.Kind != types.BodyText && s.Media == nil {
				t.Fatalf("第 %d 条的媒体丢了：%+v", i+1, p.Body)
			}
		}
		if !hasText {
			t.Fatalf("第 %d 条没有文字：%+v", i+1, p.Body)
		}
		if i == 0 && p.Quote != "-71" {
			t.Fatalf("引用只该挂第一条：%q", p.Quote)
		}
		if i > 0 && p.Quote != "" {
			t.Fatalf("第 %d 条不该再有引用：%q", i+1, p.Quote)
		}
	}
	if strings.Join(gotMedia, ",") != "image,video" {
		t.Fatalf("媒体顺序变了：%v", gotMedia)
	}
	if gotText.String() != texts[0]+texts[1]+texts[2] {
		t.Fatalf("文字顺序或内容变了：%q", gotText.String())
	}
}

// 只有媒体、没有文字的长消息不拆（拆了只会得到只有媒体的条）；原样一条。
func TestSplitLongMessageMediaOnlyStaysOne(t *testing.T) {
	var body []types.BodySegment
	for range 5 {
		body = append(body, types.BodySegment{Kind: types.BodyImage, Media: types.MemoryMedia("i", "image/png", 1, 1, []byte("png"))})
	}
	parts := splitLongMessage(types.Part{Kind: types.PartMessage, Body: body}, 10)
	if len(parts) != 1 || len(parts[0].Body) != 5 {
		t.Fatalf("只有媒体不该拆：%+v", parts)
	}
}

// 文字版：文字和卡片语义都在，媒体段不在；链接补在最后；引用照带
func TestTextFallbackDropsMediaKeepsText(t *testing.T) {
	picture := types.MemoryMedia("pic", "image/png", 1, 1, []byte{1})
	p := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: "B站视频｜编的标题"},
		{Kind: types.BodyImage, Media: picture, Semantic: "卡片：编的标题"},
		{Kind: types.BodyText, Text: "编的说明"},
	}}
	got, ok := TextFallbacks([]types.Part{p}, nil, "-71", "https://example.invalid/v/1", 0)
	if !ok || len(got) != 1 {
		t.Fatal("有文字，应该能造文字版")
	}
	one := got[0]
	if one.Kind != types.PartMessage || one.Quote != "-71" {
		t.Fatalf("种类或引用不对：%+v", one)
	}
	for _, s := range one.Body {
		if s.Kind != types.BodyText || s.Media != nil {
			t.Fatalf("文字版里不能有媒体段：%+v", one.Body)
		}
	}
	text := one.Body[0].Text
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
	got, ok := TextFallbacks([]types.Part{p}, nil, "-72", "https://example.invalid/x", 0)
	if !ok || len(got) != 1 {
		t.Fatal("节点里有文字，应该能造")
	}
	text := got[0].Body[0].Text
	if strings.Count(text, "https://example.invalid/x") != 1 || !strings.Contains(text, "视频说明") {
		t.Fatalf("链接重复或说明丢了：%q", text)
	}
}

// 只有媒体、没有任何字：不造文字版（也没有链接可补时）
func TestTextFallbackNothingToSay(t *testing.T) {
	p := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage, Media: types.MemoryMedia("pic", "image/png", 1, 1, []byte{1})}}}
	if _, ok := TextFallbacks([]types.Part{p}, nil, "-71", "", 0); ok {
		t.Fatal("只有图、没有字，不该造文字版")
	}
}

// 文字版超过字节上限时拆成多条依次发：第二条起不重复头部，链接只在最后一条，单条文字不超上限（ADR-391）。
func TestTextFallbackSplitsOverByteLimit(t *testing.T) {
	head := "头部｜纸船"
	body := strings.Repeat("字", 500) // 1500 字节
	link := "https://example.invalid/v/1"
	p := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: head + "\n" + body},
		{Kind: types.BodyImage, Media: types.MemoryMedia("pic", "image/png", 1, 1, []byte{1}), Semantic: "图片没发出去"},
	}}
	got, ok := TextFallbacks([]types.Part{p}, nil, "-71", link, 600)
	if !ok || len(got) < 2 {
		t.Fatalf("应拆成多条：%d %v", len(got), ok)
	}
	if got[0].Quote != "-71" {
		t.Fatalf("引用只该挂第一条：%+v", got[0])
	}
	for i, part := range got[1:] {
		if part.Quote != "" {
			t.Fatalf("第 %d 条不该再引用：%q", i+2, part.Quote)
		}
	}
	full := ""
	for i, part := range got {
		n := 0
		for _, s := range part.Body {
			if s.Kind != types.BodyText {
				t.Fatalf("文字版里不能有媒体段：%+v", part.Body)
			}
			n += len(s.Text)
			full += s.Text + "\n"
		}
		if n > 600 {
			t.Fatalf("第 %d 条 %d 字节，超过 600", i+1, n)
		}
		if i > 0 && strings.Contains(part.Body[0].Text, head) {
			t.Fatalf("第 %d 条重复了头部：%q", i+1, part.Body[0].Text)
		}
		if i < len(got)-1 && strings.Contains(part.Body[0].Text, link) {
			t.Fatalf("链接只该在最后一条：第 %d 条", i+1)
		}
	}
	if strings.Count(full, "字") != 500 || !strings.Contains(got[len(got)-1].Body[len(got[len(got)-1].Body)-1].Text, link) {
		t.Fatalf("内容或链接丢了：%q", full)
	}
}
