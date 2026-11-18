package bench

// omp 壳（规格-表二执行器 第二节）：执行器直接用 omp --mode rpc 驱动。配置目录用 PI_CODING_AGENT_DIR 指到这一次的临时目录，
// 里面只写一个 magpie 来源（地址是记账代理）和一个模型，不碰主人的 ~/.omp。rpc v1 是一行一个 JSON：先来 ready；
// prompt 带 id，事件流过来，最后一定有一帧同 id 的 prompt_result（completed / aborted / error）；停在第 n 个工具之后发 abort。
// 协议照 @oh-my-pi/pi-coding-agent 18.13.1 的 src/modes/rpc/rpc-types.ts，事件字段照 2026-10-27 冒烟的输出

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OmpOptions 是起 omp 的设置
type OmpOptions struct {
	Bin      string    // omp 的路径；空的用 PATH 里的
	AgentDir string    // 这一次的配置目录（会话记录也在这里）
	Stderr   io.Writer // omp 的 stderr 写到哪
	Interval time.Duration
}

// ompFlags 关掉 omp 自己的技能、规则、上下文文件、扩展、LSP、起标题（三个壳只比内核，不比外挂）
var ompFlags = []string{"--no-skills", "--no-rules", "--no-context-files", "--no-extensions", "--no-lsp", "--no-title"}

// ompModels 写 models.yml：照 Magpie 给 omp 写的那份（openai-completions、x-session-affinity），地址换成记账代理
func ompModels(gateway, model string) string {
	return fmt.Sprintf(`providers:
  magpie:
    api: openai-completions
    auth: none
    baseUrl: %s
    compat:
      promptCacheSessionHeader: x-session-affinity
    headers:
      User-Agent: omp
    models:
      - contextWindow: 1048000
        id: %s
        input:
          - text
        maxTokens: 65536
        name: %s
        reasoning: true
        thinking:
          efforts:
            - low
            - high
            - max
          mode: effort
`, gateway, model, model)
}

// ompFrame 是 omp 吐出来的一行（只取用得到的字段）
type ompFrame struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Success    *bool           `json:"success"`
	Error      json.RawMessage `json:"error"`
	Status     string          `json:"status"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"`
	IsError    bool            `json:"isError"`
	Message    *ompMessage     `json:"message"`
}

type ompMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		Input           int64 `json:"input"`
		Output          int64 `json:"output"`
		CacheRead       int64 `json:"cacheRead"`
		CacheWrite      int64 `json:"cacheWrite"`
		ReasoningTokens int64 `json:"reasoningTokens"`
	} `json:"usage"`
	TTFT         float64 `json:"ttft"`
	Duration     float64 `json:"duration"`
	StopReason   string  `json:"stopReason"`
	ErrorMessage string  `json:"errorMessage"`
}

// errText 把 response / prompt_result 的 error（字符串或 {message}）取成一句
func errText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Message != "" {
		return o.Message
	}
	return string(raw)
}

// resultText 取工具结果里的文字（{"content":[{"type":"text","text":…}]}）
func resultText(raw json.RawMessage) string {
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func preview(s string) string {
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

// RunOmp 用 omp 跑一道题。返回的 error 只在壳本身没起来时非空（她做错不算）；Result.Error 记壳中途出的问题
func RunOmp(ctx context.Context, t Task, o OmpOptions) (Result, Usage, error) {
	res := Result{Shell: "omp"}
	if len(t.Seed) > 0 || t.KeepTokens > 0 {
		return res, Usage{}, fmt.Errorf("omp 壳不支持预置历史和改压缩保留量（B4 另想办法）")
	}
	// 配置目录要绝对路径：omp 的当前目录是工作目录，相对的 PI_CODING_AGENT_DIR 会按它解析、找不到写好的配置
	var err error
	if o.AgentDir, err = filepath.Abs(o.AgentDir); err != nil {
		return res, Usage{}, err
	}
	if err := os.MkdirAll(o.AgentDir, 0o755); err != nil {
		return res, Usage{}, err
	}
	if err := os.WriteFile(filepath.Join(o.AgentDir, "models.yml"), []byte(ompModels(t.Gateway, t.Model)), 0o644); err != nil {
		return res, Usage{}, err
	}
	role := fmt.Sprintf("modelRoles:\n  default: magpie/%s:%s\n", t.Model, t.Thinking)
	if err := os.WriteFile(filepath.Join(o.AgentDir, "config.yml"), []byte(role), 0o644); err != nil {
		return res, Usage{}, err
	}
	bin := o.Bin
	if bin == "" {
		bin = "omp"
	}
	cctx, kill := context.WithCancel(ctx)
	defer kill()
	args := append([]string{"--mode", "rpc", "--model", "magpie/" + t.Model, "--thinking", t.Thinking}, ompFlags...)
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Dir = t.Workdir
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+o.AgentDir)
	cmd.Stderr = o.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return res, Usage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, Usage{}, err
	}
	job, err := StartInJob(cmd, o.Interval)
	if err != nil {
		return res, Usage{}, err
	}
	frames := make(chan ompFrame, 64)
	go func() {
		defer close(frames)
		rd := bufio.NewReaderSize(stdout, 1<<20) // 一行可能很长（工具结果），不用 Scanner（默认 64 KB 就报错）
		for {
			line, err := rd.ReadBytes('\n')
			if len(line) > 0 {
				var f ompFrame
				if json.Unmarshal(line, &f) == nil {
					frames <- f
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var wmu sync.Mutex
	send := func(v any) error {
		b, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	finish := func(e string) (Result, Usage, error) {
		res.Error = e
		stdin.Close()
		select { // 关了输入等它自己退；不退就杀（Job 关掉时连子孙一起清）
		case <-drain(frames):
		case <-time.After(10 * time.Second):
			kill()
		}
		u, _ := job.Wait()
		return res, u, nil
	}
	if e := waitReady(frames, 60*time.Second); e != "" {
		return finish(e)
	}
	// 只要用得到的事件：默认每个 message_update 都带整条消息的累积快照（rpc-types.ts 的 messageUpdates 默认 "full"），
	// 输出量随字数平方涨，算进 omp 的 CPU 和耗时就不公平（Go、Bun 壳不往外吐）。response、prompt_result 不是会话事件，不受过滤
	if e := ompFilter(frames, send); e != "" {
		return finish(e)
	}
	turn := time.Duration(t.TurnSeconds) * time.Second
	if turn <= 0 {
		turn = 600 * time.Second
	}
	for i, st := range t.Steps {
		if st.Compact {
			sr, e := ompCompact(frames, send, fmt.Sprintf("c%d", i))
			res.Steps = append(res.Steps, sr)
			if e != "" {
				return finish(e)
			}
			continue
		}
		sr, e := ompSay(frames, send, fmt.Sprintf("s%d", i), st, turn)
		res.Steps = append(res.Steps, sr)
		if e != "" {
			return finish(e)
		}
	}
	return finish("")
}

// drain 读完剩下的帧（omp 退出时 stdout 关掉、frames 关掉），读完了关返回的通道
func drain(frames <-chan ompFrame) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for range frames {
		}
		close(done)
	}()
	return done
}

func waitReady(frames <-chan ompFrame, limit time.Duration) string {
	timeout := time.After(limit)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return "omp 没起来（还没 ready 就退出了）"
			}
			if f.Type == "ready" {
				return ""
			}
		case <-timeout:
			return "omp 没起来（等 ready 超时）"
		}
	}
}

// ompEvents 是驱动读的会话事件，别的都不要（set_event_filter，rpc-session-events.ts 的 setFilter）
var ompEvents = []string{"message_end", "tool_execution_start", "tool_execution_end"}

func ompFilter(frames <-chan ompFrame, send func(any) error) string {
	if err := send(map[string]any{"id": "f0", "type": "set_event_filter", "events": ompEvents, "messageUpdates": "delta"}); err != nil {
		return "发 set_event_filter：" + err.Error()
	}
	for f := range frames {
		if f.Type == "response" && f.ID == "f0" {
			if f.Success == nil || !*f.Success {
				return "set_event_filter 没成：" + errText(f.Error)
			}
			return ""
		}
	}
	return "omp 中途退出（设事件过滤时）"
}

// ompCompact 手动压缩一次：compact 的 response 回来时已经压完
func ompCompact(frames <-chan ompFrame, send func(any) error, id string) (StepResult, string) {
	start := time.Now()
	sr := StepResult{Compact: &CompactState{}}
	if err := send(map[string]any{"id": id, "type": "compact"}); err != nil {
		return sr, "发 compact：" + err.Error()
	}
	for f := range frames {
		if f.Type == "response" && f.ID == id {
			sr.Ms = time.Since(start).Milliseconds()
			sr.Compact.OK = f.Success != nil && *f.Success
			sr.Compact.Error = errText(f.Error)
			return sr, ""
		}
	}
	return sr, "omp 中途退出（压缩时）"
}

// ompSay 说一句话，等同 id 的 prompt_result；第 n 个工具结束后、或者到点了发 abort
func ompSay(frames <-chan ompFrame, send func(any) error, id string, st Step, turn time.Duration) (StepResult, string) {
	start := time.Now()
	var sr StepResult
	sr.Tools, sr.Calls = []ToolUse{}, []Call{}
	if err := send(map[string]any{"id": id, "type": "prompt", "message": st.Say}); err != nil {
		return sr, "发 prompt：" + err.Error()
	}
	var mu sync.Mutex // 到点的计时器在别的协程里 abort
	stopped, timedOut := false, false
	abort := func(byTimer bool) {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		stopped, timedOut = true, byTimer
		send(map[string]any{"type": "abort"})
	}
	timer := time.AfterFunc(turn, func() { abort(true) })
	defer timer.Stop()
	var during *time.Timer // 第 n 个工具开始以后过一会儿按停止（工具正在跑时停）
	duringID := ""
	done := func(e string) (StepResult, string) {
		timer.Stop()
		if during != nil {
			during.Stop() // 这一句已经说完、还没到点的停止不留给下一句
		}
		mu.Lock()
		sr.Stopped, sr.TimedOut = stopped, timedOut
		if st.StopAfterTool > 0 || st.StopDuringTool > 0 {
			triggered := stopped && !timedOut
			sr.StopTriggered = &triggered
		}
		mu.Unlock()
		sr.Ms = time.Since(start).Milliseconds()
		return sr, e
	}
	args := map[string]string{}
	counted := map[string]bool{} // 算进停止计数的（StopMatch 对得上的）
	started, ended := 0, 0       // omp 不审批：开始了的都是真执行的
	for f := range frames {
		switch f.Type {
		case "response":
			if f.ID == id && f.Success != nil && !*f.Success {
				sr.Error = errText(f.Error)
				return done("")
			}
		case "tool_execution_start":
			args[f.ToolCallID] = string(f.Args)
			if !st.Counts(f.ToolName, string(f.Args)) {
				continue
			}
			counted[f.ToolCallID] = true
			started++
			if st.StopDuringTool > 0 && started == st.StopDuringTool {
				duringID = f.ToolCallID
				during = time.AfterFunc(time.Duration(st.StopDelay())*time.Millisecond, func() { abort(false) })
			}
		case "tool_execution_end":
			if f.ToolCallID == duringID && during != nil {
				during.Stop()
			}
			status := "done"
			if f.IsError {
				status = "error"
			}
			sr.Tools = append(sr.Tools, ToolUse{Name: f.ToolName, Args: args[f.ToolCallID], Status: status, IsError: f.IsError, Preview: preview(resultText(f.Result))})
			if !counted[f.ToolCallID] {
				continue
			}
			ended++
			if st.StopAfterTool > 0 && ended == st.StopAfterTool {
				abort(false)
			}
		case "message_end":
			m := f.Message
			if m == nil || m.Role != "assistant" {
				continue
			}
			c := Call{TTFTMs: int64(m.TTFT), Ms: int64(m.Duration), Error: m.ErrorMessage}
			if m.Usage != nil {
				c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning = m.Usage.Input, m.Usage.Output, m.Usage.CacheRead, m.Usage.CacheWrite, m.Usage.ReasoningTokens
			}
			sr.Calls = append(sr.Calls, c)
			var text strings.Builder
			for _, part := range m.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
			// 回复是这一句里每条助手消息的正文按先后用换行接起来（Go 壳接 Said、Bun 壳接这一轮她说的每条，口径一样）
			if text.Len() > 0 {
				if sr.Reply != "" {
					sr.Reply += "\n"
				}
				sr.Reply += text.String()
			}
		case "prompt_result":
			if f.ID != id {
				continue
			}
			switch {
			case f.Status == "error" && sr.Error == "":
				sr.Error = errText(f.Error)
			case f.Status == "aborted":
				mu.Lock()
				self := stopped
				mu.Unlock()
				if !self {
					sr.Error = "omp 自己中止了这一句"
				}
			}
			return done("")
		}
	}
	return done("omp 中途退出")
}
