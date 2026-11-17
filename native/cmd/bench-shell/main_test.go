package main

// Go 壳给模型的条件要和主窗口一样：接假网关跑一句，第一个请求的工具清单要等于 Go 自己的工具表（记忆的三个在最前，后面是主人 × 主窗口的内置工具）。
// 预期直接取 tools.MemoryDecls、tools.OwnerConsole：以后加工具（process_output 等）只改工具表，这里不用动；
// 不再拿 Bun 的旧录像比（Go 的行为以 Go 自己的规格为准，Bun 录像只是回归基线）

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/bench"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

func toolNames(t *testing.T, body string) []string {
	t.Helper()
	var b struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range b.Tools {
		names = append(names, x.Function.Name)
	}
	return names
}

func TestShellToolsSameAsBun(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	var want []string
	for _, d := range append(tools.MemoryDecls(), tools.OwnerConsole()...) {
		want = append(want, d.Name)
	}
	if len(want) == 0 {
		t.Fatal("工具表是空的")
	}

	var mu sync.Mutex
	var bodies []string
	gw := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"m","context_length":200000}`)}, Reply: llmtest.Text("好的")}, &bodies, &mu))
	defer gw.Close()
	dir := t.TempDir()
	task := bench.Task{ID: "t", Workdir: filepath.Join(dir, "work"), DataDir: filepath.Join(dir, "data"), Gateway: gw.URL + "/v1", Model: "m",
		Thinking: "high", Character: "suzu", Steps: []bench.Step{{Say: "你好"}}}
	os.MkdirAll(task.Workdir, 0o755)
	b, _ := json.Marshal(task)
	taskFile := filepath.Join(dir, "task.json")
	os.WriteFile(taskFile, b, 0o644)
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	res := bench.Result{Shell: "go"}
	if err := run(taskFile, root, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 1 || res.Steps[0].Reply != "好的" || res.Steps[0].Error != "" {
		t.Fatalf("%+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("没发请求")
	}
	if got := toolNames(t, bodies[0]); !slices.Equal(got, want) {
		t.Fatalf("发给模型的工具清单和 Go 的工具表不一样：\n实际 %v\n预期 %v", got, want)
	}
	// 聊天记录写进 Go 的本地库（规格-聊天记录按平台分库），不建 Bun 的 shared/events.db
	p, err := paths.Init(task.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.BunEventsDB()); !os.IsNotExist(err) {
		t.Fatalf("不该建 Bun 的库：%v", err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	st, _ := set.For("console")
	var n int
	st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = 'console:suzu'`).Scan(&n)
	if n != 2 {
		t.Fatalf("本地库里该有主人那句和她的回复：%d 条", n)
	}
}

// runShell 接假网关按 replies 回、跑一遍 steps，返回结果、请求体和工作目录；mut 在写题目之前改它（加历史、改条数）
func runShell(t *testing.T, replies []llmtest.Reply, steps []bench.Step, mut ...func(*bench.Task)) (bench.Result, []string, string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	gw := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"m","context_length":200000}`)}, Replies: replies, Reply: llmtest.Text("好了")}, &bodies, &mu))
	defer gw.Close()
	dir := t.TempDir()
	task := bench.Task{ID: "t", Workdir: filepath.Join(dir, "work"), DataDir: filepath.Join(dir, "data"), Gateway: gw.URL + "/v1", Model: "m",
		Thinking: "high", Character: "suzu", Steps: steps}
	task.TurnSeconds = 120 // 这是出错时的上限，不是等待：假模型秒回，只有起 pwsh 慢，机器忙时 15 秒不够
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", outside)
	for _, f := range mut {
		f(&task)
	}
	os.MkdirAll(task.Workdir, 0o755)
	b, _ := json.Marshal(task)
	taskFile := filepath.Join(dir, "task.json")
	os.WriteFile(taskFile, b, 0o644)
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	res := bench.Result{Shell: "go"}
	if err := run(taskFile, root, &res); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return res, append([]string(nil), bodies...), task.Workdir
}

// B4 那样的长历史：40 条，答案在第一条
func longSeed(task *bench.Task) { longSeedN(40)(task) }

// longSeedN 是 n 条的长历史，答案在第一条
func longSeedN(n int) func(*bench.Task) {
	return func(task *bench.Task) { seedLines(task, n) }
}

func seedLines(task *bench.Task, n int) {
	for i := range n {
		text := "随便聊聊第 " + strconv.Itoa(i) + " 句"
		if i == 0 {
			text = "我的歌单叫 Summer Vibes"
		}
		who := "owner"
		if i%2 == 1 {
			who = "character"
		}
		task.Seed = append(task.Seed, bench.Line{Who: who, Text: text})
	}
}

// B4：recall_raw 要查得到第一句原话（原来记忆没接事件来源，一律报 no_events）
func TestShellRecallRaw(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	recall := []llmtest.Reply{llmtest.Call("c1", "recall_raw", `{"keywords":"Summer Vibes"}`)}
	res, _, _ := runShell(t, recall, []bench.Step{{Say: "歌单叫什么？"}}, longSeed)
	if len(res.Steps) != 1 || len(res.Steps[0].Tools) != 1 {
		t.Fatalf("%+v", res.Steps)
	}
	if u := res.Steps[0].Tools[0]; u.IsError || !strings.Contains(u.Preview, "Summer Vibes") {
		t.Fatalf("recall_raw 该查到第一句原话：%+v", u)
	}
}

// B4：群聊仍按 recentMessages 的锚定窗口（规格-提示词前缀稳定第五节），本地私聊改成从上下文起点读、不看它。
// 群聊带的原文起点锚在 n 的整数倍上、带 n～2n-1 条，所以“进不去”要攒到 2n 条以上：70 条时起点在第 30 条，
// 第一句落在窗口外；40 条时窗口还是从头带起。本地私聊同样 70 条，默认 30 也必须全部带上。
func TestShellRecentMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	steps := []bench.Step{{Say: "歌单叫什么？"}}
	seed := longSeedN(70)
	group := func(task *bench.Task) { task.Conversation = "console:group:b4" }
	if _, bodies, _ := runShell(t, nil, steps, seed, group); strings.Contains(bodies[0], "Summer Vibes") {
		t.Fatal("群聊默认 30 条、起点锚在 30 的整数倍：70 条时第一句不该在请求里")
	}
	if _, bodies, _ := runShell(t, nil, steps, seed, group, func(task *bench.Task) { task.RecentMessages = 100 }); !strings.Contains(bodies[0], "Summer Vibes") {
		t.Fatal("群聊 recentMessages 100，第一句该在请求里")
	}
	if _, bodies, _ := runShell(t, nil, steps, seed); !strings.Contains(bodies[0], "Summer Vibes") {
		t.Fatal("本地私聊从上下文起点读，默认 30 时 70 条也该全带上")
	}
	if _, bodies, _ := runShell(t, nil, steps, seed, func(task *bench.Task) { task.RecentMessages = 100 }); !strings.Contains(bodies[0], "Summer Vibes") {
		t.Fatal("本地私聊 recentMessages 100，第一句该在请求里")
	}
}

// B4：压缩那一步把压出来的摘要原文带进结果
func TestShellCompactSummary(t *testing.T) {
	// 每句加长：压完要明显变小才算压成
	long := func(task *bench.Task) {
		for i := range task.Seed {
			task.Seed[i].Text += strings.Repeat("今天随便聊了些天气和吃饭的事。", 20)
		}
		task.KeepTokens, task.RecentMessages = 50, 100
	}
	res, _, _ := runShell(t, []llmtest.Reply{llmtest.Text("主人的歌单叫 Summer Vibes。")}, []bench.Step{{Compact: true}}, longSeed, long)
	if len(res.Steps) != 1 || res.Steps[0].Compact == nil || !res.Steps[0].Compact.OK || !strings.Contains(res.Steps[0].Compact.Summary, "Summer Vibes") {
		t.Fatalf("%+v", res.Steps[0].Compact)
	}
}

// 批准版拒绝工作目录外的操作，告诉模型边界后仍能在工作目录里继续；非批准版保持取消反馈。
func TestShellApproveInWorkdir(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	replies := []llmtest.Reply{
		llmtest.Call("c1", "write", `{"path":"tools/x.ps1","content":"Set-Content -Path out.txt -Value ok","purpose":"写脚本"}`),
		llmtest.Call("c2", "powershell", `{"command":"pwsh -NoProfile -File tools/x.ps1","purpose":"运行脚本"}`),
		llmtest.Call("c3", "powershell", `{"command":"Set-Content (Join-Path $env:TEMP 'blocked.txt') -Value wrong","purpose":"写到外面"}`),
		llmtest.Call("c4", "powershell", `{"command":"Set-Content continued.txt -Value ok","purpose":"在工作目录继续"}`),
	}
	for _, on := range []bool{true, false} {
		t.Run(strconv.FormatBool(on), func(t *testing.T) {
			res, bodies, wd := runShell(t, replies, []bench.Step{{Say: "跑一下"}}, func(task *bench.Task) { task.ApproveInWorkdir = on })
			if len(res.Steps) != 1 || len(res.Steps[0].Tools) != 4 || len(bodies) != 5 {
				t.Fatalf("四个工具后应继续请求：%+v，请求 %d 次", res, len(bodies))
			}
			u := res.Steps[0].Tools
			status, reason, second := "cancelled", "操作被取消了（所有者发了新消息或对话被中止），没有执行。", "cancelled"
			if on {
				status, reason, second = "denied", bench.WorkdirDenied, "done"
			}
			if u[0].Status != "done" || u[1].Status != second || u[2].Status != status || u[2].Preview != reason || !strings.Contains(bodies[3], reason) || u[3].Status != "done" {
				t.Fatalf("审批反馈或继续执行不对：%+v", u)
			}
			_, err := os.Stat(filepath.Join(wd, "out.txt"))
			if (err == nil) != on {
				t.Fatalf("运行脚本的批准边界不对：%v", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(wd), "outside", "blocked.txt")); !os.IsNotExist(err) {
				t.Fatalf("外部文件不应写出：%v", err)
			}
			if b, err := os.ReadFile(filepath.Join(wd, "continued.txt")); err != nil || strings.TrimSpace(string(b)) != "ok" {
				t.Fatalf("工作目录内应继续完成：%q，%v", b, err)
			}
		})
	}
}

// B3：第 1 个工具结束就按停止——第二条命令不执行，也不再请求
func TestShellStopAfterTool(t *testing.T) {
	res, bodies, wd := runShell(t, []llmtest.Reply{
		llmtest.Call("c1", "powershell", `{"command":"Add-Content -Path ops.log -Value a"}`),
		llmtest.Call("c2", "powershell", `{"command":"Add-Content -Path ops.log -Value b"}`),
	}, []bench.Step{{Say: "写两行", StopAfterTool: 1}, {Say: "接着做", StopAfterTool: 1}})
	b, _ := os.ReadFile(filepath.Join(wd, "ops.log"))
	// 第二句从头数：接着做的第 1 个工具（写 b）结束又停，一共只请求两次
	if len(res.Steps) != 2 || !res.Steps[0].Stopped || !res.Steps[1].Stopped || len(bodies) != 2 || strings.Join(strings.Fields(string(b)), ",") != "a,b" {
		t.Fatalf("每一句停在它的第 1 个工具之后：%+v，请求 %d 次，ops.log %q", res.Steps, len(bodies), b)
	}
}

// B3：工具正在跑的时候按停止——先睡后写的命令被停在睡的时候，文件没写出来
func TestShellStopDuringTool(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	res, bodies, wd := runShell(t, []llmtest.Reply{
		llmtest.Call("c1", "powershell", `{"command":"Start-Sleep -Seconds 5; Add-Content -Path ops.log -Value done"}`),
	}, []bench.Step{{Say: "慢慢写", StopDuringTool: 1, StopDelayMs: 1000}})
	if _, err := os.Stat(filepath.Join(wd, "ops.log")); !os.IsNotExist(err) {
		t.Fatalf("停在工具正在跑的时候，后面那半句不该执行：%v", err)
	}
	if len(res.Steps) != 1 || !res.Steps[0].Stopped || len(bodies) != 1 {
		t.Fatalf("停下了、不再请求：%+v，请求 %d 次", res.Steps, len(bodies))
	}
}

// B3：只数对得上 StopMatch 的工具——第一条（写 a.log）不算，停在第二条正在睡的时候
func TestShellStopMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	res, _, wd := runShell(t, []llmtest.Reply{
		llmtest.Call("c1", "powershell", `{"command":"Start-Sleep -Seconds 2; Add-Content -Path a.log -Value x"}`),
		llmtest.Call("c2", "powershell", `{"command":"Start-Sleep -Seconds 2; Add-Content -Path b.log -Value x"}`),
	}, []bench.Step{{Say: "写两个", StopDuringTool: 1, StopDelayMs: 500, StopMatch: []string{"b.log"}}})
	_, errA := os.Stat(filepath.Join(wd, "a.log"))
	_, errB := os.Stat(filepath.Join(wd, "b.log"))
	if errA != nil || !os.IsNotExist(errB) || len(res.Steps) != 1 || !res.Steps[0].Stopped {
		t.Fatalf("第一条写完、停在第二条睡的时候：a %v，b %v，%+v", errA, errB, res.Steps)
	}
}

func TestShellStopObservation(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	for _, c := range []struct {
		name                string
		step                bench.Step
		command             string
		triggered, timedOut bool
	}{
		{"工具后", bench.Step{StopAfterTool: 1}, "Set-Content ops.log ok", true, false},
		{"工具中", bench.Step{StopDuringTool: 1, StopDelayMs: 500}, "Start-Sleep -Seconds 5", true, false},
		{"没到点", bench.Step{StopAfterTool: 2}, "Set-Content ops.log ok", false, false},
		{"超时", bench.Step{StopAfterTool: 2}, "Start-Sleep -Seconds 5", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.step.Say = "做一步"
			args, _ := json.Marshal(map[string]string{"command": c.command, "purpose": "检查停止点"})
			res, _, _ := runShell(t, []llmtest.Reply{llmtest.Call("c1", "powershell", string(args))}, []bench.Step{c.step}, func(task *bench.Task) {
				if c.timedOut {
					task.TurnSeconds = 1
				}
			})
			if len(res.Steps) != 1 {
				t.Fatalf("步骤没跑成：%+v", res)
			}
			s := res.Steps[0]
			if s.StopTriggered == nil || *s.StopTriggered != c.triggered || s.TimedOut != c.timedOut || s.Stopped != (c.triggered || c.timedOut) {
				t.Fatalf("题设停止=%v，超时=%v，结果：%+v", c.triggered, c.timedOut, s)
			}
			if c.timedOut && (len(s.Tools) != 1 || s.Tools[0].Name != "powershell") {
				t.Fatalf("超时前应已进入慢工具，不能只停在冷启动：%+v", s)
			}
		})
	}
}

// 第一个工具已经结束，延迟停止不能落到第二个慢工具上；最后只按整轮超时停。
func TestShellFinishedToolDoesNotTriggerStop(t *testing.T) {
	res, _, _ := runShell(t, []llmtest.Reply{
		llmtest.Call("c0", "write", `{"path":"quick.txt","content":"ok","purpose":"先写文件"}`),
		llmtest.Call("c1", "powershell", `{"command":"Start-Sleep -Seconds 5","purpose":"再等一会儿"}`),
	}, []bench.Step{{Say: "先写再等", StopDuringTool: 1, StopDelayMs: 500}}, func(task *bench.Task) { task.TurnSeconds = 3 })
	if len(res.Steps) != 1 {
		t.Fatalf("步骤没跑成：%+v", res)
	}
	s := res.Steps[0]
	if s.StopTriggered == nil || *s.StopTriggered || !s.TimedOut || !s.Stopped || len(s.Tools) != 2 || s.Tools[0].Status != "done" {
		t.Fatalf("工具已结束，不应迟到触发题设停止：%+v", s)
	}
}
