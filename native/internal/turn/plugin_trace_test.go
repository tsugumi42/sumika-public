package turn

// 插件日志里宿主自动记的那几行（规格-插件两层 第二点五节第 3 条）：host.load、host.unload、host.tool，以及插件自己交的 Registry.Trace。
// 只写 t.TempDir()。放回旧代码（没有这些 Write）就红：文件里没有对应的行

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/tools"
)

type traceRow struct {
	Plugin, Event, Tool, Site, Result, Kind, Error, Note string
	Gen                                                  int
	MS                                                   *int64 `json:"ms"`
}

func newTraceHub(t *testing.T) (*Hub, string) {
	t.Helper()
	h := newTestHub(t)
	dir := t.TempDir()
	h.plugins.trace = plugintrace.New(plugintrace.Options{DataDir: dir})
	return h, dir
}

func traceRows(t *testing.T, dir, plugin string) []traceRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(plugintrace.Root(dir), plugin, plugintrace.LocalDate(time.Now())+".jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []traceRow
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r traceRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("坏行 %q：%v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func TestPluginTraceLoadUnload(t *testing.T) {
	t.Parallel()
	h, dir := newTraceHub(t)
	if err := h.Use(testPlugin{id: "svc", man: Manifest{Provides: []string{"s"}}, setup: func(r *Registry) (func(), error) {
		r.Trace(plugintrace.Event{Result: plugintrace.OK, Note: "插件自己交的"})
		return nil, r.Provide("s", 1)
	}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Use(testPlugin{id: "dep", man: Manifest{Requires: []string{"s"}}, setup: func(*Registry) (func(), error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	if err := h.Use(testPlugin{id: "bad", setup: func(*Registry) (func(), error) { return nil, errors.New("配置不对") }}); err == nil {
		t.Fatal("应当启用失败")
	}
	h.StopPlugin("svc")

	svc := traceRows(t, dir, "svc")
	if len(svc) != 3 || svc[0].Event != plugintrace.EventPlugin || svc[0].Note != "插件自己交的" || svc[0].Gen != 1 ||
		svc[1].Event != plugintrace.EventLoad || svc[1].Result != plugintrace.OK || svc[1].MS == nil ||
		svc[2].Event != plugintrace.EventUnload || svc[2].Note != "关掉了" || svc[2].Gen != 1 {
		t.Fatalf("svc 的行不对：%+v", svc)
	}
	dep := traceRows(t, dir, "dep")
	if len(dep) != 2 || dep[1].Event != plugintrace.EventUnload || dep[1].Note != "依赖的服务 s 没了" {
		t.Fatalf("依赖没了要写原因：%+v", dep)
	}
	bad := traceRows(t, dir, "bad")
	if len(bad) != 1 || bad[0].Event != plugintrace.EventLoad || bad[0].Result != plugintrace.Fail || bad[0].Kind != "apply" || bad[0].Error != "配置不对" {
		t.Fatalf("启用失败要写原因：%+v", bad)
	}
}

func TestPluginTraceToolCalls(t *testing.T) {
	t.Parallel()
	h, dir := newTraceHub(t)
	allow := true
	var how string
	tool := Tool{Decl: memDecl(t, "recall_raw"), For: func(audience.Audience) bool { return allow }, Exec: func(ctx context.Context, _ ToolCall) (tools.Result, error) {
		switch how {
		case "err":
			return tools.Result{}, errors.New("记忆库打不开")
		case "panic":
			panic("坏了")
		case "ctx":
			return tools.Result{}, ctx.Err()
		case "isError":
			return tools.Result{Text: "没找到", IsError: true}, nil
		}
		return tools.Result{Text: "找到了"}, nil
	}}
	if err := h.Use(toolPlugin{"mem", []Tool{tool}}); err != nil {
		t.Fatal(err)
	}
	reg := h.plugins.tool("recall_raw")
	call := ToolCall{Name: "recall_raw", Audience: audience.Console("local:sumika")}
	run := func(ctx context.Context, mode string) {
		how = mode
		h.plugins.runTool(ctx, reg, call)
	}
	run(context.Background(), "")
	run(context.Background(), "err")
	run(context.Background(), "panic")
	run(context.Background(), "isError")
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	run(expired, "ctx")
	cancel()
	stopped, cancel2 := context.WithCancel(context.Background())
	cancel2()
	run(stopped, "ctx")
	allow = false
	run(context.Background(), "")
	allow = true
	h.StopPlugin("mem")
	run(context.Background(), "")

	var got []string
	for _, r := range traceRows(t, dir, "mem") {
		if r.Event != plugintrace.EventTool {
			continue
		}
		if r.Tool != "recall_raw" || r.MS == nil || r.Gen != 1 {
			t.Fatalf("工具行要带工具名、耗时、代号：%+v", r)
		}
		got = append(got, r.Result+"/"+r.Kind)
	}
	want := []string{"ok/", "fail/error", "fail/error", "fail/error", "fail/timeout", "skip/canceled", "fail/denied", "fail/gone"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("工具调用的结果和分类：\n得到 %v\n应为 %v", got, want)
	}
}

// 没给数据目录（测试、没配）时宿主照常，什么也不写
func TestPluginTraceNoDataDir(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	if err := h.Use(testPlugin{id: "x", setup: func(r *Registry) (func(), error) {
		r.Trace(plugintrace.Event{Result: plugintrace.OK})
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	h.StopPlugin("x")
}

// bug（prepare.go）：插件停了或换了、听众不能用时 prepareCall 直接拒掉，走不到 runTool，插件日志里没有 host.tool；
// 调度器执行前 Valid 失效也一样。改后这几处各记一行（fail/gone、fail/denied）。放回旧代码就红：一行都没有
func TestPluginTracePrepareRefusals(t *testing.T) {
	t.Parallel()
	h, dir := newTraceHub(t)
	allow := true
	tool := Tool{Decl: memDecl(t, "recall_raw"), For: func(audience.Audience) bool { return allow }, Exec: func(context.Context, ToolCall) (tools.Result, error) {
		return tools.Result{Text: "找到了"}, nil
	}}
	if err := h.Use(toolPlugin{"mem", []Tool{tool}}); err != nil {
		t.Fatal(err)
	}
	reg := h.plugins.tool("recall_raw")
	regs := map[string]*toolReg{"recall_raw": reg}
	decls := []tools.Declaration{memDecl(t, "recall_raw")}
	args, err := jsjson.Parse(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	prep := func() (PreparedCall, *tools.Result) {
		pc, imm, err := prepareCall(context.Background(), Config{}, Workspace{}, "ctx", h.plugins, Call{Name: "recall_raw", Args: args}, "", "c1", "local:sumika", "run",
			audience.Console("local:sumika"), decls, regs, kernel.Invocation{ToolID: "t1"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return pc, imm
	}
	// 1. 听众不能用：准备时就拒
	allow = false
	if _, imm := prep(); imm == nil || !imm.IsError {
		t.Fatal("听众不能用应当拒掉")
	}
	// 2. 准备时还能用，执行前听众变了：Valid 失效
	allow = true
	pc, _ := prep()
	allow = false
	if pc.Valid == nil || pc.Valid() {
		t.Fatal("听众变了 Valid 应当失效")
	}
	allow = true
	// 3. 准备好以后插件停了：Valid 失效
	pc, _ = prep()
	h.StopPlugin("mem")
	if pc.Valid() {
		t.Fatal("插件停了 Valid 应当失效")
	}
	// 4. 插件已经停了（这一轮冻结的工具表里还有它）：准备时就拒
	if _, imm := prep(); imm == nil || !strings.Contains(imm.Text, "停掉了") {
		t.Fatalf("插件停了应当拒掉：%+v", imm)
	}

	var got []string
	for _, r := range traceRows(t, dir, "mem") {
		if r.Event == plugintrace.EventTool {
			if r.Tool != "recall_raw" || r.MS == nil || r.Error == "" {
				t.Fatalf("工具行要带工具名、耗时、原因：%+v", r)
			}
			got = append(got, r.Result+"/"+r.Kind)
		}
	}
	want := []string{"fail/denied", "fail/denied", "fail/gone", "fail/gone"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("没执行的工具调用也要记：\n得到 %v\n应为 %v", got, want)
	}
}

// 工具正在跑时插件被停：runTool 里 recover 之后的 !alive 那支，记 fail/gone（以前没有测试）
func TestPluginTraceToolGoneMidExec(t *testing.T) {
	t.Parallel()
	h, dir := newTraceHub(t)
	started := make(chan struct{})
	tool := Tool{Decl: memDecl(t, "recall_raw"), Exec: func(ctx context.Context, _ ToolCall) (tools.Result, error) {
		close(started)
		<-ctx.Done() // 插件停掉时 r.ctx 取消，Exec 自己退出
		return tools.Result{}, ctx.Err()
	}}
	if err := h.Use(toolPlugin{"mem", []Tool{tool}}); err != nil {
		t.Fatal(err)
	}
	reg := h.plugins.tool("recall_raw")
	done := make(chan tools.Result, 1)
	go func() {
		done <- h.plugins.runTool(context.Background(), reg, ToolCall{Name: "recall_raw", Audience: audience.Console("local:sumika")})
	}()
	<-started
	h.StopPlugin("mem")
	res := <-done
	if !res.IsError || !strings.Contains(res.Text, "停掉了") {
		t.Fatalf("插件停了结果要作废：%+v", res)
	}
	var got []string
	for _, r := range traceRows(t, dir, "mem") {
		if r.Event == plugintrace.EventTool {
			got = append(got, r.Result+"/"+r.Kind)
		}
	}
	if strings.Join(got, " ") != "fail/gone" {
		t.Fatalf("应当一行 fail/gone：%v", got)
	}
}
