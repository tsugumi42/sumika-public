package app

// 收图下载在整个后台里的样子（规格-QQ收图下载和断线补录 第八节 M1、M2、M8、M9、M10）：
// 假协议端推带图片的消息 → 真实入库一站 → 下载器（连到假图片服务器）→ 媒体库和库里的 hash。
// 地址、号、图片内容都是编的；数据目录只用 t.TempDir()。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediafetch"
	"sumika-native/internal/mediastore"
)

const imgHost = "img.example.test"

type imgServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     map[string]int
	handlers map[string]http.HandlerFunc
}

func newImgServer(t *testing.T) *imgServer {
	t.Helper()
	s := &imgServer{reqs: map[string]int{}, handlers: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs[r.URL.Path]++
		h := s.handlers[r.URL.Path]
		s.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *imgServer) handle(path string, h http.HandlerFunc) {
	s.mu.Lock()
	s.handlers[path] = h
	s.mu.Unlock()
}

func (s *imgServer) serve(path string, status int, body []byte) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write(body)
	})
}

func (s *imgServer) hits(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[path]
}

func (s *imgServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.reqs {
		n += c
	}
	return n
}

// downloader 是连到这个假服务器的下载器：只认 imgHost，域名"解析"成一个公网地址，拨号一律引到假服务器
func (s *imgServer) downloader() *mediafetch.Downloader {
	addr := s.srv.Listener.Addr().String()
	return mediafetch.NewDownloader(mediafetch.DownloadOptions{
		Hosts:    mediafetch.HostRule{Exact: []string{imgHost}},
		LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil },
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	})
}

func imgURL(path string) string { return "http://" + imgHost + path + "?rkey=编的rkey" }

func pngBytes(tag string) []byte { return append([]byte("\x89PNG\r\n\x1a\n"), []byte(tag)...) }

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// imageFrame 是一条带文字和几张图的私聊消息
func imageFrame(now time.Time, msgID, uid string, urls ...string) string {
	segs := []string{`{"type":"text","data":{"text":"看图"}}`}
	for i, u := range urls {
		segs = append(segs, fmt.Sprintf(`{"type":"image","data":{"file":"f%d.jpg","url":%q}}`, i, u))
	}
	return fmt.Sprintf(`{"post_type":"message","message_type":"private","time":%d,"self_id":%s,"user_id":%s,"message_id":%q,"sender":{"nickname":"编的甲"},"message":[%s]}`,
		now.Unix(), testAccount, uid, msgID, strings.Join(segs, ","))
}

func (e *testEnv) openWith(is *imgServer) *App {
	e.t.Helper()
	opt := e.options()
	if is != nil {
		opt.MediaDownloader = is.downloader()
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		e.t.Fatalf("Open：%v", err)
	}
	return a
}

func (e *testEnv) segHash(a *App, pid string, idx int) string {
	e.t.Helper()
	st, err := a.set.For("qq")
	if err != nil {
		e.t.Fatal(err)
	}
	var h *string
	err = st.Events.QueryRow(`SELECT json_extract(e.segments_json, '$.segs[' || ? || '].hash') FROM events e JOIN channel_msgs m ON m.event_seq = e.seq WHERE m.platform_id = ?`, idx, pid).Scan(&h)
	if err != nil {
		return ""
	}
	if h == nil {
		return ""
	}
	return *h
}

func (e *testEnv) mediaOK(a *App, hash string, want []byte) bool {
	f, err := mediastore.OpenMedia(a.paths.Root, "qq", hash)
	if err != nil {
		return false
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return bytes.Equal(b, want)
}

// M1、M2：一张图卡着下不下来，这条消息和后面的消息照常入库；卡住的放开以后文件落盘、hash 回填
func TestMediaDownloadDoesNotBlockIntake(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	stall := make(chan struct{})
	started := make(chan struct{}, 1)
	slow := pngBytes("慢的那张")
	is.handle("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-stall:
		case <-r.Context().Done():
			return
		}
		w.Write(slow)
	})
	a := e.run(e.openWith(is))
	conn := e.peer.accept(t)
	conv := "qq:private:" + testAccount + ":" + testOwner
	e.peer.send(t, conn, imageFrame(e.now, "9001", testOwner, imgURL("/slow")))
	<-started
	e.peer.send(t, conn, privateFrame(e.now.Add(time.Second), testAccount, "9002", testOwner, "后面的话"))
	waitFor(t, "两条都入库，下载还卡着", func() bool {
		n, err := e.eventCount(a, conv)
		return err == nil && n == 2
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.intake.Idle(ctx); err != nil {
		t.Fatalf("下载卡着的时候入库一站不该被拖住：%v", err)
	}
	if h := e.segHash(a, "9001", 1); h != "" {
		t.Fatalf("还没下完就有 hash 了：%q", h)
	}
	close(stall)
	waitFor(t, "下载完回填 hash", func() bool { return e.segHash(a, "9001", 1) == sum256(slow) })
	if !e.mediaOK(a, sum256(slow), slow) {
		t.Fatal("媒体库里读不出这张图")
	}
}

// M8：陌生人的图照下载（消息照存、不观察）；同一条消息重复到达只下一次；qq.media.download 关着一张不下
func TestMediaDownloadSkips(t *testing.T) {
	t.Parallel()
	t.Run("陌生人和重复", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		is := newImgServer(t)
		content := pngBytes("主人的图")
		is.serve("/mine", 200, content)
		strangerContent := pngBytes("陌生人的图")
		is.serve("/stranger", 200, strangerContent)
		a := e.run(e.openWith(is))
		conn := e.peer.accept(t)
		e.peer.send(t, conn, imageFrame(e.now, "9101", "999", imgURL("/stranger")))
		e.peer.send(t, conn, imageFrame(e.now, "9101", "999", imgURL("/stranger"))) // 陌生人的重复消息也只下一次
		e.peer.send(t, conn, imageFrame(e.now.Add(time.Second), "9102", testOwner, imgURL("/mine")))
		e.peer.send(t, conn, imageFrame(e.now.Add(time.Second), "9102", testOwner, imgURL("/mine"))) // 同一条再到一次
		waitFor(t, "主人的图下完", func() bool { return e.segHash(a, "9102", 1) == sum256(content) })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.intake.Idle(ctx); err != nil {
			t.Fatal(err)
		}
		if err := a.media.Idle(ctx); err != nil {
			t.Fatal(err)
		}
		if is.hits("/stranger") != 1 || e.segHash(a, "9101", 1) != sum256(strangerContent) {
			t.Fatalf("陌生人的图该下载并回填，重复不重下：请求 %d 次", is.hits("/stranger"))
		}
		if n, err := e.eventCount(a, "qq:private:"+testAccount+":999"); err != nil || n != 1 {
			t.Fatalf("陌生人的消息照样要存：%d %v", n, err)
		}
		if is.hits("/mine") != 1 {
			t.Fatalf("重复到达的消息只该下一次：%d", is.hits("/mine"))
		}
	})
	t.Run("配置关掉", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		is := newImgServer(t)
		is.serve("/mine", 200, pngBytes("图"))
		e.disableMedia()
		a := e.run(e.openWith(is))
		if a.media != nil {
			t.Fatal("关着就不该造下载器")
		}
		conn := e.peer.accept(t)
		e.peer.send(t, conn, imageFrame(e.now, "9201", testOwner, imgURL("/mine")))
		waitFor(t, "入库", func() bool {
			n, err := e.eventCount(a, "qq:private:"+testAccount+":"+testOwner)
			return err == nil && n == 1
		})
		time.Sleep(200 * time.Millisecond)
		if is.total() != 0 {
			t.Fatalf("关着一张都不该下：%d 个请求", is.total())
		}
	})
	t.Run("archive 不造下载器", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.open().Close() // 先让库建出来，archive 不建库
		opt := e.options()
		opt.Mode, opt.Account = Archive, ""
		b, err := Open(context.Background(), opt)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		if b.media != nil {
			t.Fatal("archive 不该有下载器")
		}
	})
}

func (e *testEnv) disableMedia() {
	e.t.Helper()
	p := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(p)
	if err != nil {
		e.t.Fatal(err)
	}
	s := strings.Replace(string(b), "  privateAllow: []", "  privateAllow: []\n  media:\n    download: false", 1)
	if s == string(b) {
		e.t.Fatal("配置里没找到要插入的位置")
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// M10：已经收到、只存了地址的那批，重启连上后补下载（先关着下载收一条，再打开下载重启）。
func TestMediaBackfillOnStartup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	content := pngBytes("先收到后补的")
	is.serve("/late", 200, content)
	e.disableMedia()
	ctx, cancel := context.WithCancel(context.Background())
	a := e.openWith(is)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	strangerContent := pngBytes("陌生人的")
	is.serve("/stranger", 200, strangerContent)
	conn := e.peer.accept(t)
	e.peer.send(t, conn, imageFrame(e.now, "9301", testOwner, imgURL("/late")))
	e.peer.send(t, conn, imageFrame(e.now, "9302", "999", imgURL("/stranger")))
	waitFor(t, "入库", func() bool {
		n, err := e.eventCount(a, "qq:private:"+testAccount+":"+testOwner)
		m, err2 := e.eventCount(a, "qq:private:"+testAccount+":999")
		return err == nil && err2 == nil && n == 1 && m == 1
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if is.total() != 0 {
		t.Fatal("关着时不该下载")
	}
	e.writeConfig(testToken) // 配置换回默认（下载开着）
	b := e.run(e.openWith(is))
	conn = e.peer.accept(t)
	e.peer.send(t, conn, fmt.Sprintf(`{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":%s}`, testAccount))
	waitFor(t, "连上后补扫补上", func() bool { return e.segHash(b, "9301", 1) == sum256(content) })
	if is.hits("/late") != 1 {
		t.Fatalf("请求了 %d 次", is.hits("/late"))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := b.media.Idle(ctx2); err != nil {
		t.Fatal(err)
	}
	if is.hits("/stranger") != 1 || e.segHash(b, "9302", 1) != sum256(strangerContent) {
		t.Fatalf("陌生人的图补扫也该下载并回填：请求 %d 次", is.hits("/stranger"))
	}
}

// 过期的图（403）经真实的适配器向假协议端换新地址（get_msg），换来的地址下载成功
func TestMediaRefreshThroughAdapter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	content := pngBytes("换了地址的")
	is.serve("/old", 403, nil)
	is.serve("/new", 200, content)
	var mu sync.Mutex
	var asked []string
	e.peer.onAction = func(c *websocket.Conn, data []byte) {
		var o struct {
			Action string          `json:"action"`
			Echo   string          `json:"echo"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &o) != nil || o.Action != "get_msg" {
			answerInfo(c, data)
			return
		}
		var p struct {
			ID json.Number `json:"message_id"`
		}
		_ = json.Unmarshal(o.Params, &p)
		mu.Lock()
		asked = append(asked, p.ID.String())
		mu.Unlock()
		body := fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%q,"data":{"message_type":"private","message_id":%s,"user_id":%s,"time":%d,"message":[{"type":"image","data":{"file":"f0.jpg","url":%q}}]}}`,
			o.Echo, p.ID.String(), testOwner, e.now.Unix(), imgURL("/new"))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Write(ctx, websocket.MessageText, []byte(body))
	}
	a := e.run(e.openWith(is))
	conn := e.peer.accept(t)
	e.peer.send(t, conn, imageFrame(e.now, "9401", testOwner, imgURL("/old")))
	waitFor(t, "换了新地址下载成功", func() bool { return e.segHash(a, "9401", 1) == sum256(content) })
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "9401" {
		t.Fatalf("向协议端问了：%v", asked)
	}
}

// Open 先起工人，不下载旧图；适配器连上后补扫，过期图经 get_msg 换新地址。
func TestMediaRefreshBeforeConnectedThenAfter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	content := pngBytes("连上以后才换到的")
	is.serve("/old", 403, nil)
	is.serve("/new", 200, content)
	e.disableMedia()
	ctx, cancel := context.WithCancel(context.Background())
	a := e.openWith(is)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	conn := e.peer.accept(t)
	e.peer.send(t, conn, imageFrame(e.now, "9501", testOwner, imgURL("/old")))
	waitFor(t, "入库", func() bool {
		n, err := e.eventCount(a, "qq:private:"+testAccount+":"+testOwner)
		return err == nil && n == 1
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.writeConfig(testToken) // 下载开着
	var mu sync.Mutex
	asked := 0
	e.peer.onAction = func(c *websocket.Conn, data []byte) {
		var o struct {
			Action string `json:"action"`
			Echo   string `json:"echo"`
		}
		if json.Unmarshal(data, &o) != nil || o.Action != "get_msg" {
			answerInfo(c, data)
			return
		}
		mu.Lock()
		asked++
		mu.Unlock()
		body := fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%q,"data":{"message_type":"private","message_id":9501,"user_id":%s,"time":%d,"message":[{"type":"image","data":{"file":"f0.jpg","url":%q}}]}}`,
			o.Echo, testOwner, e.now.Unix(), imgURL("/new"))
		wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer wcancel()
		_ = c.Write(wctx, websocket.MessageText, []byte(body))
	}
	b := e.openWith(is)
	time.Sleep(150 * time.Millisecond)
	if is.hits("/old") != 0 {
		t.Fatalf("尚未连接就下载了旧图 %d 次", is.hits("/old"))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := b.media.Idle(ctx2); err != nil {
		t.Fatal(err)
	}
	st, err := b.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := st.Events.QueryRow(`SELECT count(*) FROM media_fetch`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("没连上时不该记任何下载状态：%d 行 %v", rows, err)
	}
	e.run(b)
	conn2 := e.peer.accept(t)
	// 适配器收到协议端的第一帧才开始核验；真实的 SnowLuma 连上就会推心跳
	e.peer.send(t, conn2, fmt.Sprintf(`{"post_type":"meta_event","meta_event_type":"heartbeat","self_id":%s}`, testAccount))
	waitFor(t, "连上以后的补扫换到新地址", func() bool { return e.segHash(b, "9501", 1) == sum256(content) })
	if remaining := b.adapter.RemainingQueryBudget(); remaining != backfillBudget-1 {
		t.Fatalf("防抖前换地址没有扣同一预算：剩余 %d，要 %d", remaining, backfillBudget-1)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 1 {
		t.Fatalf("向协议端问了 %d 次", asked)
	}
}

// M9：下载卡着的时候关机 —— 关得掉，不挂；之后同一组库还能再开（没有半截文件）
func TestMediaShutdownWhileDownloading(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	is := newImgServer(t)
	started := make(chan struct{}, 1)
	is.handle("/stuck", func(w http.ResponseWriter, r *http.Request) {
		w.Write(append(pngBytes("卡"), bytes.Repeat([]byte("q"), 600)...))
		w.(http.Flusher).Flush()
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	a := e.openWith(is)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	conn := e.peer.accept(t)
	e.peer.send(t, conn, imageFrame(e.now, "9501", testOwner, imgURL("/stuck")))
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("关机出错：%v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("下载卡着时关机挂住了")
	}
	if entries, _ := os.ReadDir(filepath.Join(e.data, "go", "media", "qq", mediastore.TmpDirName)); len(entries) != 0 {
		t.Fatalf("临时文件该删掉：%d 个", len(entries))
	}
	if !e.logContains("关完了") {
		t.Fatal("没有关完的日志")
	}
}

// 放大十倍（规格 M13）：500 条带图的私聊消息一口气交给入库一站，开着下载和关着下载各量一遍入库的总耗时和每条的 Ticket 延迟。
// 下载放在另外的工人里，开着下载不该让入库变慢。-short 跳过
func TestScaleIngestWithDownloads(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("放大测试，-short 跳过")
	}
	const n = 500
	run := func(t *testing.T, download bool) (total time.Duration, lat []time.Duration, fetched time.Duration) {
		e := newEnv(t)
		is := newImgServer(t)
		is.handle("/img", func(w http.ResponseWriter, r *http.Request) {
			w.Write(pngBytes(r.URL.Query().Get("i") + strings.Repeat("图", 20_000)))
		})
		if !download {
			e.disableMedia()
		}
		a := e.openWith(is)
		defer a.Close()
		ctx := context.Background()
		inb := func(i int) types.Inbound {
			return types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner},
				PlatformID: fmt.Sprint(5000 + i), From: types.User{ID: testOwner, Nick: "编的甲"}, Time: e.now.Add(time.Duration(i) * time.Second),
				Segs: []types.Seg{{Kind: types.SegText, Text: "看图"}, {Kind: types.SegImage, URL: imgURL(fmt.Sprintf("/img?i=%d", i)), File: fmt.Sprintf("%d.jpg", i)}}}
		}
		tickets := make([]*channel.Ticket, n)
		starts := make([]time.Time, n)
		begin := time.Now()
		for i := 0; i < n; i++ {
			starts[i] = time.Now()
			tk, err := a.intake.Submit(ctx, inb(i), channel.Options{})
			if err != nil {
				t.Fatal(err)
			}
			tickets[i] = tk
		}
		for i, tk := range tickets {
			if _, err := tk.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			lat = append(lat, time.Since(starts[i]))
		}
		idleCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := a.intake.Idle(idleCtx); err != nil {
			t.Fatal(err)
		}
		total = time.Since(begin)
		if download {
			if err := a.media.Idle(idleCtx); err != nil {
				t.Fatal(err)
			}
			fetched = time.Since(begin)
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		return
	}
	onTotal, onLat, fetched := run(t, true)
	offTotal, offLat, _ := run(t, false)
	t.Logf("500 条带图消息：开着下载 入库总耗时 %v、Ticket p99 %v、下完用时 %v；关着下载 入库总耗时 %v、Ticket p99 %v",
		onTotal.Round(time.Millisecond), onLat[n*99/100].Round(time.Millisecond), fetched.Round(time.Millisecond),
		offTotal.Round(time.Millisecond), offLat[n*99/100].Round(time.Millisecond))
}
