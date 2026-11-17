//go:build windows

package projectcontext

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"sumika-native/internal/testutil/junction"
)

func TestStrictRealPaths(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	put(t, root, "inside/AGENTS.md", "根内规则")
	put(t, outside, "AGENTS.md", "越界规则")
	caseAlias := resolver(t, Options{Root: strings.ToUpper(root), CWD: filepath.Join(root, "inside")}).Discover(background)
	if !caseAlias.Complete || !strings.Contains(caseAlias.Text, "根内规则") {
		t.Fatal("Windows 大小写别名未落在同一工作区", issues(caseAlias))
	}
	for _, c := range []struct {
		name, target string
		complete     bool
	}{{"in", filepath.Join(root, "inside"), true}, {"out", outside, false}} {
		link := filepath.Join(root, c.name)
		if err := junction.Create(link, c.target); err != nil {
			t.Fatalf("建临时联接：%v", err)
		}
		opens := 0
		r := resolver(t, Options{Root: root, CWD: link, Open: func(p string) (io.ReadCloser, error) { opens++; return os.Open(p) }})
		s := r.Discover(background)
		if s.Complete != c.complete || strings.Contains(s.Text, "越界规则") || (!c.complete && opens != 0) {
			t.Fatalf("联接 %s：complete=%v opens=%d %s", c.name, s.Complete, opens, issues(s))
		}
		d := r.BeforeWrite(background, "ctx", NewLedger(), filepath.Join(link, "new.txt"))
		if !c.complete {
			expectStatus(t, d, RulesIncomplete)
		} else {
			expectStatus(t, d, NeedContext)
		}
	}
	// import 联接同样做真实路径边界检查。
	put(t, root, "AGENTS.md", "@out/AGENTS.md")
	r := resolver(t, Options{Root: root, CWD: root})
	if s := r.Discover(background); s.Complete || strings.Contains(s.Text, "越界规则") || !strings.Contains(s.Text, "@out/AGENTS.md") {
		t.Fatal("import 联接越界未拒绝", issues(s))
	}
	// 共享冲突不是不存在，也不能选低优先级候选。
	p := put(t, root, "AGENTS.override.md", "独占文件")
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	s := r.Discover(background)
	if s.Complete || s.Sources[0].State != Unavailable || strings.Contains(s.Text, "@out/AGENTS.md") {
		t.Fatal("共享冲突发生回退", issues(s))
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", NewLedger(), filepath.Join(root, "target.txt")), RulesIncomplete)
}

func TestAccessDenied(t *testing.T) {
	root := t.TempDir()
	p := put(t, root, "AGENTS.override.md", "不允许读取")
	put(t, root, "CLAUDE.md", "不允许回退")
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	defer func() {
		if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, original, nil); err != nil {
			t.Error("恢复临时 ACL", err)
		}
	}()
	denySD, err := windows.SecurityDescriptorFromString("D:P(D;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	deny, _, err := denySD.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, deny, nil); err != nil {
		t.Fatal(err)
	}
	opens := 0
	r := resolver(t, Options{Root: root, CWD: root, Open: func(path string) (io.ReadCloser, error) { opens++; return os.Open(path) }})
	s := r.Discover(background)
	if s.Complete || s.Sources[0].State != Unavailable || opens != 0 || strings.Contains(s.Text, "不允许回退") {
		t.Fatalf("拒绝访问不应回退：opens=%d %s", opens, issues(s))
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", NewLedger(), filepath.Join(root, "target.txt")), RulesIncomplete)
}

func TestHotJunctionRetarget(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	insideFile := put(t, root, "inside/rules.md", "根内正文")
	outsideFile := put(t, outside, "rules.md", "根外秘密")
	info, err := os.Stat(insideFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outsideFile, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	join := func(target string) {
		t.Helper()
		if err := junction.Create(link, target); err != nil {
			t.Fatalf("建临时联接：%v", err)
		}
	}
	join(filepath.Dir(insideFile))
	put(t, root, "AGENTS.md", "@link/rules.md")
	opens := 0
	r := resolver(t, Options{Root: root, CWD: root, Open: func(p string) (io.ReadCloser, error) { opens++; return os.Open(p) }})
	if s := r.Discover(background); !s.Complete || !strings.Contains(s.Text, "根内正文") {
		t.Fatal("冷路径未读根内规则", issues(s))
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	join(outside)
	opens = 0
	s := r.Discover(background)
	if s.Complete || strings.Contains(s.Text, "根外秘密") || opens != 0 {
		t.Fatalf("热缓存漏过联接换目标：opens=%d %s", opens, issues(s))
	}
	expectStatus(t, r.BeforeWrite(background, "ctx", NewLedger(), filepath.Join(root, "target.txt")), RulesIncomplete)
}
