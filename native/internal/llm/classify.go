package llm

// 模型请求失败的原因类别（规格-运行页 第七节、ADR-311）：一次失败只落进一个类别，页面按类别数次数、看占比。
// 活的错误值用 ClassifyError（errors.Is / As 认哨兵和类型）；旧行只有 error 原文，用 ClassifyText 现算，不回写。
// 两个函数共用同一套判断，不许把原文直接当类别（"Request aborted"和"Request was aborted"要并成一类）。
//
// 词表以已有的判断为准（retry.go、overflow.go），不另造：取消、超时、上下文超长、内容过滤、限流、流中断、网络。
// budget_exhausted 要认 turnbudget.ErrExhausted，llm 不 import 它，由 modelcall 在调用本函数前先查；
// 原文里的 "budget_exhausted" 字样这里也认。parse_failed、loop_detected 由回合和后台任务写，这里只对旧原文认。

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// 失败原因类别。取值是稳定的 ASCII 键，界面和命令行原样显示
const (
	ClassCancelled       = "cancelled"
	ClassTimeout         = "timeout"
	ClassNetwork         = "network"
	ClassStreamError     = "stream_error"
	ClassRateLimited     = "rate_limited"
	ClassContextOverflow = "context_overflow"
	ClassContentFilter   = "content_filter"
	ClassBadRequest      = "bad_request"
	ClassModelUnknown    = "model_unknown"
	ClassAuth            = "auth"
	ClassServerError     = "server_error"
	ClassParseFailed     = "parse_failed"
	ClassLoopDetected    = "loop_detected"
	ClassBudgetExhausted = "budget_exhausted"
	ClassUnknown         = "unknown" // 认不出的：质量页单列并显示占比，占比大于一成标红
)

// ErrorClasses 是模型请求的全部类别（含 unknown），测试和界面用来核对"每个类别都有名字"
var ErrorClasses = []string{
	ClassCancelled, ClassTimeout, ClassNetwork, ClassStreamError, ClassRateLimited, ClassContextOverflow, ClassContentFilter,
	ClassBadRequest, ClassModelUnknown, ClassAuth, ClassServerError, ClassParseFailed, ClassLoopDetected, ClassBudgetExhausted, ClassUnknown,
}

// UnknownModelError：模型列表里没有这个模型（还没发请求就知道了）。带模型名，界面才说得出是哪一个。
type UnknownModelError struct{ ID string }

func (e *UnknownModelError) Error() string {
	return "模型列表里没有「" + e.ID + "」。请在设置 · 模型里换一个，或检查模型来源。"
}

// ClassifyError 给一个活的错误值分类；nil 返回空串。先看类型和哨兵，认不出的再按原文判断
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	// 取消和超时优先：被停下或到时限的请求，错误链里常同时带着别的错
	if errors.Is(err, context.Canceled) {
		return ClassCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTimeout
	}
	var um *UnknownModelError
	if errors.As(err, &um) {
		return ClassModelUnknown
	}
	var idle *IdleError
	if errors.As(err, &idle) {
		return ClassTimeout
	}
	if IsContextOverflow(err) {
		return ClassContextOverflow
	}
	var se *StopError
	if errors.As(err, &se) {
		if se.Reason == "" {
			return ClassStreamError
		}
		return classOfFinishReason(se.Reason)
	}
	var sse *StreamError
	if errors.As(err, &sse) {
		return classOfStreamEvent(sse)
	}
	var he *StatusError
	if errors.As(err, &he) {
		if explicitRateLimit(he.Code, []byte(he.RawBody)) {
			return ClassRateLimited
		}
		if c := classOfBody(he.Body + "\n" + he.RawBody); c != "" {
			return c
		}
		return classOfStatus(he.Code)
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return ClassTimeout
		}
		return ClassNetwork
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return ClassNetwork
	}
	return ClassifyText(err.Error())
}

func classOfStatus(code int) string {
	switch {
	case code == 401 || code == 403:
		return ClassAuth
	case code == 404:
		return ClassModelUnknown
	case code == 408 || code == 504:
		return ClassTimeout
	case code == 413:
		return ClassContextOverflow
	case code == 429:
		return ClassRateLimited
	case code >= 400 && code < 500:
		return ClassBadRequest
	case code >= 500:
		return ClassServerError
	}
	return ClassUnknown
}

// classOfFinishReason：服务商的结束原因表示出错时的类别。内容过滤、安全、策略类归 content_filter，其余算流出了问题
func classOfFinishReason(reason string) string {
	r := strings.ToLower(reason)
	for _, w := range []string{"content_filter", "safety", "policy", "refus", "prohibited", "blocked"} {
		if strings.Contains(r, w) {
			return ClassContentFilter
		}
	}
	return ClassStreamError
}

// classOfStreamEvent：流里来的 error 事件（回复头已经发出去，没有状态码）。按事件里的 code、type、message 判断，
// 认不出是什么的都算流出了问题（里面带着网络断开的字样也一样：外层先判，裸的 ECONNRESET 才是 network）
func classOfStreamEvent(e *StreamError) string {
	if explicitRateLimit(0, e.Data) {
		return ClassRateLimited
	}
	text := string(e.Data) + "\n" + e.Message
	if c := classOfBody(text); c != "" {
		return c
	}
	if reBadRequest.MatchString(text) {
		return ClassBadRequest
	}
	if strings.Contains(strings.ToLower(text), "overloaded") {
		return ClassServerError
	}
	return ClassStreamError
}

var (
	reModelUnknown  = regexp.MustCompile(`(?i)knows no model|unknown model|model[_ ]not[_ ]found|model[_ ]unavailable|no such model|model .{0,80} (does not exist|not found|is not available)`)
	reBadRequest    = regexp.MustCompile(`(?i)invalid[_ ]request|bad[_ ]request`)
	reContentFilter = regexp.MustCompile(`(?i)content[_ ]filter|content[_ ]policy|safety|brefus(ed|al)b|prohibited content`)
	reAuth          = regexp.MustCompile(`(?i)invalid[_ ]api[_ ]key|unauthorized|unauthenticated|authentication|permission[_ ]denied|\bforbidden\b|insufficient[_ ]quota`)
	reNetwork       = regexp.MustCompile(`(?i)econnreset|econnrefused|econnaborted|epipe|connection (reset|refused|lost|error|closed|aborted)|socket connection was closed|\beof\b|no such host|dial tcp|network is unreachable|tls handshake|broken pipe|read tcp|write tcp`)
	reTimeout       = regexp.MustCompile(`(?i)context deadline exceeded|timed? ?out|deadline exceeded|超过 ?\d+ ?(秒|分钟)没写完|没有收到新内容|没有收到模型的`)
	reCancelled     = regexp.MustCompile(`(?i)request (was )?aborted|operation was aborted|context canceled|被停下|已取消`)
	reStreamError   = regexp.MustCompile(`(?i)received error while streaming|connection lost mid-reply|upstream_stream_interrupted|error reading stream|stream ended without finish_reason|没有 finish_reason|stream (error|interrupted)`)
	reServerError   = regexp.MustCompile(`(?i)overloaded|internal_error|api_error|server error|service unavailable|bad gateway|upstream (error|unavailable)`)
	reParseFailed   = regexp.MustCompile(`输出不是合法|不是合法的识图结果|invalid json|unmarshal|parse (error|failed)`)
	reLoopDetected  = regexp.MustCompile(`思考打转`)
	reBudget        = regexp.MustCompile(`(?i)budget[_ ]exhausted|预算(用完|耗尽)`)
	reStatusPrefix  = regexp.MustCompile(`^\s*(\d{3})(?:\s|:|$)`)
)

// classOfBody 看错误正文里有没有说得很明确的原因（模型不认识、内容过滤、鉴权）；没有返回空串
func classOfBody(s string) string {
	switch { // 具体原因在前：OpenAI 兼容服务的模型不存在、密钥不对也都带 type=invalid_request_error
	case reModelUnknown.MatchString(s):
		return ClassModelUnknown
	case reContentFilter.MatchString(s):
		return ClassContentFilter
	case reAuth.MatchString(s):
		return ClassAuth
	case reBadRequest.MatchString(s):
		return ClassBadRequest
	}
	return ""
}

func classOfNetworkText(s string) string {
	if reNetwork.MatchString(s) {
		return ClassNetwork
	}
	return ""
}

// ClassifyText 只凭 error 原文分类：旧行（没有 error_class）读时现算用，也是 ClassifyError 认不出类型时的后备。
// 空串返回空串。顺序就是优先级，改顺序要同时改表驱动测试：
//  1. 取消、预算用完、本地解析和打转（原文里的明确字样）；
//  2. "N: {...}" 开头是 StatusError 的写法，按状态码分，正文里说得明确的原因（模型不认识等）优先于状态码；
//  3. 流里出的错：外层的"流出错"先判（里面带 ECONNRESET 也是 stream_error），裸的网络断开才是 network；
//  4. 超时、网络、服务端；认不出的是 unknown。
func ClassifyText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	switch {
	case reLoopDetected.MatchString(s):
		return ClassLoopDetected
	case reParseFailed.MatchString(s):
		return ClassParseFailed
	case reBudget.MatchString(s):
		return ClassBudgetExhausted
	case reCancelled.MatchString(s):
		return ClassCancelled
	}
	if m := reStatusPrefix.FindStringSubmatch(s); m != nil {
		code, _ := strconv.Atoi(m[1])
		if code >= 400 && code < 600 {
			if rateLimitKind.MatchString(s) || code == 429 {
				return ClassRateLimited
			}
			if IsContextOverflow(errors.New(s)) {
				return ClassContextOverflow
			}
			if c := classOfBody(s); c != "" {
				return c
			}
			return classOfStatus(code)
		}
	}
	if rateLimitKind.MatchString(s) {
		return ClassRateLimited
	}
	if IsContextOverflow(errors.New(s)) {
		return ClassContextOverflow
	}
	if c := classOfBody(s); c != "" {
		return c
	}
	if strings.Contains(s, "finish_reason: ") {
		return classOfFinishReason(s[strings.Index(s, "finish_reason: ")+len("finish_reason: "):])
	}
	// upstream_stream_interrupted 这类明确的"流中断"代码：不管里面带没带 connection reset，都算流出了问题
	if strings.Contains(strings.ToLower(s), "upstream_stream_interrupted") {
		return ClassStreamError
	}
	if reStreamError.MatchString(s) {
		return ClassStreamError
	}
	switch {
	case reTimeout.MatchString(s):
		return ClassTimeout
	case reNetwork.MatchString(s):
		return ClassNetwork
	case reServerError.MatchString(s):
		return ClassServerError
	}
	return ClassUnknown
}
