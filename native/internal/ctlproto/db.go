package ctlproto

// CmdDB 是库版本和升级状态的命令行写法（规格-库版本与升级护栏 第 8 条）：
//
//	db status   每个库（trace、每个平台）现在第几版、最低要什么版本的程序、本程序认识哪一版、
//	            可用还是不可用（原因）、这次打开时的快照。只读，不重新开库。
const CmdDB = "db"

// DBStatusCmd 是 db 唯一的子命令
const DBStatusCmd = "status"

// DBTrack 是一个库文件里一条迁移轨的状态。Version 是库现在的版本；MinReader 是库声明的最低可读版本
// （版本表里没有时退回按迁移文件算的值）；Latest 是本程序认识这条轨的最高号。
type DBTrack struct {
	Track     string `json:"track"`
	Version   int64  `json:"version"`
	MinReader int64  `json:"minReader"`
	Latest    int64  `json:"latest"`
}

// DBDatabase 是一个库文件（trace 或某个平台）。Status 是 ok / unavailable；
// 不可用时 Reason 是固定原因词（too_new、unknown_schema、probe_failed、create_failed、migrate_failed、
// restore_failed、no_space、snapshot_failed），Detail 写给人看，Snapshot 是失败快照（有就给）。
type DBDatabase struct {
	Name     string    `json:"name"` // trace 或平台名
	Path     string    `json:"path"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Snapshot string    `json:"snapshot,omitempty"`
	Tracks   []DBTrack `json:"tracks"`
}

// DBStatus 是 db status 的 data
type DBStatus struct {
	Databases []DBDatabase `json:"databases"`
}
