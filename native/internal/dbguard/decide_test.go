package dbguard

import (
	"reflect"
	"testing"
)

func readyTrack() Track {
	return mapTrack("chat", 2, map[int64]string{
		1: "old",
		2: "old",
		3: markerCompatible + "\nCREATE TABLE c (id INTEGER);\n",
	})
}

func reqTrack() Track {
	return mapTrack("qq", 1, map[int64]string{
		1: "old",
		2: markerBreaking + "\nALTER TABLE q ADD COLUMN x INTEGER;\n",
	})
}

func stateOf(file string, hasSchema bool, tracks ...TrackState) State {
	st := State{File: file, Exists: true, HasSchema: hasSchema, Tracks: tracks}
	for _, ts := range tracks {
		if ts.HasGooseTable {
			table := ts.Table
			if table == "" {
				table = DefaultTable
			}
			st.UserTables = append(st.UserTables, table)
		}
	}
	return st
}

func TestDecideCreate(t *testing.T) {
	tracks := []Track{readyTrack(), {Name: "memory-index", Version: 1}}
	d, err := Decide(State{File: "new.db"}, tracks)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionCreate || !d.NeedSchema {
		t.Fatalf("决策 = %+v，要 create 且补写版本表", d)
	}
}

func TestDecideTooNewWinsOverNormalTrack(t *testing.T) {
	chat := readyTrack() // latest 3, min_reader 2
	qq := reqTrack()     // latest 2, min_reader 2
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 3, Schema: &SchemaRow{Track: "chat", Version: 3, MinReader: 2}},
		TrackState{Name: "qq", HasGooseTable: true, Applied: 2, Schema: &SchemaRow{Track: "qq", Version: 2, MinReader: 4}},
	)
	d, err := Decide(st, []Track{chat, qq})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionTooNew {
		t.Fatalf("决策 = %v，要 too_new", d.Action)
	}
	want := []TooNewTrack{{Track: "qq", MinReader: 4, Latest: 2, Applied: 2}}
	if !reflect.DeepEqual(d.TooNew, want) {
		t.Fatalf("TooNew = %+v，要 %+v", d.TooNew, want)
	}
}

func TestDecideReadNewer(t *testing.T) {
	chat := readyTrack() // latest 3, min_reader 2
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 9, Schema: &SchemaRow{Track: "chat", Version: 9, MinReader: 2}},
	)
	d, err := Decide(st, []Track{chat})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionReadNewer {
		t.Fatalf("决策 = %v，要 read_newer", d.Action)
	}
	want := []NewerTrack{{Track: "chat", Version: 9, Latest: 3}}
	if !reflect.DeepEqual(d.Newer, want) {
		t.Fatalf("Newer = %+v，要 %+v", d.Newer, want)
	}
	if d.NeedSchema {
		t.Fatal("较新能读不改版本表")
	}
}

func TestDecideOpenWithoutSchema(t *testing.T) {
	chat := readyTrack()
	st := stateOf("x.db", false, TrackState{Name: "chat", HasGooseTable: true, Applied: 3})
	d, err := Decide(st, []Track{chat})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionOpen || !d.NeedSchema {
		t.Fatalf("决策 = %+v，要 open 且补写版本表", d)
	}
}

func TestDecideOpenMissingRow(t *testing.T) {
	chat := readyTrack()
	qq := reqTrack()
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 3, Schema: &SchemaRow{Track: "chat", Version: 3, MinReader: 2}},
		TrackState{Name: "qq", HasGooseTable: true, Applied: 2},
	)
	d, err := Decide(st, []Track{chat, qq})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionOpen || !d.NeedSchema {
		t.Fatalf("决策 = %+v，缺一行要补写", d)
	}
}

func TestDecideOpenSchemaBehindGoose(t *testing.T) {
	chat := readyTrack()
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 3, Schema: &SchemaRow{Track: "chat", Version: 2, MinReader: 2}},
	)
	d, err := Decide(st, []Track{chat})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionOpen || !d.NeedSchema {
		t.Fatalf("决策 = %+v，版本表落后要补写", d)
	}
}

func TestDecideOpenCurrent(t *testing.T) {
	chat := readyTrack()
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 3, Schema: &SchemaRow{Track: "chat", Version: 3, MinReader: 2}},
	)
	d, err := Decide(st, []Track{chat})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionOpen || d.NeedSchema {
		t.Fatalf("决策 = %+v，要 open 且不用补写", d)
	}
}

func TestDecideMigrate(t *testing.T) {
	chat := readyTrack() // 3
	qq := reqTrack()     // 2
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 1, Schema: &SchemaRow{Track: "chat", Version: 1, MinReader: 1}},
		TrackState{Name: "qq", HasGooseTable: true, Applied: 2, Schema: &SchemaRow{Track: "qq", Version: 2, MinReader: 2}},
	)
	d, err := Decide(st, []Track{chat, qq})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionMigrate {
		t.Fatalf("决策 = %v，要 migrate", d.Action)
	}
	want := []PendingTrack{{Track: "chat", From: 1, To: 3}}
	if !reflect.DeepEqual(d.Pending, want) {
		t.Fatalf("Pending = %+v，要 %+v", d.Pending, want)
	}
}

// 没有版本表的老库：goose 的号超过程序 latest 时不再假装能读（规格第 3 条 3a 扩大）；
// 落后就按 0 版推断，迁并补写。
func TestDecideLegacyWithoutSchema(t *testing.T) {
	chat := readyTrack() // latest 3, min_reader 2
	t.Run("更高就是太新", func(t *testing.T) {
		st := stateOf("x.db", false, TrackState{Name: "chat", HasGooseTable: true, Applied: 9})
		d, err := Decide(st, []Track{chat})
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != ActionTooNew {
			t.Fatalf("决策 = %v，要 too_new", d.Action)
		}
		want := []TooNewTrack{{Track: "chat", MinReader: 2, Latest: 3, Applied: 9, GooseAhead: true}}
		if !reflect.DeepEqual(d.TooNew, want) {
			t.Fatalf("TooNew = %+v，要 %+v", d.TooNew, want)
		}
	})
	t.Run("版本表缺这一行也按太新", func(t *testing.T) {
		st := stateOf("x.db", true, TrackState{Name: "chat", HasGooseTable: true, Applied: 9})
		d, err := Decide(st, []Track{chat})
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != ActionTooNew {
			t.Fatalf("决策 = %v，要 too_new", d.Action)
		}
	})
	t.Run("落后就迁并补写", func(t *testing.T) {
		st := stateOf("x.db", false, TrackState{Name: "chat", HasGooseTable: true, Applied: 1})
		d, err := Decide(st, []Track{chat})
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != ActionMigrate {
			t.Fatalf("决策 = %v，要 migrate", d.Action)
		}
	})
}

func TestDecideFixedTrack(t *testing.T) {
	index := Track{Name: "memory-index", Version: Floors["memory-index"]}
	st := stateOf("x.db", true, TrackState{Name: "memory-index", Applied: Floors["memory-index"], Schema: &SchemaRow{Track: "memory-index", Version: Floors["memory-index"], MinReader: Floors["memory-index"]}})
	st.UserTables = []string{"memory_vectors"}
	d, err := Decide(st, []Track{index})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionOpen || d.NeedSchema {
		t.Fatalf("决策 = %+v，要 open 且不用补写", d)
	}

	st = stateOf("x.db", true, TrackState{Name: "memory-index", Applied: Floors["memory-index"], Schema: &SchemaRow{Track: "memory-index", Version: Floors["memory-index"], MinReader: Floors["memory-index"] + 1}})
	st.UserTables = []string{"memory_vectors"}
	d, err = Decide(st, []Track{index})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionTooNew {
		t.Fatalf("决策 = %v，固定轨的 min_reader 更高也要 too_new", d.Action)
	}
}

// 规格第 3 条 3a：goose 比版本表新、也比程序新（较新的程序迁完、写版本表之前崩了），
// 版本表里的 min_reader 不可信，按太新处理。
func TestDecideSchemaBehindAndGooseAhead(t *testing.T) {
	chat := readyTrack() // latest 3
	cases := []struct {
		name          string
		applied       int64
		schemaVersion int64
		want          Action
	}{
		{"都新", 9, 2, ActionTooNew},
		{"goose 新、版本表也不落后", 9, 9, ActionReadNewer},
		{"版本表落后、goose 不比程序新", 3, 2, ActionOpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := stateOf("x.db", true,
				TrackState{Name: "chat", HasGooseTable: true, Applied: tc.applied,
					Schema: &SchemaRow{Track: "chat", Version: tc.schemaVersion, MinReader: 2}})
			d, err := Decide(st, []Track{chat})
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != tc.want {
				t.Fatalf("决策 = %v，要 %v", d.Action, tc.want)
			}
			if tc.want == ActionTooNew {
				want := []TooNewTrack{{Track: "chat", MinReader: 2, Latest: 3, Applied: 9, GooseAhead: true, SchemaVersion: 2}}
				if !reflect.DeepEqual(d.TooNew, want) {
					t.Fatalf("TooNew = %+v，要 %+v", d.TooNew, want)
				}
			}
		})
	}
}

func TestDecideUnknownSchema(t *testing.T) {
	chat := readyTrack()
	st := stateOf("x.db", false, TrackState{Name: "chat", Applied: 0})
	st.UserTables = []string{"some_other_program_table"}
	d, err := Decide(st, []Track{chat})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionUnknownSchema {
		t.Fatalf("决策 = %v，要 unknown_schema", d.Action)
	}
}

func TestDecideAcceptsLegacyWithoutGoose(t *testing.T) {
	memory := mapTrack("memory", 0, map[int64]string{1: markerBreaking + "\nCREATE TABLE m (id INTEGER);\n"})
	memory.AcceptNoGoose = true
	st := stateOf("x.db", false, TrackState{Name: "memory", Applied: 0})
	st.UserTables = []string{"bun_old_table"}
	d, err := Decide(st, []Track{memory})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionMigrate {
		t.Fatalf("决策 = %v，要按 0 版 migrate", d.Action)
	}
	if len(d.Pending) != 1 || d.Pending[0].From != 0 || d.Pending[0].To != 1 {
		t.Fatalf("Pending = %+v，要从 0 迁到 1", d.Pending)
	}
}

func TestDecideEmptyDatabaseIsFresh(t *testing.T) {
	d, err := Decide(State{File: "empty.db", Exists: true}, []Track{readyTrack()})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionCreate || !d.NeedSchema {
		t.Fatalf("决策 = %+v，一张表都没有要按新建走", d)
	}
}

// 只有 sumika_schema、没有 goose 表：不算“有用户表”，按新建走（规格第 3 条 3b）。
func TestDecideSchemaOnlyIsFresh(t *testing.T) {
	st := State{
		File: "schema-only.db", Exists: true, HasSchema: true,
		SchemaRows: map[string]SchemaRow{"memory-index": {Track: "memory-index", Version: 1, MinReader: 1}},
		Tracks:     []TrackState{{Name: "memory-index", Applied: 1}},
	}
	d, err := Decide(st, []Track{{Name: "memory-index", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionCreate || !d.NeedSchema {
		t.Fatalf("决策 = %+v，只有 sumika_schema 要按新建走", d)
	}
}

// 规格第 3 条第 4 步先于第 6 步：一轨较新、一轨有待跑时整份不迁。
func TestDecideNewerWinsOverPending(t *testing.T) {
	chat := readyTrack() // latest 3
	qq := reqTrack()     // latest 2
	st := stateOf("x.db", true,
		TrackState{Name: "chat", HasGooseTable: true, Applied: 9, Schema: &SchemaRow{Track: "chat", Version: 9, MinReader: 2}},
		TrackState{Name: "qq", HasGooseTable: true, Applied: 1, Schema: &SchemaRow{Track: "qq", Version: 1, MinReader: 1}},
	)
	d, err := Decide(st, []Track{chat, qq})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionReadNewer {
		t.Fatalf("决策 = %v，要 read_newer", d.Action)
	}
	if len(d.Pending) != 0 {
		t.Fatalf("Pending = %+v，较新能读时不该安排迁移", d.Pending)
	}
	want := []NewerTrack{{Track: "chat", Version: 9, Latest: 3}}
	if !reflect.DeepEqual(d.Newer, want) {
		t.Fatalf("Newer = %+v，要 %+v", d.Newer, want)
	}
}

func TestDecideRejectsUnknownTrackState(t *testing.T) {
	st := stateOf("x.db", true, TrackState{Name: "other", HasGooseTable: true})
	if _, err := Decide(st, []Track{readyTrack()}); err == nil {
		t.Fatal("探查结果缺这条轨时要报错")
	}
}
