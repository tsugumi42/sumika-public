package settingsreg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/config"
)

// 每个键声明“后台谁读它”：readers 里写 包路径:函数#选择器，测试逐条核对——
// 包目录存在，函数（方法写成 类型.方法）在非测试文件里存在，函数体里真的出现了那个选择器（如 Rerank.K 或 K）。
// 编一个不存在的函数、或写了一个没碰这个字段的函数，都过不了。尚未生效的键必须没有 readers，其余必须有。

// 不算后台读取处：工具和评测、第三方代码、只做展示或登记的包。
var notReaderRoots = []string{"cmd/", "third_party/", "internal/settingsreg", "internal/desktopread", "internal/clientinfo"}

func nativeRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// funcBody 找 dir 下非测试文件里名叫 name 的函数；方法写成 Type.Method。
func funcBody(t *testing.T, dir, name string) (*ast.BlockStmt, bool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			got := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				rt := fd.Recv.List[0].Type
				if st, ok := rt.(*ast.StarExpr); ok {
					rt = st.X
				}
				if id, ok := rt.(*ast.Ident); ok {
					got = id.Name + "." + got
				}
			}
			if got == name {
				return fd.Body, true
			}
		}
	}
	return nil, false
}

// hasSelector 看函数体里有没有 a.b（两段）或 b（一段）形式的选择器。
// 写成带引号的字符串（如 "name"）时，找函数体里同样的字符串字面量：角色卡这类从表里按名取值的读法没有选择器。
func hasSelector(body *ast.BlockStmt, sel string) bool {
	if strings.HasPrefix(sel, `"`) {
		lit := false
		ast.Inspect(body, func(n ast.Node) bool {
			if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING && b.Value == sel {
				lit = true
			}
			return !lit
		})
		return lit
	}
	parts := strings.Split(sel, ".")
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		s, ok := n.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != parts[len(parts)-1] {
			return true
		}
		if len(parts) == 1 {
			found = true
			return false
		}
		if inner, ok := s.X.(*ast.SelectorExpr); ok && inner.Sel.Name == parts[0] {
			found = true
			return false
		}
		return true
	})
	return found
}

func splitReader(r string) (pkg, fn, sel string, ok bool) {
	head, sel, ok := strings.Cut(r, "#")
	if !ok || sel == "" {
		return "", "", "", false
	}
	pkg, fn, ok = strings.Cut(head, ":")
	return pkg, fn, sel, ok && pkg != "" && fn != ""
}

// 键对应的 Go 字段名链（只对 yml 存储的键有意义）
func goFieldChain(t reflect.Type, key string) []string {
	var chain []string
	for _, part := range strings.Split(key, ".") {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if part == "*" {
			switch t.Kind() {
			case reflect.Map, reflect.Slice, reflect.Array:
				t = t.Elem()
				continue
			}
			return chain
		}
		f, ok := fieldByYAML(t, part)
		if !ok {
			return chain
		}
		chain = append(chain, f.Name)
		t = f.Type
	}
	return chain
}

func fieldByYAML(t reflect.Type, name string) (reflect.StructField, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")
		if tag[0] == "-" {
			continue
		}
		if len(tag) > 1 && tag[1] == "inline" {
			if x, ok := fieldByYAML(f.Type, name); ok {
				return x, true
			}
			continue
		}
		if tag[0] == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func TestReadersPointAtRealCode(t *testing.T) {
	root := nativeRoot(t)
	cfgType := reflect.TypeOf(config.Config{})
	for _, r := range records {
		if r.effect == fxNone {
			if len(r.readers) != 0 {
				t.Errorf("%s 标了尚未生效，却写了读取处 %v", r.key, r.readers)
			}
			continue
		}
		if len(r.readers) == 0 {
			t.Errorf("%s 没有声明后台谁读它（readers 为空）", r.key)
			continue
		}
		chain := goFieldChain(cfgType, r.key)
		for _, rd := range r.readers {
			pkg, fn, sel, ok := splitReader(rd)
			if !ok {
				t.Errorf("%s 的读取处写法不对（要 包:函数#选择器）：%q", r.key, rd)
				continue
			}
			for _, bad := range notReaderRoots {
				if strings.HasPrefix(pkg, bad) {
					t.Errorf("%s 的读取处 %q 不算后台（工具、评测、展示用的包）", r.key, rd)
				}
			}
			dir := filepath.Join(root, filepath.FromSlash(pkg))
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				t.Errorf("%s 的读取处 %q：包目录 %s 不存在", r.key, rd, pkg)
				continue
			}
			body, found := funcBody(t, dir, fn)
			if !found {
				t.Errorf("%s 的读取处 %q：%s 里没有函数 %s", r.key, rd, pkg, fn)
				continue
			}
			if !hasSelector(body, sel) {
				t.Errorf("%s 的读取处 %q：函数体里没有 %s", r.key, rd, sel)
			}
			if r.storage == "" && len(chain) > 0 {
				last := sel[strings.LastIndex(sel, ".")+1:]
				match := false
				for _, c := range chain {
					match = match || c == last
				}
				if !match {
					t.Errorf("%s 的读取处选择器 %s 和这个键的字段 %v 对不上", r.key, sel, chain)
				}
			}
		}
	}
}

// 没有读取处的函数名写错的情形要能被测出来：造一个假记录走同一套检查
func TestReaderCheckRejectsInventedFunction(t *testing.T) {
	root := nativeRoot(t)
	if _, ok := funcBody(t, filepath.Join(root, "internal", "app"), "NoSuchFunctionAnywhere"); ok {
		t.Fatal("不存在的函数被认出来了")
	}
	if body, ok := funcBody(t, filepath.Join(root, "internal", "app"), "App.openSelfcheck"); !ok || hasSelector(body, "NoSuchField") {
		t.Fatal("函数存在，但不该有这个字段")
	}
}
