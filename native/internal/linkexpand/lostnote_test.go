package linkexpand

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// failLoader 按地址决定图下不下得来：err 里有的地址失败，其余成功；视频一律失败。
type failLoader struct {
	fail  map[string]error
	video error
}

func (l failLoader) Image(_ context.Context, _ string, src linkreader.Media) (Loaded, error) {
	if err := l.fail[src.URL.Value]; err != nil {
		return Loaded{}, err
	}
	return Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia("i", "image/png", 1, 1, []byte("png"))}, nil
}
func (l failLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{}, l.video
}
func (failLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, errors.New("编的")
}

var errUnsafe = &linkreader.FetchError{Kind: "unsafe_target", Source: "编的"}

// 用户 2026-11-13（TikTok、Bluesky 视频样张）：媒体因我们自己的限制（下载范围等）全没发出去，只进运行记录会让群里收到
// 一条没图也没说明的消息：整条一个媒体都没有时补一句“图片没取到”“视频没取到”，但不发内部原因。
func TestLostMediaNoteWhenNothingGotThrough(t *testing.T) {
	r := blueskyImages(2)
	d := Build(&r, "-71", Options{}, 0)
	// 两张都因下载范围被拒：补一句，且不写内部原因
	parts := prepareShape(t, r, d, failLoader{fail: map[string]error{goldenImage(1).URL.Value: errUnsafe, goldenImage(2).URL.Value: errUnsafe}})
	got := segText(parts[0].Body)
	if countVisual(parts[0].Body) != 0 || !strings.HasSuffix(got, "图片没取到") || strings.Contains(got, "unsafe") || strings.Contains(got, "允许下载") {
		t.Fatalf("应补一句图片没取到：%q", got)
	}
	// 有一张发出去了：不补
	parts = prepareShape(t, r, d, failLoader{fail: map[string]error{goldenImage(1).URL.Value: errUnsafe}})
	if got := segText(parts[0].Body); countVisual(parts[0].Body) != 1 || strings.Contains(got, "没取到") {
		t.Fatalf("还有图就不补：%q", got)
	}
	// 平台那边的原因（网络不通之类）照旧写成“图片未下载：…”，不另外补
	parts = prepareShape(t, r, d, failLoader{fail: map[string]error{goldenImage(1).URL.Value: errors.New("编的网络错误"), goldenImage(2).URL.Value: errors.New("编的网络错误")}})
	if got := segText(parts[0].Body); !strings.Contains(got, "图片未下载") || strings.Contains(got, "图片没取到") {
		t.Fatalf("非静默原因不重复说明：%q", got)
	}
	// 视频被拒：说视频，不说图片
	rv := siteResult("bluesky", "post", "90042/ccccccccccccc")
	rv.Author.Name, rv.Author.Account = av("纸云"), av("paper.example.invalid")
	rv.Text = av("编的视频帖")
	v := goldenVideo()
	v.Thumbnail = linkreader.Field[string]{} // 没有缩略图顶上，才是“一个媒体都没有”
	rv = withMedia(rv, v)
	rv.Blocks = append([]linkreader.Block{{Type: "text", Text: rv.Text.Value}}, rv.Blocks...)
	dv := Build(&rv, "-71", Options{VideoEnabled: true}, 0)
	parts = prepareShape(t, rv, dv, failLoader{video: errUnsafe})
	if got := segText(parts[0].Body); countVisual(parts[0].Body) != 0 || !strings.HasSuffix(got, "视频没取到") {
		t.Fatalf("视频帖应补一句视频没取到：%q", got)
	}
	// 主人自己关了视频下载：不算失败，不补
	dv = Build(&rv, "-71", Options{}, 0)
	parts = prepareShape(t, rv, dv, failLoader{video: ErrVideoDisabled})
	for _, p := range parts {
		if strings.Contains(segText(p.Body), "没取到") {
			t.Fatalf("主人关了视频不该说没取到：%q", segText(p.Body))
		}
	}
}
