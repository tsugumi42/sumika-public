package projectcontext

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

func wired(t *testing.T, root string, replies []llmtest.Reply, change func(*turn.Config), options func(*PluginOptions)) (*turntest.Kernel, *Plugin) {
	t.Helper()
	o := PluginOptions{Enabled: true, Trust: RootsTrust{root}, Resolver: Options{Approve: ReadApproval(nil)}}
	if options != nil {
		options(&o)
	}
	p := NewPlugin(o)
	k := turntest.New(t, turntest.Options{Replies: replies, Mutate: func(c *turn.Config) {
		c.Workdir = root
		c.Instructions = p
		c.Gate = &approval.Gate{Mode: "auto", Grants: approval.LoadGrants(filepath.Join(root, "grants.yml")), Conv: approval.NewConvGrants()}
		if change != nil {
			change(c)
		}
	}})
	if err := k.Hub.Use(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Hub.StopPlugin(p.ID()) })
	return k, p
}

func requireText(t *testing.T, k *turntest.Kernel, n int, want string) {
	t.Helper()
	var text strings.Builder
	for _, m := range k.Messages(n) {
		text.WriteString(m.Text)
	}
	if !strings.Contains(text.String(), want) {
		t.Fatalf("请求 %d 缺少 %q：%s", n, want, text.String())
	}
}
func fileText(t *testing.T, path string) string {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func editReply(id string) llmtest.Reply {
	return llmtest.Call(id, "edit", `{"path":"sub/target.txt","oldText":"before","newText":"after"}`)
}

func TestPluginSubdirectoryRead(t *testing.T) {
	for _, name := range []string{"read", "ls"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "AGENTS.md", "祖先规则")
			target := put(t, root, "sub/target.txt", "before")
			put(t, root, "sub/AGENTS.md", "子目录完整规则")
			path := "sub/target.txt"
			if name == "ls" {
				path = "sub"
			}
			k, _ := wired(t, root, []llmtest.Reply{llmtest.Call("r", name, fmt.Sprintf(`{"path":%q}`, path)), editReply("e"), llmtest.Text("完成")}, nil, nil)
			k.Send("先读后改")
			requireText(t, k, 0, "祖先规则")
			requireText(t, k, 1, "子目录完整规则")
			if fileText(t, target) != "after" {
				t.Fatal("接纳 read/ls 后编辑没有直接成功")
			}
			if strings.Contains(k.Bodies()[2], "本次没有写入") {
				t.Fatal("不应多拒绝一次")
			}
		})
	}
}

func TestPluginBeforeEdit(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			root := t.TempDir()
			target := put(t, root, "sub/target.txt", "before")
			put(t, root, "sub/AGENTS.md", "编辑必须参考的完整规则")
			observed := false
			k, p := wired(t, root, []llmtest.Reply{editReply("first"), editReply("retry"), llmtest.Text("完成")}, func(c *turn.Config) {
				if replacement {
					c.ExecTool = tools.Execute
				}
				c.OnTool = func(id, name string) {
					if id == "retry" {
						observed = true
						if fileText(t, target) != "before" {
							t.Error("首次编辑已经写入")
						}
					}
				}
			}, nil)
			k.Send("直接编辑")
			requireText(t, k, 1, "编辑必须参考的完整规则")
			requireText(t, k, 1, "本次没有写入")
			if !observed || fileText(t, target) != "after" {
				t.Fatal("未观察首次拦截或重调未成功")
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if len(p.resolvers) != 1 {
				t.Fatalf("同工作区应复用 Resolver：%d", len(p.resolvers))
			}
		})
	}
}

func TestPluginSameBatch(t *testing.T) {
	root := t.TempDir()
	target := put(t, root, "sub/target.txt", "before")
	put(t, root, "sub/AGENTS.md", "同批规则")
	first := llmtest.Call("r", "read", `{"path":"sub/target.txt"}`)
	first.ToolCalls = append(first.ToolCalls, editReply("same-batch").ToolCalls...)
	k, p := wired(t, root, []llmtest.Reply{first, editReply("next-call"), llmtest.Text("完成")}, func(c *turn.Config) {
		c.OnTool = func(id, name string) {
			if id == "next-call" && fileText(t, target) != "before" {
				t.Error("同批 edit 在接纳前已经写入")
			}
		}
	}, nil)
	k.Send("同批读改")
	if fileText(t, target) != "after" {
		t.Fatal("同批屏障没有进入或后续调用未写入")
	}
	requireText(t, k, 1, "本次没有写入")
	// 同批返回之前不能提交；read 的回调只能由 Hub 接纳。
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.contexts) != 1 {
		t.Fatal("上下文账本丢失")
	}
}

func TestPluginNoReadWithoutTrust(t *testing.T) {
	for _, kind := range []string{"disabled", "untrusted", "empty", "qq"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "AGENTS.md", "绝不能读取的规则")
			reads := 0
			p := NewPlugin(PluginOptions{Enabled: kind != "disabled", Trust: RootsTrust{t.TempDir()}, Resolver: Options{Open: func(path string) (io.ReadCloser, error) { reads++; return os.Open(path) }}})
			p.active = true
			if kind == "empty" {
				p.opts.Trust = RootsTrust{}
			}
			if kind == "qq" {
				p.opts.Trust = RootsTrust{root}
			}
			in := turn.ContextIn{ContextID: "ctx", Workspace: turn.Workspace{ID: "w", Root: root, CWD: root}, Audience: audience.Console("local")}
			if kind == "qq" {
				in.Audience.Channel = "qq"
			}
			c, err := p.onContext(context.Background(), in)
			if err != nil || c != nil || reads != 0 || len(p.resolvers) != 0 {
				t.Fatalf("拒绝来源不应读正文：贡献 %+v，错误 %v，读取 %d", c, err, reads)
			}
		})
	}
}

func TestPluginProtectedRead(t *testing.T) {
	for _, kind := range []string{"workspace", "import", "user"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			protected := t.TempDir()
			reads := []string{}
			o := PluginOptions{Enabled: true, Trust: RootsTrust{root}, Resolver: Options{Approve: ReadApproval(dirs(protected)), Open: func(path string) (io.ReadCloser, error) { reads = append(reads, path); return os.Open(path) }}}
			switch kind {
			case "workspace":
				protected = root
				o.Resolver.Approve = ReadApproval(dirs(root))
				put(t, root, "AGENTS.md", "受保护正文")
			case "import":
				protected = filepath.Join(root, "secret")
				o.Resolver.Approve = ReadApproval(dirs(protected))
				put(t, root, "AGENTS.md", "@secret/rules.md")
				put(t, root, "secret/rules.md", "受保护正文")
			case "user":
				o.UserRoot = protected
				put(t, protected, "AGENTS.md", "受保护正文")
			}
			p := NewPlugin(o)
			p.active = true
			in := turn.ContextIn{ContextID: "ctx", Workspace: turn.Workspace{ID: "w", Root: root, CWD: root}, Audience: audience.Console("local")}
			for range 2 {
				c, e := p.onContext(context.Background(), in)
				if e != nil {
					t.Fatal(e)
				}
				if c == nil || !strings.Contains(strings.Join(c.Lines, "\n"), "审批未通过") || strings.Contains(strings.Join(c.Lines, "\n"), "受保护正文") {
					t.Fatalf("受保护资料没有明确拒绝：%+v", c)
				}
				if c.OnAccepted != nil {
					c.OnAccepted()
				}
			}
			for _, path := range reads {
				if inside(protected, path) {
					t.Fatalf("审批前已读 %s", path)
				}
			}
			b := p.contexts["ctx"]
			expectStatus(t, b.resolver.BeforeWrite(context.Background(), "ctx", b.ledger, filepath.Join(root, "target")), RulesIncomplete)
		})
	}
}

func TestPluginContextGeneration(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "当前规则")
	p := NewPlugin(PluginOptions{Enabled: true, Trust: RootsTrust{root}})
	p.active = true
	in := turn.ContextIn{CharacterID: "编的角色", Conversation: "local", ContextID: "old", Workspace: turn.Workspace{ID: "w", Root: root, CWD: root}, Audience: audience.Console("local")}
	old, e := p.onContext(context.Background(), in)
	if e != nil || old == nil {
		t.Fatal(e)
	}
	oldBinding := p.contexts["old"]
	in.ContextID = "new"
	fresh, e := p.onContext(context.Background(), in)
	if e != nil || fresh == nil {
		t.Fatal("换代没有重新给规则", e)
	}
	old.OnAccepted()
	if len(oldBinding.ledger.contexts) != 0 || p.contexts["old"] != nil {
		t.Fatal("旧 ContextID 贡献在换代后仍提交")
	}
	fresh.OnAccepted()
	next, e := p.onContext(context.Background(), in)
	if e != nil || next != nil {
		t.Fatal("新账本没提交或重复追加", e)
	}
}

func TestPluginBeforeWrite(t *testing.T) {
	root := t.TempDir()
	target := put(t, root, "sub/target.txt", "before")
	put(t, root, "sub/AGENTS.md", "写入所需规则")
	k, _ := wired(t, root, []llmtest.Reply{
		llmtest.Call("first", "write", `{"path":"sub/target.txt","content":"after"}`),
		llmtest.Call("retry", "write", `{"path":"sub/target.txt","content":"after"}`), llmtest.Text("完成"),
	}, func(c *turn.Config) {
		c.OnTool = func(id, name string) {
			if id == "retry" && fileText(t, target) != "before" {
				t.Error("首次 write 已写入")
			}
		}
	}, nil)
	k.Send("直接写入")
	requireText(t, k, 1, "写入所需规则")
	requireText(t, k, 1, "本次没有写入")
	if fileText(t, target) != "after" {
		t.Fatal("接纳后 write 未成功")
	}
}

func TestPluginOnlyRelevantSuccessfulTools(t *testing.T) {
	for _, name := range []string{"find", "grep", "powershell", "read", "ls"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "sub/AGENTS.md", "不应自动扫描")
			reads := 0
			p := NewPlugin(PluginOptions{Enabled: true, Trust: RootsTrust{root}, Resolver: Options{Open: func(path string) (io.ReadCloser, error) { reads++; return os.Open(path) }}})
			p.active = true
			in := turn.ContextIn{ContextID: "ctx", Workspace: turn.Workspace{ID: "w", Root: root, CWD: root}, Audience: audience.Console("local")}
			if _, err := p.onContext(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			args, _ := jsjson.Parse(`{"path":"sub"}`)
			r, err := p.Execute(context.Background(), in, name, args, func(context.Context) (tools.Result, error) {
				return tools.Result{Text: "普通结果", IsError: name == "read" || name == "ls"}, nil
			})
			if err != nil || r.Text != "普通结果" || reads != 0 || r.OnAccepted != nil {
				t.Fatalf("%s 不应触发说明：%+v %v，读取 %d", name, r, err, reads)
			}
		})
	}
}
