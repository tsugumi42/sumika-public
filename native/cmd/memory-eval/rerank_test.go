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
	"testing"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// rerankDb：在 bunDb 那条后面再加两条也带「主人」的，才有顺序可换
func rerankDb(t *testing.T) string {
	t.Helper()
	file := bunDb(t)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UnixMilli()
	for _, q := range []string{
		`INSERT INTO facts (id, kind, status, text, subject, scope, conversation, created_at, learned_at) VALUES ('2', 'fact', 'active', '主人养的那只猫很胖', 'owner', 'owner', '', ?, ?)`,
		`INSERT INTO facts (id, kind, status, text, subject, scope, conversation, created_at, learned_at) VALUES ('3', 'fact', 'active', '主人说过群友的猫叫咪咪', 'qq:1', 'conv:qq:group:0', '', ?, ?)`,
	} {
		if _, err := db.Exec(q, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

// 重排接进召回：假服务把顺序倒过来，结果跟着倒；计数和模型名进输出
func TestRerankReachesRecallAndCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			http.Error(w, "wrong path", http.StatusNotFound)
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
		if body.Model != "编的重排" || body.Query != "主人" {
			t.Errorf("模型或查询不对：%+v", body)
		}
		n := len(body.Documents)
		results := make([]map[string]any, n)
		for i := range results {
			results[i] = map[string]any{"index": n - 1 - i, "relevance_score": float64(n - i)}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "results": results})
	}))
	defer srv.Close()

	ctx := context.Background()
	owner := memory.Audience{Channel: "console", Conversation: "console:编的", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
	q := memory.Query{Text: "主人", Mode: memory.ModeExplicit}

	plain := openMemory(ctx, rerankDb(t), nil, nil, 0, nil, nil)
	base := plain.Recall(ctx, owner, q)
	plain.Close()

	er := newEvalReranker(embed.RerankConnect(srv.URL+"/v1", "编的重排", 5*time.Second), "编的重排")
	m := openMemory(ctx, rerankDb(t), nil, er, 40, nil, nil)
	defer m.Close()
	r := m.Recall(ctx, owner, q)

	if !r.Reranked || r.RerankSkipped != "" {
		t.Fatalf("这一轮该用上重排：%v %q", r.Reranked, r.RerankSkipped)
	}
	var want []string
	for i := len(base.Candidates) - 1; i >= 0; i-- {
		want = append(want, base.Candidates[i].Ref)
	}
	var got []string
	for _, h := range r.Candidates {
		got = append(got, h.Ref)
	}
	if len(want) < 2 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("结果没跟着倒：%v，要 %v", got, want)
	}
	if head := er.head(); head != "编的重排" {
		t.Fatalf("输出里要写用的哪个重排：%q", head)
	}
	if line := er.line(); !strings.Contains(line, "调用 1 次 3 条") || !strings.Contains(line, "ms") {
		t.Fatalf("输出里要写调用次数、条数、总耗时：%q", line)
	}
	if st := er.stats(); st["calls"] != 1 || st["docs"] != 3 {
		t.Fatalf("结果 JSON 里的重排统计不对：%v", st)
	}
}

// 「没排成退回原顺序」那一段：0 次要写出来，有几次按原因分开
func TestSkipText(t *testing.T) {
	cases := []struct {
		m    map[string]int
		want string
	}{
		{map[string]int{}, "0 次"},
		{map[string]int{"超时": 2}, "2 次（超时 2）"},
		{map[string]int{"超时": 1, "出错": 1, "顺序不对": 1}, "3 次（超时 1、出错 1、顺序不对 1）"},
	}
	for _, c := range cases {
		if got := skipText(c.m); got != c.want {
			t.Errorf("%v：得到 %q，要 %q", c.m, got, c.want)
		}
	}
}

// -topk：只有明确回忆、且给了正数才带 TopK；带了以后走线上 memory_search 那条 Hits 分支，重排照样用上、顺序跟着换
func TestWithTopKUsesSearchBranch(t *testing.T) {
	explicit, normal := memory.Query{Text: "主人", Mode: memory.ModeExplicit}, memory.Query{Text: "主人", Mode: memory.ModeNormal}
	if withTopK(explicit, 0).TopK != 0 || withTopK(explicit, 12).TopK != 12 || withTopK(normal, 12).TopK != 0 || withTopK(explicit, -3).TopK != 0 {
		t.Fatal("TopK 只给明确回忆、只在 -topk 是正数时带")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Documents []string `json:"documents"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		n := len(body.Documents)
		results := make([]map[string]any, n)
		for i := range results {
			results[i] = map[string]any{"index": n - 1 - i, "relevance_score": float64(n - i)}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer srv.Close()
	ctx := context.Background()
	owner := memory.Audience{Channel: "console", Conversation: "console:编的", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
	plain := openMemory(ctx, rerankDb(t), nil, nil, 0, nil, nil)
	base := plain.Recall(ctx, owner, withTopK(explicit, 12))
	plain.Close()
	m := openMemory(ctx, rerankDb(t), nil, newEvalReranker(embed.RerankConnect(srv.URL+"/v1", "编的重排", 5*time.Second), "编的重排"), 40, nil, nil)
	defer m.Close()
	r := m.Recall(ctx, owner, withTopK(explicit, 12))
	if !r.Reranked || len(r.Hits) < 2 || len(r.Hits) != len(base.Hits) {
		t.Fatalf("TopK 分支也该重排且不加不减：%v %d / %d", r.Reranked, len(r.Hits), len(base.Hits))
	}
	if r.Hits[0].Ref != base.Hits[len(base.Hits)-1].Ref {
		t.Fatalf("Hits 的顺序没跟着倒：%v 对 %v", r.Hits[0].Ref, base.Hits[len(base.Hits)-1].Ref)
	}
}
