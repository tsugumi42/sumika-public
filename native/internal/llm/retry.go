package llm

// 出错以后值不值得再请求一次（规格-Go后台 8.2 A7）：照 OpenCode 的 isRetryable（packages/ai/src/provider-error.ts:85）
// 和 pi 的 provider-retry（provider-retry.js:9）。不重试的：被停下、服务商明说的结束原因（内容过滤这类）、除了 408、409、429
// 以外的 4xx（请求本身不对，再发一次也一样）、x-should-retry 头说不要的、流里来的 error 事件说是请求不对或拒绝的。
// 流读到一半断了、结束了却没给 finish_reason、一直没有动静断开的、连不上的、5xx、流里说上游出错的、认不出来的，
// 都当一时的，重试。重试几次、等多久、说了一半的还能不能重来，由调用方管

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// StatusError：Body 留展示用的原因（截过）；RawBody 留至多 4096 字节的原文，分类时不丢 code、type。
type StatusError struct {
	Code    int
	Header  http.Header
	Body    string
	RawBody string
}

// Error 照 pi 的写法（utils/error-body.js：`${status}: ${body}`）
func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%d %s", e.Code, http.StatusText(e.Code))
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Body)
}

// IdleError：连响应头都没等到、或者流到一半卡住，超过 After 没有新内容，这边断开了。
// Kind 说是哪个看门狗：空是连接上没有任何字节（Client.Idle）；IdleFirstContent、IdleContentGap 是只有心跳没有内容（Client.FirstContent、ContentIdle）。
// 三种都是一时的错误，Retryable 认重试，ClassifyError 归 timeout（不单开一类：对用户都是"等了太久没动静"，质量页看 timeout 的占比就够，细分看错误原文）
type IdleError struct {
	After time.Duration
	Kind  string
}

// IdleError.Kind 的取值（空串是连接空闲）
const (
	IdleFirstContent = "first-content"
	IdleContentGap   = "content-gap"
)

func (e *IdleError) Error() string {
	switch e.Kind {
	case IdleFirstContent:
		return fmt.Sprintf("%v 没有收到模型的第一段内容，断开了（只有心跳或什么都没有，可能卡住了）", e.After)
	case IdleContentGap:
		return fmt.Sprintf("%v 没有收到模型的新内容，断开了（只有心跳，可能卡住了）", e.After)
	}
	return fmt.Sprintf("%v 没有收到新内容，断开了（可能卡住了）", e.After)
}

// Retryable 说这个错误值不值得再请求一次，以及服务商要求至少等多久（Retry-After；没给是 0）
func Retryable(err error) (bool, time.Duration) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	var local *localError
	if errors.As(err, &local) {
		return false, 0
	}
	if IsContextOverflow(err) {
		return false, 0
	}
	var se *StopError
	if errors.As(err, &se) {
		return se.Reason == "", 0
	}
	var sse *StreamError
	if errors.As(err, &sse) {
		// 流内报错也带着响应头，限流的 Retry-After 和 HTTP 错误走同一规则
		return streamErrorRetryable(sse.Data), retryAfter(sse.Header, time.Now())
	}
	var he *StatusError
	if errors.As(err, &he) {
		switch strings.ToLower(he.Header.Get("x-should-retry")) {
		case "true":
			return true, retryAfter(he.Header, time.Now())
		case "false":
			return false, 0
		}
		if c := he.Code; c == 408 || c == 409 || c == 429 || c >= 500 || explicitRateLimit(c, []byte(he.RawBody)) {
			return true, retryAfter(he.Header, time.Now())
		}
		return false, 0
	}
	return true, 0
}

// 流里来的 error 事件（回复头已经发出去了，没有状态码）：Magpie 写成 {"error":{"message","type","code"}}（gateway/chat.go:1438），
// 上游的错误类别放在 code（上下文超长是 context_length_exceeded，拒绝是 content_filter 或某个 policy），一般的上游出错 type 是
// api_error、没有 code。请求本身不对、拒绝、额度这类再发一次也一样，不重来；其余（一般的上游出错、限流、过载）重来
var permanentStreamError = []string{"context_length", "too_large", "content_filter", "policy", "refus", "invalid_request", "auth", "permission", "not_found", "quota", "billing"}

func streamErrorRetryable(data []byte) bool {
	if explicitRateLimit(0, data) {
		return true
	}
	var p struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &p) != nil {
		return true
	}
	var e struct {
		Type   string `json:"type"`
		Code   any    `json:"code"`
		Status any    `json:"status"`
	}
	if json.Unmarshal(p.Error, &e) != nil {
		return true
	}
	for _, v := range []any{e.Code, e.Status} {
		if n, ok := v.(float64); ok && n >= 400 && n < 500 {
			return n == 408 || n == 409 || n == 429
		}
	}
	code, _ := e.Code.(string)
	kind := strings.ToLower(e.Type + " " + code)
	for _, w := range permanentStreamError {
		if strings.Contains(kind, w) {
			return false
		}
	}
	return true
}

// retryAfter 照 OpenCode route/executor.ts:22：先看 retry-after-ms，再看 retry-after（秒数或者 HTTP 日期）
func retryAfter(h http.Header, now time.Time) time.Duration {
	if v, err := strconv.ParseFloat(h.Get("retry-after-ms"), 64); err == nil && v > 0 {
		return time.Duration(v * float64(time.Millisecond))
	}
	v := h.Get("retry-after")
	if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
