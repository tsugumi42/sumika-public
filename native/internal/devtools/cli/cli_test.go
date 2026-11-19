package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookDispatch(t *testing.T) {
	var out, errb bytes.Buffer
	if c := Run([]string{"hook"}, &out, &errb); c != 2 || !strings.Contains(errb.String(), "pre-commit") {
		t.Fatalf("hook 不带钩子名应退出码 2 并列出现有的：%d %q", c, errb.String())
	}
	if c := Run([]string{"hook", "nope"}, &out, &errb); c != 2 {
		t.Fatalf("未知钩子应退出码 2，得到 %d", c)
	}
	// commit-msg 从 check 下搬到了 hook 下：check 里不再有它
	if c := Run([]string{"check", "commit-msg", "x"}, &out, &errb); c != 2 {
		t.Fatalf("check commit-msg 已搬走，应退出码 2，得到 %d", c)
	}
	// hook commit-msg 接到了标题检查上：带分号的标题红，正常标题过
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUMIKA_DEV_ROOT", root)
	dir := t.TempDir()
	good, bad := filepath.Join(dir, "good"), filepath.Join(dir, "bad")
	if err := os.WriteFile(good, []byte("fix(模块): 现在会怎样\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("fix(模块): 现在会怎样；还有别的\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := Run([]string{"hook", "commit-msg", good}, &out, &errb); c != 0 {
		t.Fatalf("正常标题应过：%d %q", c, out.String())
	}
	if c := Run([]string{"hook", "commit-msg", bad}, &out, &errb); c != 1 {
		t.Fatalf("带分号的标题应红：%d %q", c, out.String())
	}
}

func TestRunVersionHelpAndUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if c := Run([]string{"version"}, &out, &errb); c != 0 || !strings.Contains(out.String(), "源码摘要") {
		t.Fatalf("version：%d %q", c, out.String())
	}
	out.Reset()
	if c := Run([]string{"help"}, &out, &errb); c != 0 || !strings.Contains(out.String(), "sumika-dev version") {
		t.Fatalf("help：%d %q", c, out.String())
	}
	if c := Run([]string{"nope"}, &out, &errb); c != 2 {
		t.Fatalf("未知子命令应退出码 2，得到 %d", c)
	}
	if c := Run(nil, &out, &errb); c != 2 {
		t.Fatalf("没有子命令应退出码 2，得到 %d", c)
	}
}
