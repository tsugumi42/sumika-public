//go:build !windows

package linkcard

// privateBytes 只在 Windows 上量：泄漏的是 DirectWrite 的原生内存，别的平台没有这个现象。
func privateBytes() (uint64, bool) { return 0, false }
