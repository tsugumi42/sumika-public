package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 验收 1：令牌 ----

var authRoutes = []struct {
	method, path, body string
}{
	{http.MethodGet, "/health", ""},
	{http.MethodGet, "/v1/models", ""},
	{http.MethodPost, "/v1/embeddings", `{"model":"fake-embed","input":"你好"}`},
	{http.MethodPost, "/v1/rerank", `{"model":"fake-rerank","query":"天气","documents":["晴"]}`},
	{http.MethodPost, "/v1/models/fake-embed/unload", ""},
}

func TestAuth(t *testing.T) {
	r := newRig(t, rigOpts{})
	bad := []struct{ name, header string }{
		{"缺头", ""},
		{"错令牌", "Bearer wrong-" + testToken},
		{"错方案 Basic", "Basic " + testToken},
		{"方案小写", "bearer " + testToken},
		{"方案和令牌之间两个空格", "Bearer  " + testToken},
		{"只有令牌没有方案", testToken},
		{"只有方案", "Bearer"},
	}
	for _, route := range authRoutes {
		for _, b := range bad {
			code, body, hdr := r.callWith(route.method, route.path, route.body, b.header)
			if code != http.StatusUnauthorized {
				t.Errorf("%s %s（%s）：想要 401，实际 %d", route.method, route.path, b.name, code)
				continue
			}
			if hdr.Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s %s（%s）：401 要带 WWW-Authenticate: Bearer，实际 %q", route.method, route.path, b.name, hdr.Get("WWW-Authenticate"))
			}
			if strings.Contains(string(body), testToken) {
				t.Errorf("%s %s（%s）：401 的正文里有令牌：%s", route.method, route.path, b.name, body)
			}
			mustContain(t, "401 的正文", detailOf(t, body), "Authorization")
		}
	}
	if r.loads.Load() != 0 || r.tok.calls() != 0 {
		t.Errorf("没带对令牌的请求不该进到模型：加载 %d 次，分词 %d 次", r.loads.Load(), r.tok.calls())
	}
	// 不存在的路径、OPTIONS 没带令牌也是 401，且不回 CORS 头（令牌在路由前面）
	if code, _, _ := r.callWith(http.MethodGet, "/nope", nil, ""); code != http.StatusUnauthorized {
		t.Errorf("不存在的路径没带令牌：想要 401，实际 %d", code)
	}
	code, _, hdr := r.callWith(http.MethodOptions, "/v1/embeddings", nil, "")
	if code != http.StatusUnauthorized {
		t.Errorf("OPTIONS 没带令牌：想要 401，实际 %d", code)
	}
	for k := range hdr {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("不该回 CORS 头：%s", k)
		}
	}
	// 带对令牌的，五个路由都不是 401
	for _, route := range authRoutes {
		if code, _, _ := r.call(route.method, route.path, route.body); code == http.StatusUnauthorized {
			t.Errorf("%s %s：带对令牌却是 401", route.method, route.path)
		}
	}
}

// leaksToken 是"日志里有没有令牌"的判断；下面的测试先拿它自己验一遍，判断函数坏了测试也会红。
func leaksToken(logs, token string) bool { return strings.Contains(logs, token) }

func TestTokenNotInLogs(t *testing.T) {
	if !leaksToken("AUTH header=Bearer "+testToken, testToken) || leaksToken("AUTH rejected", testToken) {
		t.Fatal("leaksToken 自己判断错了")
	}
	r := newRig(t, rigOpts{embDelay: 0})
	// 一串含 401 的请求：错方案里带着真令牌（最容易被"把头打出来排查"的日志带出去）、令牌后多一个空格、正常成功、坏请求
	for _, h := range []string{"", "Basic " + testToken, "Bearer " + testToken + "x", "bearer " + testToken, "Bearer  " + testToken, "Bearer " + testToken} {
		r.callWith(http.MethodPost, "/v1/embeddings", `{"model":"fake-embed","input":"你好"}`, h)
		r.callWith(http.MethodGet, "/health", nil, h)
	}
	r.call(http.MethodPost, "/v1/embeddings", `{"model":"fake-embed"`)        // 422
	r.call(http.MethodPost, "/v1/embeddings", `{"model":"nope","input":"x"}`) // 404
	r.call(http.MethodPost, "/v1/rerank", `{"model":"fake-rerank","query":"天气","documents":["晴","雨"]}`)
	logs := r.logs.String()
	if logs == "" {
		t.Fatal("日志是空的：这个测试没有查到东西")
	}
	if leaksToken(logs, testToken) {
		t.Fatalf("日志里有令牌：\n%s", logs)
	}
}

// ---- 验收 2：请求体上限 ----

func TestBodyLimit(t *testing.T) {
	r := newRig(t, rigOpts{})
	addr := r.ts.Listener.Addr().String()

	t.Run("Content-Length 64 MiB 不读正文", func(t *testing.T) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// 只发请求头，不发正文：服务要是去读正文就会一直等，读不到响应
		fmt.Fprintf(conn, "POST /v1/embeddings HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", testToken, 64<<20)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("没读到响应（服务在等正文？）：%v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("想要 413，实际 %d：%s", resp.StatusCode, body)
		}
		mustContain(t, "413 的正文", detailOf(t, body), "larger than", "Content-Length")
	})

	t.Run("分块 36 MiB 没有 Content-Length", func(t *testing.T) {
		// 正文是"开头合法的 JSON 加一大片 a"：要是先解析再判大小，这里会得到 422 而不是 413
		body := io.MultiReader(strings.NewReader(`{"model":"fake-embed","input":"`), io.LimitReader(repeatReader('a'), 36<<20))
		req, err := http.NewRequest(http.MethodPost, r.ts.URL+"/v1/embeddings", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("没拿到响应：%v", err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("想要 413，实际 %d：%s", resp.StatusCode, out)
		}
	})

	t.Run("单条 400 万字是 400 且不进分词也不加载模型", func(t *testing.T) {
		before := r.tok.calls()
		code, out := r.embed(strings.Repeat("字", 4_000_000), nil)
		if code != http.StatusBadRequest {
			t.Fatalf("想要 400，实际 %d：%s", code, truncateForLog(out))
		}
		mustContain(t, "400 的正文", detailOf(t, out), "characters", "allows at most 4096")
		if got := r.tok.calls() - before; got != 0 {
			t.Errorf("分词器被调了 %d 次：这条该在分词之前被拦下", got)
		}
		if r.loads.Load() != 0 {
			t.Errorf("为了回 400 加载了模型 %d 次：检查要在取租约之前做", r.loads.Load())
		}
	})
}

type repeatReader byte

func (b repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func truncateForLog(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

// ---- 验收 3：状态码表（规格-本地模型服务 3.6 的每一行）----

func TestStatusTable(t *testing.T) {
	r := newRig(t, rigOpts{maxLoaded: 1, wait: 300 * time.Millisecond, embDelay: 600 * time.Millisecond})
	rr := func(query string, docs any, extra map[string]any) (int, []byte) {
		body := map[string]any{"model": rerankID, "query": query, "documents": docs}
		for k, v := range extra {
			body[k] = v
		}
		code, out, _ := r.call(http.MethodPost, "/v1/rerank", body)
		return code, out
	}
	many := func(n int, s string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d", s, i)
		}
		return out
	}
	cases := []struct {
		name string
		do   func() (int, []byte)
		code int
		sub  string // 响应 detail 里该有的
	}{
		{"空 input 数组", func() (int, []byte) { return r.embed([]string{}, nil) }, 400, "at least 1"},
		{"空字符串", func() (int, []byte) { return r.embed("", nil) }, 400, "Empty input"},
		{"只有空白", func() (int, []byte) { return r.embed([]string{"好", " \t　\n"}, nil) }, 400, "Empty input"},
		{"65 条", func() (int, []byte) { return r.embed(many(65, "句"), nil) }, 400, "between 1 and 64"},
		{"单条超字符上限", func() (int, []byte) { return r.embed(strings.Repeat("长", 4097), nil) }, 400, "allows at most 4096"},
		{"批超 32768 token", func() (int, []byte) { return r.embed(manyLong(64, 700), nil) }, 400, "limited to 32768 tokens"},
		{"dimensions 低于下限", func() (int, []byte) { return r.embed("你好", map[string]any{"dimensions": 127}) }, 400, "dimensions must be within [128, 768]"},
		{"dimensions 超过维度", func() (int, []byte) { return r.embed("你好", map[string]any{"dimensions": 769}) }, 400, "dimensions must be within"},
		{"dimensions 是 0", func() (int, []byte) { return r.embed("你好", map[string]any{"dimensions": 0}) }, 400, "dimensions must be within"},
		{"模型不认识", func() (int, []byte) {
			c, o, _ := r.call(http.MethodPost, "/v1/embeddings", map[string]any{"model": "nope", "input": "x"})
			return c, o
		}, 404, "Unknown model 'nope'; available: fake-embed, fake-rerank"},
		{"嵌入请求拿了重排模型", func() (int, []byte) {
			c, o, _ := r.call(http.MethodPost, "/v1/embeddings", map[string]any{"model": rerankID, "input": "x"})
			return c, o
		}, 404, "is a rerank model, not embed"},
		{"重排请求拿了嵌入模型", func() (int, []byte) {
			c, o, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": embedID, "query": "q", "documents": []string{"d"}})
			return c, o
		}, 404, "is a embed model, not rerank"},
		{"重排 空查询", func() (int, []byte) { return rr("  ", []string{"晴"}, nil) }, 400, "Empty query"},
		{"重排 空 documents", func() (int, []byte) { return rr("天气", []string{}, nil) }, 400, "at least 1 document"},
		{"重排 65 条", func() (int, []byte) { return rr("天气", many(65, "文档"), nil) }, 400, "between 1 and 64"},
		{"重排 文档里有空白", func() (int, []byte) { return rr("天气", []string{"晴", "\t"}, nil) }, 400, "Empty document"},
		{"重排 查询超字符上限", func() (int, []byte) { return rr(strings.Repeat("问", 513), []string{"晴"}, nil) }, 400, "The query is 513 characters"},
		{"重排 查询超 token 预算", func() (int, []byte) { return rr(strings.Repeat("问", 111), []string{"晴"}, nil) }, 400, "allows at most 112"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 表里没有哪一行该是 503：要用到分词器的几行得等模型装好（maxLoaded 1，换模型要重新装），
			// 机器忙时装得比 rig 的 300 毫秒等待慢，会先回“还在加载”。遇到就再问，最多 10 秒（2026-11-12 合并队列里红过）
			code, out := c.do()
			for end := time.Now().Add(10 * time.Second); code == http.StatusServiceUnavailable && strings.Contains(string(out), "still loading") && time.Now().Before(end); {
				time.Sleep(20 * time.Millisecond)
				code, out = c.do()
			}
			if code != c.code {
				t.Fatalf("想要 %d，实际 %d：%s", c.code, code, truncateForLog(out))
			}
			if c.sub != "" {
				mustContain(t, "detail", detailOf(t, out), c.sub)
			}
		})
	}

	t.Run("加载失败是 500，原因里不带输入文字", func(t *testing.T) {
		r.pool.Unload(rerankID) // 前面的用例已经把它装上了（token 预算的 400 在加载以后才判）
		r.loadErrs.Store(rerankID, errors.New("找不到文件 _待分类/fake/model_quantized.onnx"))
		code, out := rr("机密查询", []string{"晴"}, nil)
		if code != 500 {
			t.Fatalf("想要 500，实际 %d：%s", code, out)
		}
		mustContain(t, "detail", detailOf(t, out), "找不到文件 _待分类/fake/model_quantized.onnx")
		if strings.Contains(string(out), "机密查询") {
			t.Errorf("500 的正文里有输入文字：%s", out)
		}
		r.loadErrs.Delete(rerankID)
	})

	t.Run("槽位满是 503", func(t *testing.T) {
		// maxLoaded=1：嵌入模型被一个进行中的请求占着（假会话慢 600 毫秒），重排模型进不来，等满 300 毫秒回 503
		done := make(chan int, 1)
		go func() {
			c, _ := r.embed("占着槽位", nil)
			done <- c
		}()
		deadline := time.Now().Add(3 * time.Second)
		for r.embSess.runs.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		t0 := time.Now()
		code, out := rr("天气", []string{"晴"}, nil)
		waited := time.Since(t0)
		if code != 503 {
			t.Fatalf("想要 503，实际 %d：%s", code, out)
		}
		if waited < 250*time.Millisecond {
			t.Errorf("503 回得太快（%v）：该先等 --wait 的 300 毫秒", waited)
		}
		mustContain(t, "detail", detailOf(t, out), "in use")
		if c := <-done; c != 200 {
			t.Errorf("占着槽位的那个请求该是 200，实际 %d", c)
		}
	})

	t.Run("方法不对和路径不对", func(t *testing.T) {
		if code, out, hdr := r.call(http.MethodGet, "/v1/embeddings", nil); code != 405 || hdr.Get("Allow") != "POST" || detailOf(t, out) != "Method Not Allowed" {
			t.Errorf("GET /v1/embeddings：想要带 Allow: POST 的 405，实际 %d %v %s", code, hdr.Get("Allow"), out)
		}
		if code, out, _ := r.call(http.MethodPost, "/health", nil); code != 405 {
			t.Errorf("POST /health：想要 405，实际 %d %s", code, out)
		}
		if code, out, _ := r.call(http.MethodGet, "/nope", nil); code != 404 || detailOf(t, out) != "Not Found" {
			t.Errorf("GET /nope：想要 404 Not Found，实际 %d %s", code, out)
		}
	})
}

// manyLong 是 n 条各 chars 个字的不同文本（每个字一个 token，批总 token 因此超过 32768）。
func manyLong(n, chars int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strings.Repeat("甲", chars) + fmt.Sprint(i)
	}
	return out
}

// ---- 验收 4：空白动物园，对着 Python 版实测的状态码 ----

type pythonHTTP struct {
	Zoo []struct {
		CP           int    `json:"cp"`
		Layout       string `json:"layout"`
		PythonStatus int    `json:"python_status"`
	} `json:"zoo"`
	Shapes []struct {
		Name         string `json:"name"`
		Path         string `json:"path"`
		BodyB64      string `json:"body_b64"`
		Note         string `json:"note"`
		PythonStatus int    `json:"python_status"`
	} `json:"shapes"`
}

func readPythonHTTP(t *testing.T) pythonHTTP {
	t.Helper()
	raw, err := os.ReadFile("testdata/python-http.json")
	if err != nil {
		t.Fatalf("读 python-http.json：%v（重新生成的命令在 testdata/README.md）", err)
	}
	var d pythonHTTP
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWhitespaceZoo(t *testing.T) {
	d := readPythonHTTP(t)
	if len(d.Zoo) < 300 {
		t.Fatalf("动物园只有 %d 条：参照文件不对", len(d.Zoo))
	}
	r := newRig(t, rigOpts{})
	layouts := map[string]func(string) string{
		"alone":   func(c string) string { return c },
		"letters": func(c string) string { return "a" + c + "b" },
		"spaced":  func(c string) string { return "a " + c + " b" },
	}
	blank, ok := 0, 0
	for _, z := range d.Zoo {
		text := layouts[z.Layout](string(rune(z.CP)))
		code, out := r.embed(text, nil)
		if code != z.PythonStatus {
			t.Errorf("U+%04X（%s）：Python 版回 %d，Go 版回 %d：%s", z.CP, z.Layout, z.PythonStatus, code, truncateForLog(out))
		}
		if z.PythonStatus == 400 {
			blank++
		} else {
			ok++
		}
	}
	// 两头都要有：拦下的和放行的都够多，参照不是全 200 或全 400
	if blank < 29 || ok < 250 {
		t.Errorf("参照里拦下 %d 条、放行 %d 条：太偏了，这个测试量不到东西", blank, ok)
	}
}

// ---- 验收 5：请求体形状对着 Python 版 ----

// 这些形状 Go 版比 Python 版更严（规格第三节第一行：类型不对一律 422，不做宽松转换）。
// 键是参照里的名字，值是 Go 版该回的状态码。多一条、少一条都红：Python 版放松了或 Go 版放松了，都要来这里改。
var stricterThanPython = map[string]int{
	"嵌入 dimensions 字符串":     422, // Pydantic 宽松模式把 "256" 当 256
	"嵌入 dimensions 带小数点的整数": 422, // Pydantic 宽松模式收 256.0
	"嵌入 dimensions 布尔":      422, // Pydantic 把 true 当 1，再被维度检查拦成 400
	"重排 top_n 字符串":          422, // 同上
	// 参照用的是假模型，Python 回 200；真分词器对带孤立代理码点的字符串抛 TypeError，路由没接，实际是 500。
	// Go 的 encoding/json 会悄悄把它换成 U+FFFD 照常算（算的是另一个字符串），所以在解析前拦成 422
	"嵌入 孤立代理对": 422,
}

func TestStrictBody(t *testing.T) {
	d := readPythonHTTP(t)
	r := newRig(t, rigOpts{})
	seen := map[string]bool{}
	for _, s := range d.Shapes {
		raw, err := base64.StdEncoding.DecodeString(s.BodyB64)
		if err != nil {
			t.Fatal(err)
		}
		body := strings.NewReplacer("$EMBED", embedID, "$RERANK", rerankID).Replace(string(raw))
		code, out, _ := r.call(http.MethodPost, s.Path, []byte(body))
		want := s.PythonStatus
		if g, ok := stricterThanPython[s.Name]; ok {
			want, seen[s.Name] = g, true
			if g == s.PythonStatus {
				t.Errorf("%s：登记成\"比 Python 严\"，但 Python 版的状态码也是 %d，登记该删了", s.Name, g)
			}
		}
		if code != want {
			t.Errorf("%s：Python 版回 %d（Go 版该回 %d），实际 %d：%s", s.Name, s.PythonStatus, want, code, truncateForLog(out))
		}
	}
	for name := range stricterThanPython {
		if !seen[name] {
			t.Errorf("登记的\"比 Python 严\"里有 %q，参照的 shapes 里没有这一条", name)
		}
	}

	// 422 的正文不回显请求里的任何东西，连未知字段的名字也不回显
	code, out, _ := r.call(http.MethodPost, "/v1/embeddings", `{"model":"fake-embed","input":"机密输入","机密字段名":1}`)
	if code != 422 {
		t.Fatalf("未知字段：想要 422，实际 %d", code)
	}
	if strings.Contains(string(out), "机密") {
		t.Errorf("422 的正文里回显了请求里的内容：%s", out)
	}
	code, out, _ = r.call(http.MethodPost, "/v1/embeddings", `{"model":"fake-embed","input":"机密输入","dimensions":"机密值"}`)
	if code != 422 || strings.Contains(string(out), "机密") {
		t.Errorf("类型不对：想要不带原文的 422，实际 %d %s", code, out)
	}
}

// ---- 验收 7：/health 和 /v1/models ----

func TestHealthAndModels(t *testing.T) {
	var rssBytes uint64 = 123456789
	r := newRig(t, rigOpts{rss: func() (uint64, error) { return rssBytes, nil }})

	var h map[string]any
	code, out, _ := r.call(http.MethodGet, "/health", nil)
	if code != 200 || json.Unmarshal(out, &h) != nil {
		t.Fatalf("/health：%d %s", code, out)
	}
	for _, k := range []string{"status", "engine", "ort_version", "models", "loaded", "resident_bytes", "rss_bytes", "max_loaded", "run_elapsed_ms", "last_ok_unix"} {
		if _, ok := h[k]; !ok {
			t.Errorf("/health 缺 %s：%s", k, out)
		}
	}
	if h["status"] != "ok" || h["engine"] != "go" || h["ort_version"] != "9.9.9-fake" || h["rss_bytes"] != float64(rssBytes) || h["max_loaded"] != float64(2) {
		t.Errorf("/health 的值不对：%s", out)
	}
	if loaded, _ := h["loaded"].([]any); loaded == nil || len(loaded) != 0 {
		t.Errorf("没加载任何模型时 loaded 该是 []，不是 null：%s", out)
	}
	rows := h["models"].([]any)
	if len(rows) != 2 {
		t.Fatalf("models 该有 2 行：%s", out)
	}
	first := rows[0].(map[string]any)
	if first["id"] != embedID || first["loaded"] != false || first["load_ms"] != nil || first["last_used_ms_ago"] != nil {
		t.Errorf("没加载的模型：load_ms、last_used_ms_ago 该是 null：%v", first)
	}
	if first["kind"] != "embed" || first["dimension"] != float64(768) || first["max_tokens"] != float64(1024) || first["matryoshka"] != true {
		t.Errorf("models 行的字段不对：%v", first)
	}
	if strings.Contains(string(out), "model_quantized") || strings.Contains(string(out), `\\`) {
		t.Errorf("/health 不列文件路径：%s", out)
	}

	if c, _ := r.embed("你好", nil); c != 200 {
		t.Fatalf("嵌入：%d", c)
	}
	out = mustGet(t, r, "/health")
	_ = json.Unmarshal(out, &h)
	first = h["models"].([]any)[0].(map[string]any)
	if first["loaded"] != true || first["load_ms"] == nil || first["last_used_ms_ago"] == nil {
		t.Errorf("加载以后 load_ms、last_used_ms_ago 该有值：%v", first)
	}
	if got := h["loaded"].([]any); len(got) != 1 || got[0] != embedID {
		t.Errorf("loaded 该是 [%s]：%v", embedID, got)
	}
	if h["last_ok_unix"].(float64) <= 0 || h["run_elapsed_ms"] != float64(0) {
		t.Errorf("跑完一次前向以后 last_ok_unix 该大于 0、run_elapsed_ms 该是 0：%s", out)
	}

	// /v1/models：只有点名的两个，每个多一个 ready
	var ml struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	out = mustGet(t, r, "/v1/models")
	if err := json.Unmarshal(out, &ml); err != nil || ml.Object != "list" || len(ml.Data) != 2 {
		t.Fatalf("/v1/models：%s", out)
	}
	for _, m := range ml.Data {
		if m["object"] != "model" || m["owned_by"] != "local" || m["ready"] != true {
			t.Errorf("/v1/models 的一行不对：%v", m)
		}
		for _, k := range []string{"id", "kind", "dimension", "max_tokens", "loaded"} {
			if _, ok := m[k]; !ok {
				t.Errorf("/v1/models 的一行缺 %s：%v", k, m)
			}
		}
	}
	if ml.Data[0]["loaded"] != true || ml.Data[1]["loaded"] != false {
		t.Errorf("loaded 不对：%v", ml.Data)
	}

	// 登记表里没有的 id：404，列可用 id
	code, out, _ = r.call(http.MethodPost, "/v1/embeddings", map[string]any{"model": "qwen3-embedding-0.6b", "input": "x"})
	if code != 404 {
		t.Fatalf("登记表里没有的 id：想要 404，实际 %d", code)
	}
	mustContain(t, "detail", detailOf(t, out), "Unknown model", "fake-embed, fake-rerank")
	// 太长的 id 不整条回显
	code, out, _ = r.call(http.MethodPost, "/v1/embeddings", map[string]any{"model": strings.Repeat("长", 5000), "input": "x"})
	if code != 404 || len(out) > 1000 {
		t.Errorf("很长的模型 id：想要不回显全文的 404，实际 %d，%d 字节", code, len(out))
	}
}

func mustGet(t *testing.T, r *rig, path string) []byte {
	t.Helper()
	code, out, _ := r.call(http.MethodGet, path, nil)
	if code != 200 {
		t.Fatalf("GET %s：%d %s", path, code, out)
	}
	return out
}

// 登记了但这次没点名的模型：说"登记了但没点名"，不说"不知道这个模型"；/v1/models 只列点名的。
func TestModelsOnlyListsNamed(t *testing.T) {
	r := newRig(t, rigOpts{only: []string{embedID}})
	var ml struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(mustGet(t, r, "/v1/models"), &ml); err != nil || len(ml.Data) != 1 || ml.Data[0]["id"] != embedID {
		t.Fatalf("只点名了嵌入模型，/v1/models 该只列它：%v", ml.Data)
	}
	code, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}})
	if code != 404 {
		t.Fatalf("没点名的重排模型：想要 404，实际 %d", code)
	}
	got := detailOf(t, out)
	mustContain(t, "detail", got, "registered but not selected", "available: fake-embed")
	if strings.Contains(got, "Unknown model") {
		t.Errorf("登记了的模型不该说不知道这个模型：%s", got)
	}
}

// --lazy-models 里文件缺了的模型：只它自己 ready=false，请求回 500 并写原因，别的模型照常。
func TestLazyModelProblemIsolated(t *testing.T) {
	const problem = "找不到文件 _待分类/fake/model_quantized.onnx"
	r := newRig(t, rigOpts{problems: map[string]string{rerankID: problem}})
	r.loadErrs.Store(rerankID, errors.New(problem))
	var ml struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(mustGet(t, r, "/v1/models"), &ml); err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{}
	for _, m := range ml.Data {
		ready[m["id"].(string)] = m["ready"]
	}
	if ready[embedID] != true || ready[rerankID] != false {
		t.Fatalf("ready 该是嵌入 true、重排 false：%v", ready)
	}
	if c, out := r.embed("你好", nil); c != 200 {
		t.Fatalf("重排模型坏了，嵌入照样该 200：%d %s", c, out)
	}
	code, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}})
	if code != 500 {
		t.Fatalf("坏了的重排模型：想要 500，实际 %d %s", code, out)
	}
	mustContain(t, "detail", detailOf(t, out), "model_quantized.onnx")
	// 文件后来补上了：不用重启，下一次请求装得上，ready 恢复
	r.loadErrs.Delete(rerankID)
	if code, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}}); code != 200 {
		t.Fatalf("文件补上以后：想要 200，实际 %d %s", code, out)
	}
	if err := json.Unmarshal(mustGet(t, r, "/v1/models"), &ml); err != nil {
		t.Fatal(err)
	}
	for _, m := range ml.Data {
		if m["ready"] != true {
			t.Errorf("装好以后 ready 该恢复：%v", m)
		}
	}
}

// 池关闭时会取消进行中的加载，加载失败的 *pool.Error 里就包着 context.Canceled。那是 500 / 503，不是"调用方走了"的 499：
// 错误映射里 *pool.Error 要排在 ctx 错误前面。
func TestPoolErrorWrappingCancelIsNot499(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.loadErrs.Store(rerankID, fmt.Errorf("加载中被取消：%w", context.Canceled))
	code, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}})
	if code != http.StatusInternalServerError {
		t.Fatalf("想要加载失败的 500，实际 %d：%s", code, out)
	}
	mustContain(t, "detail", detailOf(t, out), "加载中被取消")
}

func TestUnload(t *testing.T) {
	r := newRig(t, rigOpts{embDelay: 400 * time.Millisecond})
	post := func(id string) (int, map[string]any) {
		code, out, _ := r.call(http.MethodPost, "/v1/models/"+id+"/unload", nil)
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return code, m
	}
	if code, m := post(embedID); code != 200 || m["unloaded"] != false || m["reason"] != "not_loaded" || m["model"] != embedID {
		t.Errorf("没加载就卸：%d %v", code, m)
	}
	done := make(chan struct{})
	go func() { r.embed("占着", nil); close(done) }()
	for r.embSess.runs.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if code, m := post(embedID); code != 200 || m["unloaded"] != false || m["reason"] != "in_use" {
		t.Errorf("在用的不卸：%d %v", code, m)
	}
	<-done
	if code, m := post(embedID); code != 200 || m["unloaded"] != true || m["reason"] != "unloaded" {
		t.Errorf("用完了该卸掉：%d %v", code, m)
	}
	if code, out, _ := r.call(http.MethodPost, "/v1/models/nope/unload", nil); code != 404 {
		t.Errorf("不认识的 id：想要 404，实际 %d %s", code, out)
	}
}

// ---- 响应的形状 ----

func TestEmbeddingsResponseShape(t *testing.T) {
	r := newRig(t, rigOpts{})
	type embResp struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string          `json:"object"`
			Index     int             `json:"index"`
			Embedding json.RawMessage `json:"embedding"`
		} `json:"data"`
		Model string         `json:"model"`
		Usage map[string]int `json:"usage"`
	}
	code, out := r.embed([]string{"你好", "世界"}, nil)
	var floats embResp
	if code != 200 || json.Unmarshal(out, &floats) != nil {
		t.Fatalf("%d %s", code, truncateForLog(out))
	}
	if floats.Object != "list" || floats.Model != embedID || len(floats.Data) != 2 || floats.Data[1].Index != 1 || floats.Data[0].Object != "embedding" {
		t.Errorf("形状不对：%s", truncateForLog(out))
	}
	if floats.Usage["prompt_tokens"] != floats.Usage["total_tokens"] || floats.Usage["prompt_tokens"] != 8 { // 每句 2 个字加 <s></s>
		t.Errorf("usage 不对：%v", floats.Usage)
	}
	var vec0 []float32
	if err := json.Unmarshal(floats.Data[0].Embedding, &vec0); err != nil || len(vec0) != 768 {
		t.Fatalf("float 向量：%v 长度 %d", err, len(vec0))
	}
	var sum float64
	for _, x := range vec0 {
		sum += float64(x) * float64(x)
	}
	if math.Abs(sum-1) > 1e-5 {
		t.Errorf("向量没有归一化：范数平方 %v", sum)
	}

	// base64：小端 float32，和 float 格式同一个请求的结果逐位相同
	code, out = r.embed([]string{"你好", "世界"}, map[string]any{"encoding_format": "base64"})
	var b64 embResp
	if code != 200 || json.Unmarshal(out, &b64) != nil {
		t.Fatalf("%d %s", code, truncateForLog(out))
	}
	var s string
	if err := json.Unmarshal(b64.Data[0].Embedding, &s); err != nil {
		t.Fatalf("base64 的 embedding 该是字符串：%s", b64.Data[0].Embedding)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != 4*768 {
		t.Fatalf("base64：%v，%d 字节", err, len(raw))
	}
	for i, x := range vec0 {
		if math.Float32bits(x) != binary.LittleEndian.Uint32(raw[4*i:]) {
			t.Fatalf("第 %d 维：float 格式 %v 和 base64 解出来的不同", i, x)
		}
	}

	// dimensions 截断后重新归一化，长度对
	code, out = r.embed("你好", map[string]any{"dimensions": 256, "input_type": "query"})
	var cut embResp
	if code != 200 || json.Unmarshal(out, &cut) != nil {
		t.Fatalf("%d %s", code, truncateForLog(out))
	}
	var v256 []float32
	_ = json.Unmarshal(cut.Data[0].Embedding, &v256)
	if len(v256) != 256 {
		t.Errorf("dimensions=256 却给了 %d 维", len(v256))
	}
}

func TestRerankResponseShape(t *testing.T) {
	r := newRig(t, rigOpts{})
	type rrResp struct {
		Model   string `json:"model"`
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
		Usage map[string]int `json:"usage"`
	}
	docs := []string{"晴", "雨天", "多云转阴", "下雪了呀", "雾"}
	code, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "天气", "documents": docs})
	var resp rrResp
	if code != 200 || json.Unmarshal(out, &resp) != nil {
		t.Fatalf("%d %s", code, out)
	}
	if resp.Model != rerankID || len(resp.Results) != 5 || resp.Usage["total_tokens"] <= 0 {
		t.Fatalf("形状不对：%s", out)
	}
	seen := map[int]bool{}
	for i, it := range resp.Results {
		if seen[it.Index] {
			t.Errorf("下标 %d 出现两次", it.Index)
		}
		seen[it.Index] = true
		if i > 0 {
			prev := resp.Results[i-1]
			if prev.Score < it.Score || (prev.Score == it.Score && prev.Index > it.Index) {
				t.Errorf("排序不对（分数从大到小，同分按下标）：%v 在 %v 前面", prev, it)
			}
		}
		if it.Score <= 0 || it.Score >= 1 {
			t.Errorf("relevance_score 该在 (0,1)：%v", it)
		}
	}
	// top_n：夹到 [1, 文档数]
	for _, tc := range []struct {
		topN any
		want int
	}{{2, 2}, {0, 1}, {-5, 1}, {99, 5}, {nil, 5}} {
		code, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "天气", "documents": docs, "top_n": tc.topN})
		var rr rrResp
		if code != 200 || json.Unmarshal(out, &rr) != nil || len(rr.Results) != tc.want {
			t.Errorf("top_n=%v：想要 %d 条，实际 %d %s", tc.topN, tc.want, code, out)
		}
	}
}

// ---- 验收 10：日志行的格式，不含文字 ----

func TestLogLineFormat(t *testing.T) {
	r := newRig(t, rigOpts{})
	const secret = "绝不能出现在日志里的句子"
	r.embed([]string{secret, secret + "二"}, nil)
	r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": secret, "documents": []string{"晴", "雨", secret}, "top_n": 2})
	r.embed([]string{secret}, nil) // 缓存命中
	logs := r.logs.String()
	if strings.Contains(logs, secret) {
		t.Fatalf("日志里有输入文字：\n%s", logs)
	}
	want := []string{
		`^EMBED model=fake-embed inputs=2 tokens=\d+ cache_hits=0 dimensions=768 ms=\d+$`,
		`^RERANK model=fake-rerank documents=3 tokens=\d+ top_n=2 ms=\d+$`,
		`^EMBED model=fake-embed inputs=1 tokens=\d+ cache_hits=1 dimensions=768 ms=\d+$`,
	}
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	if len(lines) != len(want) {
		t.Fatalf("想要 %d 行日志，实际 %d 行：\n%s", len(want), len(lines), logs)
	}
	for i, re := range want {
		if !regexp.MustCompile(re).MatchString(lines[i]) {
			t.Errorf("第 %d 行不符合 %s：%q", i+1, re, lines[i])
		}
	}
}

// ---- 验收 9：panic 不泄露输入 ----

func TestPanicDoesNotLeakInput(t *testing.T) {
	const secret = "机密输入文字-不许出现在响应和日志里"
	r := newRig(t, rigOpts{panicWith: secret}) // 会话的 Run 第一次 panic，值就是输入文字
	code, out := r.embed(secret, nil)
	if code != 500 {
		t.Fatalf("想要 500，实际 %d：%s", code, out)
	}
	if strings.Contains(string(out), secret) {
		t.Errorf("响应里有 panic 的值：%s", out)
	}
	logs := r.logs.String()
	if strings.Contains(logs, secret) {
		t.Errorf("日志里有 panic 的值：\n%s", logs)
	}
	if !regexp.MustCompile(`PANIC type=string func=\S*fakeSess\S*Run`).MatchString(logs) {
		t.Errorf("日志里该有 PANIC 的类型和出事的函数名：\n%s", logs)
	}
	// 进程还活着，模型的行锁和租约都放开了：同一个模型的下一个请求正常完成
	done := make(chan int, 1)
	go func() { c, _ := r.embed("下一句", nil); done <- c }()
	select {
	case c := <-done:
		if c != 200 {
			t.Errorf("panic 以后的下一个请求想要 200，实际 %d", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("panic 以后的下一个请求卡住了：行锁或租约没放开")
	}
	// 看监视器：那次 panic 的前向不能一直算"进行中"
	if d := r.srv.mon.Longest(); d != 0 {
		t.Errorf("panic 的前向还算进行中（%v），看门狗会误杀", d)
	}
}

// ---- 验收 6：取消、超时、/health 不被挡 ----

func TestCancelStopsAtRowBoundary(t *testing.T) {
	r := newRig(t, rigOpts{embDelay: 200 * time.Millisecond})
	inputs := make([]string, 8) // 不取消的话要 1.6 秒
	for i := range inputs {
		inputs[i] = fmt.Sprintf("第%d句各不相同", i)
	}
	body, _ := json.Marshal(map[string]any{"model": embedID, "input": inputs})
	conn, err := net.Dial("tcp", r.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST /v1/embeddings HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", testToken, len(body), body)
	time.Sleep(100 * time.Millisecond) // 第一行还在算
	conn.Close()
	t0 := time.Now()

	// 下一个请求：只有一行，该在第一行算完（约 200 毫秒）之后马上拿到锁，不等剩下的 7 行
	code, out := r.embed("下一个请求", nil)
	if code != 200 {
		t.Fatalf("下一个请求：%d %s", code, out)
	}
	if took := time.Since(t0); took > 900*time.Millisecond {
		t.Errorf("下一个请求等了 %v：断开的那个批没有停在行边界", took)
	}
	time.Sleep(300 * time.Millisecond)
	if runs := r.embSess.runs.Load(); runs > 3 { // 被断开的批最多多算一行，加上下一个请求的一行
		t.Errorf("前向共跑了 %d 次：被断开的批该停下，不该算完 8 行", runs)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := r.pool.Snapshot()
		if snap.Models[0].Refs == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("被断开的请求还占着租约：%+v", snap.Models[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(r.logs.String(), "STOPPED model=fake-embed reason=disconnect") {
		t.Errorf("日志里该有 STOPPED reason=disconnect：\n%s", r.logs.String())
	}
}

func TestMaxRequestSeconds(t *testing.T) {
	r := newRig(t, rigOpts{embDelay: 200 * time.Millisecond, maxRequest: 300 * time.Millisecond})
	inputs := make([]string, 6)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("第%d句各不相同", i)
	}
	t0 := time.Now()
	code, out := r.embed(inputs, nil)
	took := time.Since(t0)
	if code != http.StatusGatewayTimeout {
		t.Fatalf("想要 504，实际 %d：%s", code, out)
	}
	mustContain(t, "504 的正文", detailOf(t, out), "--max-request-seconds 0.3", "did not finish")
	if took > 900*time.Millisecond {
		t.Errorf("504 回得太慢（%v）：该在 300 毫秒的期限之后最近的一行边界停下", took)
	}
	if code, out := r.embed("随后的请求", nil); code != 200 {
		t.Errorf("504 之后的下一个请求想要 200，实际 %d %s", code, out)
	}
	mustContain(t, "日志", r.logs.String(), "STOPPED model=fake-embed reason=timeout")
}

func TestHealthNotBlocked(t *testing.T) {
	block := make(chan struct{})
	r := newRig(t, rigOpts{embBlock: block})
	done := make(chan int, 1)
	go func() { c, _ := r.embed("卡住的前向", nil); done <- c }()
	for r.embSess.runs.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	t0 := time.Now()
	var h struct {
		Loaded       []string `json:"loaded"`
		RunElapsedMs int64    `json:"run_elapsed_ms"`
		LastOKUnix   int64    `json:"last_ok_unix"`
	}
	out := mustGet(t, r, "/health")
	if took := time.Since(t0); took > 100*time.Millisecond {
		t.Errorf("前向卡着的时候 /health 用了 %v，该在 100 毫秒内", took)
	}
	_ = json.Unmarshal(out, &h)
	if len(h.Loaded) != 1 || h.RunElapsedMs < 100 || h.LastOKUnix != 0 {
		t.Errorf("卡着的时候 run_elapsed_ms 该 ≥ 100、last_ok_unix 该是 0：%s", out)
	}
	// /v1/models 同样不被挡
	t0 = time.Now()
	mustGet(t, r, "/v1/models")
	if took := time.Since(t0); took > 100*time.Millisecond {
		t.Errorf("前向卡着的时候 /v1/models 用了 %v", took)
	}
	close(block)
	if c := <-done; c != 200 {
		t.Fatalf("放开以后该 200，实际 %d", c)
	}
	_ = json.Unmarshal(mustGet(t, r, "/health"), &h)
	if h.RunElapsedMs != 0 || h.LastOKUnix <= 0 {
		t.Errorf("跑完以后 run_elapsed_ms 该回 0、last_ok_unix 该大于 0：%+v", h)
	}
}

// ---- 监视器 ----

func TestMonitor(t *testing.T) {
	m := NewMonitor()
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	if m.Longest() != 0 || m.LastOKUnix() != 0 {
		t.Fatal("起点该是 0")
	}
	endA := m.begin()
	now = now.Add(2 * time.Second)
	endB := m.begin()
	now = now.Add(3 * time.Second)
	if got := m.Longest(); got != 5*time.Second {
		t.Errorf("两个前向在跑，该取跑得最久的那个（5 秒）：%v", got)
	}
	endA(true)
	if got := m.Longest(); got != 3*time.Second {
		t.Errorf("A 结束以后只剩 B（3 秒）：%v", got)
	}
	if m.LastOKUnix() != now.Unix() {
		t.Errorf("成功的前向结束该记下时间：%d", m.LastOKUnix())
	}
	ok := m.LastOKUnix()
	now = now.Add(time.Second)
	endB(false)
	if m.LastOKUnix() != ok || m.Longest() != 0 {
		t.Errorf("失败的前向结束不该更新 last_ok，也不该还算进行中：%d %v", m.LastOKUnix(), m.Longest())
	}
}

// 多个 goroutine 同时发请求，-race 下查数据竞争；结果都对。
func TestConcurrentRequests(t *testing.T) {
	r := newRig(t, rigOpts{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, out := r.embed(fmt.Sprintf("并发的第%d句", i), nil); c != 200 {
				t.Errorf("并发请求 %d：%d %s", i, c, truncateForLog(out))
			}
			if c, out, _ := r.call(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "天气", "documents": []string{"晴", fmt.Sprint(i)}}); c != 200 {
				t.Errorf("并发重排 %d：%d %s", i, c, out)
			}
			r.call(http.MethodGet, "/health", nil)
		}()
	}
	wg.Wait()
}
