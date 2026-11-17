package dbguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLatestAndMinReader(t *testing.T) {
	tr := mapTrack("chat", 2, map[int64]string{
		1: "old without marker\nCREATE TABLE a (id INTEGER);\n",
		2: "old without marker\nCREATE TABLE b (id INTEGER);\n",
		3: markerCompatible + "\nCREATE TABLE c (id INTEGER);\n",
		4: markerBreaking + "\nALTER TABLE c ADD COLUMN x INTEGER;\n",
		5: markerCompatible + "\nCREATE INDEX c_x ON c (x);\n",
	})
	latest, err := tr.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if latest != 5 {
		t.Fatalf("latest = %d，要 5", latest)
	}
	min, err := tr.MinReader()
	if err != nil {
		t.Fatal(err)
	}
	if min != 4 {
		t.Fatalf("min_reader = %d，要 4（3、5 兼容，4 是最大的 breaking）", min)
	}

	allCompatible := mapTrack("chat", 2, map[int64]string{
		1: "old",
		2: "old",
		3: markerCompatible + "\nCREATE TABLE c (id INTEGER);\n",
		4: markerCompatible + "\nCREATE TABLE d (id INTEGER);\n",
	})
	min, err = allCompatible.MinReader()
	if err != nil {
		t.Fatal(err)
	}
	if min != 2 {
		t.Fatalf("全兼容时 min_reader = %d，要起点号 2", min)
	}
}

func TestFixedVersionTrack(t *testing.T) {
	tr := Track{Name: "memory-index", Version: Floors["memory-index"]}
	latest, err := tr.Latest()
	if err != nil {
		t.Fatal(err)
	}
	min, err := tr.MinReader()
	if err != nil {
		t.Fatal(err)
	}
	if latest != Floors["memory-index"] || min != Floors["memory-index"] {
		t.Fatalf("latest/min = %d/%d，要 %d/%d", latest, min, Floors["memory-index"], Floors["memory-index"])
	}
	if _, err := (Track{Name: "memory-index"}).Latest(); err == nil {
		t.Fatal("没有文件和版本号的轨要报错")
	}
}

func TestMalformedMigrationName(t *testing.T) {
	tr := Track{Name: "bad", FS: fstest.MapFS{
		"README.md":      {Data: []byte("ignored")},
		"not_number.sql": {Data: []byte(markerCompatible)},
	}}
	if _, err := tr.Latest(); err == nil {
		t.Fatal("没有数字前缀的 .sql 要报错")
	}
	dup := Track{Name: "dup", FS: fstest.MapFS{
		"0001_a.sql": {Data: []byte(markerCompatible)},
		"0001_b.sql": {Data: []byte(markerCompatible)},
	}}
	if _, err := dup.Latest(); err == nil {
		t.Fatal("同号两份文件要报错")
	}
}

// 现存的五个 store 迁移目录加 memory 的迁移目录，按 Floors 登记的起点号看，必须零违规。
// events 是冻结的 Bun 线，不进 Floors，测试里固定 6。
func TestCheckExistingMigrations(t *testing.T) {
	dirs := map[string]struct {
		dir   string
		table string
	}{
		"chat":   {"../store/migrations/chat", ""},
		"events": {"../store/migrations/events", ""},
		"qq":     {"../store/migrations/qq", "goose_db_version_qq"},
		"local":  {"../store/migrations/local", "goose_db_version_local"},
		"trace":  {"../store/migrations/trace", ""},
		"memory": {"../memory/migrations", ""},
	}
	var tracks []Track
	for name, d := range dirs {
		if _, err := os.Stat(filepath.FromSlash(d.dir)); err != nil {
			t.Fatalf("找不到 %s：%v", d.dir, err)
		}
		floor, ok := Floors[name]
		if name == "events" {
			floor, ok = 6, true
		}
		if !ok {
			t.Fatalf("Floors 里没有迁移轨 %s", name)
		}
		tracks = append(tracks, Track{Name: name, FS: os.DirFS(d.dir), Table: d.table, Floor: floor})
	}
	violations, err := Check(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		for _, v := range violations {
			t.Log(v.String())
		}
		t.Fatalf("%d 个违规，要零", len(violations))
	}
	for _, tr := range tracks {
		latest, err := tr.Latest()
		if err != nil {
			t.Fatal(err)
		}
		if latest != tr.Floor {
			t.Errorf("%s latest = %d，要起点号 %d", tr.Name, latest, tr.Floor)
		}
		min, err := tr.MinReader()
		if err != nil {
			t.Fatal(err)
		}
		if min != tr.Floor {
			t.Errorf("%s min_reader = %d，要起点号 %d（存量全算 breaking）", tr.Name, min, tr.Floor)
		}
	}
}

// goose v3.29.0 的注解认法：-- 开头且含 +goose，去掉 -- 和 +goose 后 TrimSpace、EqualFold。
// 这几种 Down 写法都要报违规，Up 和其他注解不报。
func TestCheckDownAnnotationVariants(t *testing.T) {
	downs := []string{
		"--+goose down",
		"--   +goose   Down",
		"-- +goose DOWN",
	}
	for _, annotation := range downs {
		tr := Track{Name: "chat", Floor: 0, FS: fstest.MapFS{
			"0001_down.sql": {Data: []byte(markerCompatible + "\n-- +goose Up\nCREATE TABLE e (id INTEGER);\n" + annotation + "\nDROP TABLE e;\n")},
		}}
		violations, err := Check([]Track{tr})
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) != 1 || violations[0].Problem != ProblemDown {
			t.Fatalf("%q 的违规 = %+v，要一个 Down", annotation, violations)
		}
	}
	for _, annotation := range []string{"-- +goose Up", "-- +goose StatementBegin", "-- +goose NO TRANSACTION"} {
		tr := Track{Name: "chat", Floor: 0, FS: fstest.MapFS{
			"0001_other.sql": {Data: []byte(markerCompatible + "\n" + annotation + "\nCREATE TABLE e (id INTEGER);\n")},
		}}
		violations, err := Check([]Track{tr})
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) != 0 {
			t.Fatalf("%q 不该报违规：%+v", annotation, violations)
		}
	}
}

func TestCheckViolations(t *testing.T) {
	tr := Track{Name: "chat", Floor: 2, FS: fstest.MapFS{
		"0001_old.sql":          {Data: []byte("old\nCREATE TABLE a (id INTEGER);\n")},
		"0002_old.sql":          {Data: []byte("old\nCREATE TABLE b (id INTEGER);\n")},
		"0003_missing.sql":      {Data: []byte("CREATE TABLE c (id INTEGER);\n")},
		"0004_bad.sql":          {Data: []byte("-- sumika: compatibel\nCREATE TABLE d (id INTEGER);\n")},
		"0005_down.sql":         {Data: []byte(markerBreaking + "\nCREATE TABLE e (id INTEGER);\n-- +goose Down\nDROP TABLE e;\n")},
		"0006_comment_down.sql": {Data: []byte(markerCompatible + "\nCREATE TABLE f (id INTEGER);\n-- +goose Down\n-- 只有注释，没有语句\n")},
	}}
	violations, err := Check([]Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range violations {
		got[v.File] = v.Problem
	}
	want := map[string]string{
		"0003_missing.sql": ProblemMissingMarker,
		"0004_bad.sql":     ProblemBadMarker,
		"0005_down.sql":    ProblemDown,
	}
	if len(got) != len(want) {
		t.Fatalf("违规 = %v，要 %v", got, want)
	}
	for file, problem := range want {
		if got[file] != problem {
			t.Errorf("%s = %q，要 %q", file, got[file], problem)
		}
		if !strings.Contains((Violation{Track: "chat", File: file, Problem: problem}).String(), file) {
			t.Errorf("报错信息没带文件名 %s", file)
		}
	}
}
