package projectcontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"testing"
)

func TestPluginMissingTrustRootBlocksEdit(t *testing.T) {
	root, missing := t.TempDir(), t.TempDir()
	target := put(t, root, "sub/target.txt", "before")
	put(t, root, "AGENTS.md", "尚未接纳的祖先规则")
	k, _ := wired(t, root, []llmtest.Reply{editReply("e"), llmtest.Text("结束")}, nil, func(o *PluginOptions) { o.Trust = RootsTrust{root, missing} })
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	k.Send("编辑")
	if got := fileText(t, target); got != "before" {
		t.Fatalf("信任根损坏仍写入：%q", got)
	}
	requireText(t, k, 1, strings.ReplaceAll(missing, `\`, `\\`))
	requireText(t, k, 1, "恢复")
}

func TestPluginBindingRequiredBeforeWrite(t *testing.T) {
	for _, kind := range []string{"missing", "uncertain", "untrusted", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			p := NewPlugin(PluginOptions{Enabled: kind != "disabled", Trust: RootsTrust{t.TempDir()}})
			p.active = true
			in := turn.ContextIn{ContextID: "ctx", Workspace: turn.Workspace{ID: "w", Root: root, CWD: root}, Audience: audience.Console("local")}
			if kind == "uncertain" {
				in.Workspace.Root = filepath.Join(root, "missing")
			}
			if kind != "missing" {
				_, _ = p.onContext(context.Background(), in)
			}
			for _, name := range []string{"write", "edit", "read", "ls"} {
				called := false
				r, err := p.Execute(context.Background(), in, name, jsjson.Value{}, func(context.Context) (tools.Result, error) {
					called = true
					return tools.Result{Text: "普通结果"}, nil
				})
				blocked := (kind == "missing" || kind == "uncertain") && (name == "write" || name == "edit")
				if err != nil || called == blocked || r.IsError != blocked {
					t.Fatalf("%s：执行 %v，结果 %+v，错误 %v", name, called, r, err)
				}
				if blocked && !strings.Contains(r.Text, "恢复") {
					t.Fatalf("缺恢复条件：%s", r.Text)
				}
			}
		})
	}
}

type barrierTrust struct {
	base    Trust
	entered chan struct{}
	release chan struct{}
}

func (b barrierTrust) Trusted(ctx context.Context, w turn.Workspace) (bool, error) {
	ok, err := b.base.Trusted(ctx, w)
	close(b.entered)
	select {
	case <-b.release:
		return ok, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
