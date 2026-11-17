package linkexpand

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

func mediaField[T any](v T) linkreader.Field[T] {
	return linkreader.Field[T]{State: "available", Value: v}
}
func mediaPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func mediaFiles(t *testing.T, root string) (count int, size int64) {
	t.Helper()
	e := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			count++
			size += info.Size()
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return
}
func TestMediaPreparationAndCleanup(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg：生成不了真实短片（和 mediafetch 的视频测试同一个前提）")
	}
	red, blue := mediaPNG(t, color.RGBA{R: 255, A: 255}), mediaPNG(t, color.RGBA{B: 255, A: 255})
	var zipBytes bytes.Buffer
	zw := zip.NewWriter(&zipBytes)
	for i, b := range [][]byte{red, blue} {
		name := []string{"000000.png", "000001.png"}[i]
		w, e := zw.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	if e := zw.Close(); e != nil {
		t.Fatal(e)
	}
	fixture := filepath.Join(t.TempDir(), "fictional.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=red:s=160x90:r=15:d=0.2", "-c:v", "mpeg4", "-threads", "1", "-y", fixture)
	mediaPriority(cmd)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("生成真实短片：%v %s", e, output)
	}
	video, e := os.ReadFile(fixture)
	if e != nil {
		t.Fatal(e)
	}
	var regular, videoRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/original.png":
			w.Header().Set("Content-Type", "image/png")
			w.(http.Flusher).Flush()
			w.Write(red)
			io.CopyN(w, zeroMediaReader{}, imageLimit+1-int64(len(red)))
		case "/regular.png":
			regular.Add(1)
			if r.Referer() != "https://www.pixiv.net/" {
				t.Error("缺少pixiv固定Referer")
			}
			w.Write(blue)
		case "/forbidden.png":
			w.WriteHeader(403)
		case "/frames.zip":
			w.Write(zipBytes.Bytes())
		case "/video.mp4":
			videoRequests.Add(1)
			w.Write(video)
		default:
			w.Write([]byte("not a valid media container"))
		}
	}))
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	f, e := linkreader.NewFetcher(linkreader.FetchOptions{Transport: transport, Rate: linkreader.RateOptions{RequestsPerMinute: 10000}})
	if e != nil {
		t.Fatal(e)
	}
	m := NewMedia(f, t.TempDir())
	src := linkreader.Media{Type: "image", URL: mediaField("https://i.pximg.net/original.png"), Fallback: &linkreader.MediaFallback{URL: mediaField("https://i.pximg.net/regular.png"), AboveBytes: 100 * 1024 * 1024}}
	loaded, e := m.Image(context.Background(), "pixiv", src)
	if e != nil {
		t.Fatal(e)
	}
	if regular.Load() != 1 || loaded.Note != "原图超过20 MB，使用regular" || loaded.Received != imageLimit+1+int64(len(blue)) {
		t.Fatalf("原图实际超限回退：%d %+v", regular.Load(), loaded)
	}
	if e = loaded.Resource.Release(); e != nil {
		t.Fatal(e)
	}
	src.URL = mediaField("https://i.pximg.net/forbidden.png")
	if _, e = m.Image(context.Background(), "pixiv", src); e == nil || regular.Load() != 1 {
		t.Fatal("403偷偷换成regular", e, regular.Load())
	}
	p := &linkreader.UgoiraInfo{ZIP: mediaField("https://i.pximg.net/frames.zip"), Frames: mediaField([]linkreader.UgoiraFrame{{File: "000000.png", DelayMS: 67}, {File: "000001.png", DelayMS: 134}})}
	loaded, e = m.Ugoira(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	var animation *gif.GIF
	e = loaded.Resource.Read(func(r io.Reader) error { var e error; animation, e = gif.DecodeAll(r); return e })
	if e != nil {
		t.Fatal(e)
	}
	duration := 0
	for _, delay := range animation.Delay {
		duration += delay
	}
	if duration != 20 || len(animation.Image) != 2 || animation.Image[0].At(0, 0) == animation.Image[1].At(0, 0) {
		t.Fatalf("GIF时序或帧内容丢失：frames=%d delay=%v", len(animation.Image), animation.Delay)
	}
	if e = loaded.Resource.Release(); e != nil {
		t.Fatal(e)
	}
	tooMany := *p
	tooMany.Frames = mediaField(make([]linkreader.UgoiraFrame, 301))
	before := f.Requests()
	if _, e = m.Ugoira(context.Background(), &tooMany); e == nil || f.Requests() != before {
		t.Fatal("第301帧被静默截断或仍请求ZIP")
	}
	videoSource := linkreader.Media{Type: "video", URL: mediaField("https://video.twimg.com/video.mp4")}
	if _, e = m.Video(context.Background(), "x", nil, videoSource, VideoLimits{Enabled: false}, false); !errors.Is(e, ErrVideoDisabled) || videoRequests.Load() != 0 {
		t.Fatal("视频关闭仍下载")
	}
	limits := VideoLimits{Enabled: true, Timeout: time.Minute, Bytes: 1_000_000}
	loaded, e = m.Video(context.Background(), "x", nil, videoSource, limits, false)
	if e != nil {
		t.Fatal(e)
	}
	path, e := loaded.Resource.FileName()
	if e != nil {
		t.Fatal(e)
	}
	actual, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(actual, video) {
		t.Fatal("完整视频字节不符", e)
	}
	count, size := mediaFiles(t, m.root)
	if count != 1 || size != int64(len(video)) {
		t.Fatal("媒体文件峰值", count, size)
	}
	stale := filepath.Join(m.root, "old-instance")
	if e = os.MkdirAll(stale, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(stale, "orphan"), []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	old := time.Now().Add(-2 * time.Hour)
	if e = os.Chtimes(stale, old, old); e != nil {
		t.Fatal(e)
	}
	if e = m.Sweep(time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("清理误删当前实例", e)
	}
	if _, e = os.Stat(stale); !os.IsNotExist(e) {
		t.Fatal("无主旧实例未删除", e)
	}
	if e = loaded.Resource.Release(); e != nil {
		t.Fatal(e)
	}
	count, size = mediaFiles(t, m.root)
	if count != 0 || size != 0 {
		t.Fatal("终态残留媒体", count, size)
	}
	videoSource.URL = mediaField("https://video.twimg.com/broken.mp4")
	if _, e = m.Video(context.Background(), "x", nil, videoSource, limits, false); e == nil {
		t.Fatal("坏容器被当视频")
	}
	count, _ = mediaFiles(t, m.root)
	if count != 0 {
		t.Fatal("失败临时文件残留")
	}
	t.Logf("视频=%d字节；GIF=%d帧/%d厘秒；原图回退请求=%d；终态文件=%d", len(video), len(animation.Image), duration, regular.Load(), count)
}

type zeroMediaReader struct{}

func (zeroMediaReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestUndefinedFormatsDoNotExpand(t *testing.T) {
	for _, tc := range []struct{ platform, kind string }{{"github", "issue"}, {"github", "pull_request"}, {"github", "release"}, {"github", "gist"}, {"netease", "album"}, {"netease", "playlist"}} {
		t.Run(tc.platform+"/"+tc.kind, func(t *testing.T) {
			r := linkreader.Result{Platform: tc.platform, Kind: tc.kind, Title: mediaField("编的标题"), Text: mediaField("编的正文")}
			if d := Build(&r, "-71", Options{}, 2); len(d.Parts) != 0 {
				t.Fatalf("格式未定的资源被展开：%+v", d)
			}
		})
	}
}

func TestMainResultContentFields(t *testing.T) {
	r := linkreader.Result{Platform: "bilibili", Kind: "opus", Text: mediaField("编的正文"), Stats: map[string]linkreader.Field[int64]{"like": mediaField[int64](0), "comment": mediaField[int64](12), "forward": mediaField[int64](3)}}
	d := Build(&r, "-71", Options{Sites: map[string]SiteSetting{"bilibili-dynamic": {Forward: "always"}}}, 0)
	if got := d.Parts[0].Nodes[0].Body[0].Text; !strings.Contains(got, "点赞 0 | 评论 12 | 转发 3") {
		t.Fatal("丢失 main 的动态统计：", got)
	}
	for _, kind := range []string{"video", "note"} {
		r.Platform, r.Kind = "douyin", kind
		d = Build(&r, "-71", Options{}, 0)
		var got string
		if kind == "note" {
			got = d.Parts[0].Nodes[0].Body[0].Text
		} else {
			got = d.Parts[0].Body[0].Text
		}
		if want := map[string]string{"video": "编的正文", "note": "编的正文"}[kind]; !strings.HasPrefix(got, want) {
			t.Fatal("丢失 main 的抖音描述：", got)
		}
	}
}
