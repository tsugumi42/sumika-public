package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/pool"
	"sumika-native/internal/localmodels/tokenizer"
)

// 这个包的测试默认不需要真 dll、真模型：假分词器加假会话，经过真的池和真的 HTTP 处理链。
// 真的在 realexe_test.go（cmd/sumika-models），用环境变量门着。

// 令牌是编的；拼出来而不是写成一整个串，免得隐私扫描把"赋值的密钥"当成真的
var testToken = "tok-" + strings.Repeat("ab12", 5)

// ---- 假分词器（和 engine 的测试里那份同一个口径：每个字符一个 token，U+200B 不出 token）----

type fakeTok struct {
	mu     sync.Mutex
	single int
	pair   int
	count  int
}

func fakeContent(text string) []int {
	var ids []int
	for _, r := range text {
		if r == 0x200b {
			continue
		}
		ids = append(ids, 10+int(r%5000))
	}
	return ids
}

func (f *fakeTok) EncodeSingle(text string, maxTokens int) ([]int, error) {
	f.mu.Lock()
	f.single++
	f.mu.Unlock()
	c := fakeContent(text)
	c = c[:min(len(c), maxTokens-2)]
	return append(append([]int{0}, c...), 2), nil
}

func (f *fakeTok) EncodePair(query, document string, maxTokens int) ([]int, error) {
	f.mu.Lock()
	f.pair++
	f.mu.Unlock()
	q := fakeContent(query)
	budget := maxTokens - tokenizer.RerankSpecialReserve
	if len(q)+2 > budget {
		return nil, &tokenizer.QueryTooLongError{Tokens: len(q) + 2, Budget: budget}
	}
	d := fakeContent(document)
	d = d[:min(len(d), maxTokens-4-len(q))]
	out := append([]int{0}, q...)
	out = append(out, 2, 2)
	out = append(out, d...)
	return append(out, 2), nil
}

func (f *fakeTok) Count(text string) (int, error) {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	return len(fakeContent(text)) + 2, nil
}

func (f *fakeTok) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.single + f.pair + f.count
}

// ---- 假会话 ----

// fakeSess 是 ort.Session：嵌入图给 [1,n,dim] 的隐藏状态，重排图给 [1,1] 的 logit。
// delay 让每次 Run 慢一点；block 非空时 Run 一直等到它关闭（模拟卡住的前向）；panicWith 非空时 Run 直接 panic 这个值。
type fakeSess struct {
	dim       int
	rerank    bool
	delay     time.Duration
	block     chan struct{}
	panicWith any          // 非空时前 panics 次 Run 直接 panic 这个值
	panics    atomic.Int32 // 还要 panic 几次
	runs      atomic.Int64
}

func (s *fakeSess) Inputs() []ort.TensorInfo {
	return []ort.TensorInfo{
		{Name: "input_ids", Type: ort.TypeInt64, Shape: []int64{-1, -1}},
		{Name: "attention_mask", Type: ort.TypeInt64, Shape: []int64{-1, -1}},
	}
}

func (s *fakeSess) Outputs() []ort.TensorInfo {
	if s.rerank {
		return []ort.TensorInfo{{Name: "logits", Type: ort.TypeFloat32}}
	}
	return []ort.TensorInfo{{Name: "token_embeddings", Type: ort.TypeFloat32}}
}

func (s *fakeSess) Close() error { return nil }

func (s *fakeSess) Run(inputs []ort.Int64Tensor, outputs []string) (map[string]ort.FloatTensor, error) {
	s.runs.Add(1)
	if s.panicWith != nil && s.panics.Add(-1) >= 0 {
		panic(s.panicWith)
	}
	if s.block != nil {
		<-s.block
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	var ids []int64
	for _, in := range inputs {
		if in.Name == "input_ids" {
			ids = in.Data
		}
	}
	var sum int64
	for _, id := range ids {
		sum += id
	}
	if s.rerank {
		return map[string]ort.FloatTensor{"logits": {Shape: []int64{1, 1}, Data: []float32{float32(sum%7) - 3}}}, nil
	}
	n := len(ids)
	data := make([]float32, n*s.dim)
	for r := 0; r < n; r++ {
		for j := 0; j < s.dim; j++ {
			if r == 0 {
				data[j] = float32(1+(sum+int64(j)*3)%11) * 0.25
			} else {
				data[r*s.dim+j] = float32(100 + r + j)
			}
		}
	}
	return map[string]ort.FloatTensor{"token_embeddings": {Shape: []int64{1, int64(n), int64(s.dim)}, Data: data}}, nil
}

// ---- 假登记项 ----

const (
	embedID  = "fake-embed"
	rerankID = "fake-rerank"
	otherID  = "fake-other" // 登记了但这次没点名
)

func fakeSpecs() (embed, rerank engine.Spec) {
	for _, s := range engine.BuiltinSpecs() {
		switch s.Kind {
		case engine.KindEmbed:
			embed = s
		case engine.KindRerank:
			rerank = s
		}
	}
	embed.ID, embed.MaxTokens = embedID, 1024   // 字符上限 4096；维度 768、截断下限 128 仍是 gte 的，好和 Python 的状态码一一对上
	rerank.ID, rerank.MaxTokens = rerankID, 128 // 字符上限 512，查询预算 112
	return embed, rerank
}

// ---- 测试台：真的池（假加载函数）、真的处理链 ----

type rig struct {
	t        *testing.T
	tok      *fakeTok
	embSess  *fakeSess
	rrSess   *fakeSess
	pool     *pool.Pool
	srv      *Server
	ts       *httptest.Server
	logs     *syncBuf
	loads    atomic.Int64
	loadErrs sync.Map // id → error：有就让这个模型加载失败
	embedSp  engine.Spec
	rerankSp engine.Spec
}

type rigOpts struct {
	maxLoaded  int
	wait       time.Duration
	maxRequest time.Duration
	embDelay   time.Duration
	rrDelay    time.Duration
	embBlock   chan struct{}
	panicWith  any
	problems   map[string]string // 启动时就知道的问题（--lazy-models 里文件缺了）
	only       []string          // 只提供这几个模型（缺省两个都提供）
	noMonitor  bool
	rss        func() (uint64, error)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, tok: &fakeTok{}, logs: &syncBuf{}}
	r.embedSp, r.rerankSp = fakeSpecs()
	r.embSess = &fakeSess{dim: r.embedSp.Dimension, delay: o.embDelay, block: o.embBlock, panicWith: o.panicWith}
	r.embSess.panics.Store(1)
	r.rrSess = &fakeSess{rerank: true, delay: o.rrDelay}
	mon := NewMonitor()

	served := []engine.Spec{r.embedSp, r.rerankSp}
	if len(o.only) > 0 {
		served = nil
		for _, sp := range []engine.Spec{r.embedSp, r.rerankSp} {
			for _, id := range o.only {
				if sp.ID == id {
					served = append(served, sp)
				}
			}
		}
	}
	entries := make([]pool.Entry, len(served))
	for i, sp := range served {
		entries[i] = pool.Entry{Spec: sp, Problem: o.problems[sp.ID]}
	}
	load := func(ctx context.Context, spec engine.Spec) (pool.Loaded, error) {
		r.loads.Add(1)
		if e, ok := r.loadErrs.Load(spec.ID); ok {
			return pool.Loaded{}, e.(error)
		}
		switch spec.Kind {
		case engine.KindEmbed:
			emb, err := engine.NewEmbedder(spec, r.tok, wrapSession(mon, r.embSess), engine.EmbedderOptions{})
			return pool.Loaded{Embedder: emb}, err
		default:
			rr, err := engine.NewReranker(spec, r.tok, wrapSession(mon, r.rrSess))
			return pool.Loaded{Reranker: rr}, err
		}
	}
	maxLoaded := o.maxLoaded
	if maxLoaded == 0 {
		maxLoaded = 2
	}
	wait := o.wait
	if wait == 0 {
		wait = 500 * time.Millisecond
	}
	p, err := pool.New(pool.Config{
		Models: entries, Load: load,
		Estimate:  func(engine.Spec) (uint64, error) { return 1 << 20, nil },
		MaxLoaded: maxLoaded, Wait: wait, LogOut: io.Discard,
		RSS: func() (uint64, error) { return 0, errors.New("测试里不量") },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.pool = p
	cfg := Config{
		Pool: p, Models: served, Registered: []string{embedID, rerankID, otherID}, Token: testToken, ORTVersion: "9.9.9-fake",
		MaxRequest: o.maxRequest, Log: log.New(r.logs, "", 0), RSS: o.rss,
	}
	if !o.noMonitor {
		cfg.Monitor = mon
	}
	r.srv, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.ts = httptest.NewServer(r.srv.Handler())
	t.Cleanup(func() {
		r.ts.Close()
		if o.embBlock != nil {
			select {
			case <-o.embBlock:
			default:
				close(o.embBlock)
			}
		}
		p.Close()
	})
	return r
}

// wrapSession 让假会话也经过监视器，和生产里"包一层 Runtime"等价。
func wrapSession(m *Monitor, s ort.Session) ort.Session { return monitoredSession{Session: s, m: m} }

// call 发一个带令牌的请求，返回状态码和响应体。body 为 nil 就不带正文。
func (r *rig) call(method, path string, body any) (int, []byte, http.Header) {
	r.t.Helper()
	return r.callWith(method, path, body, "Bearer "+testToken)
}

func (r *rig) callWith(method, path string, body any, auth string) (int, []byte, http.Header) {
	r.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			r.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, r.ts.URL+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s：%v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatalf("读 %s %s 的响应：%v", method, path, err)
	}
	return resp.StatusCode, out, resp.Header
}

// embed 发一个嵌入请求。
func (r *rig) embed(input any, extra map[string]any) (int, []byte) {
	r.t.Helper()
	body := map[string]any{"model": embedID, "input": input}
	for k, v := range extra {
		body[k] = v
	}
	code, out, _ := r.call(http.MethodPost, "/v1/embeddings", body)
	return code, out
}

func detailOf(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("响应不是 {\"detail\": …}：%s", body)
	}
	return e.Detail
}

func mustContain(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("%s：想要含 %q，实际 %q", what, s, got)
		}
	}
}

func fmtCodes(codes []int) string { return fmt.Sprint(codes) }
