package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/llm"
)

// RerankClient 调一个 OpenAI 兼容来源的 /v1/rerank（Cohere / Jina 的形状，规格-本地模型服务 3.4）：
// 一句查询加一组文档，回「哪条排第几、分数多少」。和 Embedder 分开：它吃的是一对文本、吐的是分数，
// 失败时调用方退回原来的顺序（规格-记忆-v2 第七节，2026-10-28 加）。
// 起停、健康检查归调用方；这里只有一个请求
type RerankClient struct {
	src   llm.Source
	model string
	hc    *http.Client
}

// RerankConnect 连一个已经在跑的本地模型服务做重排（规格-本地模型服务 3.4、六.5）：不起子进程。
// 令牌和 Connect 读同一个环境变量 SUMIKA_EMBED_TOKEN（没设时带占位的 "local"，服务会回 401）；
// timeout 是一次重排请求最多等多久，零值照 defaultTimeout（30 秒）。
// baseURL 含 /v1（如 http://127.0.0.1:11440/v1），结尾的斜杠给不给都行
func RerankConnect(baseURL, model string, timeout time.Duration) *RerankClient {
	return NewRerankClient(llm.Source{Name: "本地模型服务", BaseURL: strings.TrimRight(baseURL, "/") + "/", APIKey: connectToken()}, model, timeout)
}

// NewRerankClient 建一个调用某个来源、某个重排模型的客户端；timeout 为零（或负）就用 defaultTimeout
func NewRerankClient(src llm.Source, model string, timeout time.Duration) *RerankClient {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &RerankClient{src: src, model: model, hc: &http.Client{Timeout: timeout}}
}

func (c *RerankClient) Model() string { return c.model }

// Rerank 把一组文档按和查询的相关度排一遍：order 是 docs 的下标（0 起），scores 和它一一对应、越大越相关。
// 服务保证按分数从大到小、同分按下标从小到大（规格-本地模型服务 3.4）；这里再稳一次，不信任回来的顺序。
// 0 条文档直接返回空；回的 index 越界或重复、条数对不上都算错（调用方退回原来的顺序）
func (c *RerankClient) Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	if len(docs) == 0 {
		return nil, nil, nil
	}
	reqBody, err := json.Marshal(map[string]any{"model": c.model, "query": query, "documents": docs})
	if err != nil {
		return nil, nil, fmt.Errorf("%s 的重排请求拼不出来：%w", c.src.Name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.src.BaseURL, "/")+"/rerank", bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("%s 的重排请求建不起来：%w", c.src.Name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.src.APIKey)
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s 的重排请求失败：%w", c.src.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, nil, fmt.Errorf("%s 的重排回了 %d：%s", c.src.Name, res.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, nil, fmt.Errorf("%s 的重排结果读不出来：%w", c.src.Name, err)
	}
	if len(out.Results) != len(docs) {
		return nil, nil, fmt.Errorf("%s 的重排回了 %d 条，给的是 %d 条", c.src.Name, len(out.Results), len(docs))
	}
	type item struct {
		index int
		score float64
	}
	items := make([]item, len(out.Results))
	seen := make([]bool, len(docs))
	for i, r := range out.Results {
		if r.Index < 0 || r.Index >= len(docs) || seen[r.Index] {
			return nil, nil, fmt.Errorf("%s 的重排回的 index 不对（%d，共 %d 条）", c.src.Name, r.Index, len(docs))
		}
		seen[r.Index] = true
		items[i] = item{r.Index, r.RelevanceScore}
	}
	// 与服务端同一个规矩：分数从大到小，同分按 index 从小到大（稳定）
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].index < items[j].index
	})
	order := make([]int, len(items))
	scores := make([]float64, len(items))
	for i, it := range items {
		order[i], scores[i] = it.index, it.score
	}
	return order, scores, nil
}
