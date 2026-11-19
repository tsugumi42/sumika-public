package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
)

type Entry struct {
	PublicID, ServerID, RawName, Description, SchemaDigest, Binding, Fingerprint string
	InputSchema                                                                  json.RawMessage
	Status                                                                       string
	ReadOnlyHint                                                                 bool // 服务器自称只读，没有核实
	schema                                                                       *jsonschema.Resolved
	terms                                                                        map[string]int
	length                                                                       int
}

func catalog(ctx context.Context, session *mcp.ClientSession, cfg ServerConfig, binding string) ([]Entry, error) {
	var entries []Entry
	cursor := ""
	seen := map[string]bool{}
	names := map[string]bool{}
	total := 0
	for range 1000 {
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("MCP 目录读取失败")
		}
		for _, t := range res.Tools {
			raw, err := json.Marshal(t)
			if err != nil {
				return nil, err
			}
			total += len(raw)
			if total > 32<<20 || len(entries) >= 10000 {
				return nil, fmt.Errorf("MCP 目录超限")
			}
			if !included(cfg, t.Name) {
				continue
			}
			id := publicID(cfg.ID, t.Name)
			if names[id] {
				return nil, fmt.Errorf("MCP 公开 ID 冲突")
			}
			names[id] = true
			schema, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, err
			}
			e := Entry{PublicID: id, ServerID: cfg.ID, RawName: t.Name, Description: t.Description, SchemaDigest: ecosystem.Digest(json.RawMessage(schema)), Binding: binding, Fingerprint: fingerprint(cfg), InputSchema: schema, Status: "ready", ReadOnlyHint: t.Annotations != nil && t.Annotations.ReadOnlyHint}
			if len(schema) > 32768 {
				e.Status = "schema_too_large"
			} else {
				var sch jsonschema.Schema
				if err = json.Unmarshal(schema, &sch); err != nil {
					e.Status = "invalid_schema"
				} else if sch.Type != "object" {
					e.Status = "invalid_schema"
				} else if e.schema, err = sch.Resolve(nil); err != nil {
					e.Status = "unsupported_schema"
				}
			}
			e.terms = tokens(id + " " + cfg.ID + " " + t.Name + " " + t.Description)
			for _, v := range e.terms {
				e.length += v
			}
			entries = append(entries, e)
		}
		if res.NextCursor == "" {
			sort.Slice(entries, func(i, j int) bool { return entries[i].PublicID < entries[j].PublicID })
			return entries, nil
		}
		if seen[res.NextCursor] {
			return nil, fmt.Errorf("MCP 重复目录游标")
		}
		seen[res.NextCursor] = true
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("MCP 目录页数超限")
}
func tokens(text string) map[string]int {
	out := map[string]int{}
	var word []rune
	var last rune
	flush := func() {
		if len(word) > 0 {
			out[string(word)]++
			word = word[:0]
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			out[string(r)]++
			if last != 0 {
				out[string([]rune{last, r})]++
			}
			last = r
		} else {
			last = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	return out
}

type SearchRequest struct {
	Query, Server, Cursor string
	Limit                 int
}
type SearchResult struct {
	Items      []Entry
	NextCursor string
}

func (c *Client) Search(ctx context.Context, a ecosystem.Access, q SearchRequest) (SearchResult, error) {
	if q.Limit == 0 {
		q.Limit = 8
	}
	if q.Limit < 1 || q.Limit > 20 {
		return SearchResult{}, fmt.Errorf("MCP 搜索条数越界")
	}
	var entries []Entry
	c.mu.Lock()
	servers := make([]*server, 0, len(c.servers))
	for _, s := range c.servers {
		servers = append(servers, s)
	}
	c.mu.Unlock()
	for _, s := range servers {
		s.mu.Lock()
		if (q.Server == "" || q.Server == s.config.ID) && visible(a, s) {
			entries = append(entries, s.entries...)
		}
		s.mu.Unlock()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].PublicID < entries[j].PublicID })
	version := ecosystem.Digest(entries)
	key := ecosystem.Digest([]any{a.Key(), q.Query, q.Server, q.Limit, version})
	offset := 0
	if q.Cursor != "" {
		var cursor struct {
			Key    string
			Offset int
		}
		if err := decodeCursor(q.Cursor, &cursor); err != nil || cursor.Key != key {
			return SearchResult{}, fmt.Errorf("cursor_invalidated")
		}
		offset = cursor.Offset
	}
	query := tokens(q.Query)
	scores := map[string]float64{}
	avg := 0.0
	df := map[string]int{}
	for _, e := range entries {
		avg += float64(e.length)
		for term := range query {
			if e.terms[term] > 0 {
				df[term]++
			}
		}
	}
	if len(entries) > 0 {
		avg /= float64(len(entries))
	}
	for _, e := range entries {
		for term := range query {
			tf := float64(e.terms[term])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(entries)-df[term])+0.5)/(float64(df[term])+0.5))
			scores[e.PublicID] += idf * tf * 2.2 / (tf + 1.2*(0.25+0.75*float64(e.length)/avg))
		}
	}
	if q.Query != "" {
		filtered := entries[:0]
		for _, e := range entries {
			if scores[e.PublicID] > 0 {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
		sort.SliceStable(entries, func(i, j int) bool { return scores[entries[i].PublicID] > scores[entries[j].PublicID] })
	}
	if offset < 0 || offset > len(entries) {
		return SearchResult{}, fmt.Errorf("cursor_invalidated")
	}
	out := SearchResult{}
	end := offset
	for end < len(entries) && len(out.Items) < q.Limit {
		e := entries[end]
		if e.Status != "ready" {
			e.InputSchema = nil
		}
		out.Items = append(out.Items, e)
		raw, _ := json.Marshal(out)
		if len(raw) > 65536-256 {
			out.Items = out.Items[:len(out.Items)-1]
			break
		}
		end++
	}
	if end == offset && end < len(entries) {
		return SearchResult{}, fmt.Errorf("search_item_too_large")
	}
	if end < len(entries) {
		out.NextCursor = encodeCursor(struct {
			Key    string
			Offset int
		}{key, end})
	}
	return out, ctx.Err()
}

// fingerprint 是服务连接目标的稳定指纹：服务 ID、类型、地址、命令、参数、工作目录，加上环境变量和请求头的键名。
// 不含凭据值（指纹会写进放行规则文件），不含 Sumika 自己的筛选和超时；不带进程随机量，重启后同一份配置得到同一个值。
func fingerprint(cfg ServerConfig) string {
	keys := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	raw, _ := json.Marshal(struct {
		ID, Type, URL, Command, CWD string
		Args, Env, Headers          []string
	}{cfg.ID, cfg.Type, cfg.URL, cfg.Command, cfg.CWD, cfg.Args, keys(cfg.Env), keys(cfg.Headers)})
	sum := sha256.Sum256(raw)
	return "cfg-" + hex.EncodeToString(sum[:12])
}
