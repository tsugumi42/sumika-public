package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/datalock"
)

func TestOpenIgnoresRetiredImportHistory(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{Archive, Receive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			// 先建立正常库，archive 随后只读打开它。
			e.open().cleanup()
			dir := filepath.Join(e.data, "go", "import", "interrupted")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "activation.json"), []byte("损坏的旧启用日志"), 0o600); err != nil {
				t.Fatal(err)
			}
			opt := e.options()
			opt.Mode = mode
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatalf("旧启用日志阻止后台启动：%v", err)
			}
			defer a.cleanup()
			if _, ok := a.set.Platform("qq"); !ok {
				t.Fatal("后台未打开平台库")
			}
		})
	}
}

// App 开着时一直拿着数据锁，关库后放掉。
func TestOpenAppHoldsDataLock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	lock, err := datalock.LockData(e.data)
	if lock != nil {
		lock.Close()
	}
	if !errors.Is(err, datalock.ErrDataInUse) {
		t.Fatalf("App 持库期间数据锁没被占着：%v", err)
	}
	a.cleanup()
	lock, err = datalock.LockData(e.data)
	if err != nil {
		t.Fatalf("关库后未放锁：%v", err)
	}
	lock.Close()
}
