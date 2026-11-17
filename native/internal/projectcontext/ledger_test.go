package projectcontext

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncompleteRules(t *testing.T) {
	for _, kind := range []string{"source", "total", "ancestors", "import"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			cwd := filepath.Join(root, "sub")
			put(t, root, "sub/AGENTS.md", "子规则")
			switch kind {
			case "source":
				put(t, root, "AGENTS.md", strings.Repeat("x", DefaultSourceBytes+1))
			case "total":
				put(t, root, "AGENTS.md", strings.Repeat("x", DefaultTotalBytes))
			case "ancestors":
				put(t, root, "AGENTS.md", strings.Repeat("祖", 6000))
				put(t, root, "sub/AGENTS.md", strings.Repeat("子", 6000))
			case "import":
				put(t, root, "AGENTS.md", "根规则 @missing.md")
			}
			r, l := resolver(t, Options{Root: root, CWD: cwd}), NewLedger()
			if kind == "source" {
				r.opts.TotalBytes = 2 << 20
			}
			target := filepath.Join(cwd, "target.txt")
			for range 2 {
				d := r.BeforeWrite(background, "ctx", l, target)
				expectStatus(t, d, RulesIncomplete)
				if d.Err == nil || !strings.Contains(d.Err.Error(), "恢复条件") || strings.Contains(d.Err.Error(), "重试即可") {
					t.Fatal("没有明确恢复条件", d.Err)
				}
				if kind == "ancestors" && !strings.Contains(d.Snapshot.Text, strings.Repeat("子", 6000)) {
					t.Fatal("没有优先保留子目录正文")
				}
				_, c := r.Next(background, "ctx", l)
				if c != nil && c.OnAccepted != nil {
					c.OnAccepted()
				}
			}
			put(t, root, "AGENTS.md", "修好的根规则")
			put(t, root, "sub/AGENTS.md", "修好的子规则")
			d := r.BeforeWrite(background, "ctx", l, target)
			expectStatus(t, d, NeedContext)
			accept(t, d.Contribution)
			expectStatus(t, r.BeforeWrite(background, "ctx", l, target), CanWrite)
		})
	}
}

func TestBeforeWrite(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "根规则")
	put(t, root, "sub/AGENTS.md", "子规则")
	r, l := resolver(t, Options{Root: root, CWD: root}), NewLedger()
	target := filepath.Join(root, "sub", "target.txt")
	d := r.BeforeWrite(background, "ctx", l, target)
	expectStatus(t, d, NeedContext)
	if !strings.Contains(d.Contribution.Lines[0], retryMark) || !strings.Contains(d.Contribution.Lines[0], "根规则") || !strings.Contains(d.Contribution.Lines[0], "子规则") {
		t.Fatal("没有返回完整适用规则和新调用提示")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("库不应执行写入")
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", l, target), NeedContext)
	accept(t, d.Contribution)
	expectStatus(t, r.BeforeWrite(background, "ctx", l, target), CanWrite)
	expectStatus(t, r.BeforeWrite(background, "another", l, target), NeedContext)
	// 已成功 read/ls 并接纳的子目录规则，随后编辑直接放行。
	other := NewLedger()
	_, c := r.NextTo(background, "read", other, filepath.Dir(target))
	accept(t, c)
	expectStatus(t, r.BeforeWrite(background, "read", other, target), CanWrite)
	// 新建多层目录时仍检查现有祖先。
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "sub", "new", "deeper", "file.txt")), CanWrite)
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(t.TempDir(), "outside.txt")), RulesIncomplete)
}

func TestUpdatesAndDeletion(t *testing.T) {
	root := t.TempDir()
	p := put(t, root, "AGENTS.md", "旧版规则")
	r, l := resolver(t, Options{Root: root, CWD: root}), NewLedger()
	_, c := r.Next(background, "ctx", l)
	old := strings.Join(c.Lines, "\n")
	first := c
	accept(t, c)
	if _, c = r.Next(background, "ctx", l); c != nil {
		t.Fatal("同 digest 重复追加")
	}
	put(t, root, "AGENTS.md", "新版完整规则")
	_, c = r.Next(background, "ctx", l)
	if c == nil || !strings.Contains(c.Lines[0], updateMark) || !strings.Contains(c.Lines[0], "新版完整规则") || strings.Contains(c.Lines[0], "旧版规则") {
		t.Fatal("没有追加完整新版", c)
	}
	if strings.Join(first.Lines, "\n") != old {
		t.Fatal("旧块变化")
	}
	accept(t, c)
	put(t, root, "AGENTS.md", "不完整新版 @missing.md")
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt")), RulesIncomplete)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	_, c = r.Next(background, "ctx", l)
	if c == nil || !strings.Contains(c.Lines[0], "撤销来源") {
		t.Fatal("删除没追加撤销", c)
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt")), NeedContext)
	accept(t, c)
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt")), CanWrite)
	if _, c = r.Next(background, "ctx", l); c != nil {
		t.Fatal("重复撤销")
	}
}

func TestUnavailableKeepsLedger(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "已接纳的规则")
	bad := false
	r := resolver(t, Options{Root: root, CWD: root, Open: func(p string) (io.ReadCloser, error) {
		if bad {
			return nil, os.ErrPermission
		}
		return os.Open(p)
	}})
	l := NewLedger()
	s, c := r.Next(background, "ctx", l)
	accept(t, c)
	id := s.Sources[0].ID
	prior := l.contexts["ctx"][id]
	bad = true
	r.MarkDirty("")
	s, c = r.Next(background, "ctx", l)
	if s.Complete || s.Sources[0].State != Unavailable || c == nil || strings.Contains(c.Lines[0], "撤销来源") {
		t.Fatal("unavailable 当成删除", s, c)
	}
	if c.OnAccepted != nil {
		c.OnAccepted()
	}
	if l.contexts["ctx"][id] != prior {
		t.Fatal("不可读覆盖了旧版账本")
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt")), RulesIncomplete)
	bad = false
	_, c = r.Next(background, "ctx", l)
	if c != nil {
		t.Fatal("恢复同版后不应重复正文", c)
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt")), CanWrite)
	// import 暂时不可读也不能把整组旧版账本改成一个不完整新版。
	put(t, root, "AGENTS.md", "已接纳的规则 @extra.md")
	put(t, root, "extra.md", "导入规则")
	_, c = r.Next(background, "ctx", l)
	accept(t, c)
	prior = l.contexts["ctx"][id]
	r.opts.Open = func(path string) (io.ReadCloser, error) {
		if filepath.Base(path) == "extra.md" {
			return nil, os.ErrPermission
		}
		return os.Open(path)
	}
	r.MarkDirty("")
	_, c = r.Next(background, "ctx", l)
	if c != nil && c.OnAccepted != nil {
		c.OnAccepted()
	}
	if l.contexts["ctx"][id] != prior {
		t.Fatal("import 不可读覆盖了旧版账本")
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt")), RulesIncomplete)
}

func TestAcceptanceIsDeferred(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "待接纳规则")
	r, l := resolver(t, Options{Root: root, CWD: root}), NewLedger()
	_, first := r.Next(background, "old", l)
	if len(l.contexts) != 0 {
		t.Fatal("扫描已提交账本")
	}
	_, again := r.Next(background, "old", l)
	if again == nil || strings.Join(first.Lines, "") != strings.Join(again.Lines, "") {
		t.Fatal("未接纳贡献被当成已接纳")
	}
	l.Forget("old")
	first.OnAccepted()
	if len(l.contexts) != 0 {
		t.Fatal("丢弃后的旧贡献仍能提交")
	}
	_, fresh := r.Next(background, "new", l)
	accept(t, fresh)
	fresh.OnAccepted()
	if _, c := r.Next(background, "new", l); c != nil {
		t.Fatal("接纳后同版还在重复")
	}
	if _, c := r.Next(background, "old", l); c == nil {
		t.Fatal("新上下文账本泄漏到旧 ID")
	}
}

func TestWriteRechecksContentAndDirty(t *testing.T) {
	root := t.TempDir()
	p := put(t, root, "AGENTS.md", "aaaa")
	r, l := resolver(t, Options{Root: root, CWD: root}), NewLedger()
	_, c := r.Next(background, "ctx", l)
	accept(t, c)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, "AGENTS.md", "bbbb")
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// 普通轮次可以命中元数据；写前必须比较实际正文。
	if _, c := r.Next(background, "ctx", l); c != nil {
		t.Fatal("夹具元数据没有保持不变", c)
	}
	d := r.BeforeWrite(background, "ctx", l, filepath.Join(root, "target.txt"))
	expectStatus(t, d, NeedContext)
	if !strings.Contains(d.Contribution.Lines[0], "bbbb") {
		t.Fatal("写前没有核对正文")
	}
	accept(t, d.Contribution)
	put(t, root, "AGENTS.md", "cccc")
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	r.MarkDirty(p)
	if _, c := r.Next(background, "ctx", l); c == nil || !strings.Contains(c.Lines[0], "cccc") {
		t.Fatal("dirty 没有使缓存失效", c)
	}
}

// 用真实临时文件走库的完整调用次序；宿主只在 CanWrite 后执行副作用。
func TestLibrarySmoke(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "根规则")
	put(t, root, "child/AGENTS.md", "子规则 @rules.md")
	put(t, root, "child/rules.md", "依赖规则")
	r, l := resolver(t, Options{Root: root, CWD: root}), NewLedger()
	target := filepath.Join(root, "child", "result.txt")
	apply := func() WriteDecision {
		d := r.BeforeWrite(background, "smoke", l, target)
		if d.Status == CanWrite {
			put(t, root, "child/result.txt", "实际写入")
		}
		return d
	}
	d := apply()
	expectStatus(t, d, NeedContext)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("首次已经写入")
	}
	accept(t, d.Contribution)
	expectStatus(t, apply(), CanWrite)
	text, err := os.ReadFile(target)
	if err != nil || string(text) != "实际写入" {
		t.Fatal("接纳后没有写入", err, string(text))
	}
	put(t, root, "child/rules.md", "新版依赖规则")
	d = apply()
	expectStatus(t, d, NeedContext)
	if !strings.Contains(d.Contribution.Lines[0], "新版依赖规则") {
		t.Fatal("依赖更新未传播")
	}
	accept(t, d.Contribution)
	expectStatus(t, apply(), CanWrite)
	t.Log(fmt.Sprintf("冒烟完成：首次阻塞 → 接纳 → 实际写入 → import 更新阻塞 → 接纳 → 实际写入"))
}
