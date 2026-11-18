package uiskill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 全是编的小样本，不读真仓库。

const goMod = "module sumika-native\n\nrequire (\n\tgithub.com/egoist/mygo v0.9.0\n)\n"

func deps() Deps {
	return Deps{
		PathExists: func(p string) bool { return p == "native/internal/winshell" },
		HasName:    func(pkg, name string) bool { return pkg == "winshell" && name == "Kit" },
	}
}

func hasErr(r Result, parts ...string) bool {
	for _, e := range r.Errors {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(e, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestOver150LinesIsAnError(t *testing.T) {
	r := Check("mygo v0.9.0\n"+strings.Repeat("x\n", 150), goMod, deps())
	if !hasErr(r, "入口页 151 行，超过 150 行") {
		t.Fatalf("%v", r.Errors)
	}
	// 正好 150 行（含末尾换行）不报
	r = Check("mygo v0.9.0\n"+strings.Repeat("x\n", 149), goMod, deps())
	if len(r.Errors) != 0 {
		t.Fatalf("150 行不该报：%v", r.Errors)
	}
	// CRLF 也按行数
	r = Check("mygo v0.9.0\r\n"+strings.Repeat("x\r\n", 150), goMod, deps())
	if !hasErr(r, "151 行") {
		t.Fatalf("CRLF：%v", r.Errors)
	}
}

func TestMissingInternalPathReportedWithLine(t *testing.T) {
	r := Check("看 `internal/nope`。\n\nmygo v0.9.0\n", goMod, deps())
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "第 1 行") || !strings.Contains(r.Errors[0], "internal/nope") {
		t.Fatalf("%v", r.Errors)
	}
}

func TestMissingNameInKnownPackageReported(t *testing.T) {
	r := Check("用 `winshell.Missing`。\n\nmygo v0.9.0\n", goMod, deps())
	if len(r.Errors) != 1 || !hasErr(r, "第 1 行", "winshell.Missing", "找不到") {
		t.Fatalf("%v", r.Errors)
	}
}

func TestVersionMismatchAndMissing(t *testing.T) {
	r := Check("`winshell.Kit`\n\nmygo v0.1.0\n", goMod, deps())
	if !hasErr(r, "第 3 行", "v0.1.0", "v0.9.0") {
		t.Fatalf("%v", r.Errors)
	}
	r = Check("`winshell.Kit`\n", goMod, deps())
	if !hasErr(r, "没写版本") {
		t.Fatalf("%v", r.Errors)
	}
	r = Check("`winshell.Kit`\n\nmygo v0.9.0\n", "module x\n", deps())
	if !hasErr(r, "native/go.mod 里找不到 github.com/egoist/mygo 的版本") {
		t.Fatalf("%v", r.Errors)
	}
	// 版本号和 mygo 之间隔着换行也算（JS 的 \s）
	r = Check("`winshell.Kit`\n\nmygo\nv0.9.0\n", goMod, deps())
	if len(r.Errors) != 0 {
		t.Fatalf("%v", r.Errors)
	}
}

func TestAllCorrectCountsPackagesAndNames(t *testing.T) {
	r := Check("看 `internal/winshell`，用 `winshell.Kit`。\n\n版本 mygo v0.9.0。\n", goMod, deps())
	if len(r.Errors) != 0 || r.Packages != 1 || r.Names != 1 || len(r.Skipped) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestForeignPackageIsSkippedNotError(t *testing.T) {
	r := Check("用 `ui.NewTester`、`ui.NewTester`。\n\nmygo v0.9.0\n", goMod, deps())
	if len(r.Errors) != 0 || len(r.Skipped) != 1 || r.Skipped[0] != "ui.NewTester" {
		t.Fatalf("%+v", r)
	}
}

func TestSameMissingPathReportedOnceButNamesEveryTime(t *testing.T) {
	r := Check("`internal/nope` `internal/nope/`\n`winshell.Gone`\n`winshell.Gone`\nmygo v0.9.0\n", goMod, deps())
	if len(r.Errors) != 3 { // 路径一条（去重），名字两条（不去重）
		t.Fatalf("%v", r.Errors)
	}
}

// 用真文件测 Run：读不到文件、声明查找（func、方法、type，不看 _test.go）。
func TestRunOnTempTree(t *testing.T) {
	root := t.TempDir()
	put := func(p, body string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func() (int, string) {
		var out bytes.Buffer
		c := Run(nil, root, &out, &out)
		return c, out.String()
	}
	if c, out := run(); c != 1 || !strings.HasPrefix(out, "读不到要查的文件：") {
		t.Fatalf("没有入口页：%d %q", c, out)
	}
	put("native/go.mod", goMod)
	put("native/internal/winshell/kit.go", "package winshell\n\ntype Kit struct{}\n\nfunc (k *Kit) Open() {}\n\nfunc Build(x int) {}\n")
	put("native/internal/winshell/kit_test.go", "package winshell\n\nfunc OnlyInTest() {}\n")
	put(".claude/skills/mygo-ui/SKILL.md", "看 `internal/winshell/`，用 `winshell.Kit`、`winshell.Open`、`winshell.Build`、`ui.Tester`。mygo v0.9.0\n")
	c, out := run()
	if c != 0 || out != "核对了 1 个包、3 个名字，跳过 1 个\n没核对：ui.Tester\n" {
		t.Fatalf("%d %q", c, out)
	}
	put(".claude/skills/mygo-ui/SKILL.md", "`winshell.OnlyInTest` `winshell.Build` `internal/gone` mygo v0.8.0\n")
	c, out = run()
	want := "入口页和代码对不上：\n- 第 1 行：internal/gone 在 native/ 下不存在\n- 第 1 行：winshell.OnlyInTest 在 native/internal/winshell/ 下找不到\n- 第 1 行：mygo v0.8.0 和 go.mod 的 v0.9.0 不一致\n"
	if c != 1 || out != want {
		t.Fatalf("%d\n%q\n要\n%q", c, out, want)
	}
}

// 名字所在的包里有一个读不了的 .go 条目（这里是叫 x.go 的目录），排在真正有声明的文件前面：
// 旧版 readFileSync 抛错、退出 1；不能悄悄跳过它然后放行（2026-11-19 第 4 步复核）。
func TestUnreadableGoEntryFailsClosed(t *testing.T) {
	root := t.TempDir()
	put := func(p, body string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("native/go.mod", goMod)
	put("native/internal/dirgo/z.go", "package dirgo\n\nfunc Dg() {}\n")
	if err := os.MkdirAll(filepath.Join(root, "native/internal/dirgo/x.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(".claude/skills/mygo-ui/SKILL.md", "用 `dirgo.Dg`。mygo v0.9.0\n")
	var out bytes.Buffer
	if c := Run(nil, root, &out, &out); c != 1 || !strings.HasPrefix(out.String(), "读不到要查的文件：") {
		t.Fatalf("应失败：%d %q", c, out.String())
	}
}
