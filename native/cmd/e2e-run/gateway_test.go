package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
)

func TestGatewayUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 照 Magpie：带转发头的请求当成外面转来的，本机免密钥不再适用，回 401。代理不能加这类头。
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
			http.Error(w, "forwarded request needs a gateway key", 401)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "wrong path", 404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-magpie-provider", "fictional-provider")
		// 故意从 JSON 中间切开，证明统计不依赖网络读块边界。
		for _, part := range []string{
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"编的回复\"},\"finish_reason\":null}]}\n\n",
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":20,\"prompt_tokens_det",
			"ails\":{\"cached_tokens\":80,\"cache_write_tokens\":5},\"completion_tokens_details\":{\"reasoning_tokens\":7}}}\n\ndata: [DONE]\n\n",
		} {
			io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()
	recorder := &callRecorder{}
	proxy, err := newGateway(upstream.URL+"/v1", recorder)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := llm.New(llm.Magpie(proxy.URL)).StreamChat(ctx, chat.Request{Model: "fictional-model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "编的回复" {
		t.Fatalf("代理改变回复：%q", result.Text)
	}
	got := recorder.snapshot()
	if len(got) != 1 {
		t.Fatalf("请求数=%d，要 1", len(got))
	}
	c := got[0]
	if c.Status != 200 || !c.UsageSeen || c.Prompt != 120 || c.Output != 20 || c.CacheRead != 80 || c.CacheWrite != 5 || c.Reasoning != 7 || c.Provider != "fictional-provider" {
		t.Fatalf("用量丢失、重复或缓存口径错误：%+v", c)
	}
}
