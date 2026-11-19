package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"sumika-native/internal/paths"
)

// ErrDirInsideData：备份目录落在数据目录里面。备份放进数据目录里，整包复制数据目录时旧备份会被一起复制，越滚越大，
// 数据目录坏了备份也一起没了（规格-数据目录与备份 第七节“不许把备份放回数据目录里”）
var ErrDirInsideData = errors.New("备份目录在数据目录里面")

// ErrDirTooBroad：备份目录是盘符根，或者是数据目录的上级。这样的目录并进审批门的受保护目录，整个盘 / 整个项目都变成“每次问”；
// 备份也该有一个专门放它的文件夹，不要和别的东西混在盘根或项目根里
var ErrDirTooBroad = errors.New("备份目录太宽")

// linkOnDisk：这一级是不是链接（读不出来也按是处理，宁可不碰）
func linkOnDisk(path string) bool {
	l, err := paths.IsLink(path)
	return err != nil || l
}

// CheckDir 查一个备份目录能不能用：落在数据目录里面（含就是数据目录）拒绝（ErrDirInsideData），是盘符根或数据目录的上级拒绝（ErrDirTooBroad）。
// 都按解到真实路径（联接、符号链接解开，Windows 不分大小写）以后比。目录还不存在也行
func CheckDir(p paths.Paths, dir string) error {
	_, err := InspectDir(p, dir)
	return err
}

// InspectDir 同 CheckDir，另外把 dir 解到的真实路径也交回来：解得开就一直有值（就算因为在数据目录里、太宽被拒绝），解不开是空串。
// 审批门要它：卷标识写法（\\?\Volume{GUID}\x）这类写法规整不成 D:\x，受保护目录里要有真实路径才比得上
func InspectDir(p paths.Paths, dir string) (dirReal string, err error) {
	dataReal, err := realExisting(p.Root)
	if err != nil {
		return "", fmt.Errorf("数据目录 %s 解不开：%w", p.Root, err)
	}
	dirReal, err = realExisting(dir)
	if err != nil {
		return "", fmt.Errorf("备份目录 %s 解不开：%w", dir, err)
	}
	return dirReal, checkReal(dir, dirReal, dataReal)
}

// checkReal 是 CheckDir 里只比字符串的那一半（两个都已经是真实路径），单独拿出来是为了不碰文件系统就能测各种盘符、上下级的组合
func checkReal(dir, dirReal, dataReal string) error {
	if within(dataReal, dirReal) {
		return fmt.Errorf("%w：备份目录 %s 解到 %s，在数据目录 %s 里；改 backup.dir（或清空它用数据目录旁边的 Sumika-备份）", ErrDirInsideData, dir, dirReal, dataReal)
	}
	if filepath.Dir(dirReal) == dirReal || within(dirReal, dataReal) {
		return fmt.Errorf("%w：备份目录 %s 解到 %s，是盘符根或数据目录 %s 的上级；改 backup.dir 成一个专门放备份的文件夹（或清空它用数据目录旁边的 Sumika-备份）", ErrDirTooBroad, dir, dirReal, dataReal)
	}
	return nil
}

// ResolveDir 是全程序唯一算备份目录的地方：
//  1. tempData（SUMIKA_DATA_DIR 有值：测试、冒烟检查指向了临时数据目录）：一律数据目录旁边的 Sumika-备份，不管配置写的是哪；
//  2. 否则配置的 backup.dir；空就是数据目录旁边的 Sumika-备份；相对路径按数据目录的上一级解；
//  3. 解到真实路径（联接、符号链接都解开，Windows 不分大小写）后落在数据目录里面就拒绝（ErrDirInsideData），是盘符根或数据目录的上级也拒绝（ErrDirTooBroad），不悄悄换到别处。
//     配置里写成 `\\?\D:\x` 这类设备路径前缀的，先还原成 `D:\x`（审批门拿它和受保护目录比，带前缀比不上）。
//
// 备份目录本身可以是联接（和数据目录一样）。目录还不存在也行（Run 会建）。
func ResolveDir(p paths.Paths, cfgDir string, tempData bool) (string, error) {
	cfgDir = paths.StripDevicePrefix(strings.TrimSpace(cfgDir)) // 写成 \\?\D:\x 这类设备路径前缀的先还原成 D:\x
	var dir string
	switch {
	case tempData || strings.TrimSpace(cfgDir) == "":
		dir = p.BackupRoot()
	case filepath.IsAbs(cfgDir):
		dir = filepath.Clean(cfgDir)
	default:
		dir = filepath.Join(filepath.Dir(p.Root), cfgDir)
	}
	if err := CheckDir(p, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// realExisting 把 path 里已经存在的最长一段解到真实位置（联接、符号链接都解开），再接上还没建的部分
func realExisting(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := paths.RealPath(cur)
			if err != nil {
				return "", err
			}
			parts := append([]string{real}, tail...)
			return filepath.Join(parts...), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}

// within：p 是 root 本身或在 root 里面（Windows 上不分大小写）
func within(root, p string) bool {
	if runtime.GOOS == "windows" {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
