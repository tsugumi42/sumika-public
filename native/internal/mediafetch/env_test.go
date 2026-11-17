package mediafetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 测试里的主机、地址、号、图片内容都是编的。"图片"只是带着位图文件头的几个字节。

const (
	testHost = "img.example.test"
	testConv = "qq:private:10000:10001"
	secret   = "编的rkey-SECRET"
)

type reqInfo struct {
	path, query string
	at          time.Time
}

type env struct {
	t     *testing.T
	data  string
	set   *store.Set
	st    *store.Store
	srv   *httptest.Server
	clk   *fake.Clock
	f     *Fetcher
	logMu sync.Mutex
	logs  []string

	mu       sync.Mutex
	reqs     []reqInfo
	handlers map[string]http.HandlerFunc
	dials    int
	lookup   func(host string) []net.IP
	refresh  *fakeRefresher
}

type fakeRefresher struct {
	mu      sync.Mutex
	calls   []string // 平台消息号
	segs    []types.Seg
	err     error
	entered chan struct{} // 非 nil：每次被问到先往这里发一个信号
	gate    chan struct{} // 非 nil：关掉它之前被问到的一直卡着，放开后按被问到那一刻的回答返回
}

func (r *fakeRefresher) Message(ctx context.Context, conv, platformID string) ([]types.Seg, error) {
	r.mu.Lock()
	r.calls = append(r.calls, conv+"|"+platformID)
	segs, err := r.segs, r.err // 被问到的这一刻就定下回答，后面 answer 改的只管以后的提问
	entered, gate := r.entered, r.gate
	r.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return segs, err
}

// answer 改协议端接下来的回答
func (r *fakeRefresher) answer(segs []types.Seg, err error) {
	r.mu.Lock()
	r.segs, r.err = segs, err
	r.mu.Unlock()
}

func (r *fakeRefresher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// png 造一张"图"：PNG 文件头加一段区分内容的字节
func png(tag string) []byte { return append([]byte("\x89PNG\r\n\x1a\n"), []byte(tag)...) }

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func testSegURL(path string) string { return "http://" + testHost + path + "?rkey=" + secret }

type envOpt struct {
	maxBytes   int64
	timeout    time.Duration
	workers    int
	queueSize  int
	noRefresh  bool
	hosts      HostRule
	afterDown  func()
	statsEvery time.Duration
}

func newEnv(t *testing.T, mut ...func(*envOpt)) *env {
	t.Helper()
	o := envOpt{hosts: HostRule{Exact: []string{testHost, "gxh.vip.qq.com"}}, statsEvery: -1}
	for _, m := range mut {
		m(&o)
	}
	dir := t.TempDir()
	p, err := paths.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := set.Platform("qq")
	e := &env{t: t, data: p.Root, set: set, st: st, clk: fake.NewClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)),
		handlers: map[string]http.HandlerFunc{}, refresh: &fakeRefresher{}}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.reqs = append(e.reqs, reqInfo{r.URL.Path, r.URL.RawQuery, e.clk.Now()})
		h := e.handlers[r.URL.Path]
		e.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	srvAddr := e.srv.Listener.Addr().String()
	dl := NewDownloader(DownloadOptions{
		Hosts: o.hosts, MaxBytes: o.maxBytes, Timeout: o.timeout,
		LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
			e.mu.Lock()
			fn := e.lookup
			e.mu.Unlock()
			if fn != nil {
				return fn(host), nil
			}
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			e.mu.Lock()
			e.dials++
			e.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, srvAddr)
		},
	})
	var ref Refresher = e.refresh
	if o.noRefresh {
		ref = nil
	}
	e.f, err = New(Options{Store: st, Data: p.Root, Platform: "qq", Downloader: dl, Refresher: ref, Clock: e.clk, Logf: e.logf, Warnf: e.at("WARN"), Errorf: e.at("ERROR"),
		Workers: o.workers, QueueSize: o.queueSize, StatsEvery: o.statsEvery, afterDownload: o.afterDown})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.f.Close()
		e.srv.Close()
		set.Close()
	})
	return e
}

func (e *env) logf(format string, args ...any) {
	line := strings.TrimSpace(fmt.Sprintf(format, args...))
	e.logMu.Lock()
	e.logs = append(e.logs, line)
	e.logMu.Unlock()
}

// at 给 WARN / ERROR 出口：同一个切片，行首带级别，allLogs 照样看得到
func (e *env) at(level string) func(string, ...any) {
	return func(format string, args ...any) { e.logf(level+" "+format, args...) }
}

func (e *env) allLogs() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return strings.Join(e.logs, "\n")
}

func (e *env) handle(path string, h http.HandlerFunc) {
	e.mu.Lock()
	e.handlers[path] = h
	e.mu.Unlock()
}

func (e *env) serve(path string, status int, body []byte) {
	e.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write(body)
	})
}

func (e *env) requests() []reqInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]reqInfo(nil), e.reqs...)
}

func (e *env) nreq(path string) int {
	n := 0
	for _, r := range e.requests() {
		if r.path == path {
			n++
		}
	}
	return n
}

// addEvent 存一条带这些段的事件（形状和入库一站存的一样），platformID 非空就连上平台消息号。返回 seq
func (e *env) addEvent(segs []types.Seg, platformID string) int64 {
	e.t.Helper()
	return e.addEventAt(segs, platformID, e.clk.Now().UnixMilli())
}

func (e *env) addEventAt(segs []types.Seg, platformID string, at int64) int64 {
	e.t.Helper()
	body := struct {
		Segs []types.Seg `json:"segs"`
		From types.User  `json:"from"`
		Tags []string    `json:"tags,omitempty"`
		Line string      `json:"line,omitempty"`
	}{segs, types.User{ID: "10001", Nick: "编的昵称 & <b>"}, []string{"stranger"}, "编的一行 😀"}
	seq, err := e.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: testConv, ActorKind: "human", ActorID: "qq:10001", Origin: "peer",
		Kind: "message", Text: "t", Segments: body, CreatedAt: at}, []string{"a"})
	if err != nil {
		e.t.Fatal(err)
	}
	if platformID != "" {
		if _, err := e.st.Events.Exec(`INSERT INTO channel_msgs (channel, conversation, platform_id, event_seq) VALUES ('qq', ?, ?, ?)`, testConv, platformID, seq); err != nil {
			e.t.Fatal(err)
		}
	}
	return seq
}

func (e *env) body(seq int64) string {
	e.t.Helper()
	var b string
	if err := e.st.Events.QueryRow(`SELECT segments_json FROM events WHERE seq = ?`, seq).Scan(&b); err != nil {
		e.t.Fatal(err)
	}
	return b
}

// segHash 是事件里第 idx 个段现在的 hash
func (e *env) segHash(seq int64, idx int) string {
	e.t.Helper()
	var h *string
	if err := e.st.Events.QueryRow(`SELECT json_extract(segments_json, '$.segs[' || ? || '].hash') FROM events WHERE seq = ?`, idx, seq).Scan(&h); err != nil {
		e.t.Fatal(err)
	}
	if h == nil {
		return ""
	}
	return *h
}

func (e *env) row(seq int64, idx int) store.MediaFetch {
	e.t.Helper()
	rows, err := e.st.MediaFetchRows(seq)
	if err != nil {
		e.t.Fatal(err)
	}
	r, ok := rows[idx]
	if !ok {
		e.t.Fatalf("事件 %d 的第 %d 段没有 media_fetch 行", seq, idx)
	}
	return r
}

func (e *env) start() { e.f.Start() }

func (e *env) idle() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), envLimit)
	defer cancel()
	if err := e.f.Idle(ctx); err != nil {
		e.t.Fatalf("等了 %v 还有任务没做完：%v", envLimit, err)
	}
}

// envLimit 是真等后台任务的上限，只防卡死：卡住的任务（等假钟、等屏障）怎么等都做不完，照样红；
// 有的任务要真流 100 MiB、算哈希、写库，低优先级跑在忙机器上 10 秒不够（2026-11-09 合并队列红过）。
const envLimit = 2 * time.Minute

// waitFor 等条件成立（真等，最多 envLimit）
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(envLimit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func imgSeg(path string) types.Seg {
	return types.Seg{Kind: types.SegImage, URL: testSegURL(path), File: strings.TrimPrefix(path, "/") + ".jpg"}
}
