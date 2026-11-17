package openaichat

// 流式回复的解码：SSE 行、chunk/delta 的线上字段、三种思考字段、reasoning_details 的合并、工具调用增量、用量、finish_reason。
// 只管"给一段字节、吐出事件和结果"，不碰计时、超时、看门狗和 HTTP 头：这些是传输层的事（规格-模型对话编解码 坑 11）。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
)

// Event 是流里一块带来的进展：正文增量、思考增量、工具调用增量、用量。
// Progress 说这块算不算"内容进展"（内容看门狗用）：正文、思考、工具调用的字段、认得的 reasoning_details。
// 只有用量的块、空增量、只有心跳的块都不算。
type Event struct {
	Text      string
	Thinking  string
	ToolCalls []ToolCallDelta
	Usage     *chat.Usage
	Progress  bool
}

// ToolCallDelta 是工具调用的一块增量。Index 只在块里真给了数时才非空（不给的兼容网关按 id 认）
type ToolCallDelta struct {
	Index     *int64
	ID        string
	Name      string
	Arguments string
}

// ToolCall 是拼好的一个工具调用：Args 是模型给的参数原文，没解析（解析再序列化会改空白）
type ToolCall struct {
	ID, Name, Args string
}

// Output 是解码一条流的全部产出。Result.Blocks 里的工具调用带解析好的参数；
// ToolCalls 另给原文，llm.Result 要原文（录像回放按原文比）
type Output struct {
	Result    chat.Result
	ToolCalls []ToolCall
}

// StreamError：流里来了 error 事件（回复头已经发出去，没有状态码）。Data 是这个事件的原始数据
type StreamError struct {
	Message string
	Data    []byte
}

func (e *StreamError) Error() string { return e.Message }

// StopError：服务商的结束原因表示出错（content_filter、不认识的），或者流结束了却没给 finish_reason（Reason 是空）
type StopError struct{ Reason string }

func (e *StopError) Error() string {
	if e.Reason == "" {
		return "回复流结束了，但没有 finish_reason（可能中途断了）"
	}
	return "服务商的 finish_reason 是 " + e.Reason
}

// NonStreamError：回了 200 却不是 SSE（JSON 错误、一段文字）。Body 是回复体里说的原因（去掉注释和空行）
type NonStreamError struct{ Body string }

func (e *NonStreamError) Error() string { return e.Body }

// chunk 是流里的一块；字段类型对不上时（服务商乱放）只当没有这个字段，不当成出错
type chunk struct {
	Error   json.RawMessage `json:"error"`
	Usage   json.RawMessage `json:"usage"`
	Choices []struct {
		FinishReason string          `json:"finish_reason"`
		Usage        json.RawMessage `json:"usage"`
		Delta        json.RawMessage `json:"delta"`
	} `json:"choices"`
}

// delta 是一块里 choices[0].delta 的已知字段；思考字段（各家名字不同）另按名字取原文
type delta struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		Index    *int64 `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// lenient 解 JSON：类型对不上的字段跳过（和 openai-go 一样宽容），语法错才算错
func lenient(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return nil
	}
	return err
}

// sseReader 一块一块读 SSE 流。读法照 openai-go 的 ssestream：字段名在冒号前、冒号后去掉一个空格、
// event 和 data 以外的行（含 ":" 开头的注释心跳）不理，空行才算一个事件结束，没有 data 的空事件跳过，
// data 以 [DONE] 开头就是流结束，顶层带 error 的算流内报错。
type sseReader struct {
	scn  *bufio.Scanner
	err  error
	done bool
	// sawEvent：见过 data/event 行；rest：不是 data/event、也不是注释的非空行（200 却不是 SSE 时取原因用）
	sawEvent bool
	rest     []string
}

func newSSEReader(r io.Reader) *sseReader {
	scn := bufio.NewScanner(r)
	scn.Buffer(nil, bufio.MaxScanTokenSize<<9)
	return &sseReader{scn: scn}
}

// Err 是读流出的错（正常读完、[DONE] 是 nil）
func (s *sseReader) Err() error { return s.err }

func (s *sseReader) finish(err error) bool {
	s.err, s.done = err, true
	return false
}

// next 读下一块；false 就是流结束（看 Err 是不是出错）
func (s *sseReader) next() (chunk, bool) {
	if s.done {
		return chunk{}, false
	}
	var data []byte
	for {
		got := false
		for s.scn.Scan() {
			txt := s.scn.Bytes()
			if len(txt) == 0 {
				if len(data) == 0 {
					continue
				}
				got = true
				break
			}
			trimmed := bytes.TrimSpace(txt)
			isData := bytes.HasPrefix(trimmed, []byte("data:"))
			isEvent := bytes.HasPrefix(trimmed, []byte("event:"))
			if isData || isEvent {
				s.sawEvent = true
			} else if len(trimmed) > 0 && !bytes.HasPrefix(trimmed, []byte(":")) {
				s.rest = append(s.rest, string(trimmed))
			}
			name, value, _ := bytes.Cut(txt, []byte(":"))
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			if string(name) == "data" {
				data = append(data, value...)
				data = append(data, '\n')
			}
		}
		if !got {
			if err := s.scn.Err(); err != nil {
				s.finish(err)
			} else {
				s.finish(nil)
			}
			return chunk{}, false
		}
		if bytes.HasPrefix(data, []byte("[DONE]")) {
			s.finish(nil)
			return chunk{}, false
		}
		var c chunk
		if err := lenient(data, &c); err != nil {
			s.finish(err)
			return chunk{}, false
		}
		if len(c.Error) > 0 && !bytes.Equal(bytes.TrimSpace(c.Error), []byte("null")) {
			s.finish(&StreamError{Message: fmt.Sprintf("received error while streaming: %s", errorText(c.Error)), Data: data})
			return chunk{}, false
		}
		return c, true
	}
}

// errorText：error 是字符串就取字符串本身，其余（对象）取原文
func errorText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

var reasoningFields = []string{"reasoning_content", "reasoning", "reasoning_text"}

// reasoningOf 返回这一块的思考和它在哪个字段
func reasoningOf(extra map[string]json.RawMessage) (string, string) {
	for _, k := range reasoningFields {
		f, ok := extra[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(f, &s) == nil && s != "" {
			return s, k
		}
	}
	return "", ""
}

// rawUsage 是各家放用量的位置：缓存命中 OpenAI / OpenRouter 在 prompt_tokens_details.cached_tokens，
// DeepSeek 在 prompt_cache_hit_tokens，Kimi 在顶层 cached_tokens；按这个顺序取第一个有的（0 也算有，同 pi 的 ??）
type rawUsage struct {
	PromptTokens        int64  `json:"prompt_tokens"`
	CompletionTokens    int64  `json:"completion_tokens"`
	PromptCacheHit      *int64 `json:"prompt_cache_hit_tokens"`
	CachedTokens        *int64 `json:"cached_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     *int64 `json:"cached_tokens"`
		CacheWriteTokens int64  `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// parseUsage 照 pi 的 parseChunkUsage：Input 是没命中缓存的输入（prompt_tokens 减去缓存读、缓存写）；
// Output 已经含 Reasoning
func parseUsage(raw string) (chat.Usage, bool) {
	var r rawUsage
	if raw == "" || raw == "null" || json.Unmarshal([]byte(raw), &r) != nil {
		return chat.Usage{}, false
	}
	var u chat.Usage
	switch {
	case r.PromptTokensDetails != nil && r.PromptTokensDetails.CachedTokens != nil:
		u.CacheRead = *r.PromptTokensDetails.CachedTokens
	case r.PromptCacheHit != nil:
		u.CacheRead = *r.PromptCacheHit
	case r.CachedTokens != nil:
		u.CacheRead = *r.CachedTokens
	}
	if r.PromptTokensDetails != nil {
		u.CacheWrite = r.PromptTokensDetails.CacheWriteTokens
	}
	u.Input = max(0, r.PromptTokens-u.CacheRead-u.CacheWrite)
	u.Output = r.CompletionTokens
	if r.CompletionTokensDetails != nil {
		u.Reasoning = r.CompletionTokensDetails.ReasoningTokens
	}
	return u, true
}

// Decode 读一条 OpenAI 聊天的 SSE 流：每块的进展交给 onEvent（可以为 nil），最后给出中立结果。
// 不碰计时和重试；结束原因收到了以后（finish_reason 非空）再断开、卡住都不算出错。
func Decode(r io.Reader, onEvent func(Event)) (Output, error) {
	s := newSSEReader(r)
	var text, thinking strings.Builder
	var usage chat.Usage
	var field string
	var details []jsjson.Value
	byIndex, byID := map[int64]int{}, map[string]int{}
	var calls []ToolCall
	finish := ""
	chunks := 0
	for {
		c, ok := s.next()
		if !ok {
			break
		}
		chunks++
		var ev Event
		progress := false
		usageRaw := ""
		if u := bytes.TrimSpace(c.Usage); len(u) > 0 && string(u) != "null" {
			usageRaw = string(u)
		}
		if len(c.Choices) > 0 {
			ch := c.Choices[0]
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			// 有的服务商（Moonshot）把用量放在 choice.usage 里（同 pi 371 行）
			if len(ch.Usage) > 0 && usageRaw == "" {
				usageRaw = string(ch.Usage)
			}
			var d delta
			var extra map[string]json.RawMessage
			if len(ch.Delta) > 0 {
				if lenient(ch.Delta, &d) != nil || lenient(ch.Delta, &extra) != nil {
					d, extra = delta{}, nil
				}
			}
			ev.Text = d.Content
			// 思考增量：DeepSeek 官方 reasoning_content，OpenRouter 式的网关 reasoning，还有 reasoning_text；
			// 按这个顺序取第一个不为空的（pi openai-completions.js 390 行）
			var gotField string
			ev.Thinking, gotField = reasoningOf(extra)
			if gotField != "" && field == "" {
				field = gotField
			}
			// 结构化的思考（reasoning_details，pi 416 行）：认得的一块一块合并，有了就算有一块思考（哪怕没有思考文字）
			if f, ok := extra["reasoning_details"]; ok {
				if arr, err := jsjson.Parse(string(f)); err == nil && arr.Kind == jsjson.Array {
					for _, x := range arr.Arr {
						if isReasoningDetail(x) {
							details = appendReasoningDetail(details, x)
							progress = true
						}
					}
				}
			}
			// 工具调用的分块（openai-completions.js 294 行）：先按 index 找、再按 id 找已有的那个，都没有新开一个；
			// id、名字只在还空着的时候补（名字不拼接），参数一块一块接上
			for _, tc := range d.ToolCalls {
				ev.ToolCalls = append(ev.ToolCalls, ToolCallDelta{Index: tc.Index, ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
				if tc.ID != "" || tc.Function.Name != "" || tc.Function.Arguments != "" {
					progress = true
				}
				// index 只在块里真给了数时才认（不给的兼容网关按 id 认）；按 index 记的表只在新开一个调用时登记
				hasIndex := tc.Index != nil
				i, ok := 0, false
				if hasIndex {
					i, ok = byIndex[*tc.Index]
				}
				if !ok && tc.ID != "" {
					i, ok = byID[tc.ID]
				}
				if !ok {
					calls = append(calls, ToolCall{ID: tc.ID})
					i = len(calls) - 1
					if hasIndex {
						byIndex[*tc.Index] = i
					}
					if tc.ID != "" {
						byID[tc.ID] = i
					}
				}
				call := &calls[i]
				if call.ID == "" && tc.ID != "" {
					call.ID = tc.ID
					byID[tc.ID] = i
				}
				if call.Name == "" {
					call.Name = tc.Function.Name
				}
				call.Args += tc.Function.Arguments
			}
		}
		if ev.Text != "" || ev.Thinking != "" {
			progress = true
		}
		ev.Progress = progress
		text.WriteString(ev.Text)
		thinking.WriteString(ev.Thinking)
		if u, ok := parseUsage(usageRaw); ok {
			usage = u
			ev.Usage = &u
		}
		if onEvent != nil && (progress || ev.Usage != nil) {
			onEvent(ev)
		}
	}
	replay := ReplayFrom(field, details)
	out := Output{
		Result:    chat.Result{Blocks: resultBlocks(text.String(), thinking.String(), replay, calls), Usage: usage, Replay: replay},
		ToolCalls: calls,
	}
	err := s.Err()
	if finish != "" {
		// 结束原因已经收到了，回复就是完整的：之后（用量、[DONE] 之前）断了、卡住都不算出错
		err = nil
	} else if err == nil {
		if chunks == 0 && !s.sawEvent && len(s.rest) > 0 {
			err = &NonStreamError{Body: strings.Join(s.rest, "\n")}
		}
	}
	if err != nil {
		return out, err
	}
	// 结束原因照 pi（mapStopReason、485～489 行）：没给 finish_reason、内容过滤、网络错误、不认识的都算出错
	switch finish {
	case "stop", "end":
		out.Result.StopReason = chat.StopReasonStop
	case "length":
		out.Result.StopReason = chat.StopReasonLength
	case "tool_calls", "function_call":
		out.Result.StopReason = chat.StopReasonToolUse
	default:
		out.Result.StopReason = chat.StopReasonError
		return out, &StopError{Reason: finish}
	}
	return out, nil
}

// resultBlocks 把累积的正文、思考、工具调用收成内容块：思考在前、正文其次、调用按出现顺序
func resultBlocks(text, thinking string, replay chat.Replay, calls []ToolCall) []chat.Block {
	var blocks []chat.Block
	if thinking != "" || replay.Codec != "" {
		blocks = append(blocks, chat.Thinking{Text: thinking, Replay: replay})
	}
	if text != "" {
		blocks = append(blocks, chat.Text(text))
	}
	for _, c := range calls {
		args, _ := jsjson.Parse(c.Args) // 解析不了留零值：原文在 Output.ToolCalls，内核照旧自己解析、记 ArgsErr
		blocks = append(blocks, chat.ToolCall{ID: c.ID, Name: c.Name, Arguments: args})
	}
	return blocks
}
