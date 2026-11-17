package linkexpand

import (
	"context"
	"image/color"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

type mallTransport func(*http.Request) (*http.Response, error)

func (rt mallTransport) RoundTrip(req *http.Request) (*http.Response, error) { return rt(req) }

func mallResult(t *testing.T) linkreader.Result {
	t.Helper()
	b, err := os.ReadFile("../linkreader/testdata/mall-item.json")
	if err != nil {
		t.Fatal(err)
	}
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: mallTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	l, ok := linkreader.Recognize("https://mall.bilibili.com/detail.html?itemsId=70001&share_mid=fictional-sharer&from=fictional-from")
	if !ok {
		t.Fatal("商品链接未识别")
	}
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMallExpansion(t *testing.T) {
	r := mallResult(t)
	for _, format := range []string{"card", "cover-text"} {
		{
			t.Run(format, func(t *testing.T) {
				d := Build(&r, "-71", goldenOptions(format), 2)
				if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || d.Parts[0].Quote != "-71" {
					t.Fatalf("商品应为一次消息：%+v", d)
				}
				semantic := d.Semantic()
				for _, expected := range []string{"纸舟工房 星灯摆件", "325.50～499.90", "状态：预售", "店铺：纸舟商店", "定金：¥48.75～74.98", "10-22 19:55", "2027-05", "发过 2 次", r.CleanURL} {
					if !strings.Contains(semantic, expected) {
						t.Fatalf("丢字段 %q: %s", expected, semantic)
					}
				}
				if strings.Contains(semantic, "share_mid") || strings.Contains(semantic, "fictional-sharer") || strings.Contains(semantic, "UP主") {
					t.Fatal("商品卡片污染", semantic)
				}
				body := d.Parts[0].Body
				// B 站没有分级字段：开不开"允许敏感内容"都照常带封面（ADR-309）
				if format == "card" {
					if body[0].Kind != "card" || body[0].Media.URL.Value != "https://i0.hdslb.com/bfs/mall/fictional-first.png" || body[1].Text != r.CleanURL {
						t.Fatalf("卡片段：%+v", body)
					}
				} else if len(body) != 4 || body[0].Kind != types.BodyText || body[1].Kind != types.BodyImage || body[2].Text != r.CleanURL {
					t.Fatalf("文字/首图/链接原序：%+v", body)
				}
			})
		}
	}
}

func TestMallPreparation(t *testing.T) {
	r := mallResult(t)
	png := mediaPNG(t, color.RGBA{R: 255, A: 255})
	requests := 0
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: mallTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != r.Media.Value[0].URL.Value {
			t.Fatalf("多取图片或带入追踪参数：%s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader(string(png)))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	media := NewMedia(f, t.TempDir())
	cards := linkcard.New()
	defer cards.Close()
	checked := false
	p := Preparer{Media: media, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true}, Check: func(context.Context) error { return nil }, Guard: func(_ context.Context, s string) error { checked = strings.Contains(s, "325.50～499.90"); return nil }}
	for _, format := range []string{"card", "cover-text"} {
		d := Build(&r, "-71", goldenOptions(format), 0)
		prepared, err := p.Prepare(context.Background(), &r, &d)
		if err != nil {
			t.Fatal(err)
		}
		if !checked || len(prepared.Parts) != 1 {
			t.Fatal("准备未守卫或消息形状错")
		}
		body := prepared.Parts[0].Body
		index := 0
		if format == "cover-text" {
			index = 1
		}
		if body[index].Kind != types.BodyImage || body[index].Media == nil || body[index].Media.Width <= 0 {
			t.Fatal("未产生实际图片", body)
		}
		if err := prepared.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 2 {
		t.Fatal("首图请求数", requests)
	}
	if count, size := mediaFiles(t, media.root); count != 0 || size != 0 {
		t.Fatal("商品卡片产物未释放", count, size)
	}
}
