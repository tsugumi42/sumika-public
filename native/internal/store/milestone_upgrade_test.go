package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 每份冻结的里程碑都从旧版本升到当前最新：走真实护栏入口、行数不变、版本表正确、快照留着、关键查询能跑。
func TestMilestonesUpgradeToLatest(t *testing.T) {
	for _, m := range storeMilestones() {
		t.Run(m.Name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "milestone.db")
			raw := newMilestoneDB(t, file, filepath.Join("testdata", "milestones", m.Name+".sql"))
			assertMilestoneVersions(t, raw, m)
			before := milestoneCounts(t, raw)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			var (
				db     *sql.DB
				tracks []string
				err    error
			)
			if m.TraceTo > 0 {
				db, err = OpenTraceFile(file)
				tracks = []string{"trace"}
			} else {
				db, err = OpenChatFile(file, m.Platform)
				tracks = append([]string{"chat"}, m.Platform)
			}
			if err != nil {
				t.Fatalf("护栏打开里程碑失败：%v", err)
			}
			defer db.Close()

			assertMilestoneSchema(t, db, tracks)
			after := milestoneCounts(t, db)
			for table, want := range before {
				if got := after[table]; got != want {
					t.Fatalf("%s 的行数从 %d 变成了 %d", table, want, got)
				}
			}
			snaps := snapshotsIn(filepath.Join(dir, "upgrade-snapshots"))
			if len(snaps) != 1 {
				t.Fatalf("迁移前该拍一份快照，实际 %v", snaps)
			}
			if filepath.Ext(snaps[0]) != ".sqlite" || strings.Contains(snaps[0], "-failed") {
				t.Fatalf("快照名字不对：%s", snaps[0])
			}

			if m.TraceTo > 0 {
				assertMilestoneTurns(t, db)
			} else {
				assertMilestoneRecent(t, db, m.Platform)
			}
		})
	}
}

func assertMilestoneVersions(t *testing.T, db *sql.DB, m storeMilestone) {
	t.Helper()
	if m.TraceTo > 0 {
		if got := gooseMax(t, db, "goose_db_version"); got != m.TraceTo {
			t.Fatalf("trace 里程碑在 %d，要 %d", got, m.TraceTo)
		}
		return
	}
	if got := gooseMax(t, db, "goose_db_version"); got != m.ChatTo {
		t.Fatalf("chat 里程碑在 %d，要 %d", got, m.ChatTo)
	}
	if hasMigrations(m.Platform) {
		if got := gooseMax(t, db, "goose_db_version_"+m.Platform); got != m.OwnTo {
			t.Fatalf("%s 里程碑在 %d，要 %d", m.Platform, got, m.OwnTo)
		}
	}
}

func newMilestoneDB(t *testing.T, file, sqlPath string) *sql.DB {
	t.Helper()
	b, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		db.Close()
		t.Fatalf("执行 %s：%v", sqlPath, err)
	}
	return db
}

// milestoneCounts 数每张原有表（不含 goose 版本表、sumika_schema 和 FTS 影子表）的行数。
func milestoneCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	shadow := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_list WHERE schema = 'main' AND type = 'shadow'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		shadow[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND sql IS NOT NULL
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
		if !shadow[name] {
			tables = append(tables, name)
		}
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

func assertMilestoneSchema(t *testing.T, db *sql.DB, tracks []string) {
	t.Helper()
	got := map[string]struct {
		version, minReader int64
	}{}
	rows, err := db.Query(`SELECT track, version, min_reader FROM sumika_schema ORDER BY track`)
	if err != nil {
		t.Fatalf("读 sumika_schema：%v", err)
	}
	for rows.Next() {
		var name string
		var version, minReader int64
		if err := rows.Scan(&name, &version, &minReader); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got[name] = struct {
			version, minReader int64
		}{version, minReader}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range tracks {
		dir := "migrations/" + name
		want, err := latestMigration(dir)
		if err != nil {
			t.Fatal(err)
		}
		tr, err := track(name, dir, "")
		if err != nil {
			t.Fatal(err)
		}
		minReader, err := tr.MinReader()
		if err != nil {
			t.Fatal(err)
		}
		row, ok := got[name]
		if !ok || row.version != want || row.minReader != minReader {
			t.Fatalf("%s 的版本表 %+v，要 %d/%d", name, row, want, minReader)
		}
	}
}

func assertMilestoneRecent(t *testing.T, db *sql.DB, platform string) {
	t.Helper()
	conv := "local:topic:编造"
	if platform == "qq" {
		conv = "qq:group:编造群"
	}
	st := &Store{Events: db}
	rows, last, err := st.RecentWindow(conv, "编造角色", 0, 1<<62)
	if err != nil {
		t.Fatalf("按会话读最近消息：%v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("%s 该有 3 条消息，实际 %d", conv, len(rows))
	}
	if rows[0].Text != "编造的第一句" || rows[1].Text != "编造的第二句" || rows[2].Text != "编造的第三句" {
		t.Fatalf("消息内容不对：%+v", rows)
	}
	if last == nil || last.Text != "编造的第二句" {
		t.Fatalf("该读到角色最后一条消息，实际 %+v", last)
	}
}

func assertMilestoneTurns(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT id, COALESCE(outcome, '') FROM turns ORDER BY started_at`)
	if err != nil {
		t.Fatalf("读回合列表：%v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, outcome string
		if err := rows.Scan(&id, &outcome); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"|"+outcome)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"turn-编造-1|done", "turn-编造-2|stopped", "turn-编造-3|"}
	if len(got) != len(want) {
		t.Fatalf("回合列表有 %d 条，要 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("回合列表第 %d 条是 %q，要 %q（全部 %v）", i, got[i], want[i], got)
		}
	}
}
