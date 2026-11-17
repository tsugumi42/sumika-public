package projectcontext

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

type instructionFunc func(context.Context, turn.ContextIn, string, jsjson.Value, func(context.Context) (tools.Result, error)) (tools.Result, error)

func (f instructionFunc) Execute(ctx context.Context, in turn.ContextIn, name string, args jsjson.Value, exec func(context.Context) (tools.Result, error)) (tools.Result, error) {
	return f(ctx, in, name, args, exec)
}
func signal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("未到预期屏障")
	}
}

func TestPluginCancelBeforeAcceptance(t *testing.T) {
	for _, action := range []string{"cancel", "timeout", "stop-plugin", "discard"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "sub/target.txt", "before")
			put(t, root, "sub/AGENTS.md", "尚未接纳规则")
			returned := make(chan struct{})
			release := make(chan struct{})
			k, p := wired(t, root, []llmtest.Reply{llmtest.Call("r", "read", `{"path":"sub/target.txt"}`), llmtest.Text("完成")}, nil, nil)
			var id string
			k.Hub.Cfg.Instructions = instructionFunc(func(ctx context.Context, in turn.ContextIn, name string, args jsjson.Value, exec func(context.Context) (tools.Result, error)) (tools.Result, error) {
				r, e := p.Execute(ctx, in, name, args, exec)
				id = in.ContextID
				close(returned)
				if action == "timeout" || action == "cancel" {
					<-ctx.Done()
				} else {
					<-release
				}
				if action == "discard" {
					return tools.Result{}, fmt.Errorf("丢弃已返回结果")
				}
				return r, e
			})
			if action == "timeout" {
				k.Hub.Cfg.ToolTimeout = func(string, time.Duration) time.Duration { return 50 * time.Millisecond }
			}
			out := k.Hub.Send(turntest.CharacterID, "开始读取")
			signal(t, returned)
			p.mu.Lock()
			b := p.contexts[id]
			p.mu.Unlock()
			if action == "cancel" {
				k.Hub.Stop(turntest.CharacterID)
			}
			if action == "stop-plugin" {
				k.Hub.StopPlugin(p.ID())
				close(release)
			}
			if action == "discard" {
				close(release)
			}
			select {
			case result := <-out:
				if action == "discard" && result.Err == nil {
					t.Fatal("预期结果丢弃错误")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("取消/超时没收尾")
			}
			expectStatus(t, b.resolver.BeforeWrite(context.Background(), id, b.ledger, filepath.Join(root, "sub/target.txt")), NeedContext)
			if action == "stop-plugin" {
				for _, body := range k.Bodies() {
					if strings.Contains(body, "尚未接纳规则") {
						t.Fatal("停用后正文仍进请求")
					}
				}
			}
		})
	}
}

func TestPluginCancelAfterAcceptance(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sub/target.txt", "before")
	put(t, root, "sub/AGENTS.md", "已接纳规则")
	reached := make(chan struct{})
	var p *Plugin
	k := turntest.New(t, turntest.Options{Replies: []llmtest.Reply{llmtest.Call("r", "read", `{"path":"sub/target.txt"}`)}, Before: func(ctx context.Context, n int, body string) {
		if n == 2 {
			close(reached)
			<-ctx.Done()
		}
	}})
	// 用 wired 的设置构造相同的真实工具环境，不触及真实审批目录。
	p = NewPlugin(PluginOptions{Enabled: true, Trust: RootsTrust{root}})
	k.Hub.Cfg.Workdir = root
	k.Hub.Cfg.Instructions = p
	k.Hub.Cfg.Gate = &approval.Gate{Mode: "auto", Grants: approval.LoadGrants(filepath.Join(root, "grants.yml")), Conv: approval.NewConvGrants()}
	if e := k.Hub.Use(p); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { k.Hub.StopPlugin(p.ID()) })
	out := k.Hub.Send(turntest.CharacterID, "读取后取消")
	signal(t, reached)
	k.Hub.Stop(turntest.CharacterID)
	k.Wait(out)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.contexts) != 1 {
		t.Fatalf("已接纳实例应保留账本，得到 %d 个", len(p.contexts))
	}
	for id, b := range p.contexts {
		expectStatus(t, b.resolver.BeforeWrite(context.Background(), id, b.ledger, filepath.Join(root, "sub/target.txt")), CanWrite)
	}
}

func TestPluginSoftThreshold(t *testing.T) {
	root := t.TempDir()
	put(t, root, "sub/target.txt", strings.Repeat("普通输出", 200))
	put(t, root, "sub/AGENTS.md", strings.Repeat("完整规则", 100))
	k, _ := wired(t, root, []llmtest.Reply{llmtest.Call("r", "read", `{"path":"sub/target.txt"}`), llmtest.Text("已读")}, func(c *turn.Config) {
		c.Context = turn.ContextLimits{Window: 100000, SoftRatio: 0.001, HardRatio: 0.99, Reserve: 1000, KeepTokens: 20000, NoAutoCompact: true}
	}, nil)
	k.Send("读一下")
	var original string
	for _, m := range k.Messages(1) {
		if m.Role == "tool" {
			original = m.Text
		}
	}
	if original == "" {
		t.Fatal("说明未成为工具结果")
	}
	k.Send("下一轮")
	requireText(t, k, 2, strings.Repeat("完整规则", 100))
	requireText(t, k, 2, "较早的工具结果")
	for _, m := range k.Messages(2) {
		if m.Role == "tool" {
			idx := strings.Index(original, "\n\n")
			if idx < 0 || !strings.HasSuffix(m.Text, original[idx:]) {
				t.Fatal("精简改动了资料尾部")
			}
			if strings.Contains(m.Text, strings.Repeat("普通输出", 200)) {
				t.Fatal("普通输出没有精简")
			}
		}
	}
}

func TestPluginUpdatesAndBudget(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "旧祖先说明")
	target := put(t, root, "sub/target.txt", "before")
	put(t, root, "sub/AGENTS.md", "子目录说明")
	k, _ := wired(t, root, []llmtest.Reply{llmtest.Call("r", "read", `{"path":"sub/target.txt"}`), llmtest.Text("已读"), llmtest.Text("新版收到"), editReply("overflow"), llmtest.Text("等待精简")}, nil, nil)
	k.Send("先读")
	prefix := k.Messages(1)
	put(t, root, "sub/AGENTS.md", "外部更新后的完整子目录说明")
	k.Send("核对新版")
	next := k.Messages(2)
	for i, m := range prefix {
		if next[i].Raw != m.Raw {
			t.Fatalf("旧请求前缀第 %d 条被改写", i)
		}
	}
	requireText(t, k, 2, "外部更新后的完整子目录说明")
	put(t, root, "AGENTS.md", strings.Repeat("祖", 6000))
	put(t, root, "sub/AGENTS.md", strings.Repeat("子", 6000))
	k.Send("现在编辑")
	requireText(t, k, 4, "项目说明不完整")
	if fileText(t, target) != "before" {
		t.Fatal("合计超限仍写入")
	}
}

func TestPluginFourScenes(t *testing.T) {
	for _, channel := range []string{"console", "qq"} {
		for _, scene := range []string{"private", "group"} {
			t.Run(channel+"/"+scene, func(t *testing.T) {
				root := t.TempDir()
				put(t, root, "AGENTS.md", "只在授权本地出现的说明")
				k, _ := wired(t, root, nil, nil, nil)
				conv := channel + ":" + scene + ":invented"
				a := audience.Audience{Channel: channel, Scene: scene, PrivateOK: true, OwnerAsked: true}
				seq := k.Append(conv, "owner", "invented-owner", "开始")
				k.Wait(k.Hub.Respond(turn.Input{CharacterID: turntest.CharacterID, Conversation: conv, Scope: "full", Seqs: []int64{seq}, Text: "开始", Audience: a, Addressed: true, Workspace: turn.Workspace{ID: "w", Root: root, CWD: root}}))
				if channel == "console" && scene == "private" {
					requireText(t, k, 0, "只在授权本地出现的说明")
				} else {
					body := k.Bodies()[0]
					if strings.Contains(body, "只在授权本地出现的说明") || strings.Contains(body, strings.ReplaceAll(root, `\`, `\\`)) {
						t.Fatal("QQ 完整请求泄露本地资料或路径")
					}
					for _, m := range k.Messages(0) {
						if strings.Contains(m.Text, "只在授权本地出现的说明") || strings.Contains(m.Text, root) {
							t.Fatalf("QQ 泄露本地资料：%s", m.Text)
						}
					}
				}
			})
		}
	}
}

func TestPluginRestart(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "重建前规则")
	k, p := wired(t, root, nil, nil, nil)
	k.Send("第一轮")
	p.mu.Lock()
	var oldID string
	var old *binding
	for id, b := range p.contexts {
		oldID, old = id, b
	}
	p.mu.Unlock()
	put(t, root, "AGENTS.md", "重建后的最新规则")
	k.Hub.Restart(turntest.CharacterID)
	k.Send("重建后")
	requireText(t, k, 1, "重建后的最新规则")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.contexts[oldID] != nil || len(old.ledger.contexts) != 0 {
		t.Fatal("重建后旧账本仍在")
	}
}

func TestPluginCompaction(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "压缩前规则")
	k, p := wired(t, root, []llmtest.Reply{llmtest.Text("第一轮收到"), llmtest.Text("主人正在整理临时项目，已经核对目录规则，下一步继续处理文件并验证结果。"), llmtest.Text("继续")}, func(c *turn.Config) { c.Context.KeepTokens = 50 }, nil)
	for range 6 {
		k.Append("console:suzu", "owner", "invented-owner", strings.Repeat("临时项目资料", 100))
		k.Append("console:suzu", "character", turntest.CharacterID, strings.Repeat("编造的处理记录", 100))
	}
	k.Send("建立实例")
	p.mu.Lock()
	var oldID string
	var old *binding
	for id, b := range p.contexts {
		oldID, old = id, b
	}
	p.mu.Unlock()
	result, err := k.Hub.Compact(turntest.CharacterID, "console:suzu", "manual", 0)
	if err != nil || !result.OK {
		t.Fatalf("压缩没成功：%+v %v", result, err)
	}
	put(t, root, "AGENTS.md", "压缩后的当前完整规则")
	k.Send("压缩后继续")
	requireText(t, k, 2, "压缩后的当前完整规则")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.contexts[oldID] != nil || len(old.ledger.contexts) != 0 {
		t.Fatal("压缩后旧账本仍有效")
	}
}

func TestPluginSteerUpdates(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "插话前的规则")
	reached, release := make(chan struct{}), make(chan struct{})
	p := NewPlugin(PluginOptions{Enabled: true, Trust: RootsTrust{root}})
	k := turntest.New(t, turntest.Options{Before: func(ctx context.Context, n int, body string) {
		if n == 1 {
			close(reached)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}, Mutate: func(c *turn.Config) { c.Workdir = root; c.Instructions = p }})
	if err := k.Hub.Use(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Hub.StopPlugin(p.ID()) })
	out := k.Hub.Send(turntest.CharacterID, "开始")
	signal(t, reached)
	put(t, root, "AGENTS.md", "插话时更新的完整规则")
	_, ok := k.Hub.Steer(turntest.CharacterID, "核对新版")
	close(release)
	if !ok {
		t.Fatal("没有进入可插话的回合")
	}
	k.Wait(out)
	requireText(t, k, 0, "插话前的规则")
	requireText(t, k, 1, "插话时更新的完整规则")
	for i, m := range k.Messages(0) {
		if k.Messages(1)[i].Raw != m.Raw {
			t.Fatalf("插话改动旧请求第 %d 条", i)
		}
	}
}
