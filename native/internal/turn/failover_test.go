package turn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/tools"
	"sumika-native/internal/turnbudget"
)

func requestModel(body string) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(body), &v) != nil {
		return ""
	}
	return v.Model
}

func takeOutcome(t *testing.T, o <-chan Outcome) Outcome {
	t.Helper()
	select {
	case r := <-o:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("这一轮 30 秒没回完")
		return Outcome{}
	}
}

func messageTexts(t *testing.T, h *harness, conv string) []string {
	t.Helper()
	rows, err := h.st.Events.Query(`SELECT COALESCE(text, '') FROM events WHERE conversation = ? AND kind = 'message' ORDER BY seq`, conv)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		out = append(out, text)
	}
	return out
}

func runContexts(t *testing.T, h *harness) []string {
	t.Helper()
	rows, err := h.st.Trace.Query(`SELECT model, COALESCE(attempt_id, ''), COALESCE(error_class, ''), COALESCE(context_json, '') FROM model_runs ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var model, attempt, class, ctx string
		if err := rows.Scan(&model, &attempt, &class, &ctx); err != nil {
			t.Fatal(err)
		}
		out = append(out, model+"|"+attempt+"|"+class+"|"+ctx)
	}
	return out
}

func reasoningThenDrop(w http.ResponseWriter, text, think string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload, err := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"reasoning_content": think, "content": text}}}})
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	panic(http.ErrAbortHandler)
}

func TestFailoverEmptyChainStaysOnSameModel(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "挂了", http.StatusServiceUnavailable)
		return true
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
	}})
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err == nil || strings.Contains(o.Err.Error(), "故障切换") || h.postCount() != 2 {
		t.Fatalf("空链该只重试原模型：%v，请求 %d", o.Err, h.postCount())
	}
}

func TestFailoverAfterSameModelRetries(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var models []string
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		if n <= 2 {
			http.Error(w, "挂了", http.StatusServiceUnavailable)
			return true
		}
		return false
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
	}})
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" || h.postCount() != 3 {
		t.Fatalf("同模型重试完才换：%v %+v 请求 %d", o.Err, o.Result, h.postCount())
	}
	primary := h.c.Characters[h.c.CharacterID].Model
	if len(models) != 3 || models[0] != primary || models[1] != primary || models[2] != "standby" {
		t.Fatalf("模型顺序：%v", models)
	}
	if !strings.Contains(strings.Join(runContexts(t, h), "\n"), `"reason":"server_error"`) {
		t.Fatalf("trace 没有切换原因：%v", runContexts(t, h))
	}
}

func TestFailoverSkipsRequestErrorsAndStop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"参数错", 400, "不行"},
		{"上下文太长", 413, "too long"},
		{"鉴权失败要换", 401, "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var models []string
			var mu sync.Mutex
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
				mu.Lock()
				models = append(models, requestModel(body))
				mu.Unlock()
				if n == 1 {
					http.Error(w, tc.body, tc.status)
					return true
				}
				return false
			}, mutate: func(c *Config) {
				c.Retry.Max = 1
				c.DefaultFallbackModels = []string{"standby"}
			}})
			o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
			if tc.status == 401 {
				if o.Err != nil || h.postCount() != 2 || len(models) != 2 || models[1] != "standby" {
					t.Fatalf("鉴权失败该立刻换、不重试原模型：%v 请求 %d %v", o.Err, h.postCount(), models)
				}
				return
			}
			if o.Err == nil || strings.Contains(o.Err.Error(), "故障切换") || h.postCount() != 1 || len(models) != 1 {
				t.Fatalf("请求本身的错不该换：%v 请求 %d %v", o.Err, h.postCount(), models)
			}
		})
	}
	t.Run("内容过滤", func(t *testing.T) {
		t.Parallel()
		h := newHarnessOpts(t, harnessOpts{replies: `[{"content":[],"finish":"content_filter",` + usage + `}]`, intercept: func(int, string, http.ResponseWriter, *http.Request) bool {
			return false
		}, mutate: func(c *Config) {
			c.DefaultFallbackModels = []string{"standby"}
		}})
		o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
		if o.Err == nil || h.postCount() != 1 || strings.Contains(o.Err.Error(), "standby") {
			t.Fatalf("内容过滤不换：%v 请求 %d", o.Err, h.postCount())
		}
	})
	t.Run("停下", func(t *testing.T) {
		t.Parallel()
		retrying := make(chan struct{}, 1)
		var models []string
		var mu sync.Mutex
		h := newHarnessOpts(t, harnessOpts{
			intercept: func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
				mu.Lock()
				models = append(models, requestModel(body))
				mu.Unlock()
				halfThenDrop(w, "前半句")
				return true
			},
			mutate: func(c *Config) {
				c.Retry = RetryPolicy{Base: 10 * time.Second, Cap: 10 * time.Second}
				c.DefaultFallbackModels = []string{"standby"}
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
		out := h.inGroup("群里第一句", nil)
		select {
		case <-retrying:
		case <-time.After(barrierLimit):
			t.Fatal("没进入同模型退避")
		}
		h.hub.StopIn(h.c.CharacterID, groupConv)
		o := takeOutcome(t, out)
		if o.Result == nil || !o.Result.Stopped || h.postCount() != 1 || len(models) != 1 {
			t.Fatalf("停下不该换模型：%+v 请求 %d %v", o, h.postCount(), models)
		}
	})
}

func TestFailoverRetractsPartialAndStripsReasoning(t *testing.T) {
	t.Parallel()
	const leaked = "不该留下的思考"
	var mu sync.Mutex
	var models []string
	var drafts []LocalDraft
	var noticed string
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		if n == 1 {
			reasoningThenDrop(w, "前半句", leaked)
			return true
		}
		return false
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
	}})
	conv := consoleConv(h.c.CharacterID)
	o := takeOutcome(t, h.hub.SendLocal(context.Background(), h.c.CharacterID, conv, "看半句", func(v LocalDraft) {
		mu.Lock()
		drafts = append(drafts, v)
		mu.Unlock()
		h.hub.lifecycle.mu.Lock()
		run := h.hub.lifecycle.runs[v.TurnID]
		if run != nil && run.snapshot.Process != nil {
			for _, step := range run.snapshot.Process.Steps {
				if strings.Contains(step.Target, "换成") {
					noticed = step.Target
				}
			}
		}
		h.hub.lifecycle.mu.Unlock()
	}))
	if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" || h.postCount() != 2 {
		t.Fatalf("半截后该换成功：%v %+v 请求 %d", o.Err, o.Result, h.postCount())
	}
	primary := h.c.Characters[h.c.CharacterID].Model
	if len(models) != 2 || models[0] != primary || models[1] != "standby" {
		t.Fatalf("不该同模型再打：%v", models)
	}
	reset := false
	for _, d := range drafts {
		if d.Reset {
			reset = true
		}
	}
	if !reset {
		t.Fatalf("没撤回草稿：%+v", drafts)
	}
	if noticed == "" || !strings.Contains(noticed, "standby") {
		t.Fatalf("本地过程没有切换说明：%q", noticed)
	}
	texts := messageTexts(t, h, conv)
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, "前半句") || strings.Contains(joined, leaked) || !strings.Contains(joined, "好的") {
		t.Fatalf("历史：%v", texts)
	}
	follow := takeOutcome(t, h.hub.Send(h.c.CharacterID, "下一句"))
	if follow.Err != nil {
		t.Fatal(follow.Err)
	}
	body := h.body(1)
	if strings.Contains(body, "前半句") || strings.Contains(body, leaked) || strings.Contains(body, "reasoning_content") {
		t.Fatalf("下一轮把失败草稿或思考带回去了：%s", body)
	}
	rows := runContexts(t, h)
	seenAttempt := map[string]bool{}
	failoverRow := false
	for _, row := range rows {
		parts := strings.SplitN(row, "|", 4)
		if len(parts) == 4 && parts[1] != "" {
			seenAttempt[parts[1]] = true
		}
		if strings.Contains(row, `"failover"`) && strings.Contains(row, `"reason":"network"`) && strings.Contains(row, primary) {
			failoverRow = true
		}
	}
	if len(seenAttempt) < 2 || !failoverRow {
		t.Fatalf("trace 没把两次尝试和切换原因记在一起：%v", rows)
	}
}

func TestFailoverDoesNotRepeatFinishedTools(t *testing.T) {
	t.Parallel()
	executed := 0
	var models []string
	var mu sync.Mutex
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("ls", `{"path":"."}`) + `,` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		if n == 2 || n == 3 {
			http.Error(w, "挂了", http.StatusServiceUnavailable)
			return true
		}
		return false
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
		c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
			executed++
			return tools.Result{Text: "工具只执行一次的结果"}, nil
		}
	}})
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "先查目录"))
	if o.Err != nil || executed != 1 || h.postCount() != 4 || len(models) != 4 || models[3] != "standby" {
		t.Fatalf("工具 %d 次，请求 %d，模型 %v，错 %v", executed, h.postCount(), models, o.Err)
	}
	if strings.Count(h.body(1), "工具只执行一次的结果") != 1 {
		t.Fatalf("换模型的请求没有原样带上一次工具结果：%s", h.body(1))
	}
}

func TestFailoverCooldownSkipsThenRetries(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	var models []string
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		if n <= 2 {
			http.Error(w, "挂了", http.StatusServiceUnavailable)
			return true
		}
		return false
	}, mutate: func(c *Config) {
		c.Now = func() time.Time { return now }
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
	}})
	primary := h.c.Characters[h.c.CharacterID].Model
	if o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "第一句")); o.Err != nil {
		t.Fatal(o.Err)
	}
	if o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "冷却中")); o.Err != nil {
		t.Fatal(o.Err)
	}
	if len(models) != 4 || models[3] != "standby" {
		t.Fatalf("冷却期不该再打原模型：%v", models)
	}
	now = now.Add(61 * time.Second)
	if o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "过期后再试")); o.Err != nil {
		t.Fatal(o.Err)
	}
	if len(models) != 5 || models[4] != primary {
		t.Fatalf("过期后该再试原模型：%v", models)
	}
}

func TestFailoverChainExhaustedNamesModels(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "挂了", http.StatusServiceUnavailable)
		return true
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
	}})
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	primary := h.c.Characters[h.c.CharacterID].Model
	if o.Err == nil || h.postCount() != 4 || !strings.Contains(o.Err.Error(), primary) || !strings.Contains(o.Err.Error(), "standby") || !strings.Contains(o.Err.Error(), "模型故障切换") {
		t.Fatalf("全链失败：%v 请求 %d", o.Err, h.postCount())
	}
}

func TestFailoverStopsWhenBudgetRunsOut(t *testing.T) {
	t.Parallel()
	var models []string
	var mu sync.Mutex
	h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		http.Error(w, "挂了", http.StatusServiceUnavailable)
		return true
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.BudgetConfig = config.DefaultBudget()
		c.BudgetConfig.MaxRequests = 2
		c.BudgetConfig.Local.MaxRequests = 2 // 主窗口私聊用 local 那组额度
		c.DefaultFallbackModels = []string{"standby"}
	}})
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err == nil || !errors.Is(o.Err, turnbudget.ErrExhausted) || h.postCount() != 2 {
		t.Fatalf("预算用完不该再派发：%v 请求 %d %v", o.Err, h.postCount(), models)
	}
	for _, model := range models {
		if model == "standby" {
			t.Fatalf("预算挡住以后仍打了备选：%v", models)
		}
	}
}

func TestFailoverRefusesTextOnlyBackupForImages(t *testing.T) {
	t.Parallel()
	var models []string
	var mu sync.Mutex
	h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		http.Error(w, "挂了", http.StatusServiceUnavailable)
		return true
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
		c.PrepareImages = func(context.Context, Input, llm.Model) (string, []llm.Image) {
			return "看图", []llm.Image{{DataURL: "data:image/png;base64,abc"}}
		}
	}})
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "看图"))
	if o.Err == nil || !strings.Contains(o.Err.Error(), "图片") || !strings.Contains(o.Err.Error(), "standby") || h.postCount() != 2 {
		t.Fatalf("不该丢掉图片继续：%v 请求 %d %v", o.Err, h.postCount(), models)
	}
	for _, model := range models {
		if model == "standby" {
			t.Fatalf("能力不够的备选仍被调用：%v", models)
		}
	}
}

func TestFailoverQQDoesNotAnnounceSwitch(t *testing.T) {
	t.Parallel()
	var models []string
	var mu sync.Mutex
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
		mu.Lock()
		models = append(models, requestModel(body))
		mu.Unlock()
		if n <= 2 {
			http.Error(w, "挂了", http.StatusServiceUnavailable)
			return true
		}
		return false
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
		c.DefaultFallbackModels = []string{"standby"}
		c.OnProcess = func(v ProcessStep) {
			if strings.Contains(v.Target, "换成") || strings.Contains(v.Tool, "模型切换") {
				t.Errorf("QQ 过程回调不该收到切换说明：%+v", v)
			}
		}
	}})
	o := takeOutcome(t, h.inGroup("群里一句", nil))
	if o.Err != nil || h.postCount() != 3 || len(models) != 3 || models[2] != "standby" {
		t.Fatalf("QQ 该先重试再换：%v 请求 %d %v", o.Err, h.postCount(), models)
	}
	for _, text := range messageTexts(t, h, groupConv) {
		if strings.Contains(text, "换成") || strings.Contains(text, "故障切换") {
			t.Fatalf("QQ 对外消息带了切换说明：%q", text)
		}
	}
}

func TestFailoverExplicitEmptyChainDisablesDefault(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{intercept: func(_ int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "挂了", http.StatusServiceUnavailable)
		return true
	}, mutate: func(c *Config) {
		c.Retry.Max = 1
	}})
	h.hub.Cfg.DefaultFallbackModels = []string{"standby"}
	h.hub.Cfg.FallbackModels = map[string][]string{h.c.CharacterID: {}}
	o := takeOutcome(t, h.hub.Send(h.c.CharacterID, "你好"))
	if o.Err == nil || strings.Contains(o.Err.Error(), "standby") || h.postCount() != 2 {
		t.Fatalf("显式空链不该用全局备选：%v 请求 %d", o.Err, h.postCount())
	}
}
