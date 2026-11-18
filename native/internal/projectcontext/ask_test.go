package projectcontext

// B7：工作区还没有信任、里面又有说明文件时问一次；问完 Trust 重新判。没有说明文件、已经信任的不问。

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
)

type flagTrust struct{ ok atomic.Bool }

func (f *flagTrust) Trusted(context.Context, turn.Workspace) (bool, error) { return f.ok.Load(), nil }

func TestInstructionFiles(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "根规则")
	put(t, root, "sub/CLAUDE.md", "子规则")
	put(t, root, "sub/deep/AGENTS.override.md", "覆盖")
	put(t, root, "sub/deep/notes.txt", "不是说明文件")
	got := InstructionFiles(root, filepath.Join(root, "sub", "deep"))
	want := []string{filepath.Join(root, "sub", "deep", "AGENTS.override.md"), filepath.Join(root, "sub", "CLAUDE.md"), filepath.Join(root, "AGENTS.md")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("从工作目录向上到根：%v", got)
	}
	empty := t.TempDir()
	if got := InstructionFiles(empty, empty); len(got) != 0 {
		t.Fatalf("没有说明文件：%v", got)
	}
	// 目录叫 AGENTS.md 不算（不是普通文件）
	put(t, empty, "AGENTS.md/x.txt", "x")
	if got := InstructionFiles(empty, empty); len(got) != 0 {
		t.Fatalf("目录不算说明文件：%v", got)
	}
}

func TestPluginAsksOnceThenReads(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "问过以后才读的规则")
	trust := &flagTrust{}
	var asked int
	var gotRoot string
	var gotFiles []string
	k, _ := wired(t, root, []llmtest.Reply{llmtest.Text("好")}, nil, func(o *PluginOptions) {
		o.Trust = trust
		o.Ask = func(_ context.Context, _ turn.ContextIn, r string, files []string) {
			asked++
			gotRoot, gotFiles = r, files
			trust.ok.Store(true) // 主人答了"记住"
		}
	})
	k.Send("开始")
	if asked != 1 || len(gotFiles) != 1 || !strings.HasSuffix(gotFiles[0], "AGENTS.md") || gotRoot == "" {
		t.Fatalf("该问一次：问了 %d 次 根 %q 文件 %v", asked, gotRoot, gotFiles)
	}
	requireText(t, k, 0, "问过以后才读的规则")
}

func TestPluginAskDeclinedReadsNothing(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "没信任就不该出现的规则")
	var asked int
	k, _ := wired(t, root, []llmtest.Reply{llmtest.Text("好")}, nil, func(o *PluginOptions) {
		o.Trust = &flagTrust{}
		o.Ask = func(context.Context, turn.ContextIn, string, []string) { asked++ } // 主人拒绝了：Trust 仍是否
	})
	k.Send("开始")
	if asked != 1 {
		t.Fatalf("问了 %d 次", asked)
	}
	for _, m := range k.Messages(0) {
		if strings.Contains(m.Text, "没信任就不该出现的规则") {
			t.Fatal("没有信任却读了说明文件")
		}
	}
}

func TestPluginNoAskWithoutFilesOrWhenTrusted(t *testing.T) {
	// 没有说明文件：不问
	root := t.TempDir()
	put(t, root, "readme.txt", "x")
	var asked int
	k, _ := wired(t, root, []llmtest.Reply{llmtest.Text("好")}, nil, func(o *PluginOptions) {
		o.Trust = &flagTrust{}
		o.Ask = func(context.Context, turn.ContextIn, string, []string) { asked++ }
	})
	k.Send("开始")
	if asked != 0 {
		t.Fatalf("没有说明文件也问了：%d", asked)
	}
	// 已经信任：不问
	root2 := t.TempDir()
	put(t, root2, "AGENTS.md", "已经信任的规则")
	trusted := &flagTrust{}
	trusted.ok.Store(true)
	k2, _ := wired(t, root2, []llmtest.Reply{llmtest.Text("好")}, nil, func(o *PluginOptions) {
		o.Trust = trusted
		o.Ask = func(context.Context, turn.ContextIn, string, []string) { asked++ }
	})
	k2.Send("开始")
	if asked != 0 {
		t.Fatalf("已经信任还问：%d", asked)
	}
	requireText(t, k2, 0, "已经信任的规则")
}
