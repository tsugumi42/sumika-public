package snowluma

import (
	"testing"

	"sumika-native/internal/channel/types"
)

// ADR-310：QQ 适配器默认声明私聊、群的图片、GIF、视频和合并转发内媒体都能发，不靠真机验过才打开的静态开关。
func TestMediaCapsDeclaredByDefault(t *testing.T) {
	caps := New(nil).Caps()
	for _, scene := range []string{types.SceneGroup, types.ScenePrivate} {
		m := caps.Media(scene)
		if !m.Image || !m.ImageBase64 || !m.Video || !m.GIF || !m.ForwardImage || !m.ForwardVideo || !m.ForwardGIF {
			t.Fatalf("%s 的媒体能力缺项：%+v", scene, m)
		}
	}
}

// 没有可用资源的媒体段不能声称能发：base64 模式下也要拦在出口前，不让 encodeBody 去读空资源
func TestMediaSegmentWithoutResourceIsUnsupported(t *testing.T) {
	caps := New(nil).Caps()
	p := types.Part{Kind: types.PartMessage, Conv: privateConv(), Body: []types.BodySegment{{Kind: types.BodyImage}}}
	if caps.Supports(p) {
		t.Fatal("没有媒体资源的图片段不该被 Caps 收下")
	}
}
