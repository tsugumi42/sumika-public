package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// 假服务：形状和 sumika-models / server.py 一样（/health、/v1/embeddings 认 base64、/v1/rerank、/v1/models/{id}/unload、/v1/models），
// 向量和分数是文字的确定性函数。TestParityPassesWhenIdentical 拿同一种假服务当两边；别的测试给它加"改坏"的开关，看命令会不会红。

type fakeService struct {
	t *testing.T
	// 改坏的开关：
	vecNoise   float32 // 加在每个向量第 0 维上的偏差（之后重新归一化）
	scoreNoise float64 // 加在每个重排分数上的偏差
	reverse    bool    // 重排把每组的名次倒过来
	failBatch  bool    // 64 条一批的嵌入请求回 500
}

func (f *fakeService) vec(text string) []float32 {
	h := fnv.New64a()
	h.Write([]byte(text))
	seed := h.Sum64()
	v := make([]float32, 768)
	var n float64
	for i := range v {
		seed = seed*6364136223846793005 + 1442695040888963407
		x := float64(int64(seed>>11))/float64(1<<52) - 1
		v[i] = float32(x)
		n += x * x
	}
	if f.vecNoise != 0 {
		v[0] += f.vecNoise
		n = 0
		for _, x := range v {
			n += float64(x) * float64(x)
		}
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(n))
	}
	return v
}

func (f *fakeService) score(q, d string) float64 {
	h := fnv.New64a()
	h.Write([]byte(q + "\x00" + d))
	x := float64(h.Sum64()%10000)/1000 - 5 // logit 在 [-5, 5)
	return 1/(1+math.Exp(-x)) + f.scoreNoise
}

func fakeBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (f *fakeService) handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"status": "ok", "engine": "go", "rss_bytes": 100 << 20, "loaded": []string{}})
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"data": []map[string]string{{"id": gteID}, {"id": bgeID}}})
	})
	mux.HandleFunc("/v1/models/", func(w http.ResponseWriter, r *http.Request) { write(w, map[string]any{"unloaded": true}) })
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if f.failBatch && len(body.Input) == speedBatch {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tokens := 0
		items := make([]map[string]any, len(body.Input))
		for i, s := range body.Input {
			if fakeBlank(s) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"detail":"` + s + `"}`)) // 故意回显输入：命令必须丢掉响应体
				return
			}
			v := f.vec(s)
			b := make([]byte, 4*len(v))
			for k, x := range v {
				binary.LittleEndian.PutUint32(b[4*k:], math.Float32bits(x))
			}
			items[i] = map[string]any{"index": i, "embedding": base64.StdEncoding.EncodeToString(b)}
			tokens += utf8.RuneCountInString(s) + 2
		}
		write(w, map[string]any{"data": items, "usage": map[string]int{"prompt_tokens": tokens}})
	})
	mux.HandleFunc("/v1/rerank", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string   `json:"query"`
			Docs  []string `json:"documents"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Query) == "" || utf8.RuneCountInString(body.Query) > 4000 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		res := make([]map[string]any, len(body.Docs))
		for i, d := range body.Docs {
			s := f.score(body.Query, d)
			if f.reverse {
				s = 1 - s
			}
			res[i] = map[string]any{"index": i, "relevance_score": s}
		}
		write(w, map[string]any{"results": res})
	})
	return mux
}

// fakeProc 把假服务包成 collect 要的 serverProc（不起进程）。
func fakeProc(t *testing.T, engine string, f *fakeService) *serverProc {
	t.Helper()
	unloadSettle = 0
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return &serverProc{l: launch{Engine: engine}, log: &lineBuf{}, cli: newClient(srv.URL, "t"), done: make(chan struct{}), exit: -1, began: time.Now()}
}

// fakeCounter：一个字符一个 token，加两个特殊符号。
type fakeCounter struct{}

func (fakeCounter) Count(text string) (int, error) { return utf8.RuneCountInString(text) + 2, nil }

// runFakes 对两份假服务各送 passes 遍，返回分析要的 goRuns / pyRuns 和清单。
func runFakes(t *testing.T, goSvc, pySvc *fakeService, passes int) *analysis {
	t.Helper()
	pl := testPlan(t, 2048)
	a := &analysis{o: &options{}, pl: pl, tok: &tokenResult{Single: map[string]verdict{}, Pair: map[[2]string]verdict{}}}
	for pass := 1; pass <= passes; pass++ {
		for _, side := range []struct {
			name string
			svc  *fakeService
			dst  *[]*sideRun
		}{{"go", goSvc, &a.goRuns}, {"python", pySvc, &a.pyRuns}} {
			run, err := collect(context.Background(), fakeProc(t, side.name, side.svc), pl, pass)
			if err != nil {
				t.Fatal(err)
			}
			*side.dst = append(*side.dst, run)
		}
	}
	return a
}
