package watchdog

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// 和 mygo/internal/windows/login.go 同一处：Run 项，以及任务管理器"启动"页关掉某项时写的 StartupApproved
const (
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKeyPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

type regStore struct{ run, approved string }

// RegistryRunStore 是真的当前用户 Run 项
func RegistryRunStore() RunStore { return regStore{run: runKeyPath, approved: approvedKeyPath} }

// registryRunStoreAt 换一对键路径（只给测试：用临时子键，不碰真的 Run 项）
func registryRunStoreAt(run, approved string) RunStore { return regStore{run: run, approved: approved} }

func (r regStore) Get(name string) (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, r.run, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, err == nil
}

func (r regStore) Set(name, value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, r.run, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, value)
}

func (r regStore) Delete(name string) error { return deleteValue(r.run, name) }

func (r regStore) ClearDisabled(name string) error { return deleteValue(r.approved, name) }

func (r regStore) Disabled(name string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, r.approved, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(name)
	return err == nil && len(b) > 0 && b[0]&1 == 1 // 第一个字节是奇数就是关掉了
}

// deleteValue 删一个值；键或值本来就不存在不算错
func deleteValue(path, name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// longPath 把 8.3 短路径名（PROGRA~1 那种）展开成长名字；文件不存在或展开失败原样返回
func longPath(p string) string {
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}
