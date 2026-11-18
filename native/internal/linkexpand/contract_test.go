package linkexpand

// 接上链接发送改版 A 线的契约字段（规格-链接发送改版 B2 的 QuoteOf、B3 的视频节点、prepare.go 的 ReplyTo 映射）。数据都是编的

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 有引用的推文进合并转发：被引推文的节点在最前面（用户 2026-11-12），主推文标着它；被引推文不可用时不标
func TestQuotedTweetNodeMarksQuoteOf(t *testing.T) {
	r := caseNamed(t, "x-quote")
	d := build(r, Options{})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward || len(d.Parts[0].Nodes) != 2 {
		t.Fatalf("应是一个两节点的转发：%v", kinds(d))
	}
	nodes := d.Parts[0].Nodes
	if nodes[0].QuoteOf != 0 || nodes[1].QuoteOf != 1 || !strings.Contains(itemsTextOf(nodes[1]), r.CleanURL) || strings.Contains(itemsTextOf(nodes[0]), r.CleanURL) {
		t.Fatalf("被引推文在第 1 个节点、主推文在第 2 个并指向它：%d %d", nodes[0].QuoteOf, nodes[1].QuoteOf)
	}
	gone := caseNamed(t, "x-quote")
	gone.Quote = linkreader.Field[*linkreader.Result]{}
	gone.Blocks = []linkreader.Block{{Type: "text", Text: "短帖正文"}, {Type: "quote_unavailable", Reason: "deleted"}}
	d = build(gone, Options{Sites: map[string]SiteSetting{"x": {Forward: "always"}}})
	for _, n := range d.Parts[0].Nodes {
		if n.QuoteOf != 0 {
			t.Fatalf("被引推文不可用时不该标引用：%+v", d.Parts[0].Nodes)
		}
	}
}

// 不带引用的推文、直发的推文都不标
func TestNoQuoteOfWithoutQuote(t *testing.T) {
	for _, name := range []string{"x-short-image", "x-long"} {
		for _, p := range build(caseNamed(t, name), Options{}).Parts {
			for _, n := range p.Nodes {
				if n.QuoteOf != 0 {
					t.Fatalf("%s 不该有引用：%+v", name, p.Nodes)
				}
			}
		}
	}
}

// KeepNodes：删节点后引用跟着挪；被引的节点删掉了就清成 0
func TestKeepNodesRemapsQuoteOf(t *testing.T) {
	nodes := []Node{{Name: "主", QuoteOf: 4}, {Name: "图"}, {Name: "空"}, {Name: "被引"}}
	got := KeepNodes(nodes, func(i int) bool { return i != 2 })
	if len(got) != 3 || got[0].QuoteOf != 3 || got[2].Name != "被引" {
		t.Fatalf("删掉中间一个后应指向第 3 个：%+v", got)
	}
	got = KeepNodes(nodes, func(i int) bool { return i != 3 })
	if got[0].QuoteOf != 0 {
		t.Fatalf("被引的节点没了，引用应清掉：%+v", got)
	}
	// 越界的旧值（不该出现）也清掉，不指到别处
	got = KeepNodes([]Node{{QuoteOf: 9}}, func(int) bool { return true })
	if got[0].QuoteOf != 0 {
		t.Fatalf("越界引用应清掉：%+v", got)
	}
}

func xVideoForward(t *testing.T) linkreader.Result {
	r := xImages(4)
	v := goldenVideo()
	r.Media.Value = append(r.Media.Value, v)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "video", Media: &v})
	return r
}

// 视频进转发开着（生产默认）：视频是转发里的一个节点；关着（或这个会话退回过）：视频拿出来紧跟在转发后面单独一条
func TestForwardVideoOffSplitsVideos(t *testing.T) {
	on := build(xVideoForward(t), Options{ForwardVideo: true})
	if len(on.Parts) != 1 || on.Parts[0].Kind != types.PartForward {
		t.Fatalf("开着时只有一个转发：%v", kinds(on))
	}
	videoNodes := 0
	for _, n := range on.Parts[0].Nodes {
		for _, it := range n.Body {
			if it.Kind == types.BodyVideo {
				videoNodes++
			}
		}
	}
	if videoNodes != 1 {
		t.Fatalf("开着时视频在转发节点里：%d", videoNodes)
	}
	off := build(xVideoForward(t), Options{})
	if len(off.Parts) != 2 || off.Parts[0].Kind != types.PartForward || off.Parts[1].Kind != types.PartMessage {
		t.Fatalf("关着时应是转发 + 一条视频：%v", kinds(off))
	}
	if len(off.Parts[1].Body) != 1 || off.Parts[1].Body[0].Kind != types.BodyVideo || off.Parts[1].Body[0].Standalone {
		t.Fatalf("后面那条只有视频（不标 Standalone，免得 Prepare 挪到最后）：%+v", off.Parts[1].Body)
	}
	if len(off.Parts[0].Nodes) != len(on.Parts[0].Nodes)-1 {
		t.Fatalf("转发里少的正好是视频那个节点：%d / %d", len(off.Parts[0].Nodes), len(on.Parts[0].Nodes))
	}
	for _, n := range off.Parts[0].Nodes {
		for _, it := range n.Body {
			if it.Kind == types.BodyVideo {
				t.Fatal("关着时转发里不该还有视频")
			}
		}
	}
}

// 带视频的引用推文关掉视频进转发：拆出视频后引用仍指向被引推文的节点
func TestForwardVideoSplitKeepsQuoteOf(t *testing.T) {
	r := caseNamed(t, "x-quote")
	v := goldenVideo()
	r.Media = av([]linkreader.Media{v})
	r.Blocks = []linkreader.Block{{Type: "text", Text: "短帖正文"}, {Type: "video", Media: &v}, r.Blocks[1]}
	d := build(r, Options{})
	if len(d.Parts) != 2 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("应是转发 + 一条视频：%v", kinds(d))
	}
	nodes := d.Parts[0].Nodes
	main := -1
	for i, n := range nodes {
		if strings.Contains(itemsTextOf(n), "短帖正文") {
			main = i
		}
	}
	if main < 1 || nodes[main].QuoteOf != 1 || itemsTextOf(nodes[0]) == "" || strings.Contains(itemsTextOf(nodes[0]), "短帖正文") {
		t.Fatalf("被引推文在最前面、拆出视频后主推文仍指向它：%+v", nodes)
	}
}

func itemsTextOf(n Node) string {
	s := ""
	for _, it := range n.Body {
		if it.Kind == types.BodyText {
			s += it.Text
		}
	}
	return s
}

// videoOffLoader 的视频一律“主人关了”：Prepare 不写说明，只有这个视频的节点准备完是空的
type videoOffLoader struct{}

func (videoOffLoader) Image(context.Context, string, linkreader.Media) (Loaded, error) {
	return Loaded{}, errors.New("编的：不下图")
}
func (videoOffLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{}, ErrVideoDisabled
}
func (videoOffLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, errors.New("编的")
}

// Prepare 把 QuoteOf 映射成 ForwardNode.ReplyTo；中间有节点准备完是空的（被丢掉）时按留下的节点重新对下标
func TestPrepareMapsQuoteOfToReplyTo(t *testing.T) {
	caps := types.MediaCaps{Image: true, ImageBase64: true, ForwardImage: true, Video: true, ForwardVideo: true}
	p := &Preparer{Media: videoOffLoader{}, Direct: caps, Forward: caps, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
	r := linkreader.Result{}
	d := Draft{Parts: []Part{{Kind: types.PartForward, Nodes: []Node{
		{Name: "主", QuoteOf: 3, Body: []Item{text("主推文")}},
		{Name: "视频", Body: []Item{{Kind: types.BodyVideo, Media: goldenVideo()}}},
		{Name: "被引", Body: []Item{text("被引推文")}},
	}}}}
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(prepared.Parts) != 1 || len(prepared.Parts[0].Nodes) != 2 {
		t.Fatalf("视频节点准备完是空的，应丢掉：%+v", prepared.Parts)
	}
	n := prepared.Parts[0].Nodes
	if n[0].ReplyTo != 2 || n[1].ReplyTo != 0 {
		t.Fatalf("丢掉一个节点后引用应指向第 2 个：%d %d", n[0].ReplyTo, n[1].ReplyTo)
	}
	// 没有引用时 ReplyTo 是零值（旧行为）
	d = Draft{Parts: []Part{{Kind: types.PartForward, Nodes: []Node{{Name: "甲", Body: []Item{text("一")}}, {Name: "乙", Body: []Item{text("二")}}}}}}
	pr, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Release()
	for _, x := range pr.Parts[0].Nodes {
		if x.ReplyTo != 0 {
			t.Fatalf("没引用不该有 ReplyTo：%+v", pr.Parts[0].Nodes)
		}
	}
}
