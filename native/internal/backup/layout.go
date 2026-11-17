package backup

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// 数据目录里哪些路径归哪一类、哪些不带（规格-数据目录与备份 10.1）。全程序只有这一份表：
// “默认带，列出来的不带”——没认领的新东西默认算她本人，宁可多带也不静默漏掉。路径都是相对数据目录、斜杠、小写比较。
// 路径本身从 paths 来（两处媒体目录的名字在 mediaRoots），这里不拼任何绝对路径。

// 整目录不带
var skipDirs = []string{
	"cache", "logs", "run", "selfcheck",
	"dev/screens",
	"go/media-thumbs",
	"go/work/checkpoints",
	"go/upgrade-snapshots",
	"import", "go/import",
	".restore",
	// 数据目录里放着的别的备份（backup/、backups/、go/backup-swap-*）另判，见 backupNamed
}

// 媒体目录：按 backup.dailyMedia 挑（旧版媒体在 shared/media）
var mediaRoots = []string{"go/media", "shared/media"}

// 素材目录（前缀）：characters/<角色>/drafts 和 characters/_shared* 另判。
// dev/ 下只带回放样本和种子（规格 5.3.0）：数据目录里的 dev/ 还放测试完整输出、handoff、试验稿，又大又不是她本人的东西
var materialDirs = []string{"dev/replay", "dev/seeds", "reports"}

// 不带的文件：库的旁路文件、锁文件、临时文件
var skipSuffixes = []string{"-wal", "-shm", "-journal", ".lock", ".tmp", ".partial"}

// 派生索引：坏了删掉重建，不带
var skipFiles = []string{"memory-v2.db", "thumbs.db", "desktop.ini"}

func lower(s string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(s)
	}
	return s
}

func underDir(rel, dir string) bool {
	rel, dir = lower(rel), lower(dir)
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// kind 是备份时怎么处理一个文件
type kind int

const (
	kindSkip  kind = iota // 不带
	kindFile              // 原样复制
	kindDB                // 库：取快照
	kindMedia             // 媒体：按档挑
)

// classify 给一个文件（相对数据目录，斜杠）定怎么处理、归哪一类
func classify(rel string) (kind, Class) {
	if devSkipped(rel) {
		return kindSkip, ""
	}
	base := path.Base(rel)
	if strings.HasPrefix(lower(base), ".tmp") {
		return kindSkip, ""
	}
	for _, s := range skipSuffixes {
		if strings.HasSuffix(lower(base), s) {
			return kindSkip, ""
		}
	}
	for _, f := range skipFiles {
		if lower(base) == f {
			return kindSkip, ""
		}
	}
	for _, d := range skipDirs {
		if underDir(rel, d) {
			return kindSkip, ""
		}
	}
	if backupNamed(rel) {
		return kindSkip, ""
	}
	for _, d := range mediaRoots {
		if underDir(rel, d) {
			return kindMedia, ClassMedia
		}
	}
	if isConfigTemp(rel) { // 程序自己写配置时留下的临时文件
		return kindSkip, ""
	}
	class := ClassSelf
	for _, d := range materialDirs {
		if underDir(rel, d) {
			class = ClassMaterial
		}
	}
	// characters/<角色>/drafts/…、characters/_shared*/…
	parts := strings.Split(lower(rel), "/")
	if len(parts) >= 3 && parts[0] == "characters" && parts[2] == "drafts" {
		class = ClassMaterial
	}
	if len(parts) >= 2 && parts[0] == "characters" && strings.HasPrefix(parts[1], "_shared") {
		class = ClassMaterial
	}
	if strings.HasSuffix(lower(base), ".db") {
		return kindDB, class
	}
	return kindFile, class
}

// devSkipped：dev/ 下除了 dev/replay、dev/seeds 以外的都不带
func devSkipped(rel string) bool {
	if !underDir(rel, "dev") || lower(rel) == "dev" {
		return false
	}
	return !underDir(rel, "dev/replay") && !underDir(rel, "dev/seeds")
}

// skipDir 整个目录不用进去
func skipDir(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	if devSkipped(rel) {
		return true
	}
	if strings.HasPrefix(lower(path.Base(rel)), ".tmp") {
		return true
	}
	for _, d := range skipDirs {
		if underDir(rel, d) {
			return true
		}
	}
	return backupNamed(rel)
}

// item 是这一轮要带的一个文件
type item struct {
	rel   string
	abs   string
	size  int64
	kind  kind
	class Class
}

// skipRecord 是走数据目录时没法带的东西：链接、读不了的目录和文件。属于要备份的内容（她本人、素材、库）的是问题，这份不能记 ok；
// 媒体只记一笔，清单里记个数（backup list 和自检看得到）
type skipRecord struct {
	rel   string
	why   string
	media bool
}

// sqliteMagic 是 SQLite 库文件的头 16 个字节
var sqliteMagic = []byte("SQLite format 3\x00")

func isSQLiteFile(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return true // 读不开的当库处理，由快照那一步报具体原因
	}
	defer f.Close()
	buf := make([]byte, len(sqliteMagic))
	n, _ := io.ReadFull(f, buf)
	return n == len(buf) && bytes.Equal(buf, sqliteMagic)
}

func underMediaRoot(rel string) bool {
	for _, d := range mediaRoots {
		if underDir(rel, d) {
			return true
		}
	}
	return false
}

// dirBytesNoLink：本身是链接的目录不进去数（和备份不跟链接一致），算 0
func dirBytesNoLink(ctx context.Context, abs string) int64 {
	if linkOnDisk(abs) {
		return 0
	}
	return dirBytes(ctx, abs)
}

// isConfigTemp：config/ 下程序自己写配置时留下的临时文件（.sumika-config-*）：不带，也不用提醒。config/ 下别的文件一律逐字节原样带
func isConfigTemp(rel string) bool {
	return underDir(rel, "config") && strings.HasPrefix(lower(path.Base(rel)), ".sumika-config-")
}

// collect 走一遍数据目录（root 要先解到真实路径），列出要带的文件。不跟随联接和符号链接：遇到的记进 skips（属于要备份的内容的是问题，媒体只记一笔）。
// 媒体文件也列在这里（kindMedia），由调用方按档挑。notes 是顺手记下的小事（假 .db 当普通文件带走之类）；config/ 下的文件一律逐字节原样带，只有 .sumika-config-* 临时文件不带。
func collect(ctx context.Context, root string) (items []item, skips []skipRecord, notes []string, err error) {
	// skipped 记一个没法带的路径：先看它如果能带会是什么——本来就不带的（缓存、日志、dev/screens……）不算
	skipped := func(rel string, isDir bool, why string) {
		probe := rel
		if isDir {
			probe = rel + "/_"
		}
		k, _ := classify(probe)
		if k == kindSkip {
			return
		}
		skips = append(skips, skipRecord{rel: rel, why: why, media: k == kindMedia})
	}
	var backups []skippedBackup
	err = filepath.WalkDir(root, func(abs string, d fs.DirEntry, werr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if werr != nil {
			if abs == root {
				return werr
			}
			rel := relOf(root, abs)
			isDir := d != nil && d.IsDir()
			skipped(rel, isDir, "读不了："+werr.Error())
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if abs == root {
			return nil
		}
		rel := relOf(root, abs)
		if d.IsDir() {
			if skipDir(rel) {
				if backupNamed(rel) { // 走到这里的一定是它本身（上一层已经整个跳过了）：记一笔跳过了什么、多大
					backups = append(backups, skippedBackup{rel: rel, bytes: dirBytesNoLink(ctx, abs)})
				}
				return fs.SkipDir
			}
			if !underMediaRoot(rel) && isBackupFolder(abs) { // 不管叫什么名字，里面有备份清单的就是备份
				backups = append(backups, skippedBackup{rel: rel, bytes: dirBytesNoLink(ctx, abs)})
				return fs.SkipDir
			}
			if linkOnDisk(abs) {
				skipped(rel, true, "是链接目录，不跟进去")
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // 符号链接、联接、设备……一律不跟
			skipped(rel, false, "不是普通文件（链接之类），不跟")
			return nil
		}
		k, class := classify(rel)
		if k == kindSkip {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			skipped(rel, false, "读不了："+err.Error())
			return nil
		}
		if k == kindDB && (fi.Size() == 0 || !isSQLiteFile(abs)) {
			k = kindFile
			notes = append(notes, rel+"：名字是 .db 但文件头不是 SQLite 库，当普通文件带走")
		}
		items = append(items, item{rel: rel, abs: abs, size: fi.Size(), kind: k, class: class})
		return nil
	})
	if len(backups) > 0 {
		notes = append(notes, skippedBackupNote(backups))
	}
	return items, skips, notes, err
}

func relOf(root, abs string) string {
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

// dataFileExists 只给测试和调用方用：数据目录里这个相对路径是不是一个存在的普通文件
func dataFileExists(root, rel string) bool {
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}
