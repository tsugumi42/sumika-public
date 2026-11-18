package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sumika-native/internal/testutil/junction"
)

func linkDir(t *testing.T, link, target string) {
	t.Helper()
	if err := junction.Create(link, target); err != nil {
		t.Fatalf("建编造联接：%v", err)
	}
}

func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// memoryPathFixture：编的数据目录，Go 的副本和 Bun 的原库各一份
func memoryPathFixture(t *testing.T) (data, goDB, bunDB string) {
	t.Helper()
	data = t.TempDir()
	goDB = filepath.Join(data, "go", "characters", "suzu", "memory.db")
	bunDB = filepath.Join(data, "characters", "suzu", "memory.db")
	put(t, goDB, "go")
	put(t, bunDB, "bun")
	return
}

func wantMemoryPathError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrMemoryPath) {
		t.Fatalf("应该被拒绝（ErrMemoryPath），得到 %v", err)
	}
}

func TestCheckMemoryCreatePath(t *testing.T) {
	t.Run("还不存在的一整条放行", func(t *testing.T) {
		data := t.TempDir()
		if err := CheckMemoryCreatePath(data, filepath.Join(data, "go", "characters", "suzu", "memory.db")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("中间目录已存在、文件还没有放行", func(t *testing.T) {
		data := t.TempDir()
		if err := os.MkdirAll(filepath.Join(data, "go", "characters", "suzu"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := CheckMemoryCreatePath(data, filepath.Join(data, "go", "characters", "suzu", "memory.db")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("0字节文件放行", func(t *testing.T) {
		data, goDB, _ := memoryPathFixture(t)
		if err := os.Truncate(goDB, 0); err != nil {
			t.Fatal(err)
		}
		if err := CheckMemoryCreatePath(data, goDB); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("角色目录是指向数据目录外的联接拒绝", func(t *testing.T) {
		data := t.TempDir()
		outside := t.TempDir()
		parent := filepath.Join(data, "go", "characters")
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		linkDir(t, filepath.Join(parent, "suzu"), outside)
		wantMemoryPathError(t, CheckMemoryCreatePath(data, filepath.Join(parent, "suzu", "memory.db")))
	})
	t.Run("库文件是悬空符号链接拒绝", func(t *testing.T) {
		data := t.TempDir()
		file := filepath.Join(data, "go", "characters", "suzu", "memory.db")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "外面.db"), file); err != nil {
			t.Skipf("这台机器建不了文件符号链接（%v）", err)
		}
		wantMemoryPathError(t, CheckMemoryCreatePath(data, file))
	})
	t.Run("数据目录是联接、下面都还没有放行", func(t *testing.T) {
		data := t.TempDir()
		alias := filepath.Join(t.TempDir(), "别名")
		linkDir(t, alias, data)
		if err := CheckMemoryCreatePath(alias, filepath.Join(alias, "go", "characters", "suzu", "memory.db")); err != nil {
			t.Fatalf("数据目录是联接时建库地址应该放行：%v", err)
		}
	})
	t.Run("形状不对拒绝", func(t *testing.T) {
		data := t.TempDir()
		for _, rel := range []string{"go/characters/memory.db", "go/characters/suzu/sub/memory.db", "go/memory.db", "memory.db"} {
			wantMemoryPathError(t, CheckMemoryCreatePath(data, filepath.Join(data, filepath.FromSlash(rel))))
		}
	})
}

func TestCheckMemoryPath(t *testing.T) {
	t.Run("go副本放行", func(t *testing.T) {
		data, goDB, _ := memoryPathFixture(t)
		if err := CheckMemoryPath(data, goDB); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Bun原库拒绝", func(t *testing.T) {
		data, _, bunDB := memoryPathFixture(t)
		wantMemoryPathError(t, CheckMemoryPath(data, bunDB))
	})
	t.Run("点点绕回Bun原库拒绝", func(t *testing.T) {
		data, _, _ := memoryPathFixture(t)
		wantMemoryPathError(t, CheckMemoryPath(data, filepath.Join(data, "go", "characters", "..", "..", "characters", "suzu", "memory.db")))
	})
	t.Run("数据目录之外拒绝", func(t *testing.T) {
		data, _, _ := memoryPathFixture(t)
		other := filepath.Join(t.TempDir(), "go", "characters", "suzu", "memory.db")
		put(t, other, "x")
		wantMemoryPathError(t, CheckMemoryPath(data, other))
	})
	t.Run("形状不对拒绝", func(t *testing.T) {
		data, _, _ := memoryPathFixture(t)
		for _, rel := range []string{"go/characters/memory.db", "go/characters/suzu/sub/memory.db", "go/memory.db", "memory.db"} {
			p := filepath.Join(data, filepath.FromSlash(rel))
			put(t, p, "x")
			wantMemoryPathError(t, CheckMemoryPath(data, p))
		}
	})
	t.Run("目录不是文件拒绝", func(t *testing.T) {
		data, _, _ := memoryPathFixture(t)
		d := filepath.Join(data, "go", "characters", "suzu", "memory.db.d")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		wantMemoryPathError(t, CheckMemoryPath(data, d))
	})
	t.Run("文件不存在不算不是副本", func(t *testing.T) {
		data, goDB, _ := memoryPathFixture(t)
		if err := os.Remove(goDB); err != nil {
			t.Fatal(err)
		}
		err := CheckMemoryPath(data, goDB)
		if !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrMemoryPath) {
			t.Fatalf("缺文件应该报不存在，得到 %v", err)
		}
	})
	t.Run("角色目录是指向Bun的联接拒绝", func(t *testing.T) {
		data, goDB, _ := memoryPathFixture(t)
		dir := filepath.Dir(goDB)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		linkDir(t, dir, filepath.Join(data, "characters", "suzu"))
		wantMemoryPathError(t, CheckMemoryPath(data, goDB))
	})
	t.Run("角色目录是指向另一个角色的联接拒绝", func(t *testing.T) {
		// 真实位置仍在 go/characters/<角色>/ 下，只有"路径里有联接"这一条拦得住：suzu 的名字下挂的是 kiri 的库
		data, goDB, _ := memoryPathFixture(t)
		put(t, filepath.Join(data, "go", "characters", "kiri", "memory.db"), "kiri")
		dir := filepath.Dir(goDB)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		linkDir(t, dir, filepath.Join(data, "go", "characters", "kiri"))
		wantMemoryPathError(t, CheckMemoryPath(data, goDB))
	})
	t.Run("go目录是联接拒绝", func(t *testing.T) {
		data, goDB, _ := memoryPathFixture(t)
		// 另一处真实的 go/characters/suzu/memory.db，数据目录里的 go 联接过去：真实位置也在 characters/<角色>/ 下，但中间有联接
		elsewhere := t.TempDir()
		put(t, filepath.Join(elsewhere, "characters", "suzu", "memory.db"), "x")
		if err := os.RemoveAll(filepath.Join(data, "go")); err != nil {
			t.Fatal(err)
		}
		linkDir(t, filepath.Join(data, "go"), elsewhere)
		wantMemoryPathError(t, CheckMemoryPath(data, goDB))
	})
	t.Run("库文件本身是符号链接拒绝", func(t *testing.T) {
		data, goDB, bunDB := memoryPathFixture(t)
		if err := os.Remove(goDB); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(bunDB, goDB); err != nil {
			t.Skipf("这台机器建不了文件符号链接（%v）；目录联接那几格已经覆盖了联接", err)
		}
		wantMemoryPathError(t, CheckMemoryPath(data, goDB))
	})
	t.Run("硬链接到Bun原库拒绝", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("只在 Windows 上查硬链接数")
		}
		data, goDB, bunDB := memoryPathFixture(t)
		if err := os.Remove(goDB); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(bunDB, goDB); err != nil {
			t.Fatal(err)
		}
		wantMemoryPathError(t, CheckMemoryPath(data, goDB))
	})
	t.Run("数据目录本身是联接放行", func(t *testing.T) {
		data, _, _ := memoryPathFixture(t)
		alias := filepath.Join(t.TempDir(), "别名")
		linkDir(t, alias, data)
		if err := CheckMemoryPath(alias, filepath.Join(alias, "go", "characters", "suzu", "memory.db")); err != nil {
			t.Fatalf("数据目录是联接时副本应该放行（启动时的 paths.Open 也允许）：%v", err)
		}
	})
	t.Run("数据目录是联接而角色目录也是联接拒绝", func(t *testing.T) {
		data, goDB, _ := memoryPathFixture(t)
		dir := filepath.Dir(goDB)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		linkDir(t, dir, filepath.Join(data, "characters", "suzu"))
		alias := filepath.Join(t.TempDir(), "别名")
		linkDir(t, alias, data)
		wantMemoryPathError(t, CheckMemoryPath(alias, filepath.Join(alias, "go", "characters", "suzu", "memory.db")))
	})
	t.Run("大小写变体", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("只有 Windows 的路径不分大小写")
		}
		data, _, _ := memoryPathFixture(t)
		upper := filepath.Join(data, "GO", "Characters", "SUZU", "MEMORY.DB")
		if err := CheckMemoryPath(data, upper); err != nil {
			t.Fatalf("同一个 Go 副本改大小写还是它：%v", err)
		}
		wantMemoryPathError(t, CheckMemoryPath(data, filepath.Join(data, "CHARACTERS", "SUZU", "MEMORY.DB")))
	})
}
