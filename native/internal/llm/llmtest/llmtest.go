// Package llmtest 是测试用的假网关：照 Magpie 的样子回模型清单和 SSE，记下原样的请求体。内核的对拍（turn 的 45 份场景）、
// 内核自己的契约测试和 turntest 夹具共用这一份；它不引用 turn，内核的包内测试也能用
package llmtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Reply 是一次请求的回复（和 scripts/go-golden-turn.test.ts 的 Reply 一样，场景文件的 reply、replies 照这个格式）
type Reply struct {
	Reasoning []string          `json:"reasoning"`
	Content   []string          `json:"content"`
	Usage     json.RawMessage   `json:"usage"`
	Headers   map[string]string `json:"headers"`
	Finish    string            `json:"finish"` // 最后的 finish_reason；空是 stop（有工具调用时是 tool_calls）
	ToolCalls []ToolCall        `json:"toolCalls"`
	// ReasoningDetails：结构化的思考，每一项是一块里的 reasoning_details 数组
	ReasoningDetails []json.RawMessage `json:"reasoningDetails"`
}

// ToolCall 是回复里的一个工具调用
type ToolCall struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Args []string `json:"args"` // 参数 JSON 拆成的几块
	// RepeatName：每一块都带着名字（pi 只在名字还空着时补，不拼接）；NoIndex：块里不带 index（pi 按 id 认）
	RepeatName bool `json:"repeatName"`
	NoIndex    bool `json:"noIndex"`
}

// usage 是 Text、Call 用的用量（场景文件里的回复自己写用量）
var usage = json.RawMessage(`{"prompt_tokens":100,"completion_tokens":10}`)

// Text 是一段正文的回复，parts 是一块一块吐的
func Text(parts ...string) Reply { return Reply{Content: parts, Usage: usage} }

// Call 是调一个工具的回复，args 是参数的 JSON（一块吐完）
func Call(id, name, args string) Reply {
	return Reply{ToolCalls: []ToolCall{{ID: id, Name: name, Args: []string{args}}}, Usage: usage}
}

// Script 是假网关照着回的东西
type Script struct {
	Models  []json.RawMessage // GET 模型清单回的 data
	Reply   Reply             // 没写在 Replies 里的那几次回它
	Replies []Reply           // 第 n 次请求回 Replies[n-1]
	// Hold 说第 nth 次请求（从 1 数）停在第几段正文上：吐完前 n 段就停住，等对方中止连接；负数是不停。nil 是都不停
	Hold func(nth int) int
}

// Handler 是假网关本身：POST 记下原样的请求体再按 Script 吐 SSE，GET 回模型清单。bodies 归 mu 管
// （调用方要看现在是第几次请求，就用同一把锁）
func Handler(s Script, bodies *[]string, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"data": s.Models})
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(b))
		nth := len(*bodies)
		mu.Unlock()
		rp := s.Reply
		if n := nth - 1; n < len(s.Replies) {
			rp = s.Replies[n]
		}
		hold := -1
		if s.Hold != nil {
			hold = s.Hold(nth)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for k, v := range rp.Headers {
			w.Header().Set(k, v)
		}
		chunk := func(o map[string]any) {
			o["id"], o["object"], o["created"], o["model"] = "golden", "chat.completion.chunk", 0, "x"
			j, _ := json.Marshal(o)
			fmt.Fprintf(w, "data: %s\n\n", j)
		}
		delta := func(d map[string]any, finish any) {
			chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": finish}}})
		}
		for i, r := range rp.Reasoning {
			d := map[string]any{"reasoning_content": r}
			if i == 0 {
				d["role"] = "assistant"
			}
			delta(d, nil)
		}
		for _, d := range rp.ReasoningDetails {
			delta(map[string]any{"reasoning_details": d}, nil)
		}
		for i, s := range rp.Content {
			if i == hold {
				break
			}
			delta(map[string]any{"content": s}, nil)
		}
		if hold >= 0 {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		// 工具调用：第一块带 id、名字，后面只带参数；几个调用的块交替着吐（和 golden 脚本一样）
		most := 0
		for _, tc := range rp.ToolCalls {
			most = max(most, len(tc.Args))
		}
		for k := 0; k < most; k++ {
			for i, tc := range rp.ToolCalls {
				if k >= len(tc.Args) {
					continue
				}
				fn := map[string]any{"arguments": tc.Args[k]}
				if tc.RepeatName {
					fn["name"] = tc.Name
				}
				call := map[string]any{"index": i, "function": fn}
				if k == 0 {
					call = map[string]any{"index": i, "id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": tc.Args[0]}}
				}
				if tc.NoIndex {
					delete(call, "index")
					call["id"] = tc.ID
				}
				delta(map[string]any{"tool_calls": []any{call}}, nil)
			}
		}
		finish := rp.Finish
		if finish == "" {
			finish = "stop"
			if len(rp.ToolCalls) > 0 {
				finish = "tool_calls"
			}
		}
		delta(map[string]any{}, finish)
		chunk(map[string]any{"choices": []any{}, "usage": rp.Usage})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}
