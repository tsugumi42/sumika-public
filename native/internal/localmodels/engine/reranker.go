package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"

	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/tokenizer"
	"sumika-native/internal/pyspace"
)

// RerankRequest 是一次重排请求。TopN 用指针：没传（nil）是全要，传了 0 或负数夹成 1。
type RerankRequest struct {
	Query     string
	Documents []string
	TopN      *int
}

// RerankResult 是一个文档的名次：Index 是它在请求里的下标，Score 是 sigmoid 之后的 0～1 分数（越大越相关）。
// Logit 是 sigmoid 之前的值（float32 的输出转成 float64），给进程内的对拍用，HTTP 接口不返回它。
type RerankResult struct {
	Index int
	Score float64
	Logit float64
}

// RerankResponse 的 Results 按分数从高到低，同分按下标从小到大，已经按 TopN 截过。Tokens 是所有对的 token 总数（含特殊符号）。
type RerankResponse struct {
	Results []RerankResult
	Tokens  int
}

// Reranker 给（查询，文档）打分：先量查询，编一对，一对一行前向取 logit，sigmoid（float64）。
type Reranker struct {
	spec Spec
	tok  Tokenizer
	run  *runner
}

// NewReranker 核对登记项和会话：模型要是重排用的，图要有 logits 输出。
func NewReranker(spec Spec, tok Tokenizer, sess ort.Session) (*Reranker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Kind != KindRerank {
		return nil, fmt.Errorf("%s 不是重排模型（kind=%s）", spec.ID, spec.Kind)
	}
	if tok == nil {
		return nil, fmt.Errorf("%s: 没有分词器", spec.ID)
	}
	r, err := newRunner(spec.ID, sess, "logits")
	if err != nil {
		return nil, err
	}
	return &Reranker{spec: spec, tok: tok, run: r}, nil
}

// Spec 是这个重排器的登记项。
func (r *Reranker) Spec() Spec { return r.spec }

// CheckRerankRequest 做请求本身的检查：查询和文档的空白、条数、字符上限，都在分词之前，不碰模型。
// Rerank 开头调它；HTTP 层在取模型租约之前也调它（理由同 CheckEmbedRequest）。错误都是 *RequestError。
// 查询的 token 预算要分词才知道，留在 Rerank 里。
func CheckRerankRequest(spec Spec, req RerankRequest) error {
	if pyspace.IsBlank(req.Query) {
		return requestErrorf("Empty query strings are not supported")
	}
	if len(req.Documents) == 0 {
		return requestErrorf("Provide at least 1 document")
	}
	if len(req.Documents) > MaxBatchSize {
		return requestErrorf("Provide between 1 and %d documents", MaxBatchSize)
	}
	for _, d := range req.Documents {
		if pyspace.IsBlank(d) {
			return requestErrorf("Empty document strings are not supported")
		}
	}
	if rc := utf8.RuneCountInString(req.Query); rc > spec.CharLimit() {
		return requestErrorf("The query is %d characters; '%s' allows at most %d (%d tokens × %d)",
			rc, spec.ID, spec.CharLimit(), spec.MaxTokens, CharsPerToken)
	}
	for _, d := range req.Documents {
		if rc := utf8.RuneCountInString(d); rc > spec.CharLimit() {
			return requestErrorf("The document is %d characters; '%s' allows at most %d (%d tokens × %d)",
				rc, spec.ID, spec.CharLimit(), spec.MaxTokens, CharsPerToken)
		}
	}
	return nil
}

// Rerank 给每个文档打分。错误分三类，见 RequestError 的说明；批中途 ctx 取消只停在行边界。
func (r *Reranker) Rerank(ctx context.Context, req RerankRequest) (RerankResponse, error) {
	spec := r.spec
	if err := CheckRerankRequest(spec, req); err != nil {
		return RerankResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return RerankResponse{}, err
	}

	// 查询的预算要把两个特殊符号算进去：Count 是内容加 <s>、</s>，超过 max_tokens − 16 就 400（差 2 就是边界错位）
	qTokens, err := r.tok.Count(req.Query)
	if err != nil {
		return RerankResponse{}, fmt.Errorf("%s: 分词：%w", spec.ID, err)
	}
	budget := spec.MaxTokens - tokenizer.RerankSpecialReserve
	if qTokens > budget {
		return RerankResponse{}, requestErrorf("Query is %d tokens; '%s' allows at most %d (max_tokens %d minus %d for the pair)",
			qTokens, spec.ID, budget, spec.MaxTokens, tokenizer.RerankSpecialReserve)
	}
	rows := make([][]int, len(req.Documents))
	total := 0
	for i, d := range req.Documents {
		ids, err := r.tok.EncodePair(req.Query, d, spec.MaxTokens)
		var tooLong *tokenizer.QueryTooLongError
		if errors.As(err, &tooLong) {
			return RerankResponse{}, requestErrorf("Query is %d tokens; '%s' allows at most %d", tooLong.Tokens, spec.ID, tooLong.Budget)
		}
		if err != nil {
			return RerankResponse{}, fmt.Errorf("%s: 分词：%w", spec.ID, err)
		}
		if len(ids) == 0 {
			return RerankResponse{}, requestErrorf("Every (query, document) pair must tokenize to at least one token")
		}
		if len(ids) > spec.MaxTokens {
			return RerankResponse{}, requestErrorf("A pair is %d tokens after truncation; the limit is %d", len(ids), spec.MaxTokens)
		}
		rows[i] = ids
		total += len(ids)
	}
	if total > MaxBatchTokens {
		return RerankResponse{}, requestErrorf("A batch is limited to %d tokens", MaxBatchTokens)
	}

	scores := make([]float64, len(rows))
	logits := make([]float64, len(rows))
	for i, ids := range rows {
		t, err := r.run.run(ctx, ids)
		if err != nil {
			return RerankResponse{}, fmt.Errorf("%s: 前向：%w", spec.ID, err)
		}
		if len(t.Shape) != 2 || t.Shape[0] != 1 || t.Shape[1] != 1 || len(t.Data) != 1 {
			return RerankResponse{}, fmt.Errorf("%s: 输出 logits 的形状 %v（%d 个值）和预期 [1 1] 不一致", spec.ID, t.Shape, len(t.Data))
		}
		logit := float64(t.Data[0])
		if math.IsNaN(logit) || math.IsInf(logit, 0) {
			return RerankResponse{}, fmt.Errorf("%s: 重排输出含 NaN / Inf", spec.ID)
		}
		logits[i] = logit
		scores[i] = 1 / (1 + math.Exp(-logit)) // float64 的 sigmoid：越大越相关
	}

	order := make([]RerankResult, len(scores))
	for i, s := range scores {
		order[i] = RerankResult{Index: i, Score: s, Logit: logits[i]}
	}
	sort.Slice(order, func(a, b int) bool {
		if order[a].Score != order[b].Score {
			return order[a].Score > order[b].Score
		}
		return order[a].Index < order[b].Index // 同分按下标
	})
	limit := len(order)
	if req.TopN != nil {
		limit = max(1, min(*req.TopN, len(order)))
	}
	return RerankResponse{Results: order[:limit], Tokens: total}, nil
}
