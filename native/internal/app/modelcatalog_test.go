package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/paths"
)

// catalogGate 是模型目录后台重试的可控等待：要等时把时长发到 waits，测试放行一次再往下走（不靠墙钟）
type catalogGate struct {
	waits   chan time.Duration
	release chan struct{}
}

func newCatalogGate() *catalogGate {
	return &catalogGate{waits: make(chan time.Duration, 16), release: make(chan struct{}, 16)}
}

func (g *catalogGate) wait(ctx context.Context, d time.Duration) bool {
	select {
	case g.waits <- d:
	case <-ctx.Done():
		return false
	}
	select {
	case <-g.release:
		return true
	case <-ctx.Done():
		return false
	}
}

// catalogServer 是一个能在同一个地址关掉、再开起来的模型服务：关着时就是连不上（相当于 Magpie 没开）
type catalogServer struct {
	addr string
	mu   sync.Mutex
	srv  *http.Server
}

func newCatalogServer(t *testing.T) *catalogServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return &catalogServer{addr: addr}
}

// start 在原地址起来，/models 回 data 里的模型
func (s *catalogServer) start(t *testing.T, data string) {
	t.Helper()
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		t.Fatalf("在 %s 起假模型服务：%v", s.addr, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":`+data+`}`)
	})}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func (s *catalogServer) url() string { return "http://" + s.addr + "/v1" }

// 来源关着启动：照常起来（进接收中），模型能力取自上一份缓存，ReadAt 是缓存的时间（规格 6.1 验收第 1 条）
func TestModelCatalogCacheKeepsStartupRunning(t *testing.T) {
	e := newEnv(t)
	// 先活着一遍：列模型成功、把目录写进缓存
	a := e.open()
	if cur := a.currentModels(); cur.CatalogError != "" || len(cur.Models) != 1 {
		t.Fatalf("活着的假网关没读成：%+v", cur)
	}
	a.Close()
	e.llm.Close() // 来源关掉：下次启动列模型连不上
	b := e.start()
	cur := b.currentModels()
	if cur.CatalogError == "" {
		t.Fatal("来源关了，CatalogError 该有原因")
	}
	if len(cur.Models) != 1 || cur.Models[0].ID != testModel || cur.Models[0].Window() != 32768 {
		t.Fatalf("模型能力没取自缓存：%+v", cur.Models)
	}
	if !cur.ReadAt.Equal(e.nowAt()) {
		t.Fatalf("ReadAt 该是缓存读到的时间 %v，现在是 %v", e.nowAt(), cur.ReadAt)
	}
	if !e.logContains("的缓存") {
		t.Fatalf("用缓存要在日志里说一声，现在的日志：%v", e.logSnapshot())
	}
}

// 启动后把来源打开：退避时间内自己恢复，CatalogError 清空、目录换成新的、缓存也换了，不用重启（验收第 2 条）
func TestModelCatalogRecoversWithoutRestart(t *testing.T) {
	e := newEnv(t)
	srv := newCatalogServer(t) // 现在关着
	gate := newCatalogGate()
	lines := make(chan string, 128)
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.url()))
	opt.CatalogRetry = CatalogRetry{Wait: gate.wait}
	opt.Logf = func(f string, v ...any) {
		e.logf(f, v...)
		select {
		case lines <- fmt.Sprintf(f, v...):
		default:
		}
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("目录取不到不能挡启动：%v", err)
	}
	t.Cleanup(func() { a.Close() })
	if a.currentModels().CatalogError == "" {
		t.Fatal("来源关着，CatalogError 该有原因")
	}
	srv.start(t, `[{"id":"recovered/1","context_length":64000},{"id":"recovered/2"}]`)
	select {
	case d := <-gate.waits:
		if d != 30*time.Second {
			t.Fatalf("第一次重试前该等 30 秒，等了 %v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("后台没开始按退避重试")
	}
	gate.release <- struct{}{}
	deadline := time.After(10 * time.Second)
	for waiting := true; waiting; {
		select {
		case line := <-lines:
			if strings.Contains(line, "模型来源恢复了") {
				waiting = false
			}
		case <-deadline:
			t.Fatalf("来源回来了没恢复，现在的日志：%v", e.logSnapshot())
		}
	}
	cur := a.currentModels()
	if cur.CatalogError != "" || len(cur.Models) != 2 || cur.Models[0].ID != "recovered/1" || cur.Models[0].Window() != 64000 {
		t.Fatalf("恢复后的目录不对：%q %+v", cur.CatalogError, cur.Models)
	}
	if !cur.ReadAt.Equal(e.nowAt()) {
		t.Fatalf("恢复后 ReadAt 该是这次读到的时间 %v，现在是 %v", e.nowAt(), cur.ReadAt)
	}
	b, err := os.ReadFile(a.paths.ModelCatalogCache())
	if err != nil {
		t.Fatalf("恢复后没写缓存：%v", err)
	}
	var c modelCatalogCache
	if err := json.Unmarshal(b, &c); err != nil || c.Source != srv.url() || len(c.Models) != 2 || c.Models[0].ID != "recovered/1" {
		t.Fatalf("缓存没换成新的（%v）：%s", err, b)
	}
}

// 列模型超时、超大、非 JSON：各报对的错，都不挡启动（验收第 3 条）
func TestModelCatalogFailuresDoNotBlockStartup(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		handler http.HandlerFunc
		want    string
	}{
		{"超时", 50 * time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}, "超时"},
		{"超大", 0, func(w http.ResponseWriter, r *http.Request) {
			chunk := make([]byte, 64<<10)
			for i := range chunk {
				chunk[i] = 'x'
			}
			for written := 0; written <= llm.ModelsMaxBytes; written += len(chunk) {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		}, "模型列表太大"},
		{"非 JSON", 0, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>不是模型列表</html>"))
		}, "读不懂"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			srv := httptest.NewServer(c.handler)
			t.Cleanup(srv.Close)
			opt := e.options()
			client := llm.New(llm.Magpie(srv.URL))
			client.ModelsTimeout = c.timeout
			opt.LLM = client
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatalf("列模型%s挡了启动：%v", c.name, err)
			}
			t.Cleanup(func() { a.Close() })
			if cur := a.currentModels(); !strings.Contains(cur.CatalogError, c.want) {
				t.Fatalf("CatalogError = %q，想要包含 %q", cur.CatalogError, c.want)
			}
		})
	}
}

// 缓存的来源地址和现在配置的不一样：不用（验收第 4 条）
func TestModelCatalogCacheIgnoresOtherSource(t *testing.T) {
	e := newEnv(t)
	p, err := paths.Open(e.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModelCatalogCache(p.ModelCatalogCache(), "http://elsewhere.example/v1", e.nowAt(), []llm.Model{{ID: "stale/1", ContextLength: 4096}}); err != nil {
		t.Fatal(err)
	}
	e.llm.Close()
	a := e.open()
	defer a.Close()
	cur := a.currentModels()
	if cur.CatalogError == "" || len(cur.Models) != 0 {
		t.Fatalf("地址对不上的缓存不该用：%+v", cur)
	}
	if !e.logContains("另一个来源") {
		t.Fatalf("不用缓存的原因要记一行，现在的日志：%v", e.logSnapshot())
	}
}

// 目录好的、角色用的模型不在列表：照常进接收中，日志说一次；发消息才报"不在模型列表"（验收第 5 条）
func TestModelNotInCatalogDoesNotBlockStartup(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"other/one","context_length":8192}]}`)
	}))
	t.Cleanup(srv.Close)
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("模型不在列表不能挡启动：%v", err)
	}
	defer a.Close()
	if cur := a.currentModels(); cur.CatalogError != "" {
		t.Fatalf("目录是好的，CatalogError 该是空的：%q", cur.CatalogError)
	}
	if !e.logContains("不在模型的列表里") {
		t.Fatalf("模型不在列表要在日志里说一次，现在的日志：%v", e.logSnapshot())
	}
	out := <-a.hub.Send(testChar, "在吗")
	if out.Err == nil || !strings.Contains(out.Err.Error(), "模型列表里没有") {
		t.Fatalf("发消息该报不在模型列表：%v", out.Err)
	}
}

// 目录取不到时发消息：报"模型来源连不上"，不报"模型不存在"（验收第 6 条）
func TestSendWithoutCatalogReportsSourceDown(t *testing.T) {
	e := newEnv(t)
	e.llm.Close()
	a := e.open()
	defer a.Close()
	if cur := a.currentModels(); cur.CatalogError == "" {
		t.Fatal("来源关着，CatalogError 该有原因")
	}
	out := <-a.hub.Send(testChar, "在吗")
	if out.Err == nil || !strings.Contains(out.Err.Error(), "模型来源连不上") {
		t.Fatalf("发消息该报模型来源连不上：%v", out.Err)
	}
	if strings.Contains(out.Err.Error(), "模型列表里没有") {
		t.Fatalf("目录取不到不该说模型不存在：%v", out.Err)
	}
}

// 退避的节奏照规格：30 秒、1 分、2 分、5 分，之后一直 5 分（6.1 第 5 条）
func TestCatalogRetryDelays(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}
	if len(catalogRetryDelays) != len(want) {
		t.Fatalf("退避有 %d 档，想要 %d 档", len(catalogRetryDelays), len(want))
	}
	for i := range want {
		if catalogRetryDelays[i] != want[i] {
			t.Fatalf("第 %d 档是 %v，想要 %v", i+1, catalogRetryDelays[i], want[i])
		}
	}
}
