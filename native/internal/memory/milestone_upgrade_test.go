package memory

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/dbguard"
)

func TestMemoryMilestonesUpgrade(t *testing.T) {
	for _, m := range memoryMilestones() {
		t.Run(m.Name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "memory.db")
			raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join("..", "store", "testdata", "milestones", m.Name+".sql"))
			if err != nil {
				raw.Close()
				t.Fatal(err)
			}
			if _, err := raw.Exec(string(b)); err != nil {
				raw.Close()
				t.Fatalf("执行里程碑 SQL：%v", err)
			}
			version, hasGoose := memoryMilestoneVersion(t, raw)
			if m.Migrate {
				if !hasGoose || version != 3 {
					raw.Close()
					t.Fatalf("迁完的里程碑该在 goose 3，实际 %d（有表 %v）", version, hasGoose)
				}
			} else if hasGoose {
				raw.Close()
				t.Fatalf("Bun 时代里程碑不该有 goose 表，实际在 %d", version)
			}
			before := memoryMilestoneCounts(t, raw)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			snapDir := filepath.Join(dir, "snapshots")
			rep, err := MigrateGuarded(context.Background(), file, snapDir, "characters/编造角色/memory.db")
			if err != nil {
				t.Fatalf("护栏迁移记忆库：%v", err)
			}
			snaps := snapshotFiles(t, snapDir)
			if m.Migrate {
				// 已经是 goose 0003、只差 sumika_schema：护栏只补写版本表，不拍迁移快照。
				if rep.Action != dbguard.ActionOpen {
					t.Fatalf("已经是最新的库该走 open，实际 %s", rep.Action)
				}
				if len(snaps) != 0 {
					t.Fatalf("没有待跑迁移不该拍快照：%v", snaps)
				}
			} else {
				if rep.Action != dbguard.ActionMigrate {
					t.Fatalf("Bun 老库该走 migrate，实际 %s", rep.Action)
				}
				if len(snaps) != 1 || strings.Contains(snaps[0], "-failed") {
					t.Fatalf("迁移前该拍一份快照，实际 %v", snaps)
				}
			}
			assertMemorySchema(t, file)

			mem, err := Open(context.Background(), file, nil, nil, Options{})
			if err != nil {
				t.Fatalf("按真实入口打开迁移后的记忆库：%v", err)
			}
			facts := map[string]bool{}
			for _, it := range mem.Items() {
				if it.Type == "fact" {
					facts[it.Text] = true
				}
			}
			if err := mem.Close(); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"编造的事实一", "编造的事实二", "编造的事实三"} {
				if !facts[text] {
					t.Fatalf("迁移后读不到事实 %q，实际 %v", text, facts)
				}
			}

			db, err := openReadOnly(file)
			if err != nil {
				t.Fatal(err)
			}
			after := memoryMilestoneCounts(t, db)
			db.Close()
			for table, want := range before {
				if got := after[table]; got != want {
					t.Fatalf("%s 的行数从 %d 变成了 %d", table, want, got)
				}
			}
		})
	}
}

func memoryMilestoneVersion(t *testing.T, db *sql.DB) (int64, bool) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return 0, false
	}
	var version int64
	if err := db.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version, true
}

func memoryMilestoneCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND sql IS NOT NULL
		AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'goose_db_version%' AND name <> 'sumika_schema' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]int, len(tables))
	for _, name := range tables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM "` + name + `"`).Scan(&n); err != nil {
			t.Fatalf("数 %s 的行数：%v", name, err)
		}
		out[name] = n
	}
	return out
}

func snapshotFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sqlite") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func assertMemorySchema(t *testing.T, file string) {
	t.Helper()
	tracks, err := memoryTracks()
	if err != nil {
		t.Fatal(err)
	}
	latest, err := tracks[0].Latest()
	if err != nil {
		t.Fatal(err)
	}
	minReader, err := tracks[0].MinReader()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openReadOnly(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, min int64
	if err := db.QueryRow(`SELECT version, min_reader FROM sumika_schema WHERE track = 'memory'`).Scan(&version, &min); err != nil {
		t.Fatalf("读记忆库版本表：%v", err)
	}
	if version != latest || min != minReader {
		t.Fatalf("记忆库版本表是 %d/%d，要 %d/%d", version, min, latest, minReader)
	}
}
