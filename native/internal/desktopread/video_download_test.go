package desktopread

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/mediafetch"
)

func TestVideoDownloadVisibilityAndServiceClose(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	local, _ := set.Platform("local")
	var requests atomic.Int32
	started := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write(append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 1024)...))
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	dl := mediafetch.NewDownloader(mediafetch.DownloadOptions{
		Hosts:    mediafetch.HostRule{Exact: []string{"video.example.test"}},
		LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil },
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
	f, err := mediafetch.New(mediafetch.Options{Store: qq, Data: root, Platform: "qq", Downloader: dl, StatsEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	defer f.Close()
	s.WithVideoDownloads(f)
	defer s.Close()
	body := json.RawMessage(`{"segs":[{"kind":"video","url":"http://video.example.test/v"},{"kind":"image","url":"http://video.example.test/i"}]}`)
	conv := "qq:group:90002"
	id := addSegs(t, qq, "qq", conv, body, "", []string{"a"})
	source := func(conv, id string) api.VideoDownloadRef {
		return api.VideoDownloadRef{Source: mediaRef(conv, id, "").Source}
	}
	valid := source(conv, id)
	hidden := addSegs(t, qq, "qq", conv, body, "", []string{"outside"})
	localID := addSegs(t, local, "console", "console:a", body, "", nil)
	localGroup := addSegs(t, local, "console", "console:group:madeup", body, "", nil)
	bad := []api.VideoDownloadRef{source(conv, hidden), source("qq:group:90003", id), source("console:a", localID), source("console:group:madeup", localGroup), source(conv, "missing")}
	wrongIndex := valid
	wrongIndex.Index = -1
	bad = append(bad, wrongIndex)
	wrongIndex = valid
	wrongIndex.Index = 1
	bad = append(bad, wrongIndex)
	wrongIndex = valid
	wrongIndex.Index = 2
	bad = append(bad, wrongIndex)
	wrongPlatform := valid
	wrongPlatform.Source.Event.Platform = "local"
	bad = append(bad, wrongPlatform)
	ctx := context.Background()
	for _, ref := range bad {
		if _, err := s.RequestVideoDownload(ctx, ref); err == nil {
			t.Fatalf("伪造来源被接受：%+v", ref)
		}
		if _, err := s.WatchVideoDownload(ctx, ref); err == nil {
			t.Fatalf("伪造来源订阅被接受：%+v", ref)
		}
		if err := s.CancelVideoDownload(ctx, ref); err == nil {
			t.Fatalf("伪造来源取消被接受：%+v", ref)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("授权前已联网")
	}
	sub, err := s.WatchVideoDownload(ctx, valid)
	if err != nil {
		t.Fatal(err)
	}
	p := <-sub.Updates()
	if p.Value.State != api.VideoNotDownloaded {
		t.Fatalf("初始状态：%+v", p)
	}
	if _, err := s.RequestVideoDownload(ctx, valid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("下载未开始")
	}
	if err := s.CancelVideoDownload(ctx, valid); err != nil {
		t.Fatal(err)
	}
	sub.Close()
	sub.Close()
	// QQ 私聊同样能下载；宿主关闭必须收掉未消费的订阅，不能等界面读下一条。
	private := "qq:private:90001:90003"
	privateID := addSegs(t, qq, "qq", private, body, "", []string{"a"})
	ref := source(private, privateID)
	unread, err := s.WatchVideoDownload(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestVideoDownload(ctx, ref); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("私聊下载未开始")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("未消费订阅阻止 Service.Close")
	}
	unread.Close()
	if _, err := s.RequestVideoDownload(ctx, ref); !errors.Is(err, ErrClosed) {
		t.Fatal("关闭后仍接受下载")
	}
	f.Close()
}
