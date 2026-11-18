//go:build windows

package secrets

import (
	"errors"
	"path/filepath"
	"testing"
)

// 验收：真实 DPAPI 的本机副本（Go 自己写、自己解），模拟重装后主密码解开并重包。
func TestRealDPAPIRoundTrip(t *testing.T) {
	useTestCost(t) // 真实 DPAPI，scrypt 用测试小参数
	path := filepath.Join(t.TempDir(), "secrets.bin")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create：%v", err)
	}
	mustSet(t, s, "qq.100.token", "编的令牌")
	code := mustPassphrase(t, s, "真的主密码八个字")
	opened, err := Open(path)
	if err != nil {
		t.Fatalf("本机那份应能直接打开：%v", err)
	}
	if value, ok := opened.Get("qq.100.token"); !ok || value != "编的令牌" {
		t.Error("直接打开后应读到原值")
	}
	dropLocalCopy(t, path)
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("去掉本机那份后应锁着，得到 %v", err)
	}
	unlocked, err := Unlock(path, "真的主密码八个字", Passphrase)
	if err != nil {
		t.Fatalf("用主密码解开：%v", err)
	}
	if unlocked.LocalCopyError() != nil {
		t.Fatalf("本机副本应包回去，得到 %v", unlocked.LocalCopyError())
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("重包后应能直接打开：%v", err)
	}
	if _, err := Unlock(path, code, RecoveryCode); err != nil {
		t.Errorf("恢复码也应能用：%v", err)
	}
}
