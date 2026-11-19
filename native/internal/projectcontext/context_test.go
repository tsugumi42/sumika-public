package projectcontext

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var background = context.Background()

func put(t testing.TB, root, name, text string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func resolver(t testing.TB, o Options) *Resolver {
	t.Helper()
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func accept(t testing.TB, c *Contribution) {
	t.Helper()
	if c == nil || c.OnAccepted == nil {
		t.Fatal("没有可接纳的贡献")
	}
	c.OnAccepted()
}
func issues(s Snapshot) string {
	var b strings.Builder
	for _, i := range s.Issues {
		b.WriteString(i.String() + "\n")
	}
	return b.String()
}
func expectStatus(t testing.TB, d WriteDecision, want WriteStatus) {
	t.Helper()
	if d.Status != want {
		t.Fatalf("写前状态=%s，要 %s；错误=%v；诊断=%s", d.Status, want, d.Err, issues(d.Snapshot))
	}
}

func TestDiscoveryOrder(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	cwd := filepath.Join(root, "sub")
	put(t, user, "AGENTS.override.md", "用户正文")
	put(t, user, "AGENTS.md", "错误用户正文")
	put(t, root, "AGENTS.md", "根正文")
	put(t, root, "CLAUDE.md", "错误根正文")
	put(t, root, "AGENTS.local.md", "根补充")
	put(t, root, "CLAUDE.local.md", "错误根补充")
	put(t, root, "sub/AGENTS.override.md", "子正文")
	put(t, root, "sub/AGENTS.md", "错误子正文")
	put(t, root, "sub/CLAUDE.local.md", "子补充")
	r := resolver(t, Options{Root: root, CWD: cwd, UserRoot: user})
	s := r.Discover(background)
	if !s.Complete {
		t.Fatal(issues(s))
	}
	pos := -1
	for _, body := range []string{"用户正文", "根正文", "根补充", "子正文", "子补充"} {
		n := strings.Index(s.Text, body)
		if n <= pos {
			t.Fatalf("顺序不对：%s\n%s", body, s.Text)
		}
		pos = n
	}
	if strings.Contains(s.Text, "错误") {
		t.Fatal("读了低优先级候选")
	}
	fallback := t.TempDir()
	put(t, fallback, "rules.md", "自定文件")
	s = resolver(t, Options{Root: fallback, CWD: fallback, Fallback: []string{"rules.md"}}).Discover(background)
	if !strings.Contains(s.Text, "自定文件") {
		t.Fatal("未读取 fallback")
	}
}

func TestFallbackNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../x", "a/b", "a\\b", "a\x00b", "C:rules.md", "rules.md:secret", "NUL", "con.txt", "COM1.md", "LPT²", "CONIN$", "file.", "file "} {
		if _, err := New(Options{Root: root, CWD: root, Fallback: []string{name}}); err == nil {
			t.Errorf("接纳了危险文件名 %q", name)
		}
	}
	if _, err := New(Options{Root: root, CWD: root, Fallback: []string{"规则 1.md"}}); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyAndUnreadable(t *testing.T) {
	root := t.TempDir()
	p := put(t, root, "AGENTS.override.md", "")
	put(t, root, "AGENTS.md", "不能回退")
	r := resolver(t, Options{Root: root, CWD: root})
	s := r.Discover(background)
	if !s.Complete || s.Sources[0].Path != p || s.Sources[0].State != Present || strings.Contains(s.Text, "不能回退") {
		t.Fatalf("空 override 没有选中：%+v", s)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	s = r.Discover(background)
	if s.Complete || s.Sources[0].State != Unavailable || strings.Contains(s.Text, "不能回退") {
		t.Fatal("不可读高优先级候选发生回退", s)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	put(t, root, "AGENTS.override.md", string([]byte{0xff}))
	if s = r.Discover(background); s.Complete || !strings.Contains(issues(s), "UTF-8") {
		t.Fatal("非法 UTF-8 被载入", s)
	}
}

func TestWorktreeBoundary(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "work")
	cwd := filepath.Join(root, "sub")
	put(t, outer, "AGENTS.md", "外面正文")
	put(t, root, ".git", "gitdir: ../main/.git/worktrees/work")
	put(t, root, "AGENTS.md", "worktree 正文")
	put(t, root, "sub/AGENTS.md", "cwd 正文")
	s := resolver(t, Options{Root: root, CWD: cwd}).Discover(background)
	if !s.Complete || !strings.Contains(s.Text, "worktree 正文") || strings.Contains(s.Text, "外面正文") {
		t.Fatal("越过调用方的 worktree 根", s)
	}
	s = resolver(t, Options{CWD: cwd}).Discover(background)
	if !s.Complete || strings.Contains(s.Text, "worktree 正文") || !strings.Contains(s.Text, "cwd 正文") {
		t.Fatal("无仓库时没停在 cwd", s)
	}
	s = resolver(t, Options{Root: filepath.Join(outer, "missing"), CWD: filepath.Join(outer, "missing")}).Discover(background)
	if s.Complete || strings.Contains(s.Text, "外面正文") {
		t.Fatal("探测失败却向上读取", s)
	}
	r := resolver(t, Options{Root: root, CWD: cwd})
	if s = r.DiscoverTo(background, outer); s.Complete {
		t.Fatal("允许目标目录越界")
	}
	fileRoot := put(t, outer, "file-root", "普通文件")
	if s := resolver(t, Options{Root: fileRoot, CWD: fileRoot}).Discover(background); s.Complete {
		t.Fatal("普通文件被当成空规则目录")
	}
}

type countedReader struct {
	io.ReadCloser
	bytes *int
}

func (r countedReader) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	*r.bytes += n
	return n, e
}

func TestApprovalBeforeReadAndCache(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "正文 @extra.md")
	put(t, root, "extra.md", "导入正文")
	bytes, approvals := 0, 0
	denied := true
	approved := map[string]bool{}
	r := resolver(t, Options{Root: root, CWD: root, Approve: func(ctx context.Context, path string) error {
		approvals++
		if denied {
			return errors.New("拒绝")
		}
		approved[path] = true
		return ctx.Err()
	}, Open: func(path string) (io.ReadCloser, error) {
		if !approved[path] {
			t.Fatalf("审批前打开了正文 %s", path)
		}
		delete(approved, path)
		rd, err := os.Open(path)
		return countedReader{rd, &bytes}, err
	}})
	s := r.Discover(background)
	if s.Complete || bytes != 0 || approvals != 1 || !strings.Contains(issues(s), "审批未通过") || strings.Contains(s.Text, "正文 @") {
		t.Fatalf("拒绝审批后读了正文：bytes=%d approvals=%d %s", bytes, approvals, issues(s))
	}
	denied = false
	s = r.Discover(background)
	if !s.Complete || bytes == 0 || approvals != 3 {
		t.Fatalf("未走两个来源审批：%d %d %s", bytes, approvals, issues(s))
	}
	bytes = 0
	s = r.Discover(background)
	if !s.Complete || bytes != 0 || approvals != 5 {
		t.Fatalf("热路径没重新审批或重读了正文：%d %d", bytes, approvals)
	}
	denied = true
	s = r.Discover(background)
	if s.Complete || bytes != 0 || strings.Contains(s.Text, "导入正文") {
		t.Fatal("缓存绕过当次审批")
	}
	denied = false
	r.opts.Approve = func(_ context.Context, path string) error {
		if filepath.Base(path) == "extra.md" {
			return errors.New("导入拒绝")
		}
		return nil
	}
	if s = r.Discover(background); s.Complete || !strings.Contains(s.Text, "@extra.md") || strings.Contains(s.Text, "导入正文") {
		t.Fatal("import 绕过审批", s)
	}
	ctx, cancel := context.WithCancel(background)
	cancel()
	if s = r.Discover(ctx); s.Complete || bytes != 0 {
		t.Fatal("取消后仍读取或注入", s)
	}
}

func TestImportSyntax(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	// 根外确实存在正文，避免把找不到文件误当作边界保护生效。
	outer := t.TempDir()
	root, user = filepath.Join(outer, "project"), filepath.Join(outer, "user")
	put(t, outer, "outside.md", "根外秘密标记")
	put(t, root, "sub/AGENTS.md", strings.Join([]string{"@../rules\\ file.md", "`@inline.md`", "```go", "@fenced.md", "```", "~~~", "@tilde.md", "~~~", "`` @two.md ``", "`多行", "@multi.md`", "\\@escaped.md x" + "@email.md"}, "\n"))
	put(t, root, "rules file.md", "展开的正文")
	s := resolver(t, Options{Root: root, CWD: filepath.Join(root, "sub")}).Discover(background)
	if !s.Complete || !strings.Contains(s.Text, "展开的正文") || strings.Contains(issues(s), ".md") {
		t.Fatal("import 语法不对", issues(s), s.Text)
	}
	for _, literal := range []string{"`@inline.md`", "@fenced.md", "@tilde.md", "@two.md", "@multi.md", "\\@escaped.md", "x" + "@email.md"} {
		if !strings.Contains(s.Text, literal) {
			t.Errorf("代码或非引用被改了：%s", literal)
		}
	}
	put(t, user, "AGENTS.md", "@~/rules.md")
	put(t, user, "rules.md", "用户引用正文")
	s = resolver(t, Options{Root: root, CWD: root, UserRoot: user}).Discover(background)
	if !s.Complete || !strings.Contains(s.Text, "用户引用正文") {
		t.Fatal("用户根引用没展开", issues(s))
	}
	for _, ref := range []string{"@https://example.invalid/a.md", `@\\server\share\a.md`, "@../outside.md", "@~/rules.md", "@rules.md:stream"} {
		put(t, root, "AGENTS.md", ref)
		s = resolver(t, Options{Root: root, CWD: root}).Discover(background)
		if s.Complete || !strings.Contains(s.Text, ref) || len(s.Issues) == 0 || strings.Contains(s.Text, "根外秘密标记") {
			t.Errorf("非法引用未保留并拒绝：%s %s", ref, issues(s))
		}
	}
	put(t, user, "AGENTS.md", "@~/../outside.md")
	if s = resolver(t, Options{Root: root, CWD: root, UserRoot: user}).Discover(background); s.Complete || strings.Contains(s.Text, "根外秘密标记") {
		t.Fatal("用户引用逃出根")
	}
}

func TestImportLimits(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "AGENTS.md", "@d1.md")
		for i := 1; i < 5; i++ {
			put(t, root, fmt.Sprintf("d%d.md", i), fmt.Sprintf("@d%d.md", i+1))
		}
		put(t, root, "d5.md", "第五层")
		r := resolver(t, Options{Root: root, CWD: root})
		if s := r.Discover(background); !s.Complete || !strings.Contains(s.Text, "第五层") {
			t.Fatal("第五跳应成功", issues(s))
		}
		put(t, root, "d5.md", "@d6.md")
		put(t, root, "d6.md", "第六层不能读")
		s := r.Discover(background)
		if s.Complete || !strings.Contains(issues(s), "超过 5 跳") || !strings.Contains(s.Text, "@d6.md") || strings.Contains(s.Text, "第六层不能读") {
			t.Fatal("第六跳未拒绝", issues(s))
		}
	})
	t.Run("count", func(t *testing.T) {
		root := t.TempDir()
		var refs strings.Builder
		for i := range 65 {
			name := fmt.Sprintf("f%d.md", i)
			put(t, root, name, fmt.Sprintf("文件%d", i))
			if i < 64 {
				fmt.Fprintf(&refs, "@%s\n", name)
			}
		}
		put(t, root, "AGENTS.md", refs.String())
		opens := 0
		r := resolver(t, Options{Root: root, CWD: root, TotalBytes: 128 << 10, Open: func(p string) (io.ReadCloser, error) { opens++; return os.Open(p) }})
		if s := r.Discover(background); !s.Complete || opens != 65 {
			t.Fatalf("64 个导入应成功：opens=%d %s", opens, issues(s))
		}
		// 第二份说明的 import 也用同一份额度。
		put(t, root, "AGENTS.local.md", "@f64.md")
		opens = 0
		s := r.Discover(background)
		if s.Complete || !strings.Contains(issues(s), "超过 64") || opens != 1 || !strings.Contains(s.Text, "@f64.md") {
			t.Fatalf("额度被重置或读取了第 65 个导入：opens=%d %s", opens, issues(s))
		}
	})
	t.Run("denied-imports", func(t *testing.T) {
		root := t.TempDir()
		var refs strings.Builder
		for i := range 70 {
			name := fmt.Sprintf("denied%d.md", i)
			put(t, root, name, "不可读取")
			fmt.Fprintf(&refs, "@%s\n", name)
		}
		put(t, root, "AGENTS.md", refs.String())
		approvals := 0
		r := resolver(t, Options{Root: root, CWD: root, Approve: func(_ context.Context, path string) error {
			approvals++
			if filepath.Base(path) != "AGENTS.md" {
				return errors.New("拒绝 import")
			}
			return nil
		}})
		s := r.Discover(background)
		if s.Complete || approvals != 65 || !strings.Contains(issues(s), "超出 import 文件数") || !strings.Contains(s.Text, "@denied69.md") {
			t.Fatalf("失败 import 未停止展开：审批=%d，要 65；%s", approvals, issues(s))
		}
	})
	t.Run("invalid-files", func(t *testing.T) {
		root := t.TempDir()
		var refs strings.Builder
		for i := range 65 {
			name := fmt.Sprintf("invalid%d.md", i)
			put(t, root, name, string([]byte{0xff}))
			fmt.Fprintf(&refs, "@%s\n", name)
		}
		put(t, root, "AGENTS.md", refs.String())
		opens := 0
		r := resolver(t, Options{Root: root, CWD: root, Open: func(path string) (io.ReadCloser, error) { opens++; return os.Open(path) }})
		s := r.Discover(background)
		if s.Complete || opens != 65 || !strings.Contains(issues(s), "超过 64") {
			t.Fatalf("失败 import 未占额度：opens=%d，要 65（根 + 64 import），包含限额诊断=%v", opens, strings.Contains(issues(s), "超过 64"))
		}
	})
	t.Run("identity", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "AGENTS.md", "@a.md @alias.md @a.md")
		p := put(t, root, "a.md", "唯一正文")
		if err := os.Link(p, filepath.Join(root, "alias.md")); err != nil {
			t.Fatal(err)
		}
		r := resolver(t, Options{Root: root, CWD: root})
		s := r.Discover(background)
		if !s.Complete || strings.Count(s.Text, "唯一正文") != 1 {
			t.Fatal("未按文件身份去重", issues(s), s.Text)
		}
		put(t, root, "a.md", "@AGENTS.md")
		s = r.Discover(background)
		if s.Complete || !strings.Contains(issues(s), "循环") || !strings.Contains(s.Text, "@AGENTS.md") {
			t.Fatal("循环未诊断", issues(s))
		}
	})
}

func TestByteBoundaries(t *testing.T) {
	root := t.TempDir()
	for _, n := range []int{9, 10, 11} {
		put(t, root, "AGENTS.md", strings.Repeat("a", n))
		s := resolver(t, Options{Root: root, CWD: root, SourceBytes: 10}).Discover(background)
		if s.Complete != (n <= 10) {
			t.Errorf("单源 %d 字节边界不对：%s", n, issues(s))
		}
	}
	put(t, root, "AGENTS.md", strings.Repeat("中", 100))
	base := resolver(t, Options{Root: root, CWD: root}).Discover(background)
	exact := len(base.Text) + len(updateMark) + len(retryMark)
	for _, n := range []int{exact - 1, exact, exact + 1} {
		s := resolver(t, Options{Root: root, CWD: root, TotalBytes: n}).Discover(background)
		if s.Complete != (n >= exact) || len(s.Text) > n || !utf8.ValidString(s.Text) {
			t.Errorf("总量 %d 边界不对：len=%d complete=%v %s", n, len(s.Text), s.Complete, issues(s))
		}
	}
	// 文件元数据低估也不能使有界读取超限；最多读 limit+1 个字节。
	read := 0
	r := resolver(t, Options{Root: root, CWD: root, SourceBytes: 400, Open: func(string) (io.ReadCloser, error) {
		return countedReader{io.NopCloser(strings.NewReader(strings.Repeat("x", 1000))), &read}, nil
	}})
	if s := r.Discover(background); s.Complete || read != 401 {
		t.Fatalf("未限量读取：%d %s", read, issues(s))
	}
	put(t, root, "AGENTS.md", strings.Repeat("中", 1000))
	for _, limit := range []int{2000, 2001, 2002} {
		s := resolver(t, Options{Root: root, CWD: root, TotalBytes: limit}).Discover(background)
		kept := strings.Count(s.Sources[0].Rendered, "中")
		if s.Complete || kept == 0 || kept >= 1000 || !utf8.ValidString(s.Text) || len(s.Text) > limit {
			t.Fatalf("中文截断不对：预算=%d，保留=%d 字，UTF-8=%v，输出=%d 字节", limit, kept, utf8.ValidString(s.Text), len(s.Text))
		}
	}
}
