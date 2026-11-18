package turn

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

type spanRow struct {
	kind, name, outcome string
	ms                  int64
}

func (h *harness) spans(t *testing.T) []spanRow {
	t.Helper()
	rows, err := h.st.Trace.Query(`SELECT kind, name, outcome, ms FROM turn_spans ORDER BY turn_id, seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []spanRow
	for rows.Next() {
		var r spanRow
		if err := rows.Scan(&r.kind, &r.name, &r.outcome, &r.ms); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func spanOf(rows []spanRow, kind string) (spanRow, bool) {
	for _, r := range rows {
		if r.kind == kind {
			return r, true
		}
	}
	return spanRow{}, false
}

// 带工具的一轮：各环节都有，工具执行的时间算在 tool.exec，模型名和工具名记下了，正文没进表。
// 钟只在工具执行时往前走：其他环节都是 0，执行的时间算到别的段上一眼就看出来，机器再忙也不影响。
func TestTimelineCoversATurnWithTools(t *testing.T) {
	t.Parallel()
	const secret = "这段参数不该进表"
	const execTime = time.Minute
	base := time.Now()
	var ticks atomic.Int64
	h := newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"a","name":"ls","args":["{\"path\":\"` + secret + `\"}"]}],` + usage + `},{"content":["好了"],` + usage + `}]`,
		mutate: func(c *Config) {
			c.Now = func() time.Time { return base.Add(time.Duration(ticks.Load()) * execTime) }
			c.ExecTool = func(ctx context.Context, _, name string, args jsjson.Value) (tools.Result, error) {
				ticks.Add(1)
				return tools.Result{Text: "结果"}, nil
			}
		},
	})
	if r := h.send("看看目录"); r.Reply != "好了" {
		t.Fatalf("回复不对：%q", r.Reply)
	}
	rows := h.spans(t)
	for _, kind := range []string{"turn.total", "context.request", "model.wait", "model.first", "model.stream", "tool.prepare", "tool.queue", "tool.exec", "tool.save", "turn.save"} {
		if _, ok := spanOf(rows, kind); !ok {
			t.Errorf("没有 %s：%+v", kind, rows)
		}
	}
	exec, _ := spanOf(rows, "tool.exec")
	if exec.name != "ls" || exec.ms != execTime.Milliseconds() {
		t.Fatalf("工具执行应记工具名和实际耗时：%+v", exec)
	}
	if prep, _ := spanOf(rows, "tool.prepare"); prep.ms != 0 {
		t.Fatalf("执行的时间不该算进准备：%+v", prep)
	}
	total, _ := spanOf(rows, "turn.total")
	for _, r := range rows {
		if r.ms < 0 {
			t.Fatalf("耗时为负：%+v", r)
		}
		if r.kind != "turn.total" && r.ms > total.ms+1 {
			t.Fatalf("单段比整轮还长：%+v，整轮 %+v", r, total)
		}
		if strings.Contains(r.name, secret) {
			t.Fatalf("参数进了表：%+v", r)
		}
	}
}

// 模型出错重试：失败那次记 failed，等重试单独一段，最后成功那次是 ok。
func TestTimelineRecordsRetry(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, r *http.Request) bool {
		if n == 1 {
			w.Header().Set("Retry-After-Ms", "20")
			http.Error(w, "慢一点", http.StatusTooManyRequests)
			return true
		}
		return false
	}})
	if r := h.send("你好"); r.Reply != "好的" {
		t.Fatalf("回复不对：%q", r.Reply)
	}
	rows := h.spans(t)
	if _, ok := spanOf(rows, "model.retry"); !ok {
		t.Fatalf("没有 model.retry：%+v", rows)
	}
	failed, succeeded := false, false
	for _, r := range rows {
		failed = failed || (r.kind == "model.first" && r.outcome == "failed")
		succeeded = succeeded || (r.kind == "model.stream" && r.outcome == "ok")
	}
	if !failed || !succeeded {
		t.Fatalf("失败那次记 failed，最后成功那次记 ok：%+v", rows)
	}
}

// 真的等审批的时间算在准备，不算在执行。
func TestTimelineApprovalWaitIsNotExec(t *testing.T) {
	t.Parallel()
	tl := newTimeline(time.Now)
	end := tl.begin("tool.prepare", "write")
	time.Sleep(30 * time.Millisecond)
	end(false)
	tl.begin("tool.exec", "write")(false)
	if tl.spans[0].Ms < 20 || tl.spans[1].Ms >= 20 {
		t.Fatalf("准备和执行应分开算：%+v", tl.spans)
	}
}

// 没等到响应头时不编后面几段，只记一段失败的等待。
func TestTimelineModelFailureWithoutBytes(t *testing.T) {
	t.Parallel()
	tl := newTimeline(time.Now)
	tl.model("m", time.Now(), &llm.Result{}, context.DeadlineExceeded)
	if len(tl.spans) != 1 || tl.spans[0].Kind != "model.wait" || tl.spans[0].Outcome != "failed" {
		t.Fatalf("应只有一段失败的等待：%+v", tl.spans)
	}
}

// 表坏了也不影响回合：写不进去只记日志。
func TestTimelineWriteFailureDoesNotBreakTurn(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	if _, err := h.st.Trace.Exec(`DROP TABLE turn_spans`); err != nil {
		t.Fatal(err)
	}
	if r := h.send("你好"); r.Reply != "好的" {
		t.Fatalf("回复不对：%q", r.Reply)
	}
	found := false
	for _, l := range h.logs {
		found = found || strings.Contains(l, "记回合时间线出错")
	}
	if !found {
		t.Fatalf("写失败要记日志：%v", h.logs)
	}
}

// 超过保留期的清掉，新的不动。
func TestPruneSpansKeepsRecent(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	now := time.Now()
	old := now.AddDate(0, 0, -store.SpanRetentionDays-1).UnixMilli()
	if err := h.st.AddSpans("旧", []store.Span{{Kind: "turn.total", StartedAt: old, Ms: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddSpans("新", []store.Span{{Kind: "turn.total", StartedAt: now.UnixMilli(), Ms: 1}}); err != nil {
		t.Fatal(err)
	}
	n, err := h.st.PruneSpans(now.AddDate(0, 0, -store.SpanRetentionDays).UnixMilli())
	if err != nil || n != 1 {
		t.Fatalf("应清掉 1 行：%d %v", n, err)
	}
	var left int
	if err := h.st.Trace.QueryRow(`SELECT count(*) FROM turn_spans WHERE turn_id = '新'`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("新的不该动：%d %v", left, err)
	}
}
