package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// usageFile 是备份目录里放“实际总占用”的小文件（不在任何备份文件夹里，轮换和 Scan 都不碰它）：
// 每次备份结束时算好写进去，backup list 只读它。
const usageFile = ".usage.json"

type usageRecord struct {
	Program string    `json:"program"`
	Source  string    `json:"source"` // 是哪个数据目录的备份结束时算的；总览只认自己的
	At      time.Time `json:"at"`
	Bytes   int64     `json:"bytes"`
}

func writeUsage(dir string, bytes int64, at time.Time, source string) error {
	b, err := json.Marshal(usageRecord{Program: Program, Source: source, At: at, Bytes: bytes})
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, usageFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, usageFile))
}

// readUsage 读上次算好的占用；没有、读不懂、不是本程序写的都返回 false
func readUsage(dir, source string) (usageRecord, bool) {
	b, err := os.ReadFile(filepath.Join(dir, usageFile))
	if err != nil || len(b) > 4096 {
		return usageRecord{}, false
	}
	var r usageRecord
	if json.Unmarshal(b, &r) != nil || r.Program != Program || r.Source != source || r.Bytes < 0 {
		return usageRecord{}, false
	}
	return r, true
}
