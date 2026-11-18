package store

// 打开时记下的每个库的样子：版本、最低可读、程序的最高版本、状态、快照。
// sumika-ctl db status 只读这份记录，不在命令行里重新开库（规格第 8 条）。

import (
	"database/sql"
	"sort"

	"sumika-native/internal/dbguard"
)

// DBTrackStatus 是一个库文件里一条迁移轨的状态。
// Version 是库现在的版本；不可用时是探查看到的那一版。MinReader 是库声明的（版本表），
// 读不到时退回程序按迁移文件算的值。Latest 是程序认识这条轨的最高号。
type DBTrackStatus struct {
	Track     string
	Version   int64
	MinReader int64
	Latest    int64
}

// DBFileStatus 是一个库文件（trace 或某个平台）打开时记下的状态。
// Status 是 ok / unavailable；不可用时 Reason 是固定原因词，Detail 写给人看，Snapshot 是失败快照（有就给）。
type DBFileStatus struct {
	Name     string // trace 或平台名
	Path     string
	Status   string
	Reason   string
	Detail   string
	Snapshot string
	Tracks   []DBTrackStatus
}

// DBStatus 是这组库打开时记下的全部库，trace 在前，平台按名字排好。只看历史（只读归档）也记下每个库的状态。
func (s *Set) DBStatus() []DBFileStatus {
	out := make([]DBFileStatus, len(s.dbs))
	copy(out, s.dbs)
	return out
}

// Unavailable 是登记了、库却打不开的平台（平台名 → 原因）。返回一份拷贝；没有时是空表而不是 nil。
// 调用方用它停掉整个平台：不收、不发、不补录、通道不起（规格第 8 条）。
func (s *Set) Unavailable() map[string]*dbguard.Unavailable {
	out := make(map[string]*dbguard.Unavailable, len(s.unavailable))
	for name, u := range s.unavailable {
		out[name] = u
	}
	return out
}

// noteDB 在打开（或打不开）一个库文件之后记下它的状态。
// 版本和最低可读以库里的版本表为准；打不开时没有连接，就用探查结果和程序按迁移文件算的值。
func (s *Set) noteDB(name, file string, tracks []dbguard.Track, rep dbguard.Report, db *sql.DB, u *dbguard.Unavailable) {
	row := DBFileStatus{Name: name, Path: file, Status: "ok"}
	if u != nil {
		row.Status, row.Reason, row.Detail, row.Snapshot = "unavailable", u.Reason, u.Detail, u.Snapshot
	}
	fromReport := make(map[string]int64, len(rep.Tracks))
	for _, t := range rep.Tracks {
		fromReport[t.Track] = t.From
	}
	schema := map[string]struct{ version, minReader int64 }{}
	if db != nil {
		rows, err := db.Query(`SELECT track, version, min_reader FROM ` + dbguard.SchemaTable)
		if err == nil {
			for rows.Next() {
				var track string
				var version, minReader int64
				if rows.Scan(&track, &version, &minReader) == nil {
					schema[track] = struct{ version, minReader int64 }{version, minReader}
				}
			}
			rows.Close()
		}
	}
	for _, t := range tracks {
		latest, _ := t.Latest()
		tr := DBTrackStatus{Track: t.Name, Latest: latest}
		if sr, ok := schema[t.Name]; ok {
			tr.Version, tr.MinReader = sr.version, sr.minReader
		} else {
			tr.Version = fromReport[t.Name]
			tr.MinReader, _ = t.MinReader()
		}
		row.Tracks = append(row.Tracks, tr)
	}
	sort.SliceStable(row.Tracks, func(i, j int) bool { return row.Tracks[i].Track < row.Tracks[j].Track })
	s.dbs = append(s.dbs, row)
}
