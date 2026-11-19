package checkpoint

import (
	"errors"
	"fmt"
	"os"
)

// removeSameFile 在 dir 里删掉名为 name 的文件，但只在它仍是 want 描述的那一个文件时才删。
// dir 是调用方已经核验过身份的材料目录（removeRestoreMaterial 持有的那个 *os.Root），这里直接用它，
// 不按路径字符串重新打开目录，调用方也负责关它。
// 没有按句柄删除的原语的系统（Unix）用它：先用不跟链接的 Lstat 比对象身份，再按名删，
// 路径中间的链接、名字被换成链接或别的文件都不会让它删到别处。
// 残留的缝：比对和删除之间名字若被有权写这个目录的人换入别的文件，会删到新文件；
// 材料目录是检查点自己建的，能写它的人本来就能直接改被还原的文件，这条缝不扩大谁能做什么。
// 调用方必须在整个比对和删除期间一直开着 want 对应的那个文件：Unix 上靠设备号加 inode 号认文件，
// 文件一旦没人握着又被删掉，inode 号会被新建的文件复用，别的文件就“长得像”它了。
// 名字已经不在了算做完（目标就是它不在）
func removeSameFile(dir *os.Root, name string, want os.FileInfo) error {
	cur, err := dir.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// 类型也要一样：符号链接、目录等换在这个名字上，即使设备号和 inode 号碰巧相同（Linux 上释放的 inode 号会马上被新文件复用）也不删
	if cur.Mode().Type() != want.Mode().Type() || !os.SameFile(want, cur) {
		return fmt.Errorf("材料名字已换成别的文件，保留 %s：%w", name, ErrConflict)
	}
	if err := dir.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
