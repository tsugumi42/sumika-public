package linkplugin

// 视频放进合并转发以后的两种退回（复核问题 2、3）

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// fileVideo 是一个留着硬链接目录的编的视频（视频退回单独一条时要靠它补发）
func fileVideo(t *testing.T) *types.MediaResource {
	t.Helper()
	vdir := filepath.Join(t.TempDir(), "inst", "v1")
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vdir, "out.mp4")
	if err := os.WriteFile(path, []byte("编的视频字节"), 0o600); err != nil {
		t.Fatal(err)
	}
	vf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	video, err := types.FileMedia("v", "video/mp4", vf, func() error { return os.RemoveAll(vdir) })
	if err != nil {
		t.Fatal(err)
	}
	return video
}

// 带视频的转发没确认、等不到回声：视频拿出来单独补发，剩下的图转 180° 重发（不整段退文字）；
// 不知道是不是视频的问题，不记“这个会话转发里放不了视频”
func TestForwardWithVideoNoEchoSplitsVideoAndTurnsImages(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return ok(fmt.Sprintf("m%d", n))
	}, nil)
	pic := picture(t, 2)
	orig := digest(t, pic)
	fw := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyText, Text: "正文"}, {Kind: types.BodyImage, Media: pic}}},
		{Name: "编的作者", Body: []types.BodySegment{{Kind: types.BodyVideo, Media: fileVideo(t)}}},
	}}
	rep := f.deliver(nil, fw)
	s := f.out.sent()
	if len(s) != 3 || s[1].kind != types.PartForward || s[1].videos != 0 || len(s[1].images) != 1 || s[1].images[0] == orig ||
		s[2].kind != types.PartMessage || s[2].videos != 1 {
		t.Fatalf("转图的转发（不带视频）一次、视频单独一条：%+v", s)
	}
	for _, p := range s {
		if strings.Contains(p.text, "没发出去") {
			t.Fatalf("都送到了，不该有文字版：%+v", s)
		}
	}
	if !rep.delivered || rep.fallback {
		t.Fatalf("都送到了：%+v", rep)
	}
	if !f.r.p.forwardVideoOn(true, g1) {
		t.Fatal("没回声不能当成视频进不了转发")
	}
}

// 转发里同时有引用和视频、回包明确被拒：先当成引用的问题（适配器对带引用的节点一律拒），不记视频的退回；
// 去掉引用按旧排法重发：视频没有备份放不回转发，照旧单独一条
func TestForwardReplyAndVideoRejectedBlamesReplyFirst(t *testing.T) {
	t.Parallel()
	f := newFlow(t, func(n int, p sentPart) echoReply {
		if p.reply {
			return unsupported()
		}
		return ok(fmt.Sprintf("m%d", n))
	}, nil)
	fw := types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "被引", Body: []types.BodySegment{{Kind: types.BodyImage, Media: picture(t, 2)}}},
		{Name: "引用", ReplyTo: 1, Body: []types.BodySegment{{Kind: types.BodyText, Text: "评论"}, {Kind: types.BodyVideo, Media: fileVideo(t)}}},
	}}
	rep := f.deliver(nil, fw)
	s := f.out.sent()
	if len(s) != 3 || s[1].reply || s[1].kind != types.PartForward || s[1].videos != 0 || len(s[1].images) != 1 ||
		s[2].kind != types.PartMessage || s[2].videos != 1 {
		t.Fatalf("去掉引用的转发重发一次、视频单独一条：%+v", s)
	}
	if !rep.delivered {
		t.Fatalf("送到了：%+v", rep)
	}
	if !f.r.p.forwardVideoOn(true, g1) {
		t.Fatal("被拒的是引用，不该记视频进不了转发")
	}
}
