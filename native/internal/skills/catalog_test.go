package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
)

func skillAccess(t *testing.T, caps ...string) (*ecosystem.Authority, ecosystem.Access) {
	t.Helper()
	auth := ecosystem.NewAuthority()
	a, err := auth.Issue(kernel.Scope{CharacterID: "test-character", Conversation: "test-conversation", ScopeID: "test-scope", Platform: "local", Channel: "console", AccountID: "90001", Scene: "private", ActorKind: "owner", ActorID: "90002", AuthorityID: ecosystem.ID(), AuthorityRevision: 1}, caps, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return auth, a
}
func makeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: 编造的测试资料\n---\n"+body), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestSkillAdmissionAndVersion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	makeSkill(t, root, "sumika", strings.Repeat("汉🙂\r\n", 400))
	makeSkill(t, root, "hidden", "secret")
	auth, a := skillAccess(t, "skill:project", "skill:builtin")
	approved := 0
	c, err := New(Options{Enabled: true, Roots: []Root{{ID: "project", Path: root}}, Approve: func(ctx context.Context, a ecosystem.Access, p string) error {
		approved++
		if strings.Contains(p, "hidden") {
			return ecosystem.ErrDenied
		}
		return nil
	}, ContextMaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Scan(ctx, a); err != nil {
		t.Fatal(err)
	}
	if approved < 2 {
		t.Fatal("未在读取元数据前审批")
	}
	index, err := c.Read(ctx, a, "ctx", Request{Listing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Items) != 2 {
		t.Fatalf("可见目录=%+v", index.Items)
	}
	p, err := c.Read(ctx, a, "ctx", Request{Name: "builtin:sumika", Limit: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "builtin:sumika" || strings.Contains(p.Text, "编造的测试资料") {
		t.Fatal("内置被覆盖")
	}
	p.Disposition.Discard()
	p, err = c.Read(ctx, a, "ctx", Request{Name: "sumika", Limit: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if p.Complete || p.End <= p.Start {
		t.Fatal("未分页")
	}
	if u, r := c.Usage("ctx"); u != 0 || r != len(p.Text) {
		t.Fatalf("预留错误 %d/%d", u, r)
	}
	p.Disposition.Accept()
	p.Disposition.Discard()
	if u, r := c.Usage("ctx"); u != len(p.Text) || r != 0 {
		t.Fatalf("接纳未恰一次 %d/%d", u, r)
	}
	next, err := c.Read(ctx, a, "ctx", Request{Name: "sumika", Cursor: p.NextCursor, Limit: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if next.Start != p.End {
		t.Fatal("分页不连续")
	}
	next.Disposition.Discard()
	makeSkill(t, root, "sumika", "更新后的正文")
	if _, err = c.Read(ctx, a, "ctx", Request{Name: "sumika", Cursor: p.NextCursor, Limit: 1024}); err == nil {
		t.Fatal("旧版本 cursor 被使用")
	}
	for _, resource := range []string{"../outside", "C:/outside", "refs/x:stream", "//server/share", "a/../../outside"} {
		if _, err = c.Read(ctx, a, "ctx", Request{Name: "sumika", Resource: resource}); err == nil {
			t.Fatalf("路径未拒绝 %q", resource)
		}
	}
	p, err = c.Read(ctx, a, "old", Request{Name: "sumika"})
	if err != nil {
		t.Fatal(err)
	}
	c.Forget("old")
	p.Disposition.Accept()
	if used, _ := c.Usage("old"); used != 0 {
		t.Fatal("旧上下文复活")
	}
	auth.Revoke(a.Scope().AuthorityID)
	if _, err = c.Read(ctx, a, "ctx", Request{Name: "builtin:sumika"}); err == nil {
		t.Fatal("撤权仍可读")
	}
}
func TestSkillParallelReservation(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "demo", strings.Repeat("x", 4000))
	_, a := skillAccess(t, "skill:root")
	c, err := New(Options{Enabled: true, Roots: []Root{{ID: "root", Path: root}}, Approve: func(context.Context, ecosystem.Access, string) error { return nil }, ContextMaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Scan(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	pages := make(chan Page, 8)
	for range 8 {
		wg.Go(func() {
			if p, err := c.Read(t.Context(), a, "ctx", Request{Name: "demo", Limit: 2048}); err == nil {
				pages <- p
			}
		})
	}
	wg.Wait()
	close(pages)
	_, reserved := c.Usage("ctx")
	if reserved > 4096 {
		t.Fatalf("并发穿透 %d", reserved)
	}
	n := 0
	for p := range pages {
		n++
		p.Disposition.Discard()
	}
	if n == 0 || n == 8 {
		t.Fatalf("准入数 %d", n)
	}
	if u, r := c.Usage("ctx"); u != 0 || r != 0 {
		t.Fatal("丢弃未释放")
	}
}
func TestBuiltinManualAndCandidateBoundary(t *testing.T) {
	_, a := skillAccess(t, "skill:builtin")
	c, err := New(Options{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Scan(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"SKILL.md", "references/tools.md", "references/approval.md", "references/features.md", "references/configuration.md", "references/channels.md", "references/project-context-and-skills.md", "references/troubleshooting.md"} {
		var text strings.Builder
		cursor := ""
		for {
			p, err := c.Read(t.Context(), a, "manual", Request{Name: "builtin:sumika", Resource: file, Cursor: cursor})
			if err != nil {
				t.Fatal(file, err)
			}
			p.Disposition.Accept()
			text.WriteString(p.Text)
			if p.Complete {
				break
			}
			cursor = p.NextCursor
		}
		expected, err := readBuiltin(file)
		if err != nil || text.String() != string(expected) {
			t.Fatalf("离线资源分页丢字 %s %v", file, err)
		}
	}
	candidate := t.TempDir()
	makeSkill(t, candidate, "candidate", "must not load")
	if _, err = c.Read(t.Context(), a, "manual", Request{Name: "candidate"}); err == nil {
		t.Fatal("候选目录自动启用")
	}
}
