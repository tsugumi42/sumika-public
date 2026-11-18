package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// 旧行原文的样子来自真实 trace.db 的只读副本里的失败行，只留决定类别的字样，号码、地址、令牌、模型回复全部去掉或换成占位
func TestClassifyTextRealShapes(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"连接被重置（裸）", "Open Design: ECONNRESET: The socket connection was closed unexpectedly. For more information, pass `verbose: true` in the second argument to fetch() (3: you can retry your request)", ClassNetwork},
		{"只有 socket 关闭的句子", "The socket connection was closed unexpectedly. For more information, pass `verbose: true` in the second argument to fetch()", ClassNetwork},
		{"连不上", "Open Design: ECONNREFUSED: Unable to connect. Is the computer able to access the url? (2: you can retry your request)", ClassNetwork},
		{"通用连接错误", "Connection error.", ClassNetwork},
		{"请求被中止（短写法）", "Request aborted", ClassCancelled},
		{"请求被中止（长写法）", "Request was aborted", ClassCancelled},
		{"停在工具上以后的请求", "The operation was aborted.", ClassCancelled},
		{"守卫超时", "context deadline exceeded", ClassTimeout},
		{"超过 N 秒没写完", "超过 90 秒没写完", ClassTimeout},
		{"流里出错（外层是流出错，里面带断开）", `Magpie 调用 auto/model-x 出错：received error while streaming: {"message":"Open Design: ECONNRESET: The socket connection was closed unexpectedly."}`, ClassStreamError},
		{"流中途断开", "Open Design: connection lost mid-reply (ECONNRESET: The socket connection was closed unexpectedly.)", ClassStreamError},
		{"流中断代码", "[code=upstream_stream_interrupted] Error reading stream: read tcp 10.0.0.1:1->10.0.0.2:2: read: connection reset by peer", ClassStreamError},
		{"流里的 EOF", `Magpie 调用 x/y 出错：received error while streaming: {"message":"Provider: Post \"<url>\": EOF"}`, ClassStreamError},
		{"模型名不认识", `404: {"code":null,"message":"magpie knows no model \"group/x\"; did you mean a/b, c/d?"}`, ClassModelUnknown},
		{"模型名不认识（没带状态码）", `magpie knows no model "x"`, ClassModelUnknown},
		{"上游没路由，网关 504", `504: {"code":null,"message":"magpie held this request for 42s without routing it to any provider, so it was stopped rather than left waiting.","param":null,"type":"api_error"}`, ClassTimeout},
		{"上游内部错误 500", `500: {"code":null,"message":"Open Design: [code=internal_error] failed to evaluate membership entitlement","param":null,"type":"api_error"}`, ClassServerError},
		{"503 里带 billing 字样的上游超时，不是鉴权", `503: {"code":"workspace_billing_scope_unavailable","message":"resolve Billing Scope: Get \"<url>\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"}`, ClassServerError},
		{"服务端过载", "Our servers are currently overloaded. Please try again later.", ClassServerError},
		{"429", `429: {"message":"slow down"}`, ClassRateLimited},
		{"限流字样", "rate limit exceeded for this key", ClassRateLimited},
		{"401", `401: {"message":"bad key"}`, ClassAuth},
		{"403", `403: {"message":"no"}`, ClassAuth},
		{"400", `400: {"message":"messages must not be empty"}`, ClassBadRequest},
		{"413 上下文超长", "413: request entity too large", ClassContextOverflow},
		{"上下文超长字样", `400: {"error":{"code":"context_length_exceeded","message":"This model's maximum context length is exceeded"}}`, ClassContextOverflow},
		{"结束原因是内容过滤", "Provider finish_reason: content_filter", ClassContentFilter},
		{"结束原因不认识", "Provider finish_reason: weird", ClassStreamError},
		{"流结束没有结束原因", "Stream ended without finish_reason", ClassStreamError},
		{"输出不是合法 JSON", "输出不是合法 JSON：[{...略...}]", ClassParseFailed},
		{"输出不是合法的识图结果", "输出不是合法的识图结果（类别不在表里：X）：{...略...}", ClassParseFailed},
		{"思考打转", "思考打转：「ok」挨着出现 5 次", ClassLoopDetected},
		{"预算耗尽", "budget_exhausted", ClassBudgetExhausted},
		{"认不出的", "something nobody has seen before", ClassUnknown},
	}
	for _, c := range cases {
		if got := ClassifyText(c.text); got != c.want {
			t.Errorf("%s：%q 分成了 %q，应是 %q", c.name, c.text, got, c.want)
		}
	}
	if ClassifyText("") != "" || ClassifyText("  \n") != "" {
		t.Error("空串必须返回空串，不能分成 unknown")
	}
	// 两种 aborted 写法必须是同一类（规格第九节）
	if ClassifyText("Request aborted") != ClassifyText("Request was aborted") {
		t.Error("两种 aborted 写法没有并成一类")
	}
}

func TestClassifyErrorTyped(t *testing.T) {
	hdr := http.Header{}
	status := func(code int, body, raw string) error {
		return &StatusError{Code: code, Header: hdr, Body: body, RawBody: raw}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"取消", context.Canceled, ClassCancelled},
		{"取消被包了一层", fmt.Errorf("x：%w", context.Canceled), ClassCancelled},
		{"到时限", context.DeadlineExceeded, ClassTimeout},
		{"取消优先于别的错", errors.Join(status(500, "boom", ""), context.Canceled), ClassCancelled},
		{"一直没动静", &IdleError{After: 90 * time.Second}, ClassTimeout},
		{"429", status(429, "slow", ""), ClassRateLimited},
		{"正文里明说限流", status(400, "x", `{"error":{"type":"rate_limit_error"}}`), ClassRateLimited},
		{"401", status(401, "no", ""), ClassAuth},
		{"404", status(404, "no", ""), ClassModelUnknown},
		{"400 模型不认识", status(400, `magpie knows no model "x"`, ""), ClassModelUnknown},
		{"422", status(422, "bad", ""), ClassBadRequest},
		{"500", status(500, "bad", ""), ClassServerError},
		{"503", status(503, "bad", ""), ClassServerError},
		{"504", status(504, "held", ""), ClassTimeout},
		{"413", status(413, "big", ""), ClassContextOverflow},
		{"上下文超长（流里）", &StreamError{Message: "m", Data: []byte(`{"error":{"code":"context_length_exceeded"}}`)}, ClassContextOverflow},
		{"流里限流", &StreamError{Message: "m", Data: []byte(`{"error":{"type":"rate_limit_error"}}`)}, ClassRateLimited},
		{"流里一般错误", &StreamError{Message: "boom", Data: []byte(`{"error":{"message":"boom","type":"api_error"}}`)}, ClassStreamError},
		{"流里内容过滤", &StreamError{Message: "m", Data: []byte(`{"error":{"code":"content_filter"}}`)}, ClassContentFilter},
		{"流里模型不认识", &StreamError{Message: "m", Data: []byte(`{"error":{"message":"magpie knows no model"}}`)}, ClassModelUnknown},
		{"没有结束原因", &StopError{Source: "s", Model: "m"}, ClassStreamError},
		{"内容过滤结束", &StopError{Source: "s", Model: "m", Reason: "content_filter"}, ClassContentFilter},
		{"EOF", errors.New("Post \"<url>\": EOF"), ClassNetwork},
		{"认不出", errors.New("???"), ClassUnknown},
	}
	for _, c := range cases {
		if got := ClassifyError(c.err); got != c.want {
			t.Errorf("%s：分成了 %q，应是 %q", c.name, got, c.want)
		}
	}
}

func TestErrorClassesAreDistinctASCII(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ErrorClasses {
		if seen[c] {
			t.Errorf("类别 %q 重复", c)
		}
		seen[c] = true
		for _, r := range c {
			if r > 127 {
				t.Errorf("类别 %q 不是 ASCII 键", c)
			}
		}
	}
}
