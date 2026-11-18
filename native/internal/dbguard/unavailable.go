package dbguard

import "strings"

// 块不可用的固定原因词（规格第 8 条；自检 store.unavailable 的例子里也用这几个）。
const (
	ReasonTooNew         = "too_new"
	ReasonUnknownSchema  = "unknown_schema"
	ReasonProbeFailed    = "probe_failed"
	ReasonCreateFailed   = "create_failed"
	ReasonMigrateFailed  = "migrate_failed"
	ReasonRestoreFailed  = "restore_failed"
	ReasonNoSpace        = "no_space"
	ReasonSnapshotFailed = "snapshot_failed"
)

// Unavailable 是一个库文件打不开、对应块要停下的错误。
// Reason 用上面的固定词；Detail 写给人看的说明；Snapshot 是这次迁移前快照的路径（没有就是空）。
// Err 是这一层包着的底层原因（探查、goose、换回等），供 errors.Is / errors.As 找根因。
// Error() 只拼路径、轨名、版本和 Detail，不带任何库里的数据内容，也不打印 Err。
type Unavailable struct {
	File     string
	Track    string
	Reason   string
	Detail   string
	Snapshot string
	Err      error
}

// Unwrap 让 errors.Is / errors.As 能穿过固定原因词找到底层错误。
func (e *Unavailable) Unwrap() error { return e.Err }

func (e *Unavailable) Error() string {
	var b strings.Builder
	b.WriteString("库 ")
	b.WriteString(e.File)
	b.WriteString(" 不可用（")
	b.WriteString(e.Reason)
	b.WriteString("）")
	if e.Track != "" {
		b.WriteString("：迁移轨 ")
		b.WriteString(e.Track)
	}
	if e.Detail != "" {
		b.WriteString("：")
		b.WriteString(e.Detail)
	}
	if e.Snapshot != "" {
		b.WriteString("；快照在 ")
		b.WriteString(e.Snapshot)
	}
	return b.String()
}
