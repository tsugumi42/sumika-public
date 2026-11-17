package archcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 主人的共用记忆库（owner-memory.db）的路径只在 internal/paths 拼一次，别处用 Paths.OwnerMemoryDB()。
// 各处自己 filepath.Join(SharedDir(), "owner-memory.db") 会让改位置时漏改、存储统计也认不出它（质检 X-28）。
// 扫生产代码里的字符串字面量；测试文件自己在临时目录造假库，不在范围内。
func TestOwnerMemoryPathOnlyInPaths(t *testing.T) {
	const name = "owner-memory.db" // 查的是文件名；存储类别的键 owner-memory 不是路径，不算
	const allowed = "internal/paths/paths.go"
	fset := token.NewFileSet()
	var hits []string
	sawPaths := false
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		isPaths := strings.HasSuffix(filepath.ToSlash(path), allowed)
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.Contains(s, name) {
				return true
			}
			if isPaths {
				sawPaths = true
				return true
			}
			hits = append(hits, filepath.ToSlash(path)+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+" "+strconv.Quote(s))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawPaths {
		t.Fatalf("%s 里找不到 %q：路径表改名或搬家了就把这个测试一起改，不然它什么也没查", allowed, name)
	}
	for _, h := range hits {
		t.Errorf("%s：共用记忆库的路径请用 paths.Paths.OwnerMemoryDB()，不要手拼", h)
	}
}
