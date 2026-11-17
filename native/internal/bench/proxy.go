package bench

// 记账代理（规格-表二执行器 第二节）：三个壳的模型请求都发给它，它给每个请求加上 X-Magpie-Session（这一次的编号）再原样转给
// Magpie，回复逐块转回、不攒（攒了首字时间就全是代理的）。跑完按这个头从 magpie usage 的 session 列把记录对回到每一次。
// 同一时刻只跑一个壳，所以标签是代理上的"当前这一次"，不用壳自己带

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// SessionHeader 是 Magpie 认的会话头（记进 usage 的 session 列，最长 128 个字）
const SessionHeader = "X-Magpie-Session"

// Proxy 是记账代理
type Proxy struct {
	upstream string
	srv      *http.Server
	ln       net.Listener
	mu       sync.Mutex
	tag      string
	counts   map[string]int
}

// StartProxy 在本机随便一个端口起代理，转给 upstream（如 http://127.0.0.1:3425）
func StartProxy(upstream string) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{upstream: strings.TrimSuffix(upstream, "/"), ln: ln, counts: map[string]int{}}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.serve)}
	go p.srv.Serve(ln)
	return p, nil
}

// URL 是代理的地址（不带 /v1）
func (p *Proxy) URL() string { return "http://" + p.ln.Addr().String() }

// SetTag 换"当前这一次"的编号；空的不打标签
func (p *Proxy) SetTag(tag string) {
	p.mu.Lock()
	p.tag = tag
	p.mu.Unlock()
}

// Count 是打了这个标签的请求数（POST 才算，列模型清单不算）
func (p *Proxy) Count(tag string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[tag]
}

// Close 关掉代理
func (p *Proxy) Close() error { return p.srv.Close() }

func (p *Proxy) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	url := p.upstream + req.URL.Path
	if req.URL.RawQuery != "" {
		url += "?" + req.URL.RawQuery
	}
	up, err := http.NewRequestWithContext(req.Context(), req.Method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range req.Header {
		if k == "Accept-Encoding" { // 让 Go 自己要压缩、自己解开，转回去的是原文
			continue
		}
		up.Header[k] = vs
	}
	p.mu.Lock()
	tag := p.tag
	if tag != "" && req.Method == http.MethodPost {
		p.counts[tag]++
	}
	p.mu.Unlock()
	if tag != "" {
		up.Header.Set(SessionHeader, tag)
	}
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
