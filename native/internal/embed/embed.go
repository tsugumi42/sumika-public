// Package embed 把文本变成向量（规格-记忆-v2 第十节、13.1）。调用一律走 OpenAI 兼容的 /v1/embeddings：
// 本机的嵌入子进程（local.go）和服务商的嵌入 API 对记忆来说是同一个插口，只是地址和钥匙不同（规格-记忆 第五节"本地模型都要有 API 兜底"）。
// 起停、健康检查只有本机那份要管，放在 local.go，和调用分开。
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/llm"
)

// Embedder 是记忆、学说法要的"算向量"插口
type Embedder interface {
	// Embed 按顺序返回每句的向量（没归一化，见 Unit）；嵌入不可用时返回错误，调用方退回只用全文
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model 写进向量表：换了模型的旧向量认得出来（同样 1024 维也不能混用）
	Model() string
}

// ErrUnavailable 是"现在算不了"（本机的还没起来、挂了）：调用方当作没有向量，不当成出错
var ErrUnavailable = errors.New("嵌入现在不可用")

// Type 是本地模型服务的非标准字段 input_type（规格-本地模型服务 3.3、六.3）：这句是查询还是库存，带前缀的模型（Qwen 的 instruct）靠它决定加不加前缀。
// OpenAI 的形状里没有这个字段，所以只有对接本地模型服务时才带；普通的 Embed 不带它
type Type string

const (
	TypeQuery    Type = "query"    // 查询：问的那一句
	TypeDocument Type = "document" // 库存：存进去的条目（服务端不给的话默认就是它）
)

// Typed 是能说"这是查询 / 这是库存"的嵌入插口（可选实现）：只实现 Embedder 的，一律当库存、不带 input_type
type Typed interface {
	Embedder
	EmbedTyped(ctx context.Context, t Type, texts []string) ([][]float32, error)
}

// Connect 连一个已经在跑的 OpenAI 兼容嵌入服务（本地模型服务，规格-本地模型服务 六.3）：不起子进程。
// timeout 是一次嵌入请求最多等多久：本地服务一次前向只放一条，一批 64 条在慢模型上要几分钟（memory-eval 给 10 分钟）；
// 零值照 NewClient 的 30 秒。客户端超时走人时服务那边也会停（规格-本地模型服务 第五节），不会接着算完再排队。
// 令牌照服务那边的约定读环境变量 SUMIKA_EMBED_TOKEN（服务没设它就不肯起）；没设时带一个占位的，服务会回 401。
// baseURL 含 /v1（如 http://127.0.0.1:11440/v1），结尾的斜杠给不给都行
func Connect(baseURL, model string, timeout time.Duration) *Client {
	return newClient(llm.Source{Name: "本地模型服务", BaseURL: strings.TrimRight(baseURL, "/") + "/", APIKey: connectToken()}, model, timeout)
}

// connectToken 是 Connect 带的令牌：SUMIKA_EMBED_TOKEN，没设就是占位的 "local"
func connectToken() string {
	if t := os.Getenv("SUMIKA_EMBED_TOKEN"); t != "" {
		return t
	}
	return "local"
}

// Client 调一个 OpenAI 兼容的嵌入来源
type Client struct {
	src   llm.Source
	model string
	hc    *http.Client
}

// defaultTimeout 是一次嵌入请求的默认超时（照 daemon/plugins/embed.ts 的 30 秒）：本机桥一批算得快，30 秒够
const defaultTimeout = 30 * time.Second

// NewClient 建一个调用某个来源、某个嵌入模型的客户端。一次请求 30 秒超时（照 daemon/plugins/embed.ts）
func NewClient(src llm.Source, model string) *Client {
	return newClient(src, model, 0)
}

// newClient 是 NewClient / Connect 的共用部分：timeout 为零（或负）就用 defaultTimeout
func newClient(src llm.Source, model string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	hc := &http.Client{Timeout: timeout}
	return &Client{src: src, model: model, hc: hc}
}

func (c *Client) Model() string { return c.model }

func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, "", texts)
}

// EmbedTyped 照 Embed 算，另在请求体里带 input_type（本地模型服务按它决定加不加查询前缀）
func (c *Client) EmbedTyped(ctx context.Context, t Type, texts []string) ([][]float32, error) {
	if t != TypeQuery && t != TypeDocument {
		return nil, fmt.Errorf("input_type 只有 query / document 两种：%q", t)
	}
	return c.embed(ctx, t, texts)
}

// embed 是两条路的共用部分：t 为空就不带 input_type
func (c *Client) embed(ctx context.Context, t Type, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	p := embedRequest{Model: c.model, Input: texts, EncodingFormat: "float", InputType: string(t)}
	res, err := c.post(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("%s 的嵌入请求失败：%w", c.src.Name, err)
	}
	if len(res.Data) != len(texts) {
		return nil, fmt.Errorf("%s 回了 %d 个向量，要的是 %d 个", c.src.Name, len(res.Data), len(texts))
	}
	data := append([]embedding(nil), res.Data...)
	sort.Slice(data, func(i, j int) bool { return data[i].Index < data[j].Index })
	out := make([][]float32, len(data))
	for i, d := range data {
		v := make([]float32, len(d.Embedding))
		for k, x := range d.Embedding {
			v[k] = float32(x)
		}
		out[i] = v
	}
	return out, nil
}

// embedRequest 是 /embeddings 的请求体；InputType 为空就不带
type embedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
	InputType      string   `json:"input_type,omitempty"`
}

type embedding struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type embedResponse struct {
	Data []embedding `json:"data"`
}

// post 发一次嵌入请求（不重试，重试归调用方）。回了非 2xx 的，错误是 *llm.StatusError，调用方按状态码认
func (c *Client) post(ctx context.Context, p embedRequest) (embedResponse, error) {
	var out embedResponse
	body, err := json.Marshal(p)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.src.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.src.APIKey)
	req.Header.Set("Accept", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		msg := strings.TrimSpace(string(raw))
		var j struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &j) == nil && j.Error.Message != "" {
			msg = j.Error.Message
		}
		return out, &llm.StatusError{Code: res.StatusCode, Header: res.Header, Body: msg, RawBody: string(raw)}
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("回复读不懂：%w", err)
	}
	return out, nil
}

// Unit 返回归一化成单位长度的副本：存进去和查的时候都先归一化，余弦就等于点积（全零向量原样返回）
func Unit(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if s == 0 {
		copy(out, v)
		return out
	}
	n := float32(1 / math.Sqrt(s))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

// Dot 是两个等长向量的点积；单位向量的点积就是余弦相似度
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
