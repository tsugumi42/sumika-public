package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// 第一帧已按实际尺寸留框，位图到达后不跳变，左右没有 4:3 灰底。
func TestChatImageFromDisplayDimensions(t *testing.T) {
	for _, tc := range []struct {
		name, text string
	}{{"图文消息", "编的图片"}, {"纯图片消息", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			key := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:90002"}
			ref := &api.MediaRef{Source: api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "portrait"}}, Hash: "portrait"}
			segments := []api.DisplaySegment{{Kind: "image", Media: ref, Width: 450, Height: 800}}
			if tc.text != "" {
				segments = append([]api.DisplaySegment{{Kind: "text", Text: tc.text}}, segments...)
			}
			m := msgFromView(key, api.MessageView{Ref: ref.Source, Kind: "message", ActorKind: "human", DisplayName: "编的", Segments: segments}, func(string) (character, bool) { return character{}, false })
			n := &imgNet{files: map[string][]byte{"portrait": facePNG(450, 800, red)}}
			a, q := imgApp(t, n, m)
			tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
			before, ok := tt.Find("图片")
			if !ok || !approx(before.W/before.H, 450.0/800, .001) {
				t.Fatalf("加载前框%+v，没有采用投影中的450:800", before)
			}
			settleImgs(a, q, tt)
			after, _ := tt.Find("图片")
			if before != after {
				t.Fatalf("图片到达后框变了：%+v → %+v", before, after)
			}
			frame := tt.Image()
			for _, dx := range []float32{3, after.W / 2, after.W - 3} {
				if pixel := frame.RGBAAt(int(after.X+dx), int(after.Y+after.H/2)); !near(pixel, opaqueRed, 40) {
					t.Fatalf("图中线 x=%.1f 是 %v，应为图片红色", dx, pixel)
				}
			}
			t.Logf("加载前后框%.3f×%.3f，左右像素均为图片色", after.W, after.H)
		})
	}
}

// 生产 DTO 的动图要读取原件并解出多帧，不依赖显示档位。
func TestDisplayAnimatedImageLoadsOriginal(t *testing.T) {
	key := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:90002"}
	ref := &api.MediaRef{Source: api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "animated"}}, Hash: "animated"}
	d := api.DisplaySegment{Kind: "image", Media: ref, Width: 30, Height: 80, Animated: true}
	m := msgFromView(key, api.MessageView{Ref: ref.Source, Kind: "message", ActorKind: "human", Segments: []api.DisplaySegment{d}}, func(string) (character, bool) { return character{}, false })
	n := &imgNet{files: map[string][]byte{"animated": gifSample(t, 30, 80, []colorN{cRed, cGreen}, []int{10, 10}, 0)}}
	a, q := imgApp(t, n, m)
	tt := testerOf(900, 700, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	if tiers := n.tiers("animated"); len(tiers) != 1 || tiers[0] != 0 {
		t.Fatalf("动图应取原文件 tier=0：%v", tiers)
	}
	e := animationEntry(t, a, "qq/animated")
	if e.anim == nil || len(e.anim.frames) != 2 {
		t.Fatalf("没有解成两帧动图：%+v", e)
	}
}
