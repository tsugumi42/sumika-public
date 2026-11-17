package linkexpand

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/taskrun"
)

// 假下载器：按地址回剧本里的结果，记下每次请求的地址。图的字节是编的，只关心体积
type scriptLoader struct {
	calls []string
	reply map[string]struct {
		size int
		err  error
	}
	made []*types.MediaResource
}

func (s *scriptLoader) Image(_ context.Context, _ string, src linkreader.Media) (Loaded, error) {
	s.calls = append(s.calls, src.URL.Value)
	r, ok := s.reply[src.URL.Value]
	if !ok {
		return Loaded{}, errors.New("剧本里没有这个地址")
	}
	if r.err != nil {
		return Loaded{}, r.err
	}
	res := types.MemoryMedia("x", "image/jpeg", 1, 1, make([]byte, r.size))
	s.made = append(s.made, res)
	return Loaded{Resource: res, Kind: types.BodyImage}, nil
}
func (*scriptLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{}, errors.New("不该下载视频")
}
func (*scriptLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, errors.New("不该下载动图")
}

const (
	xOrig  = "https://pbs.twimg.com/media/fiction.jpg?name=orig"
	xLarge = "https://pbs.twimg.com/media/fiction.jpg?name=large"
)

type reply = struct {
	size int
	err  error
}

func xMedia() linkreader.Media {
	return linkreader.Media{Type: "image", URL: linkreader.Field[string]{State: "available", Value: xOrig}}
}

// X 的原图超限：退 name=large 再试一次，并写明用的是较小的档
func TestXOriginalTooLargeFallsBackToLarge(t *testing.T) {
	for name, err := range map[string]error{
		"下载时超限":  linkreader.ErrMediaLimit,
		"地址失效":   &linkreader.FetchError{Kind: "not_found", Status: 404},
		"连不上图片站": &linkreader.FetchError{Kind: "network"},
	} {
		t.Run(name, func(t *testing.T) {
			l := &scriptLoader{reply: map[string]reply{xOrig: {err: err}, xLarge: {size: 300_000}}}
			p := &Preparer{Media: l}
			got, e := p.loadImage(context.Background(), "x", xMedia())
			if e != nil || got.Resource == nil {
				t.Fatal("large 档应当成功", e)
			}
			if len(l.calls) != 2 || l.calls[0] != xOrig || l.calls[1] != xLarge {
				t.Fatalf("应先取原图、失败后取 large：%v", l.calls)
			}
			if !strings.Contains(got.Note, "large") {
				t.Fatal("要写明用的是 large 档", got.Note)
			}
		})
	}
}

// 限流、要登录换 large 也一样不行：不重试；两档都不行报原图那一档的原因
func TestXLargeNotTriedWhenItCannotHelp(t *testing.T) {
	for _, kind := range []string{"rate_limited", "login_required", "unsafe_target"} {
		l := &scriptLoader{reply: map[string]reply{xOrig: {err: &linkreader.FetchError{Kind: kind}}, xLarge: {size: 1}}}
		if _, e := (&Preparer{Media: l}).loadImage(context.Background(), "x", xMedia()); e == nil || len(l.calls) != 1 {
			t.Fatalf("%s 不该换 large：%v %v", kind, e, l.calls)
		}
	}
	l := &scriptLoader{reply: map[string]reply{xOrig: {err: linkreader.ErrMediaLimit}, xLarge: {err: &linkreader.FetchError{Kind: "not_found"}}}}
	if _, e := (&Preparer{Media: l}).loadImage(context.Background(), "x", xMedia()); !errors.Is(e, linkreader.ErrMediaLimit) || len(l.calls) != 2 {
		t.Fatalf("两档都不行，报原图那一档的原因：%v %v", e, l.calls)
	}
	// 别的网站、不是 name=orig 的地址：没有 large 可退
	l = &scriptLoader{reply: map[string]reply{xOrig: {err: linkreader.ErrMediaLimit}}}
	if _, e := (&Preparer{Media: l}).loadImage(context.Background(), "pixiv", xMedia()); e == nil || len(l.calls) != 1 {
		t.Fatal("别的网站不走 X 的退档", e, l.calls)
	}
}

// 发送前体积检查：下载下来超过配置上限的图不发，资源释放；X 的原图超配置上限也退 large；上限不能超过 20 MB
func TestImageMaxBytesChecksBeforeSend(t *testing.T) {
	l := &scriptLoader{reply: map[string]reply{xOrig: {size: 2_000_000}, xLarge: {size: 500_000}}}
	p := &Preparer{Media: l, ImageMaxBytes: 1_000_000}
	got, e := p.loadImage(context.Background(), "x", xMedia())
	if e != nil || got.Resource.Size != 500_000 {
		t.Fatalf("原图 2 MB 超过 1 MB 上限，应退 large：%v %+v", e, got)
	}
	if l.made[0].Available() {
		t.Fatal("超限的原图要释放")
	}
	l = &scriptLoader{reply: map[string]reply{xOrig: {size: 2_000_000}, xLarge: {size: 1_500_000}}}
	if _, e = (&Preparer{Media: l, ImageMaxBytes: 1_000_000}).loadImage(context.Background(), "x", xMedia()); !errors.Is(e, linkreader.ErrMediaLimit) {
		t.Fatal("两档都超限应报超过大小上限", e)
	}
	for _, r := range l.made {
		if r.Available() {
			t.Fatal("超限的图都要释放")
		}
	}
	if got := (&Preparer{ImageMaxBytes: 50_000_000}).imageMax(); got != imageLimit {
		t.Fatalf("上限不能超过下载端的 20 MB：%d", got)
	}
	if got := (&Preparer{}).imageMax(); got != imageLimit {
		t.Fatalf("没配置用默认：%d", got)
	}
}

// 下载失败写明白原因，不再只说“下载失败”；运行页的类别也跟着分
func TestMediaReasonSaysWhyDownloadFailed(t *testing.T) {
	cases := []struct {
		err    error
		reason string
		class  string
	}{
		{&linkreader.FetchError{Kind: "not_found", Status: 404}, "图片地址已失效（HTTP 404）", taskrun.ClassNotFound},
		{&linkreader.FetchError{Kind: "login_required", Status: 403}, "图片需要登录才能看（HTTP 403）", taskrun.ClassLoginRequired},
		{&linkreader.FetchError{Kind: "rate_limited", Status: 429}, "图片站限流了，稍后再发一次链接试试", taskrun.ClassRateLimited},
		{&linkreader.FetchError{Kind: "network"}, "下载中断，或连不上图片站", taskrun.ClassNetwork},
		{&linkreader.FetchError{Kind: "unsafe_target"}, "地址不在允许下载的网站里，没有下载", taskrun.ClassUnknown}, // 视频也走这里，不写“图片”
		{&linkreader.FetchError{Kind: "unsafe_target", Reason: linkreader.ReasonFakeIPNoProxy}, "本机 DNS 给了 fake-ip 假地址，又没有代理可走，没有下载（linkReader.proxy 要配代理）", taskrun.ClassNetwork},
		{&linkreader.FetchError{Kind: "unsafe_target", Reason: linkreader.ReasonFakeIPNotListed}, "本机 DNS 给了 fake-ip 假地址，这个网站不在允许下载的名单里，没有下载", taskrun.ClassUnknown},
		{&linkreader.FetchError{Kind: "interface_error", Status: 502}, "图片站返回了错误（HTTP 502）", taskrun.ClassUnknown},
		{linkreader.ErrMediaLimit, "超过大小上限", taskrun.ClassBlocked},
		{errors.New("编的别的错"), "下载失败", taskrun.ClassUnknown},
	}
	for _, c := range cases {
		if got := MediaReason(c.err); got != c.reason {
			t.Fatalf("%v：得到 %q，应为 %q", c.err, got, c.reason)
		}
		if class, _ := MediaClass(c.err); class != c.class {
			t.Fatalf("%v：类别 %q，应为 %q", c.err, class, c.class)
		}
	}
}
