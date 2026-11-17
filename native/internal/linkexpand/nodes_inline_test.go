package linkexpand

import (
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

func inlineImg(i int) Item {
	return Item{Kind: types.BodyImage, Media: linkreader.Media{Type: "image", URL: av(fmt.Sprintf("https://i0.example.invalid/n-%d.png", i))}}
}

// 图文混排（用户 2026-11-14）：文字后面的图留在同一个节点里；视频单独成节点，视频后面接着的文字和图又是新节点。
func TestToNodesKeepsImagesWithText(t *testing.T) {
	video := Item{Kind: types.BodyVideo}
	nodes := toNodes([]Item{text("甲"), text("乙"), inlineImg(1), inlineImg(2), video, text("丙"), inlineImg(3)}, 0)
	var got []string
	for _, n := range nodes {
		got = append(got, itemKinds(n.Body))
	}
	if want := []string{"text,image,image", "video", "text,image"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("节点 %v，想要 %v", got, want)
	}
	if nodes[0].Body[0].Text != "甲\n乙" {
		t.Fatalf("相邻文字接成一段：%q", nodes[0].Body[0].Text)
	}
}

// 一组开头就是图（前面没有文字，比如 pixiv 的“链接一个节点、后面一图一节点”）仍一图一节点；卡片、动图帧等其它项各自成节点。
func TestToNodesImageOnlyGroupStaysOnePerNode(t *testing.T) {
	var items []Item
	for i := 1; i <= 4; i++ {
		items = append(items, inlineImg(i))
	}
	if nodes := toNodes(items, 0); len(nodes) != 4 {
		t.Fatalf("纯图一图一节点：%d", len(nodes))
	}
	card := Item{Kind: "card"}
	nodes := toNodes([]Item{text("文"), card, inlineImg(1)}, 0)
	if len(nodes) != 3 || itemKinds(nodes[1].Body) != "card" {
		t.Fatalf("卡片单独成节点：%d", len(nodes))
	}
}

// 一个节点最多 NodeImageMax 张图，超了另起一个节点接着放（不丢、不重复、顺序不变）。
func TestToNodesImageLimitStartsNewNode(t *testing.T) {
	items := []Item{text("正文")}
	total := NodeImageMax*2 + 3
	for i := 1; i <= total; i++ {
		items = append(items, inlineImg(i))
	}
	nodes := toNodes(items, 0)
	if len(nodes) != 3 {
		t.Fatalf("应是 3 个节点：%d", len(nodes))
	}
	next := 1
	for i, n := range nodes {
		imgs := 0
		for _, it := range n.Body {
			if it.Kind == types.BodyImage {
				imgs++
				if want := fmt.Sprintf("https://i0.example.invalid/n-%d.png", next); it.Media.URL.Value != want {
					t.Fatalf("图顺序乱了：%s，想要 %s", it.Media.URL.Value, want)
				}
				next++
			}
		}
		if imgs > NodeImageMax {
			t.Fatalf("节点 %d 有 %d 张图", i+1, imgs)
		}
	}
	if next != total+1 {
		t.Fatalf("图丢了或重复了：数到 %d", next-1)
	}
}

// 文字超过节点字节上限要切开时，图留在原来的位置：切出来的前几块各占一个节点，最后一块带着后面的图；切点前的图、切点后的图都不丢。
func TestToNodesTextSplitKeepsImagePosition(t *testing.T) {
	long := strings.Repeat("这是一行编的文字，用来凑够字数。\n", 200) // 约一万字节，远超节点上限
	nodes := toNodes([]Item{text("开头"), inlineImg(1), text(long), inlineImg(2), inlineImg(3)}, 300)
	if len(nodes) < 2 {
		t.Fatalf("应切成多个节点：%d", len(nodes))
	}
	if got := itemKinds(nodes[0].Body); !strings.HasPrefix(got, "text,image") {
		t.Fatalf("第一个节点开头是文字和切点前的那张图：%s", got)
	}
	var imgs []string
	for _, n := range nodes {
		bytes := 0
		for _, it := range n.Body {
			switch it.Kind {
			case types.BodyText:
				bytes += len(it.Text)
			case types.BodyImage:
				imgs = append(imgs, it.Media.URL.Value[strings.LastIndex(it.Media.URL.Value, "n-"):])
			}
		}
		if bytes > 300 {
			t.Fatalf("一个节点的文字 %d 字节", bytes)
		}
	}
	if strings.Join(imgs, ",") != "n-1.png,n-2.png,n-3.png" {
		t.Fatalf("图的顺序或数量不对：%v", imgs)
	}
	last := nodes[len(nodes)-1]
	if got := itemKinds(last.Body); got != "text,image,image" {
		t.Fatalf("最后一块文字带着后面两张图：%s", got)
	}
}

// 提示（“发过 N 次”之类）接在头部节点开头那段文字后面，在图前面，不接在图后面。
func TestNoteGoesBeforeInlineImages(t *testing.T) {
	nodes := toNodes([]Item{text("头部"), inlineImg(1)}, 0)
	markNoteEnd(nodes)
	p := Part{Kind: types.PartForward, Nodes: nodes}
	appendToForward(&p, "发过 2 次")
	if got := itemKinds(p.Nodes[0].Body); got != "text,image" || p.Nodes[0].Body[0].Text != "头部\n发过 2 次" {
		t.Fatalf("提示该接在文字后面：%s %q", got, p.Nodes[0].Body[0].Text)
	}
}
