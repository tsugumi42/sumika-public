package backup

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// Usage 是备份目录的实际占用：按文件身份去重，硬链接的几个名字只算一次。读不了的文件当 0，不让占用提醒因为一个坏文件整个失灵。
// 备份目录里别人放的东西也算：占的是同一块盘。
func Usage(dir string) int64 { return UsageCtx(context.Background(), dir) }

// UsageCtx 同 Usage，每一项看一眼 ctx。只数备份目录里带本程序清单的备份文件夹（含没做完的）和 .usage.json：
// 主人自己放在备份目录里的东西、别的程序的文件不是这里的占用，也不该每次都走一遍
func UsageCtx(ctx context.Context, dir string) int64 {
	entries, _ := Scan(dir)
	seen := map[string]bool{}
	var total int64
	for _, e := range entries {
		total += usageTree(ctx, e.Folder, seen)
	}
	if fi, err := os.Stat(filepath.Join(dir, usageFile)); err == nil && fi.Mode().IsRegular() {
		total += fi.Size()
	}
	return total
}

// usageTree 数一棵目录树的实际占用：按文件身份去重，硬链接的几个名字只算一次；seen 在几棵树之间共用（备份之间共用的媒体只算一次）
func usageTree(ctx context.Context, dir string, seen map[string]bool) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		id, err := fileID(p)
		if err != nil {
			total += fi.Size() // 取不到身份就按各算各的，宁可多报
			return nil
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		total += fi.Size()
		return nil
	})
	return total
}
