package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

type modelCall struct {
	Started                                          time.Time
	MS                                               int64
	Status                                           int
	Model, Provider, RouteModel                      string
	Prompt, Output, CacheRead, CacheWrite, Reasoning int64
	UsageSeen                                        bool
	Error                                            string
}
type callRecorder struct {
	mu    sync.Mutex
	calls []*modelCall
}

func (r *callRecorder) snapshot() []modelCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]modelCall, len(r.calls))
	for i, c := range r.calls {
		out[i] = *c
	}
	return out
}
func newGateway(target string, r *callRecorder) (*httptest.Server, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	// 不用 NewSingleHostReverseProxy：它的 Director 会加 X-Forwarded-For，Magpie 见到转发头就不再按本机直连免密钥，
	// 整轮回 401。Rewrite 只改目标地址（含 Host），不调 SetXForwarded，请求在网关看来和后台直连一样。
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(u) }}
	proxy.FlushInterval = -1
	proxy.ModifyResponse = func(resp *http.Response) error {
		if !strings.HasSuffix(resp.Request.URL.Path, "/chat/completions") {
			return nil
		}
		c := resp.Request.Context().Value(callKey{}).(*modelCall)
		r.mu.Lock()
		c.Status = resp.StatusCode
		c.Provider = resp.Header.Get("x-magpie-provider")
		c.RouteModel = resp.Header.Get("x-magpie-model")
		r.mu.Unlock()
		resp.Body = &usageBody{ReadCloser: resp.Body, r: r, c: c}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		if c, ok := req.Context().Value(callKey{}).(*modelCall); ok {
			r.mu.Lock()
			c.Error = err.Error()
			c.MS = time.Since(c.Started).Milliseconds()
			r.mu.Unlock()
		}
		http.Error(w, "gateway request failed", 502)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/chat/completions") {
			c := &modelCall{Started: time.Now()}
			r.mu.Lock()
			r.calls = append(r.calls, c)
			r.mu.Unlock()
			req = req.WithContext(context.WithValue(req.Context(), callKey{}, c))
		}
		proxy.ServeHTTP(w, req)
	})), nil
}

type callKey struct{}

// 每次只留半行 SSE；不会把长对话和模型思考复制进报告采样器。
type usageBody struct {
	io.ReadCloser
	r       *callRecorder
	c       *modelCall
	pending []byte
}

func (b *usageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.pending = append(b.pending, p[:n]...)
		for {
			at := bytes.IndexByte(b.pending, '\n')
			if at < 0 {
				break
			}
			b.line(b.pending[:at])
			b.pending = b.pending[at+1:]
		}
	}
	if err != nil && err != io.EOF {
		b.r.mu.Lock()
		b.c.Error = err.Error()
		b.r.mu.Unlock()
	}
	return n, err
}
func (b *usageBody) line(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	line = bytes.TrimSpace(line[5:])
	var chunk struct {
		Model string `json:"model"`
		Usage *struct {
			Prompt  int64  `json:"prompt_tokens"`
			Output  int64  `json:"completion_tokens"`
			Hit     *int64 `json:"prompt_cache_hit_tokens"`
			Cached  *int64 `json:"cached_tokens"`
			Details *struct {
				Cached *int64 `json:"cached_tokens"`
				Write  int64  `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
			Completion *struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &chunk) != nil {
		return
	}
	b.r.mu.Lock()
	defer b.r.mu.Unlock()
	if chunk.Model != "" {
		b.c.Model = chunk.Model
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		b.c.Error = string(chunk.Error)
	}
	if u := chunk.Usage; u != nil {
		b.c.UsageSeen = true
		b.c.Prompt = u.Prompt
		b.c.Output = u.Output
		if u.Details != nil && u.Details.Cached != nil {
			b.c.CacheRead = *u.Details.Cached
		} else if u.Hit != nil {
			b.c.CacheRead = *u.Hit
		} else if u.Cached != nil {
			b.c.CacheRead = *u.Cached
		}
		if u.Details != nil {
			b.c.CacheWrite = u.Details.Write
		}
		if u.Completion != nil {
			b.c.Reasoning = u.Completion.Reasoning
		}
	}
}
func (b *usageBody) Close() error {
	if len(b.pending) > 0 {
		b.line(b.pending)
		b.pending = nil
	}
	err := b.ReadCloser.Close()
	b.r.mu.Lock()
	b.c.MS = time.Since(b.c.Started).Milliseconds()
	b.r.mu.Unlock()
	return err
}
