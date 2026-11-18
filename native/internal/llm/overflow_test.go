package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// 两份上游规则里的服务商原文；每条独有规则都给一个样本，不凭状态码 400 笼统判。
func TestContextOverflow(t *testing.T) {
	for _, text := range []string{
		"prompt is too long: 213462 tokens > 200000 maximum",
		"Prompt too long", "Prompt exceeds max length", "request_too_large",
		"Input is too long for requested model",
		"Your input exceeds the context window of this model",
		"Requested token count exceeds the model's maximum context length of 131072 tokens",
		"Input length (265330) exceeds model's maximum context length (262144).",
		"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)",
		"This model's maximum prompt length is 131072 but the request contains 537812 tokens",
		"Please reduce the length of the messages or completion",
		"This endpoint's maximum context length is 8192 tokens. However, you requested about 10000 tokens",
		"Input length 10000 exceeds the maximum allowed input length of 8192 tokens.",
		"The input (10000 tokens) is longer than the model's context length (8192 tokens).",
		"prompt token count of 10000 exceeds the limit of 8192",
		"the request exceeds the available context size, try increasing it",
		"tokens to keep from the initial prompt is greater than the context length",
		"invalid params, context window exceeds limit",
		"Your request exceeded model token limit: 8192 (requested: 10000)",
		"Prompt contains 10000 tokens, too large for model with 8192 maximum context length",
		"Prompt has 10,000 tokens, but the configured context size is 8,192 tokens",
		"model_context_window_exceeded", "prompt too long; exceeded max context length by 200 tokens",
		"Range of input length should be [1, 8192]", "context_length_exceeded", "context length exceeded",
		"Too many tokens", "Token limit exceeded", "413: Request exceeds the maximum size",
		`github-copilot API error (413): {"message":"failed to parse request","code":""}`,
		"413 (no body)",
		"prompt (10000 tokens) + max tokens (1000) exceeds the context (8192)",
		"prompt (8192 tokens) leaves no room to answer in the context (8192)",
		"requested tokens exceed context size", "context size overflow",
		"input too large for n_ctx", "requested tokens greater than n_ctx",
		"prompt filled the context window", "chat history exceeds the 100-message limit",
	} {
		t.Run(text, func(t *testing.T) {
			if !IsContextOverflow(fmt.Errorf("网关：%w", &StatusError{Code: 400, Body: text})) {
				t.Fatalf("没有认出上下文溢出：%q", text)
			}
		})
	}
	for _, text := range []string{
		"Throttling error: Too many tokens, please wait before trying again.",
		"Service unavailable: Too many tokens", "rate limit: too many tokens", "too many requests: token limit exceeded",
		"invalid API key", "insufficient_quota", "content_filter", "permission denied", "model not found",
		"invalid_request_error", "unexpected EOF", "connection refused", "upstream timeout", "billing required",
		"400 Bad Request", "401 Unauthorized", "429 Too Many Requests", "500 Internal Server Error",
		"the response body mentions HTTP 413", "output max_tokens must be positive",
		"400 status code (no body)",
	} {
		if IsContextOverflow(errors.New(text)) {
			t.Errorf("不该当成上下文溢出：%q", text)
		}
	}
	for _, err := range []error{nil, context.Canceled, fmt.Errorf("context_length_exceeded: %w", context.DeadlineExceeded)} {
		if IsContextOverflow(err) {
			t.Errorf("空错误、取消不该压缩：%v", err)
		}
	}
}

func TestContextOverflowNotRetryable(t *testing.T) {
	err := fmt.Errorf("网关：%w", &StatusError{Code: 500, Header: http.Header{"X-Should-Retry": {"true"}}, Body: "prompt is too long"})
	if ok, _ := Retryable(err); ok || !IsContextOverflow(err) {
		t.Fatalf("上下文溢出优先于状态码和重试头：%v", err)
	}
	_, err = streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseLine(w, "data: {\"error\":{\"message\":\"太长了\",\"type\":\"api_error\",\"code\":\"context_length_exceeded\"}}\n\n")
	})
	if !IsContextOverflow(err) {
		t.Fatalf("流错误里的 code 也要认：%v", err)
	}
	_, err = streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseLine(w, "data: {\"error\":{\"message\":\"prompt is too long\",\"type\":\"api_error\"}}\n\n")
	})
	if ok, _ := Retryable(err); ok || !IsContextOverflow(err) {
		t.Fatalf("没有 code 的流错误也不原样重试：%v", err)
	}
	_, err = streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseLine(w, "data: {\"error\":{\"message\":\"Throttling error: Too many tokens, please wait before trying again.\",\"type\":\"api_error\"}}\n\n")
	})
	if ok, _ := Retryable(err); !ok || IsContextOverflow(err) {
		t.Fatalf("流里的 Bedrock 限流按普通重试，不压缩：%v", err)
	}
}

func TestContextOverflowHTTPCode(t *testing.T) {
	for _, status := range []int{400, 200} {
		for _, field := range []string{"code", "type"} {
			t.Run(fmt.Sprintf("%d/%s", status, field), func(t *testing.T) {
				_, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":{"message":"太长了",%q:"context_length_exceeded"}}`, field)
				})
				var se *StatusError
				if !errors.As(err, &se) || se.Body != "太长了" || !IsContextOverflow(err) {
					t.Fatalf("HTTP code/type 要保留，展示仍用 message：%v", err)
				}
				if ok, _ := Retryable(err); ok {
					t.Fatalf("上下文溢出不原样重试：%v", err)
				}
			})
		}
	}
}

func TestContextOverflowBodylessHTTP(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, "", false},
		{400, "400 status code (no body)", false},
		{413, "", true}, // pi 的通用 HTTP 413 规则照旧。
	} {
		t.Run(fmt.Sprintf("%d/%s", c.status, c.body), func(t *testing.T) {
			_, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				fmt.Fprint(w, c.body)
			})
			if err == nil || IsContextOverflow(err) != c.want {
				t.Fatalf("没有 provider 不能猜空正文 400 是溢出：%v，应该 %v", err, c.want)
			}
		})
	}
}

func TestContextOverflowRateLimitPriority(t *testing.T) {
	for _, transport := range []string{"HTTP", "SSE"} {
		for _, c := range []struct {
			name   string
			status int
			fields string
		}{
			{"状态429", 429, `"type":"invalid_request_error"`},
			{"类型限流", 400, `"type":"rate_limit_error","code":"context_length_exceeded"`},
			{"代码限流", 400, `"type":"invalid_request_error","code":"rate_limit_exceeded"`},
			{"数字状态限流", 400, `"type":"invalid_request_error","status":429`},
		} {
			t.Run(transport+"/"+c.name, func(t *testing.T) {
				_, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "7")
					body := fmt.Sprintf(`{"error":{"message":"too many tokens",%s}}`, c.fields)
					if transport == "SSE" {
						w.Header().Set("Content-Type", "text/event-stream")
						if c.status == 429 {
							body = `{"error":{"message":"too many tokens","code":429,"type":"invalid_request_error"}}`
						}
						sseLine(w, "data: "+body+"\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(c.status)
						fmt.Fprint(w, body)
					}
				})
				if IsContextOverflow(err) {
					t.Fatalf("明确限流不能压缩：%v", err)
				}
				if ok, wait := Retryable(err); err == nil || !ok || wait != 7*time.Second {
					t.Fatalf("限流按 A7 重试并保留 Retry-After：%v，重试 %v，等 %v", err, ok, wait)
				}
			})
		}
	}
}
