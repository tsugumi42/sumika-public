package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/localmodels/ort"
)

// logitsByCall 让第 n 次前向（从 1 数）给出 vals[n-1] 这个 logit。
func logitsByCall(s *fakeSession, vals ...float32) {
	calls := 0
	s.row = func(n int, ids []int64, name string) ort.FloatTensor {
		calls++ // 重排是一次一行、锁里串行，不会并发改它
		return ort.FloatTensor{Shape: []int64{1, 1}, Data: []float32{vals[calls-1]}}
	}
}

func mustRerank(t *testing.T, r *Reranker, req RerankRequest) RerankResponse {
	t.Helper()
	res, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank：%v", err)
	}
	return res
}

func docsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("候选文档%d", i)
	}
	return out
}

// ---- GM3-7：查询预算、排序、top_n、sigmoid ----

func TestRerankBudgetAndOrder(t *testing.T) {
	t.Run("查询预算把两个特殊符号算进去：1008 通过，1009 报错", func(t *testing.T) {
		sess := newRerankSession()
		r, tok := newTestReranker(t, sess)
		ok := strings.Repeat("a", 1006) // 内容 1006 + <s> + </s> = 1008，正好等于 1024 − 16
		if _, err := r.Rerank(context.Background(), RerankRequest{Query: ok, Documents: []string{"文档"}}); err != nil {
			t.Fatalf("1008 该通过：%v", err)
		}
		over := strings.Repeat("a", 1007) // 1009
		calls := sess.callCount()
		pairs := tok.pair
		_, err := r.Rerank(context.Background(), RerankRequest{Query: over, Documents: []string{"文档"}})
		if !isRequestErr(err, "Query is 1009 tokens") || !isRequestErr(err, "at most 1008") {
			t.Fatalf("1009 该是 400：%v", err)
		}
		if sess.callCount() != calls || tok.pair != pairs {
			t.Fatal("查询超预算时不该编对、前向")
		}
	})

	t.Run("分数是 float64 的 sigmoid", func(t *testing.T) {
		sess := newRerankSession()
		logits := []float32{0.3, 2, -3, 0}
		logitsByCall(sess, logits...)
		r, _ := newTestReranker(t, sess)
		res := mustRerank(t, r, RerankRequest{Query: "查询", Documents: docsN(4)})
		byIndex := map[int]float64{}
		for _, x := range res.Results {
			byIndex[x.Index] = x.Score
			if x.Logit != float64(logits[x.Index]) {
				t.Errorf("文档 %d：Logit=%v，想要模型输出 %v", x.Index, x.Logit, logits[x.Index])
			}
		}
		for i, l := range logits {
			want := 1 / (1 + math.Exp(-float64(l)))
			if byIndex[i] != want {
				t.Errorf("文档 %d：logit %v 的分数 %v，想要 float64 的 sigmoid %v", i, l, byIndex[i], want)
			}
		}
		if byIndex[3] != 0.5 {
			t.Errorf("logit 0 该是 0.5：%v", byIndex[3])
		}
		if float64(float32(byIndex[0])) == byIndex[0] {
			t.Error("测试前提：0.3 的 sigmoid 转 float32 会丢精度，否则分辨不出是不是 float64 算的")
		}
	})

	t.Run("同分按下标", func(t *testing.T) {
		sess := newRerankSession()
		logitsByCall(sess, 1, 2, 2, 2, 0)
		r, _ := newTestReranker(t, sess)
		res := mustRerank(t, r, RerankRequest{Query: "查询", Documents: docsN(5)})
		var order []int
		for _, x := range res.Results {
			order = append(order, x.Index)
		}
		if fmt.Sprint(order) != "[1 2 3 0 4]" {
			t.Fatalf("名次 %v，想要 [1 2 3 0 4]（同分的 1、2、3 按下标）", order)
		}
	})

	t.Run("top_n 夹到 [1, 文档数]", func(t *testing.T) {
		for _, c := range []struct {
			topN *int
			want int
		}{{nil, 5}, {ptr(0), 1}, {ptr(-3), 1}, {ptr(1), 1}, {ptr(2), 2}, {ptr(5), 5}, {ptr(100), 5}} {
			sess := newRerankSession()
			logitsByCall(sess, 0, 1, 2, 3, 4)
			r, _ := newTestReranker(t, sess)
			res := mustRerank(t, r, RerankRequest{Query: "查询", Documents: docsN(5), TopN: c.topN})
			if len(res.Results) != c.want {
				label := "nil"
				if c.topN != nil {
					label = fmt.Sprint(*c.topN)
				}
				t.Errorf("top_n=%s：返回 %d 个，想要 %d", label, len(res.Results), c.want)
			}
			if res.Results[0].Index != 4 {
				t.Errorf("第一名该是分最高的文档 4：%v", res.Results)
			}
		}
	})
}

func TestRerankPairsAreOneRowEach(t *testing.T) {
	sess := newRerankSession()
	r, _ := newTestReranker(t, sess)
	docs := []string{"短", "中等长度的文档", strings.Repeat("长", 3000)}
	res := mustRerank(t, r, RerankRequest{Query: "问题", Documents: docs})
	calls := sess.snapshot()
	if len(calls) != 3 {
		t.Fatalf("3 个文档该前向 3 次，实际 %d 次", len(calls))
	}
	total := 0
	for i, c := range calls {
		pair, _ := (&fakeTok{}).EncodePair("问题", docs[i], 1024)
		in := c.inputs[0]
		if in.Name != "input_ids" || len(in.Shape) != 2 || in.Shape[0] != 1 || in.Shape[1] != int64(len(pair)) {
			t.Fatalf("第 %d 次：%s 的形状 %v，该是 [1 %d]", i, in.Name, in.Shape, len(pair))
		}
		for k := range pair {
			if in.Data[k] != int64(pair[k]) {
				t.Fatalf("第 %d 次的 id 和一对的分词结果不同", i)
			}
		}
		if len(c.outputs) != 1 || c.outputs[0] != "logits" {
			t.Fatalf("该只点名 logits：%v", c.outputs)
		}
		total += len(pair)
	}
	if res.Tokens != total {
		t.Fatalf("Tokens=%d，想要各对之和 %d", res.Tokens, total)
	}
	// 3000 个字的文档被截：一对总长正好是 max_tokens，查询没被截
	last := calls[2].inputs[0]
	if last.Shape[1] != 1024 {
		t.Fatalf("超长文档那一对长 %d，该被截到 1024", last.Shape[1])
	}
	q := idsOf("问题", 1024)[1:3]
	if last.Data[1] != q[0] || last.Data[2] != q[1] {
		t.Fatal("查询不该被截")
	}
}

func TestRerankRequestErrors(t *testing.T) {
	sess := newRerankSession()
	r, tok := newTestReranker(t, sess)
	long := strings.Repeat("长", 4097) // 字符上限 1024 × 4 = 4096
	cases := []struct {
		name string
		req  RerankRequest
		want string
	}{
		{"空查询", RerankRequest{Query: "", Documents: docsN(1)}, "Empty query"},
		{"空白查询", RerankRequest{Query: " \t\x1f", Documents: docsN(1)}, "Empty query"},
		{"没有文档", RerankRequest{Query: "查询"}, "at least 1 document"},
		{"65 个文档", RerankRequest{Query: "查询", Documents: docsN(65)}, "between 1 and 64"},
		{"空白文档", RerankRequest{Query: "查询", Documents: []string{"好的", "  "}}, "Empty document"},
		{"查询超字符上限", RerankRequest{Query: long, Documents: docsN(1)}, "The query is 4097 characters"},
		{"文档超字符上限", RerankRequest{Query: "查询", Documents: []string{"好的", long}}, "The document is 4097 characters"},
	}
	for _, c := range cases {
		if _, err := r.Rerank(context.Background(), c.req); !isRequestErr(err, c.want) {
			t.Errorf("%s：想要含 %q 的 400，实际 %v", c.name, c.want, err)
		}
	}
	if sess.callCount() != 0 || tok.pair != 0 || tok.count != 0 {
		t.Error("这些请求都该在分词之前被拦下")
	}

	// 64 个文档、每个都被截到 1024：65536 个 token，超过 32768
	docs := make([]string, 64)
	for i := range docs {
		docs[i] = fmt.Sprintf("%d", i) + strings.Repeat("文", 1500)
	}
	if _, err := r.Rerank(context.Background(), RerankRequest{Query: "查询", Documents: docs}); !isRequestErr(err, "32768") {
		t.Fatalf("一批 token 超限该 400：%v", err)
	}
	if sess.callCount() != 0 {
		t.Fatal("超限的请求不该前向")
	}
	// 31 个 × 1024 = 31744 通过
	if _, err := r.Rerank(context.Background(), RerankRequest{Query: "查询", Documents: docs[:31]}); err != nil {
		t.Fatalf("31 × 1024 = 31744 该通过：%v", err)
	}
}

func TestRerankBadLogitsAreErrors(t *testing.T) {
	for name, c := range map[string]struct {
		tensor ort.FloatTensor
		want   string
	}{
		"NaN":      {ort.FloatTensor{Shape: []int64{1, 1}, Data: []float32{float32(math.NaN())}}, "NaN"},
		"-Inf":     {ort.FloatTensor{Shape: []int64{1, 1}, Data: []float32{float32(math.Inf(-1))}}, "NaN"},
		"形状 [1 2]": {ort.FloatTensor{Shape: []int64{1, 2}, Data: []float32{1, 2}}, "形状"},
		"形状 [2 1]": {ort.FloatTensor{Shape: []int64{2, 1}, Data: []float32{1, 2}}, "形状"},
		"一维":       {ort.FloatTensor{Shape: []int64{1}, Data: []float32{1}}, "形状"},
	} {
		t.Run(name, func(t *testing.T) {
			sess := newRerankSession()
			sess.row = func(n int, ids []int64, name string) ort.FloatTensor { return c.tensor }
			r, _ := newTestReranker(t, sess)
			res, err := r.Rerank(context.Background(), RerankRequest{Query: "查询", Documents: docsN(2)})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("想要含 %q 的错误：%v", c.want, err)
			}
			var re *RequestError
			if errors.As(err, &re) {
				t.Fatal("模型的问题是 500 类，不是 RequestError")
			}
			if len(res.Results) != 0 {
				t.Fatal("出错时不该返回名次")
			}
		})
	}
}

func TestRerankCancelBetweenPairs(t *testing.T) {
	sess := newRerankSession()
	sess.delay = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess.onRun = func(n int) error {
		if n == 2 {
			cancel()
		}
		return nil
	}
	r, _ := newTestReranker(t, sess)
	_, err := r.Rerank(ctx, RerankRequest{Query: "查询", Documents: docsN(5)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("该返回取消错误：%v", err)
	}
	if sess.callCount() != 2 {
		t.Fatalf("第二对跑完就该停，前向 %d 次", sess.callCount())
	}
}

func TestNewRerankerRejects(t *testing.T) {
	if _, err := NewReranker(mustBuiltin(t, "gte-multilingual-base"), &fakeTok{}, newRerankSession()); err == nil || !strings.Contains(err.Error(), "不是重排模型") {
		t.Errorf("嵌入模型不能当重排用：%v", err)
	}
	spec := mustBuiltin(t, "bge-reranker-v2-m3")
	noLogits := newRerankSession()
	noLogits.outputs = []ort.TensorInfo{{Name: "scores", Type: ort.TypeFloat32}}
	if _, err := NewReranker(spec, &fakeTok{}, noLogits); err == nil || !strings.Contains(err.Error(), "logits") {
		t.Errorf("没有 logits 输出的图该在加载时被拒绝：%v", err)
	}
	if _, err := NewReranker(spec, nil, newRerankSession()); err == nil {
		t.Error("没有分词器该被拒绝")
	}
	if _, err := NewReranker(spec, &fakeTok{}, nil); err == nil {
		t.Error("没有会话该被拒绝")
	}
}
