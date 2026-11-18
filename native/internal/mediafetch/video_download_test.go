package mediafetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func videoUpdate(t *testing.T, sub api.Subscription[api.VideoDownloadProgress], want func(api.VideoDownloadProgress) bool) api.VideoDownloadProgress {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case next, ok := <-sub.Updates():
			if !ok {
				t.Fatal("视频订阅过早关闭")
			}
			if next.Err != nil {
				t.Fatal(next.Err)
			}
			if want(next.Value) {
				return next.Value
			}
		case <-timer.C:
			t.Fatal("没有收到期望的视频状态")
		}
	}
}

func TestVideoAutoByteBoundaryAndManualReuse(t *testing.T) {
	for _, tc := range []struct {
		name           string
		size           int
		known, chunked bool
	}{
		{"below", 1023, false, false}, {"equal", 1024, true, false}, {"declared-over", 1025, false, false}, {"stream-over", 1025, false, true}, {"known-over", 1025, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.f.noVideoTools()
			e.f.dl.videoMax = 1024
			content := append(tinyMP4(), make([]byte, tc.size-len(tinyMP4()))...)
			e.handle("/video", func(w http.ResponseWriter, r *http.Request) {
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				w.Write(content)
			})
			segs := []types.Seg{{Kind: types.SegVideo, URL: testSegURL("/video")}}
			if tc.known {
				segs[0].Size = int64(tc.size)
			}
			seq := e.addEvent(segs, "8100")
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			row := e.row(seq, 0)
			if tc.size <= 1024 {
				if row.State != store.MediaDone || e.segHash(seq, 0) != sha(content) {
					t.Fatalf("边界内未完成：%+v", row)
				}
				return
			}
			if row.State != store.MediaTooBig || len(mediaFiles(t, e)) != 0 {
				t.Fatalf("超限必须等手动且不留文件：%+v", row)
			}
			tmp, err := os.ReadDir(filepath.Join(e.data, "go", "media", "qq", ".tmp"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(tmp) != 0 {
				t.Fatal("自动超限残留临时文件")
			}
			if tc.known && e.nreq("/video") != 0 {
				t.Fatal("声明超限不该联网")
			}
			sub, err := e.f.WatchVideo(context.Background(), seq, 0, segs[0])
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			videoUpdate(t, sub, func(p api.VideoDownloadProgress) bool { return p.State == api.VideoAwaitingManual })
			if _, err := e.f.RequestVideo(seq, 0, segs); err != nil {
				t.Fatal(err)
			}
			p := videoUpdate(t, sub, func(p api.VideoDownloadProgress) bool { return p.State == api.VideoDownloaded })
			if p.Received != int64(tc.size) || p.Total != int64(tc.size) || p.Hash != sha(content) {
				t.Fatalf("完成字节错误：%+v", p)
			}
			before := e.nreq("/video")
			if p, err := e.f.RequestVideo(seq, 0, segs); err != nil || p.State != api.VideoDownloaded || e.nreq("/video") != before {
				t.Fatalf("完成后重复下载：%+v %v", p, err)
			}
			other := e.addEvent(segs, "8101")
			if _, err := e.f.RequestVideo(other, 0, segs); err != nil {
				t.Fatal(err)
			}
			e.idle()
			if e.segHash(other, 0) != sha(content) || len(mediaFiles(t, e)) != 1 {
				t.Fatal("相同内容没有去重")
			}
		})
	}
	// 队列溢出只恢复本轮接收过的视频，旧时间戳的补录也不能丢；未接收旧记录不跟着下。
	e := newEnv(t, func(o *envOpt) { o.queueSize = 1 })
	e.f.noVideoTools()
	e.serve("/current", 200, tinyMP4())
	old := e.addEvent([]types.Seg{{Kind: types.SegVideo, URL: testSegURL("/old")}}, "old-video")
	segs := []types.Seg{{Kind: types.SegVideo, URL: testSegURL("/current")}}
	var last int64
	for i := range 6 {
		last = e.addEventAt(segs, fmt.Sprint(8500+i), e.clk.Now().Add(-30*24*time.Hour).UnixMilli())
		e.f.Offer(last, segs)
	}
	e.start()
	waitFor(t, "溢出的补录视频恢复", func() bool { return e.segHash(last, 0) == sha(tinyMP4()) })
	e.idle()
	if e.nreq("/current") != 6 || e.nreq("/old") != 0 || e.segHash(old, 0) != "" {
		t.Fatal("恢复遗漏或扩大到旧视频")
	}
}

func TestVideoManualProgressConcurrencyCancel(t *testing.T) {
	e := newEnv(t)
	e.f.noVideoTools()
	e.f.dl.videoMax = 512
	// 只把总时限改短，Transport 的响应头时限不变；手动不能沿用图片总时限。
	e.f.dl.timeout = time.Nanosecond
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	content := append(tinyMP4(), make([]byte, 4096-len(tinyMP4()))...)
	for i := range gates {
		gate := gates[i]
		e.handle(fmt.Sprintf("/v%d", i), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Write(content[:1024])
			w.(http.Flusher).Flush()
			select {
			case <-gate:
				w.Write(content[1024:])
			case <-r.Context().Done():
			}
		})
	}
	segs := []types.Seg{{Kind: types.SegVideo, URL: testSegURL("/v0")}, {Kind: types.SegVideo, URL: testSegURL("/v1")}, {Kind: types.SegVideo, URL: testSegURL("/v2")}}
	seq := e.addEvent(segs, "8200")
	e.start()
	ctx := context.Background()
	subs := make([]api.Subscription[api.VideoDownloadProgress], 2)
	for i := range 2 {
		var err error
		subs[i], err = e.f.WatchVideo(ctx, seq, i, segs[i])
		if err != nil {
			t.Fatal(err)
		}
		defer subs[i].Close()
		if _, err := e.f.RequestVideo(seq, i, segs); err != nil {
			t.Fatal(err)
		}
		p := videoUpdate(t, subs[i], func(p api.VideoDownloadProgress) bool { return p.Received == 1024 })
		if p.State != api.VideoManualDownloading || p.Total != 4096 {
			t.Fatalf("进度没有实际字节：%+v", p)
		}
	}
	if _, err := e.f.RequestVideo(seq, 2, segs); !errors.Is(err, api.ErrVideoDownloadBusy) || e.nreq("/v2") != 0 {
		t.Fatalf("第三路没有被挡住：%v", err)
	}
	if _, err := e.f.RequestVideo(seq, 0, segs); err != nil || e.nreq("/v0") != 1 {
		t.Fatal("重复请求重新联网")
	}
	if err := e.f.CancelVideo(ctx, seq, 0); err != nil {
		t.Fatal(err)
	}
	videoUpdate(t, subs[0], func(p api.VideoDownloadProgress) bool { return p.State == api.VideoDownloadCanceled })
	if e.segHash(seq, 0) != "" {
		t.Fatal("取消回填了 hash")
	}
	subs[1].Close()
	// 退订只停止观察，后台任务和两路门槛仍然存在。
	if _, err := e.f.RequestVideo(seq, 2, segs); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "第三个任务开始", func() bool { return e.nreq("/v2") == 1 })
	if _, err := e.f.RequestVideo(seq, 0, segs); !errors.Is(err, api.ErrVideoDownloadBusy) {
		t.Fatal("退订错误地取消了第二路")
	}
	close(gates[1])
	waitFor(t, "第二个视频完成", func() bool { return e.segHash(seq, 1) == sha(content) })
	e.f.Close()
	if e.row(seq, 2).State != store.MediaCanceled || e.segHash(seq, 2) != "" {
		t.Fatal("退出没有取消在途任务")
	}
	tmp, err := os.ReadDir(filepath.Join(e.data, "go", "media", "qq", ".tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmp) != 0 || len(mediaFiles(t, e)) != 1 {
		t.Fatalf("取消或退出留下半文件：tmp=%d", len(tmp))
	}
}

func TestVideoManualAddressBoundary(t *testing.T) {
	for _, name := range []string{"scheme", "host", "userinfo", "port", "dns", "redirect", "refresh-blocked", "refresh-ok", "http500", "cancel-refresh"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.f.noVideoTools()
			raw := testSegURL("/v")
			switch name {
			case "scheme":
				raw = "file:///编的.mp4"
			case "host":
				raw = "http://evil.example.test/v"
			case "userinfo":
				raw = "http://user@" + testHost + "/v"
			case "port":
				raw = "http://" + testHost + ":3001/v"
			case "dns":
				e.lookup = func(string) []net.IP { return []net.IP{net.ParseIP("127.0.0.1")} }
			}
			e.handle("/v", func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.1/private", 302)
				case "refresh-blocked", "refresh-ok", "cancel-refresh":
					w.WriteHeader(403)
				case "http500":
					w.WriteHeader(500)
				default:
					w.Write(tinyMP4())
				}
			})
			fresh := "http://127.0.0.1/private"
			if name == "refresh-ok" {
				fresh = testSegURL("/fresh")
				e.serve("/fresh", 200, tinyMP4())
			}
			e.refresh.answer([]types.Seg{{Kind: types.SegVideo, URL: fresh}}, nil)
			if name == "cancel-refresh" {
				e.refresh.gate = make(chan struct{})
				e.refresh.entered = make(chan struct{}, 1)
			}
			segs := []types.Seg{{Kind: types.SegVideo, URL: raw}}
			seq := e.addEvent(segs, "8300")
			e.start()
			if _, err := e.f.RequestVideo(seq, 0, segs); err != nil {
				t.Fatal(err)
			}
			if name == "cancel-refresh" {
				select {
				case <-e.refresh.entered:
				case <-time.After(envLimit):
					t.Fatal("没进入刷新")
				}
				ctx, cancel := context.WithTimeout(context.Background(), envLimit)
				defer cancel()
				if err := e.f.CancelVideo(ctx, seq, 0); err != nil {
					t.Fatal(err)
				}
			}
			e.idle()
			want := store.MediaBlocked
			switch name {
			case "refresh-ok":
				want = store.MediaDone
			case "http500":
				want = store.MediaFailed
			case "cancel-refresh":
				want = store.MediaCanceled
			}
			if row := e.row(seq, 0); row.State != want {
				t.Fatalf("%s：%+v", name, row)
			}
			if name == "scheme" || name == "host" || name == "userinfo" || name == "port" || name == "dns" {
				if len(e.requests()) != 0 {
					t.Fatal("被拦的地址发了请求")
				}
			}
			if bytes.Contains([]byte(e.allLogs()), []byte(secret)) {
				t.Fatal("日志泄漏 rkey")
			}
		})
	}
}
