package llmtest

// 录制（表一 A12）：把请求转给真的网关，回复原样转回客户端、同时记下来，测试结束时写成录像（格式同 cassette.go）。
// 照 OpenCode 的 http-recorder 录制模式：只在本地录，CI 里报错不录；写进去的只有请求的方法、路径、请求体和回复的状态码、
// 回复体、几样要紧的回复头——请求头不记（key 在头里），回复头只留 content-type 和 x-magpie-*（别的可能带账号、追踪信息）

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

type recorder struct {
	t        TB
	file     string
	upstream string
	mu       sync.Mutex
	c        Cassette
}

// Record 返回录制的 handler：每个请求转给 upstream（比如 http://127.0.0.1:3425），回复原样转回去并记下；meta 写进录像的 metadata
func Record(t TB, file, upstream string, meta map[string]any) http.HandlerFunc {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Errorf("CI 里不录真实回复：录像 %s 不改", file)
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "CI 里不录", http.StatusInternalServerError)
		}
	}
	r := &recorder{t: t, file: file, upstream: strings.TrimSuffix(upstream, "/"), c: Cassette{Version: 1, Metadata: meta}}
	t.Cleanup(r.finish)
	return r.serve
}

// keptHeader：写进录像的回复头
func keptHeader(k string) bool {
	k = strings.ToLower(k)
	return k == "content-type" || strings.HasPrefix(k, "x-magpie-")
}

func (r *recorder) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	url := r.upstream + req.URL.Path
	if req.URL.RawQuery != "" {
		url += "?" + req.URL.RawQuery
	}
	up, err := http.NewRequestWithContext(req.Context(), req.Method, url, bytes.NewReader(body))
	if err != nil {
		r.t.Errorf("录制：转发请求建不了：%v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range req.Header {
		if k == "Accept-Encoding" { // 让 Go 自己要压缩、自己解开，记下的是原文
			continue
		}
		up.Header[k] = vs
	}
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		r.t.Errorf("录制：%s 连不上：%v", r.upstream, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	headers := map[string]string{}
	for k := range resp.Header {
		w.Header()[k] = resp.Header[k]
		if keptHeader(k) {
			headers[strings.ToLower(k)] = resp.Header.Get(k)
		}
	}
	w.WriteHeader(resp.StatusCode)
	var got bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	r.mu.Lock()
	r.c.Interactions = append(r.c.Interactions, Interaction{
		Request:  RecordedRequest{Method: req.Method, URL: req.URL.Path, Body: string(body)},
		Response: RecordedResponse{Status: resp.StatusCode, Headers: headers, Body: got.String()},
	})
	r.mu.Unlock()
}

func (r *recorder) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.c.Interactions) == 0 {
		r.t.Errorf("录制：一次请求都没有，录像 %s 不写", r.file)
		return
	}
	b, err := json.MarshalIndent(r.c, "", "  ")
	if err == nil {
		err = os.WriteFile(r.file, append(b, '\n'), 0o644)
	}
	if err != nil {
		r.t.Errorf("录像 %s 写不了：%v", r.file, err)
	}
}
