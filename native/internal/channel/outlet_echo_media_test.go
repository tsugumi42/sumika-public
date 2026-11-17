package channel

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 带图的回声认领和等回声（规格-链接发送改版 A1）。号、群、文字、图都是编的。

var mediaCaps = types.Caps{ForwardGroup: true, MediaGroup: types.MediaCaps{Image: true, ImageBase64: true, GIF: true, ForwardImage: true}}

// unguardedLinks 照宿主的装配放过链接插件的来源（它发的是网页原文，不过记忆守卫）
func unguardedLinks(c *OutletConfig) {
	c.UnguardedOrigins = map[string]bool{"automation:link-reader": true}
}

func pic(b byte) *types.MediaResource {
	return types.MemoryMedia("img", "image/png", 1, 1, []byte{b, b, b})
}

func md5Of(b byte) string {
	s := md5.Sum([]byte{b, b, b})
	return hex.EncodeToString(s[:])
}

// sendPics 让角色 ca 在群 g1 里发一段“文字 + 几张图”，返回计划号
func (h *oh) sendPics(text string, bytes ...byte) string {
	h.t.Helper()
	body := []types.BodySegment{{Kind: types.BodyText, Text: text}}
	var res []*types.MediaResource
	for _, b := range bytes {
		m := pic(b)
		res = append(res, m)
		body = append(body, types.BodySegment{Kind: types.BodyImage, Media: m})
	}
	id, err := h.out.SendOwned(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{{Kind: types.PartMessage, Body: body}}, res)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// picEcho 是一个带图的回声：files 是每张图在协议端的文件名（读得出 md5 的写 md5，读不出的写不透明的号）
func picEcho(platformID, text string, files ...string) types.Inbound {
	in := echoOf(acctA, group("g1"), platformID, text)
	for _, f := range files {
		in.Segs = append(in.Segs, types.Seg{Kind: types.SegImage, File: f})
	}
	return in
}

// 带图回声按图数和 md5 认领；入库一站不再把它存成“她的号上别人发的”一条
func TestEchoWithImagesIsClaimed(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.withIntake()
	h.unknownReceipts()
	id := h.sendPics("看图", 1, 2)
	h.waitPlan(id, store.PlanUnknown)
	before := countRows(t, h.st, `SELECT count(*) FROM events`)

	h.push(picEcho("7001", "看图", md5Of(1)+".png", "opaque-file-id"))
	h.waitPlan(id, store.PlanDone)
	if pt := h.plan(id).Parts[0]; pt.Via != types.ViaEcho || pt.PlatformID != "7001" {
		t.Fatalf("带图的回声该认领：%+v", pt)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events`); n != before {
		t.Fatalf("认领了就不该多存一条：%d → %d", before, n)
	}
	if n := countRows(t, h.st, `SELECT count(*) FROM events WHERE origin = 'human'`); n != 0 {
		t.Fatalf("带图回声被存成了 origin = human：%d", n)
	}
	n, ok := h.out.AwaitPart(context.Background(), "automation:link-reader", id, 0, 0)
	if !ok || n.PlatformID != "7001" || n.MediaMissing || n.Account != acctA {
		t.Fatalf("认领后立刻能读到回声：%+v %v", n, ok)
	}
	if _, ok := h.out.AwaitPart(context.Background(), "automation:别的插件", id, 0, 0); ok {
		t.Fatal("别的来源读不到这一段的回声")
	}
}

// 回声里图的标识读得出却对不上（别的图）不认；图比段多不认；视频数不一样不认
func TestEchoImageIDMismatchNotClaimed(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	id := h.sendPics("看图", 1)
	h.waitPlan(id, store.PlanUnknown)
	forged := "ffffffffffffffffffffffffffffffff"
	if h.claim(picEcho("7001", "看图", forged+".png")) {
		t.Fatal("标识读得出却不是我们发的图，不认")
	}
	if h.claim(picEcho("7002", "看图", md5Of(1)+".png", md5Of(1)+".png")) {
		t.Fatal("回声的图比段多，不认")
	}
	v := picEcho("7003", "看图", md5Of(1)+".png")
	v.Segs = append(v.Segs, types.Seg{Kind: types.SegVideo})
	if h.claim(v) {
		t.Fatal("段没有视频，回声有，不认")
	}
	e := picEcho("7004", "看图", md5Of(1)+".png")
	e.Segs = append(e.Segs, types.Seg{Kind: types.SegEmoji, Ref: "1"})
	if h.claim(e) {
		t.Fatal("带表情的回声不认")
	}
	ctl := picEcho("7005", "看图\u0000‮", md5Of(1)+".png")
	if h.claim(ctl) {
		t.Fatal("文字里塞了控制字符，规范化后对不上，不认")
	}
	if !h.claim(picEcho("7006", "看图", md5Of(1)+".png")) {
		t.Fatal("对得上的才认")
	}
}

// 同一次发的两段、同样的文字：回声的图对上哪段认哪段，先发先认，每段只认一次，不串
func TestEchoForOtherPartNotClaimed(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	a := h.sendPics("同样的字", 1)
	h.waitPlan(a, store.PlanUnknown)
	b := h.sendPics("同样的字", 2)
	h.waitPlan(b, store.PlanUnknown)
	if !h.claim(picEcho("7001", "同样的字", md5Of(2)+".png")) {
		t.Fatal("第二段的回声该认领第二段")
	}
	if pa, pb := h.plan(a).Parts[0], h.plan(b).Parts[0]; pa.State != store.PartUnknown || pb.PlatformID != "7001" {
		t.Fatalf("回声认到了别的段：%+v / %+v", pa, pb)
	}
	if h.claim(picEcho("7002", "同样的字", md5Of(2)+".png")) {
		t.Fatal("第二段已经认过，第一段的图对不上，不认")
	}
	if !h.claim(picEcho("7003", "同样的字", md5Of(1)+".png")) {
		t.Fatal("第一段自己的回声认第一段")
	}
}

// 回声只剩文字（0 张图）或少了一部分图：照样认领，记成吞图
func TestEchoTextOnlyFlagsMediaMissing(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	a := h.sendPics("被吞了", 1, 2)
	h.waitPlan(a, store.PlanUnknown)
	if !h.claim(picEcho("7001", "被吞了")) {
		t.Fatal("只剩文字的回声也该认领")
	}
	if n, ok := h.out.AwaitPart(context.Background(), "", a, 0, 0); !ok || !n.MediaMissing {
		t.Fatalf("只剩文字要记成吞图：%+v %v", n, ok)
	}
	b := h.sendPics("吞一半", 3, 4)
	h.waitPlan(b, store.PlanUnknown)
	if !h.claim(picEcho("7002", "吞一半", md5Of(3)+".png")) {
		t.Fatal("少了一部分图的回声也该认领")
	}
	if n, ok := h.out.AwaitPart(context.Background(), "", b, 0, 0); !ok || !n.MediaMissing {
		t.Fatalf("少一部分也按吞图：%+v %v", n, ok)
	}
	c := h.sendPics("完整", 5)
	h.waitPlan(c, store.PlanUnknown)
	h.claim(picEcho("7003", "完整", "opaque"))
	if n, ok := h.out.AwaitPart(context.Background(), "", c, 0, 0); !ok || n.MediaMissing {
		t.Fatalf("图数对上不是吞图：%+v %v", n, ok)
	}
}

// 带图的合并转发：回声恰有一个转发段就认（不比节点正文）
func TestEchoForwardWithImagesIsClaimed(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	m := pic(9)
	part := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "节点", Body: []types.BodySegment{{Kind: types.BodyImage, Media: m}}}}}
	id, err := h.out.SendOwned(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{part}, []*types.MediaResource{m})
	if err != nil {
		t.Fatal(err)
	}
	h.waitPlan(id, store.PlanUnknown)
	fw := echoOf(acctA, group("g1"), "7001", "")
	fw.Segs = []types.Seg{{Kind: types.SegForward, Ref: "res"}}
	if !h.claim(fw) {
		t.Fatal("带图的合并转发的回声该认领")
	}
}

// 等回声：在等的时候回声到了立刻醒；到点没到返回 false，之后回声再到是迟到，交给回调时 Late 为真；没等过的回声不算迟到
func TestAwaitPartWakesAndMarksLate(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	h.unknownReceipts()
	got := make(chan EchoClaimed, 4)
	h.out.onClaim("automation:link-reader", func(c EchoClaimed) { got <- c })

	a := h.sendPics("第一张", 1)
	h.waitPlan(a, store.PlanUnknown)
	done := make(chan EchoNote, 1)
	go func() {
		n, ok := h.out.AwaitPart(context.Background(), "automation:link-reader", a, 0, 2*time.Second)
		if !ok {
			n = EchoNote{}
		}
		done <- n
	}()
	h.clk.WaitTimer(t, 2*time.Second)
	h.clk.Advance(time.Second)
	h.claim(picEcho("7001", "第一张", md5Of(1)+".png"))
	if n := <-done; n.PlatformID != "7001" || n.LatencyMS != 1000 {
		t.Fatalf("在等的时候回声到了要立刻醒，带上延迟：%+v", n)
	}
	if c := <-got; c.Late || c.PlanID != a {
		t.Fatalf("等到了的回声不是迟到：%+v", c)
	}

	b := h.sendPics("第二张", 2)
	h.waitPlan(b, store.PlanUnknown)
	go func() {
		_, ok := h.out.AwaitPart(context.Background(), "automation:link-reader", b, 0, 2*time.Second)
		if ok {
			done <- EchoNote{PlatformID: "不该等到"}
		} else {
			done <- EchoNote{}
		}
	}()
	h.clk.WaitTimer(t, 2*time.Second)
	h.clk.Advance(2 * time.Second)
	if n := <-done; n.PlatformID != "" {
		t.Fatal("到点没回声就是没等到")
	}
	h.clk.Advance(3 * time.Second)
	h.claim(picEcho("7002", "第二张", md5Of(2)+".png"))
	if c := <-got; !c.Late || c.PlanID != b || c.Note.LatencyMS != 5000 {
		t.Fatalf("等过又放弃以后才到的回声是迟到：%+v", c)
	}
}
