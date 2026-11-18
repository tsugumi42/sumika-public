//go:build !windows

package secrets

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// 返工四：非 Windows 上同样的错误码（这里 5，Windows 上是拒绝访问、Unix 上是 EIO）一律
// 不重试：立刻失败，一次都不等。
func TestRenameDoesNotRetryOnOtherPlatforms(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	oldRename, oldWait := renameTmp, renameRetryWait
	t.Cleanup(func() { renameTmp, renameRetryWait = oldRename, oldWait })
	calls := 0
	renameTmp = func(from, to string) error {
		calls++
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.Errno(5)}
	}
	renameRetryWait = func(time.Duration) { t.Error("非 Windows 上不该重试") }
	if err := s.Set("qq.100.token", "编的令牌"); err == nil {
		t.Fatal("替换失败时 Set 应报错")
	}
	if calls != 1 {
		t.Errorf("非 Windows 上不该重试，rename 调了 %d 次", calls)
	}
	if _, ok := s.Get("qq.100.token"); ok {
		t.Error("失败的值不该留在内存里")
	}
}
