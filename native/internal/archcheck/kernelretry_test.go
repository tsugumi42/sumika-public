package archcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 回合内核故障的解除入口是主人和运维的操作，不是模型能做的事。解除这条链上的每一层都只许下面登记的文件引用：
//
//	RetryFault / RetryAllFaults / RetryFaultOf（turn.Hub）← internal/app/ctl_kernel.go
//	KernelRetry（App 的方法 / ctlserver 的后端接口）      ← internal/ctlserver/kernel.go、根包 ctl_kernel.go
//
// 查的是所有选择器引用，不只调用表达式：把方法当值取出来（`return h.RetryAllFaults`）、包一层再调，也算引用。
// 模型的工具清单里没有它由 turn 包的 TestRetryFaultIsNotInAnyModelToolList 守着；这条守的是“换个名字包一层工具”的路：
// 谁新加了引用点，这里就红，逼着作者回答“这是谁的操作、四种场合各是什么行为”。以后界面的“重试”按钮接进来时在这里加一行，并说明它走的是主窗口。
var kernelRetryRefs = map[string][]string{
	"RetryFault":     {"internal/app/ctl_kernel.go"},
	"RetryAllFaults": {"internal/app/ctl_kernel.go"},
	"RetryFaultOf":   {}, // 谁都不许调，直到界面的“重试”按钮接进来（到时在这里登记它的文件）
	"KernelRetry":    {"ctl_kernel.go", "internal/ctlserver/kernel.go"},
}

// kernelRetryRefsIn 返回一个源文件里引用了哪些受守卫的方法名（任何选择器 x.Name，不管是不是调用）。
func kernelRetryRefsIn(t *testing.T, name, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	// 包名限定的类型引用（ctlproto.KernelRetry 是协议里的数据类型）不是方法引用
	pkgs := map[string]bool{}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgs[name] = true
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, isIdent := sel.X.(*ast.Ident); isIdent && pkgs[x.Name] {
				return true
			}
			if _, guarded := kernelRetryRefs[sel.Sel.Name]; guarded {
				out = append(out, sel.Sel.Name)
			}
		}
		return true
	})
	return out
}

// kernelRetryViolations 按登记名单查一组文件（相对 native/ 的路径 → 源码），返回违规说明。
func kernelRetryViolations(t *testing.T, files map[string]string) []string {
	t.Helper()
	var bad []string
	for rel, src := range files {
		for _, name := range kernelRetryRefsIn(t, rel, src) {
			ok := false
			for _, allowed := range kernelRetryRefs[name] {
				ok = ok || allowed == rel
			}
			if !ok {
				bad = append(bad, rel+" 引用了 "+name)
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// 守卫自己能抓到两种绕法：别处直接调用，以及把方法当值取出来；登记的文件不报。
func TestKernelRetryGuardCatchesCallsAndMethodValues(t *testing.T) {
	got := kernelRetryViolations(t, map[string]string{
		"internal/tools/wrap.go":       "package tools\nfunc f(a interface{ KernelRetry() }) { a.KernelRetry() }\n",
		"internal/other/value.go":      "package other\nfunc g(h interface{ RetryAllFaults() }) func() { return h.RetryAllFaults }\n",
		"internal/app/ctl_kernel.go":   "package app\nfunc k(h interface{ RetryFault() }) { h.RetryFault() }\n",
		"internal/ctlserver/kernel.go": "package ctlserver\nfunc s(b interface{ KernelRetry() }) { b.KernelRetry() }\n",
	})
	want := []string{"internal/other/value.go 引用了 RetryAllFaults", "internal/tools/wrap.go 引用了 KernelRetry"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("守卫该抓到调用和方法值两种绕法、放过登记的文件：\n得到 %v\n应是 %v", got, want)
	}
}

func TestKernelRetryHasOnlyOperatorCallers(t *testing.T) {
	root := "../.." // native/
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(path)
		if d.IsDir() {
			// turn 包自己（定义和内部互相调用）和测试数据不查
			if d.Name() == "testdata" || d.Name() == "vendor" || strings.HasSuffix(slash, "internal/turn") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = string(src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range kernelRetryViolations(t, files) {
		t.Errorf("%s：内核故障的解除入口是主人和运维的操作，不能由别处（尤其模型能走到的路径）引用；确实要加，先在 kernelRetryRefs 里登记并写明是谁的操作", v)
	}
	// 登记的每个文件里都真的有引用：改名或搬家了就把名单一起改，不然这条测试什么也没查
	seen := map[string]bool{}
	for rel, src := range files {
		for _, name := range kernelRetryRefsIn(t, rel, src) {
			seen[name+"@"+rel] = true
		}
	}
	for name, allowed := range kernelRetryRefs {
		for _, rel := range allowed {
			if !seen[name+"@"+rel] {
				t.Errorf("登记的 %s 里没有找到对 %s 的引用：改名或搬家了就把 kernelRetryRefs 一起改", rel, name)
			}
		}
	}
}
