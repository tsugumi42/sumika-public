package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 内容空闲看门狗的测试：心跳不算进展，正文、思考、工具参数才算。
// 判断"会不会触发"的用假定时器（手动触发、数重置次数），不靠墙钟；
// 端到端的"心跳不断也能断开"用很小的真时限，只等结果出来，不 sleep。

const (
	hb      = ": keepalive\n\n"
	content = `{"choices":[{"index":0,"delta":{"content":"字"}}]}`
	finish  = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
)

type fakeTimer struct {
	mu      sync.Mutex
	f       func()
	resets  int
	stopped bool
}

func (t *fakeTimer) Reset(time.Duration) bool { t.mu.Lock(); t.resets++; t.mu.Unlock(); return true }
func (t *fakeTimer) Stop() bool               { t.mu.Lock(); t.stopped = true; t.mu.Unlock(); return true }
func (t *fakeTimer) fire()                    { t.f() }

type fakeClock struct {
	mu     sync.Mutex
	timers []*fakeTimer
	made   chan struct{} // 每建一个定时器叫一声
}

func newFakeClock() *fakeClock { return &fakeClock{made: make(chan struct{}, 8)} }

func (c *fakeClock) afterFunc(_ time.Duration, f func()) watchTimer {
	t := &fakeTimer{f: f}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	c.made <- struct{}{}
	return t
}

func (c *fakeClock) at(i int) *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timers[i]
}

func (c *fakeClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

func writePiece(w http.ResponseWriter, p string) {
	if strings.HasPrefix(p, ":") {
		fmt.Fprint(w, p)
	} else {
		fmt.Fprintf(w, "data: %s\n\n", p)
	}
}

// scripted 起一个按给定原文（含心跳）吐完就结束的假网关
func scripted(pieces ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range pieces {
			writePiece(w, p)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// stalled 吐完 pieces 以后给 sent 叫一声，然后只发心跳（forever 为真时每毫秒一次）或者什么都不发，等调用方断开
func stalled(sent chan<- struct{}, forever bool, pieces ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, p := range pieces {
			writePiece(w, p)
		}
		fl.Flush()
		if sent != nil {
			sent <- struct{}{}
		}
		if !forever {
			<-r.Context().Done()
			return
		}
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := fmt.Fprint(w, hb); err != nil {
					return
				}
				fl.Flush()
			}
		}
	}))
}

func run(t *testing.T, c *Client) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.StreamChat(ctx, simpleReq("m"), nil)
}

func wantIdle(t *testing.T, err error, kind string) {
	t.Helper()
	var ie *IdleError
	if !errors.As(err, &ie) || ie.Kind != kind {
		t.Fatalf("应该是 %q 的 IdleError，得到 %v", kind, err)
	}
	if ok, _ := Retryable(err); !ok {
		t.Fatalf("内容空闲断开应该算一时的错误、让调用方重试：%v", err)
	}
	if got := ClassifyError(err); got != ClassTimeout {
		t.Fatalf("质量分类应该是 timeout，得到 %q", got)
	}
	// 旧行只有原文时也要认出来
	if got := ClassifyText(err.Error()); got != ClassTimeout {
		t.Fatalf("原文分类应该是 timeout，得到 %q：%s", got, err)
	}
}

// 只有心跳、一个字不给：真时限很小，心跳不断也要在首个内容时限断开（旧代码心跳让连接一直活着，永远等下去）
func TestContentIdleHeartbeatOnlyTriggers(t *testing.T) {
	srv := stalled(nil, true)
	defer srv.Close()
	c := New(Magpie(srv.URL + "/v1"))
	c.FirstContent, c.ContentIdle = 30*time.Millisecond, time.Hour
	_, err := run(t, c)
	wantIdle(t, err, IdleFirstContent)
}

// 先发一段内容、之后只有心跳：按内容间隔断开，已收到的字留着
func TestContentIdleGapAfterContentTriggers(t *testing.T) {
	srv := stalled(nil, true, content)
	defer srv.Close()
	c := New(Magpie(srv.URL + "/v1"))
	c.FirstContent, c.ContentIdle = time.Hour, 30*time.Millisecond
	res, err := run(t, c)
	wantIdle(t, err, IdleContentGap)
	if res.Text != "字" {
		t.Fatalf("断开前收到的字应该留着：%q", res.Text)
	}
}

// 用假定时器手动触发：心跳之后触发首个内容时限；内容之后触发间隔时限
func TestContentIdleFakeTimerFire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces []string
		kind   string
		index  int
	}{
		{"只有心跳", []string{hb, hb}, IdleFirstContent, 0},
		{"内容后只有心跳", []string{content, hb, hb}, IdleContentGap, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := make(chan struct{}, 1)
			srv := stalled(sent, false, tc.pieces...)
			defer srv.Close()
			clk := newFakeClock()
			c := New(Magpie(srv.URL + "/v1"))
			c.afterFunc = clk.afterFunc
			go func() {
				<-sent // 服务端已经把这些吐完、在等：这时候到点
				// 内容后的间隔定时器在客户端读到内容时才建，等它出现再触发
				for clk.count() <= tc.index {
					<-clk.made
				}
				clk.at(tc.index).fire()
			}()
			_, err := run(t, c)
			wantIdle(t, err, tc.kind)
		})
	}
}

// 内容持续到来：每段内容重置间隔定时器，心跳不重置，全程不触发
func TestContentIdleProgressKeepsAlive(t *testing.T) {
	srv := scripted(hb, content, hb, hb, content, hb, content, hb, finish)
	defer srv.Close()
	clk := newFakeClock()
	c := New(Magpie(srv.URL + "/v1"))
	c.afterFunc = clk.afterFunc
	res, err := run(t, c)
	if err != nil || res.Text != "字字字" {
		t.Fatalf("内容持续到来不该断开：%q %v", res.Text, err)
	}
	if clk.count() != 2 {
		t.Fatalf("应该一个首个内容定时器加一个间隔定时器，得到 %d 个", clk.count())
	}
	if !clk.at(0).stopped || clk.at(0).resets != 0 {
		t.Fatalf("首个内容定时器收到第一段内容就该停：%+v", clk.at(0))
	}
	// 第二、第三段内容各重置一次；五个心跳一次都不重置
	if got := clk.at(1).resets; got != 2 {
		t.Fatalf("间隔定时器应该只被两段内容重置，得到 %d 次", got)
	}
	if !clk.at(1).stopped {
		t.Fatal("流读完以后看门狗要停")
	}
}

// 思考、工具调用参数、结构化思考都算进展；心跳、空增量、只有用量的块不算
func TestContentIdleWhatCountsAsProgress(t *testing.T) {
	progress := map[string]string{
		"思考流":   `{"choices":[{"index":0,"delta":{"reasoning_content":"想"}}]}`,
		"工具参数":  `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
		"结构化思考": `{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"想","index":0}]}}]}`,
	}
	for name, p := range progress {
		t.Run(name, func(t *testing.T) {
			srv := scripted(p, p, finish)
			defer srv.Close()
			clk := newFakeClock()
			c := New(Magpie(srv.URL + "/v1"))
			c.afterFunc = clk.afterFunc
			run(t, c)
			if clk.count() != 2 || clk.at(1).resets != 1 {
				t.Fatalf("两块%s应该建了间隔定时器并重置一次：定时器 %d 个", name, clk.count())
			}
		})
	}
	t.Run("不算进展的", func(t *testing.T) {
		srv := scripted(hb, `{"choices":[{"index":0,"delta":{}}]}`, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0}}`, `{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text"}]}}]}`, finish)
		defer srv.Close()
		clk := newFakeClock()
		c := New(Magpie(srv.URL + "/v1"))
		c.afterFunc = clk.afterFunc
		run(t, c)
		if clk.count() != 1 {
			t.Fatalf("没有内容就不该建间隔定时器：%d 个", clk.count())
		}
		if clk.at(0).resets != 0 {
			t.Fatalf("首个内容定时器不该被重置：%d", clk.at(0).resets)
		}
	})
}

// 时限设成 0：不建定时器，只有心跳也不会被它断开（有 Idle 等别的限制）
func TestContentIdleZeroDisabled(t *testing.T) {
	srv := scripted(hb, hb, content, hb, finish)
	defer srv.Close()
	clk := newFakeClock()
	c := New(Magpie(srv.URL + "/v1"))
	c.FirstContent, c.ContentIdle = 0, 0
	c.afterFunc = clk.afterFunc
	res, err := run(t, c)
	if err != nil || res.Text != "字" {
		t.Fatalf("不管时限时应该正常读完：%q %v", res.Text, err)
	}
	if clk.count() != 0 {
		t.Fatalf("时限 0 不该建定时器：%d", clk.count())
	}
	// 只关首个、留间隔：内容来了以后才建
	clk = newFakeClock()
	c.ContentIdle = time.Hour
	c.afterFunc = clk.afterFunc
	if _, err := run(t, c); err != nil || clk.count() != 1 {
		t.Fatalf("只开间隔时，内容来了才建一个：%d %v", clk.count(), err)
	}
}

// 默认值是 180 秒
func TestContentIdleDefaults(t *testing.T) {
	c := New(Magpie("http://x/v1"))
	if c.FirstContent != 180*time.Second || c.ContentIdle != 180*time.Second {
		t.Fatalf("默认应该是 180 秒：%v %v", c.FirstContent, c.ContentIdle)
	}
}
