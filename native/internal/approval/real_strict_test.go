package approval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRealStrictAndNewWriteTarget(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "new", "child.txt")
	if got, err := RealStrict(missing); err == nil || got != "" {
		t.Fatalf("读取不存在的目标必须失败：%q %v", got, err)
	}
	realRoot, err := RealStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := RealForWrite(missing); err != nil || got != filepath.Join(realRoot, "new", "child.txt") {
		t.Fatalf("新文件应接到真实父目录：%q %v", got, err)
	}
}

func TestRealForWriteDoesNotHideErrors(t *testing.T) {
	root := t.TempDir()
	for _, failure := range []error{os.ErrPermission, errors.New("共享冲突"), errors.New("认不准目标")} {
		t.Run(failure.Error(), func(t *testing.T) {
			calls := 0
			got, err := realForWrite(filepath.Join(root, "file"), func(p string) (string, error) {
				calls++
				if calls == 1 {
					return "", failure
				}
				return root, nil
			})
			if !errors.Is(err, failure) || got != "" || calls != 1 {
				t.Fatalf("不能沿父目录掩盖错误：got=%q err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestRealForWriteCleansBeforeResolve(t *testing.T) {
	root := t.TempDir()
	// 不用 Join：它会预先吃掉 ..，测不到解析前的词法处理。
	path := root + string(filepath.Separator) + "repo/missing/../../outside/a.txt"
	want := filepath.Join(root, "outside", "a.txt")
	got, err := realForWrite(path, func(p string) (string, error) {
		if p != filepath.Clean(p) {
			return "", errors.New("真实路径解析收到没清理的路径")
		}
		if p == root {
			return filepath.Join(root, "real"), nil
		}
		return "", os.ErrNotExist
	})
	if err != nil || got != filepath.Join(root, "real", "outside", "a.txt") {
		t.Fatalf("应先清理 %q 再解析父目录：%q %v；词法目标 %q", path, got, err, want)
	}
	if got, err := RealForWrite(path); err != nil || got != filepath.Join(RealOf(root), "outside", "a.txt") {
		t.Fatalf("真实新建路径应与清理后的目标一致：%q %v", got, err)
	}
}

func TestRealForWriteRejectsParentSuffix(t *testing.T) {
	// 相对 .. 也不能在真实父目录之后拼回；绝对输入 Clean 后不会再留下这种片段。
	got, err := realForWrite(filepath.Join("..", "new.txt"), func(p string) (string, error) {
		if p == "." {
			return t.TempDir(), nil
		}
		return "", os.ErrNotExist
	})
	if err == nil || got != "" {
		t.Fatalf("剩余路径中的 .. 必须失败：%q %v", got, err)
	}
}
