package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/llm"
)

// llmSource 是一个编的来源：base 是 http://127.0.0.1:端口，这里补上 /v1/
func llmSource(base string) llm.Source {
	return llm.Source{Name: "假的", BaseURL: base + "/v1/", APIKey: "编的令牌"}
}

// rerankSrv 是假的 /v1/rerank：记下请求、按给的 results 回（本地模型服务的形状，规格-本地模型服务 3.4）
func rerankSrv(t *testing.T, token string, results func(docs []string) string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var body struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体读不出来：%v", err)
			return
		}
		m := map[string]any{"model": body.Model, "query": body.Query, "documents": body.Documents}
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"results":%s,"usage":{"total_tokens":1}}`, body.Model, results(body.Documents))
	}))
	return srv, &bodies
}

// RerankConnect 照本地模型服务的 /v1/rerank 调：带令牌、按 index 还原顺序，分数从大到小（同分按 index）
func TestRerankClient(t *testing.T) {
	t.Setenv("SUMIKA_EMBED_TOKEN", "编的令牌")
	srv, bodies := rerankSrv(t, "编的令牌", func([]string) string {
		// 故意乱序、留一对同分，看客户端会不会按分数稳一遍
		return `[{"index":2,"relevance_score":0.5},{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5}]`
	})
	defer srv.Close()
	c := RerankConnect(srv.URL+"/v1", "编的重排", 5*time.Second)
	order, scores, err := c.Rerank(context.Background(), "编的问题", []string{"一", "二", "三"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(order) != "[0 1 2]" || fmt.Sprint(scores) != "[0.9 0.5 0.5]" {
		t.Fatalf("没按分数排稳：%v %v", order, scores)
	}
	if c.Model() != "编的重排" {
		t.Fatalf("模型名不对：%q", c.Model())
	}
	b := (*bodies)[0]
	if b["model"] != "编的重排" || b["query"] != "编的问题" || fmt.Sprint(b["documents"]) != "[一 二 三]" {
		t.Fatalf("请求体不对：%v", b)
	}
	// 0 条不发请求
	order, scores, err = c.Rerank(context.Background(), "编的问题", nil)
	if err != nil || len(order) != 0 || len(scores) != 0 || len(*bodies) != 1 {
		t.Fatalf("0 条该直接回空、不发请求：%v %v %v", order, scores, err)
	}
}

// 服务坏了、回的不是一组 index：都算错，调用方好退回原来的顺序
func TestRerankClientRejectsBadReplies(t *testing.T) {
	cases := map[string]struct {
		status  int
		results string
		want    string
	}{
		"500":     {500, "", "500"},
		"越界":      {200, `[{"index":3,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]`, "index"},
		"重复":      {200, `[{"index":1,"relevance_score":0.9},{"index":1,"relevance_score":0.1}]`, "index"},
		"少一条":     {200, `[{"index":0,"relevance_score":0.9}]`, "2 条"},
		"没有结果":    {200, `[]`, "条"},
		"不是 JSON": {200, `不是`, "读不出来"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if c.status != http.StatusOK {
					http.Error(w, "假服务坏了", c.status)
					return
				}
				fmt.Fprintf(w, `{"model":"编的重排","results":%s}`, c.results)
			}))
			defer srv.Close()
			cc := NewRerankClient(llmSource(srv.URL), "编的重排", 5*time.Second)
			if _, _, err := cc.Rerank(context.Background(), "编的问题", []string{"一", "二"}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("该报错（包含 %q）：%v", c.want, err)
			}
		})
	}
}

// 超时：ctx 到点就走，不等客户端自己的上限
func TestRerankClientHonorsContext(t *testing.T) {
	// 服务 1 秒后才回：照 ctx 走的话 50 毫秒就回来，照客户端上限（5 秒）走的话会等到 1 秒
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"编的重排","results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.1}]}`)
	}))
	defer srv.Close()
	c := NewRerankClient(llmSource(srv.URL), "编的重排", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, _, err := c.Rerank(ctx, "编的问题", []string{"一", "二"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx 到点该报 DeadlineExceeded：%v", err)
	}
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("没照 ctx 走，等了 %v", d)
	}
}
