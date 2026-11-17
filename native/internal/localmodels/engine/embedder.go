package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/pyspace"
)

// EmbedderOptions 是嵌入器的选项。
type EmbedderOptions struct {
	// MaxLineTokens 是单行 token 的上限（服务参数 --max-line-tokens）：0 = 登记表的 max_tokens。
	// 只能收紧、不能放宽（比登记值大就按登记值）；超过的截断，和 Python 同一种语义，只是更早（规格 2.5"单行最大长度"、差异 S2）。
	// 字符数上限不随它变，仍按登记表的 max_tokens × 4：单行本身打断不了（Run 没有取消），所以长行要在这里限住。
	MaxLineTokens int
}

// EmbedRequest 是一次嵌入请求。Dimensions 用指针：没传（nil）和传了 0 不是一回事，0 要报错（和 Python 的 None 一样）。
type EmbedRequest struct {
	Texts      []string
	InputType  InputType // 零值 = document
	Task       string    // 查询前缀里 {task} 的内容，空 = 登记表的 query_task
	Dimensions *int
}

// EmbedResult 里的向量都是调用方自己的（和缓存互不相干）。Tokens 含特殊符号和缓存命中的那几条。
type EmbedResult struct {
	Vectors   [][]float32
	Tokens    int
	CacheHits int
}

// Embedder 把文本变成向量：加前缀 → 判空 → 字符上限 → 缓存 → 分词 → 一次一行前向 → 取第 0 行（cls）→ 查坏值 → L2 → 缓存 → 截断 dimensions。
// 顺序和 server.py 一样：同一个坏输入，两边报同一类错。可以多个 goroutine 同时用；会话和分词器的关闭归创建者。
type Embedder struct {
	spec    Spec
	tok     Tokenizer
	run     *runner
	cache   *lru
	maxLine int
}

// NewEmbedder 核对登记项和会话：模型要是嵌入用的，图的输入输出认得，要有 last_hidden_state 或 token_embeddings。
// 池化取 token_embeddings 的第 0 行再 L2，不读 sentence_embedding：它和"CLS + L2"余弦是 1.000000 但不逐位相同，
// 对拍的参照是 server.py（规格 2.5"用哪个输出"）。
func NewEmbedder(spec Spec, tok Tokenizer, sess ort.Session, o EmbedderOptions) (*Embedder, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Kind != KindEmbed {
		return nil, fmt.Errorf("%s 不是嵌入模型（kind=%s）", spec.ID, spec.Kind)
	}
	if tok == nil {
		return nil, fmt.Errorf("%s: 没有分词器", spec.ID)
	}
	maxLine := spec.MaxTokens
	switch {
	case o.MaxLineTokens < 0:
		return nil, fmt.Errorf("%s: 单行 token 上限不能是负数（%d）", spec.ID, o.MaxLineTokens)
	case o.MaxLineTokens > 0 && o.MaxLineTokens < MinMaxTokens:
		return nil, fmt.Errorf("%s: 单行 token 上限至少 %d（现在是 %d）", spec.ID, MinMaxTokens, o.MaxLineTokens)
	case o.MaxLineTokens > 0 && o.MaxLineTokens < maxLine:
		maxLine = o.MaxLineTokens
	}
	r, err := newRunner(spec.ID, sess, "last_hidden_state", "token_embeddings")
	if err != nil {
		return nil, err
	}
	return &Embedder{spec: spec, tok: tok, run: r, cache: newLRU(CacheSize), maxLine: maxLine}, nil
}

// Spec 是这个嵌入器的登记项。
func (e *Embedder) Spec() Spec { return e.spec }

// MaxLineTokens 是实际生效的单行 token 上限。
func (e *Embedder) MaxLineTokens() int { return e.maxLine }

// ClearCache 清空缓存（模型卸载时）。
func (e *Embedder) ClearCache() { e.cache.clear() }

// CheckEmbedRequest 做请求本身的检查：input_type、条数、空白、dimensions、字符上限（含前缀），都在分词之前，不碰模型。
// Embed 开头调它；HTTP 层在取模型租约之前也调它（server.py 先验再取租约：坏请求不该为了回 400 去加载模型，
// 也不该在池满的时候回 503 而不是 400）。两处是同一个函数，门槛不会各改各的。错误都是 *RequestError。
func CheckEmbedRequest(spec Spec, req EmbedRequest) error {
	switch req.InputType {
	case "", InputDocument, InputQuery:
	default:
		return requestErrorf("input_type must be 'query' or 'document'")
	}
	n := len(req.Texts)
	if n == 0 {
		return requestErrorf("Provide at least 1 input string")
	}
	if n > MaxBatchSize {
		return requestErrorf("Provide between 1 and %d input strings", MaxBatchSize)
	}
	for _, t := range req.Texts {
		if pyspace.IsBlank(t) { // 只按 Python 的空白集合判，不按"内容 id 是否为空"判
			return requestErrorf("Empty input strings are not supported")
		}
	}
	if req.Dimensions != nil {
		if reason := spec.DimensionsError(*req.Dimensions); reason != "" {
			return &RequestError{Detail: reason}
		}
	}
	prefix := spec.Prefix(req.InputType, req.Task)
	for _, t := range req.Texts {
		if rc := utf8.RuneCountInString(prefix + t); rc > spec.CharLimit() { // 按字符（rune）数，不是字节数
			return requestErrorf("An input is %d characters; '%s' allows at most %d (%d tokens × %d), prefix included",
				rc, spec.ID, spec.CharLimit(), spec.MaxTokens, CharsPerToken)
		}
	}
	return nil
}

// Embed 算一批文本的向量。返回的错误分三类，见 RequestError 的说明。
// 批中途 ctx 取消：已经算完的几行不进缓存（和 Python 一致，下次重算），只停在行边界。
func (e *Embedder) Embed(ctx context.Context, req EmbedRequest) (EmbedResult, error) {
	spec := e.spec
	if err := CheckEmbedRequest(spec, req); err != nil {
		return EmbedResult{}, err
	}
	n := len(req.Texts)
	prefix := spec.Prefix(req.InputType, req.Task)
	prepared := make([]string, n)
	for i, t := range req.Texts {
		prepared[i] = prefix + t
	}
	if err := ctx.Err(); err != nil {
		return EmbedResult{}, err
	}

	vectors := make([][]float32, n)
	counts := make([]int, n)
	var missing []int
	for i, p := range prepared {
		if vec, tokens, ok := e.cache.get(p); ok {
			vectors[i], counts[i] = vec, tokens
			continue
		}
		missing = append(missing, i)
	}
	rows := make([][]int, len(missing))
	for k, i := range missing {
		ids, err := e.tok.EncodeSingle(prepared[i], e.maxLine)
		if err != nil {
			return EmbedResult{}, fmt.Errorf("%s: 分词：%w", spec.ID, err)
		}
		if len(ids) == 0 {
			return EmbedResult{}, fmt.Errorf("%s: 分词器给出了空的 id 序列", spec.ID)
		}
		rows[k], counts[i] = ids, len(ids)
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	// 全命中缓存也要查：一批最多多少 token 没有缓存命中的豁免
	if total > MaxBatchTokens {
		return EmbedResult{}, requestErrorf("A batch is limited to %d tokens", MaxBatchTokens)
	}

	computed := make([][]float32, len(missing))
	for k, ids := range rows {
		vec, err := e.forward(ctx, ids)
		if err != nil {
			return EmbedResult{}, err
		}
		computed[k] = vec
	}
	for k, i := range missing { // 整批算完才进缓存
		vectors[i] = computed[k]
		e.cache.put(prepared[i], computed[k], counts[i])
	}

	if req.Dimensions != nil && *req.Dimensions != spec.Dimension {
		for i := range vectors {
			v, err := truncateAndNormalize(vectors[i], *req.Dimensions)
			if err != nil {
				return EmbedResult{}, err
			}
			vectors[i] = v
		}
	}
	return EmbedResult{Vectors: vectors, Tokens: total, CacheHits: n - len(missing)}, nil
}

// forward 跑一行（形状 [1, n]），取 token_embeddings 的第 0 行，查坏值，L2。
func (e *Embedder) forward(ctx context.Context, ids []int) ([]float32, error) {
	id, n, dim := e.spec.ID, len(ids), e.spec.Dimension
	t, err := e.run.run(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%s: 前向：%w", id, err)
	}
	if len(t.Shape) != 3 || t.Shape[0] != 1 || t.Shape[1] != int64(n) || t.Shape[2] != int64(dim) || len(t.Data) != n*dim {
		return nil, fmt.Errorf("%s: 输出 %s 的形状 %v（%d 个值）和预期 [1 %d %d] 不一致", id, e.run.output, t.Shape, len(t.Data), n, dim)
	}
	vec := append([]float32(nil), t.Data[:dim]...) // 第 0 行（cls），拷出来，不持有 ORT 给的大张量
	if err := normalizeInPlace(vec, e.spec.Normalize); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return vec, nil
}

// l2Norm 是向量的 L2 范数，用 float64 累加再转回 float32；有 NaN / Inf 就报错。
func l2Norm(vec []float32) (float32, error) {
	var sum float64
	for _, x := range vec {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, errors.New("模型吐出了 NaN / Inf")
		}
		sum += f * f
	}
	norm := float32(math.Sqrt(sum))
	if math.IsInf(float64(norm), 0) {
		return 0, errors.New("模型吐出的向量范数溢出")
	}
	return norm, nil
}

// normalizeInPlace 查坏值（NaN / Inf / 零向量）；normalize 为真就除以 float32 的范数。坏值一律是错误，不凑一个向量。
func normalizeInPlace(vec []float32, normalize bool) error {
	norm, err := l2Norm(vec)
	if err != nil {
		return err
	}
	if !(norm > 0) {
		return errors.New("模型吐出了零向量")
	}
	if normalize {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return nil
}

// truncateAndNormalize 在拷贝上截到前 d 维再重新归一化（matryoshka）；截完是零向量是 400 类。
func truncateAndNormalize(vec []float32, d int) ([]float32, error) {
	if d < 1 || d > len(vec) {
		return nil, fmt.Errorf("dimensions %d 超出向量的 %d 维", d, len(vec))
	}
	out := append([]float32(nil), vec[:d]...)
	norm, err := l2Norm(out)
	if err != nil {
		return nil, err
	}
	if !(norm > 0) {
		return nil, requestErrorf("The requested dimensions produce a zero vector")
	}
	for i := range out {
		out[i] /= norm
	}
	return out, nil
}
