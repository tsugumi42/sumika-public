package linkexpand

import (
	"archive/zip"
	"bytes"
	"context"
	"image/color"
	"image/gif"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 动图走完整路径：取数（illust + ugoira_meta）→ 排消息 → 下载 ZIP 合成 GIF。
// 全部用编的帧和编的作品；取不到时说清原因（pixiv 匿名限制和不存在都在这里，不绕登录）。

type flowTransport func(*http.Request) (*http.Response, error)

func (f flowTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func flowResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const flowIllust = `{"error":false,"body":{"illustId":"90100","illustTitle":"编造的动图","userId":"90101","userName":"编造画师甲","userAccount":"sample","createDate":"2026-11-01T12:00:00+09:00",` +
	`"urls":{"original":"https://i.pximg.net/img-original/img/2026/11/01/12/30/45/90100_ugoira0.jpg","regular":"https://i.pximg.net/regular.png"},"width":2,"height":2,"pageCount":1,"xRestrict":0,"illustType":2,"aiType":1,"tags":{"tags":[]}}}`

const flowMeta = `{"error":false,"body":{"originalSrc":"https://i.pximg.net/frames.zip","src":"https://i.pximg.net/frames600.zip","mime_type":"image/png","frames":[{"file":"000000.png","delay":100},{"file":"000001.png","delay":100},{"file":"000002.png","delay":200}]}}`

// flowEnv 起一个假的 pixiv：取数用的 Fetcher 吃编的 ajax 响应，下载用的 MediaManager 连到本地 TLS 服务拿 ZIP 和封面。
func flowEnv(t *testing.T, metaStatus int, metaBody string) (*linkreader.Result, *Preparer) {
	t.Helper()
	colors := []color.Color{color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	var zipBytes bytes.Buffer
	zw := zip.NewWriter(&zipBytes)
	for i, c := range colors {
		w, e := zw.Create([]string{"000000.png", "000001.png", "000002.png"}[i])
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(mediaPNG(t, c)); e != nil {
			t.Fatal(e)
		}
	}
	if e := zw.Close(); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/frames.zip":
			w.Write(zipBytes.Bytes())
		case "/regular.png":
			w.Write(mediaPNG(t, color.RGBA{R: 9, A: 255}))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	media := server.Client().Transport.(*http.Transport).Clone()
	media.TLSClientConfig.InsecureSkipVerify = true
	media.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(media.CloseIdleConnections)
	mediaFetcher, e := linkreader.NewFetcher(linkreader.FetchOptions{Transport: media, Rate: linkreader.RateOptions{RequestsPerMinute: 10000}})
	if e != nil {
		t.Fatal(e)
	}
	api, e := linkreader.NewFetcher(linkreader.FetchOptions{Rate: linkreader.RateOptions{RequestsPerMinute: 10000}, Transport: flowTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/ajax/illust/90100":
			return flowResponse(200, flowIllust), nil
		case "/ajax/illust/90100/ugoira_meta":
			return flowResponse(metaStatus, metaBody), nil
		}
		t.Errorf("没想到的请求：%s", req.URL)
		return flowResponse(404, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	l, ok := linkreader.Recognize("https://www.pixiv.net/artworks/90100")
	if !ok {
		t.Fatal("识别不出")
	}
	r, e := api.Fetch(context.Background(), l)
	if e != nil {
		t.Fatal(e)
	}
	p := &Preparer{Media: NewMedia(mediaFetcher, t.TempDir()),
		Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil },
		Direct: types.MediaCaps{Image: true, GIF: true}, Forward: types.MediaCaps{Image: true, GIF: true}}
	return &r, p
}

func flowPrepare(t *testing.T, r *linkreader.Result, p *Preparer) []types.BodySegment {
	t.Helper()
	d := Build(r, "", Options{}, 0)
	prepared, err := p.Prepare(context.Background(), r, &d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { prepared.Release() })
	var all []types.BodySegment
	for _, part := range prepared.Parts {
		all = append(all, part.Body...)
	}
	return all
}

func TestUgoiraFullPathComposesGIF(t *testing.T) {
	r, p := flowEnv(t, 200, flowMeta)
	if r.Ugoira == nil || r.Ugoira.ZIP.State != "available" || len(r.Ugoira.Frames.Value) != 3 {
		t.Fatalf("取数没拿到动图信息：%+v", r.Ugoira)
	}
	segs := flowPrepare(t, r, p)
	var gifs, images int
	var animation *gif.GIF
	for _, s := range segs {
		switch s.Kind {
		case types.BodyGIF:
			gifs++
			if err := s.Media.Read(func(rd io.Reader) error {
				var e error
				animation, e = gif.DecodeAll(rd)
				return e
			}); err != nil {
				t.Fatal(err)
			}
		case types.BodyImage:
			images++
		}
	}
	if gifs != 1 || images != 0 {
		t.Fatalf("应是一张合成的动图、没有静态图：gif=%d image=%d %+v", gifs, images, segs)
	}
	delay := 0
	for _, d := range animation.Delay {
		delay += d
	}
	// 帧延迟 100、100、200 毫秒，总 4 个百分之一秒的 40 倍：400ms = 40
	if len(animation.Image) != 3 || delay != 40 || animation.Image[0].At(0, 0) == animation.Image[1].At(0, 0) {
		t.Fatalf("帧数、总时长或内容不对：frames=%d delay=%v", len(animation.Image), animation.Delay)
	}
}

func TestUgoiraWithoutGIFCapabilityKeepsThumbnailAndSaysWhy(t *testing.T) {
	r, p := flowEnv(t, 200, flowMeta)
	p.Direct.GIF = false
	var text string
	var images int
	for _, s := range flowPrepare(t, r, p) {
		if s.Kind == types.BodyText {
			text += s.Text + "\n"
		}
		if s.Kind == types.BodyImage {
			images++
		}
		if s.Kind == types.BodyGIF {
			t.Fatal("没有动图能力却发了 GIF")
		}
	}
	if !strings.Contains(text, "动图未下载：") || !strings.Contains(text, ErrGIFCapability.Error()) || images != 1 {
		t.Fatalf("应留首帧缩略图并写原因：images=%d %q", images, text)
	}
}

// 匿名取不到动图（pixiv 限制级常见 404 或 403）：消息里写清楚原因，不写“媒体容器无效”。
func TestUgoiraAnonymousFailuresSayWhy(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"404", 404, `{"error":true,"message":"","body":[]}`, "动图未下载：匿名取不到（已删除，或需要登录才能看）"},
		{"403", 403, `{}`, "动图未下载：匿名取不到，需要登录"},
		{"业务拒绝", 200, `{"error":true,"message":"x","body":[]}`, "动图未下载：匿名取不到，需要登录"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, p := flowEnv(t, c.status, c.body)
			var text string
			for _, s := range flowPrepare(t, r, p) {
				if s.Kind == types.BodyText {
					text += s.Text + "\n"
				}
				if s.Kind == types.BodyGIF {
					t.Fatal("取不到 ZIP 却有 GIF")
				}
			}
			if !strings.Contains(text, c.want) || strings.Contains(text, "容器") {
				t.Fatalf("原因没写清：%q", text)
			}
		})
	}
}

func TestSourceErrorClassAndText(t *testing.T) {
	for _, c := range []struct{ reason, text, class string }{
		{"anonymous_access_denied", "匿名取不到，需要登录", "login_required"},
		{"api_rejected_anonymous_request", "匿名取不到，需要登录", "login_required"},
		{"not_found", "匿名取不到（已删除，或需要登录才能看）", "not_found"},
		{"rate_limited", "被对方限流了，稍后再试", "rate_limited"},
		{"weird", "取数时没拿到", "unknown"},
	} {
		err := &SourceError{Reason: c.reason}
		if MediaReason(err) != c.text {
			t.Errorf("%s 的文字：%q", c.reason, MediaReason(err))
		}
		if class, _ := MediaClass(err); class != c.class {
			t.Errorf("%s 的类别：%s", c.reason, class)
		}
	}
}

// 内部原因代码（interface_error 等）不写进发出去的消息，也不补“图片没取到”这句。
func TestInternalReasonCodeNotLeaked(t *testing.T) {
	for _, code := range []string{"interface_error", "response_too_large", "canceled", "failed"} {
		r := goldenBase("pixiv", "artwork", "90001")
		r.Title = av("纸船试航")
		r.Media = av([]linkreader.Media{goldenImage(0)})
		r.Media.Truncated, r.Media.Reason = true, code
		r.Blocks = []linkreader.Block{{Type: "image", Media: &r.Media.Value[0]}}
		got := Build(&r, "", Options{}, 0).Describe()[0]
		if strings.Contains(got, code) || strings.Contains(got, "只取到第一页") {
			t.Fatalf("%s 漏进消息：%q", code, got)
		}
		if sourceReason(code) != "" || strings.Contains(ReasonText(code), code) {
			t.Fatalf("%s 没被挡住", code)
		}
	}
}

// 作品有图但页图匿名取不到：消息末尾说一句，不是悄悄发一条没图的消息。
func TestPixivMissingPagesNote(t *testing.T) {
	r := goldenBase("pixiv", "artwork", "90001")
	r.Title = av("纸船试航")
	r.Media = linkreader.Field[[]linkreader.Media]{State: "not_returned", Reason: "anonymous_access_denied"}
	got := Build(&r, "", Options{}, 0).Describe()[0]
	if !strings.Contains(got, "图片没取到：匿名取不到，需要登录") {
		t.Fatalf("缺原因：%q", got)
	}
	// 多页只取到首图
	r.Media = av([]linkreader.Media{goldenImage(0)})
	r.Media.Truncated, r.Media.Reason = true, "not_found"
	r.Blocks = []linkreader.Block{{Type: "image", Media: &r.Media.Value[0]}}
	if got = Build(&r, "", Options{}, 0).Describe()[0]; !strings.Contains(got, "只取到第一页：匿名取不到（已删除") {
		t.Fatalf("多页缺原因：%q", got)
	}
	// 页数没返回不是失败，不加这句
	r.Media.Reason = "page_count_not_returned"
	if got = Build(&r, "", Options{}, 0).Describe()[0]; strings.Contains(got, "只取到") || strings.Contains(got, "图片没取到") {
		t.Fatalf("不该提示：%q", got)
	}
}
