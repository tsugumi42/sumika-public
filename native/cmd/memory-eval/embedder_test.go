package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// 假的嵌入服务：照本地模型服务的 /v1/embeddings 回（OpenAI 形状，认 input_type），把每次请求记下来
type fakeEmbedSrv struct {
	mu       sync.Mutex
	requests []fakeEmbedReq
	dim      int
}

type fakeEmbedReq struct {
	model     string
	inputType string
	input     []string
}

func (f *fakeEmbedSrv) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	if f.dim == 0 {
		f.dim = 3
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		var body struct {
			Model     string   `json:"model"`
			Input     []string `json:"input"`
			InputType string   `json:"input_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体读不出来：%v", err)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, fakeEmbedReq{body.Model, body.InputType, body.Input})
		f.mu.Unlock()
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := []item{}
		for i := range body.Input {
			v := make([]float64, f.dim)
			v[0] = float64(i + 1)
			data = append(data, item{"embedding", i, v})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": body.Model,
			"usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1}})
	}
}

func (f *fakeEmbedSrv) seen() []fakeEmbedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeEmbedReq(nil), f.requests...)
}

// 给了 -embed-url：不起子进程，也不去读 sumika.yml（配置给一个不存在的编的路径）
func TestEmbedURLSkipsSubprocess(t *testing.T) {
	fake := &fakeEmbedSrv{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler(t)))
	defer srv.Close()
	ev, stop := setUpEmbedder(embedSetup{url: srv.URL + "/v1", model: "编的模型", timeout: 5 * time.Second, dataDir: filepath.Join(t.TempDir(), "不存在的数据目录")}, t.Logf)
	defer stop()
	if ev == nil || ev.typed == nil {
		t.Fatalf("连外面服务的那条路应该带 input_type：%v", ev)
	}
	if _, isLocal := ev.inner.(*embed.Local); isLocal {
		t.Fatal("给了 -embed-url 还起了本机子进程")
	}
	if _, err := ev.Embed(context.Background(), []string{"编的一句"}); err != nil {
		t.Fatalf("连外面服务应该能算：%v", err)
	}
	if head := ev.head(); !strings.Contains(head, "编的模型") || !strings.Contains(head, "3 维") {
		t.Fatalf("输出里要说明模型和维度：%q", head)
	}
	if line := ev.line(); !strings.Contains(line, "调用 1 次 1 条") || !strings.Contains(line, "ms") {
		t.Fatalf("输出里要说明算了几次、一共多久：%q", line)
	}
}

// -embed-timeout 传到了客户端（本地服务一次前向只放一条，一批 64 条要几分钟；默认 10 分钟）：
// 慢于上限就报错，给够了就等得到
func TestEmbedTimeoutReachesClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"model":"m"}`)
	}))
	defer srv.Close()
	setup := embedSetup{url: srv.URL + "/v1", model: "编的模型", dataDir: filepath.Join(t.TempDir(), "不存在的数据目录")}
	setup.timeout = 50 * time.Millisecond
	short, stopShort := setUpEmbedder(setup, t.Logf)
	defer stopShort()
	if _, err := short.Embed(context.Background(), []string{"编的一句"}); err == nil {
		t.Fatal("-embed-timeout 50 毫秒：300 毫秒才回的服务应该报错")
	}
	setup.timeout = 5 * time.Second
	long, stopLong := setUpEmbedder(setup, t.Logf)
	defer stopLong()
	if _, err := long.Embed(context.Background(), []string{"编的一句"}); err != nil {
		t.Fatalf("-embed-timeout 5 秒应该等得到 300 毫秒：%v", err)
	}
}

// 条目和查询分开算：Open 期间是 document，Open 以后是 query；请求里带对的 model 和 input_type
func TestDocumentsThenQueries(t *testing.T) {
	fake := &fakeEmbedSrv{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler(t)))
	defer srv.Close()
	ev := newEvalEmbedder(embed.Connect(srv.URL+"/v1", "编的模型", 0))

	m := openMemory(context.Background(), bunDb(t), ev, nil, 0, nil, nil)
	defer m.Close()
	m.Recall(context.Background(), memory.Audience{Channel: "console", Conversation: "console:编的", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}, memory.Query{Text: "猫叫什么"})

	reqs := fake.seen()
	if len(reqs) < 2 {
		t.Fatalf("该有 Open 一次、查询至少一次：%+v", reqs)
	}
	first := reqs[0]
	if first.inputType != "document" || !strings.Contains(strings.Join(first.input, ""), "团子") {
		t.Fatalf("Open 时算的是条目，要带 document 和条目正文：%+v", first)
	}
	for i, r := range reqs[1:] {
		if r.inputType != "query" {
			t.Fatalf("第 %d 次（Open 以后）要带 query：%+v", i+2, r)
		}
	}
	for _, r := range reqs {
		if r.model != "编的模型" {
			t.Fatalf("请求里的 model 不对：%+v", r)
		}
	}
	if head := ev.head(); !strings.Contains(head, "编的模型") || !strings.Contains(head, "3 维") {
		t.Fatalf("输出里要说明模型和维度：%q", head)
	}
	if line := ev.line(); !strings.Contains(line, "一共") {
		t.Fatalf("输出里要说明一共多久：%q", line)
	}
}

// bunDb 照 Bun 版 memory.db 的表（只建 loadItems 读到的列）造一个编的临时库
func bunDb(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只建 loadItems 要读的几列，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`INSERT INTO facts VALUES ('1', 'fact', 'active', '主人养了一只叫团子的猫', 'owner', 'owner', '', ?, ?, NULL, NULL)`, now, now); err != nil {
		t.Fatal(err)
	}
	return file
}
