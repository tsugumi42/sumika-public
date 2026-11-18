package mediafetch

import (
	"bytes"
	"io"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
	"testing"
)

func TestCardCoverDownload(t *testing.T) {
	e := newEnv(t)
	content := png("编的封面")
	e.serve("/cover", 200, content)
	segs := []types.Seg{{Kind: types.SegCard, CardTitle: "编的卡片", CardURL: "https://example.com/page", URL: imgSeg("/cover").URL}}
	seq := e.addEvent(segs, "701")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	hash := sha(content)
	if e.segHash(seq, 0) != hash || e.row(seq, 0).State != store.MediaDone {
		t.Fatalf("封面未回填：%s %+v", e.body(seq), e.row(seq, 0))
	}
	f, err := mediastore.OpenMedia(e.data, "qq", hash)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("封面不是下载的文件")
	}
}
