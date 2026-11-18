package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/llm/chat"
)

// 配置里的 Headers 不许盖掉客户端自己设的保留头：带 Authorization 的请求根本不发，报本地错误（不重试、不切换）
func TestHeadersCannotOverrideReserved(t *testing.T) {
	var requests int
	var auth, client string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		auth, client = r.Header.Get("Authorization"), r.Header.Get("X-Client")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(Magpie(srv.URL + "/v1"))
	_, err := c.StreamChat(context.Background(), chat.Request{Model: "m", Style: chat.StyleBackground,
		Messages: []chat.Message{msg(chat.RoleUser, "x")},
		Headers:  map[string]string{"authorization": "Bearer 假的"}}, nil)
	var local *localError
	if err == nil || !errors.As(err, &local) || !strings.Contains(err.Error(), "authorization") {
		t.Fatalf("应该报本地错误、点名那个头：%v", err)
	}
	if ok, _ := Retryable(err); ok {
		t.Fatalf("本地错误不该重试：%v", err)
	}
	if requests != 0 {
		t.Fatalf("保留头被拒绝时不该发请求，发了 %d 次", requests)
	}

	// 允许的头照设；真正的 key 还在
	res, err := c.StreamChat(context.Background(), chat.Request{Model: "m", Style: chat.StyleBackground,
		Messages: []chat.Message{msg(chat.RoleUser, "x")},
		Headers:  map[string]string{"X-Client": "sumika"}}, nil)
	if err != nil || res.Text != "好" {
		t.Fatalf("普通头应该照发：%v %q", err, res.Text)
	}
	if auth != "Bearer magpie-sumika" || client != "sumika" {
		t.Fatalf("Authorization = %q，X-Client = %q", auth, client)
	}
}
