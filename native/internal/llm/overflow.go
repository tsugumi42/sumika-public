package llm

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

// 报错规则照 pi-ai dist/utils/overflow.js 的 OVERFLOW_PATTERNS、NON_OVERFLOW_PATTERNS，
// 另补 omp pi-ai src/error/flags.ts 的 CONTEXT_OVERFLOW_EVIDENCE_PATTERNS（下面标 omp）。
// 这里只认错误；成功回复的 usage / length 由回合结合实例窗口判断。正则在包初始化时编好。
// pi 的空正文 400 特例只给 Cerebras；这里没有 provider，不搬，避免把普通 400 当成溢出。
var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt (?:is )?too long`),                                                                                         // pi：Anthropic、z.ai、Ollama
	regexp.MustCompile(`(?i)prompt exceeds max length`),                                                                                       // pi：z.ai 中国端点
	regexp.MustCompile(`(?i)request_too_large`),                                                                                               // pi：Anthropic HTTP 413
	regexp.MustCompile(`(?i)input is too long for requested model`),                                                                           // pi、omp：Bedrock
	regexp.MustCompile(`(?i)exceeds the context window`),                                                                                      // pi、omp：OpenAI
	regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?maximum context length(?: of [\d,]+ tokens?|\s*\([\d,]+\))`),                       // pi：LiteLLM、OpenAI 代理
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),                                                                          // pi、omp：Gemini
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),                                                                                    // pi、omp：xAI
	regexp.MustCompile(`(?i)reduce the length of the messages`),                                                                               // pi、omp：Groq
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),                                                                            // pi、omp：OpenRouter
	regexp.MustCompile(`(?i)exceeds (?:the )?maximum allowed input length of [\d,]+ tokens?`),                                                 // pi：Poolside
	regexp.MustCompile(`(?i)input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)`),                                  // pi：Together
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),                                                                                        // pi、omp：Copilot
	regexp.MustCompile(`(?i)exceeds the available context size`),                                                                              // pi、omp：llama.cpp
	regexp.MustCompile(`(?i)greater than the context length`),                                                                                 // pi、omp：LM Studio
	regexp.MustCompile(`(?i)context window exceeds limit`),                                                                                    // pi、omp：MiniMax
	regexp.MustCompile(`(?i)exceeded model token limit`),                                                                                      // pi、omp：Kimi
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),                                                             // pi：Mistral
	regexp.MustCompile(`(?i)prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?`),                                    // pi：DS4
	regexp.MustCompile(`(?i)model_context_window_exceeded`),                                                                                   // pi、omp：z.ai
	regexp.MustCompile(`(?i)range of input length should be`),                                                                                 // pi：DashScope / Qwen
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded|too many tokens|token limit exceeded`),                                              // pi、omp：通用
	regexp.MustCompile(`(?i)^(?:[\w -]+ API error \(413\):|413(?:\s|:|$))`),                                                                   // pi：原始 SDK / 格式化 HTTP 413
	regexp.MustCompile(`(?i)\bprompt\s*\(\s*\d+\s+tokens\s*\)\s*\+\s*max\s+tokens\s*\(\s*\d+\s*\)\s+exceeds\s+the\s+context\s*\(\s*\d+\s*\)`), // omp：Strata
	regexp.MustCompile(`(?i)\bprompt\s*\(\s*\d+\s+tokens\s*\)\s+leaves\s+no\s+room\s+to\s+answer\s+in\s+the\s+context\s*\(\s*\d+\s*\)`),       // omp：Strata 没空间回复
	regexp.MustCompile(`(?i)requested tokens?.*exceed.*context (window|length|size)`),                                                         // omp：本地 OpenAI 兼容服务
	regexp.MustCompile(`(?i)context (window|length|size).*(exceeded|overflow|too small)`),                                                     // omp：本地服务
	regexp.MustCompile(`(?i)(prompt|input).*(too long|too large).*(context|n_ctx)`),                                                           // omp：llama.cpp 变体
	regexp.MustCompile(`(?i)requested tokens?.*(exceeds?|greater than).*(n_ctx|context)`),                                                     // omp：llama.cpp n_ctx
	regexp.MustCompile(`(?i)prompt filled the context window`),                                                                                // omp：Ollama
	regexp.MustCompile(`(?i)chat history exceeds the \d+-message limit`),                                                                      // omp：消息条数上限
}

var nonOverflowPattern = regexp.MustCompile(`(?i)^(Throttling error|Service unavailable):|rate limit|too many requests`)

// IsContextOverflow 认服务商说的“上下文超了”；限流、取消优先排除，不走普通原样重试。
func IsContextOverflow(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := err.Error()
	var status *StatusError
	if errors.As(err, &status) {
		if explicitRateLimit(status.Code, []byte(status.RawBody)) {
			return false
		}
		text = status.Body
		if status.RawBody != "" {
			text += "\n" + status.RawBody
		}
		if status.Code == 413 {
			text = status.Error() + "\n" + status.RawBody
		}
	}
	var stream *StreamError
	if errors.As(err, &stream) {
		if explicitRateLimit(0, stream.Data) {
			return false
		}
		// 先放 message：Bedrock 的限流前缀要在开头认；原始数据保留 code 里的 context_length_exceeded。
		text = string(stream.Data)
		var body struct {
			Error struct{ Message string }
		}
		if json.Unmarshal(stream.Data, &body) == nil {
			text = body.Error.Message + "\n" + text
		}
	}
	if nonOverflowPattern.MatchString(text) {
		return false
	}
	for _, p := range overflowPatterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// HTTP 和 SSE 都先看明确的限流标记，避免 too many tokens 被通用溢出规则抢走。
var rateLimitKind = regexp.MustCompile(`(?i)rate[_ -]?limit|throttl|too[_ -]many[_ -]requests`)

func explicitRateLimit(status int, data []byte) bool {
	if status == 429 {
		return true
	}
	var body struct {
		Error struct {
			Type   string
			Code   any
			Status any
		}
	}
	if json.Unmarshal(data, &body) != nil {
		return false
	}
	if rateLimitKind.MatchString(body.Error.Type) {
		return true
	}
	for _, value := range []any{body.Error.Code, body.Error.Status} {
		switch v := value.(type) {
		case float64:
			if v == 429 {
				return true
			}
		case string:
			if v == "429" || rateLimitKind.MatchString(v) {
				return true
			}
		}
	}
	return false
}
