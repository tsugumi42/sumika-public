package turn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// 按会话选工作目录（规格-按会话选工作目录）：内核每轮冻结时问宿主这个会话绑了哪个目录。

type fakeBindings struct {
	mu    sync.Mutex
	dirs  map[string]string
	asked []string
	err   error
}

func (f *fakeBindings) set(conv, dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[conv] = dir
}
func (f *fakeBindings) lookup(_ context.Context, conv string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, conv)
	return f.dirs[conv], f.err
}

// settle 等一轮结束，出错也交回来看（wait 遇到错误直接失败）。
func settle(t *testing.T, ch <-chan Outcome) Outcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(2 * time.Minute):
		t.Fatal("这一轮 2 分钟没回完")
		return Outcome{}
	}
}

func readBatch(id string) string {
	return fmt.Sprintf(`{"toolCalls":[{"id":%q,"name":"read","args":["{\"path\":\"same.txt\"}"]}],`+usage+`}`, id)
}

func sessionHarness(t *testing.T, replies string, b *fakeBindings, execs *int) *harness {
	t.Helper()
	var mu sync.Mutex
	return newHarnessOpts(t, harnessOpts{replies: replies, mutate: func(c *Config) {
		c.WorkspaceFor = b.lookup
		c.ExecTool = func(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
			mu.Lock()
			*execs++
			mu.Unlock()
			return tools.Execute(ctx, cwd, name, args)
		}
	}})
}

func toolTexts(h *harness, conv string) []string {
	l := h.hub.live[liveKey(Input{CharacterID: h.c.CharacterID, Conversation: conv, Scope: ConsoleScope, Audience: audience.Console(conv)})]
	var out []string
	if l != nil {
		for _, m := range l.msgs {
			if m.Role == "toolResult" {
				out = append(out, m.Text)
			}
		}
	}
	return out
}

func writeSame(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 验收 1：两个会话各绑一个项目，同时跑，各读各的。
func TestSessionWorkspaceTwoProjects(t *testing.T) {
	t.Parallel()
	b := &fakeBindings{dirs: map[string]string{}}
	execs := 0
	h := sessionHarness(t, `[`+readBatch("a")+`,`+readBatch("b")+`,`+textReply+`,`+textReply+`]`, b, &execs)
	dirs := []string{t.TempDir(), t.TempDir()}
	outs := make([]<-chan Outcome, 2)
	for i, dir := range dirs {
		writeSame(t, dir, fmt.Sprintf("项目%d", i))
		conv := fmt.Sprintf("console:proj%d", i)
		b.set(conv, dir)
		outs[i] = h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Text: fmt.Sprint(i), Audience: audience.Console(conv), Scope: ConsoleScope})
	}
	for _, out := range outs {
		if o := settle(t, out); o.Err != nil {
			t.Fatal(o.Err)
		}
	}
	for i := range dirs {
		texts := toolTexts(h, fmt.Sprintf("console:proj%d", i))
		if len(texts) != 1 || !strings.Contains(texts[0], fmt.Sprintf("项目%d", i)) {
			t.Errorf("会话 %d 读到别处：%q", i, texts)
		}
	}
}

// 验收 2：改目录从下一轮生效；同一个会话换目录会重建实例。
func TestSessionWorkspaceChangeWaitsForNextTurn(t *testing.T) {
	t.Parallel()
	b := &fakeBindings{dirs: map[string]string{}}
	execs := 0
	h := sessionHarness(t, `[`+readBatch("a")+`,`+textReply+`,`+readBatch("b")+`,`+textReply+`]`, b, &execs)
	a, c := t.TempDir(), t.TempDir()
	writeSame(t, a, "旧项目")
	writeSame(t, c, "新项目")
	conv := "console:change"
	b.set(conv, a)
	send := func(text string) {
		if o := settle(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Text: text, Audience: audience.Console(conv), Scope: ConsoleScope})); o.Err != nil {
			t.Fatal(o.Err)
		}
	}
	send("一")
	b.set(conv, c) // 两轮之间改绑定
	send("二")
	joined := strings.Join(toolTexts(h, conv), "|")
	if !strings.Contains(joined, "新项目") {
		t.Fatalf("第二轮没有用新目录：%q", joined)
	}
}

// 验收 3：绑定的目录用不了，这一轮不跑，工具一次没执行，不退到缺省目录。
func TestSessionWorkspaceMissingRefuses(t *testing.T) {
	t.Parallel()
	b := &fakeBindings{dirs: map[string]string{}}
	execs := 0
	h := sessionHarness(t, `[`+readBatch("a")+`,`+textReply+`]`, b, &execs)
	gone := filepath.Join(t.TempDir(), "已删除的项目")
	file := filepath.Join(t.TempDir(), "是文件.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"不存在": gone, "是文件": file, "相对路径": "relative/dir"}
	for name, dir := range cases {
		conv := "console:bad-" + name
		b.set(conv, dir)
		o := settle(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Text: "在吗", Audience: audience.Console(conv), Scope: ConsoleScope}))
		if o.Err == nil || !strings.Contains(o.Err.Error(), dir) {
			t.Errorf("%s：应当报错并带路径：%v", name, o.Err)
		}
	}
	if execs != 0 {
		t.Fatalf("目录不可用时工具不能执行：%d", execs)
	}
}

// 读绑定失败也不回退。
func TestSessionWorkspaceLookupErrorDoesNotFallBack(t *testing.T) {
	t.Parallel()
	b := &fakeBindings{dirs: map[string]string{}, err: errors.New("读绑定失败")}
	execs := 0
	h := sessionHarness(t, `[`+readBatch("a")+`,`+textReply+`]`, b, &execs)
	conv := "console:lookup"
	o := settle(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Text: "在吗", Audience: audience.Console(conv), Scope: ConsoleScope}))
	if o.Err == nil || !strings.Contains(o.Err.Error(), "读绑定失败") || execs != 0 {
		t.Fatalf("读失败应当让这一轮不跑：%v execs=%d", o.Err, execs)
	}
}

// 本地群聊、QQ 不问绑定：群里没有目录可设；QQ 的工作区清零。
func TestSessionWorkspaceHookOnlyForOwnerPrivate(t *testing.T) {
	t.Parallel()
	plain := t.TempDir()
	b := &fakeBindings{dirs: map[string]string{"console:x:group:g": t.TempDir(), "qq:1": t.TempDir()}}
	cfg := Config{Workdir: plain, WorkspaceFor: b.lookup}
	group := Input{Conversation: "console:x:group:g", Audience: audience.Audience{Channel: "console", Conversation: "console:x:group:g", Scene: "group", PrivateOK: true}}
	got, err := group.freezeWorkspace(context.Background(), cfg)
	if err != nil || got.Workspace.CWD != plain {
		t.Fatalf("本地群聊应当用缺省目录：%+v %v", got.Workspace, err)
	}
	qq := Input{Conversation: "qq:1", Audience: audience.Audience{Channel: "qq", Scene: "private", PrivateOK: true}}
	got, err = qq.freezeWorkspace(context.Background(), cfg)
	if err != nil || got.Workspace != (Workspace{}) {
		t.Fatalf("QQ 不该有工作区：%+v %v", got.Workspace, err)
	}
	if len(b.asked) != 0 {
		t.Fatalf("群聊和 QQ 不该问绑定：%v", b.asked)
	}
	own := Input{Conversation: "console:x", Audience: audience.Console("console:x")}
	if got, err = own.freezeWorkspace(context.Background(), cfg); err != nil || got.Workspace.CWD != plain || len(b.asked) != 1 {
		t.Fatalf("主人私聊没绑定应当用缺省并问一次：%+v %v %v", got.Workspace, err, b.asked)
	}
	// 已经带目录的（在跑的实例、子任务继承）不再问
	own.Workspace = Workspace{CWD: b.dirs["qq:1"]}
	if _, err = own.freezeWorkspace(context.Background(), cfg); err != nil || len(b.asked) != 1 {
		t.Fatalf("带目录的不该再问：%v %v", err, b.asked)
	}
}

func TestCheckDir(t *testing.T) {
	dir := t.TempDir()
	if err := CheckDir(dir); err != nil {
		t.Fatalf("好目录：%v", err)
	}
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"相对": "a/b", "文件": f, "不存在": filepath.Join(dir, "无"), "空串": ""} {
		if err := CheckDir(p); err == nil {
			t.Errorf("%s 应当拒绝", name)
		}
	}
	oldList, oldTimeout := listDirNames, dirCheckTimeout
	t.Cleanup(func() { listDirNames, dirCheckTimeout = oldList, oldTimeout })
	block := make(chan struct{})
	defer close(block)
	listDirNames, dirCheckTimeout = func(string) error { <-block; return nil }, 20*time.Millisecond
	if err := CheckDir(dir); err == nil || !strings.Contains(err.Error(), "没有响应") {
		t.Fatalf("卡住的路径应当报没有响应：%v", err)
	}
	listDirNames, dirCheckTimeout = func(string) error { return os.ErrPermission }, time.Second
	if err := CheckDir(dir); err == nil || !strings.Contains(err.Error(), "没有权限") {
		t.Fatalf("列不出应当报没有权限：%v", err)
	}
}
