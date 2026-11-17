package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/localmodels/ort"
)

// ---- 小工具 ----

func idsOf(text string, maxTokens int) []int64 {
	ids, _ := (&fakeTok{}).EncodeSingle(text, maxTokens)
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out
}

func bitsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Float32bits(a[i]) != math.Float32bits(b[i]) {
			return false
		}
	}
	return true
}

func closeTo(t *testing.T, label string, got []float32, want []float64, tol float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s：长度 %d，想要 %d", label, len(got), len(want))
	}
	for i := range got {
		if d := math.Abs(float64(got[i]) - want[i]); d > tol {
			t.Fatalf("%s：第 %d 维 %v，想要 %v（差 %g）", label, i, got[i], want[i], d)
		}
	}
}

func norm64(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func isRequestErr(err error, wantSub string) bool {
	var re *RequestError
	return errors.As(err, &re) && strings.Contains(re.Detail, wantSub)
}

func mustEmbed(t *testing.T, e *Embedder, req EmbedRequest) EmbedResult {
	t.Helper()
	res, err := e.Embed(context.Background(), req)
	if err != nil {
		t.Fatalf("Embed(%d 条)：%v", len(req.Texts), err)
	}
	return res
}

func texts(ss ...string) EmbedRequest { return EmbedRequest{Texts: ss} }

// ---- GM3-1：池化跟着 token_embeddings ----

func TestPoolingFollowsTokenEmbeddings(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension) // 输出里同时有 token_embeddings 和 sentence_embedding（全是 9，故意不同）
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	res := mustEmbed(t, e, texts("你好世界"))
	closeTo(t, "向量", res.Vectors[0], expectedVec(idsOf("你好世界", spec.MaxTokens), spec.Dimension, true), 1e-6)
	for _, c := range sess.snapshot() {
		if len(c.outputs) != 1 || c.outputs[0] != "token_embeddings" {
			t.Fatalf("该只点名 token_embeddings，实际请求了 %v", c.outputs)
		}
	}

	t.Run("last_hidden_state 优先", func(t *testing.T) {
		s := newEmbedSession(spec.Dimension, "token_embeddings", "last_hidden_state", "sentence_embedding")
		e, _ := newTestEmbedder(t, spec, s, EmbedderOptions{})
		mustEmbed(t, e, texts("甲"))
		if got := s.snapshot()[0].outputs; len(got) != 1 || got[0] != "last_hidden_state" {
			t.Fatalf("图里有 last_hidden_state 该用它，实际请求 %v", got)
		}
	})
	t.Run("只有 sentence_embedding 的图不收", func(t *testing.T) {
		s := newEmbedSession(spec.Dimension, "sentence_embedding")
		if _, err := NewEmbedder(spec, &fakeTok{}, s, EmbedderOptions{}); err == nil || !strings.Contains(err.Error(), "token_embeddings") {
			t.Fatalf("没有 last_hidden_state / token_embeddings 的图该在加载时就被拒绝：%v", err)
		}
	})
}

// ---- GM3-2：一次一行 ----

func TestOneRowPerRun(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	batch := []string{"短", "稍微长一点的一句话", "第三句，长度又不一样，所以不能被补 pad 凑成一样长"}
	res := mustEmbed(t, e, EmbedRequest{Texts: batch})
	calls := sess.snapshot()
	if len(calls) != len(batch) {
		t.Fatalf("%d 条文本该跑 %d 次前向（一次一行），实际 %d 次", len(batch), len(batch), len(calls))
	}
	lens := map[int]bool{}
	for i, c := range calls {
		ids := idsOf(batch[i], spec.MaxTokens)
		lens[len(ids)] = true
		if len(c.inputs) != 2 {
			t.Fatalf("第 %d 次：输入 %d 个，该是 input_ids 和 attention_mask", i, len(c.inputs))
		}
		for _, in := range c.inputs {
			if len(in.Shape) != 2 || in.Shape[0] != 1 || in.Shape[1] != int64(len(ids)) {
				t.Fatalf("第 %d 次 %s 的形状 %v，该是 [1 %d]", i, in.Name, in.Shape, len(ids))
			}
			switch in.Name {
			case "input_ids":
				for k := range ids {
					if in.Data[k] != ids[k] {
						t.Fatalf("第 %d 次 input_ids 和分词结果不一样", i)
					}
				}
			case "attention_mask":
				for _, m := range in.Data {
					if m != 1 {
						t.Fatalf("第 %d 次 attention_mask 里有 %d：不补 pad，该全是 1", i, m)
					}
				}
			default:
				t.Fatalf("多喂了 %s", in.Name)
			}
		}
	}
	if len(lens) != len(batch) {
		t.Fatalf("三条的长度该各不相同（否则测不出补 pad）：%v", lens)
	}
	wantTokens := 0
	for _, s := range batch {
		wantTokens += len(idsOf(s, spec.MaxTokens))
	}
	if res.Tokens != wantTokens {
		t.Fatalf("Tokens = %d，想要 %d（含特殊符号）", res.Tokens, wantTokens)
	}
}

func TestPositionIDsFedWhenGraphWantsThem(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	sess.inputs = append(sess.inputs, ort.TensorInfo{Name: "position_ids", Type: i64, Shape: []int64{-1, -1}})
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	mustEmbed(t, e, texts("四个字啊"))
	c := sess.snapshot()[0]
	if len(c.inputs) != 3 || c.inputs[2].Name != "position_ids" {
		t.Fatalf("输入该按图里的顺序给三个：%v", c.inputs)
	}
	for i, p := range c.inputs[2].Data {
		if p != int64(i) {
			t.Fatalf("position_ids[%d] = %d，该是 0..n-1", i, p)
		}
	}
}

func TestRunnerRejectsUnsupportedGraphs(t *testing.T) {
	spec := smallSpec()
	mk := func(extra ...ort.TensorInfo) *fakeSession {
		s := newEmbedSession(spec.Dimension)
		s.inputs = append(s.inputs, extra...)
		return s
	}
	for name, c := range map[string]struct {
		s    *fakeSession
		want string
	}{
		"带 KV 缓存的图（Qwen 那种）":    {mk(ort.TensorInfo{Name: "past_key_values.0.key", Type: ort.TypeFloat32}), "没实现"},
		"不认识的输入 token_type_ids": {mk(ort.TensorInfo{Name: "token_type_ids", Type: i64}), "不认识"},
		"input_ids 不是 int64": {func() *fakeSession {
			s := mk()
			s.inputs[0].Type = ort.TypeFloat32
			return s
		}(), "int64"},
		"没有 input_ids": {func() *fakeSession {
			s := mk()
			s.inputs = s.inputs[1:]
			return s
		}(), "input_ids"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewEmbedder(spec, &fakeTok{}, c.s, EmbedderOptions{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("想要含 %q 的错误：%v", c.want, err)
			}
		})
	}
}

func TestNewEmbedderRejects(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	if _, err := NewEmbedder(mustBuiltin(t, "bge-reranker-v2-m3"), &fakeTok{}, newRerankSession(), EmbedderOptions{}); err == nil || !strings.Contains(err.Error(), "不是嵌入模型") {
		t.Errorf("重排模型不能当嵌入用：%v", err)
	}
	bad := spec
	bad.Pooling = "mean"
	if _, err := NewEmbedder(bad, &fakeTok{}, sess, EmbedderOptions{}); err == nil {
		t.Error("登记项不合格该被拒绝")
	}
	if _, err := NewEmbedder(spec, nil, sess, EmbedderOptions{}); err == nil {
		t.Error("没有分词器该被拒绝")
	}
	if _, err := NewEmbedder(spec, &fakeTok{}, nil, EmbedderOptions{}); err == nil {
		t.Error("没有会话该被拒绝")
	}
	for _, n := range []int{-1, 1, 7} {
		if _, err := NewEmbedder(spec, &fakeTok{}, sess, EmbedderOptions{MaxLineTokens: n}); err == nil {
			t.Errorf("MaxLineTokens=%d 该被拒绝", n)
		}
	}
}

// ---- GM3-3：坏向量是错误，不吐向量 ----

func TestBadVectorsAreErrors(t *testing.T) {
	spec := smallSpec()
	dim := spec.Dimension
	row := func(f func(n int) ort.FloatTensor) func(n int, ids []int64, name string) ort.FloatTensor {
		return func(n int, ids []int64, name string) ort.FloatTensor { return f(n) }
	}
	filled := func(n, d int, first func(j int) float32) []float32 {
		data := make([]float32, n*d)
		for r := 0; r < n; r++ {
			for j := 0; j < d; j++ {
				if r == 0 {
					data[j] = first(j)
				} else {
					data[r*d+j] = 1
				}
			}
		}
		return data
	}
	one := func(j int) float32 { return 1 }
	cases := map[string]struct {
		f    func(n int) ort.FloatTensor
		want string
	}{
		"NaN": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(dim)}, Data: filled(n, dim, func(j int) float32 {
				if j == 3 {
					return float32(math.NaN())
				}
				return 1
			})}
		}, "NaN"},
		"+Inf": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(dim)}, Data: filled(n, dim, func(j int) float32 {
				if j == 0 {
					return float32(math.Inf(1))
				}
				return 1
			})}
		}, "NaN"},
		"零向量": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(dim)}, Data: filled(n, dim, func(j int) float32 { return 0 })}
		}, "零向量"},
		"范数溢出": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(dim)}, Data: filled(n, dim, func(j int) float32 { return math.MaxFloat32 })}
		}, "溢出"},
		"多一行": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n + 1), int64(dim)}, Data: filled(n+1, dim, one)}
		}, "形状"},
		"批维是 2 但数据只有一行": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{2, int64(n), int64(dim)}, Data: filled(n, dim, one)}
		}, "形状"},
		"批大小是 2": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{2, int64(n), int64(dim)}, Data: filled(2*n, dim, one)}
		}, "形状"},
		"维度少一": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(dim - 1)}, Data: filled(n, dim-1, one)}
		}, "形状"},
		"只有二维": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(dim)}, Data: filled(1, dim, one)}
		}, "形状"},
		"数据比形状短": {func(n int) ort.FloatTensor {
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(dim)}, Data: make([]float32, n*dim-1)}
		}, "形状"},
	}
	const secret = "绝密正文XYZ"
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			sess := newEmbedSession(dim)
			sess.row = row(c.f)
			e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
			res, err := e.Embed(context.Background(), texts(secret))
			if err == nil {
				t.Fatalf("坏输出该是错误，却得到了向量 %v", res.Vectors)
			}
			var re *RequestError
			if errors.As(err, &re) {
				t.Fatalf("模型的问题是 500 类，不是 RequestError：%v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误里该有 %q：%v", c.want, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("错误里不该带输入文字：%v", err)
			}
			if len(res.Vectors) != 0 {
				t.Fatal("出错时不该返回向量")
			}
			if e.cache.len() != 0 {
				t.Fatal("出错的结果不该进缓存")
			}
		})
	}
}

func TestNormalizeFalseKeepsRawRow(t *testing.T) {
	spec := smallSpec()
	spec.Normalize = false
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	res := mustEmbed(t, e, texts("原样"))
	closeTo(t, "不归一化", res.Vectors[0], expectedVec(idsOf("原样", spec.MaxTokens), spec.Dimension, false), 0)
	if n := norm64(res.Vectors[0]); math.Abs(n-1) < 1e-3 {
		t.Fatalf("范数 %g：不该被归一化", n)
	}
	// dimensions 等于全维：不动（不归一化的模型也不能被偷偷归一化）
	full := spec.Dimension
	res = mustEmbed(t, e, EmbedRequest{Texts: []string{"原样"}, Dimensions: &full})
	closeTo(t, "dimensions 等于全维", res.Vectors[0], expectedVec(idsOf("原样", spec.MaxTokens), spec.Dimension, false), 0)
	// 截断 dimensions 不管模型归不归一化，都要重新归一化
	d := 4
	res = mustEmbed(t, e, EmbedRequest{Texts: []string{"原样"}, Dimensions: &d})
	if n := norm64(res.Vectors[0]); math.Abs(n-1) > 1e-6 {
		t.Fatalf("截断后范数 %g，该是 1", n)
	}
}

// ---- GM3-4：缓存和批限额 ----

func TestCacheAndBatchLimit(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	big := make([]string, 5)
	for i := range big {
		big[i] = strings.Repeat(string(rune('a'+i)), 8000) // 8000 个内容 token，加特殊符号 8002
	}
	for _, b := range big { // 一条一条先放进缓存
		if res := mustEmbed(t, e, texts(b)); res.Tokens != 8002 {
			t.Fatalf("单条 Tokens=%d，想要 8002", res.Tokens)
		}
	}
	if sess.callCount() != 5 {
		t.Fatalf("预置缓存跑了 %d 次前向，想要 5", sess.callCount())
	}
	// 5 条 × 8002 = 40010 > 32768：全命中缓存也要拒绝
	if _, err := e.Embed(context.Background(), EmbedRequest{Texts: big}); !isRequestErr(err, "32768") {
		t.Fatalf("5 条 8000 token 全在缓存里也该被 400 拦下：%v", err)
	}
	// 4 条 × 8002 = 32008 通过，全命中、不再前向、命中的也计 token
	res := mustEmbed(t, e, EmbedRequest{Texts: big[:4]})
	if res.Tokens != 4*8002 || res.CacheHits != 4 || sess.callCount() != 5 {
		t.Fatalf("4 条：Tokens=%d（想要 %d）CacheHits=%d（想要 4）前向 %d 次（想要还是 5）", res.Tokens, 4*8002, res.CacheHits, sess.callCount())
	}
	// 4 条命中 + 1 条没缓存的：超限，拒绝发生在前向之前，新的那条也不进缓存
	fresh := strings.Repeat("z", 8000)
	if _, err := e.Embed(context.Background(), EmbedRequest{Texts: append(append([]string{}, big[:4]...), fresh)}); !isRequestErr(err, "32768") {
		t.Fatalf("命中加没命中超限该被拦下：%v", err)
	}
	if sess.callCount() != 5 {
		t.Fatalf("超限的请求不该前向，前向 %d 次", sess.callCount())
	}
	if res := mustEmbed(t, e, texts(fresh)); res.CacheHits != 0 {
		t.Fatal("被拒绝的请求里那条新文本不该进缓存")
	}
}

func TestBatchCountAndBlankRules(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	mk := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("第%d句", i)
		}
		return out
	}
	if _, err := e.Embed(context.Background(), EmbedRequest{}); !isRequestErr(err, "at least 1") {
		t.Errorf("0 条：%v", err)
	}
	if _, err := e.Embed(context.Background(), EmbedRequest{Texts: mk(65)}); !isRequestErr(err, "between 1 and 64") {
		t.Errorf("65 条：%v", err)
	}
	if res := mustEmbed(t, e, EmbedRequest{Texts: mk(64)}); len(res.Vectors) != 64 {
		t.Errorf("64 条该通过：%d 个向量", len(res.Vectors))
	}
	before := sess.callCount()
	for _, bad := range []string{"", " ", "\t\n", "\x1f", "\u3000"} {
		if _, err := e.Embed(context.Background(), texts("正常的一条", bad)); !isRequestErr(err, "Empty input") {
			t.Errorf("夹着空白输入 %q 该整批 400：%v", bad, err)
		}
	}
	if sess.callCount() != before {
		t.Error("有空白输入的请求不该前向（整批先验后算）")
	}
}

// ---- GM3-5：dimensions ----

func TestDimensions(t *testing.T) {
	spec := mustBuiltin(t, "gte-multilingual-base")
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	full := mustEmbed(t, e, texts("截断用的句子")).Vectors[0]
	if len(full) != 768 || math.Abs(norm64(full)-1) > 1e-6 {
		t.Fatalf("全维向量：长度 %d 范数 %g", len(full), norm64(full))
	}
	calls := sess.callCount()
	for _, d := range []int{-5, 0, 1, 64, 127, 769, 10000} {
		d := d
		_, err := e.Embed(context.Background(), EmbedRequest{Texts: []string{"截断用的句子"}, Dimensions: &d})
		if !isRequestErr(err, "dimensions must be within [128, 768]") {
			t.Errorf("dimensions=%d 该 400：%v", d, err)
		}
	}
	if sess.callCount() != calls {
		t.Error("dimensions 不合法的请求不该前向")
	}
	d128 := 128
	got := mustEmbed(t, e, EmbedRequest{Texts: []string{"截断用的句子"}, Dimensions: &d128}).Vectors[0]
	if len(got) != 128 || math.Abs(norm64(got)-1) > 1e-6 {
		t.Fatalf("128 维：长度 %d 范数 %g", len(got), norm64(got))
	}
	want := make([]float64, 128)
	n := norm64(full[:128])
	for i := range want {
		want[i] = float64(full[i]) / n
	}
	closeTo(t, "128 维是全维前 128 个分量重新归一化", got, want, 1e-6)
	d768 := 768
	same := mustEmbed(t, e, EmbedRequest{Texts: []string{"截断用的句子"}, Dimensions: &d768}).Vectors[0]
	if !bitsEqual(same, full) {
		t.Error("dimensions 等于全维时不动：该和不传逐位相同")
	}

	t.Run("不支持 matryoshka 的模型", func(t *testing.T) {
		s := smallSpec()
		s.Matryoshka, s.MinDimensions = false, DefaultMinDimensions
		e, _ := newTestEmbedder(t, s, newEmbedSession(s.Dimension), EmbedderOptions{})
		d := 4
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: []string{"甲"}, Dimensions: &d}); !isRequestErr(err, "does not support dimensions") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("截完是零向量是 400，缓存里的全维向量不受影响", func(t *testing.T) {
		s := smallSpec()
		sess := newEmbedSession(s.Dimension)
		sess.row = func(n int, ids []int64, name string) ort.FloatTensor {
			data := make([]float32, n*s.Dimension)
			for r := 0; r < n; r++ {
				for j := 4; j < s.Dimension; j++ { // 前 4 维是 0，后面不是
					data[r*s.Dimension+j] = 1
				}
			}
			return ort.FloatTensor{Shape: []int64{1, int64(n), int64(s.Dimension)}, Data: data}
		}
		e, _ := newTestEmbedder(t, s, sess, EmbedderOptions{})
		d := 4
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: []string{"甲"}, Dimensions: &d}); !isRequestErr(err, "zero vector") {
			t.Fatalf("%v", err)
		}
		res := mustEmbed(t, e, texts("甲"))
		if res.CacheHits != 1 || len(res.Vectors[0]) != 8 {
			t.Fatalf("全维向量该已在缓存里：命中 %d 个，长度 %d", res.CacheHits, len(res.Vectors[0]))
		}
	})
}

// ---- GM3-6：字符上限按 rune ----

func TestCharLimitIsRunes(t *testing.T) {
	spec := smallSpec() // max_tokens 8192 → 字符上限 32768
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	han := func(n int) string { return strings.Repeat("汉", n) }
	if len(han(20000)) != 60000 {
		t.Fatal("测试前提：2 万个汉字是 6 万字节")
	}
	for _, n := range []int{20000, 32768} {
		res := mustEmbed(t, e, texts(han(n)))
		if res.Tokens != 8192 {
			t.Errorf("%d 个汉字：Tokens=%d，该被截到 8192（内容 8190 加两个特殊符号）", n, res.Tokens)
		}
	}
	calls := sess.callCount()
	if _, err := e.Embed(context.Background(), texts(han(32769))); !isRequestErr(err, "32769 characters") {
		t.Fatalf("32769 个汉字该被 400 拦下：%v", err)
	}
	if sess.callCount() != calls {
		t.Error("超字符上限的请求不该分词、前向")
	}

	t.Run("前缀算进字符数", func(t *testing.T) {
		s := smallSpec()
		s.QueryPrefix = "前缀：" // 3 个字符
		e, _ := newTestEmbedder(t, s, newEmbedSession(s.Dimension), EmbedderOptions{})
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: []string{han(32765)}, InputType: InputQuery}); err != nil {
			t.Fatalf("32765 + 3 = 32768 该通过：%v", err)
		}
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: []string{han(32766)}, InputType: InputQuery}); !isRequestErr(err, "prefix included") {
			t.Fatalf("32766 + 3 = 32769 该被拦下：%v", err)
		}
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: []string{han(32766)}}); err != nil {
			t.Fatalf("同一条按 document 没有前缀，32766 该通过：%v", err)
		}
	})
}

// ---- GM3-12：零宽字符不算空 ----

func TestZeroWidthOnlyIsNotEmpty(t *testing.T) {
	spec := smallSpec()
	for _, text := range []string{"\u200b", strings.Repeat("\u200b", 32768), " \u200b ", "\ufeff"} {
		sess := newEmbedSession(spec.Dimension)
		e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
		res, err := e.Embed(context.Background(), texts(text))
		if err != nil {
			t.Fatalf("%d 个字符、只含零宽的输入不是空，该照常算：%v", len([]rune(text)), err)
		}
		if len(res.Vectors) != 1 || math.Abs(norm64(res.Vectors[0])-1) > 1e-6 {
			t.Fatalf("该有一个单位向量：%v", res.Vectors)
		}
		if sess.callCount() != 1 {
			t.Fatalf("前向 %d 次，想要 1", sess.callCount())
		}
	}
	// 假分词器对 U+200B 不出内容 token：这一行的 ids 就是 [<s>, </s>]，引擎不能因为"内容为空"拒绝
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	mustEmbed(t, e, texts("\u200b\u200b"))
	if ids := sess.snapshot()[0].inputs[0].Data; len(ids) != 2 || ids[0] != 0 || ids[1] != 2 {
		t.Fatalf("前向的 input_ids = %v，该是 [0 2]", ids)
	}
}

// ---- GM3-11：缓存不和调用方共用切片 ----

func TestCacheNotAliased(t *testing.T) {
	spec := mustBuiltin(t, "gte-multilingual-base")
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	d128 := 128

	// 先要 128 维，再要全维
	short := mustEmbed(t, e, EmbedRequest{Texts: []string{"别名检查"}, Dimensions: &d128}).Vectors[0]
	if len(short) != 128 {
		t.Fatalf("128 维：%d", len(short))
	}
	full := mustEmbed(t, e, texts("别名检查"))
	if full.CacheHits != 1 || len(full.Vectors[0]) != 768 || math.Abs(norm64(full.Vectors[0])-1) > 1e-6 {
		t.Fatalf("全维应当是缓存命中：命中 %d 长度 %d 范数 %g", full.CacheHits, len(full.Vectors[0]), norm64(full.Vectors[0]))
	}
	// 和另一个全新嵌入器直接算的全维逐位相同：缓存里存的是没被截断改过的全维向量
	e2, _ := newTestEmbedder(t, spec, newEmbedSession(spec.Dimension), EmbedderOptions{})
	fresh := mustEmbed(t, e2, texts("别名检查")).Vectors[0]
	if !bitsEqual(full.Vectors[0], fresh) {
		t.Fatal("先要 128 维以后，缓存里的全维向量被改过")
	}

	// 调用方改了拿到的向量，再取一次不受影响
	keep := append([]float32(nil), full.Vectors[0]...)
	for i := range full.Vectors[0] {
		full.Vectors[0][i] = 0
	}
	for i := range short {
		short[i] = -1
	}
	again := mustEmbed(t, e, texts("别名检查"))
	if !bitsEqual(again.Vectors[0], keep) {
		t.Fatal("调用方改了返回的向量，缓存跟着变了")
	}

	// 第一次算出来的向量（没走缓存的）被调用方改了，缓存里也不能变
	first := mustEmbed(t, e, texts("第一次算的"))
	snapshot := append([]float32(nil), first.Vectors[0]...)
	for i := range first.Vectors[0] {
		first.Vectors[0][i] = 7
	}
	if second := mustEmbed(t, e, texts("第一次算的")); !bitsEqual(second.Vectors[0], snapshot) {
		t.Fatal("放进缓存时没拷贝：调用方改了第一次拿到的向量，缓存跟着变了")
	}
}

func TestCachedSecondPassIdentical(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	batch := []string{"一", "二二", "三三三", "二二"} // 里面有重复的：同一批里重复的各算各的，和 Python 一致
	first := mustEmbed(t, e, EmbedRequest{Texts: batch})
	if first.CacheHits != 0 || sess.callCount() != 4 {
		t.Fatalf("第一遍：命中 %d（想要 0），前向 %d 次（想要 4）", first.CacheHits, sess.callCount())
	}
	second := mustEmbed(t, e, EmbedRequest{Texts: batch})
	if second.CacheHits != 4 || sess.callCount() != 4 {
		t.Fatalf("第二遍：命中 %d（想要 4），前向 %d 次（想要还是 4）", second.CacheHits, sess.callCount())
	}
	if second.Tokens != first.Tokens {
		t.Fatalf("Tokens 第二遍 %d，第一遍 %d", second.Tokens, first.Tokens)
	}
	for i := range batch {
		if !bitsEqual(first.Vectors[i], second.Vectors[i]) {
			t.Fatalf("第 %d 条两遍不是逐位相同", i)
		}
	}
	if !bitsEqual(first.Vectors[1], first.Vectors[3]) {
		t.Fatal("同一批里的重复文本向量该相同")
	}
}

func TestCacheLRU(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	name := func(i int) string { return fmt.Sprintf("条目%03d", i) }
	for start := 0; start < CacheSize; start += MaxBatchSize { // 256 条，一次最多 64
		var batch []string
		for i := start; i < start+MaxBatchSize; i++ {
			batch = append(batch, name(i))
		}
		mustEmbed(t, e, EmbedRequest{Texts: batch})
	}
	if e.cache.len() != CacheSize {
		t.Fatalf("缓存 %d 条，想要 %d", e.cache.len(), CacheSize)
	}
	calls := sess.callCount()
	mustEmbed(t, e, texts(name(0))) // 命中，挪到最前
	mustEmbed(t, e, texts(name(CacheSize)))
	if e.cache.len() != CacheSize {
		t.Fatalf("放满以后再放一条，缓存 %d 条，想要 %d", e.cache.len(), CacheSize)
	}
	if res := mustEmbed(t, e, texts(name(0))); res.CacheHits != 1 {
		t.Error("刚用过的第 0 条不该被丢")
	}
	if res := mustEmbed(t, e, texts(name(1))); res.CacheHits != 0 {
		t.Error("最久没用的第 1 条该被丢了")
	}
	if got := sess.callCount() - calls; got != 2 { // name(256) 和被丢后重算的 name(1)
		t.Errorf("之后新增前向 %d 次，想要 2", got)
	}
	e.ClearCache()
	if e.cache.len() != 0 {
		t.Error("ClearCache 后缓存该是空的")
	}
}

// ---- 取消和锁 ----

func TestCancelBetweenRows(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	sess.delay = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess.onRun = func(n int) error {
		if n == 2 {
			cancel() // 第二行开始跑的时候取消：这一行打断不了，跑完在行边界停
		}
		return nil
	}
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	batch := []string{"甲甲", "乙乙", "丙丙", "丁丁"}
	res, err := e.Embed(ctx, EmbedRequest{Texts: batch})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("该返回取消错误：%v", err)
	}
	if len(res.Vectors) != 0 {
		t.Error("取消时不返回向量")
	}
	if sess.callCount() != 2 {
		t.Fatalf("第二行跑完就该停，前向 %d 次（想要 2）", sess.callCount())
	}
	if e.cache.len() != 0 {
		t.Fatalf("取消的这批，已经算完的 %d 行不该进缓存", e.cache.len())
	}
	sess.onRun = nil
	sess.delay = 0
	if again := mustEmbed(t, e, EmbedRequest{Texts: batch}); again.CacheHits != 0 || sess.callCount() != 6 {
		t.Fatalf("下次全部重算：命中 %d（想要 0），前向共 %d 次（想要 6）", again.CacheHits, sess.callCount())
	}

	t.Run("请求一开始就取消了", func(t *testing.T) {
		sess := newEmbedSession(spec.Dimension)
		e, tok := newTestEmbedder(t, spec, sess, EmbedderOptions{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := e.Embed(ctx, texts("甲")); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
		if sess.callCount() != 0 || tok.single != 0 {
			t.Fatal("已经取消的请求不该分词、前向")
		}
	})
	t.Run("超时是 DeadlineExceeded", func(t *testing.T) {
		sess := newEmbedSession(spec.Dimension)
		sess.delay = 40 * time.Millisecond
		e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := e.Embed(ctx, texts("甲", "乙", "丙")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
		if sess.callCount() >= 3 {
			t.Fatalf("超时以后不该把三行都算完：前向 %d 次", sess.callCount())
		}
	})
}

func TestLockWaitHonorsContext(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	sess.delay = 300 * time.Millisecond
	started := make(chan struct{})
	var once sync.Once
	sess.onRun = func(n int) error { once.Do(func() { close(started) }); return nil }
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})

	done := make(chan error, 1)
	go func() { _, err := e.Embed(context.Background(), texts("占着锁的")); done <- err }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, err := e.Embed(ctx, texts("等锁的"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等锁时该看 ctx：%v", err)
	}
	if waited := time.Since(t0); waited > 200*time.Millisecond {
		t.Fatalf("等锁等了 %v：ctx 到点就该走，不该等到锁放开", waited)
	}
	if err := <-done; err != nil {
		t.Fatalf("占着锁的那一路该正常完成：%v", err)
	}
	if sess.callCount() != 1 {
		t.Fatalf("等锁超时的那一路不该前向：共 %d 次", sess.callCount())
	}
}

// 模型锁只围住一行的 Run：后台补向量的大批进行中，来一个单句，单句最多等一行的时间，不等整批。
func TestRowLockInterleaves(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	sess.delay = 20 * time.Millisecond
	single := "单句请求"
	singleIDs := idsOf(single, spec.MaxTokens)
	signal := make(chan struct{})
	sess.onRun = func(n int) error {
		if n == 4 {
			close(signal)
		}
		return nil
	}
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	const batchRows = 40
	batch := make([]string, batchRows)
	for i := range batch {
		batch[i] = fmt.Sprintf("批量第%d条文字", i)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	var batchErr, singleErr error
	var singleTook time.Duration
	go func() { defer wg.Done(); _, batchErr = e.Embed(context.Background(), EmbedRequest{Texts: batch}) }()
	go func() {
		defer wg.Done()
		<-signal
		t0 := time.Now()
		_, singleErr = e.Embed(context.Background(), texts(single))
		singleTook = time.Since(t0)
	}()
	wg.Wait()
	if batchErr != nil || singleErr != nil {
		t.Fatalf("批：%v；单句：%v", batchErr, singleErr)
	}
	pos := -1
	calls := sess.snapshot()
	for i, c := range calls {
		ids := c.inputs[0].Data
		if len(ids) == len(singleIDs) && ids[1] == singleIDs[1] && ids[2] == singleIDs[2] && ids[3] == singleIDs[3] {
			pos = i + 1 // 第几次前向
		}
	}
	if pos < 0 || len(calls) != batchRows+1 {
		t.Fatalf("单句的前向在第 %d 次，共 %d 次（想要 %d）", pos, len(calls), batchRows+1)
	}
	// 单句在第 4 行跑着的时候到：先进先出，最多再等当前这一行，它就是下一行（第 5 次）；留一点调度的余量
	if pos > 7 {
		t.Fatalf("单句排在第 %d 次前向，批共 %d 行：锁该只围一行，单句不该等整批", pos, batchRows)
	}
	if singleTook > time.Duration(batchRows/2)*sess.delay {
		t.Fatalf("单句用了 %v，批整个要 %v", singleTook, time.Duration(batchRows)*sess.delay)
	}
}

// ---- 请求检查的顺序和别的参数 ----

func TestEmbedCheckOrder(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, tok := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	bad := ptr(99999)
	tooLong := strings.Repeat("长", spec.CharLimit()+1)
	cases := []struct {
		name string
		req  EmbedRequest
		want string
	}{
		{"input_type 最先", EmbedRequest{Texts: nil, InputType: "passage"}, "input_type"},
		{"没有输入先于 dimensions", EmbedRequest{Texts: nil, Dimensions: bad}, "at least 1"},
		{"条数过多先于空白", EmbedRequest{Texts: make([]string, 65), Dimensions: bad}, "between 1 and 64"},
		{"空白先于 dimensions", EmbedRequest{Texts: []string{" "}, Dimensions: bad}, "Empty input"},
		{"dimensions 先于字符上限", EmbedRequest{Texts: []string{tooLong}, Dimensions: bad}, "dimensions must be"},
		{"字符上限", EmbedRequest{Texts: []string{tooLong}}, "characters"},
	}
	for _, c := range cases {
		if c.name == "条数过多先于空白" {
			for i := range c.req.Texts {
				c.req.Texts[i] = "x"
			}
		}
		if _, err := e.Embed(context.Background(), c.req); !isRequestErr(err, c.want) {
			t.Errorf("%s：想要含 %q 的 400，实际 %v", c.name, c.want, err)
		}
	}
	if sess.callCount() != 0 || tok.single != 0 {
		t.Error("这些请求都该在分词之前被拦下")
	}
}

func TestPrefixApplied(t *testing.T) {
	spec := smallSpec()
	spec.QueryPrefix = "问：{task}｜"
	spec.DocumentPrefix = "答："
	spec.QueryTask = "默认任务"
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})

	eq := func(a []int64, b []int64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	last := func() []int64 { c := sess.snapshot(); return c[len(c)-1].inputs[0].Data }

	mustEmbed(t, e, EmbedRequest{Texts: []string{"正文"}, InputType: InputQuery})
	if !eq(last(), idsOf("问：默认任务｜正文", spec.MaxTokens)) {
		t.Error("查询侧该用默认任务句")
	}
	mustEmbed(t, e, EmbedRequest{Texts: []string{"正文"}, InputType: InputQuery, Task: "换任务"})
	if !eq(last(), idsOf("问：换任务｜正文", spec.MaxTokens)) {
		t.Error("请求里的 task 该覆盖默认任务句")
	}
	mustEmbed(t, e, texts("正文"))
	if !eq(last(), idsOf("答：正文", spec.MaxTokens)) {
		t.Error("不写 input_type 就是 document，用库存侧前缀")
	}
	// 缓存的键是加了前缀的文本：同一段正文，查询侧和库存侧是两个键
	n := sess.callCount()
	if res := mustEmbed(t, e, EmbedRequest{Texts: []string{"正文"}, InputType: InputQuery}); res.CacheHits != 1 || sess.callCount() != n {
		t.Error("同样的前缀和正文该命中缓存")
	}
	if res := mustEmbed(t, e, EmbedRequest{Texts: []string{"正文"}, InputType: InputDocument}); res.CacheHits != 1 {
		t.Error("document 那条上面已经放进缓存了")
	}
	if res := mustEmbed(t, e, EmbedRequest{Texts: []string{"正文"}, InputType: InputQuery, Task: "第三个任务"}); res.CacheHits != 0 {
		t.Error("任务句不同，前缀不同，不该命中")
	}
}

// ---- 单行 token 上限（--max-line-tokens）----

func TestMaxLineTokens(t *testing.T) {
	spec := smallSpec()
	han := func(n int) string { return strings.Repeat("汉", n) }

	t.Run("缺省等于登记值，比登记值大也按登记值", func(t *testing.T) {
		for _, v := range []int{0, 8192, 100000} {
			e, _ := newTestEmbedder(t, spec, newEmbedSession(spec.Dimension), EmbedderOptions{MaxLineTokens: v})
			if e.MaxLineTokens() != 8192 {
				t.Errorf("MaxLineTokens=%d：生效 %d，想要 8192", v, e.MaxLineTokens())
			}
		}
	})
	t.Run("截断更早，批 token 按截断后算", func(t *testing.T) {
		sess := newEmbedSession(spec.Dimension)
		e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{MaxLineTokens: 2048})
		res := mustEmbed(t, e, texts(han(5000)))
		if res.Tokens != 2048 {
			t.Fatalf("5000 个汉字：Tokens=%d，想要 2048（内容 2046 加两个特殊符号）", res.Tokens)
		}
		if n := sess.snapshot()[0].inputs[0].Shape[1]; n != 2048 {
			t.Fatalf("前向的长度 %d，想要 2048", n)
		}
		// 字符上限不随它变：31000 个汉字照收，32769 个照拒
		mustEmbed(t, e, texts(han(31000)))
		if _, err := e.Embed(context.Background(), texts(han(32769))); !isRequestErr(err, "characters") {
			t.Fatalf("字符上限仍按登记值：%v", err)
		}
		// 16 条 × 2048 = 32768 通过，17 条超限
		mk := func(n int) []string {
			out := make([]string, n)
			for i := range out {
				out[i] = fmt.Sprintf("%d", i) + han(5000)
			}
			return out
		}
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: mk(16)}); err != nil {
			t.Fatalf("16 × 2048 = 32768 刚好在限内：%v", err)
		}
		if _, err := e.Embed(context.Background(), EmbedRequest{Texts: mk(17)}); !isRequestErr(err, "32768") {
			t.Fatalf("17 × 2048 超限：%v", err)
		}
	})
}

// ---- GM3-13：并发（go test -race）----

func TestConcurrentEmbed(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	words := make([]string, 24)
	for i := range words {
		words[i] = fmt.Sprintf("并发文本%d", i)
	}
	d4, d6 := 4, 6
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for it := 0; it < 40; it++ {
				a, b := words[(g*5+it)%len(words)], words[(g*3+it*7)%len(words)]
				req := EmbedRequest{Texts: []string{a, b}}
				switch it % 3 {
				case 1:
					req.Dimensions = &d4
				case 2:
					req.Dimensions = &d6
				}
				res, err := e.Embed(context.Background(), req)
				if err != nil {
					errc <- err
					return
				}
				for k, text := range req.Texts {
					full := expectedVec(idsOf(text, spec.MaxTokens), spec.Dimension, true)
					want := full
					if req.Dimensions != nil {
						want = append([]float64(nil), full[:*req.Dimensions]...)
						var s float64
						for _, x := range want {
							s += x * x
						}
						for i := range want {
							want[i] /= math.Sqrt(s)
						}
					}
					if len(res.Vectors[k]) != len(want) {
						errc <- fmt.Errorf("长度 %d，想要 %d", len(res.Vectors[k]), len(want))
						return
					}
					for i := range want {
						if math.Abs(float64(res.Vectors[k][i])-want[i]) > 1e-6 {
							errc <- fmt.Errorf("%q 第 %d 维 %v，想要 %v", text, i, res.Vectors[k][i], want[i])
							return
						}
					}
				}
				if g == 0 && it%10 == 9 {
					e.ClearCache()
				}
			}
		}(g)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
}

// HTTP 层在取模型租约之前调 CheckEmbedRequest、CheckRerankRequest：坏请求不该为了回 400 去加载模型。
// 它们和 Embed、Rerank 开头的检查是同一个函数，所以对同一个坏请求给同一句话；只有一个登记项就够，不需要模型。
func TestCheckRequestsNeedNoModel(t *testing.T) {
	spec := smallSpec()
	tooLong := strings.Repeat("长", spec.CharLimit()+1)
	embed := []struct {
		name string
		req  EmbedRequest
		want string
	}{
		{"input_type 不认识", EmbedRequest{Texts: []string{"a"}, InputType: "passage"}, "input_type"},
		{"没有输入", EmbedRequest{}, "at least 1"},
		{"空白", EmbedRequest{Texts: []string{"a", " 　"}}, "Empty input"},
		{"dimensions 非法", EmbedRequest{Texts: []string{"a"}, Dimensions: ptr(99999)}, "dimensions must be"},
		{"超字符上限", EmbedRequest{Texts: []string{tooLong}}, "characters"},
	}
	for _, c := range embed {
		err := CheckEmbedRequest(spec, c.req)
		if !isRequestErr(err, c.want) {
			t.Errorf("嵌入 %s：想要含 %q 的 400，实际 %v", c.name, c.want, err)
		}
		e, _ := newTestEmbedder(t, spec, newEmbedSession(spec.Dimension), EmbedderOptions{})
		_, viaEmbed := e.Embed(context.Background(), c.req)
		if viaEmbed == nil || err == nil || viaEmbed.Error() != err.Error() {
			t.Errorf("嵌入 %s：Embed 和 CheckEmbedRequest 该给同一句话：%v / %v", c.name, viaEmbed, err)
		}
	}
	if err := CheckEmbedRequest(spec, EmbedRequest{Texts: []string{"一句正常的话"}, InputType: InputQuery, Dimensions: ptr(spec.Dimension)}); err != nil {
		t.Errorf("正常请求不该被拦：%v", err)
	}

	rspec := mustBuiltin(t, "bge-reranker-v2-m3")
	rerank := []struct {
		name string
		req  RerankRequest
		want string
	}{
		{"空查询", RerankRequest{Query: "\t", Documents: []string{"d"}}, "Empty query"},
		{"没有文档", RerankRequest{Query: "q"}, "at least 1 document"},
		{"空白文档", RerankRequest{Query: "q", Documents: []string{"d", " "}}, "Empty document"},
		{"查询超字符上限", RerankRequest{Query: strings.Repeat("长", rspec.CharLimit()+1), Documents: []string{"d"}}, "The query is"},
		{"文档超字符上限", RerankRequest{Query: "q", Documents: []string{strings.Repeat("长", rspec.CharLimit()+1)}}, "The document is"},
	}
	for _, c := range rerank {
		err := CheckRerankRequest(rspec, c.req)
		if !isRequestErr(err, c.want) {
			t.Errorf("重排 %s：想要含 %q 的 400，实际 %v", c.name, c.want, err)
		}
	}
	if err := CheckRerankRequest(rspec, RerankRequest{Query: "q", Documents: []string{"d"}}); err != nil {
		t.Errorf("正常请求不该被拦：%v", err)
	}
}
