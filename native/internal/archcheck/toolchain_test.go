package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 外部程序的查找收到一处（规格-外部程序清单 设计第 6 条）：native/internal/ 下非测试的 Go 文件里，
// exec.LookPath 只许出现在 internal/toolchain；exec.Command / exec.CommandContext 的第一个参数是
// 字符串字面量且不是绝对路径的，也只许出现在 internal/toolchain。
// 例外清单只许变短；每条写清为什么留、什么时候去掉。
type execException struct{ File, Reason string }

var (
	execLookPathExceptions = []execException{
		{File: "mcpclient/process.go", Reason: "MCP 命令是用户在配置里写的名字，按 PATH 找是用户的本意，不是登记表里的固定程序"},
	}
	execCommandLiteralExceptions = []execException{
		{File: "bench/ledger.go", Reason: "magpie 是开发用的用量 CLI，不是运行期程序，登记表不登记它"},
	}
	execExceptionMax = 2
)

type execHit struct {
	File string
	Line int
	Call string // LookPath、Command、CommandContext
	Arg  string // 字面量的第一个参数；LookPath 是名字
}

type execScan struct {
	files    int
	hits     []execHit
	problems []string
}

// scanExecCalls 扫 root 下所有非测试的 .go 文件；按 import 认 os/exec 的本地名字，别名也认。
func scanExecCalls(root string) execScan {
	s := execScan{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(p)
		if err != nil {
			s.problems = append(s.problems, rel+"：读不了（"+err.Error()+"）")
			return nil
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			s.problems = append(s.problems, rel+"：解析失败（"+err.Error()+"）")
			return nil
		}
		s.files++
		imports := map[string]string{}
		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			name := path.Base(importPath)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = importPath
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || imports[ident.Name] != "os/exec" {
				return true
			}
			switch sel.Sel.Name {
			case "LookPath":
				arg, _ := stringLiteral(call.Args, 0)
				s.hits = append(s.hits, execHit{File: rel, Line: fset.Position(call.Pos()).Line, Call: "LookPath", Arg: arg})
			case "Command", "CommandContext":
				idx := 0
				if sel.Sel.Name == "CommandContext" {
					idx = 1
				}
				arg, ok := stringLiteral(call.Args, idx)
				if !ok {
					return true
				}
				if filepath.IsAbs(arg) {
					return true
				}
				s.hits = append(s.hits, execHit{File: rel, Line: fset.Position(call.Pos()).Line, Call: sel.Sel.Name, Arg: arg})
			}
			return true
		})
		return nil
	})
	if err != nil {
		s.problems = append(s.problems, "扫描 "+root+" 失败："+err.Error())
	}
	return s
}

// stringLiteral 取第 i 个参数，必须是解引号成功的字符串字面量。
func stringLiteral(args []ast.Expr, i int) (string, bool) {
	if i >= len(args) {
		return "", false
	}
	lit, ok := args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return text, true
}

// execProblems 把扫描结果和例外清单对一遍。纯函数：真实的树和测试里的桩树用同一份判断。
func execProblems(s execScan, lookPathEx, commandEx []execException, max int) []string {
	out := append([]string(nil), s.problems...)
	key := func(file, kind string) string { return file + "\x00" + kind }
	exceptions := map[string]string{}
	add := func(list []execException, kind string) {
		for _, ex := range list {
			if strings.TrimSpace(ex.Reason) == "" {
				out = append(out, "例外清单里 "+ex.File+" 没写原因")
			}
			exceptions[key(ex.File, kind)] = ex.Reason
		}
	}
	add(lookPathEx, "LookPath")
	add(commandEx, "Command")
	if total := len(lookPathEx) + len(commandEx); total > max {
		out = append(out, fmt.Sprintf("例外清单有 %d 项，超过上限 %d：清单只许变少，要加条目先问主人", total, max))
	} else if total < max {
		out = append(out, fmt.Sprintf("例外清单只剩 %d 项，把 execExceptionMax 同步改小到 %d", total, total))
	}

	sort.Slice(s.hits, func(i, j int) bool {
		if s.hits[i].File != s.hits[j].File {
			return s.hits[i].File < s.hits[j].File
		}
		return s.hits[i].Line < s.hits[j].Line
	})
	used := map[string]bool{}
	for _, h := range s.hits {
		if strings.HasPrefix(h.File, "toolchain/") {
			continue
		}
		k := key(h.File, h.Call)
		if h.Call == "CommandContext" {
			k = key(h.File, "Command")
		}
		if _, ok := exceptions[k]; ok {
			used[k] = true
			continue
		}
		switch h.Call {
		case "LookPath":
			out = append(out, fmt.Sprintf("%s:%d 直接 exec.LookPath(%q)：外部程序一律向 internal/toolchain 要；真的有理由，登记进例外清单并写原因", h.File, h.Line, h.Arg))
		default:
			out = append(out, fmt.Sprintf("%s:%d 用字面量 %q 起程序（%s）：外部程序一律向 internal/toolchain 要，或写成绝对路径", h.File, h.Line, h.Arg, h.Call))
		}
	}
	for _, ex := range lookPathEx {
		if !used[key(ex.File, "LookPath")] {
			out = append(out, "例外清单里的 "+ex.File+" 已经没有 LookPath 命中了：从清单删掉这一项")
		}
	}
	for _, ex := range commandEx {
		if !used[key(ex.File, "Command")] {
			out = append(out, "例外清单里的 "+ex.File+" 已经没有字面量起程序命中了：从清单删掉这一项")
		}
	}
	return out
}

func TestInternalFindsExternalProgramsOnlyInToolchain(t *testing.T) {
	scan := scanExecCalls("..")
	if scan.files < minInternalGoFiles {
		t.Fatalf("只扫到 %d 个非测试的 .go 文件：扫描器坏了或根目录不对，这一项什么也没查", scan.files)
	}
	for _, p := range execProblems(scan, execLookPathExceptions, execCommandLiteralExceptions, execExceptionMax) {
		t.Error(p)
	}
}

// 测试自己的检查：自己找 LookPath、字面量起程序的写法必须报出来；绝对路径、变量、toolchain 自己、测试文件不报。
func TestExecCheckerShapes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"bad.go":          "package bad\n\nimport \"os/exec\"\n\nfunc f() { exec.LookPath(\"ffmpeg\") }\n",
		"literal.go":      "package bad\n\nimport \"os/exec\"\n\nfunc f() { exec.Command(\"ffmpeg\", \"-version\") }\n",
		"contextlit.go":   "package bad\n\nimport \"os/exec\"\n\nfunc f() { exec.CommandContext(nil, \"ffmpeg\", \"-version\") }\n",
		"alias.go":        "package bad\n\nimport e \"os/exec\"\n\nfunc f() { _, _ = e.LookPath(\"ffmpeg\") }\n",
		"absolute.go":     "package bad\n\nimport \"os/exec\"\n\nfunc f() { exec.Command(`C:\\tools\\ffmpeg.exe`) }\n",
		"variable.go":     "package bad\n\nimport \"os/exec\"\n\nfunc f(p string) { exec.Command(p) }\n",
		"toolchain/ok.go": "package toolchain\n\nimport \"os/exec\"\n\nfunc f() { exec.LookPath(\"ffmpeg\"); exec.Command(\"ffmpeg\") }\n",
		"skip_test.go":    "package bad\n\nimport \"os/exec\"\n\nfunc f() { exec.LookPath(\"ffmpeg\") }\n",
		"testdata/x.go":   "package x\n\nimport \"os/exec\"\n\nfunc f() { exec.LookPath(\"ffmpeg\") }\n",
	})

	scan := scanExecCalls(root)
	if len(scan.problems) != 0 {
		t.Fatalf("干净的桩树不该有问题：%v", scan.problems)
	}
	if scan.files != 7 {
		t.Errorf("应扫到 7 个非测试文件，实际 %d", scan.files)
	}
	got := map[string]string{}
	for _, h := range scan.hits {
		got[h.File] = h.Call
	}
	for file, call := range map[string]string{
		"bad.go": "LookPath", "literal.go": "Command", "contextlit.go": "CommandContext", "alias.go": "LookPath",
	} {
		if got[file] != call {
			t.Errorf("没扫到 %s 的 %s：%v", file, call, scan.hits)
		}
	}
	for _, file := range []string{"absolute.go", "variable.go", "skip_test.go", "testdata/x.go"} {
		if _, ok := got[file]; ok {
			t.Errorf("%s 不该报：%v", file, scan.hits)
		}
	}
	if strings.Contains(strings.Join(execProblems(scan, nil, nil, 0), "\n"), "toolchain/ok.go") {
		t.Error("internal/toolchain 自己不该报")
	}

	joined := strings.Join(execProblems(scan, nil, nil, 0), "\n")
	for _, must := range []string{"bad.go:5", `"ffmpeg"`, "literal.go:5", "contextlit.go:5", "alias.go:5"} {
		if !strings.Contains(joined, must) {
			t.Errorf("空清单下应报出 %q，实际：\n%s", must, joined)
		}
	}

	ex := []execException{{File: "bad.go", Reason: "测试"}}
	if out := execProblems(scan, ex, nil, 1); strings.Contains(strings.Join(out, "\n"), "bad.go:5") {
		t.Errorf("登记过的文件不该再报：%v", out)
	}
	stale := strings.Join(execProblems(scan, []execException{{File: "nope.go", Reason: "测试"}}, nil, 1), "\n")
	if !strings.Contains(stale, "nope.go") {
		t.Errorf("已经不成立的例外要报：%s", stale)
	}
}
