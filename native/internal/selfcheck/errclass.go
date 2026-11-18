package selfcheck

import "strings"

// 入库失败的错误类别。只记类别、不记错误原文：原文可能带会话号或内容（规格 S2）
const (
	StoreFull     = "SQLITE_FULL"     // 磁盘满、库满
	StoreBusy     = "SQLITE_BUSY"     // 被锁住、等不到
	StoreReadonly = "SQLITE_READONLY" // 库只读
	StoreOther    = "other"
)

// StoreErrorClass 把入库失败的错误归类
func StoreErrorClass(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "sqlite_full"), strings.Contains(s, "database or disk is full"), strings.Contains(s, "no space left"), strings.Contains(s, "not enough space"), strings.Contains(s, "disk full"):
		return StoreFull
	case strings.Contains(s, "sqlite_busy"), strings.Contains(s, "sqlite_locked"), strings.Contains(s, "database is locked"), strings.Contains(s, "database table is locked"):
		return StoreBusy
	case strings.Contains(s, "sqlite_readonly"), strings.Contains(s, "readonly database"), strings.Contains(s, "read-only"):
		return StoreReadonly
	}
	return StoreOther
}
