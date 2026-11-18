package archcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 规格-日志分级 3 节、5.1：模块和级别写在行首的标签里，消息正文不再自带前缀。
// 扫生产代码里交给日志出口的第一个字符串：以 `snowluma:` 这样的模块前缀、或 `WARN `、`ERROR ` 这样的级别词开头就红
// （前缀会让文件里变成 `INFO snowluma snowluma: …`；自己写级别词会让 `--level warn` 过滤不到）。
var logCallNames = map[string]bool{
	"logf": true, "Logf": true, "warnf": true, "Warnf": true, "errorf": true, "Errorf": true, "debugf": true, "Debugf": true,
}

// “链接插件：”这样的中文模块前缀也算（合并 main 时又带进来一次）
var logLead = regexp.MustCompile(`^(?:[a-z0-9_]+[:：]|链接插件[:：]|(?:DEBUG|INFO|WARN|ERROR)[ :：])`)

// 不走 Sumika 日志出口的目录：sumika-models 是单独的进程，行格式是它自己的（标准库 log 的时间 + 级别词 + model=…），
// 子进程的 stderr 只留尾部给启动失败的诊断，不按行并进 Sumika 的日志。
var logPrefixSkipDirs = []string{"internal/localmodels/"}

func TestLogMessagesHaveNoOwnPrefix(t *testing.T) {
	// archcheck 在 native/internal/archcheck：往上两层是 native/，连 cmd/ 和根包一起扫
	root := "../.."
	fset := token.NewFileSet()
	var hits []string
	err := filepath.WalkDir(filepath.Join(root, "."), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(path)
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, skip := range logPrefixSkipDirs {
			if strings.Contains(slash, skip) {
				return nil
			}
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && (x.Name == "fmt" || x.Name == "errors") {
					return true // fmt.Errorf 造的是错误值，不是日志
				}
				name = fn.Sel.Name
			}
			if !logCallNames[name] {
				return true
			}
			if s, ok := leadingString(call.Args[0]); ok && logLead.MatchString(s) {
				hits = append(hits, filepath.ToSlash(path)+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" "+strconv.Quote(clip(s)))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("日志消息自带模块前缀或级别词（模块和级别由出口加，正文不重复）：%s", h)
	}
}

// leadingString 取一个参数表达式最前面的字符串字面量（`"a"+x` 取 "a"）
func leadingString(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, err := strconv.Unquote(v.Value)
			return s, err == nil
		}
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			return leadingString(v.X)
		}
	}
	return "", false
}

func clip(s string) string {
	if r := []rune(s); len(r) > 24 {
		return string(r[:24]) + "…"
	}
	return s
}
