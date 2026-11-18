//go:build !windows

package watchdog

import "errors"

// longPath 只有 Windows 有 8.3 短路径名；别的系统原样返回
func longPath(p string) string { return p }

type noStore struct{}

// RegistryRunStore 只有 Windows 有
func RegistryRunStore() RunStore { return noStore{} }

func (noStore) Get(string) (string, bool) { return "", false }
func (noStore) Set(string, string) error  { return errors.New("只有 Windows 能设开机自启") }
func (noStore) Delete(string) error       { return errors.New("只有 Windows 能设开机自启") }
func (noStore) Disabled(string) bool      { return false }
func (noStore) ClearDisabled(string) error {
	return nil
}
