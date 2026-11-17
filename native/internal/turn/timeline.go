package turn

import (
	"sync"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/store"
)

// timeline 攒一轮的耗时（规格-回合耗时时间线）。回合里只往内存追加，结束才写库，计时点上不做 IO。
// 工具并发跑，所以带锁。方法对 nil 也能调：不走回合入口的调用方（测试直接跑工具）不用管它。
type timeline struct {
	mu    sync.Mutex
	now   func() time.Time
	spans []store.Span
}

func newTimeline(now func() time.Time) *timeline {
	if now == nil {
		now = time.Now
	}
	return &timeline{now: now}
}

func (t *timeline) add(kind, name string, start time.Time, d time.Duration, outcome string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.spans = append(t.spans, store.Span{Kind: kind, Name: name, StartedAt: start.UnixMilli(), Ms: max(d.Milliseconds(), 0), Outcome: outcome})
}

// begin 开始一段，返回结束它的函数；failed 为真记成失败。
func (t *timeline) begin(kind, name string) func(failed bool) {
	if t == nil {
		return func(bool) {}
	}
	start := t.now()
	return func(failed bool) {
		outcome := "ok"
		if failed {
			outcome = "failed"
		}
		t.add(kind, name, start, t.now().Sub(start), outcome)
	}
}

// model 把一次模型调用拆成排队、等首字节、等首段内容、流式四段。
// 没有首字节就不编后面几段：失败的请求只记一段 model.wait。
func (t *timeline) model(name string, start time.Time, res *llm.Result, err error) {
	if t == nil {
		return
	}
	elapsed := t.now().Sub(start)
	queue := elapsed - res.Total
	if res.Total <= 0 || queue < 0 {
		queue = 0
	}
	if queue > 0 {
		t.add("model.queue", name, start, queue, "ok")
	}
	sent := start.Add(queue)
	if res.FirstByte <= 0 {
		t.add("model.wait", name, sent, elapsed-queue, "failed")
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	t.add("model.wait", name, sent, res.FirstByte, "ok")
	if res.FirstContent <= 0 { // 响应头回来了但一段内容都没有（比如 429）
		t.add("model.first", name, sent.Add(res.FirstByte), res.Total-res.FirstByte, outcome)
		return
	}
	t.add("model.first", name, sent.Add(res.FirstByte), res.FirstContent-res.FirstByte, "ok")
	t.add("model.stream", name, sent.Add(res.FirstContent), res.Total-res.FirstContent, outcome)
}

// flush 写库。写不进去只记日志：这是观测数据，不能影响回合。
func (t *timeline) flush(st *store.Store, turnID string, logf func(string, ...any)) {
	if t == nil || st == nil {
		return
	}
	t.mu.Lock()
	spans := t.spans
	t.spans = nil
	t.mu.Unlock()
	if err := st.AddSpans(turnID, spans); err != nil && logf != nil {
		logf("记回合时间线出错：%v", err)
	}
}
