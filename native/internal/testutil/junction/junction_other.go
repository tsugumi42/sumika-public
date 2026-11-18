//go:build !windows

package junction

import "os"

// Create 在 link 创建指向 target 的目录符号链接；父目录由调用者准备。
func Create(link, target string) error { return os.Symlink(target, link) }
