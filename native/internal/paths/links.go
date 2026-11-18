package paths

import "strings"

// IsLink 报告 path 这一级是不是符号链接或重解析点（Windows 上联接、云盘占位都算）。备份、清理这类要“不跟随链接”的地方用它，
// 和 CheckMemoryPath 用的是同一个判断。
func IsLink(path string) (bool, error) { return isLink(path) }

// RealPath 把 path（要已经存在）解到真实位置：符号链接、联接都解开；Windows 上不分大小写的部分给出文件系统里的写法。
func RealPath(path string) (string, error) { return realPath(path) }

// StripDevicePrefix 去掉 Windows 的设备路径前缀，还原成普通写法：`\\?\D:\x` 和 `\\.\D:\x` 变成 `D:\x`，`\\?\UNC\主机\共享` 变成 `\\主机\共享`，
// 内核路径 `\??\D:\x` 同理（斜杠写法也认）。别的（`\\?\Volume{…}`、设备名）原样返回。
// 配置里写的路径要拿去和受保护目录比，带着前缀写出来的和规整后的 D:\x 比不上，比之前先用它去掉
func StripDevicePrefix(p string) string {
	if len(p) < 4 {
		return p
	}
	head := strings.ReplaceAll(p[:4], "/", `\`)
	if head != `\\?\` && head != `\\.\` && head != `\??\` {
		return p
	}
	rest := p[4:]
	if head != `\??\` && len(rest) >= 4 && strings.EqualFold(rest[:3], "UNC") && (rest[3] == '\\' || rest[3] == '/') {
		return `\\` + rest[4:]
	}
	if len(rest) >= 2 && rest[1] == ':' && (rest[0]|0x20 >= 'a' && rest[0]|0x20 <= 'z') {
		return rest
	}
	return p
}
