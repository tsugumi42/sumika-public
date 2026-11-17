package main

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

func TestChatMediaRejectsOldMessageAndSource(t *testing.T) {
	t.Parallel()
	m := &msg{id: "one"}
	a := &app{msgs: []*msg{m}, gen: 3, srcGen: 4}
	r := &chatMedia{a: a, message: m, index: 0, gen: 3, sourceGen: 4}
	if !r.valid("one") {
		t.Fatal("current version rejected")
	}
	m.version++
	if r.valid("one") {
		t.Fatal("old version accepted")
	}
	m.version--
	a.msgs[0] = &msg{id: "one"}
	if r.valid("one") {
		t.Fatal("replaced message accepted")
	}
	a.msgs[0] = m
	a.srcGen++
	if r.valid("one") {
		t.Fatal("old source accepted")
	}
	a.srcGen--
	a.gen++
	if r.valid("one") {
		t.Fatal("old session accepted")
	}
	a.gen--
	layer := &forwardLayer{msgs: []*msg{m}}
	a.forward.open = true
	a.forward.stack = []*forwardLayer{layer}
	a.forward.gen = 7
	r.layer = layer
	r.forwardGen = 7
	if !r.valid("one") {
		t.Fatal("current forward rejected")
	}
	a.forward.stack = append(a.forward.stack, &forwardLayer{})
	if r.valid("one") {
		t.Fatal("covered forward accepted")
	}
}

func TestChatMediaImageReportsBitmapAndFailure(t *testing.T) {
	t.Parallel()
	s := seg{Kind: "image", Width: 80, Height: 40, position: 0, Media: &api.MediaRef{Hash: "abc", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}
	bmp := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 8, 4)))
	st := newImageStore(t.Context(), func(fn func()) { fn() })
	st.scale = 1
	k := keyOf(s, 256, false, 1)
	st.m[k] = &imgEntry{state: imgOK, bmp: bmp}
	m := &msg{id: "m", segs: []seg{s}, version: 1}
	a := &app{msgs: []*msg{m}, gen: 1, srcGen: 1}
	r := &chatMedia{a: a, message: m, index: 0, gen: 1, sourceGen: 1, version: 1, images: st}
	got := r.Image("m", 0, false, 256)
	if got.State != chatui.MediaReady || got.Image == nil {
		t.Fatalf("ready: %+v", got)
	}
	st.m[k].state = imgFailed
	got = r.Image("m", 0, false, 256)
	if got.State != chatui.MediaFailed || got.Description == "" {
		t.Fatalf("failed: %+v", got)
	}
	m.version++
	got = r.Image("m", 0, false, 256)
	if got.State != chatui.MediaMissing {
		t.Fatalf("stale: %+v", got)
	}
}

func TestChatMediaFaceFallsBackAndDownloadMapsProgress(t *testing.T) {
	t.Parallel()
	f := &faceStore{table: map[string]faceInfo{
		"1": {Name: "笑", Kind: "emoji", Char: "🙂"},
		"2": {Name: "名字", Kind: "classic"},
	}, imgs: map[string]*faceEntry{"2": {state: faceMissing}}}
	a := &app{faces: f}
	r := &chatMedia{a: a, message: &msg{id: "m"}}
	if got := r.Face("1"); got.State != chatui.MediaMissing || got.Description != "🙂" {
		t.Fatalf("character: %+v", got)
	}
	if got := r.Face("2"); got.State != chatui.MediaMissing || got.Description != "[名字]" {
		t.Fatalf("missing image: %+v", got)
	}
	s := seg{Kind: "video", position: 0, Download: &api.VideoDownloadRef{}}
	m := &msg{id: "m", segs: []seg{s}}
	a.msgs = []*msg{m}
	a.gen, a.srcGen = 1, 1
	r = &chatMedia{a: a, message: m, index: 0, gen: 1, sourceGen: 1, downloads: &downloadStore{items: map[downloadKey]*downloadUI{
		s.downloadKey: {progress: api.VideoDownloadProgress{State: api.VideoManualDownloading, Received: 3, Total: 4}},
	}}}
	got := r.Download("m", 0)
	if got.State != chatui.MediaDownloading || got.Progress != 0.75 {
		t.Fatalf("progress: %+v", got)
	}
}
