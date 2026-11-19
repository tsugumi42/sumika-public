package engine

import (
	"math"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/tokenizer"
)

// 这个包的测试默认不需要真 dll、真模型：假分词器加假会话。真的在 real_test.go，用环境变量门着。

// ---- 假分词器 ----

// fakeTok 每个字符一个 token（id = 10 + 码点 % 5000），U+200B 不出 token（像真分词器一样：内容 id 为空，
// 但文本不是空白）。截断的写法和 tokenizer 包一样：内容留到 max−2，一对只截文档。
type fakeTok struct {
	mu     sync.Mutex
	single int // EncodeSingle 调用次数
	pair   int
	count  int
}

func fakeContent(text string) []int {
	var ids []int
	for _, r := range text {
		if r == 0x200b {
			continue
		}
		ids = append(ids, 10+int(r%5000))
	}
	return ids
}

func (f *fakeTok) EncodeSingle(text string, maxTokens int) ([]int, error) {
	f.mu.Lock()
	f.single++
	f.mu.Unlock()
	c := fakeContent(text)
	c = c[:min(len(c), maxTokens-2)]
	return append(append([]int{0}, c...), 2), nil
}

func (f *fakeTok) EncodePair(query, document string, maxTokens int) ([]int, error) {
	f.mu.Lock()
	f.pair++
	f.mu.Unlock()
	q := fakeContent(query)
	budget := maxTokens - tokenizer.RerankSpecialReserve
	if len(q)+2 > budget {
		return nil, &tokenizer.QueryTooLongError{Tokens: len(q) + 2, Budget: budget}
	}
	d := fakeContent(document)
	d = d[:min(len(d), maxTokens-4-len(q))]
	out := append([]int{0}, q...)
	out = append(out, 2, 2)
	out = append(out, d...)
	return append(out, 2), nil
}

func (f *fakeTok) Count(text string) (int, error) {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	return len(fakeContent(text)) + 2, nil
}

// ---- 假会话 ----

type fakeCall struct {
	inputs  []ort.Int64Tensor
	outputs []string
}

// fakeSession 记录每次 Run 的输入，按名字返回输出。row 决定输出的内容，缺省见 defaultRow。
type fakeSession struct {
	dim     int
	inputs  []ort.TensorInfo
	outputs []ort.TensorInfo
	delay   time.Duration

	// row 返回名为 name 的输出；ids 是这一行的 input_ids。缺省：嵌入图给 [1,n,dim] 的隐藏状态，重排图给 [1,1] 的 logit。
	row func(n int, ids []int64, name string) ort.FloatTensor
	// onRun 在第 n 次（从 1 数）Run 的开头调用；返回的错误当作这次 Run 的错误
	onRun func(n int) error

	mu    sync.Mutex
	calls []fakeCall
}

var i64 = ort.TypeInt64

func newEmbedSession(dim int, outputs ...string) *fakeSession {
	if len(outputs) == 0 {
		outputs = []string{"token_embeddings", "sentence_embedding"}
	}
	s := &fakeSession{
		dim:    dim,
		inputs: []ort.TensorInfo{{Name: "input_ids", Type: i64, Shape: []int64{-1, -1}}, {Name: "attention_mask", Type: i64, Shape: []int64{-1, -1}}},
	}
	for _, o := range outputs {
		s.outputs = append(s.outputs, ort.TensorInfo{Name: o, Type: ort.TypeFloat32})
	}
	return s
}

func newRerankSession() *fakeSession {
	return &fakeSession{
		inputs:  []ort.TensorInfo{{Name: "input_ids", Type: i64, Shape: []int64{-1, -1}}, {Name: "attention_mask", Type: i64, Shape: []int64{-1, -1}}},
		outputs: []ort.TensorInfo{{Name: "logits", Type: ort.TypeFloat32}},
	}
}

func (s *fakeSession) Inputs() []ort.TensorInfo  { return s.inputs }
func (s *fakeSession) Outputs() []ort.TensorInfo { return s.outputs }
func (s *fakeSession) Close() error              { return nil }

func (s *fakeSession) Run(inputs []ort.Int64Tensor, outputs []string) (map[string]ort.FloatTensor, error) {
	cp := make([]ort.Int64Tensor, len(inputs))
	for i, in := range inputs {
		cp[i] = ort.Int64Tensor{Name: in.Name, Shape: append([]int64(nil), in.Shape...), Data: append([]int64(nil), in.Data...)}
	}
	s.mu.Lock()
	s.calls = append(s.calls, fakeCall{inputs: cp, outputs: append([]string(nil), outputs...)})
	n := len(s.calls)
	s.mu.Unlock()
	if s.onRun != nil {
		if err := s.onRun(n); err != nil {
			return nil, err
		}
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	var ids []int64
	for _, in := range inputs {
		if in.Name == "input_ids" {
			ids = in.Data
		}
	}
	res := map[string]ort.FloatTensor{}
	for _, name := range outputs {
		if s.row != nil {
			res[name] = s.row(len(ids), ids, name)
		} else {
			res[name] = s.defaultRow(len(ids), ids, name)
		}
	}
	return res, nil
}

// defaultRow：隐藏状态的第 0 行是 ids 的确定函数（全正、不为零），别的行是别的值；sentence_embedding 故意和第 0 行不同。
func (s *fakeSession) defaultRow(n int, ids []int64, name string) ort.FloatTensor {
	switch name {
	case "logits":
		return ort.FloatTensor{Shape: []int64{1, 1}, Data: []float32{fakeLogit(ids)}}
	case "sentence_embedding":
		data := make([]float32, s.dim)
		for j := range data {
			data[j] = 9
		}
		return ort.FloatTensor{Shape: []int64{1, int64(s.dim)}, Data: data}
	default: // token_embeddings、last_hidden_state
		data := make([]float32, n*s.dim)
		for r := 0; r < n; r++ {
			for j := 0; j < s.dim; j++ {
				data[r*s.dim+j] = fakeCell(ids, r, j)
			}
		}
		return ort.FloatTensor{Shape: []int64{1, int64(n), int64(s.dim)}, Data: data}
	}
}

func fakeCell(ids []int64, r, j int) float32 {
	var sum int64
	for _, id := range ids {
		sum += id
	}
	if r == 0 {
		return float32(1+(sum+int64(j)*3)%11) * 0.25
	}
	return float32(100 + r + j)
}

// fakeLogit 从最后一个内容 token 和长度算出一个确定的 logit。
func fakeLogit(ids []int64) float32 {
	return float32(ids[len(ids)-2]%13)/2 - 3 + float32(len(ids))*0.01
}

// expectedVec 是 fakeCell 的第 0 行按 float64 归一化的参照（和引擎里的 float32 实现各写一遍，互不依赖）。
func expectedVec(ids []int64, dim int, normalize bool) []float64 {
	v := make([]float64, dim)
	var sum float64
	for j := range v {
		v[j] = float64(fakeCell(ids, 0, j))
		sum += v[j] * v[j]
	}
	if normalize {
		for j := range v {
			v[j] /= math.Sqrt(sum)
		}
	}
	return v
}

func (s *fakeSession) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *fakeSession) snapshot() []fakeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeCall(nil), s.calls...)
}

// ---- 登记项 ----

func newTestEmbedder(t testing.TB, spec Spec, sess *fakeSession, o EmbedderOptions) (*Embedder, *fakeTok) {
	t.Helper()
	tok := &fakeTok{}
	e, err := NewEmbedder(spec, tok, sess, o)
	if err != nil {
		t.Fatal(err)
	}
	return e, tok
}

func newTestReranker(t testing.TB, sess *fakeSession) (*Reranker, *fakeTok) {
	t.Helper()
	tok := &fakeTok{}
	r, err := NewReranker(mustBuiltin(t, "bge-reranker-v2-m3"), tok, sess)
	if err != nil {
		t.Fatal(err)
	}
	return r, tok
}
