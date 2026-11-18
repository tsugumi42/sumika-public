package app

// 用户钩子的装配（规格-用户钩子 第十二节）：整包路径——配置文件、插件、内核挂点、模型请求、运行记录。
// 用假网关和真 PowerShell；数据目录和工作目录都是临时的。

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/llm/llmtest"
)

type hookEnv struct {
	e    *testEnv
	work string
	a    *App
}

// openHooks 开一个后台：工作目录里放一个 a.go，全局钩子写进数据目录的 config/hooks.json；replies 是假模型每一步回什么
func openHooks(t *testing.T, hooksJSON, extraYAML string, replies ...llmtest.Reply) *hookEnv {
	t.Helper()
	e := newEnv(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(replies...), &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "a.go"), []byte("package main\n\nfunc main() { x := 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.extraConfig = "tools:\n  workdir: \"" + filepath.ToSlash(work) + "\"\n" + extraYAML
	e.writeConfig(testToken)
	if hooksJSON != "" {
		if err := os.WriteFile(filepath.Join(e.data, "config", "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := e.open()
	t.Cleanup(func() { a.Close() })
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() { // edit 要审批：这里一律放行（钩子本身不经审批）
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			for _, s := range a.approvals.Snapshot() {
				if s.Decision == nil {
					a.approvals.Decide(s.Request.ID, "once", "")
				}
			}
		}
	}()
	return &hookEnv{e: e, work: work, a: a}
}

func (h *hookEnv) hookRuns(t *testing.T) []string {
	t.Helper()
	rows, err := h.a.set.TraceStore().Trace.Query(`SELECT outcome || '/' || coalesce(class,'') FROM task_runs WHERE module='hooks' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

const editCall = `{"path":"a.go","edits":[{"oldText":"x := 1","newText":"x := 2"}]}`

const bothHooks = `{"hooks":{
"PostToolUse":[{"matcher":"Edit|Write","paths":["**/*.go"],"hooks":[{"type":"command","command":"Write-Output 'HOOK-OK-7731'"}]}],
"Stop":[{"paths":["**/*.go"],"hooks":[{"type":"command","command":"[Console]::Error.WriteLine('vet 没过 5521'); exit 2"}]}]}}`

// 改了 .go：工具后的输出进下一次请求；结束前的检查永远不过时，续跑到上限（默认 3 次）就收；运行页里有记录
func TestUserHooksRoundTripAndLimit(t *testing.T) {
	t.Parallel()
	h := openHooks(t, bothHooks, "", llmtest.Call("e", "edit", editCall), llmtest.Text("改好了"), llmtest.Text("还是改好了"))
	out := <-h.a.hub.Send(testChar, "把 x 改成 2")
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	bodies := h.e.bodiesCopy()
	if len(bodies) != 5 {
		t.Fatalf("要 5 次请求（调 edit、收结果、续跑 3 次），实际 %d", len(bodies))
	}
	if !strings.Contains(bodies[1], "HOOK-OK-7731") || !strings.Contains(bodies[1], "用户钩子 PostToolUse") {
		t.Errorf("工具后钩子的输出没进第二次请求")
	}
	for i, want := range []string{"第 1 次，最多 3 次", "第 2 次，最多 3 次", "第 3 次，最多 3 次"} {
		if b := bodies[2+i]; !strings.Contains(b, "vet 没过 5521") || !strings.Contains(b, want) {
			t.Errorf("第 %d 次请求缺结束前钩子的反馈 %q", 3+i, want)
		}
	}
	if strings.Contains(bodies[1], "vet 没过 5521") {
		t.Error("结束前钩子在还有工具要调的时候不该跑")
	}
	if got, _ := os.ReadFile(filepath.Join(h.work, "a.go")); !strings.Contains(string(got), "x := 2") {
		t.Fatalf("edit 没生效：%s", got)
	}
	// 记一行 PostToolUse 成功，Stop 跑了 4 次（续跑 3 次 + 上限那次留记录），到上限留 loop_limit
	runs := strings.Join(h.hookRuns(t), " ")
	if strings.Count(runs, "ok/") != 1 || strings.Count(runs, "failed/check_failed") != 4 || !strings.Contains(runs, "loop_limit") {
		t.Fatalf("运行记录：%s", runs)
	}
	// 回合时间线里有两种钩子的段
	rows, err := h.a.set.TraceStore().Trace.Query(`SELECT DISTINCT kind FROM turn_spans WHERE kind LIKE 'hook.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := map[string]bool{}
	for rows.Next() {
		var k string
		rows.Scan(&k)
		kinds[k] = true
	}
	if !kinds["hook.post_tool"] || !kinds["hook.stop"] {
		t.Fatalf("时间线：%v", kinds)
	}
}

// stopMaxContinues: 0 只跑不续；hooks.enabled: false 一条都不装；没有 hooks.json 什么都不变
func TestUserHooksOffAndDefault(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, hooks, yaml string
		requests          int
		note              bool
		runs              int
	}{
		{"没有钩子文件", "", "", 2, false, 0},
		{"总开关关了", bothHooks, "hooks:\n  enabled: false\n", 2, false, 0},
		{"续跑次数 0", bothHooks, "hooks:\n  stopMaxContinues: 0\n", 2, true, 3}, // 工具后 1 行 + Stop 1 行 + 上限 1 行
	} {
		h := openHooks(t, c.hooks, c.yaml, llmtest.Call("e", "edit", editCall), llmtest.Text("改好了"), llmtest.Text("再来一遍"))
		if out := <-h.a.hub.Send(testChar, "把 x 改成 2"); out.Err != nil {
			t.Fatalf("%s：%v", c.name, out.Err)
		}
		bodies := h.e.bodiesCopy()
		if len(bodies) != c.requests {
			t.Errorf("%s：请求 %d 次，要 %d 次", c.name, len(bodies), c.requests)
			continue
		}
		if got := strings.Contains(bodies[1], "HOOK-OK-7731"); got != c.note {
			t.Errorf("%s：尾注=%v", c.name, got)
		}
		if n := len(h.hookRuns(t)); n != c.runs {
			t.Errorf("%s：运行记录 %d 行，要 %d 行", c.name, n, c.runs)
		}
	}
}

// 项目级钩子：工作区里的 .sumika/hooks.json 在信任根没开时不跑，并留一行 untrusted
func TestUserHooksProjectNeedsTrust(t *testing.T) {
	t.Parallel()
	h := openHooks(t, "", "", llmtest.Call("e", "edit", editCall), llmtest.Text("改好了"))
	project := filepath.Join(h.work, ".sumika", "hooks.json")
	os.MkdirAll(filepath.Dir(project), 0o755)
	os.WriteFile(project, []byte(`{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"Write-Output 'PROJECT-HOOK-4419'"}]}]}}`), 0o644)
	if out := <-h.a.hub.Send(testChar, "把 x 改成 2"); out.Err != nil {
		t.Fatal(out.Err)
	}
	for _, b := range h.e.bodiesCopy() {
		if strings.Contains(b, "PROJECT-HOOK-4419") {
			t.Fatal("没开项目说明的信任根，项目钩子不该跑")
		}
	}
	if runs := strings.Join(h.hookRuns(t), " "); !strings.Contains(runs, "skipped/untrusted") {
		t.Fatalf("要留一行 untrusted：%s", runs)
	}
}
