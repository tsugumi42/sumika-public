package linkplugin

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

func forwardResult() *linkreader.Result {
	av := func(s string) linkreader.Field[string] { return linkreader.Field[string]{State: "available", Value: s} }
	r := &linkreader.Result{Platform: "bilibili", Kind: "dynamic", Title: av("编的标题"), Text: av("编的正文")}
	r.Author.Name = av("编的作者")
	return r
}

// 降级路径包出来的合并转发也带节点名设置和卡片文字（没写节点名：用 QQ 昵称，退路是作者名）
func TestAsForwardUsesForwardSettings(t *testing.T) {
	d := &delivery{nodeName: "编的作者", result: forwardResult()}
	got := d.asForward(msg("", txt("正文")))
	if n := got.Nodes[0]; n.Name != "编的作者" || !n.Self || got.Card != nil {
		t.Fatalf("没设置：节点要用昵称、退路作者名、没有卡片：%+v", got)
	}
	d.fwd = linkexpand.ForwardOptions{NodeName: "编的节点", Source: "$标题", News: []string{"$作者"}}
	got = d.asForward(msg("", txt("正文")))
	if n := got.Nodes[0]; n.Name != "编的节点" || n.Self {
		t.Fatalf("设置了节点名要写死：%+v", n)
	}
	if got.Card == nil || got.Card.Source != "编的标题" || len(got.Card.News) != 1 || got.Card.News[0] != "编的作者" {
		t.Fatalf("卡片文字没带上：%+v", got.Card)
	}
	// 本来就是转发段的原样返回，不改它的名字和卡片
	orig := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "原名", Self: true, Body: types.TextBody("甲")}}, Card: &types.ForwardCard{Source: "原卡片"}}
	if g := d.asForward(orig); g.Nodes[0].Name != "原名" || g.Card.Source != "原卡片" {
		t.Fatalf("转发段应原样返回：%+v", g)
	}
}

// 转 180° 重建、备份和克隆，卡片、“用昵称”和节点的引用（ReplyTo）都不能半路丢
func TestForwardCardAndSelfSurviveRotateBackupClone(t *testing.T) {
	card := &types.ForwardCard{Source: "编的标题"}
	orig := types.Part{Kind: types.PartForward, Card: card, Nodes: []types.ForwardNode{
		{Name: "甲", Self: true, Body: []types.BodySegment{txt("文字")}},
		{Name: "乙", Self: true, ReplyTo: 1, Body: []types.BodySegment{realImg(t, "1"), realImg(t, "2")}},
	}}
	d := &delivery{}
	turned, err := d.build(orig, formTurned)
	if err != nil {
		t.Fatal(err)
	}
	if turned.Card != card || !turned.Nodes[0].Self || !turned.Nodes[1].Self || turned.Nodes[1].ReplyTo != 1 || len(turned.Nodes[1].Body) != 2 {
		t.Fatalf("转 180° 重建丢了卡片、Self 或引用，或者把节点拆开了：%+v", turned)
	}
	for _, p := range backupParts([]types.Part{orig}) {
		if p.Card != card || !p.Nodes[0].Self || !p.Nodes[1].Self || p.Nodes[1].ReplyTo != 1 {
			t.Fatalf("备份丢了卡片、Self 或引用：%+v", p)
		}
	}
	cp, res, err := clonePart(orig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, r := range res {
			_ = r.Release()
		}
	}()
	if cp.Card != card || !cp.Nodes[0].Self || !cp.Nodes[1].Self || cp.Nodes[1].ReplyTo != 1 {
		t.Fatalf("克隆丢了卡片、Self 或引用：%+v", cp)
	}
}

func realImg(t *testing.T, id string) types.BodySegment {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return types.BodySegment{Kind: types.BodyImage, Media: types.MemoryMedia(id, "image/png", 2, 2, b.Bytes()), Semantic: "卡片：" + id}
}
