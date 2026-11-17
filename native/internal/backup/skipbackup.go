package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 数据目录里放着的别的备份不带：它们是备份，不是她的数据（规格第七节“不许把备份放回数据目录里”）。
// 主会话换 exe 时留的手动备份（backup/swap-*、go/backup-swap-*）一份就几百 MB，带进每天的备份里只会白占盘，而且每份还会被当成她的库去取快照。
// 认法有两种：按名字（backup/、backups/、go/backup-swap-*），和按里面有没有备份清单（本程序的 program、旧 Bun 备份的 kind，不管文件夹叫什么）。

const (
	bunBackupKind = "sumika-backup" // 旧 Bun 备份清单里 "kind" 的值
	sniffMax      = 1 << 20         // 认清单只读开头这么多：program / kind 都写在最前面
)

// backupNamed：这个路径是不是按名字认定的“数据目录里的备份”（它本身或它里面的东西）
func backupNamed(rel string) bool {
	if underDir(rel, "backup") || underDir(rel, "backups") {
		return true
	}
	parts := strings.Split(lower(rel), "/")
	return len(parts) >= 2 && parts[0] == "go" && strings.HasPrefix(parts[1], "backup-swap-")
}

// isBackupFolder：abs 这个目录里有没有备份清单（本程序的或旧 Bun 的）。别的 manifest.json（插件的之类）不算
func isBackupFolder(abs string) bool {
	file := filepath.Join(abs, ManifestFile)
	fi, err := os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return false
	}
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	return sniffBackupManifest(io.LimitReader(f, sniffMax))
}

// sniffBackupManifest 顺着顶层的键看，遇到 program 或 kind 就判断；最多看 64 个键，读不懂、不是对象一律当不是
func sniffBackupManifest(r io.Reader) bool {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	for i := 0; i < 64 && dec.More(); i++ {
		kt, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := kt.(string)
		if !ok {
			return false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return false
		}
		var s string
		switch key {
		case "program":
			if json.Unmarshal(raw, &s) == nil && s == Program {
				return true
			}
		case "kind":
			if json.Unmarshal(raw, &s) == nil && s == bunBackupKind {
				return true
			}
		}
	}
	return false
}

// skippedBackup 是走数据目录时认出来、整个没带的一份备份
type skippedBackup struct {
	rel   string
	bytes int64
}

// dirBytes 只数普通文件的大小，不跟链接；读不了的跳过。给清单 notes 里“跳过了多大”用，数不准也不影响备份
func dirBytes(ctx context.Context, dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// skippedBackupNote 把没带的备份写成清单 notes 里的一句：点名前几处和各自多大，再给总数
func skippedBackupNote(list []skippedBackup) string {
	var total int64
	var parts []string
	for i, sb := range list {
		total += sb.bytes
		if i < 5 {
			parts = append(parts, fmt.Sprintf("%s（%s）", sb.rel, gbText(sb.bytes)))
		}
	}
	s := "数据目录里放着的备份没带（它们是备份，不是她的数据）：" + strings.Join(parts, "、")
	if len(list) > 5 {
		s += fmt.Sprintf("，等共 %d 处", len(list))
	}
	return s + "；合计 " + gbText(total)
}

func gbText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	}
}
