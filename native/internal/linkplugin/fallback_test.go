package linkplugin

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 编的样子：一段文字，一段图，一段转发（节点里有视频）。号、链接都是编的
func img(id string) types.BodySegment {
	return types.BodySegment{Kind: types.BodyImage, Media: types.MemoryMedia(id, "image/png", 1, 1, []byte("编的图字节"+id)), Semantic: "卡片：" + id}
}
func txt(s string) types.BodySegment { return types.BodySegment{Kind: types.BodyText, Text: s} }
func msg(quote string, body ...types.BodySegment) types.Part {
	return types.Part{Kind: types.PartMessage, Quote: quote, Body: body}
}
func fwd(nodes ...types.ForwardNode) types.Part {
	return types.Part{Kind: types.PartForward, Nodes: nodes}
}
func node(body ...types.BodySegment) types.ForwardNode {
	return types.ForwardNode{Name: "编的作者", Body: body}
}

const link = "https://example.invalid/v/1"

// 备份：出口发完会释放原资源，备份是独立的拷贝，之后还能再拷出可发的资源；视频不备份，只能退文字
func TestBackupSurvivesReleaseAndVideoHasNone(t *testing.T) {
	orig := msg("", img("a"))
	backup := backupParts([]types.Part{orig})
	if err := orig.Body[0].Media.Release(); err != nil {
		t.Fatal(err)
	}
	cp, res, err := clonePart(backup[0])
	if err != nil || len(res) != 1 {
		t.Fatalf("备份应能再拷出可发的段：%v %d", err, len(res))
	}
	var got string
	_ = cp.Body[0].Media.Read(func(r io.Reader) error { b, _ := io.ReadAll(r); got = string(b); return nil })
	if got != "编的图字节a" {
		t.Fatalf("备份字节不对：%q", got)
	}
	if cp.Body[0].Media == backup[0].Body[0].Media {
		t.Fatal("每次发送要新拷一份，不能把备份本身交给出口去释放")
	}
	video := fwd(node(types.BodySegment{Kind: types.BodyVideo, Media: types.MemoryMedia("v", "video/mp4", 1, 1, []byte{1})}))
	vb := backupParts([]types.Part{video})
	if _, _, err := clonePart(vb[0]); err == nil {
		t.Fatal("视频没有备份，不该能重发")
	}
	if !hasLost(vb[0]) {
		t.Fatal("带视频的段应标没有备份")
	}
}

// 按图记的结论：同一场合同一张图的哈希，到期忘掉；条数封顶
func TestVerdictBookPerImageTTL(t *testing.T) {
	var b verdictBook
	now := time.Unix(1000, 0)
	k1, k2 := "群|aaaa", "群|bbbb"
	b.mark(k1, formDirect, false, now)
	if bad, good := b.get(k1, now); !bad[formDirect] || good != -1 {
		t.Fatal("被拒应记下", bad, good)
	}
	if bad, _ := b.get(k2, now); bad[formDirect] {
		t.Fatal("一张图被拒不该影响别的图")
	}
	b.mark(k1, formForward, true, now)
	if bad, good := b.get(k1, now); good != int(formForward) || !bad[formDirect] {
		t.Fatal("发成过的形式应记下", bad, good)
	}
	if bad, good := b.get(k1, now.Add(verdictTTL)); bad[formDirect] || good != -1 {
		t.Fatal("到期应忘掉")
	}
	for i := range verdictMax + 50 {
		b.mark(string(rune('A'+i%26))+time.Duration(i).String(), formDirect, false, now.Add(time.Duration(i)))
	}
	if len(b.m) > verdictMax {
		t.Fatalf("条数应封顶：%d", len(b.m))
	}
	if bad, _ := b.get("", now); bad[formDirect] {
		t.Fatal("空键不记")
	}
}

// 文字版的原因：写明试过哪几种形式；能力缺失写场合；本地原因另说
func TestFailureNoteWording(t *testing.T) {
	one := msg("", img("a"))
	cases := []struct {
		it    textItem
		scene string
		want  string
	}{
		{textItem{part: one, category: CategoryRejected, tried: []form{formDirect, formForward, formTurned}}, types.SceneGroup, "图没发出去：QQ 拒收了这张图（直接发、合并转发、转 180° 的合并转发都被拒）"},
		{textItem{part: one, category: CategoryRejected}, types.SceneGroup, "图没发出去：QQ 拒收了这张图"},
		{textItem{part: msg("", img("a"), img("b")), category: CategoryRejected, tried: []form{formDirect}}, types.SceneGroup, "图没发出去：QQ 拒收了这组图（直接发都被拒）"},
		{textItem{part: one, category: CategoryUnsupported}, types.SceneGroup, "图没发出去：这个群暂时发不了图"},
		{textItem{part: one, category: CategoryUnsupported}, types.ScenePrivate, "图没发出去：这个会话暂时发不了图"},
		{textItem{part: one, category: CategoryOther}, types.SceneGroup, "图没发出去：发送时出了问题"},
	}
	for _, c := range cases {
		if got := failureNote(c.it, c.scene); got != c.want {
			t.Fatalf("得到 %q，应为 %q", got, c.want)
		}
	}
	if failureNote(textItem{part: msg("", txt("纯文字"))}, types.SceneGroup) != "" {
		t.Fatal("没有媒体就没有说明")
	}
}

func rungsDelivery(rotateOnReject bool) *delivery {
	d := &delivery{p: &Plugin{opt: Options{Now: func() time.Time { return time.Unix(1, 0) }, Logf: func(string, ...any) {}}}}
	d.snap.Settings.Expand.RotateOnReject = rotateOnReject
	return d
}

// 阶梯写死：原样发被拒后只有一级“同一个容器里所有静态图一起转 180°”，之后就是文字版（规格-链接发送改版 A2）
func TestRungsOrderAndMemory(t *testing.T) {
	d := rungsDelivery(true)
	direct, forward := msg("", img("a"), img("b")), fwd(node(img("a")), node(img("b")))
	if got := d.rungs(direct, "", CategoryRejected, 0, false); len(got) != 1 || got[0] != formRotated {
		t.Fatalf("消息段被拒：整条转 180° 直接发一次：%v", got)
	}
	if got := d.rungs(forward, "", CategoryRejected, 0, false); len(got) != 1 || got[0] != formTurned {
		t.Fatalf("转发段被拒：整个转发里转 180° 一次：%v", got)
	}
	d.p.verdicts.mark("k", formRotated, false, d.p.opt.Now())
	if got := d.rungs(direct, "k", CategoryRejected, 0, false); len(got) != 0 {
		t.Fatalf("转图被拒过就直接文字版：%v", got)
	}
	d.p.verdicts.mark("k", formRotated, true, d.p.opt.Now())
	if got := d.rungs(direct, "k", CategoryRejected, 0, false); len(got) != 1 || got[0] != formRotated {
		t.Fatalf("转图发成过就照转图发：%v", got)
	}
}

// 转图帮不上：能力缺失用不转的合并转发（转发段没有可换的）；转图开关关着同样；动图、视频被拒直接文字版
func TestRungsPlainUsesUnturnedForward(t *testing.T) {
	d := rungsDelivery(true)
	direct, forward := msg("", img("a")), fwd(node(img("a")))
	if got := d.rungs(direct, "", CategoryUnsupported, 0, false); len(got) != 1 || got[0] != formForward {
		t.Fatalf("能力缺失的消息段：%v", got)
	}
	if got := d.rungs(forward, "", CategoryUnsupported, 0, false); len(got) != 0 {
		t.Fatalf("能力缺失的转发段：%v", got)
	}
	if got := rungsDelivery(false).rungs(direct, "", CategoryRejected, 0, false); len(got) != 1 || got[0] != formForward {
		t.Fatalf("转图开关关着：%v", got)
	}
	gif := msg("", types.BodySegment{Kind: types.BodyGIF, Media: types.MemoryMedia("g", "image/gif", 1, 1, []byte{1})})
	if got := d.rungs(gif, "", CategoryRejected, 0, false); len(got) != 0 {
		t.Fatalf("动图被拒直接文字版：%v", got)
	}
	if got := d.rungs(gif, "", CategoryUnsupported, 0, false); len(got) != 1 || got[0] != formForward {
		t.Fatalf("动图能力缺失仍可走不转的合并转发：%v", got)
	}
	video := msg("", types.BodySegment{Kind: types.BodyVideo, Media: types.MemoryMedia("v", "video/mp4", 1, 1, []byte{1})})
	if got := d.rungs(video, "", CategoryRejected, 0, false); len(got) != 0 {
		t.Fatalf("视频被拒直接文字版、不转：%v", got)
	}
}

// 网站的发图方式：rotate 起步就转图直接发，forward 起步就包成合并转发并转图；按发图方式起步时转图开关不管
func TestRungsStartByImageMode(t *testing.T) {
	d := rungsDelivery(false)
	direct, forward := msg("", img("a")), fwd(node(img("a")))
	if got := d.rungs(direct, "", CategoryRejected, 1, true); len(got) != 1 || got[0] != formRotated {
		t.Fatalf("rotate 从转图直接发起：%v", got)
	}
	if got := d.rungs(direct, "", CategoryRejected, 2, true); len(got) != 1 || got[0] != formTurned {
		t.Fatalf("forward 起步就是合并转发里转图：%v", got)
	}
	if got := d.rungs(forward, "", CategoryRejected, 1, true); len(got) != 1 || got[0] != formTurned {
		t.Fatalf("本来就是转发的段，rotate 也是转发里转图：%v", got)
	}
}

func TestCanTurnOnlyStaticImages(t *testing.T) {
	gif := types.BodySegment{Kind: types.BodyGIF, Media: types.MemoryMedia("g", "image/gif", 1, 1, []byte{1})}
	if !canTurn(msg("", img("a"))) || canTurn(msg("", gif)) || canTurn(msg("", img("a"), gif)) || canTurn(msg("", txt("字"))) {
		t.Fatal("只有全是静态图才转")
	}
}

// 转 180° 这一级：造出来的是合并转发，节点里的静态图字节变了（卡片是整张渲染图，封面在里面一起转）、说明文字（Semantic）保留；
// 原备份不被改动
func TestBuildTurnedForwardRotatesImagesKeepsSemantic(t *testing.T) {
	d := &delivery{nodeName: "编的作者"}
	pngBytes := func(w, h int) []byte {
		im := image.NewNRGBA(image.Rect(0, 0, w, h))
		im.Set(0, 0, color.NRGBA{R: 255, A: 255})
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	raw := pngBytes(3, 2)
	orig := msg("", types.BodySegment{Kind: types.BodyImage, Media: types.MemoryMedia("card", "image/png", 3, 2, raw), Semantic: "卡片：编的标题"})
	got, err := d.build(orig, formTurned)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != types.PartForward || len(got.Nodes) != 1 || got.Nodes[0].Name != "编的作者" {
		t.Fatalf("转 180° 这一级是合并转发：%+v", got)
	}
	seg := got.Nodes[0].Body[0]
	if seg.Semantic != "卡片：编的标题" {
		t.Fatal("说明文字要保留", seg.Semantic)
	}
	var turned []byte
	_ = seg.Media.Read(func(r io.Reader) error { turned, _ = io.ReadAll(r); return nil })
	if bytes.Equal(turned, raw) {
		t.Fatal("转 180° 的图字节要和原来不同")
	}
	im, err := png.Decode(bytes.NewReader(turned))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := im.At(2, 1).RGBA(); r < 0x8000 {
		t.Fatal("左上角的红点应转到右下角")
	}
	var after []byte
	_ = orig.Body[0].Media.Read(func(r io.Reader) error { after, _ = io.ReadAll(r); return nil })
	if !bytes.Equal(after, raw) {
		t.Fatal("原图不该被改")
	}
	// 解不开的图：这一级没准备成，返回错误（调用方记一行并换下一级）
	bad := msg("", types.BodySegment{Kind: types.BodyImage, Media: types.MemoryMedia("x", "image/png", 1, 1, []byte{1, 2, 3})})
	if _, err := d.build(bad, formTurned); err == nil {
		t.Fatal("坏图转不了，应返回错误")
	}
}
