package hook

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/devtools/gitrun"
)

// 测试里起 git：环境里的 GIT_* 一律清掉（LESSONS：钩子里继承的 GIT_DIR 会让 git 改到真仓库）；Staged 自己继承 os.Environ()，所以要在进程环境里清。
func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	t.Setenv("GOWORK", "off") // 临时目录里的小模块不要被外面的 go.work 收编
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, code, err := gitrun.Run(dir, nil, nil, args...); err != nil || code != 0 {
		t.Fatalf("git %v 失败：code=%d err=%v", args, code, err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	clearGitEnv(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "core.autocrlf", "false")
	return dir
}

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pick 从真实的挑选表里按名字取项（测试跑的是生产的那一份，不是另造的）。
func pick(t *testing.T, want ...string) []Item {
	t.Helper()
	var out []Item
	for _, w := range want {
		found := false
		for _, it := range Items {
			if it.Name == w {
				out = append(out, it)
				found = true
			}
		}
		if !found {
			t.Fatalf("表里没有 %s", w)
		}
	}
	return out
}

func execute(t *testing.T, root string, items []Item, staged []string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := Execute(&Ctx{Root: root}, items, staged, &out)
	return out.String(), code
}

func fake(name string, code int, text string) Item {
	return Item{Name: name, Run: func(_ *Ctx, _ []string, w io.Writer) int {
		fmt.Fprint(w, text)
		return code
	}}
}

func TestExecuteAllGreen(t *testing.T) {
	out, code := execute(t, t.TempDir(), []Item{fake("甲", 0, "甲的输出\n"), fake("乙", 0, "")}, nil)
	if code != 0 {
		t.Fatalf("全过应退出 0：%d\n%s", code, out)
	}
	if !strings.Contains(out, "== 甲：过") || !strings.Contains(out, "甲的输出") || !strings.Contains(out, "汇总：2 项过、0 项红、0 项没挑上") {
		t.Fatalf("输出不对：\n%s", out)
	}
}

// 改坏检查 2 的靶子：只要有一项红，退出码就是 1，其余项照跑、照打印。
func TestExecuteRedItemMakesExitCodeOne(t *testing.T) {
	for _, red := range []int{1, 2, 255} {
		items := []Item{fake("甲", 0, "甲\n"), fake("乙", red, "乙出错了\n"), fake("丙", 0, "丙\n")}
		out, code := execute(t, t.TempDir(), items, nil)
		if code != 1 {
			t.Fatalf("乙退出码 %d 时总退出码应为 1，得到 %d\n%s", red, code, out)
		}
		for _, want := range []string{fmt.Sprintf("== 乙：红（退出码 %d）", red), "乙出错了", "== 丙：过", "汇总：2 项过、1 项红、0 项没挑上", "红的：乙"} {
			if !strings.Contains(out, want) {
				t.Fatalf("缺 %q：\n%s", want, out)
			}
		}
	}
}

func TestExecuteCountsNotPicked(t *testing.T) {
	items := []Item{fake("总跑", 0, ""), {Name: "只跑 ts", Globs: []string{"**/*.ts"}, Run: func(*Ctx, []string, io.Writer) int { return 0 }}}
	out, code := execute(t, t.TempDir(), items, []string{"a.md"})
	if code != 0 || !strings.Contains(out, "汇总：1 项过、0 项红、1 项没挑上") {
		t.Fatalf("code=%d\n%s", code, out)
	}
	if strings.Contains(out, "只跑 ts") {
		t.Fatalf("没挑上的不该出现在输出里：\n%s", out)
	}
}

func TestExecutePanicIsRedAndOthersStillRun(t *testing.T) {
	boom := Item{Name: "崩", Run: func(*Ctx, []string, io.Writer) int { panic("炸了") }}
	out, code := execute(t, t.TempDir(), []Item{boom, fake("好", 0, "好的输出\n")}, nil)
	if code != 1 || !strings.Contains(out, "检查自己崩溃了：炸了") || !strings.Contains(out, "好的输出") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

// 改坏检查 3 的靶子：并行的几项各有各的输出，段里只能有自己的行（规格“禁止的偷懒写法”：并行项不共用输出）。
func TestExecuteOutputsStaySeparate(t *testing.T) {
	var items []Item
	for i := 0; i < 8; i++ {
		tag := fmt.Sprintf("项%d", i)
		items = append(items, Item{Name: tag, Run: func(_ *Ctx, _ []string, w io.Writer) int {
			for n := 0; n < 200; n++ {
				fmt.Fprintf(w, "%s 第 %d 行\n", tag, n)
				if n%10 == 0 {
					runtime.Gosched()
				}
			}
			return 0
		}})
	}
	out, code := execute(t, t.TempDir(), items, nil)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	sections := strings.Split(out, "== 项")[1:]
	if len(sections) != 8 {
		t.Fatalf("段数不对：%d", len(sections))
	}
	for i := 0; i < 8; i++ {
		tag := fmt.Sprintf("项%d", i)
		sec := sections[i]
		if !strings.HasPrefix(sec, fmt.Sprintf("%d：过", i)) {
			t.Fatalf("第 %d 段不是 %s 的：%.40q", i, tag, sec)
		}
		if got := strings.Count(sec, tag+" 第 "); got != 200 {
			t.Fatalf("%s 的段里有 %d 行自己的输出，应为 200", tag, got)
		}
		for j := 0; j < 8; j++ {
			if j != i && strings.Contains(sec, fmt.Sprintf("项%d 第 ", j)) {
				t.Fatalf("%s 的段里混进了项%d 的输出", tag, j)
			}
		}
	}
}

func TestStagedOnlyACMRAndSlashPaths(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "old.go", "package x\n")
	put(t, dir, "gone.go", "package x\n")
	put(t, dir, "keep/中文 路径.md", "# a\n")
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init")
	git(t, dir, "rm", "-q", "gone.go") // 删除：不算
	if err := os.MkdirAll(filepath.Join(dir, "native"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "mv", "old.go", "native/new.go") // 改名：算新名字
	put(t, dir, "keep/中文 路径.md", "# b\n")        // 修改：算
	put(t, dir, "sub/added.txt", "x\n")          // 新增：算
	git(t, dir, "add", "-A")
	got, err := Staged(dir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"native/new.go": true, "keep/中文 路径.md": true, "sub/added.txt": true}
	if len(got) != len(want) {
		t.Fatalf("暂存文件 %v，应为 %v", got, want)
	}
	for _, f := range got {
		if !want[f] {
			t.Fatalf("多出 %q：%v", f, got)
		}
	}
}

func TestStagedFailsOutsideRepo(t *testing.T) {
	clearGitEnv(t)
	if _, err := Staged(t.TempDir(), os.Environ()); err == nil {
		t.Fatal("不在仓库里应报错，不能当作没有暂存文件放行")
	}
}

// 接线：真的挑选表里的“冲突标记”项，对暂存区里的冲突标记要红，没有就过。
func TestConflictItemWired(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "ok.md", "# 干净\n")
	git(t, dir, "add", "ok.md")
	if out, code := execute(t, dir, pick(t, "冲突标记"), []string{"ok.md"}); code != 0 {
		t.Fatalf("干净的应过：%d\n%s", code, out)
	}
	put(t, dir, "bad.md", "a\n<<<<<<< HEAD\nb\n=======\nc\n>>>>>>> x\n")
	git(t, dir, "add", "bad.md")
	out, code := execute(t, dir, pick(t, "冲突标记"), []string{"bad.md"})
	if code != 1 || !strings.Contains(out, "bad.md:2") {
		t.Fatalf("有冲突标记应红：%d\n%s", code, out)
	}
}

const goodGo = "package x\n\nfunc F() int { return 1 }\n"

// CRLF 的工作区（autocrlf 签出来的 .go 是 CRLF）：Go格式 读暂存区里的 LF，不误报。
func TestGoFmtItemIgnoresCRLFWorkingTree(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "native/x.go", goodGo)
	git(t, dir, "add", "native/x.go")
	put(t, dir, "native/x.go", strings.ReplaceAll(goodGo, "\n", "\r\n"))
	out, code := execute(t, dir, pick(t, "Go格式"), []string{"native/x.go"})
	if code != 0 {
		t.Fatalf("暂存区格式对、工作区是 CRLF：应过 %d\n%s", code, out)
	}
	put(t, dir, "native/y.go", "package x\nfunc G()   int {return 1}\n")
	git(t, dir, "add", "native/y.go")
	out, code = execute(t, dir, pick(t, "Go格式"), []string{"native/y.go"})
	if code != 1 || !strings.Contains(out, "native/y.go") {
		t.Fatalf("没 gofmt 应红：%d\n%s", code, out)
	}
}

func goModule(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	put(t, dir, "native/go.mod", "module example.test/m\n\ngo 1.22\n")
	// Printf 的参数个数对不上：vet 一定报
	put(t, dir, "native/bad/b.go", "package bad\n\nimport \"fmt\"\n\nfunc F() { fmt.Printf(\"%d %d\\n\", 1) }\n")
	put(t, dir, "native/good/g.go", "package good\n\nfunc G() int { return 1 }\n")
	return dir
}

func TestGoVetItem(t *testing.T) {
	dir := goModule(t)
	vet := pick(t, "Go检查")
	if out, code := execute(t, dir, vet, []string{"native/bad/b.go"}); code != 1 || !strings.Contains(out, "Printf") {
		t.Fatalf("暂存的包 vet 报错应红：%d\n%s", code, out)
	}
	if out, code := execute(t, dir, vet, []string{"native/good/g.go"}); code != 0 {
		t.Fatalf("别的包有问题不归这次管：%d\n%s", code, out)
	}
	if out, code := execute(t, dir, vet, []string{"native/go.mod"}); code != 1 {
		t.Fatalf("go.mod 变了 vet 整个模块，bad 也在里面：%d\n%s", code, out)
	}
}

func TestGoVetSkipsPackagesWithoutBuildableFilesAndDeletedDirs(t *testing.T) {
	dir := goModule(t)
	put(t, dir, "native/lin/a_plan9.go", "package lin\n\nfunc F() {}\n") // 本机（Windows / Linux）都不编
	vet := pick(t, "Go检查")
	if out, code := execute(t, dir, vet, []string{"native/lin/a_plan9.go"}); code != 0 || !strings.Contains(out, "没有落在还存在的包里") {
		t.Fatalf("构建约束排除的目录应跳过：%d\n%s", code, out)
	}
	if out, code := execute(t, dir, vet, []string{"native/gone/dir/z.go", "native/x/testdata/t.go"}); code != 0 || !strings.Contains(out, "没有落在还存在的包里") {
		t.Fatalf("已删的目录和 testdata 应跳过：%d\n%s", code, out)
	}
}

// 起不来的程序算红，不当成通过（“缺外部依赖”不能静默放行）。
func TestMissingProgramIsRed(t *testing.T) {
	old := BunProgram
	BunProgram = filepath.Join(t.TempDir(), "不存在的bun")
	t.Cleanup(func() { BunProgram = old })
	out, code := execute(t, t.TempDir(), pick(t, "类型检查"), []string{"scripts/a.ts"})
	if code != 1 || !strings.Contains(out, "起不来") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

// 子进程不带 GIT_*：钩子里继承的 GIT_DIR 指到别处，子进程里的 git 仍落在 dir 自己的仓库上。
func TestSpawnDropsGitEnv(t *testing.T) {
	dir := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "别的仓库", ".git"))
	var out bytes.Buffer
	c := &Ctx{Root: dir}
	if code := c.spawn(&out, dir, gitrun.Program, "rev-parse", "--is-inside-work-tree"); code != 0 || strings.TrimSpace(out.String()) != "true" {
		t.Fatalf("code=%d %q", code, out.String())
	}
}

// 暂存区什么都没有：整个钩子跳过（和原 lefthook 一样），退出 0；只有删除时 stagedAny 看得到、Staged 看不到。
func TestPreCommitSkipsWhenNothingStaged(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.go", "package x\n")
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init")
	var out bytes.Buffer
	if code := PreCommit(nil, dir, &out, &out); code != 0 || !strings.Contains(out.String(), "暂存区没有改动") {
		t.Fatalf("空暂存区应跳过：%d %q", code, out.String())
	}
	git(t, dir, "rm", "-q", "a.go")
	if got, err := Staged(dir, os.Environ()); err != nil || len(got) != 0 {
		t.Fatalf("纯删除时 Staged 应为空：%v %v", got, err)
	}
	if got, err := stagedAny(dir, os.Environ()); err != nil || len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("纯删除时 stagedAny 应看到 a.go：%v %v", got, err)
	}
}

func TestHooksRegistered(t *testing.T) {
	for _, name := range []string{"pre-commit", "commit-msg"} {
		if Hooks[name] == nil {
			t.Errorf("hook 下少了 %s", name)
		}
	}
}
