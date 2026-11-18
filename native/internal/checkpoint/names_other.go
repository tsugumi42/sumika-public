//go:build !windows

package checkpoint

// 非 Windows 目录保持敏感；保留叶子原名，不引入通用 Unicode 折叠。
func insensitiveNameKey(name string) (string, error) { return name, nil }
