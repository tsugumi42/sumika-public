package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	typeDoc   = "document"
	typeQuery = "query"
)

// client 是对一份服务（Go 版或 Python 版，形状一样）的最小 HTTP 客户端。比较要看状态码，所以不用 internal/embed 的客户端：
// 那个把非 2xx 都变成错误。非 2xx 的响应体一律丢掉、只留状态码（Python 的 FastAPI 422 会把请求里的值原样回显，规格 2.6）。
type client struct {
	base  string // http://127.0.0.1:端口
	token string
	hc    *http.Client
}

func newClient(base, token string) *client {
	return &client{base: base, token: token, hc: &http.Client{Timeout: 15 * time.Minute}}
}

// call 发一个请求。返回状态码和（2xx 时的）正文；网络错误返回 err，非 2xx 不是 err。
func (c *client) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil, nil
	}
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// embedResult 是一次 /v1/embeddings 的结果。
type embedResult struct {
	Status int
	Vecs   [][]float32
	Tokens int
	MS     float64
}

// embed 向量一律要 base64（小端 float32）：直接拿到 float32 的字节，绕开 "Python 的 float64 文本 vs Go 的 float32 最短表示" 的取整问题。
func (c *client) embed(ctx context.Context, model string, texts []string, inputType string) (embedResult, error) {
	t0 := time.Now()
	status, raw, err := c.call(ctx, http.MethodPost, "/v1/embeddings", map[string]any{
		"model": model, "input": texts, "encoding_format": "base64", "input_type": inputType,
	})
	res := embedResult{Status: status, MS: float64(time.Since(t0).Microseconds()) / 1000}
	if err != nil || status != http.StatusOK {
		return res, err
	}
	var out struct {
		Data []struct {
			Index     int    `json:"index"`
			Embedding string `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return res, fmt.Errorf("嵌入响应解析不了：%w", err)
	}
	if len(out.Data) != len(texts) {
		return res, fmt.Errorf("送了 %d 条，回了 %d 条", len(texts), len(out.Data))
	}
	res.Vecs = make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || res.Vecs[d.Index] != nil {
			return res, errors.New("嵌入响应的 index 不对")
		}
		v, err := decodeF32(d.Embedding)
		if err != nil {
			return res, err
		}
		res.Vecs[d.Index] = v
	}
	res.Tokens = out.Usage.PromptTokens
	return res, nil
}

func decodeF32(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) == 0 || len(b)%4 != 0 {
		return nil, errors.New("base64 向量解不开或长度不是 4 的倍数")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// rerankResult：scores 按文档下标排好（不是按名次）。
type rerankResult struct {
	Status int
	Scores []float64
	MS     float64
}

// rerank 要全部文档的分数（top_n = 文档数），好比排序。
func (c *client) rerank(ctx context.Context, model, query string, docs []string) (rerankResult, error) {
	t0 := time.Now()
	status, raw, err := c.call(ctx, http.MethodPost, "/v1/rerank", map[string]any{
		"model": model, "query": query, "documents": docs, "top_n": len(docs),
	})
	res := rerankResult{Status: status, MS: float64(time.Since(t0).Microseconds()) / 1000}
	if err != nil || status != http.StatusOK {
		return res, err
	}
	var out struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return res, fmt.Errorf("重排响应解析不了：%w", err)
	}
	if len(out.Results) != len(docs) {
		return res, fmt.Errorf("送了 %d 个文档，回了 %d 个", len(docs), len(out.Results))
	}
	res.Scores = make([]float64, len(docs))
	seen := make([]bool, len(docs))
	for _, r := range out.Results {
		if r.Index < 0 || r.Index >= len(docs) || seen[r.Index] {
			return res, errors.New("重排响应的 index 不对")
		}
		seen[r.Index] = true
		res.Scores[r.Index] = r.Score
	}
	return res, nil
}

// health 返回 /health 里我们要的几项（Python 版没有 rss_bytes）。
type healthInfo struct {
	RSSBytes uint64
	Loaded   []string
	Engine   string
}

func (c *client) health(ctx context.Context) (healthInfo, int, error) {
	status, raw, err := c.call(ctx, http.MethodGet, "/health", nil)
	if err != nil || status != http.StatusOK {
		return healthInfo{}, status, err
	}
	var h struct {
		RSSBytes uint64   `json:"rss_bytes"`
		Loaded   []string `json:"loaded"`
		Engine   string   `json:"engine"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return healthInfo{}, status, err
	}
	return healthInfo{RSSBytes: h.RSSBytes, Loaded: h.Loaded, Engine: h.Engine}, status, nil
}

// unload 卸载一个模型（同时清空它的缓存）。
func (c *client) unload(ctx context.Context, model string) (int, error) {
	status, _, err := c.call(ctx, http.MethodPost, "/v1/models/"+model+"/unload", map[string]any{})
	return status, err
}

// models 返回 /v1/models 里的 id 列表。
func (c *client) models(ctx context.Context) ([]string, error) {
	status, raw, err := c.call(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("/v1/models 回了 %d", status)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	ids := make([]string, len(out.Data))
	for i, d := range out.Data {
		ids[i] = d.ID
	}
	return ids, nil
}
