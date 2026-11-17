package llm

// 请求字节对拍（规格-模型配置 M1 验收 1）：同一份只有 magpie.baseUrl 的旧配置，发给模型网关的
// 方法、路径、主机、请求头、请求体要逐字节一致。
//
// 期望文件是 M1 改动之前（分支基点 97bb1d107，llm.Magpie 还是唯一来源的写法）录的；录法：
// 删掉 testdata/magpie-request.golden.json，在 native/ 里跑
//   SUMIKA_REQUEST_RECORD=1 bun ../scripts/slot.ts go test -short -count=1 ./internal/llm -run TestMagpieRequestBytes
// 只在请求形状有意改变、并在提交说明里登记后重录；平时不要更新。
//
// 主机名里的端口每次 httptest 都不同，统一记成 127.0.0.1:PORT；别的都原样。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/llm/chat"
)

const requestGolden = "testdata/magpie-request.golden.json"

type recordedRequest struct {
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	Host          string              `json:"host"`
	ContentLength int64               `json:"contentLength"`
	Headers       map[string][]string `json:"headers"`
	Body          string              `json:"body"`
}

func TestMagpieRequestBytes(t *testing.T) {
	var got recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		host := r.Host
		if u, err := url.Parse("//" + host); err == nil {
			host = u.Hostname() + ":PORT"
		}
		headers := make(map[string][]string, len(r.Header))
		for k, vs := range r.Header {
			headers[k] = slices.Clone(vs)
		}
		got = recordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Host: host, ContentLength: r.ContentLength, Headers: headers, Body: string(body)}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"index":0,"delta":{"content":"好"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// 只有 magpie.baseUrl、没有 providers: 的旧配置（迁移第 1 条）
	cfg, err := config.Parse([]byte("magpie:\n  baseUrl: "+srv.URL+"/v1\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	req := chat.Request{
		Model:      "group/auto-test",
		SystemRole: chat.SystemRoleDeveloper,
		Messages: []chat.Message{
			{Role: chat.RoleSystem, Blocks: []chat.Block{chat.Text("系统 <&>")}},
			{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("你好")}},
		},
		MaxOutputTokens: 1234,
		Thinking:        "high",
		CacheKey:        "p2-7:bytes",
		LongCache:       true,
		NoStore:         true,
		Headers:         map[string]string{"X-Test": "值"},
		Style:           chat.StyleForeground,
	}
	if _, err := New(Magpie(cfg.MagpieBaseURL())).StreamChat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.Body == "" {
		t.Fatalf("没录到请求：%+v", got)
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	want, err := os.ReadFile(requestGolden)
	if os.IsNotExist(err) && os.Getenv("SUMIKA_REQUEST_RECORD") != "" {
		if err := os.WriteFile(requestGolden, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("没有期望文件，已按这次请求录了一份；核对后重跑")
	}
	if err != nil {
		t.Fatal(err)
	}
	// 期望文件只是这份记录的文本形式：检出时 autocrlf 会把换行改成 CRLF（合并队列的 worktree 就是），比之前统一成 LF；
	// 请求本身的字节（正文、请求头、Content-Length）都在 JSON 的字符串里，不受影响
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(b, want) {
		t.Fatalf("发给网关的请求和 M1 之前录的不一样：\n--- 录的\n%s\n--- 这次\n%s", want, b)
	}
}
