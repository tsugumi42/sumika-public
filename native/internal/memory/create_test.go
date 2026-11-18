package memory

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
	"sumika-native/internal/testutil/junction"
)

// BaseSchema 建出来的表正好是 baseColumns 里的五张，列不多不少（顺序也一致）。
func TestBaseSchemaMatchesBaseColumns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "memory.db")
	db, err := openBaseDB(context.Background(), file, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables := userTables(t, db)
	if len(tables) != len(baseColumns) {
		t.Fatalf("BaseSchema 建出 %v，baseColumns 有 %d 张表", tables, len(baseColumns))
	}
	for _, table := range tables {
		want, ok := baseColumns[table]
		if !ok {
			t.Fatalf("BaseSchema 多建了表 %s", table)
		}
		got := tableColumns(t, db, table)
		if !slices.Equal(got, want) {
			t.Fatalf("%s 的列是 %v，baseColumns 是 %v", table, got, want)
		}
	}
	for table := range baseColumns {
		if !slices.Contains(tables, table) {
			t.Fatalf("BaseSchema 没建表 %s", table)
		}
	}
}

// 基础建表放在一个事务里：第二条语句失败时，前面已经建的表也回滚掉（复核 M1）。
func TestOpenBaseDBSchemaIsAtomic(t *testing.T) {
	file := filepath.Join(t.TempDir(), "memory.db")
	// 同名的 episodes 视图让 CREATE TABLE episodes 冲突
	execFile(t, file, `CREATE VIEW episodes AS SELECT 1 AS id`)
	if _, err := openBaseDB(context.Background(), file, true); err == nil {
		t.Fatal("episodes 冲突时基础建表没报错")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("基础建表失败后留下了 %d 张表，要一张不剩", tables)
	}
}

func createFixture(t *testing.T) (data, file, rel string) {
	t.Helper()
	data = t.TempDir()
	rel = "go/characters/编的角色/memory.db"
	return data, filepath.Join(data, filepath.FromSlash(rel)), rel
}

func TestCreateIfEmptyCreatesMissingDB(t *testing.T) {
	ctx := context.Background()
	data, file, rel := createFixture(t)
	created, rep, err := CreateIfEmpty(ctx, data, file, filepath.Join(t.TempDir(), "snapshots"), rel)
	if err != nil {
		t.Fatal(err)
	}
	if !created || rep.Action != dbguard.ActionCreate {
		t.Fatalf("created=%v action=%v，要新建", created, rep.Action)
	}
	if version, minReader, by := schemaRow(t, file, "memory"); version != 3 || minReader != 3 || by == "" {
		t.Fatalf("版本表是 version=%d min_reader=%d migrated_by=%q，要 3/3/非空", version, minReader, by)
	}
	if got := gooseVersion(t, file); got != 3 {
		t.Fatalf("goose 版本是 %d，要 3", got)
	}
	if left, _ := filepath.Glob(file + ".empty-*"); len(left) != 0 {
		t.Fatalf("新建成功后留下了旁名文件：%v", left)
	}
}

func TestCreateIfEmptyCreatesZeroByteFile(t *testing.T) {
	ctx := context.Background()
	data, file, rel := createFixture(t)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	created, _, err := CreateIfEmpty(ctx, data, file, "", rel)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("0 字节文件该按空库新建")
	}
	if got := gooseVersion(t, file); got != 3 {
		t.Fatalf("goose 版本是 %d，要 3", got)
	}
	if left, _ := filepath.Glob(file + ".empty-*"); len(left) != 0 {
		t.Fatalf("新建成功后留下了旁名文件：%v", left)
	}
}

func TestCreateIfEmptyCreatesSchemaOnlyDB(t *testing.T) {
	ctx := context.Background()
	data, file, rel := createFixture(t)
	execFile(t, file, `CREATE TABLE sumika_schema (track TEXT PRIMARY KEY, version INTEGER NOT NULL, min_reader INTEGER NOT NULL,
		migrated_by TEXT NOT NULL, migrated_at TEXT NOT NULL)`)
	created, _, err := CreateIfEmpty(ctx, data, file, "", rel)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("只有 sumika_schema 的库该按空库新建")
	}
	if version, _, _ := schemaRow(t, file, "memory"); version != 3 {
		t.Fatalf("版本表是 %d，要 3", version)
	}
}

// 已有用户表的库一律不碰：Bun 形状、已迁移、列不对的旧形状都一样（复核 C1）。
func TestCreateIfEmptyLeavesNonEmptyDBAlone(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		build func(t *testing.T, file string)
	}{
		{"Bun形状", func(t *testing.T, file string) {
			execFile(t, file, BaseSchema)
			execFile(t, file, `INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES ('F1', 'fact', '编的事实', 'owner', 'private', 'active', 1)`)
		}},
		{"已迁到3", func(t *testing.T, file string) {
			execFile(t, file, BaseSchema)
			if err := Migrate(context.Background(), file); err != nil {
				t.Fatal(err)
			}
		}},
		{"列不对的旧形状", func(t *testing.T, file string) {
			// 故意的裁剪列：这是要测的“列不对的旧形状”，不改成 BaseSchema。
			execFile(t, file, `CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT,
				conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, file, rel := createFixture(t)
			c.build(t, file)
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			created, rep, err := CreateIfEmpty(ctx, data, file, "", rel)
			if err != nil {
				t.Fatal(err)
			}
			if created {
				t.Fatal("已有用户表的库不该新建")
			}
			if rep.Action == dbguard.ActionCreate {
				t.Fatalf("护栏判定是 %v，要不动", rep.Action)
			}
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(before, after) {
				t.Fatal("CreateIfEmpty 改动了已有库")
			}
		})
	}
}

// restore 不覆盖原名已有的文件（这次新建没删干净）：报错、留着那份，其他旁文件照常挪回。
func TestStagedEmptyOriginalRestoreRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "memory.db")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file+"-wal", []byte("调用前的旁文件"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged, err := stageEmptyOriginal(file)
	if err != nil || staged == nil {
		t.Fatalf("挪空库：staged=%v err=%v", staged, err)
	}
	if err := os.WriteFile(file, []byte("这次新建的"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = staged.restore()
	if err == nil || !strings.Contains(err.Error(), "已经有文件") {
		t.Fatalf("原名已有文件时 restore 该报错：%v", err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "这次新建的" {
		t.Fatalf("restore 覆盖了原名已有的文件：%q %v", b, err)
	}
	if b, err := os.ReadFile(file + "-wal"); err != nil || string(b) != "调用前的旁文件" {
		t.Fatalf("旁文件没挪回原名：%q %v", b, err)
	}
}

// 可写连接打不开：新建失败是 create_failed，调用前不存在的文件不留下。
func TestCreateIfEmptyOpenDBErrorLeavesNoFile(t *testing.T) {
	old := createOpenDB
	createOpenDB = func(context.Context, string, bool) (*sql.DB, error) {
		return nil, errors.New("编的打不开")
	}
	defer func() { createOpenDB = old }()

	data, file, rel := createFixture(t)
	created, rep, err := CreateIfEmpty(context.Background(), data, file, "", rel)
	if err == nil || created {
		t.Fatalf("建库失败该报错：created=%v err=%v", created, err)
	}
	var u *dbguard.Unavailable
	if !errors.As(err, &u) || u.Reason != dbguard.ReasonCreateFailed {
		t.Fatalf("错误是 %v，要 create_failed", err)
	}
	if rep.Action != dbguard.ActionCreate {
		t.Fatalf("rep.Action 是 %v，要 create（调用方据此不拦启动）", rep.Action)
	}
	if _, statErr := os.Stat(file); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("调用前不存在的文件留下了：%v", statErr)
	}
}

// fullMigrationsWithBad0004 把内嵌的 0001～0003 原样拷进 MapFS，再加一个会失败的 0004：
// 迁移失败测试里 0001～0003 真的跑过，不是只拿一个 0004 的空轨。
func fullMigrationsWithBad0004(t *testing.T) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(migrationsFS, "migrations", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(migrationsFS, p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out["migrations/0004_bad.sql"] = &fstest.MapFile{
		Data: []byte("-- sumika: compatible\nALTER TABLE no_such_table ADD COLUMN x;\n"),
	}
	return out
}

// 迁移失败（完整 0001～0003 + 坏的 0004）：三种原始形状都退回原样。
// 调用前不存在的文件不留任何东西；0 字节 / 只有 sumika_schema 的文件字节一样；去掉注入再调一次能建成。
func TestCreateIfEmptyMigrationFailureRestoresShapes(t *testing.T) {
	ctx := context.Background()
	shapes := []struct {
		name  string
		build func(t *testing.T, file string)
	}{
		{"不存在", func(*testing.T, string) {}},
		{"0字节", func(t *testing.T, file string) {
			if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"只有sumika_schema", func(t *testing.T, file string) {
			execFile(t, file, `CREATE TABLE sumika_schema (track TEXT PRIMARY KEY, version INTEGER NOT NULL, min_reader INTEGER NOT NULL,
				migrated_by TEXT NOT NULL, migrated_at TEXT NOT NULL)`)
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			data, file, rel := createFixture(t)
			shape.build(t, file)
			_, statErr := os.Stat(file)
			existed := statErr == nil
			var before []byte
			if existed {
				var err error
				before, err = os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
			}

			old := migrationFS
			migrationFS = fullMigrationsWithBad0004(t)
			defer func() { migrationFS = old }()

			created, rep, err := CreateIfEmpty(ctx, data, file, "", rel)
			if err == nil || created {
				t.Fatalf("迁移失败该报错：created=%v err=%v", created, err)
			}
			if rep.Action != dbguard.ActionCreate {
				t.Fatalf("rep.Action 是 %v，要 create", rep.Action)
			}
			if existed {
				after, err := os.ReadFile(file)
				if err != nil {
					t.Fatalf("调用前就在的文件没了：%v", err)
				}
				if !slices.Equal(before, after) {
					t.Fatalf("失败后原文件字节变了：前 %d 字节，后 %d 字节", len(before), len(after))
				}
			} else if _, statErr := os.Stat(file); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("调用前不存在的文件留下了：%v", statErr)
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if _, statErr := os.Stat(file + suffix); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("失败后留下了 %s：%v", suffix, statErr)
				}
			}
			if left, _ := filepath.Glob(file + ".empty-*"); len(left) != 0 {
				t.Fatalf("失败后旁名没挪回或没删：%v", left)
			}

			migrationFS = old
			created, _, err = CreateIfEmpty(ctx, data, file, "", rel)
			if err != nil || !created {
				t.Fatalf("去掉注入后没建成：created=%v err=%v", created, err)
			}
			if got := gooseVersion(t, file); got != 3 {
				t.Fatalf("建成后 goose 版本是 %d，要 3", got)
			}
		})
	}
}

// 目录建不出来（这里用文件占住角色目录）：位置检查报错、rep.Action 仍是 create，不留下库文件。
func TestCreateIfEmptyParentBlocked(t *testing.T) {
	data, file, rel := createFixture(t)
	blocked := filepath.Dir(file)
	if err := os.MkdirAll(filepath.Dir(blocked), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("占住目录名"), 0600); err != nil {
		t.Fatal(err)
	}
	created, rep, err := CreateIfEmpty(context.Background(), data, file, "", rel)
	if err == nil || created {
		t.Fatalf("目录建不出来该报错：created=%v err=%v", created, err)
	}
	if rep.Action != dbguard.ActionCreate {
		t.Fatalf("rep.Action 是 %v，要 create（调用方据此不拦启动）", rep.Action)
	}
	if _, statErr := os.Stat(file); statErr == nil {
		t.Fatal("目录建不出来还留下了库文件")
	}
}

// 角色 id 先过 ValidCharacterID：带 .. 的不建（复核 S1）。
func TestCreateIfEmptyRejectsBadCharacterID(t *testing.T) {
	data := t.TempDir()
	file := filepath.Join(data, "go", "characters", "a..b", "memory.db")
	created, rep, err := CreateIfEmpty(context.Background(), data, file, "", "go/characters/a..b/memory.db")
	if err == nil || created {
		t.Fatalf("不合法角色 id 建库了：created=%v err=%v", created, err)
	}
	if rep.Action != dbguard.ActionCreate {
		t.Fatalf("rep.Action 是 %v，要 create", rep.Action)
	}
	if _, statErr := os.Stat(file); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("不合法角色 id 留下了文件：%v", statErr)
	}
}

// 角色目录是 outside 的联接、或 memory.db 是悬空符号链接：位置检查拒绝，数据目录外不建任何文件（验收 5a）。
func TestCreateIfEmptyRefusesLinks(t *testing.T) {
	ctx := context.Background()
	t.Run("角色目录是联接", func(t *testing.T) {
		data := t.TempDir()
		outside := t.TempDir()
		parent := filepath.Join(data, "go", "characters")
		if err := os.MkdirAll(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := junction.Create(filepath.Join(parent, "编的角色"), outside); err != nil {
			t.Skipf("这台机器建不了目录联接：%v", err)
		}
		file := filepath.Join(parent, "编的角色", "memory.db")
		created, rep, err := CreateIfEmpty(ctx, data, file, "", "go/characters/编的角色/memory.db")
		if err == nil || created {
			t.Fatalf("联接下的库建成了：created=%v err=%v", created, err)
		}
		if rep.Action != dbguard.ActionCreate {
			t.Fatalf("rep.Action 是 %v，要 create", rep.Action)
		}
		if !errors.Is(err, paths.ErrMemoryPath) {
			t.Fatalf("错误是 %v，要 ErrMemoryPath", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("数据目录外多出了文件：%v err=%v", entries, err)
		}
	})
	t.Run("memory.db是悬空符号链接", func(t *testing.T) {
		data := t.TempDir()
		file := filepath.Join(data, "go", "characters", "编的角色", "memory.db")
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "外面.db")
		if err := os.Symlink(outside, file); err != nil {
			t.Skipf("这台机器建不了文件符号链接：%v", err)
		}
		created, rep, err := CreateIfEmpty(ctx, data, file, "", "go/characters/编的角色/memory.db")
		if err == nil || created {
			t.Fatalf("悬空符号链接下的库建成了：created=%v err=%v", created, err)
		}
		if rep.Action != dbguard.ActionCreate {
			t.Fatalf("rep.Action 是 %v，要 create", rep.Action)
		}
		if _, statErr := os.Stat(outside); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("数据目录外建出了目标文件：%v", statErr)
		}
	})
}

// ctx 取消不是建库失败：返回取消错误，Action 不标 create（复核 S2）。
func TestCreateIfEmptyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, file, rel := createFixture(t)
	created, rep, err := CreateIfEmpty(ctx, data, file, "", rel)
	if created || err == nil {
		t.Fatalf("取消后还建了库：created=%v err=%v", created, err)
	}
	if rep.Action == dbguard.ActionCreate {
		t.Fatal("取消被标成了建库失败")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误是 %v，要取消错误", err)
	}
}

func execFile(t *testing.T, file, q string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func userTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
