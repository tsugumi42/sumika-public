package mediafetch

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	stdpng "image/png"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

func metaPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := stdpng.Encode(&b, image.NewRGBA(image.Rect(0, 0, 30, 80))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func metaGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{}
	for range frames {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 30, 80), color.Palette{color.Black, color.White}))
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func metaJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 30, 80)), nil); err != nil {
		t.Fatal(err)
	}
	// APP1：小端 TIFF，IFD0 的方向 SHORT=6。
	exif := []byte("Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	head := []byte{0xff, 0xe1, 0, byte(len(exif) + 2)}
	out := append([]byte{}, b.Bytes()[:2]...)
	out = append(out, head...)
	out = append(out, exif...)
	return append(out, b.Bytes()[2:]...)
}

// 真 HTTP 下载和落盘，断言事务提交后的元数据；单帧 GIF 不能误当动图。
func TestDownloadImageMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		w, h int
		anim bool
	}{
		{"png", metaPNG(t), 30, 80, false},
		{"gif-two", metaGIF(t, 2), 30, 80, true},
		{"gif-one", metaGIF(t, 1), 30, 80, false},
		{"jpeg-exif6", metaJPEG(t), 80, 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.serve("/meta", 200, tc.body)
			segs := []types.Seg{{Kind: types.SegImage, URL: testSegURL("/meta")}}
			seq := e.addEvent(segs, "")
			e.f.Offer(seq, segs)
			e.f.Start()
			ctx, cancel := context.WithTimeout(context.Background(), envLimit)
			defer cancel()
			if err := e.f.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			rows, err := e.st.MediaFetchRows(seq)
			if err != nil {
				t.Fatal(err)
			}
			m := rows[0]
			if m.State != store.MediaDone || m.Hash != sha(tc.body) || m.Width != tc.w || m.Height != tc.h || m.Animated != tc.anim {
				t.Fatalf("下载元数据 %+v，要 %dx%d animated=%v", m, tc.w, tc.h, tc.anim)
			}
		})
	}
}

// 旧 done 行启动后补齐；缺文件排在前面也不阻塞后续，结束后不再刷日志。
func TestBackfillImageMetadata(t *testing.T) {
	e := newEnv(t)
	missing := e.addEvent([]types.Seg{{Kind: types.SegImage, Hash: strings.Repeat("a", 64)}}, "")
	if err := e.st.PutMediaFetch(store.MediaFetch{EventSeq: missing, Kind: "image", State: store.MediaDone, Hash: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	body := metaGIF(t, 2)
	hash, _, err := mediastore.Put(e.data, "qq", bytes.NewReader(body), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	seq := e.addEvent([]types.Seg{{Kind: types.SegImage, Hash: hash}}, "")
	if err := e.st.PutMediaFetch(store.MediaFetch{EventSeq: seq, Kind: "image", State: store.MediaDone, Hash: hash}); err != nil {
		t.Fatal(err)
	}
	e.f.Start()
	deadline := time.After(envLimit)
	for {
		rows, err := e.st.MediaFetchRows(seq)
		if err != nil {
			t.Fatal(err)
		}
		if m := rows[0]; m.Width == 30 && m.Height == 80 && m.Animated {
			break
		}
		select {
		case <-deadline:
			t.Fatal("后台没补上旧 done 行")
		case <-time.After(5 * time.Millisecond):
		}
	}
	e.f.Close()
	rows, err := e.st.MediaFetchRows(missing)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Width != 0 || rows[0].State != store.MediaDone || e.allLogs() != "" {
		t.Fatalf("缺文件应跳过，行=%+v 日志=%s", rows, e.allLogs())
	}
}
