package linkexpand

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 视频下载是主人自己在设置里关的：消息里不该多一句“视频未下载：已关闭”。
// 能力不支持这种真的发不出去的情况，文字照常写。
func TestVideoOffAddsNoTextToMessage(t *testing.T) {
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: mallTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	item := Item{Kind: types.BodyVideo}
	d := Draft{Parts: []Part{{Kind: types.PartMessage, Body: []Item{{Kind: types.BodyText, Text: "正文"}, item}}}}
	run := func(caps types.MediaCaps) string {
		p := Preparer{Media: NewMedia(f, t.TempDir()), Direct: caps, Limits: VideoLimits{Enabled: false},
			Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
		prepared, err := p.Prepare(context.Background(), &linkreader.Result{}, &d)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Release()
		var text strings.Builder
		for _, part := range prepared.Parts {
			for _, seg := range part.Body {
				if seg.Kind == types.BodyText {
					text.WriteString(seg.Text)
				}
			}
		}
		return text.String()
	}
	if got := run(types.MediaCaps{Image: true, Video: true}); strings.Contains(got, "未下载") || !strings.Contains(got, "正文") {
		t.Fatalf("主人关了视频下载，消息里只该有正文：%q", got)
	}
	if got := run(types.MediaCaps{Image: true}); !strings.Contains(got, "视频未下载") {
		t.Fatalf("场合不支持视频时要写明原因：%q", got)
	}
}
