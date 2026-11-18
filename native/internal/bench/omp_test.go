package bench

// omp 驱动：用测试程序自己扮一个假的 omp（BENCH_FAKE_OMP=1，照真 omp 的帧格式吐），查一句话的工具和回复、
// 第 n 个工具后停下再接着说、手动压缩、到点超时、配置目录和参数

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("BENCH_FAKE_OMP") == "1" { // 真 omp 的参数（--mode rpc …）测试框架不认，在解析参数之前接走
		fakeOmp()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeOmp 照 omp rpc v1 吐帧：说"列文件"→ 两个工具、一段回复；说"停"→ 一个工具后等 abort，等不到才接着第二个；
// 说"睡"→ 什么都不做，等 abort；compact 回成功
func fakeOmp() {
	os.WriteFile(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "argv.txt"), []byte(strings.Join(os.Args[1:], " ")), 0o644)
	out := json.NewEncoder(os.Stdout)
	emit := func(v map[string]any) { out.Encode(v) }
	lines := make(chan map[string]any)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			lines <- m
		}
		close(lines)
	}()
	waitAbort := func(d time.Duration) bool {
		select {
		case m := <-lines:
			return m["type"] == "abort"
		case <-time.After(d):
			return false
		}
	}
	tool := func(id, name, args, text string) {
		emit(map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": name, "args": json.RawMessage(args)})
		emit(map[string]any{"type": "tool_execution_end", "toolCallId": id, "toolName": name, "isError": name == "bash",
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	assistant := func(text string, in int) {
		content := []any{map[string]any{"type": "thinking", "thinking": "想一下"}}
		if text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": content,
			"usage": map[string]any{"input": in, "output": 7, "cacheRead": 100, "cacheWrite": 0, "reasoningTokens": 3}, "ttft": 1500.4, "duration": 2900.9}})
	}
	emit(map[string]any{"type": "ready", "protocolVersion": 1})
	for m := range lines {
		id, _ := m["id"].(string)
		switch m["type"] {
		case "set_event_filter":
			b, _ := json.Marshal(m)
			os.WriteFile(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "filter.json"), b, 0o644)
			emit(map[string]any{"type": "response", "id": id, "command": "set_event_filter", "success": true})
		case "compact":
			emit(map[string]any{"type": "response", "id": id, "command": "compact", "success": true})
		case "prompt":
			emit(map[string]any{"type": "response", "id": id, "command": "prompt", "success": true})
			emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": m["message"]}}}})
			status := "completed"
			switch msg := m["message"].(string); {
			case strings.Contains(msg, "列文件"):
				assistant("我先看看。", 6848) // 中间那条也带正文：回复要取最后一条的
				tool("c1", "read", `{"path":"."}`, "a.txt\nb.md")
				tool("c2", "bash", `{"command":"ls"}`, "出错了")
				assistant("有两个文件。", 149)
			case strings.Contains(msg, "停"):
				assistant("", 50)
				tool("c3", "write", `{"path":"x"}`, "写好了")
				if waitAbort(3 * time.Second) {
					status = "aborted"
				} else {
					tool("c4", "write", `{"path":"y"}`, "又写了")
					assistant("两个都写了。", 60)
				}
			case strings.Contains(msg, "慢"): // 一个慢工具开始了、还没结束：等 abort，等到了它以出错结束
				emit(map[string]any{"type": "tool_execution_start", "toolCallId": "c5", "toolName": "bash", "args": json.RawMessage(`{"command":"sleep 5; echo done >> ops.log"}`)})
				if waitAbort(3 * time.Second) {
					emit(map[string]any{"type": "tool_execution_end", "toolCallId": "c5", "toolName": "bash", "isError": true,
						"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Command aborted"}}}})
					status = "aborted"
				} else {
					emit(map[string]any{"type": "tool_execution_end", "toolCallId": "c5", "toolName": "bash", "isError": false,
						"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": ""}}}})
					assistant("跑完了。", 30)
				}
			case strings.Contains(msg, "发布"): // 先读发布脚本（很快），隔 1.5 秒再调用它（慢）：等到 abort 就停在那里
				tool("c6", "read", `{"path":"tools/publish.sh"}`, "echo publish")
				if waitAbort(1500 * time.Millisecond) {
					status = "aborted"
					break
				}
				emit(map[string]any{"type": "tool_execution_start", "toolCallId": "c7", "toolName": "bash", "args": json.RawMessage(`{"command":"bash tools/publish.sh dev"}`)})
				aborted := waitAbort(3 * time.Second)
				emit(map[string]any{"type": "tool_execution_end", "toolCallId": "c7", "toolName": "bash", "isError": aborted,
					"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "已发布 dev"}}}})
				if aborted {
					status = "aborted"
				}
			case strings.Contains(msg, "睡"):
				if waitAbort(30 * time.Second) {
					status = "aborted"
				}
			}
			emit(map[string]any{"type": "prompt_result", "id": id, "status": status, "agentInvoked": true, "sessionSettled": true})
		}
	}
}

func runFake(t *testing.T, task Task) (Result, string) {
	t.Helper()
	agent := filepath.Join(t.TempDir(), "agent")
	task.Workdir, task.Gateway, task.Model, task.Thinking = t.TempDir(), "http://127.0.0.1:1/v1", "auto/deepseek-v4.1-flash", "high"
	t.Setenv("BENCH_FAKE_OMP", "1")
	res, _, err := RunOmp(context.Background(), task, OmpOptions{Bin: os.Args[0], AgentDir: agent, Stderr: io.Discard, Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return res, agent
}

func TestOmpSay(t *testing.T) {
	res, agent := runFake(t, Task{Steps: []Step{{Say: "列文件"}, {Compact: true}}})
	if res.Error != "" || len(res.Steps) != 2 {
		t.Fatalf("%+v", res)
	}
	s := res.Steps[0]
	if s.Reply != "我先看看。\n有两个文件。" || s.Stopped || s.Error != "" {
		t.Fatalf("回复是每条助手消息的正文按先后接起来：%+v", s)
	}
	if len(s.Tools) != 2 || s.Tools[0].Name != "read" || s.Tools[0].Args != `{"path":"."}` || s.Tools[0].Preview != "a.txt\nb.md" ||
		s.Tools[1].Status != "error" || !s.Tools[1].IsError {
		t.Fatalf("工具：%+v", s.Tools)
	}
	if len(s.Calls) != 2 || s.Calls[0].Input != 6848 || s.Calls[0].CacheRead != 100 || s.Calls[0].Reasoning != 3 || s.Calls[0].TTFTMs != 1500 || s.Calls[1].Input != 149 {
		t.Fatalf("每次请求：%+v", s.Calls)
	}
	if c := res.Steps[1].Compact; c == nil || !c.OK {
		t.Fatalf("压缩：%+v", res.Steps[1])
	}
	filter, _ := os.ReadFile(filepath.Join(agent, "filter.json"))
	var f struct {
		Events         []string `json:"events"`
		MessageUpdates string   `json:"messageUpdates"`
	}
	json.Unmarshal(filter, &f)
	if slices.Contains(f.Events, "message_update") || !slices.Contains(f.Events, "message_end") || !slices.Contains(f.Events, "tool_execution_end") || f.MessageUpdates != "delta" {
		t.Fatalf("只订用得到的事件、不要累积快照：%s", filter)
	}
	argv, _ := os.ReadFile(filepath.Join(agent, "argv.txt"))
	models, _ := os.ReadFile(filepath.Join(agent, "models.yml"))
	if !strings.Contains(string(argv), "--mode rpc --model magpie/auto/deepseek-v4.1-flash --thinking high --no-skills") ||
		!strings.Contains(string(models), "baseUrl: http://127.0.0.1:1/v1") || !strings.Contains(string(models), "id: auto/deepseek-v4.1-flash") {
		t.Fatalf("参数、配置目录：%s\n%s", argv, models)
	}
}

func TestOmpStopAfterTool(t *testing.T) {
	res, _ := runFake(t, Task{Steps: []Step{{Say: "写两个文件，停", StopAfterTool: 1}, {Say: "列文件"}}})
	if res.Error != "" || len(res.Steps) != 2 {
		t.Fatalf("%+v", res)
	}
	s := res.Steps[0]
	if !s.Stopped || s.TimedOut || s.Error != "" || len(s.Tools) != 1 || s.StopTriggered == nil || !*s.StopTriggered {
		t.Fatalf("第 1 个工具结束就停：%+v", s)
	}
	if res.Steps[1].Reply != "我先看看。\n有两个文件。" {
		t.Fatalf("停下以后接着说：%+v", res.Steps[1])
	}
}

// 配置目录给的是相对路径：omp 的当前目录是工作目录，要先转成绝对的，不然它读不到写好的配置
func TestOmpRelativeAgentDir(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BENCH_FAKE_OMP", "1")
	task := Task{Workdir: t.TempDir(), Gateway: "http://127.0.0.1:1/v1", Model: "m", Thinking: "high", Steps: []Step{{Say: "列文件"}}}
	if _, _, err := RunOmp(context.Background(), task, OmpOptions{Bin: os.Args[0], AgentDir: "agent-rel", Stderr: io.Discard, Interval: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("agent-rel", "argv.txt")); err != nil {
		t.Fatalf("omp 读的配置目录和写配置的不是同一个：%v", err)
	}
}

// 工具正在跑的时候按停止：第 1 个工具开始 200 毫秒后发 abort，这一句停下（不是超时），下一句照常
func TestOmpStopDuringTool(t *testing.T) {
	res, _ := runFake(t, Task{Steps: []Step{{Say: "慢慢跑", StopDuringTool: 1, StopDelayMs: 200}, {Say: "列文件"}}})
	if res.Error != "" || len(res.Steps) != 2 {
		t.Fatalf("%+v", res)
	}
	s := res.Steps[0]
	if !s.Stopped || s.TimedOut || s.Error != "" || len(s.Tools) != 1 || !s.Tools[0].IsError || s.StopTriggered == nil || !*s.StopTriggered {
		t.Fatalf("工具正在跑时停下：%+v", s)
	}
	if res.Steps[1].Stopped || res.Steps[1].Reply == "" {
		t.Fatalf("下一句照常，不被上一句的停止波及：%+v", res.Steps[1])
	}
}

// 只数对得上 StopMatch 的工具：读发布脚本那次不算（不然 200 毫秒后就停在读完以后），停在真调用发布脚本正在跑时
func TestOmpStopMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	res, _ := runFake(t, Task{Steps: []Step{{Say: "发布", StopDuringTool: 1, StopDelayMs: 200, StopMatch: []string{"publish.ps1 ", "publish.sh "}}}})
	s := res.Steps[0]
	if !s.Stopped || s.TimedOut || len(s.Tools) != 2 || s.Tools[0].Name != "read" || !s.Tools[1].IsError {
		t.Fatalf("停在发布脚本正在跑时：%+v", s)
	}
	// 按结束数也一样：读脚本那次结束不算，发布脚本跑完（3 秒后）才停
	res, _ = runFake(t, Task{Steps: []Step{{Say: "发布", StopAfterTool: 1, StopMatch: []string{"publish.sh "}}}})
	if s := res.Steps[0]; !s.Stopped || len(s.Tools) != 2 || s.Tools[1].IsError {
		t.Fatalf("停在发布脚本跑完以后：%+v", s)
	}
}

// 第一句的工具很快跑完、还没到点这一句就说完了：那次停止作废，不能停到第二句上（第二句等到自己的时限才停、记超时）
func TestOmpStopDuringToolExpires(t *testing.T) {
	res, _ := runFake(t, Task{Steps: []Step{{Say: "列文件", StopDuringTool: 1, StopDelayMs: 700}, {Say: "睡"}}, TurnSeconds: 2})
	if len(res.Steps) != 2 || res.Steps[0].Stopped || res.Steps[0].StopTriggered == nil || *res.Steps[0].StopTriggered {
		t.Fatalf("第一句没等到点就说完了：%+v", res.Steps)
	}
	if s := res.Steps[1]; !s.Stopped || !s.TimedOut {
		t.Fatalf("第二句是自己到点停的（超时），不是被上一句的停止停的：%+v", s)
	}
}

func TestOmpTimeout(t *testing.T) {
	start := time.Now()
	res, _ := runFake(t, Task{Steps: []Step{{Say: "睡", StopAfterTool: 1}}, TurnSeconds: 1})
	s := res.Steps[0]
	if !s.Stopped || !s.TimedOut || s.Error != "" || s.StopTriggered == nil || *s.StopTriggered {
		t.Fatalf("到点按停止、记超时：%+v", s)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatal(fmt.Sprint("太久：", d))
	}
}

func TestOmpFinishedToolDoesNotTriggerStop(t *testing.T) {
	// 发布夹具先读脚本、隔 1.5 秒才执行；200 毫秒的停止不能落在读完以后。
	res, _ := runFake(t, Task{Steps: []Step{{Say: "发布", StopDuringTool: 1, StopDelayMs: 200}}, TurnSeconds: 8})
	if len(res.Steps) != 1 {
		t.Fatalf("步骤没跑成：%+v", res)
	}
	s := res.Steps[0]
	if s.StopTriggered == nil || *s.StopTriggered || s.Stopped || s.TimedOut || len(s.Tools) != 2 || s.Tools[1].IsError {
		t.Fatalf("读脚本已结束，不能迟到停在发布上：%+v", s)
	}
}
