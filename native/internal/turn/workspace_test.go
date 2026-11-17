package turn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
)

func TestWorkspaceRootAndChannelBinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cwd := filepath.Join(root, "nested", "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Dir(cwd)
	if err := os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspace("project", cwd)
	if err != nil || w.Root != inner || w.CWD != cwd || w.ID != "project" {
		t.Fatalf("最近的 .git 文件应当截住根：%+v %v", w, err)
	}
	if err := os.Remove(filepath.Join(inner, ".git")); err != nil {
		t.Fatal(err)
	}
	w, err = NewWorkspace("project", cwd)
	if err != nil || w.Root != root {
		t.Fatalf(".git 目录根不对：%+v %v", w, err)
	}
	plain := t.TempDir()
	w, err = NewWorkspace("plain", plain)
	if err != nil || w.Root != plain {
		t.Fatalf("无仓库应当只用 cwd：%+v %v", w, err)
	}
	for _, scene := range []string{"private", "group"} {
		for _, channel := range []string{"console", "qq"} {
			in := Input{Conversation: "编的会话", Audience: audience.Audience{Channel: channel, Scene: scene}}
			got, err := in.freezeWorkspace(context.Background(), Config{Workdir: plain})
			if err != nil {
				t.Fatal(err)
			}
			if channel == "console" && got.Workspace.CWD != plain || channel == "qq" && got.Workspace != (Workspace{}) {
				t.Fatalf("%s/%s 工作区错误：%+v", channel, scene, got.Workspace)
			}
			in.Workspace = Workspace{ID: "explicit", CWD: cwd}
			got, err = in.freezeWorkspace(context.Background(), Config{Workdir: plain})
			if err != nil {
				t.Fatal(err)
			}
			if channel == "console" && got.Workspace.CWD != cwd || channel == "qq" && got.Workspace != (Workspace{}) {
				t.Fatalf("%s/%s 显式工作区错误：%+v", channel, scene, got.Workspace)
			}
		}
	}
	if _, err := NewWorkspace("bad", "relative"); err == nil {
		t.Fatal("相对工作目录不能取进程 cwd")
	}
	if w, err := NewWorkspace("broken", filepath.Join(root, "bad\x00")); err == nil || w != (Workspace{}) {
		t.Fatalf("探测出错不能跳过并沿父目录找根：%+v %v", w, err)
	}
}

// 真读写、真 PowerShell 走同一批调用；替换执行器仍转到真实工具，验证传参未漏掉。
func TestWorkspaceToolsAndApprovalStayInSession(t *testing.T) {
	t.Parallel()
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("替换执行器=%v", replacement), func(t *testing.T) {
			h := newHarness(t, false, nil)
			cfg := h.hub.Cfg
			if replacement {
				cfg.ExecTool = func(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
					return tools.Execute(ctx, cwd, name, args)
				}
			}
			dirs := []string{t.TempDir(), t.TempDir()}
			for i, dir := range dirs {
				if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte(fmt.Sprintf("session-%d", i)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg.Gate.ProtectedDirs = []string{dirs[1]}
			var mu sync.Mutex
			var cards []approval.Request
			cfg.Gate.Ask = func(_ context.Context, r approval.Request, _ *approval.Pending) approval.Decision {
				mu.Lock()
				cards = append(cards, r)
				mu.Unlock()
				return approval.Decision{Kind: "approved"}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := make(chan struct{})
			type result struct {
				i    int
				msgs []Msg
				err  error
			}
			done := make(chan result, 2)
			for i, dir := range dirs {
				identity := kernel.Invocation{RootID: fmt.Sprintf("workspace-root-%d", i), RunID: "turn-ws", Kind: "foreground", Scope: kernel.Scope{CharacterID: h.c.CharacterID, Conversation: fmt.Sprintf("console:ws%d", i)}}
				if err := AdmitBudgetRoot(ctx, cfg.Budget, identity, cfg.BudgetConfig.Limits(time.Now())); err != nil {
					t.Fatal(err)
				}
				toolCtx := kernel.WithInvocation(ctx, identity)
				go func(ctx context.Context) {
					<-start
					args := func(raw string) jsjson.Value {
						v, err := jsjson.Parse(raw)
						if err != nil {
							panic(err)
						}
						return v
					}
					// 先并行读，再串行写和运行命令，覆盖两条执行分支。
					calls := Msg{Calls: []Call{{ID: fmt.Sprintf("r%d", i), Name: "read", Args: args(`{"path":"same.txt"}`)}, {ID: fmt.Sprintf("l%d", i), Name: "ls", Args: args(`{"path":"."}`)}}}
					w := Workspace{ID: fmt.Sprint(i), CWD: dir, Root: dir}
					ms, err := runTools(ctx, cfg, w, "context", h.st, h.hub.plugins, calls, "toolUse", h.c.CharacterID, fmt.Sprintf("console:ws%d", i), audience.Console(""), tools.OwnerConsole(), nil, "turn-ws", &repeatGuard{}, nil)
					if err == nil {
						calls.Calls = []Call{{ID: fmt.Sprintf("w%d", i), Name: "write", Args: args(`{"path":"same.txt","content":"updated"}`)}, {ID: fmt.Sprintf("p%d", i), Name: "powershell", Args: args(`{"command":"(Get-Location).Path; Set-Content -LiteralPath shell.txt -Value shell-done"}`)}}
						var more []Msg
						more, err = runTools(ctx, cfg, w, "context", h.st, h.hub.plugins, calls, "toolUse", h.c.CharacterID, fmt.Sprintf("console:ws%d", i), audience.Console(""), tools.OwnerConsole(), nil, "turn-ws", &repeatGuard{}, nil)
						ms = append(ms, more...)
					}
					done <- result{i, ms, err}
				}(toolCtx)
			}
			close(start)
			for range dirs {
				select {
				case r := <-done:
					if r.err != nil || len(r.msgs) != 4 {
						t.Fatalf("会话 %d 工具未完成：%v %+v", r.i, r.err, r.msgs)
					}
					if !strings.Contains(r.msgs[0].Text, fmt.Sprintf("session-%d", r.i)) || !strings.Contains(strings.ToLower(r.msgs[3].Text), strings.ToLower(dirs[r.i])) {
						t.Errorf("会话 %d 读或命令串目录：%+v", r.i, r.msgs)
					}
					for _, f := range []string{"same.txt", "shell.txt"} {
						b, err := os.ReadFile(filepath.Join(dirs[r.i], f))
						want := "updated"
						if f == "shell.txt" {
							want = "shell-done"
						}
						if err != nil || strings.TrimSpace(string(b)) != want {
							t.Errorf("会话 %d 写入 %s 不对：%q %v", r.i, f, b, err)
						}
					}
				case <-ctx.Done():
					t.Fatal("两个工作区调用未及时退出")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			readA, readB, writes := false, false, 0
			writeDirs := map[string]bool{}
			for _, c := range cards {
				if c.Tool == "read" {
					readA = readA || c.Subject == filepath.Join(dirs[0], "same.txt")
					readB = readB || c.Subject == filepath.Join(dirs[1], "same.txt")
				}
				if c.Tool == "write" {
					writes++
					writeDirs[c.Subject] = true
					for i, dir := range dirs {
						if c.Subject == filepath.Join(dir, "same.txt") && (c.Preview == nil || !strings.Contains(c.Preview.Text, fmt.Sprintf("session-%d", i))) {
							t.Errorf("审批预览用了别处的旧内容：%+v", c)
						}
					}
				}
			}
			if readA || !readB || writes != 2 || !writeDirs[filepath.Join(dirs[0], "same.txt")] || !writeDirs[filepath.Join(dirs[1], "same.txt")] {
				t.Fatalf("受保护目录判定或写预览串会话：readA=%v readB=%v writes=%d，卡片=%+v", readA, readB, writes, cards)
			}
			if cfg.Workdir != h.wd {
				t.Fatal("调用改动了缺省工作目录")
			}
		})
	}
}

func TestControlledToolResultsSurviveSlim(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("受控资料\n", 60)
	msgs := []Msg{{Role: "assistant", Calls: []Call{{ID: "controlled"}, {ID: "ordinary"}}}, {Role: "toolResult", CallID: "controlled", Text: body, Controlled: true}, {Role: "toolResult", CallID: "ordinary", Text: body}, {Role: "user", Text: "下一轮"}}
	b := &Built{Soft: 1}
	sent, n := b.slim(msgs)
	if n != 1 || sent[1].Text != body || sent[2].Text == body || msgs[2].Text != body {
		t.Fatalf("受控资料应逐字保留，普通结果仅改当次请求：n=%d sent=%+v", n, sent)
	}
	var forged Msg
	if err := json.Unmarshal([]byte(`{"Controlled":true,"Text":"外来文字"}`), &forged); err != nil {
		t.Fatal(err)
	}
	if forged.Controlled {
		t.Fatal("外来 JSON 伪造了宿主标记")
	}
}

func TestWorkspaceBindingReachesHubTools(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	started := 0
	all := make(chan struct{})
	batch := func(id string) string {
		return fmt.Sprintf(`{"toolCalls":[{"id":%q,"name":"read","args":["{\"path\":\"same.txt\"}"]}],`+usage+`}`, id)
	}
	h := newHarnessOpts(t, harnessOpts{replies: `[` + batch("a") + `,` + batch("b") + `,` + textReply + `,` + textReply + `]`, mutate: func(c *Config) {
		c.ExecTool = func(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
			mu.Lock()
			started++
			if started == 2 {
				close(all)
			}
			mu.Unlock()
			select {
			case <-all:
			case <-ctx.Done():
				return tools.Result{}, ctx.Err()
			case <-time.After(barrierLimit):
				return tools.Result{}, fmt.Errorf("另一会话没有开始工具")
			}
			return tools.Execute(ctx, cwd, name, args)
		}
	}})
	dirs := []string{t.TempDir(), t.TempDir()}
	var bindings []ContextIn
	h.use(contextPlugin("workspace", func(_ context.Context, in ContextIn) (*Contribution, error) {
		mu.Lock()
		bindings = append(bindings, in)
		mu.Unlock()
		return nil, nil
	}))
	outs := make([]<-chan Outcome, 2)
	for i, dir := range dirs {
		if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte(fmt.Sprintf("独立目录%d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		conv := fmt.Sprintf("console:workspace%d", i)
		outs[i] = h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Text: fmt.Sprint(i), Audience: audience.Console(conv), Scope: ConsoleScope, Workspace: Workspace{ID: conv, CWD: dir, Root: "外来根不能代替宿主探测"}})
	}
	for _, out := range outs {
		wait(t, out)
	}
	if len(bindings) != 2 {
		t.Fatalf("没有收到两个会话的冻结绑定：%+v", bindings)
	}
	for _, in := range bindings {
		i := 0
		if in.Text == "1" {
			i = 1
		}
		if in.Workspace.CWD != dirs[i] || in.Workspace.Root != dirs[i] || in.ContextID == "" {
			t.Errorf("宿主绑定不对：%+v", in)
		}
		l := h.hub.live[liveKey(Input{CharacterID: in.CharacterID, Conversation: in.Conversation, Scope: in.Scope, Audience: in.Audience})]
		found := false
		for _, msg := range l.msgs {
			if msg.Role == "toolResult" {
				found = true
				if !strings.Contains(msg.Text, fmt.Sprintf("独立目录%d", i)) {
					t.Errorf("会话工具读了别处：%+v", msg)
				}
			}
		}
		if !found {
			t.Error("实例没有接纳真实文件结果")
		}
	}
}

func TestControlledToolResultsReachModel(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read", "write"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := strings.Repeat("正文必须逐字保留\n", 60)
			calls := fmt.Sprintf(`{"toolCalls":[{"id":"controlled","name":%q,"args":["{\"path\":\"c.txt\",\"content\":\"x\"}"]},{"id":"ordinary","name":%q,"args":["{\"path\":\"o.txt\",\"content\":\"x\"}"]}],`+usage+`}`, name, name)
			h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + calls + `,` + textReply + `,` + textReply + `]`, mutate: func(c *Config) {
				c.ExecTool = func(_ context.Context, _, _ string, args jsjson.Value) (tools.Result, error) {
					return tools.Result{Text: body, Controlled: args.Obj["path"].Str == "c.txt"}, nil
				}
			}})
			h.send("第一轮")
			// refresh 会重算软门槛；仅压低比率，硬门槛不改。
			h.hub.Cfg.Context.SoftRatio = 0.000001
			h.send("第二轮")
			got, _ := h.toolMsgs(2)
			if got["controlled"] != body || got["ordinary"] == body || !strings.Contains(got["ordinary"], "较早的工具结果") {
				t.Fatalf("发给模型的受控正文或普通精简不对：%v", got)
			}
		})
	}
}
