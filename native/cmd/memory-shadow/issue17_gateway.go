package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/memory"
	"sumika-native/internal/tools"
)

type issue17Call struct {
	Purpose   string
	Request   json.RawMessage
	Response  string
	Usage     llm.Usage
	UsageSeen bool
	Route     llm.Route
	MS        int64
	Error     string
}

type issue17Gateway struct {
	mu        sync.Mutex
	mode      mode
	limit     int
	calls     []issue17Call
	exhausted bool
	client    *llm.Client
	server    *httptest.Server
	embedding embed.Embedder
}

func newIssue17Gateway(m mode, limit int, base string) *issue17Gateway {
	g := &issue17Gateway{mode: m, limit: limit, client: llm.New(llm.Magpie(base))}
	g.embedding = issue17Embed{}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}
func (g *issue17Gateway) snapshot() ([]issue17Call, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]issue17Call(nil), g.calls...), g.exhausted
}
func (g *issue17Gateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}
func (g *issue17Gateway) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var req struct {
		Model               string          `json:"model"`
		Messages            json.RawMessage `json:"messages"`
		Tools               json.RawMessage `json:"tools"`
		CacheKey            string          `json:"prompt_cache_key"`
		Thinking            string          `json:"reasoning_effort"`
		MaxCompletionTokens int64           `json:"max_completion_tokens"`
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err = json.Unmarshal(req.Messages, &msgs); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	textOf := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &parts)
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	summary := len(msgs) == 1 && strings.Contains(textOf(msgs[0].Content), "episode_record")
	purpose := "answer"
	if summary {
		purpose = "summary"
	}
	g.mu.Lock()
	if g.mode == modeReal && len(g.calls) >= g.limit {
		g.exhausted = true
		g.mu.Unlock()
		http.Error(w, "issue17 spend exhausted", 400)
		return
	}
	idx := len(g.calls)
	g.calls = append(g.calls, issue17Call{Purpose: purpose, Request: json.RawMessage(raw)})
	g.mu.Unlock()
	started := time.Now()
	result := llm.Result{}
	if g.mode == modeReal {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		// 原样转发生产 Hub 拼的 messages / tools，不添加答案、来源或新系统规则。
		var chatMsgs []chat.Message
		for _, m := range msgs {
			role := chat.Role(m.Role)
			switch role {
			case chat.RoleSystem, chat.RoleUser, chat.RoleAssistant, chat.RoleTool:
				chatMsgs = append(chatMsgs, chat.Message{Role: role, Blocks: []chat.Block{chat.Text(textOf(m.Content))}})
			}
		}
		var decls []tools.Declaration
		if len(req.Tools) > 0 && string(req.Tools) != "null" {
			var rawTools []struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			}
			if json.Unmarshal(req.Tools, &rawTools) == nil {
				for _, t := range rawTools {
					decls = append(decls, tools.Declaration{Name: t.Function.Name, Description: t.Function.Description, Parameters: string(t.Function.Parameters)})
				}
			}
		}
		result, err = g.client.StreamChat(ctx, chat.Request{Model: req.Model, Style: chat.StyleForeground, Messages: chatMsgs, Tools: decls,
			CacheKey: req.CacheKey, Thinking: req.Thinking, MaxOutputTokens: req.MaxCompletionTokens, NoStore: true}, nil)
	} else if summary {
		result.Text = issue17FakeSummary(textOf(msgs[0].Content))
	} else {
		// 假回答只复述实际收到的记忆块；不查问题预期，不能把它的结果当质量分数。
		var b strings.Builder
		for _, m := range msgs {
			s := textOf(m.Content)
			if strings.Contains(s, "可能相关的记忆：") {
				start := strings.LastIndex(s, "可能相关的记忆：")
				b.WriteString(s[start:])
				b.WriteByte('\n')
			}
		}
		result.Text = b.String()
		if result.Text == "" {
			result.Text = "没有可用的记忆证据，我不知道。"
		}
	}
	rec := issue17Call{Purpose: purpose, Request: json.RawMessage(raw), Response: result.Text, Usage: result.Usage, UsageSeen: result.UsageSeen, Route: result.Route, MS: time.Since(started).Milliseconds()}
	if err != nil {
		rec.Error = err.Error()
	}
	g.mu.Lock()
	g.calls[idx] = rec
	g.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(obj any) { b, _ := json.Marshal(obj); _, _ = fmt.Fprintf(w, "data: %s\n\n", b) }
	delta := map[string]any{"role": "assistant", "content": result.Text}
	if len(result.ToolCalls) > 0 {
		var calls []any
		for i, t := range result.ToolCalls {
			calls = append(calls, map[string]any{"index": i, "id": t.ID, "type": "function", "function": map[string]any{"name": t.Name, "arguments": t.Args}})
		}
		delta["tool_calls"] = calls
	}
	stop := "stop"
	if len(result.ToolCalls) > 0 {
		stop = "tool_calls"
	}
	emit(map[string]any{"id": "issue17", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": stop}}})
	if result.UsageSeen {
		u := result.Usage
		emit(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": u.Input + u.CacheRead + u.CacheWrite, "completion_tokens": u.Output, "prompt_tokens_details": map[string]any{"cached_tokens": u.CacheRead, "cache_write_tokens": u.CacheWrite}}})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

type issue17Generator struct{ client *llm.Client }

func (g issue17Generator) Call(ctx context.Context, model, prompt, key string) (memory.Reply, error) {
	r, err := g.client.StreamChat(ctx, chat.Request{Model: model, Style: chat.StyleBackground, CacheKey: key, NoStore: true,
		Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text(prompt)}}}}, nil)
	return memory.Reply{Text: r.Text}, err
}
