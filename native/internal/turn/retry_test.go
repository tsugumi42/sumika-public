package turn

// 规格 8.2 A7：模型请求出错以后有限地重试、等着重试时能停下；说了一半的不重复也不丢。对着写明的预期测
// （照 OpenCode session-runner.test.ts：:6202 次数有上限、退避在区间里，:5632 退避时被停下不再请求，:5836 流出了一半断掉）

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/llm"
)

// sseChunk 吐一块流式回复
func sseChunk(w http.ResponseWriter, content string) {
	o := map[string]any{"id": "golden", "object": "chat.completion.chunk", "created": 0, "model": "x",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}, "finish_reason": nil}}}
	j, _ := json.Marshal(o)
	fmt.Fprintf(w, "data: %s\n\n", j)
	w.(http.Flusher).Flush()
}

// halfThenDrop：吐出一段正文就断开连接（流到一半断了）
func halfThenDrop(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	sseChunk(w, text)
	panic(http.ErrAbortHandler)
}

// runRow 是 model_runs 的一行里这些测试要看的几样
type runRow struct {
	stage, err, rebuild string
	ttft                *int64
}

// runRows 是 model_runs 一行一行（按写进去的先后；没出错的 err、没标重建的 rebuild 是空）
func (h *harness) runRows() []runRow {
	rows, err := h.st.Trace.Query(`SELECT stage, coalesce(error, ''), coalesce(rebuild, ''), ttft_ms FROM model_runs ORDER BY rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		var r runRow
		rows.Scan(&r.stage, &r.err, &r.rebuild, &r.ttft)
		out = append(out, r)
	}
	return out
}

func (h *harness) runErrors() []string {
	var out []string
	for _, r := range h.runRows() {
		out = append(out, r.err)
	}
	return out
}

func (h *harness) postCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.posts
}

// 出错、限流以后重来：同一个请求再发一次，服务商要求等的照等（只拉长）；每次出错各记一行、记一行日志
func TestRetryTransient(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []string
	var at []time.Time
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		bodies, at = append(bodies, body), append(at, time.Now())
		mu.Unlock()
		switch n {
		case 1:
			http.Error(w, `{"error":{"message":"上游忙"}}`, http.StatusInternalServerError)
			return true
		case 2:
			w.Header().Set("Retry-After-Ms", "150")
			http.Error(w, "慢一点", http.StatusTooManyRequests)
			return true
		}
		return false
	}})
	r := h.send("你好")
	if r.Reply != "好的" || h.postCount() != 3 {
		t.Fatalf("两次出错以后第三次说完：%q，请求了 %d 次", r.Reply, h.postCount())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatal("重来的是同一个请求")
	}
	if gap := at[2].Sub(at[1]); gap < 150*time.Millisecond {
		t.Fatalf("服务商要求等 150 毫秒，只等了 %v", gap)
	}
	rows := h.runRows()
	if len(rows) != 3 || !strings.Contains(rows[0].err, "500: 上游忙") || !strings.Contains(rows[1].err, "429: 慢一点") ||
		!strings.Contains(rows[0].err, "以后再请求") || rows[2].err != "" {
		t.Fatalf("每次出错各记一行（带服务商说的原因、第几次、等多久），最后一行没出错：%+v", rows)
	}
	// 出错重来的记成 reply-retry（不进回复的前缀复用对比链），重建的标记在真正回完的那一行
	if rows[0].stage != "reply-retry" || rows[1].stage != "reply-retry" || rows[2].stage != "reply" ||
		rows[0].rebuild != "" || rows[2].rebuild != "fresh" {
		t.Fatalf("stage 和重建标记：%+v", rows)
	}
	if !h.logged("以后再请求") {
		t.Fatalf("重试要记日志：%v", h.logs)
	}
}

// 次数有上限：一直 503，第一次加 4 次重来，一共 5 次（SDK 自己的重试关掉了，不然是 15 次），报原来的错
func TestRetryGivesUp(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "挂了", http.StatusServiceUnavailable)
		return true
	}})
	o := <-h.hub.Send(h.c.CharacterID, "你好")
	if o.Err == nil || !strings.Contains(o.Err.Error(), "503: 挂了") || h.postCount() != 5 {
		t.Fatalf("应该请求 5 次以后报原来的错：%v，请求了 %d 次", o.Err, h.postCount())
	}
	rows := h.runRows()
	if len(rows) != 5 || rows[3].stage != "reply-retry" || !strings.Contains(rows[3].err, "第 4 次请求出错") ||
		rows[4].stage != "reply" || strings.Contains(rows[4].err, "以后再请求") {
		t.Fatalf("前四次记成要重来，最后一次是报出去的错：%+v", rows)
	}
}

// 不重来的几种：请求本身不对的 4xx、服务商说别重试的、要等太久的、内容过滤；服务商说要重试的 4xx 照它的
func TestRetryNotForThese(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		status int
		header map[string]string
		posts  int
	}{
		{"请求不对", 400, nil, 1},
		{"没授权", 401, nil, 1},
		{"服务商说别重试", 500, map[string]string{"X-Should-Retry": "false"}, 1},
		{"要等一小时", 429, map[string]string{"Retry-After": "3600"}, 1},
		{"服务商说要重试", 400, map[string]string{"X-Should-Retry": "true"}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
				if n > 1 {
					return false
				}
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				http.Error(w, "不行", c.status)
				return true
			}})
			<-h.hub.Send(h.c.CharacterID, "你好")
			if h.postCount() != c.posts {
				t.Fatalf("应该请求 %d 次，实际 %d 次", c.posts, h.postCount())
			}
		})
	}
	t.Run("内容过滤", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[{"content":[],"finish":"content_filter",` + usage + `}]`,
			intercept: func(int, string, http.ResponseWriter, *http.Request) bool { return false }})
		if o := <-h.hub.Send(h.c.CharacterID, "你好"); o.Err == nil || h.postCount() != 1 {
			t.Fatalf("内容过滤不重来：%v，请求了 %d 次", o.Err, h.postCount())
		}
	})
}

// 等着重来时按停止：马上停、不再请求，这一轮算被停下。用渠道的回合、出错那次先说了半句（审查查出：没发出去的那一行
// 原来带着出错那次的首字时间和开始时刻）
func TestRetryStopDuringBackoff(t *testing.T) {
	t.Parallel()
	retrying := make(chan struct{}, 1)
	h := newHarnessOpts(t, harnessOpts{
		intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
			halfThenDrop(w, "前半句")
			return true
		},
		mutate: func(c *Config) {
			c.Retry = RetryPolicy{Base: time.Hour, Cap: time.Hour} // 退避够长：停不下来就一直等，不用量多快
			logf := c.Logf
			c.Logf = func(f string, a ...any) {
				logf(f, a...)
				if strings.Contains(f, "以后再请求") {
					select {
					case retrying <- struct{}{}:
					default:
					}
				}
			}
		},
	})
	settled := h.watchSettled()
	out := h.inGroup("群里第一句", nil)
	<-retrying
	start := time.Now()
	h.hub.StopIn(h.c.CharacterID, groupConv)
	var o Outcome
	select {
	case o = <-out:
	case <-time.After(barrierLimit):
		t.Fatal("停止以后这一轮还没回完（还在等重试）")
	}
	if o.Result == nil || !o.Result.Stopped || h.postCount() != 1 {
		t.Fatalf("应该马上停下、只请求过 1 次：%+v，请求了 %d 次，用了 %v", o, h.postCount(), time.Since(start))
	}
	// 记下的：出错那次一行；没发出去的那一次照停在工具上那样记（审查查出：原来带着上一次的首字时间）
	rows := h.runRows()
	if len(rows) != 2 || rows[0].stage != "reply-retry" || rows[1].stage != "reply" || rows[1].ttft != nil {
		t.Fatalf("model_runs：%+v", rows)
	}
	if s := settled.waitN(t, 1); len(s) != 1 || s[0].Outcome != OutcomeStopped || len(s[0].Said) != 0 {
		t.Fatalf("回合定了应该是 stopped、出错那次的半句不交出去：%+v", s)
	}
}

// 主窗口说了半句断掉：推给界面的字不重来（不重复），半句照旧留着、存进库（不丢），报错
func TestRetryNotAfterShownText(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		if n == 1 {
			halfThenDrop(w, "前半句")
		}
		return false
	}})
	o := <-h.hub.Send(h.c.CharacterID, "你好")
	var n int
	h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind = 'character' AND text = '前半句'`).Scan(&n)
	if o.Err == nil || h.postCount() != 1 || n != 1 {
		t.Fatalf("说了半句断掉不重来、半句存一次：%v，请求了 %d 次，存了 %d 次", o.Err, h.postCount(), n)
	}
}

// 渠道的回合说了半句断掉：半句回合定了才交出去、谁也没看到，重来；交出去的只有重来的那次说的
func TestRetryChannelPartial(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		if n == 1 {
			halfThenDrop(w, "前半句")
		}
		return false
	}})
	r := wait(t, h.inGroup("群里第一句", nil))
	if !reflect.DeepEqual(r.Said, []Said{{0, "好的"}}) || h.postCount() != 2 {
		t.Fatalf("应该重来一次、交出去的只有重来那次的：%+v，请求了 %d 次", r.Said, h.postCount())
	}
}

// 结束了却没给 finish_reason（一个字没说）、一直没有新内容断开的，都当一时的，重来
// idleTimers 接管连接看门狗的定时器：到不到点由测试决定，和机器多忙无关。每次请求一个，只有 fire 才断
type idleTimers struct {
	mu     sync.Mutex
	timers []*idleTimer
}

type idleTimer struct {
	f      func()
	resets atomic.Int64
}

func (t *idleTimer) Reset(time.Duration) bool { t.resets.Add(1); return true }
func (t *idleTimer) Stop() bool               { return true }

func (s *idleTimers) after(_ time.Duration, f func()) llm.WatchTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := &idleTimer{f: f}
	s.timers = append(s.timers, x)
	return x
}

// last 是最新一次请求的看门狗（请求发出前就建好了）
func (s *idleTimers) last() *idleTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timers[len(s.timers)-1]
}

func TestRetryNoFinishAndIdle(t *testing.T) {
	t.Parallel()
	t.Run("没给结束原因", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
			if n == 1 {
				w.Header().Set("Content-Type", "text/event-stream")
				sseChunk(w, "")
				return true
			}
			return false
		}})
		if r := h.send("你好"); r.Reply != "好的" || h.postCount() != 2 {
			t.Fatalf("应该重来一次：%q，请求了 %d 次", r.Reply, h.postCount())
		}
	})
	t.Run("连响应头都没等到", func(t *testing.T) {
		t.Parallel()
		timers := &idleTimers{}
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, _ http.ResponseWriter, r *http.Request) bool {
			if n == 1 {
				timers.last().f() // 这一次卡住了：看门狗到点
				<-r.Context().Done()
				return true
			}
			return false
		}})
		h.client.IdleTimer = timers.after
		if r := h.send("你好"); r.Reply != "好的" || h.postCount() != 2 {
			t.Fatalf("卡住的那次断开以后应该重来：%q，请求了 %d 次", r.Reply, h.postCount())
		}
		if errs := h.runErrors(); !strings.Contains(errs[0], "没有收到新内容") {
			t.Fatalf("卡住的那次要记成没有新内容断开：%q", errs)
		}
	})
	t.Run("渠道的回合说到一半卡住", func(t *testing.T) {
		t.Parallel()
		timers := &idleTimers{}
		h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, r *http.Request) bool {
			if n == 1 {
				w.Header().Set("Content-Type", "text/event-stream")
				sseChunk(w, "前半句")
				timers.last().f() // 说到一半卡住了：看门狗到点
				<-r.Context().Done()
				return true
			}
			return false
		}})
		h.client.IdleTimer = timers.after
		r := wait(t, h.inGroup("群里第一句", nil))
		if !reflect.DeepEqual(r.Said, []Said{{0, "好的"}}) || h.postCount() != 2 {
			t.Fatalf("说到一半卡住的那次断开、重来，交出去的只有重来那次的：%+v，请求了 %d 次", r.Said, h.postCount())
		}
	})
	t.Run("慢、有心跳但没卡住", func(t *testing.T) {
		t.Parallel()
		timers := &idleTimers{}
		h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("Content-Type", "text/event-stream")
			timer := timers.last()
			for i := range 6 {
				before := timer.resets.Load()
				if i%2 == 0 {
					fmt.Fprint(w, ": keepalive\n\n")
					w.(http.Flusher).Flush()
				} else {
					sseChunk(w, "嗯")
				}
				// 客户端读到这一段、看门狗重新计时了才发下一段（这样下面数得清每段都算了动静）；等不到就往下走，断言会红
				for deadline := time.Now().Add(barrierLimit); timer.resets.Load() == before && time.Now().Before(deadline); {
					time.Sleep(time.Millisecond)
				}
			}
			fmt.Fprint(w, `data: {"id":"golden","object":"chat.completion.chunk","created":0,"model":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return true
		}})
		h.client.IdleTimer = timers.after
		if r := h.send("你好"); r.Reply != "嗯嗯嗯" || h.postCount() != 1 {
			t.Fatalf("一段段来、每段都有动静，不该断：%q，请求了 %d 次", r.Reply, h.postCount())
		}
		if got := timers.last().resets.Load(); got < 6 {
			t.Fatalf("心跳和内容每段都该让看门狗重新计时，只重置了 %d 次", got)
		}
	})
}

// 等多久：第 n 次重来前名义上等 min(1 秒 × 2^n, 8 秒)，上下浮动 20%；服务商要求等的只拉长；次数用完、要等太久、不值得的都不再请求
func TestRetryWait(t *testing.T) {
	t.Parallel()
	transient := errors.New("断了")
	for _, r := range []float64{0, 0.5, 0.999} {
		p := RetryPolicy{Rand: func() float64 { return r }}
		for n, nominal := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
			d, ok := p.wait(transient, n)
			if !ok || d < nominal*8/10 || d > nominal*12/10 {
				t.Fatalf("第 %d 次重来前应该等 %v 上下 20%%，实际 %v（%v）", n+1, nominal, d, ok)
			}
		}
		if _, ok := p.wait(transient, 4); ok {
			t.Fatal("第 5 次重来超过上限")
		}
	}
	p := RetryPolicy{Rand: func() float64 { return 0 }}
	limited := func(after string) error {
		return fmt.Errorf("x：%w", &llm.StatusError{Code: 429, Header: http.Header{"Retry-After": {after}}})
	}
	if d, ok := p.wait(limited("30"), 0); !ok || d != 30*time.Second {
		t.Fatalf("服务商要求等 30 秒：%v %v", d, ok)
	}
	if _, ok := p.wait(limited("3600"), 0); ok {
		t.Fatal("要等一小时的不等")
	}
	if _, ok := p.wait(&llm.StatusError{Code: 400}, 0); ok {
		t.Fatal("请求本身不对的不重来")
	}
}
