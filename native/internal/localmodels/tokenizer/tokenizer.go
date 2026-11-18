package tokenizer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sentencepiece "sumika-native/third_party/goSentencePiece"
)

// RerankSpecialReserve 是重排时留给特殊符号的 token 数：查询带上 <s> 和 </s> 以后不能超过 maxTokens 减去它
// （tools/local-models/server.py 的 RERANK_SPECIAL_RESERVE）。
const RerankSpecialReserve = 16

// Spec 是登记表里关于一份 tokenizer.json 的约束（规格 2.3、2.4）。
type Spec struct {
	// SHA256 是登记的 tokenizer.json 的哈希（小写十六进制）。必填：没有哈希不加载。
	SHA256 string
}

// Tokenizer 是加载好的分词器，只读，可以多个 goroutine 同时用。
type Tokenizer struct {
	sp       *sentencepiece.Tokenizer
	bos, eos int
}

// QueryTooLongError 是重排的查询放不进预算（带上两个特殊符号以后超过 maxTokens − RerankSpecialReserve）。
type QueryTooLongError struct{ Tokens, Budget int }

func (e *QueryTooLongError) Error() string {
	return fmt.Sprintf("查询有 %d 个 token（含 <s> 和 </s>），最多 %d 个", e.Tokens, e.Budget)
}

// Load 核对哈希，然后建出分词器。哈希不对一律拒绝加载。
// normalizer、pre_tokenizer 按文件里写的执行（fork 只认 gte 和 bge 用的那几种，别的报错）。
func Load(spec Spec, raw []byte) (tk *Tokenizer, err error) {
	defer func() {
		if r := recover(); r != nil {
			tk, err = nil, fmt.Errorf("建分词器时内部错误（%T）", r)
		}
	}()
	if spec.SHA256 == "" {
		return nil, errors.New("登记表没有写 tokenizer.json 的哈希，不加载")
	}
	if got := sha256Hex(raw); got != spec.SHA256 {
		return nil, fmt.Errorf("tokenizer.json 的 SHA-256 是 %s，登记的是 %s", got, spec.SHA256)
	}
	fields, err := topLevelFields(raw)
	if err != nil {
		return nil, err
	}
	bos, eos, err := specialIDs(raw, fields)
	if err != nil {
		return nil, err
	}
	sp, err := sentencepiece.NewTokenizerFromJSONReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("建分词器：%w", err)
	}
	return &Tokenizer{sp: sp, bos: bos, eos: eos}, nil
}

// specialIDs 从 post_processor 的模板里读 <s> 和 </s> 的 id，并要求模板正是
// 单条 [<s> A </s>]、一对 [<s> A </s> </s> B </s>]（gte 和 bge 都是；不是就报错，不去猜）。
func specialIDs(data []byte, fields map[string][2]int) (bos, eos int, err error) {
	rawPP, err := field(data, fields, "post_processor")
	if err != nil {
		return 0, 0, err
	}
	var pp struct {
		Type          string                       `json:"type"`
		Single        []map[string]json.RawMessage `json:"single"`
		Pair          []map[string]json.RawMessage `json:"pair"`
		SpecialTokens map[string]struct {
			IDs []int `json:"ids"`
		} `json:"special_tokens"`
	}
	if err := json.Unmarshal(rawPP, &pp); err != nil || pp.Type != "TemplateProcessing" {
		return 0, 0, fmt.Errorf("post_processor 不是 TemplateProcessing：%v", err)
	}
	id := func(name string) (int, error) {
		t, ok := pp.SpecialTokens[name]
		if !ok || len(t.IDs) != 1 {
			return 0, fmt.Errorf("post_processor 的 special_tokens 里没有 %s，或不是恰好一个 id", name)
		}
		return t.IDs[0], nil
	}
	if bos, err = id("<s>"); err != nil {
		return 0, 0, err
	}
	if eos, err = id("</s>"); err != nil {
		return 0, 0, err
	}
	// 模板每一项是 {"SpecialToken": {"id": …}} 或 {"Sequence": {"id": "A"/"B"}}，拼成 "<s>" "A" "</s>" 这样的串来比
	shape := func(tmpl []map[string]json.RawMessage) string {
		var parts []string
		for _, e := range tmpl {
			for kind, v := range e {
				var x struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(v, &x)
				switch kind {
				case "SpecialToken":
					parts = append(parts, x.ID)
				case "Sequence":
					parts = append(parts, "$"+x.ID)
				default:
					parts = append(parts, "?"+kind)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	if got := shape(pp.Single); got != "<s> $A </s>" {
		return 0, 0, fmt.Errorf("post_processor 的单条模板是 %q，该是 %q", got, "<s> $A </s>")
	}
	if got := shape(pp.Pair); got != "<s> $A </s> </s> $B </s>" {
		return 0, 0, fmt.Errorf("post_processor 的一对模板是 %q，该是 %q", got, "<s> $A </s> </s> $B </s>")
	}
	return bos, eos, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// content 取内容 id（不带特殊符号、不截断）。库对没见过的字符会不会 panic 没测过，所以所有入口都 recover 成错误；
// 错误里只写 panic 的类型，不写值（值可能是输入文字）。
func (t *Tokenizer) content(text string) (ids []int, err error) {
	defer func() {
		if r := recover(); r != nil {
			ids, err = nil, fmt.Errorf("分词器内部错误（%T）", r)
		}
	}()
	return t.sp.Encode(text), nil
}

// Content 是 <s>、</s> 之间的部分：不带特殊符号、不截断。
func (t *Tokenizer) Content(text string) ([]int, error) { return t.content(text) }

// EncodeSingle 返回 [<s>] + 内容 + [</s>]，内容最多留 maxTokens−2 个（HF 的 longest_first：先截内容再补特殊符号，
// 不像库自己的截断那样会把结尾的 </s> 切掉）。空文本得到 [<s>, </s>]。
func (t *Tokenizer) EncodeSingle(text string, maxTokens int) ([]int, error) {
	if maxTokens < 2 {
		return nil, fmt.Errorf("maxTokens 是 %d，放不下 <s> 和 </s>", maxTokens)
	}
	c, err := t.content(text)
	if err != nil {
		return nil, err
	}
	c = c[:min(len(c), maxTokens-2)]
	out := make([]int, 0, len(c)+2)
	out = append(out, t.bos)
	out = append(out, c...)
	return append(out, t.eos), nil
}

// EncodePair 返回重排用的一对：[<s>] 查询 [</s> </s>] 文档 [</s>]。总长不超过 maxTokens，只截文档
// （HF 的 only_second），查询不截：查询带上两个特殊符号超过 maxTokens−RerankSpecialReserve 就报 *QueryTooLongError。
func (t *Tokenizer) EncodePair(query, document string, maxTokens int) ([]int, error) {
	q, err := t.content(query)
	if err != nil {
		return nil, err
	}
	budget := maxTokens - RerankSpecialReserve
	if len(q)+2 > budget {
		return nil, &QueryTooLongError{Tokens: len(q) + 2, Budget: budget}
	}
	d, err := t.content(document)
	if err != nil {
		return nil, err
	}
	d = d[:min(len(d), maxTokens-4-len(q))]
	out := make([]int, 0, len(q)+len(d)+4)
	out = append(out, t.bos)
	out = append(out, q...)
	out = append(out, t.eos, t.eos)
	out = append(out, d...)
	return append(out, t.eos), nil
}

// Count 是文本带上两个特殊符号、不截断的 token 数（server.py 里算查询预算用的）。
func (t *Tokenizer) Count(text string) (int, error) {
	c, err := t.content(text)
	if err != nil {
		return 0, err
	}
	return len(c) + 2, nil
}
