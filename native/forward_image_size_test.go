package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// 实际转发窗口的第一帧按 DTO 尺寸占位；纯图片不多套壳，图文仍保留正文气泡。
func TestForwardImageFrameDimensions(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
		text bool
	}{
		{"portrait", 300, 800, false}, {"landscape-exif", 800, 300, false}, {"caption", 300, 800, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:90002"}
			ref := &api.MediaRef{Source: api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "image"}}, Hash: tc.name, Forward: []string{"outer"}}
			segments := []api.DisplaySegment{{Kind: "image", Media: ref, Width: tc.w, Height: tc.h}}
			if tc.text {
				segments = append([]api.DisplaySegment{{Kind: "text", Text: "编的说明"}}, segments...)
			}
			req := api.ForwardRequest{Source: ref.Source, ID: "outer"}
			node := api.ForwardNode{UID: "90001", Name: "编的甲", Segments: segments}
			m := forwardNodeMsg(req, node, 0)
			a, q := imgApp(t, &imgNet{files: map[string][]byte{tc.name: facePNG(tc.w, tc.h, red)}})
			p := newProbes(a)
			layer := &forwardLayer{request: req, view: api.ForwardView{ID: "outer", State: "ready", Nodes: []api.ForwardNode{node}}, msgs: []*msg{m}}
			layer.rows = forwardRows(layer.msgs)
			a.forward = forwardViewState{open: true, stack: []*forwardLayer{layer}}
			tt := testerOf(900, 800, func(c *ui.Context) { p.ctx = c; a.forwardWindow(c) })
			before, ok := tt.Find("图片")
			if !ok {
				t.Fatalf("没有图片：%v", tt.Texts())
			}
			if !tc.text && !approx(before.W/before.H, float32(tc.w)/float32(tc.h), .001) {
				t.Fatalf("裸图未保留元数据比例：%+v，应为%d:%d", before, tc.w, tc.h)
			}
			if tc.text {
				caption, exists := tt.Find("编的说明")
				if !exists || caption.Y+caption.H > before.Y+.5 {
					t.Fatalf("图文顺序改变：说明%+v，图片%+v", caption, before)
				}
			}
			h := p.hs[fmt.Sprintf("shell:%s:0", m.id)]
			if (h != nil) != tc.text {
				t.Fatalf("纯图不应套白壳，图文应有壳：text=%v shell=%v", tc.text, h != nil)
			}
			settleImgs(a, q, tt)
			after, _ := tt.Find("图片")
			if after != before {
				t.Fatalf("位图到达后跳变：%+v → %+v", before, after)
			}
			t.Logf("框 %.3f×%.3f，加载后跳变 0，白壳 %v", after.W, after.H, h != nil)
		})
	}
}
