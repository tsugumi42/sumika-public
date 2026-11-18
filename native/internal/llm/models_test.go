package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 列模型超时：服务端一直不回（或者回了头、正文不动），到时限报"超时"，不把整次启动卡住（规格-模型配置 6.1 第 1 条、验收第 3 条）
func TestModelsTimesOut(t *testing.T) {
	t.Run("等不到回复", func(t *testing.T) {
		cancelled := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done() // 一直等到请求被取消（读数照 ctx 结束，不靠墙钟）
			close(cancelled)
		}))
		defer srv.Close()
		c := New(Magpie(srv.URL))
		c.ModelsTimeout = 100 * time.Millisecond
		_, err := c.Models(context.Background())
		if err == nil || !strings.Contains(err.Error(), "超时") || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("超时没有报对：%v", err)
		}
		select {
		case <-cancelled:
		case <-time.After(5 * time.Second):
			t.Fatal("请求取消了，服务端没收到 ctx 结束")
		}
	})
	t.Run("正文读到一半不动", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush() // 头先到，正文一直不写
			<-r.Context().Done()
		}))
		defer srv.Close()
		c := New(Magpie(srv.URL))
		c.ModelsTimeout = 100 * time.Millisecond
		_, err := c.Models(context.Background())
		if err == nil || !strings.Contains(err.Error(), "超时") || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("读到一半卡住没有报超时：%v", err)
		}
	})
}

// 列模型响应超过 8 MB：报"模型列表太大"，不整份读进来（验收第 3 条）
func TestModelsTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for written := 0; written <= ModelsMaxBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	_, err := New(Magpie(srv.URL)).Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "模型列表太大") {
		t.Fatalf("超大响应没有报对：%v", err)
	}
}

// 列模型回了 200 但不是 JSON：报"读不懂"（验收第 3 条）
func TestModelsUnreadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>不是模型列表</html>"))
	}))
	defer srv.Close()
	_, err := New(Magpie(srv.URL)).Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "读不懂") {
		t.Fatalf("非 JSON 没有报对：%v", err)
	}
}

// 列模型的时限按来源地址定：回环 3 秒、其余 10 秒（第六节探测时限）
func TestModelsTimeoutByAddress(t *testing.T) {
	for _, c := range []struct {
		base string
		want time.Duration
	}{
		{"http://127.0.0.1:3425/v1", ModelsLocalTimeout},
		{"http://127.9.9.9:1/v1", ModelsLocalTimeout},
		{"http://[::1]:3425/v1", ModelsLocalTimeout},
		{"http://localhost:3425/v1", ModelsLocalTimeout},
		{"http://10.0.0.5:8080/v1", ModelsRemoteTimeout},
		{"https://api.example.com/v1", ModelsRemoteTimeout},
		{"不是地址", ModelsRemoteTimeout},
	} {
		if got := modelsTimeout(c.base); got != c.want {
			t.Errorf("%s 的时限是 %v，想要 %v", c.base, got, c.want)
		}
	}
}
