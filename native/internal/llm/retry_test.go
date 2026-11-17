package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// 规格 8.2 A7：哪些错误值得再请求一次（照 OpenCode isRetryable、pi provider-retry）
func TestRetryable(t *testing.T) {
	status := func(code int, h ...string) error {
		hd := http.Header{}
		for i := 0; i+1 < len(h); i += 2 {
			hd.Set(h[i], h[i+1])
		}
		return fmt.Errorf("Magpie 调用 x 出错：%w", &StatusError{Code: code, Header: hd})
	}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"被停下", context.Canceled, false},
		{"到了期限", fmt.Errorf("x：%w", context.DeadlineExceeded), false},
		{"内容过滤", &StopError{Reason: "content_filter"}, false},
		{"没给结束原因", &StopError{}, true},
		{"卡住断开", fmt.Errorf("x：%w", &IdleError{After: time.Minute}), true},
		{"500", status(500), true},
		{"502", status(502), true},
		{"429", status(429), true},
		{"408", status(408), true},
		{"409", status(409), true},
		{"400", status(400), false},
		{"401", status(401), false},
		{"404", status(404), false},
		{"500 说别重试", status(500, "X-Should-Retry", "false"), false},
		{"400 说要重试", status(400, "X-Should-Retry", "true"), true},
		{"连不上、认不出来的", errors.New("dial tcp: connection refused"), true},
	} {
		if got, _ := Retryable(c.err); got != c.want {
			t.Errorf("%s：应该是 %v", c.name, c.want)
		}
	}
}

// streamRaw：假服务商的回复体由 write 手写（出错、断开、心跳、卡住），idle 是看门狗
func streamRaw(t *testing.T, idle time.Duration, write func(w http.ResponseWriter, r *http.Request)) (Result, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(write))
	defer srv.Close()
	c := New(Magpie(srv.URL + "/v1"))
	c.Idle = idle
	return c.StreamChat(context.Background(), simpleReq("m"), nil)
}

// idleTimer 是手动的 Idle 看门狗：每次重新计时叫一声，到不到点由测试决定，和机器多忙无关
type idleTimer struct {
	mu     sync.Mutex
	f      func()
	resets chan struct{}
}

func newIdleTimer() *idleTimer { return &idleTimer{resets: make(chan struct{}, 256)} }

func (i *idleTimer) after(_ time.Duration, f func()) WatchTimer {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.f = f
	return i
}

// fire 让看门狗到点
func (i *idleTimer) fire() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.f()
}

func (i *idleTimer) Reset(time.Duration) bool {
	select {
	case i.resets <- struct{}{}:
	default:
	}
	return true
}

func (i *idleTimer) Stop() bool { return true }

// waitReset 等客户端读到东西、重新计时。心跳和字节不算动静的改法等不到，到时限就红
func (i *idleTimer) waitReset(t *testing.T) bool {
	t.Helper()
	select {
	case <-i.resets:
		return true
	case <-time.After(time.Minute):
		t.Error("一分钟了看门狗还没重新计时：读到的字节没算动静")
		return false
	}
}

// streamIdle 和 streamRaw 一样，看门狗换成手动的
func streamIdle(t *testing.T, i *idleTimer, write func(w http.ResponseWriter, r *http.Request)) (Result, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(write))
	defer srv.Close()
	c := New(Magpie(srv.URL + "/v1"))
	c.Idle = time.Hour
	c.IdleTimer = i.after
	return c.StreamChat(context.Background(), simpleReq("m"), nil)
}

func sseLine(w http.ResponseWriter, s string) {
	fmt.Fprint(w, s)
	w.(http.Flusher).Flush()
}

const (
	chunkHalf = `data: {"choices":[{"index":0,"delta":{"content":"前半句"}}]}` + "\n\n"
	chunkDone = `data: {"choices":[{"index":0,"delta":{"content":"完整的一句"},"finish_reason":"stop"}]}` + "\n\n"
)

// 审查查出：流里来的 error 事件（回复头已经发了，没有状态码）按它说的类别分，照 Magpie 的写法（gateway/chat.go:1438）
func TestStreamErrorEvents(t *testing.T) {
	for _, c := range []struct {
		name, payload string
		want          bool
	}{
		{"一般的上游出错", `{"message":"上游 502","type":"api_error"}`, true},
		{"限流", `{"message":"慢一点","type":"api_error","code":"rate_limit_exceeded"}`, true},
		{"上下文超长", `{"message":"太长了","type":"api_error","code":"context_length_exceeded"}`, false},
		{"拒绝", `{"message":"不行","type":"invalid_request_error","code":"content_filter"}`, false},
		{"请求太大", `{"message":"太大","type":"request_too_large"}`, false},
		{"额度用完", `{"message":"没钱了","type":"api_error","code":"insufficient_quota"}`, false},
		{"数字的 400", `{"message":"不对","code":400}`, false},
		{"数字的 429", `{"message":"慢一点","code":429}`, true},
	} {
		_, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			sseLine(w, `data: {"error":`+c.payload+"}\n\n")
		})
		if got, _ := Retryable(err); err == nil || got != c.want {
			t.Errorf("%s：%v，应该是 %v", c.name, err, c.want)
		}
	}
}

// 审查查出：回了 200 却是 JSON 错误，按错误报、带上原因、不重来；只有心跳就结束的照旧算没给结束原因（重来）
func TestNotStreamBody(t *testing.T) {
	_, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":{"message":"model not found: foo"}}`)
	})
	var se *StatusError
	if ok, _ := Retryable(err); !errors.As(err, &se) || se.Body != "model not found: foo" || ok {
		t.Fatalf("200 的 JSON 错误应该报出原因、不重来：%v", err)
	}
	_, err = streamRaw(t, time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseLine(w, ": keepalive\n\n")
	})
	var stop *StopError
	if ok, _ := Retryable(err); !errors.As(err, &stop) || stop.Reason != "" || !ok {
		t.Fatalf("只有心跳就结束的是没给结束原因、重来：%v", err)
	}
}

// 审查查出：结束原因收到了，回复就是完整的；之后断了、卡住都不算出错
func TestFinishThenCut(t *testing.T) {
	for name, after := range map[string]func(w http.ResponseWriter, r *http.Request, i *idleTimer){
		"断开": func(http.ResponseWriter, *http.Request, *idleTimer) { panic(http.ErrAbortHandler) },
		"卡住": func(_ http.ResponseWriter, r *http.Request, i *idleTimer) {
			// 客户端读到结束原因以后看门狗到点，还是算完整的回复
			if i.waitReset(t) {
				i.fire()
			}
			<-r.Context().Done()
		},
	} {
		i := newIdleTimer()
		res, err := streamIdle(t, i, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			sseLine(w, chunkDone)
			after(w, r, i)
		})
		if err != nil || res.Stop != "stop" || res.Text != "完整的一句" {
			t.Fatalf("%s：完整的回复不该算出错：%v %q %q", name, err, res.Stop, res.Text)
		}
	}
}

// 看门狗按连接上的字节算：只有心跳（注释）、慢慢来的都不断；流到一半一个字节都不来才断
func TestIdleCountsBytes(t *testing.T) {
	for name, gap := range map[string]string{"心跳": ": keepalive\n\n", "慢慢来的": `data: {"choices":[{"index":0,"delta":{"content":"嗯"}}]}` + "\n\n"} {
		i := newIdleTimer()
		res, err := streamIdle(t, i, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for range 8 {
				sseLine(w, gap)
				// 每一段都要让看门狗重新计时，才发下一段
				if !i.waitReset(t) {
					return
				}
			}
			sseLine(w, chunkDone)
		})
		if err != nil || res.Stop != "stop" {
			t.Fatalf("%s一段段来，每段都重新计时，不该断：%v", name, err)
		}
	}
	i := newIdleTimer()
	res, err := streamIdle(t, i, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseLine(w, chunkHalf)
		if i.waitReset(t) {
			i.fire()
		}
		<-r.Context().Done()
	})
	var ie *IdleError
	if ok, _ := Retryable(err); !errors.As(err, &ie) || !ok || res.Text != "前半句" {
		t.Fatalf("流到一半卡住应该断开、记成卡住、留着半句：%v %q", err, res.Text)
	}
}

// Retry-After：先看毫秒的，再看秒数或 HTTP 日期
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"9"}}, 1500 * time.Millisecond},
		{http.Header{"Retry-After": {"2.5"}}, 2500 * time.Millisecond},
		{http.Header{"Retry-After": {now.Add(10 * time.Second).Format(http.TimeFormat)}}, 10 * time.Second},
		{http.Header{"Retry-After": {now.Add(-time.Second).Format(http.TimeFormat)}}, 0},
		{http.Header{}, 0},
	} {
		if got := retryAfter(c.h, now); got != c.want {
			t.Errorf("%v：应该是 %v，实际 %v", c.h, c.want, got)
		}
	}
}
