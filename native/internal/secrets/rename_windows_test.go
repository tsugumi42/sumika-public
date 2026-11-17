//go:build windows

package secrets

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 返工四：替换文件被拒（Windows 的拒绝访问）时照 robustio 重试，第三次成功就算写成。
func TestRenameRetriesAccessDenied(t *testing.T) {
	_, path := newVaultEnv(t)
	s := mustCreate(t, path)
	oldRename, oldWait := renameTmp, renameRetryWait
	t.Cleanup(func() { renameTmp, renameRetryWait = oldRename, oldWait })
	calls, waits := 0, 0
	renameTmp = func(from, to string) error {
		calls++
		if calls <= 2 {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_ACCESS_DENIED}
		}
		return oldRename(from, to)
	}
	renameRetryWait = func(time.Duration) { waits++ }
	mustSet(t, s, "qq.100.token", "编的令牌")
	if calls != 3 {
		t.Errorf("应重试到第三次成功，rename 调了 %d 次", calls)
	}
	if waits != 2 {
		t.Errorf("两次重试各应等一次，等了 %d 次", waits)
	}
	if value, ok := s.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("重试成功后值应在库里")
	}
	if _, err := Open(path); err != nil {
		t.Errorf("写成功后应能重新打开：%v", err)
	}
}
