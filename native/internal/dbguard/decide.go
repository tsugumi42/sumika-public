package dbguard

import "fmt"

// Action 是开一个库文件时护栏判定的结果。
type Action int

const (
	ActionNone          Action = iota // 还没判定，或探查 / 判定失败就提前返回
	ActionCreate                      // 文件不存在或一张表都没有：照现在新建、跑完所有轨、写版本表，不拍快照
	ActionTooNew                      // 有轨的 min_reader 大于程序的 latest：拒绝打开，块不可用
	ActionReadNewer                   // 库比程序新但还在最低可读范围内：正常打开，不迁移、不改版本表
	ActionOpen                        // 没有待跑的迁移：正常打开，版本表缺行或落后时补写
	ActionMigrate                     // 有待跑的迁移：先拍快照再迁
	ActionUnknownSchema               // 有用户表、却一张 goose 表都没有：认不出的库，拒绝当 0 版迁移
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionCreate:
		return "create"
	case ActionTooNew:
		return "too_new"
	case ActionReadNewer:
		return "read_newer"
	case ActionOpen:
		return "open"
	case ActionMigrate:
		return "migrate"
	case ActionUnknownSchema:
		return "unknown_schema"
	default:
		return fmt.Sprintf("Action(%d)", int(a))
	}
}

// TooNewTrack 是一条“库比程序要求的新”的轨：库的 min_reader 大于程序的 latest。
// GooseAhead 为真表示规格第 3 条 3a 那一种：goose 的号比版本表记的 version 和程序的 latest 都新，
// 版本表里的 min_reader 不可信（较新的程序迁完、写版本表之前崩了）。
type TooNewTrack struct {
	Track         string
	MinReader     int64
	Latest        int64
	Applied       int64
	GooseAhead    bool
	SchemaVersion int64
}

// NewerTrack 是一条“库比程序新、但还能读”的轨。
type NewerTrack struct {
	Track   string
	Version int64
	Latest  int64
}

// PendingTrack 是一条有待跑迁移的轨：From 是库现在的版本，To 是程序的最新号。
type PendingTrack struct {
	Track string
	From  int64
	To    int64
}

// Decision 是按状态和程序迁移轨算出来的开库路线。
// NeedSchema 只在 ActionOpen 时有意义：不用迁移，但版本表缺表、缺行或落后，要补写。
type Decision struct {
	Action     Action
	TooNew     []TooNewTrack
	Newer      []NewerTrack
	Pending    []PendingTrack
	NeedSchema bool
}

// Decide 按规格第 3 条 1～6 和第 6 条判定怎么开这个库。它不碰数据库、不写文件；
// 读迁移文件系统算 latest / min_reader，轨定义本身有问题时返回 error。
//
// 优先级照规格的顺序：文件不存在或一张表都没有 → 认不出的库 → 太新 → 较新能读 → 没有待跑 → 有待跑。
// “认不出的库”先于太新：没有 goose 表时读不到可靠的迁移号。
// “较新能读”先于“有待跑”：规格要求这种情况不迁移、不改版本表。
func Decide(state State, tracks []Track) (Decision, error) {
	if !state.Exists || len(state.UserTables) == 0 {
		return Decision{Action: ActionCreate, NeedSchema: true}, nil
	}
	expectsGoose := false
	acceptsNoGoose := false
	for _, t := range tracks {
		if t.FS == nil {
			continue
		}
		expectsGoose = true
		if t.AcceptNoGoose {
			acceptsNoGoose = true
		}
	}
	anyGoose := false
	for _, ts := range state.Tracks {
		if ts.HasGooseTable {
			anyGoose = true
			break
		}
	}
	if expectsGoose && !anyGoose && !acceptsNoGoose {
		return Decision{Action: ActionUnknownSchema}, nil
	}
	byName := make(map[string]TrackState, len(state.Tracks))
	for _, ts := range state.Tracks {
		byName[ts.Name] = ts
	}
	var tooNew []TooNewTrack
	var newer []NewerTrack
	var pending []PendingTrack
	needSchema := !state.HasSchema
	for _, t := range tracks {
		latest, err := t.Latest()
		if err != nil {
			return Decision{}, err
		}
		minReader, err := t.MinReader()
		if err != nil {
			return Decision{}, err
		}
		ts, ok := byName[t.Name]
		if !ok {
			return Decision{}, fmt.Errorf("探查结果里没有迁移轨 %s", t.Name)
		}
		applied := ts.Applied
		if t.FS == nil && ts.Schema == nil {
			applied = t.Version
		}
		dbMinReader := minReader // 没有版本表 / 缺行：按程序的标记推断（规格第 6 条）
		var schemaVersion int64
		if ts.Schema != nil {
			dbMinReader = ts.Schema.MinReader
			schemaVersion = ts.Schema.Version
		}
		// 3a 扩大（afde768e）：goose 的号比程序新，而且版本表没有这一行（或没有版本表）、
		// 或记的版本更旧，说明较新的程序迁完、写版本表之前崩了；min_reader 无从得知或不可信。
		gooseAhead := applied > latest && (ts.Schema == nil || applied > schemaVersion)
		if dbMinReader > latest {
			tooNew = append(tooNew, TooNewTrack{Track: t.Name, MinReader: dbMinReader, Latest: latest, Applied: applied})
		} else if gooseAhead {
			tooNew = append(tooNew, TooNewTrack{
				Track: t.Name, MinReader: dbMinReader, Latest: latest, Applied: applied,
				GooseAhead: true, SchemaVersion: schemaVersion,
			})
		}
		if applied > latest {
			newer = append(newer, NewerTrack{Track: t.Name, Version: applied, Latest: latest})
		}
		if applied < latest {
			pending = append(pending, PendingTrack{Track: t.Name, From: applied, To: latest})
		}
		if ts.Schema == nil || ts.Schema.Version != applied {
			needSchema = true
		}
	}
	if len(tooNew) > 0 {
		return Decision{Action: ActionTooNew, TooNew: tooNew}, nil
	}
	if len(newer) > 0 {
		// 规格第 4 条：不迁移、不改版本表。
		return Decision{Action: ActionReadNewer, Newer: newer}, nil
	}
	if len(pending) > 0 {
		return Decision{Action: ActionMigrate, Pending: pending}, nil
	}
	return Decision{Action: ActionOpen, NeedSchema: needSchema}, nil
}
