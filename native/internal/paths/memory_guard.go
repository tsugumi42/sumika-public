package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrMemoryPath 表示库不在角色记忆目录中，或路径含链接。
var ErrMemoryPath = errors.New("不是角色记忆目录下的普通库文件")

// CheckMemoryPath 查 db 是 <数据目录>/go/characters/<角色>/<文件名> 下的普通文件。
// 比的是真实路径，不比字符串：
//   - 先按字面算相对于数据目录的位置（Abs 已经消掉了 ..），位置不对（Bun 的 characters/<角色>/memory.db、数据目录之外）直接拒绝；
//   - 数据目录以下的每一级（go、characters、角色目录、文件本身）都不许是符号链接、联接（junction）或别的重解析点；
//     数据目录本身允许是联接（启动时的 paths.Open 一样允许），先解到真实路径再比；
//   - 再把整条路径解到真实位置，要求仍在真实的 <数据目录>/go/characters/ 下（Windows 上不分大小写，改大小写骗不过去）；
//   - Windows 上文件有不止一个硬链接也拒绝，避免别处通过同一文件写入。
//
// 文件要已经存在；写入侧不新建库。
func CheckMemoryPath(data, db string) error {
	return checkMemoryPath(data, db, false)
}

// CheckMemoryCreatePath 是建库前的位置检查：db 和末几级目录可以还不存在。
// 已经存在的每一级同样不许是符号链接、联接或别的重解析点；已存在的部分解到真实路径、接上还不存在的那几级后，
// 必须仍在真实的 <数据目录>/go/characters/<角色>/ 下。文件已经存在时按 CheckMemoryPath 查（含硬链接）。
func CheckMemoryCreatePath(data, db string) error {
	return checkMemoryPath(data, db, true)
}

func checkMemoryPath(data, db string, allowMissing bool) error {
	dataAbs, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	dbAbs, err := filepath.Abs(db)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(dataAbs, dbAbs)
	if err != nil || filepath.IsAbs(rel) {
		return fmt.Errorf("%w：库文件不在数据目录里", ErrMemoryPath)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || !sameName(parts[0], "go") || !sameName(parts[1], "characters") || parts[2] == "" || parts[3] == "" {
		return fmt.Errorf("%w：库文件不在 <数据目录>/go/characters/<角色>/ 下", ErrMemoryPath)
	}
	// 已经存在到哪一级：allowMissing 时从第一级看不见的目录起，往后都不存在（有上级才能有下级）。
	// dataAbs 自己必须存在（启动时 paths.Open 已经建好），从它算起。
	cur := dataAbs
	lastExisting := dataAbs
	missing := false
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		if missing {
			continue
		}
		fi, statErr := os.Lstat(cur)
		if statErr != nil {
			if allowMissing && errors.Is(statErr, os.ErrNotExist) {
				missing = true
				continue
			}
			return statErr
		}
		link, err := isLink(cur)
		if err != nil {
			return err
		}
		if link {
			return fmt.Errorf("%w：路径里有符号链接或联接", ErrMemoryPath)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return fmt.Errorf("%w：路径中间不是目录", ErrMemoryPath)
		}
		lastExisting = cur
	}
	var realTarget string
	if missing {
		realBase, err := realPath(lastExisting)
		if err != nil {
			return err
		}
		rest, err := filepath.Rel(lastExisting, dbAbs)
		if err != nil {
			return err
		}
		realTarget = filepath.Join(realBase, rest)
	} else {
		if fi, err := os.Stat(dbAbs); err != nil {
			return err
		} else if !fi.Mode().IsRegular() {
			return fmt.Errorf("%w：库文件不是普通文件", ErrMemoryPath)
		}
		realTarget, err = realPath(dbAbs)
		if err != nil {
			return err
		}
	}
	realData, err := realPath(dataAbs)
	if err != nil {
		return err
	}
	inner, err := filepath.Rel(filepath.Join(realData, "go", "characters"), realTarget)
	if err != nil || filepath.IsAbs(inner) || inner == ".." || strings.HasPrefix(inner, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w：真实位置不在 go/characters/ 下", ErrMemoryPath)
	}
	if n := len(strings.Split(inner, string(filepath.Separator))); n != 2 {
		return fmt.Errorf("%w：真实位置不在 go/characters/<角色>/ 下", ErrMemoryPath)
	}
	if !missing {
		if n, err := hardLinks(dbAbs); err != nil {
			return err
		} else if n > 1 {
			return fmt.Errorf("%w：库文件有 %d 个硬链接", ErrMemoryPath, n)
		}
	}
	return nil
}
