package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

// realPath 的结果不带 \\?\ 前缀，共享盘的前缀还原成 \\服务器\共享。
// 以前常量少写了一个反斜杠，去前缀的分支是死代码：CheckMemoryPath 碰巧对（两边都带同样的前缀），但哪天有一边不经 realPath 就会无声误判
func TestTrimVerbatim(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`\\?\C:\data\go`, `C:\data\go`},
		{`\\?\UNC\srv\share\data`, `\\srv\share\data`},
		{`C:\data\go`, `C:\data\go`},
		{`\\srv\share\data`, `\\srv\share\data`},
	} {
		if got := trimVerbatim(tc.in); got != tc.want {
			t.Errorf("trimVerbatim(%q) = %q，应该是 %q", tc.in, got, tc.want)
		}
	}
}

func TestRealPathHasNoVerbatimPrefix(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.db")
	put(t, file, "x")
	for _, p := range []string{dir, file} {
		got, err := realPath(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(got, `\\?\`) {
			t.Errorf("realPath(%q) = %q，不该带 verbatim 前缀（问号开头的那种）", p, got)
		}
		if !sameName(filepath.Base(got), filepath.Base(p)) {
			t.Errorf("realPath(%q) = %q，末级名字变了", p, got)
		}
	}
}
