// Package modelmeta 管模型的窗口和单次输出上限从哪来（ADR-334）：
// 用户覆盖 > 服务商或网关返回 > models.dev 本地缓存（只补空）> 保守默认并标"未知"。
// 这个包只处理 models.dev 这一层和合并；服务商返回的字段由 llm 包解码。
//
// models.dev 的数据不能原样信：同一个模型在各家转售商里限额差很大，输出上限常被写成等于窗口，图像和语音模型窗口是 0。
// 所以这里只留需要的字段、过滤坏数据、原厂优先，各家说法不一致时取保守的最小值并标出来。
package modelmeta

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Entry 是 models.dev 里一个服务商下的一个模型（瘦身后只留限额和状态）
type Entry struct {
	Provider string `json:"p"`
	ID       string `json:"id"`
	Context  int    `json:"c,omitempty"`
	Input    int    `json:"i,omitempty"`
	Output   int    `json:"o,omitempty"`
	Status   string `json:"s,omitempty"` // deprecated、beta、alpha；空是正常
	Type     string `json:"t,omitempty"` // 空或 chat 是聊天模型
}

// origins 是模型原厂在 models.dev 里的服务商 ID：同一个模型有原厂条目时只看原厂的，转售商（venice、orcarouter 等）常把限额写错
var origins = map[string]bool{
	"openai": true, "anthropic": true, "deepseek": true, "xai": true, "google": true, "agnes": true, "mistral": true,
	"moonshotai": true, "zai": true, "alibaba": true, "minimax": true, "meta": true, "cohere": true,
}

// Catalog 是解码后的一份只读快照
type Catalog struct {
	byName map[string][]Entry
	Count  int
}

// Found 是一次查询的结果：每个字段 0 = models.dev 没有可信的值
type Found struct {
	Context, Output int
	// Mixed：各家说法不一致，取了最小值
	Mixed bool
	// From 是用到的服务商 ID（原厂优先），悬停提示用
	From []string
}

// decodeCatalog 读 models.dev 的 api.json（顶层是 {服务商ID: {models: {模型ID: {...}}}}）。
// 单个模型读不懂（字段类型不对、id 含控制字符）只丢这一条，不影响别的；整体不是 JSON 才报错
func decodeCatalog(data []byte) ([]Entry, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	var out []Entry
	for provider, raw := range top {
		if badID(provider) {
			continue
		}
		var p struct {
			Models map[string]json.RawMessage `json:"models"`
		}
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		for id, mraw := range p.Models {
			var m struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				State string `json:"status"`
				Limit struct {
					Context int `json:"context"`
					Input   int `json:"input"`
					Output  int `json:"output"`
				} `json:"limit"`
			}
			if json.Unmarshal(mraw, &m) != nil || badID(id) {
				continue
			}
			l := m.Limit
			if l.Context < 0 || l.Input < 0 || l.Output < 0 {
				continue
			}
			out = append(out, Entry{Provider: provider, ID: id, Context: l.Context, Input: l.Input, Output: l.Output, Status: m.State, Type: m.Type})
		}
	}
	return out, nil
}

func badID(s string) bool {
	if s == "" || len(s) > 200 {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// NewCatalog 建查询用的索引。非聊天模型（图像、语音、向量……）不进目录；
// 窗口是 0 的条目没有窗口可用（输出同理）；输出不小于窗口的当输出未知（转售商常把输出写成窗口）
func NewCatalog(entries []Entry) *Catalog {
	c := &Catalog{byName: map[string][]Entry{}}
	for _, e := range entries {
		if e.Type != "" && e.Type != "chat" {
			continue
		}
		if e.Context <= 0 && e.Output <= 0 {
			continue
		}
		if e.Output >= e.Context {
			e.Output = 0
		}
		key := strings.ToLower(e.ID)
		c.byName[key] = append(c.byName[key], e)
		c.Count++
	}
	return c
}

// names 是一个模型 ID 在 models.dev 里可能的写法：去掉服务商前缀（网关的 id 形如 codex/gpt-6.1-sol），
// 小数点写成横线（deepseek-v4.1-flash → deepseek-v4-1-flash）。不按"前缀相同"模糊匹配：gpt-5.5 不能配到 gpt-5.5-pro
func names(id string) []string {
	n := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	if n == "" {
		return nil
	}
	out := []string{n}
	if d := strings.ReplaceAll(n, ".", "-"); d != n {
		out = append(out, d)
	}
	return out
}

// Lookup 查一个模型 ID（网关或服务商给的写法）。没有可信的值返回 false
func (c *Catalog) Lookup(id string) (Found, bool) {
	if c == nil {
		return Found{}, false
	}
	var cands []Entry
	for _, n := range names(id) {
		cands = append(cands, c.byName[n]...)
		if len(cands) > 0 {
			break
		}
	}
	if len(cands) == 0 {
		return Found{}, false
	}
	// 原厂优先：有原厂条目就只看原厂的
	var origin []Entry
	for _, e := range cands {
		if origins[e.Provider] {
			origin = append(origin, e)
		}
	}
	if len(origin) > 0 {
		cands = origin
	}
	var f Found
	seen := map[string]bool{}
	for _, e := range cands {
		if e.Context > 0 {
			if f.Context > 0 && e.Context != f.Context {
				f.Mixed = true
			}
			if f.Context == 0 || e.Context < f.Context {
				f.Context = e.Context
			}
		}
		if e.Output > 0 {
			if f.Output > 0 && e.Output != f.Output {
				f.Mixed = true
			}
			if f.Output == 0 || e.Output < f.Output {
				f.Output = e.Output
			}
		}
		if !seen[e.Provider] {
			seen[e.Provider] = true
			f.From = append(f.From, e.Provider)
		}
	}
	if f.Context == 0 && f.Output == 0 {
		return Found{}, false
	}
	// 取最小值之后输出不能再不小于窗口
	if f.Output >= f.Context && f.Context > 0 {
		f.Output = 0
	}
	return f, true
}
