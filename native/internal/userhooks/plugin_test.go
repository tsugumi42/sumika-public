package userhooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/taskrun"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// fakeRun 记下每次收到的命令，按顺序回预设结果
type fakeRun struct {
	mu    sync.Mutex
	calls []tools.ShellCommand
	reply func(tools.ShellCommand) (code int, stdout, stderr string, err error)
	peak  atomic.Int32
	now   atomic.Int32
	hold  time.Duration
}

func (f *fakeRun) run(ctx context.Context, c tools.ShellCommand) (int, error) {
	n := f.now.Add(1)
	defer f.now.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.hold > 0 {
		select {
		case <-time.After(f.hold):
		case <-ctx.Done():
			return -1, nil
		}
	}
	code, so, se, err := 0, "", "", error(nil)
	if f.reply != nil {
		code, so, se, err = f.reply(c)
	}
	c.Stdout.Write([]byte(so))
	c.Stderr.Write([]byte(se))
	return code, err
}

func (f *fakeRun) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type rowSink struct {
	mu   sync.Mutex
	rows []taskrun.Row
}

func (s *rowSink) AddTaskRun(r taskrun.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, r)
	return nil
}

func (s *rowSink) classes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.rows {
		out = append(out, r.Module+"/"+r.Outcome+"/"+r.Class)
	}
	return out
}

type fixedTrust struct {
	ok  bool
	err error
}

func (f fixedTrust) Trusted(context.Context, turn.Workspace) (bool, error) { return f.ok, f.err }

type rig struct {
	t      *testing.T
	dir    string // 工作区根
	global string
	fake   *fakeRun
	sink   *rowSink
	p      *Plugin
}

func newRig(t *testing.T, trust Trust, globalJSON string, mod func(*Options)) *rig {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, dir: dir, fake: &fakeRun{}, sink: &rowSink{}}
	r.global = filepath.Join(t.TempDir(), "hooks.json")
	if globalJSON != "" {
		r.write(r.global, globalJSON)
	}
	o := Options{GlobalFile: r.global, Trust: trust, Degrade: &taskrun.Degrader{Sink: r.sink}, Run: r.fake.run, StopMax: 3}
	if mod != nil {
		mod(&o)
	}
	r.p = New(o)
	return r
}

func (r *rig) write(path, content string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) toolEvent(channel, name, argsJSON string) turn.ToolResultEvent {
	args, err := jsjson.Parse(argsJSON)
	if err != nil {
		r.t.Fatal(err)
	}
	return turn.ToolResultEvent{Audience: audience.Audience{Channel: channel, PrivateOK: true}, Workspace: turn.Workspace{CWD: r.dir, Root: r.dir},
		CallID: "c1", Name: name, Args: args, Text: "ok", TurnID: "t1", CharacterID: "ch", Conversation: "console:1"}
}

func (r *rig) stopEvent(channel string, tl ...turn.ToolFact) turn.StopEvent {
	return turn.StopEvent{Audience: audience.Audience{Channel: channel, PrivateOK: true}, Workspace: turn.Workspace{CWD: r.dir, Root: r.dir},
		Answer: "做完了", Tools: tl, TurnID: "t1", CharacterID: "ch", Conversation: "console:1"}
}

func okEdit(t *testing.T, path string) turn.ToolFact {
	t.Helper()
	args, err := jsjson.Parse(`{"path":` + jsString(path) + `}`)
	if err != nil {
		t.Fatal(err)
	}
	return turn.ToolFact{Name: "edit", Args: args, OK: true}
}

func jsString(s string) string {
	return string(jsjson.Stringify(jsjson.Value{Kind: jsjson.String, Str: s}))
}

const gofmtHook = `{"hooks":{"PostToolUse":[{"matcher":"Edit|Write","paths":["**/*.go"],"hooks":[{"type":"command","command":"gofmt -l \"$env:SUMIKA_FILE_PATH\""}]}],
"Stop":[{"paths":["**/*.go"],"hooks":[{"type":"command","command":"go vet ./...","onFailure":"block"}]}]}}`

func TestNoFileNoRun(t *testing.T) {
	r := newRig(t, nil, "", nil)
	ctx := context.Background()
	if notes, err := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`)); err != nil || len(notes) != 0 {
		t.Fatalf("%v %v", notes, err)
	}
	if d, err := r.p.onStop(ctx, r.stopEvent("console", okEdit(t, "a.go"))); err != nil || d != (turn.StopDecision{}) {
		t.Fatalf("%+v %v", d, err)
	}
	if r.fake.count() != 0 || len(r.sink.rows) != 0 {
		t.Fatal("没有配置文件时什么也不该跑、不该记")
	}
}

func TestPostToolMatchingAndNotes(t *testing.T) {
	r := newRig(t, nil, gofmtHook, nil)
	ctx := context.Background()
	r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return 0, "a.go\n", "", nil }
	for _, c := range []struct{ tool, args string }{
		{"edit", `{"path":"a.txt"}`}, {"read", `{"path":"a.go"}`}, {"powershell", `{"command":"gofmt -w a.go"}`}, {"edit", `{"edits":[]}`},
	} {
		if notes, _ := r.p.onToolResult(ctx, r.toolEvent("console", c.tool, c.args)); len(notes) != 0 || r.fake.count() != 0 {
			t.Fatalf("%s %s 不该匹配：%v", c.tool, c.args, notes)
		}
	}
	notes, err := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"sub/a.go"}`))
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "用户钩子 PostToolUse") || !strings.Contains(notes[0], "a.go") {
		t.Fatalf("%v %v", notes, err)
	}
	c := r.fake.calls[0]
	want := filepath.Join(r.dir, "sub", "a.go")
	if !strings.Contains(strings.Join(c.Env, "\n"), "SUMIKA_FILE_PATH="+want) || c.Dir != r.dir || !strings.Contains(string(c.Stdin), `"tool_name":"edit"`) || !strings.Contains(string(c.Stdin), "file_path") {
		t.Fatalf("%+v", c)
	}
	if got := r.sink.classes(); len(got) != 1 || got[0] != "hooks/ok/" {
		t.Fatalf("成功也要记一行：%v", got)
	}
}

func TestPostToolOutcomesToNotes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		feedback string
		code     int
		so, se   string
		err      error
		want     string // 空表示没有尾注
		class    string
	}{
		{"退出 0 没输出", "", 0, "", "", nil, "", ""},
		{"退出 0 有输出", "", 0, "a.go", "", nil, "a.go", ""},
		{"feedback failure 时成功输出不给", `"feedback":"failure",`, 0, "a.go", "", nil, "", ""},
		{"feedback never", `"feedback":"never",`, 1, "x", "", nil, "", taskrun.ClassExitNonzero},
		{"退出 2 给 stderr", "", 2, "so", "vet 没过", nil, "vet 没过", taskrun.ClassCheckFailed},
		{"退出 2 没 stderr 给 stdout", "", 2, "只有 stdout", "", nil, "只有 stdout", taskrun.ClassCheckFailed},
		{"退出 2 什么都没有", "", 2, "", "", nil, "没有输出", taskrun.ClassCheckFailed},
		{"退出 1 带输出", "", 1, "a.go", "oops", nil, "退出码 1", taskrun.ClassExitNonzero},
		{"起不来", "", -1, "", "", errors.New("找不到 PowerShell"), "没跑起来", taskrun.ClassSpawnFailed},
		{"JSON additionalContext", "", 0, `{"hookSpecificOutput":{"additionalContext":"记得跑测试"}}`, "", nil, "记得跑测试", ""},
		{"JSON decision block", "", 0, `{"decision":"block","reason":"格式不对"}`, "", nil, "格式不对", taskrun.ClassCheckFailed},
		{"JSON continue false 只在 PostToolUse 上提醒", "", 0, `{"continue":false,"stopReason":"别写了"}`, "", nil, "别写了", taskrun.ClassInvalidOutput},
	} {
		global := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"x",` + c.feedback + `"timeout":5}]}]}}`
		r := newRig(t, nil, global, nil)
		r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return c.code, c.so, c.se, c.err }
		notes, err := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
		if err != nil {
			t.Fatalf("%s：钩子的失败不能变成回合的错误：%v", c.name, err)
		}
		joined := strings.Join(notes, "|")
		if (c.want == "") != (joined == "") || !strings.Contains(joined, c.want) {
			t.Errorf("%s：尾注 %q 要含 %q", c.name, joined, c.want)
		}
		rows := r.sink.classes()
		if len(rows) != 1 || !strings.HasSuffix(rows[0], "/"+c.class) {
			t.Errorf("%s：运行记录 %v 要以 /%s 结尾", c.name, rows, c.class)
		}
	}
}

// 场合：QQ 私聊、QQ 群、别的渠道一律不跑；子运行的门在内核（in.local()），这里是第二道
func TestLocalGate(t *testing.T) {
	r := newRig(t, nil, gofmtHook, nil)
	ctx := context.Background()
	r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return 2, "", "不行", nil }
	for _, ch := range []string{"qq", "telegram", "", "CONSOLE"} {
		if notes, _ := r.p.onToolResult(ctx, r.toolEvent(ch, "edit", `{"path":"a.go"}`)); len(notes) != 0 {
			t.Errorf("渠道 %q 收到了尾注", ch)
		}
		if d, _ := r.p.onStop(ctx, r.stopEvent(ch, okEdit(t, "a.go"))); d != (turn.StopDecision{}) {
			t.Errorf("渠道 %q 的结束前钩子有了回答", ch)
		}
	}
	if r.fake.count() != 0 {
		t.Fatal("QQ 场合零调用")
	}
	// 本地私聊和本地群聊都是 console，只差 Scene：都要跑
	e := r.toolEvent("console", "edit", `{"path":"a.go"}`)
	e.Audience.Scene = "group"
	if notes, _ := r.p.onToolResult(ctx, e); len(notes) != 1 {
		t.Fatal("本地群聊要跑")
	}
}

func TestProjectTrust(t *testing.T) {
	ctx := context.Background()
	project := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"project-hook"}]}]}}`
	for _, c := range []struct {
		name  string
		trust Trust
		runs  bool
	}{
		{"信任根没开", nil, false},
		{"不信任", fixedTrust{ok: false}, false},
		{"信任检查出错", fixedTrust{ok: true, err: errors.New("身份核不了")}, false},
		{"信任", fixedTrust{ok: true}, true},
	} {
		r := newRig(t, c.trust, "", nil)
		r.write(filepath.Join(r.dir, ".sumika", "hooks.json"), project)
		for i := 0; i < 3; i++ { // 每个事件都会问一次，提醒只留一次
			notes, _ := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
			_ = notes
		}
		if got := r.fake.count() > 0; got != c.runs {
			t.Errorf("%s：跑了=%v", c.name, got)
		}
		if !c.runs {
			if rows := r.sink.classes(); len(rows) != 1 || !strings.HasSuffix(rows[0], "/"+taskrun.ClassUntrusted) {
				t.Errorf("%s：该只记一次 untrusted：%v", c.name, rows)
			}
		}
	}
	// 全局钩子不看信任根
	r := newRig(t, fixedTrust{ok: false}, project, nil)
	if _, _ = r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`)); r.fake.count() != 1 {
		t.Fatal("全局钩子在不信任的工作区也要跑")
	}
}

// .sumika 是联接、指到工作区外面：真实位置不在工作区里，"信任这个目录"管不到它
func TestProjectTrustJunctionEscape(t *testing.T) {
	r := newRig(t, fixedTrust{ok: true}, "", nil)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	r.write(filepath.Join(outside, "hooks.json"), `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"escaped"}]}]}}`)
	link := filepath.Join(r.dir, ".sumika")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Skipf("建不了联接：%v %s", err, out)
	}
	notes, _ := r.p.onToolResult(context.Background(), r.toolEvent("console", "edit", `{"path":"a.go"}`))
	if r.fake.count() != 0 || len(notes) != 0 {
		t.Fatal("指到工作区外的项目钩子不该跑")
	}
	if rows := r.sink.classes(); len(rows) != 1 || !strings.HasSuffix(rows[0], "/"+taskrun.ClassUntrusted) {
		t.Fatalf("%v", rows)
	}
}

// 敌对的工具参数：路径带引号、$()、反引号、换行、NUL。只进环境变量和标准输入，命令文字一个字节都不变
func TestArgsNotInCommand(t *testing.T) {
	r := newRig(t, nil, gofmtHook, nil)
	for _, name := range []string{`a"b$(calc).go`, "a`b;rm -rf.go", "line\nbreak.go", "nul\x00byte.go", `$env:USERNAME.go`} {
		r.fake.calls = nil
		e := r.toolEvent("console", "edit", `{"path":`+jsString(name)+`}`)
		if _, err := r.p.onToolResult(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if r.fake.count() != 1 {
			t.Fatalf("%q：跑了 %d 次", name, r.fake.count())
		}
		c := r.fake.calls[0]
		if c.Command != `gofmt -l "$env:SUMIKA_FILE_PATH"` {
			t.Errorf("%q：命令文字变了 %q", name, c.Command)
		}
		env := strings.Join(c.Env, "\x01")
		hasFile := strings.Contains(env, "SUMIKA_FILE_PATH=")
		if strings.ContainsRune(name, 0) == hasFile {
			t.Errorf("%q：带 NUL 的路径不设环境变量，别的要设（设了=%v）", name, hasFile)
		}
		for _, kv := range c.Env {
			if strings.ContainsRune(kv, 0) {
				t.Errorf("%q：环境变量里有 NUL", name)
			}
		}
	}
}

func TestStopDecisions(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name       string
		extra      string // handler 上多写的
		code       int
		so, se     string
		last       bool
		cont, halt bool
		reasonHas  string
	}{
		{"通过", "", 0, "", "", false, false, false, ""},
		{"通过但有输出不续跑", "", 0, "all good", "", false, false, false, ""},
		{"退出 2 续跑", "", 2, "", "vet 报错", false, true, false, "vet 报错"},
		{"退出 2 没说原因给默认句", "", 2, "", "", false, true, false, "检查没过"},
		{"退出 1 默认不续", "", 1, "x", "", false, false, false, ""},
		{"退出 1 有 onFailure block 续", `,"onFailure":"block"`, 1, "x", "y", false, true, false, "x"},
		{"JSON block 续", "", 0, `{"decision":"block","reason":"测试没过"}`, "", false, true, false, "测试没过"},
		{"JSON block 没 reason 不续", "", 0, `{"decision":"block"}`, "", false, false, false, ""},
		{"JSON continue false 结束", "", 0, `{"continue":false}`, "", false, false, true, ""},
		{"JSON additionalContext 续", "", 0, `{"hookSpecificOutput":{"additionalContext":"补一条"}}`, "", false, true, false, "补一条"},
		{"到上限只留记录", "", 2, "", "还是不行", true, true, false, "还是不行"},
	} {
		global := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"check"` + c.extra + `}]}]}}`
		r := newRig(t, nil, global, nil)
		r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return c.code, c.so, c.se, nil }
		e := r.stopEvent("console", okEdit(t, "a.go"))
		e.Last = c.last
		d, err := r.p.onStop(ctx, e)
		if err != nil || d.Continue != c.cont || d.Halt != c.halt || !strings.Contains(d.Reason, c.reasonHas) {
			t.Errorf("%s：%+v %v", c.name, d, err)
		}
		if c.last {
			if rows := r.sink.classes(); len(rows) != 2 || !strings.HasSuffix(rows[1], "/"+taskrun.ClassLoopLimit) {
				t.Errorf("%s：到上限要留 loop_limit：%v", c.name, rows)
			}
		}
	}
}

func TestStopMatchesWhatTheTurnDid(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, gofmtHook, nil) // Stop 只在这一轮改过 .go 时跑
	r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return 2, "", "不行", nil }
	for _, tl := range [][]turn.ToolFact{
		nil,
		{okEdit(t, "a.md")},
		{{Name: "edit", Args: okEdit(t, "a.go").Args, OK: false}}, // 失败的编辑不算改过
		{{Name: "read", Args: okEdit(t, "a.go").Args, OK: true}},
	} {
		if d, _ := r.p.onStop(ctx, r.stopEvent("console", tl...)); d.Continue {
			t.Errorf("%+v 不该触发 Stop 钩子", tl)
		}
	}
	if r.fake.count() != 0 {
		t.Fatal("没匹配到就不该跑，也不该记")
	}
	e := r.stopEvent("console", okEdit(t, "sub/a.go"), okEdit(t, "sub/a.go"))
	e.Continued = 1
	if d, _ := r.p.onStop(ctx, e); !d.Continue {
		t.Fatal("改过 .go 要跑")
	}
	in := string(r.fake.calls[0].Stdin)
	if !strings.Contains(in, `"stop_hook_active":true`) || strings.Count(in, "a.go") != 1 || !strings.Contains(in, `"last_assistant_message":"做完了"`) {
		t.Fatalf("标准输入不对（同一文件只列一次、续跑过 stop_hook_active 为真）：%s", in)
	}
}

func TestDedupeAndParallelLimit(t *testing.T) {
	ctx := context.Background()
	var hooks []string
	for i := 0; i < 6; i++ {
		hooks = append(hooks, `{"type":"command","command":"cmd`+string(rune('a'+i))+`"}`)
	}
	hooks = append(hooks, hooks[0], hooks[1]) // 重复的只跑一次
	global := `{"hooks":{"PostToolUse":[{"hooks":[` + strings.Join(hooks, ",") + `]}]}}`
	r := newRig(t, nil, global, func(o *Options) { o.MaxParallel = 2 })
	r.fake.hold = 60 * time.Millisecond
	r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return 0, "x", "", nil }
	notes, _ := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	if r.fake.count() != 6 || len(notes) != 6 {
		t.Fatalf("按命令去重：跑了 %d 次，尾注 %d 条", r.fake.count(), len(notes))
	}
	if peak := r.fake.peak.Load(); peak != 2 {
		t.Fatalf("并发峰值 %d，上限是 2", peak)
	}
	// 尾注按配置顺序，不按谁先跑完
	for i, n := range notes {
		if !strings.Contains(n, "cmd"+string(rune('a'+i))) {
			t.Errorf("第 %d 条尾注顺序不对：%s", i, n)
		}
	}
}

func TestCancelledHookGivesNoNote(t *testing.T) {
	r := newRig(t, nil, gofmtHook, nil)
	r.fake.hold = 30 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	notes, err := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	if err != nil || len(notes) != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("%v %v %v", notes, err, time.Since(start))
	}
	if rows := r.sink.classes(); len(rows) != 1 || !strings.Contains(rows[0], taskrun.OutcomeCancelled) {
		t.Fatalf("%v", rows)
	}
}

func TestSlotWaitHonoursCancel(t *testing.T) {
	r := newRig(t, nil, gofmtHook, func(o *Options) { o.MaxParallel = 1 })
	r.p.slots <- struct{}{} // 名额被占满
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	notes, _ := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	if len(notes) != 0 || r.fake.count() != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("等名额时也要响应停止：%v", notes)
	}
}

// 配置报告：坏配置整个文件不跑、报一次；改好后下一次事件就生效；单条坏的只跳这一条
func TestLoaderReports(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, `{"hooks":`, nil)
	for i := 0; i < 3; i++ {
		r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	}
	if r.fake.count() != 0 {
		t.Fatal("坏配置不跑")
	}
	if rows := r.sink.classes(); len(rows) != 1 || !strings.HasSuffix(rows[0], "/"+taskrun.ClassConfigInvalid) {
		t.Fatalf("同一份内容只报一次：%v", rows)
	}
	good := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"ok-one"},{"type":"command","command":"bad","shell":"zsh"}]}]}}`
	r.write(r.global, good)
	future := time.Now().Add(time.Hour)
	os.Chtimes(r.global, future, future) // 文件系统时间精度不够时，靠修改时间变了认出改动
	notes, _ := r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	if r.fake.count() != 1 || r.fake.calls[0].Command != "ok-one" || len(notes) != 0 {
		t.Fatalf("改好后下一次事件就生效，坏的那条跳过：%d", r.fake.count())
	}
	if rows := r.sink.classes(); len(rows) != 3 { // config_invalid（第一份）、config_invalid（跳过的那条）、这次运行
		t.Fatalf("%v", rows)
	}
	// 目录当文件、超大文件都不当钩子读
	r.write(r.global, strings.Repeat(" ", maxConfigBytes+1))
	r.fake.calls = nil
	r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	if r.fake.count() != 0 {
		t.Fatal("超大配置不跑")
	}
}

func TestIfAndStopTools(t *testing.T) {
	ctx := context.Background()
	global := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"only-go","if":"Edit(*.go)"},{"type":"command","command":"only-git","if":"Bash(git *)"}]}]}}`
	r := newRig(t, nil, global, nil)
	r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.go"}`))
	r.p.onToolResult(ctx, r.toolEvent("console", "edit", `{"path":"a.md"}`))
	r.p.onToolResult(ctx, r.toolEvent("console", "powershell", `{"command":"git status"}`))
	r.p.onToolResult(ctx, r.toolEvent("console", "powershell", `{"command":"dir"}`))
	var got []string
	for _, c := range r.fake.calls {
		got = append(got, c.Command)
	}
	if strings.Join(got, ",") != "only-go,only-git" {
		t.Fatalf("%v", got)
	}
}

// 进度（过程气泡里的"钩子"一步）和时间线的段：真跑了才有，没匹配到什么都不留
func TestProgressAndSpans(t *testing.T) {
	r := newRig(t, nil, gofmtHook, nil)
	var steps []turn.ProcessStep
	var spans []string
	e := r.toolEvent("console", "edit", `{"path":"a.go"}`)
	e.Stage = turn.StageTools{
		Process: func(s turn.ProcessStep) { steps = append(steps, s) },
		Span: func(kind, name string) func(bool) {
			spans = append(spans, kind+"/"+name)
			return func(failed bool) { spans = append(spans, "end") }
		},
	}
	r.fake.reply = func(tools.ShellCommand) (int, string, string, error) { return 1, "a.go", "", nil }
	r.p.onToolResult(context.Background(), e)
	if len(steps) != 2 || steps[0].State != "running" || steps[1].State != "failed" || steps[0].ID != steps[1].ID || steps[0].Tool != "钩子" || !strings.Contains(steps[0].Target, "PostToolUse") {
		t.Fatalf("%+v", steps)
	}
	if len(spans) != 2 || spans[0] != "hook.post_tool/gofmt" || spans[1] != "end" {
		t.Fatalf("%v", spans)
	}
	steps, spans = nil, nil
	e2 := r.toolEvent("console", "read", `{"path":"a.go"}`)
	e2.Stage = e.Stage
	r.p.onToolResult(context.Background(), e2)
	if len(steps) != 0 || len(spans) != 0 {
		t.Fatal("没匹配到不该留进度和时间线")
	}
}
