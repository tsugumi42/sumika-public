package datalock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDataLockExclusionAndRelease(t *testing.T) {
	data := t.TempDir()
	lock, err := LockData(data)
	if err != nil {
		t.Fatalf("LockData: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	for _, acquire := range []struct {
		name string
		lock func(string) (*DataLock, error)
	}{
		{"LockData", LockData},
		{"LockExisting", LockExisting},
	} {
		t.Run(acquire.name, func(t *testing.T) {
			other, err := acquire.lock(data)
			if other != nil {
				t.Cleanup(func() { _ = other.Close() })
			}
			if !errors.Is(err, ErrDataInUse) {
				t.Fatalf("second %s: got %v, want ErrDataInUse", acquire.name, err)
			}
		})
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	again, err := LockData(data)
	if err != nil {
		t.Fatalf("LockData after release: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
}

func TestLockExistingDoesNotCreateLockFile(t *testing.T) {
	data := t.TempDir()
	lock, err := LockExisting(data)
	if lock != nil {
		t.Cleanup(func() { _ = lock.Close() })
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LockExisting: got %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(data, "go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LockExisting created or could not inspect go directory: %v", err)
	}
}

// 两把锁互不相干：backend.lock 被拿着不影响 local.lock，反过来也一样；各自被占时报各自的错
func TestLocalLockIndependentOfBackendLock(t *testing.T) {
	data := t.TempDir()
	backend, err := LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	local, err := LockLocal(data)
	if err != nil {
		t.Fatalf("backend.lock 被拿着时 local.lock 该能拿：%v", err)
	}
	t.Cleanup(func() { _ = local.Close() })

	if _, err := LockLocal(data); !errors.Is(err, ErrLocalInUse) || errors.Is(err, ErrDataInUse) {
		t.Fatalf("local.lock 被占该是 ErrLocalInUse（不是 ErrDataInUse）：%v", err)
	}
	if _, err := LockData(data); !errors.Is(err, ErrDataInUse) || errors.Is(err, ErrLocalInUse) {
		t.Fatalf("backend.lock 被占该是 ErrDataInUse（不是 ErrLocalInUse）：%v", err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := LockLocal(data)
	if err != nil {
		t.Fatalf("放掉以后该能再拿：%v", err)
	}
	_ = again.Close()
}

// local.lock 文件已经存在时 LockExisting 也要认它（复核 R3）：Go 不在、Bun 在写本地时，老工具不能和它同时碰库。
// 不存在就不探、不建
func TestLockExistingHonoursLocalLock(t *testing.T) {
	data := t.TempDir()
	backend, err := LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil { // backend.lock 的文件留着，Go 不在
		t.Fatal(err)
	}

	t.Run("local.lock 不存在：照旧拿得到，也不去建它", func(t *testing.T) {
		l, err := LockExisting(data)
		if err != nil {
			t.Fatalf("没人在写本地该拿得到：%v", err)
		}
		_ = l.Close()
		if _, err := os.Stat(filepath.Join(data, "go", LocalLockName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("只读诊断不能为了探测去建 local.lock：%v", err)
		}
	})

	t.Run("local.lock 被占（Bun 在写）：ErrLocalInUse，并且没把 backend.lock 留在手里", func(t *testing.T) {
		bun, err := LockLocal(data)
		if err != nil {
			t.Fatal(err)
		}
		defer bun.Close()
		l, err := LockExisting(data)
		if l != nil {
			_ = l.Close()
		}
		if !errors.Is(err, ErrLocalInUse) || errors.Is(err, ErrDataInUse) {
			t.Fatalf("local.lock 被占该是 ErrLocalInUse：%v", err)
		}
		again, err := LockData(data)
		if err != nil {
			t.Fatalf("拒绝之后不该占着 backend.lock：%v", err)
		}
		_ = again.Close()
	})

	t.Run("local.lock 存在但没人拿：拿得到，一起放", func(t *testing.T) {
		l, err := LockExisting(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := LockLocal(data); !errors.Is(err, ErrLocalInUse) {
			t.Fatalf("诊断期间不该有人拿到 local.lock（写者要等它结束）：%v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		free, err := LockLocal(data)
		if err != nil {
			t.Fatalf("Close 之后 local.lock 该放掉：%v", err)
		}
		_ = free.Close()
	})

	t.Run("backend.lock 被占：还是 ErrDataInUse", func(t *testing.T) {
		goBackend, err := LockData(data)
		if err != nil {
			t.Fatal(err)
		}
		defer goBackend.Close()
		if _, err := LockExisting(data); !errors.Is(err, ErrDataInUse) {
			t.Fatalf("%v", err)
		}
	})
}

// 会写本地库的老工具用 LockExistingForWrite：两把锁都拿到才算数，没有就建 local.lock，哪把被占都拒绝，拒绝后什么都不占着
func TestLockExistingForWriteNeedsBothLocks(t *testing.T) {
	t.Run("没有 backend.lock 文件：不建，原样报打开的错", func(t *testing.T) {
		data := t.TempDir()
		if l, err := LockExistingForWrite(data); l != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%v %v", l, err)
		}
		if _, err := os.Stat(filepath.Join(data, "go")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("不该建 go 目录：%v", err)
		}
	})

	data := t.TempDir()
	first, err := LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	t.Run("local.lock 不存在：建出来并拿住，Bun 之后起来拿不到", func(t *testing.T) {
		l, err := LockExistingForWrite(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := LockLocal(data); !errors.Is(err, ErrLocalInUse) {
			t.Fatalf("写的时候 Bun 不该拿得到 local.lock：%v", err)
		}
		if _, err := LockData(data); !errors.Is(err, ErrDataInUse) {
			t.Fatalf("写的时候 Go 后台不该拿得到 backend.lock：%v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		for _, lock := range []func(string) (*DataLock, error){LockData, LockLocal} {
			again, err := lock(data)
			if err != nil {
				t.Fatalf("Close 之后两把锁都该放掉：%v", err)
			}
			_ = again.Close()
		}
	})

	t.Run("local.lock 被占：ErrLocalInUse，backend.lock 不留", func(t *testing.T) {
		bun, err := LockLocal(data)
		if err != nil {
			t.Fatal(err)
		}
		defer bun.Close()
		l, err := LockExistingForWrite(data)
		if l != nil {
			_ = l.Close()
		}
		if !errors.Is(err, ErrLocalInUse) || errors.Is(err, ErrDataInUse) {
			t.Fatalf("该是 ErrLocalInUse：%v", err)
		}
		again, err := LockData(data)
		if err != nil {
			t.Fatalf("拒绝之后不该占着 backend.lock：%v", err)
		}
		_ = again.Close()
	})

	t.Run("backend.lock 被占：ErrDataInUse，没建 local.lock 也没占它", func(t *testing.T) {
		fresh := t.TempDir()
		g, err := LockData(fresh)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if _, err := LockExistingForWrite(fresh); !errors.Is(err, ErrDataInUse) {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(filepath.Join(fresh, "go", LocalLockName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("backend.lock 都没拿到，不该去建 local.lock：%v", err)
		}
	})
}
