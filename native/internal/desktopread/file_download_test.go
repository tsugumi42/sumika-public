package desktopread

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediafetch"
)

func TestFileDownloadNestedAuthorizationAndPlayback(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	defer s.Close()
	st, _ := set.Platform("qq")
	ctx := context.Background()
	clip := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clip":
			_, _ = w.Write(clip)
		case "/image":
			_, _ = w.Write(pngBytes)
		default:
			_, _ = w.Write([]byte("MZ\x00program"))
		}
	}))
	defer srv.Close()
	dl := mediafetch.NewDownloader(mediafetch.DownloadOptions{Hosts: mediafetch.HostRule{Exact: []string{"files.example.test"}}, LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }, Dial: func(ctx context.Context, n, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, srv.Listener.Addr().String())
	}})
	f, err := mediafetch.New(mediafetch.Options{Store: st, Data: root, Platform: "qq", Downloader: dl, StatsEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	defer f.Close()
	s.WithVideoDownloads(f)
	for _, conv := range []string{"qq:private:90001:90003", "qq:group:90002"} {
		t.Run(conv, func(t *testing.T) {
			inner := types.ForwardTree{ID: "inner-" + conv, Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: types.SegFile, File: "视频.dat", URL: "http://files.example.test/clip"}, {Kind: types.SegFile, File: "图片.png", URL: "http://files.example.test/image"}, {Kind: types.SegFile, File: "程序.exe", URL: "http://files.example.test/program"}}}}}
			outer := types.ForwardTree{ID: "outer-" + conv, Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: types.SegForward, Ref: inner.ID}}}}}
			for _, tree := range []types.ForwardTree{inner, outer} {
				if err := st.PutForward(ctx, tree, 1000, conv); err != nil {
					t.Fatal(err)
				}
			}
			sourceID := addSegs(t, st, "qq", conv, map[string]any{"segs": []types.Seg{{Kind: types.SegForward, Ref: outer.ID}}}, "", []string{"a"})
			source := mediaRef(conv, sourceID, "").Source
			ref := api.VideoDownloadRef{Source: source, Forward: []string{outer.ID, inner.ID}}
			for _, bad := range []api.VideoDownloadRef{
				{Source: source, Forward: []string{inner.ID}},
				{Source: source, Forward: []string{outer.ID, "unrelated"}},
				{Source: source, Forward: []string{outer.ID, outer.ID}},
				{Source: source, Forward: ref.Forward, Node: -1},
				{Source: source, Forward: ref.Forward, Node: 1},
				{Source: source, Forward: ref.Forward, Index: 3},
			} {
				if _, err := s.RequestVideoDownload(ctx, bad); err == nil {
					t.Fatal("伪造下载位置通过")
				}
				if sub, err := s.WatchVideoDownload(ctx, bad); err == nil {
					sub.Close()
					t.Fatal("伪造订阅位置通过")
				}
				if err := s.CancelVideoDownload(ctx, bad); err == nil {
					t.Fatal("伪造取消位置通过")
				}
			}
			for idx := range inner.Nodes[0].Segs {
				ref.Index = idx
				sub, err := s.WatchVideoDownload(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.RequestVideoDownload(ctx, ref); err != nil {
					t.Fatal(err)
				}
				// 等的是“下载完成”这个事件，这个期限只是防卡死的安全网：5 秒在机器忙、Defender 扫临时文件时不够
				// （2026-11-19 合并队列红过一次“下载未结束”），放到 60 秒，正常情况下仍在事件到达时立刻结束
				deadline := time.NewTimer(60 * time.Second)
				done := false
				for !done {
					select {
					case p := <-sub.Updates():
						if p.Value.State == api.VideoDownloadFailed {
							t.Fatalf("下载失败：%+v", p)
						}
						done = p.Value.State == api.VideoDownloaded
					case <-deadline.C:
						t.Fatal("下载未结束")
					}
				}
				deadline.Stop()
				sub.Close()
			}
			v, err := s.ReadForward(ctx, api.ForwardRequest{Source: source, ID: inner.ID, Via: []string{outer.ID}})
			if err != nil {
				t.Fatal(err)
			}
			segs := v.Value.Nodes[0].Segments
			if segs[0].FileKind != "video" || segs[0].Media == nil || segs[0].Download.Node != 0 || len(segs[0].Download.Forward) != 2 {
				t.Fatalf("视频文件投影：%+v", segs[0])
			}
			p, err := s.PrepareVideo(ctx, *segs[0].Media)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != string(clip) {
				t.Fatal("播放副本不是原件")
			}
			if segs[1].FileKind != "image" || segs[1].Media == nil {
				t.Fatalf("图片文件投影：%+v", segs[1])
			}
			image, err := s.ReadMedia(ctx, *segs[1].Media, 0)
			if err != nil || string(image.Value.Bytes) != string(pngBytes) {
				t.Fatal("图片没有走媒体读取")
			}
			if segs[2].Media != nil || segs[2].FileKind != "" {
				t.Fatal("程序被当成可播放媒体")
			}
			forged := *segs[0].Media
			forged.Forward = []string{inner.ID}
			if _, err := s.PrepareVideo(ctx, forged); err == nil {
				t.Fatal("播放绕过外层路径")
			}
		})
	}
	// 正文文件也给相同的凭据；跨会话、隐藏事件和本地入口由原下载授权测试覆盖。
	id := addSegs(t, st, "qq", "qq:group:90002", map[string]any{"segs": []types.Seg{{Kind: types.SegFile, File: "图片.png", URL: "http://files.example.test/image"}}}, "", []string{"a"})
	ref := api.VideoDownloadRef{Source: mediaRef("qq:group:90002", id, "").Source}
	if _, err := s.RequestVideoDownload(ctx, ref); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := f.Idle(waitCtx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadMessages(ctx, api.ConversationRequest{Key: keyOf("qq:group:90002"), Limit: 200}, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range page.Value.Items {
		if m.Ref.Event.EventID == id {
			d := m.Segments[0]
			found = d.FileKind == "image" && d.Media != nil && d.Download != nil
		}
	}
	if !found {
		t.Fatal("正文文件未生成媒体凭据")
	}
}
