package llmtest

// 录像回放自己的测试（表一 A12 的过线：录像对不上、用完、没用完都报错）

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

// fakeTB 记下报的错、收尾的函数，测试自己决定什么时候收尾
type fakeTB struct {
	errs     []string
	cleanups []func()
	failed   bool
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
	f.failed = true
}
func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }
func (f *fakeTB) Failed() bool      { return f.failed }
func (f *fakeTB) done() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

func (f *fakeTB) has(sub string) bool {
	for _, e := range f.errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// cassette 写一份录像：每一项是（请求体，回复体）
func cassette(t *testing.T, pairs ...[2]string) string {
	t.Helper()
	c := Cassette{Version: 1}
	for _, p := range pairs {
		c.Interactions = append(c.Interactions, Interaction{
			Request:  RecordedRequest{Method: "POST", URL: "/v1/chat/completions", Body: p[0]},
			Response: RecordedResponse{Status: 200, Headers: map[string]string{"content-type": "text/event-stream"}, Body: p[1]},
		})
	}
	b, _ := json.Marshal(c)
	file := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func post(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func serve(t *testing.T, f *fakeTB, file string) *httptest.Server {
	srv := httptest.NewServer(Replay(f, file))
	t.Cleanup(srv.Close)
	return srv
}

// 每个请求取还没用过的、对得上的第一个：同样的请求按录的先后用掉；键的先后不算；都用完了没有错
func TestReplayInOrder(t *testing.T) {
	f := &fakeTB{}
	srv := serve(t, f, cassette(t, [2]string{`{"a":1,"b":2}`, "一"}, [2]string{`{"c":3}`, "二"}, [2]string{`{"a":1,"b":2}`, "三"}))
	for _, c := range []struct{ body, want string }{{`{"c":3}`, "二"}, {`{"b":2,"a":1}`, "一"}, {`{"a":1,"b":2}`, "三"}} {
		if code, got := post(t, srv, c.body); code != 200 || got != c.want {
			t.Fatalf("%s：%d %q，应该是 %q", c.body, code, got, c.want)
		}
	}
	f.done()
	if len(f.errs) != 0 {
		t.Fatalf("都用完了不该报错：%v", f.errs)
	}
}

// 对不上：回 500，报出哪里不一样
func TestReplayMismatch(t *testing.T) {
	f := &fakeTB{}
	srv := serve(t, f, cassette(t, [2]string{`{"model":"m","messages":[{"content":"在吗"}]}`, "一"}))
	if code, _ := post(t, srv, `{"model":"m","messages":[{"content":"你好"}]}`); code != 500 {
		t.Fatalf("对不上要回 500：%d", code)
	}
	if !f.has("对不上") || !f.has("$.messages[0].content 录的是 在吗，来的是 你好") {
		t.Fatalf("要说出哪里不一样：%v", f.errs)
	}
	// 请求体一样、路径不一样也算对不上
	resp, err := http.Post(srv.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"m","messages":[{"content":"在吗"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 500 || !f.has("来的是 POST /v1/embeddings") {
		t.Fatalf("路径不一样：%d %v", resp.StatusCode, f.errs)
	}
}

// 用完了又来一次：回 500，报"用完了"
func TestReplayExhausted(t *testing.T) {
	f := &fakeTB{}
	srv := serve(t, f, cassette(t, [2]string{`{"a":1}`, "一"}))
	post(t, srv, `{"a":1}`)
	if code, _ := post(t, srv, `{"a":1}`); code != 500 || !f.has("都用完了") {
		t.Fatalf("用完了：%d %v", code, f.errs)
	}
}

// 没用完：收尾时报；测试已经红了的不再报
func TestReplayUnused(t *testing.T) {
	f := &fakeTB{}
	srv := serve(t, f, cassette(t, [2]string{`{"a":1}`, "一"}, [2]string{`{"a":2}`, "二"}))
	post(t, srv, `{"a":1}`)
	f.done()
	if !f.has("没用完：用了 1 / 2") {
		t.Fatalf("没用完要报：%v", f.errs)
	}
	g := &fakeTB{}
	srv = serve(t, g, cassette(t, [2]string{`{"a":1}`, "一"}, [2]string{`{"a":2}`, "二"}))
	post(t, srv, `{"a":9}`) // 对不上，测试已经红了
	g.done()
	if len(g.errs) != 1 || g.has("没用完") {
		t.Fatalf("已经红了的不再报没用完：%v", g.errs)
	}
}

// 本地重写：照先后把请求换成这一次的，回复不动，收尾写回文件；CI 里不许
func TestReplayRecordRequests(t *testing.T) {
	t.Setenv(RecordEnv, "1")
	t.Setenv("CI", "")
	f := &fakeTB{}
	file := cassette(t, [2]string{`{"old":1}`, "一"}, [2]string{`{"old":2}`, "二"})
	srv := serve(t, f, file)
	for i, want := range []string{"一", "二"} {
		if _, got := post(t, srv, fmt.Sprintf(`{"new":%d}`, i)); got != want {
			t.Fatalf("第 %d 次：%q", i, got)
		}
	}
	f.done()
	var c Cassette
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &c)
	if len(f.errs) != 0 || c.Interactions[0].Request.Body != `{"new":0}` || c.Interactions[1].Request.Body != `{"new":1}` ||
		c.Interactions[1].Response.Body != "二" || c.Interactions[0].Request.URL != "/v1/chat/completions" {
		t.Fatalf("请求换成这一次的、回复不动：%+v %v", c.Interactions, f.errs)
	}

	t.Setenv("CI", "true")
	g := &fakeTB{}
	file = cassette(t, [2]string{`{"old":1}`, "一"})
	srv = serve(t, g, file)
	if code, _ := post(t, srv, `{"new":0}`); code != 500 || !g.has("CI 里不许重写录像") || !g.has("对不上") {
		t.Fatalf("CI 里照常回放、对不上照样红：%d %v", code, g.errs)
	}
	g.done()
	b, _ = os.ReadFile(file)
	if !strings.Contains(string(b), `{\"old\":1}`) {
		t.Fatalf("CI 里不改文件：%s", b)
	}
}
