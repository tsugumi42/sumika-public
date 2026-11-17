package turn

// 真实模型试验（规格-工具历史恢复和压缩第六节）：只在设了 SUMIKA_REAL_HISTORY_OUT（输出目录）时跑，经本机 Magpie 发真请求，
// 数据全在临时目录。多步编程题做到一半模拟重启，接着干，看模型知道改过什么；再压缩一次，量压缩前后的 token 和缓存命中。
// 模型用 SUMIKA_REAL_HISTORY_MODEL（默认 auto/deepseek-flash）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

type realCall struct {
	N             int    `json:"n"`
	Kind          string `json:"kind"`
	Phase         string `json:"phase"`
	Status        int    `json:"status"`
	BodyBytes     int    `json:"bodyBytes"`
	ToolMessages  int    `json:"toolMessages"`
	PromptTokens  int    `json:"promptTokens"`
	CachedTokens  int    `json:"cachedTokens"`
	CacheHitRatio string `json:"cacheHitRatio"`
	Completion    int    `json:"completionTokens"`
}

func TestRealToolHistory(t *testing.T) {
	out := os.Getenv("SUMIKA_REAL_HISTORY_OUT")
	if out == "" {
		t.Skip("设 SUMIKA_REAL_HISTORY_OUT 才跑真实模型试验")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("SUMIKA_REAL_HISTORY_MODEL")
	if model == "" {
		model = "auto/deepseek-flash"
	}
	var mu sync.Mutex
	var calls []realCall
	phase := "初始"
	setPhase := func(p string) { mu.Lock(); phase = p; mu.Unlock() }
	var executed int
	var inflight sync.WaitGroup // 每个请求记完用量才算完，ask 等它，不靠固定等待
	intercept := func(n int, body string, w http.ResponseWriter, r *http.Request) bool {
		var req map[string]any
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Errorf("请求体不是 JSON：%v", err)
			return false
		}
		inflight.Add(1)
		defer inflight.Done()
		req["model"] = model
		raw, _ := json.Marshal(req)
		up, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://127.0.0.1:3425"+r.URL.Path, bytes.NewReader(raw))
		up.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(up)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return true
		}
		defer resp.Body.Close()
		rec := realCall{N: n, Kind: "reply", Status: resp.StatusCode, BodyBytes: len(raw), ToolMessages: strings.Count(body, `"role":"tool"`)}
		if strings.Contains(body, compactMarker) {
			rec.Kind = "compact"
		}
		mu.Lock()
		rec.Phase = phase
		mu.Unlock()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		sc := bufio.NewReaderSize(resp.Body, 1<<20)
		for {
			line, err := sc.ReadBytes('\n')
			if len(line) > 0 {
				w.Write(line)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				s := strings.TrimSpace(string(line))
				if strings.HasPrefix(s, "data: {") {
					var chunk struct {
						Usage *struct {
							Prompt  int `json:"prompt_tokens"`
							Compl   int `json:"completion_tokens"`
							Details struct {
								Cached int `json:"cached_tokens"`
							} `json:"prompt_tokens_details"`
							Hit int `json:"prompt_cache_hit_tokens"`
						} `json:"usage"`
					}
					if json.Unmarshal([]byte(strings.TrimPrefix(s, "data: ")), &chunk) == nil && chunk.Usage != nil {
						rec.PromptTokens, rec.Completion = chunk.Usage.Prompt, chunk.Usage.Compl
						rec.CachedTokens = max(chunk.Usage.Details.Cached, chunk.Usage.Hit)
					}
				}
			}
			if err != nil {
				break
			}
		}
		if rec.PromptTokens > 0 {
			rec.CacheHitRatio = ratio(rec.CachedTokens, rec.PromptTokens)
		}
		mu.Lock()
		calls = append(calls, rec)
		mu.Unlock()
		return true
	}
	h := newHarnessOpts(t, harnessOpts{replies: replyList(textReply), intercept: intercept, mutate: func(c *Config) {
		workLimits(c)
		c.IdleAfter = 30 * time.Minute
		c.ExecTool = func(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
			executed++
			return tools.Execute(ctx, cwd, name, args)
		}
	}})
	approveWork(h)
	ask := func(root, text string) (string, *Result) {
		t.Helper()
		in := rootInput(h, text, root)
		done := make(chan Outcome, 1)
		go func() { done <- <-h.hub.Respond(in) }()
		select {
		case o := <-done:
			if o.Err != nil || o.Result == nil {
				t.Fatalf("%s 没回出来：%+v", root, o)
			}
			inflight.Wait() // 最后一次请求的用量在流读完以后才记，别漏
			return o.Result.Reply, o.Result
		case <-time.After(8 * time.Minute):
			t.Fatal("等了 8 分钟，真实模型没回")
		}
		return "", nil
	}
	conv := consoleConv(h.c.CharacterID)
	transcript := map[string]any{"model": model}

	setPhase("第一轮：做题")
	r1, _ := ask("real-root-1", "请在当前工作目录里做一个小任务：新建 wordfreq.py，里面有函数 count_words(text)，返回 [(词, 次数)] 按次数从多到少排，忽略大小写和标点；再新建 test_wordfreq.py，至少三个断言；最后用 python test_wordfreq.py 跑一遍，确认通过再告诉我。")
	transcript["round1"] = r1
	if _, err := os.Stat(filepath.Join(h.wd, "wordfreq.py")); err != nil {
		t.Fatalf("第一轮没建出 wordfreq.py：%v", err)
	}
	execAfter1 := executed

	setPhase("重启后：问改过什么")
	h.hub.RestartIn(h.c.CharacterID, conv)
	r2, _ := ask("real-root-2", "先不要动手，也不要调工具：只凭你的记忆回答，刚才你建过或改过哪些文件？每个文件一句话说做了什么。")
	transcript["round2AfterRestart"] = r2
	transcript["executedDuringRound2"] = executed - execAfter1
	if !strings.Contains(r2, "wordfreq.py") || !strings.Contains(r2, "test_wordfreq.py") {
		t.Errorf("重启后模型说不出改过哪些文件：%s", r2)
	}

	setPhase("重启后：接着干（不重启，看前缀复用）")
	r3, _ := ask("real-root-3", "接着干：给 count_words 加一个可选参数 top_n（默认 None，给了就只返回前 top_n 个），补一个断言，再跑一遍测试。")
	transcript["round3"] = r3
	setPhase("第四轮：同实例再问")
	r4, _ := ask("real-root-4", "测试现在是通过的吗？跑的是哪条命令？一句话。")
	transcript["round4"] = r4

	setPhase("压缩")
	h.setHard(1000000)
	h.hub.Cfg.Context.KeepTokens = 300
	c, err := h.hub.Compact(h.c.CharacterID, conv, "manual", 0)
	if err != nil || !c.OK {
		t.Fatalf("压缩没成：%+v %v", c, err)
	}
	var before, after int
	var summary, kind string
	h.st.Events.QueryRow(`SELECT tokens_before, tokens_after, summary, kind FROM context_epochs ORDER BY created_at DESC LIMIT 1`).Scan(&before, &after, &summary, &kind)
	transcript["compact"] = map[string]any{"kind": kind, "tokensBefore": before, "tokensAfter": after, "summary": summary}
	setPhase("压缩后：问改过什么")
	r5, _ := ask("real-root-5", "先不要调工具：只凭你的记忆回答，这个会话里你建过或改过哪些文件？跑过什么命令？")
	transcript["round5AfterCompact"] = r5
	if !strings.Contains(r5, "wordfreq.py") {
		t.Errorf("压缩后模型说不出改过哪些文件：%s", r5)
	}
	transcript["calls"] = calls
	transcript["executedTotal"] = executed
	b, _ := json.MarshalIndent(transcript, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "real-tool-history.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		t.Logf("#%d %-7s %-24s 请求体 %6d 字节 工具消息 %2d prompt %6d 缓存 %6d (%s) 输出 %d", c.N, c.Kind, c.Phase, c.BodyBytes, c.ToolMessages, c.PromptTokens, c.CachedTokens, c.CacheHitRatio, c.Completion)
	}
}

func ratio(a, b int) string {
	if b == 0 {
		return "-"
	}
	return strings.TrimRight(strings.TrimRight(jsonFloat(float64(a)*100/float64(b)), "0"), ".") + "%"
}

func jsonFloat(f float64) string { b, _ := json.Marshal(float64(int(f*10)) / 10); return string(b) }
