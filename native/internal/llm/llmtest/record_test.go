package llmtest

// 录制自己的测试：用假的上游，不调真模型

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 转给上游、回复原样转回去；录像里是请求的方法、路径、请求体和回复原文，回复头只留 content-type 和 x-magpie-*，请求头不记；
// 录下来的能照样回放
func TestRecord(t *testing.T) {
	t.Setenv("CI", "")
	const sse = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"},\"finish_reason\":\"stop\"}]}\n\n: keepalive\n\ndata: [DONE]\n\n"
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Magpie-Provider", "某渠道")
		w.Header().Set("Set-Cookie", "session=秘密")
		w.Header().Set("X-Request-Account", "账号")
		io.WriteString(w, sse)
	}))
	defer up.Close()
	file := filepath.Join(t.TempDir(), "rec.json")
	f := &fakeTB{}
	srv := httptest.NewServer(Record(f, file, up.URL, map[string]any{"source": "测试"}))
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer 秘密钥匙")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	srv.Close()
	if string(b) != sse || gotAuth != "Bearer 秘密钥匙" {
		t.Fatalf("原样转发、原样转回：%q %q", b, gotAuth)
	}
	f.done()
	raw, _ := os.ReadFile(file)
	var c Cassette
	json.Unmarshal(raw, &c)
	if len(f.errs) != 0 || len(c.Interactions) != 1 || c.Metadata["source"] != "测试" {
		t.Fatalf("录了一次：%v %s", f.errs, raw)
	}
	it := c.Interactions[0]
	if it.Request != (RecordedRequest{Method: "POST", URL: "/v1/chat/completions", Body: `{"model":"m"}`}) || it.Response.Body != sse ||
		fmt.Sprint(it.Response.Headers) != fmt.Sprint(map[string]string{"content-type": "text/event-stream", "x-magpie-provider": "某渠道"}) {
		t.Fatalf("录像的内容：%+v", it)
	}
	if strings.Contains(string(raw), "秘密") || strings.Contains(string(raw), "账号") {
		t.Fatalf("请求头、别的回复头不进录像：%s", raw)
	}
	// 录下来的能照样回放
	g := &fakeTB{}
	rs := httptest.NewServer(Replay(g, file))
	defer rs.Close()
	if code, body := post(t, rs, `{"model":"m"}`); code != 200 || body != sse {
		t.Fatalf("回放：%d %q", code, body)
	}
	g.done()
	if len(g.errs) != 0 {
		t.Fatalf("回放没报错：%v", g.errs)
	}
}

// CI 里不录；一次请求都没有不写文件
func TestRecordRefused(t *testing.T) {
	t.Setenv("CI", "true")
	f := &fakeTB{}
	file := filepath.Join(t.TempDir(), "rec.json")
	Record(f, file, "http://127.0.0.1:1", nil)
	if !f.has("CI 里不录") {
		t.Fatalf("CI 里要报：%v", f.errs)
	}
	t.Setenv("CI", "")
	g := &fakeTB{}
	Record(g, file, "http://127.0.0.1:1", nil)
	g.done()
	if _, err := os.Stat(file); !os.IsNotExist(err) || !g.has("一次请求都没有") {
		t.Fatalf("没有请求不写文件：%v", g.errs)
	}
}
