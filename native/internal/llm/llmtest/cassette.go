package llmtest

// 录像回放（规格-Go后台 8.2 表一 A12"模型接口兼容"）：照 OpenCode 的 http-recorder（packages/http-recorder：recorder.ts、
// http/matching.ts、replay/state.ts）。录像是一份 JSON：version、metadata、interactions，每次交互是请求（方法、路径、请求体）和
// 回复（状态码、头、原样的回复体，SSE 就是 data: 那几行）。回放时每个请求在还没用过的交互里取第一个对得上的（方法、路径一样，
// 请求体是 JSON 的按 JSON 比、键的先后不算），照录下的回；对不上、用完了都回 500 并报错，测试结束时有没用过的也报错
// （测试已经红了的不再报，免得盖住真正的错）。
//
// 请求体可以在本地重写成这一次发的（环境变量 SUMIKA_RECORD_REQUESTS=1：照先后把还没用过的那几次的请求换成这一次的，回复不动，
// 测试结束时写回文件）；CI 里（CI 有值）不许重写，照常回放、对不上照样红。回复体是录下来的原样，重写时不碰

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Cassette 是一份录像
type Cassette struct {
	Version      int            `json:"version"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Interactions []Interaction  `json:"interactions"`
}

// Interaction 是一次请求和它的回复
type Interaction struct {
	Request  RecordedRequest  `json:"request"`
	Response RecordedResponse `json:"response"`
}

// RecordedRequest 是录下的请求：路径不带主机（回放的假网关每次端口不一样），请求体原样
type RecordedRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// RecordedResponse 是录下的回复，回复体原样
type RecordedResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// TB 是回放要用的几样测试方法（*testing.T 就有；回放自己的测试换成记下来的）
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Cleanup(func())
	Failed() bool
}

// RecordEnv 设成 1 时在本地重写录像里的请求体（见包注释）
const RecordEnv = "SUMIKA_RECORD_REQUESTS"

type replayer struct {
	t       TB
	file    string
	mu      sync.Mutex
	c       Cassette
	used    []bool
	update  bool
	changed bool
}

// Replay 读 file 里的录像，返回回放它的 handler
func Replay(t TB, file string) http.HandlerFunc {
	t.Helper()
	r := &replayer{t: t, file: file}
	b, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(b, &r.c)
	}
	if err != nil {
		t.Errorf("录像 %s 读不了：%v", file, err)
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "录像读不了", http.StatusInternalServerError)
		}
	}
	r.used = make([]bool, len(r.c.Interactions))
	if os.Getenv(RecordEnv) == "1" {
		if os.Getenv("CI") != "" {
			t.Errorf("CI 里不许重写录像（%s=1）：录像 %s 照常回放", RecordEnv, file)
		} else {
			r.update = true
		}
	}
	t.Cleanup(r.finish)
	return r.serve
}

func (r *replayer) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	i := -1
	for k, it := range r.c.Interactions {
		if r.used[k] {
			continue
		}
		if r.update {
			r.c.Interactions[k].Request = RecordedRequest{Method: req.Method, URL: req.URL.Path, Body: string(body)}
			r.changed = true
			i = k
			break
		}
		if it.Request.Method == req.Method && it.Request.URL == req.URL.Path && sameBody(it.Request.Body, string(body)) {
			i = k
			break
		}
	}
	if i < 0 {
		msg := r.mismatch(req, string(body))
		r.mu.Unlock()
		r.t.Errorf("%s", msg)
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	r.used[i] = true
	resp := r.c.Interactions[i].Response
	r.mu.Unlock()
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, resp.Body)
}

// mismatch 说这一次为什么对不上：都用完了，或者和还没用过的第一次哪里不一样（最多列 8 处）
func (r *replayer) mismatch(req *http.Request, body string) string {
	var first *RecordedRequest
	for k := range r.c.Interactions {
		if !r.used[k] {
			first = &r.c.Interactions[k].Request
			break
		}
	}
	if first == nil {
		return fmt.Sprintf("录像 %s 的 %d 次都用完了，又来了一次 %s %s", r.file, len(r.c.Interactions), req.Method, req.URL.Path)
	}
	var diffs []string
	if first.Method != req.Method || first.URL != req.URL.Path {
		diffs = append(diffs, fmt.Sprintf("录的是 %s %s，来的是 %s %s", first.Method, first.URL, req.Method, req.URL.Path))
	}
	diffs = append(diffs, bodyDiff(first.Body, body)...)
	if len(diffs) > 8 {
		diffs = append(diffs[:8], "……")
	}
	return fmt.Sprintf("请求对不上录像 %s（和还没用过的第一次比）：%s", r.file, strings.Join(diffs, "；"))
}

func (r *replayer) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.update && r.changed {
		b, err := json.MarshalIndent(r.c, "", "  ")
		if err == nil {
			err = os.WriteFile(r.file, append(b, '\n'), 0o644)
		}
		if err != nil {
			r.t.Errorf("录像 %s 写不回去：%v", r.file, err)
		}
	}
	if r.t.Failed() {
		return
	}
	n := 0
	for _, u := range r.used {
		if u {
			n++
		}
	}
	if n < len(r.used) {
		r.t.Errorf("录像 %s 没用完：用了 %d / %d", r.file, n, len(r.used))
	}
}

// sameBody：两边都是 JSON 的按 JSON 比（键的先后不算），不是的比原文
func sameBody(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil {
		return reflect.DeepEqual(x, y)
	}
	return a == b
}

// bodyDiff 列出两份请求体不一样的地方（JSON 路径）
func bodyDiff(want, got string) []string {
	var x, y any
	if json.Unmarshal([]byte(want), &x) != nil || json.Unmarshal([]byte(got), &y) != nil {
		if want == got {
			return nil
		}
		return []string{"请求体不一样（不是 JSON，按原文比）"}
	}
	var out []string
	var walk func(path string, a, b any)
	walk = func(path string, a, b any) {
		switch av := a.(type) {
		case map[string]any:
			bv, ok := b.(map[string]any)
			if !ok {
				out = append(out, path+" 类型不一样")
				return
			}
			keys := map[string]bool{}
			for k := range av {
				keys[k] = true
			}
			for k := range bv {
				keys[k] = true
			}
			var ks []string
			for k := range keys {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				walk(path+"."+k, av[k], bv[k])
			}
		case []any:
			bv, ok := b.([]any)
			if !ok || len(av) != len(bv) {
				out = append(out, fmt.Sprintf("%s 条数不一样", path))
				return
			}
			for i := range av {
				walk(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])
			}
		default:
			if !reflect.DeepEqual(a, b) {
				out = append(out, fmt.Sprintf("%s 录的是 %v，来的是 %v", path, a, b))
			}
		}
	}
	walk("$", x, y)
	return out
}
