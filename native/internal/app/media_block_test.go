package app

import (
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func picPart() types.Part {
	return types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage, Media: types.MemoryMedia("pic", "image/png", 1, 1, []byte{1})}, {Kind: types.BodyVideo, Media: types.MemoryMedia("v", "video/mp4", 1, 1, []byte{1})}}}
}

func fwdVideoPart() types.Part {
	return types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "外部内容", Body: []types.BodySegment{{Kind: types.BodyGIF, Media: types.MemoryMedia("g", "image/gif", 1, 1, []byte{1})}}}}}
}

// 媒体种类：直接消息和转发节点分开列
func TestPartMediaKinds(t *testing.T) {
	t.Parallel()
	if got := partMediaKinds(picPart()); len(got) != 2 || got[0] != mediaImage || got[1] != mediaVideo {
		t.Fatalf("直接消息的种类：%v", got)
	}
	if got := partMediaKinds(fwdVideoPart()); len(got) != 1 || got[0] != mediaFwdGIF {
		t.Fatalf("转发节点的种类：%v", got)
	}
	if got := partMediaKinds(types.Part{Kind: types.PartMessage, Body: types.TextBody("字")}); len(got) != 0 {
		t.Fatalf("纯文字不该有媒体种类：%v", got)
	}
}

// (b) 没有失败记录时，链接展开拿到的能力就是适配器声明的（ADR-310 默认能发）
func TestNarrowKeepsDeclaredCapsWithoutRecords(t *testing.T) {
	t.Parallel()
	var b mediaBlocks
	all := types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true, ForwardImage: true, ForwardVideo: true, ForwardGIF: true}
	direct, forward := b.narrow(types.SceneGroup, all, t0)
	if !direct.Image || !direct.ImageBase64 || !direct.Video || !direct.GIF || !forward.Image || !forward.Video || !forward.GIF || !forward.ImageBase64 {
		t.Fatalf("没有记录时能力被收窄了：direct=%+v forward=%+v", direct, forward)
	}
}

// (c) 记下某场合的图片后：同场合的图片退掉，其他场合、转发内、其他种类不动；ImageBase64 不动
func TestRecordedComboNarrowsOnlyThatSceneAndKind(t *testing.T) {
	t.Parallel()
	var b mediaBlocks
	all := types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true, ForwardImage: true, ForwardVideo: true, ForwardGIF: true}
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5001"}
	if fresh := b.record(conv, picPart(), t0); len(fresh) != 2 {
		t.Fatalf("图和视频都该记下：%v", fresh)
	}
	if fresh := b.record(conv, picPart(), t0); len(fresh) != 0 {
		t.Fatalf("已经记过的不该再报新：%v", fresh)
	}
	direct, forward := b.narrow(types.SceneGroup, all, t0)
	if direct.Image || direct.Video || !direct.GIF || !direct.ImageBase64 {
		t.Fatalf("群里的直接图片和视频应当退掉、动图不动：%+v", direct)
	}
	if !forward.Image || !forward.Video || !forward.GIF || !forward.ImageBase64 {
		t.Fatalf("转发内的媒体不该跟着退：%+v", forward)
	}
	privateDirect, _ := b.narrow(types.ScenePrivate, all, t0)
	if !privateDirect.Image || !privateDirect.Video {
		t.Fatalf("私聊不该跟着群里的记录退：%+v", privateDirect)
	}
}

// 转发内的动图记下后，只退转发内的动图，直接消息的动图照常
func TestRecordedForwardComboStaysForward(t *testing.T) {
	t.Parallel()
	var b mediaBlocks
	all := types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true, ForwardImage: true, ForwardVideo: true, ForwardGIF: true}
	conv := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: "9001"}
	b.record(conv, fwdVideoPart(), t0)
	direct, forward := b.narrow(types.ScenePrivate, all, t0)
	if !direct.GIF || forward.GIF || !forward.Image {
		t.Fatalf("转发内动图应当单独退掉：direct=%+v forward=%+v", direct, forward)
	}
}

// 能力缺失的降级带有效期：到期后下一条链接再试，不用重启
func TestBlockExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	var b mediaBlocks
	all := types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true}
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5001"}
	b.record(conv, picPart(), t0)
	if d, _ := b.narrow(types.SceneGroup, all, t0.Add(mediaBlockTTL-time.Second)); d.Image {
		t.Fatal("有效期内应当仍退成文字")
	}
	if d, _ := b.narrow(types.SceneGroup, all, t0.Add(mediaBlockTTL)); !d.Image || !d.Video {
		t.Fatal("到期后应当恢复")
	}
	if fresh := b.record(conv, picPart(), t0.Add(mediaBlockTTL+time.Minute)); len(fresh) != 2 {
		t.Fatalf("到期后再次降级应当重新报：%v", fresh)
	}
}

// 回执原因归三类：不支持 / 这一张被拒 / 本地原因
func TestFailureCategory(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		"协议端拒绝：不支持这类消息": "协议端不支持",
		"协议端拒绝":         "协议端拒绝",
		"没连上":           "发送出了问题",
		"没写出去":          "发送出了问题",
	} {
		if got := failureCategory(reason); got != want {
			t.Fatalf("%q 归成 %q，应为 %q", reason, got, want)
		}
	}
}
