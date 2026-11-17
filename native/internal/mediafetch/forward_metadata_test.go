package mediafetch

import (
	"bytes"
	"context"
	"fmt"
	"image"
	stdpng "image/png"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

// 同一文件经过真 HTTP 下载；转发图、表情、卡片封面和正文遵守同一解码口径。
func TestForwardDownloadMetadata(t *testing.T) {
	var landscape bytes.Buffer
	if err := stdpng.Encode(&landscape, image.NewRGBA(image.Rect(0, 0, 80, 30))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind string
		body       []byte
		w, h       int
		anim       bool
	}{
		{"portrait", "image", metaPNG(t), 30, 80, false},
		{"landscape", "image", landscape.Bytes(), 80, 30, false},
		{"landscape-exif", "image", metaJPEG(t), 80, 30, false},
		{"animated", "emoji", metaGIF(t, 2), 30, 80, true},
		{"cover", "card", metaPNG(t), 30, 80, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.serve("/meta", 200, tc.body)
			tree := types.ForwardTree{ID: "metadata", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: tc.kind, URL: testSegURL("/meta")}}}}}
			if err := e.st.PutForward(context.Background(), tree, 1000, testConv); err != nil {
				t.Fatal(err)
			}
			if err := e.f.OfferForward(tree, testConv); err != nil {
				t.Fatal(err)
			}
			e.start()
			e.idle()
			rows, err := e.st.ForwardMediaRows(context.Background(), tree.ID)
			if err != nil {
				t.Fatal(err)
			}
			m := rows[[2]int{0, 0}]
			if m.State != store.MediaDone || m.Hash != sha(tc.body) || m.Width != tc.w || m.Height != tc.h || m.Animated != tc.anim {
				t.Fatalf("转发下载元数据 %+v，要 %dx%d animated=%v", m, tc.w, tc.h, tc.anim)
			}
			// 同 URL 再登记不能丢元数据；URL 换掉必须清掉旧文件的尺寸。
			registered, err := e.st.RegisterForwardMedia(tree.ID, 0, testConv, store.MediaFetch{Kind: tc.kind, URLSig: store.MediaURLSig(testSegURL("/meta")), State: store.MediaPending})
			if err != nil || registered.Width != tc.w || registered.Height != tc.h {
				t.Fatalf("重复登记丢尺寸：%+v %v", registered, err)
			}
			registered, err = e.st.RegisterForwardMedia(tree.ID, 0, testConv, store.MediaFetch{Kind: tc.kind, URLSig: "changed", State: store.MediaPending})
			if err != nil || registered.Width != 0 || registered.Height != 0 || registered.Animated {
				t.Fatalf("换 URL 留旧尺寸：%+v %v", registered, err)
			}
		})
	}
}

// 超过一页的旧行在启动时自动补扫；缺文件、无效文件头和失败行不会堵住游标或被改成成功。
func TestForwardBackfillMetadata(t *testing.T) {
	e := newEnv(t)
	body := metaGIF(t, 2)
	hash, _, err := mediastore.Put(e.data, "qq", bytes.NewReader(body), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	bad, _, err := mediastore.Put(e.data, "qq", bytes.NewReader([]byte("编的坏文件头")), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := range metaPage + 2 {
		h, state := hash, store.MediaDone
		switch i {
		case 0:
			h = strings.Repeat("a", 64)
		case 1:
			h = bad
		case 2:
			state = store.MediaFailed
		}
		if err := e.st.PutForwardMedia("old", i, testConv, store.MediaFetch{Kind: "image", State: state, Hash: h}); err != nil {
			t.Fatal(err)
		}
	}
	e.start()
	deadline := time.Now().Add(envLimit)
	for {
		rows, err := e.st.ForwardMediaRows(context.Background(), "old")
		if err != nil {
			t.Fatal(err)
		}
		if rows[[2]int{metaPage + 1, 0}].Width == 30 {
			for i := range metaPage + 2 {
				m := rows[[2]int{i, 0}]
				if i < 3 {
					if m.Width != 0 || (i == 2 && m.State != store.MediaFailed) {
						t.Fatalf("不应补的行 %d：%+v", i, m)
					}
				} else if m.Width != 30 || m.Height != 80 || !m.Animated {
					t.Fatalf("旧行 %d 未补齐：%+v", i, m)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("启动补扫没有跨过第一页")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.f.Close()
	if logs := e.allLogs(); logs != "" {
		t.Fatal(fmt.Sprintf("补扫不应刷日志：%s", logs))
	}
}
