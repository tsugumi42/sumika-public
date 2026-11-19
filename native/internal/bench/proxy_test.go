package bench

// 记账代理：加上 X-Magpie-Session、原样转发请求和回复、逐块转回（上游还没说完，客户端已经收到第一块）、按标签数请求

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxy(t *testing.T) {
	release := make(chan struct{})
	var gotSession, gotAuth, gotBody, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession, gotAuth, gotPath = r.Header.Get(SessionHeader), r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Magpie-Provider", "某渠道")
		io.WriteString(w, "data: 第一块\n\n")
		w.(http.Flusher).Flush()
		<-release // 第二块等测试看到第一块以后再放
		io.WriteString(w, "data: 第二块\n\n")
	}))
	defer up.Close()
	rel := sync.OnceFunc(func() { close(release) })
	defer rel() // 测试中途失败也先放行上游，不然关上游时卡住
	p, err := StartProxy(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetTag("t2-1-b1-go-1")
	req, _ := http.NewRequest("POST", p.URL()+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer magpie-local")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req) // 代理攒着不回时连响应头都不来，到点就红
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		l, _ := rd.ReadString('\n')
		first <- l
	}()
	select {
	case l := <-first:
		if l != "data: 第一块\n" {
			t.Fatalf("第一块：%q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("上游还没说完，第一块要先到（逐块转回）")
	}
	rel()
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), "第二块") || resp.Header.Get("X-Magpie-Provider") != "某渠道" {
		t.Fatalf("其余原样转回：%q %v", rest, resp.Header)
	}
	if gotSession != "t2-1-b1-go-1" || gotAuth != "Bearer magpie-local" || gotBody != `{"model":"m"}` || gotPath != "/v1/chat/completions" {
		t.Fatalf("转给上游的：%q %q %q %q", gotSession, gotAuth, gotBody, gotPath)
	}
	// 列模型清单不算请求；换了标签按新的算；没有标签不加头
	http.Get(p.URL() + "/v1/models")
	if p.Count("t2-1-b1-go-1") != 1 {
		t.Fatalf("POST 才算：%d", p.Count("t2-1-b1-go-1"))
	}
	p.SetTag("")
	http.Get(p.URL() + "/v1/models")
	if gotSession != "" {
		t.Fatalf("没有标签不加头：%q", gotSession)
	}
}
