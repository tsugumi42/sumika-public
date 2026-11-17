package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/memory"
	"sumika-native/internal/testutil/junction"
)

// 全新目录加一张卡：启动后库在，版本表 memory=3、goose 最大号 3，每张表的列和
// “BaseSchema 造的 Bun 形状旧库经 Migrate 迁到 3”逐表相同（规格-记忆库从零建库 验收 1）。
func TestNewCharacterMemoryDBCreatedAndShapeMatchesMigrated(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	t.Cleanup(a.cleanup)
	file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("全新数据目录启动后没有 memory.db：%v", err)
	}
	if got := memoryTrackVersion(t, file); got != 3 {
		t.Fatalf("sumika_schema 的 memory 是 %d，要 3", got)
	}
	if got := memoryGooseMax(t, file); got != 3 {
		t.Fatalf("goose 最大号是 %d，要 3", got)
	}
	created := memoryShape(t, file)
	if _, ok := created["sumika_schema"]; !ok {
		t.Fatalf("新建库没有 sumika_schema：%v", created)
	}
	delete(created, "sumika_schema")

	ref := filepath.Join(t.TempDir(), "ref.db")
	execSQLFile(t, ref, memory.BaseSchema)
	if err := memory.Migrate(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	migrated := memoryShape(t, ref)
	if len(created) != len(migrated) {
		t.Fatalf("新建库的表 %v，迁移到 3 的旧库是 %v", shapeTableNames(created), shapeTableNames(migrated))
	}
	for table, want := range migrated {
		got, ok := created[table]
		if !ok {
			t.Fatalf("新建库缺表 %s", table)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s 的列是 %v，迁移到 3 的旧库是 %v", table, got, want)
		}
	}
}

// 已有库不被重跑建表：Bun 形状带编造数据启动后一条不少；已经迁到 3 的库启动前后主文件字节相同。
func TestMemoryDBAutoCreateKeepsOldDBUntouched(t *testing.T) {
	t.Parallel()
	t.Run("Bun形状带数据", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		execSQLFile(t, file, memory.BaseSchema)
		execSQLFile(t, file, `INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES ('F1', 'fact', '编的旧事实', 'owner', 'private', 'active', 1)`)
		before := readFile(t, file)
		a := e.open()
		t.Cleanup(a.cleanup)
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var text string
		if err := db.QueryRow(`SELECT text FROM facts WHERE id = 'F1'`).Scan(&text); err != nil {
			t.Fatalf("旧数据读不出来：%v", err)
		}
		if text != "编的旧事实" {
			t.Fatalf("旧数据变了：%q", text)
		}
		if !bytes.Equal(before, readFile(t, file)) {
			t.Fatal("启动改动了 Bun 形状的旧库")
		}
	})
	t.Run("已迁到3", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		// 这一步只验“启动不重跑建表、不改库”；显式 [] 让写入保持只读，字节才有可比性（默认 [*] 会记开写时间）
		e.configure("memory:\n  write:\n    characters: []\n")
		file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		execSQLFile(t, file, memory.BaseSchema)
		if err := memory.Migrate(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		before := readFile(t, file)
		a := e.open()
		t.Cleanup(a.cleanup)
		if !bytes.Equal(before, readFile(t, file)) {
			t.Fatal("启动改动了已经迁到 3 的库")
		}
	})
}

// 只在空的时候建：有表但列不对的库不碰、照旧只读挂上；0 字节和只有 sumika_schema 的库建。
func TestMemoryDBAutoCreateOnlyWhenEmpty(t *testing.T) {
	t.Parallel()
	t.Run("有表但列不对", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		// 故意的裁剪列：这是要测的“列不对的已有库”，不改成 BaseSchema。
		execSQLFile(t, file, `CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT,
			conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER);
CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT);`)
		before := readFile(t, file)
		a := e.open()
		t.Cleanup(a.cleanup)
		if a.memories[testChar] == nil {
			t.Fatal("列不对的已有库以前能只读挂上，现在没有")
		}
		if !bytes.Equal(before, readFile(t, file)) {
			t.Fatal("列不对的已有库被改动了")
		}
	})
	t.Run("0字节", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		a := e.open()
		t.Cleanup(a.cleanup)
		if a.memories[testChar] == nil {
			t.Fatal("0 字节的库没有建成、没挂上记忆")
		}
		if got := memoryTrackVersion(t, file); got != 3 {
			t.Fatalf("0 字节库建成后 memory 是 %d，要 3", got)
		}
	})
	t.Run("只有sumika_schema", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
		execSQLFile(t, file, `CREATE TABLE sumika_schema (track TEXT PRIMARY KEY, version INTEGER NOT NULL, min_reader INTEGER NOT NULL,
			migrated_by TEXT NOT NULL, migrated_at TEXT NOT NULL)`)
		a := e.open()
		t.Cleanup(a.cleanup)
		if a.memories[testChar] == nil {
			t.Fatal("只有 sumika_schema 的库没有建成、没挂上记忆")
		}
		if got := memoryTrackVersion(t, file); got != 3 {
			t.Fatalf("库建成后 memory 是 %d，要 3", got)
		}
	})
}

// 建库失败不拦启动：别的角色照常挂记忆，这个角色不挂、一条带角色/路径/原因词的提醒；
// 调用前不存在的文件不留下，调用前就在的 0 字节文件不删；去掉注入再启动一次自动建成、提醒清掉（验收 5）。
func TestMemoryCreateFailureDoesNotBlockStartup(t *testing.T) {
	e := newEnv(t)
	goMemoryDB(t, e, testChar)
	cardOnly(t, e, "nodb")
	cardOnly(t, e, "empty")
	emptyFile := filepath.Join(e.data, "go", "characters", "empty", "memory.db")
	if err := os.MkdirAll(filepath.Dir(emptyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	nodbFile := filepath.Join(e.data, "go", "characters", "nodb", "memory.db")
	e.configure("memory:\n  write:\n    characters: [nodb, empty]\n")

	opt := e.options()
	opt.NewMemoryDB = func(ctx context.Context, data, file, snapshotDir, relName string) (bool, dbguard.Report, error) {
		switch {
		case strings.Contains(file, "nodb"):
			return false, dbguard.Report{File: file, Action: dbguard.ActionCreate}, &dbguard.Unavailable{
				File: file, Reason: dbguard.ReasonCreateFailed, Detail: "编的建库失败",
			}
		case strings.Contains(file, "empty"):
			return false, dbguard.Report{File: file, Action: dbguard.ActionCreate}, &dbguard.Unavailable{
				File: file, Reason: dbguard.ReasonMigrateFailed, Detail: "编的迁移失败",
			}
		}
		return memory.CreateIfEmpty(ctx, data, file, snapshotDir, relName)
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("建库失败拦住了启动：%v", err)
	}
	if a.memories[testChar] == nil {
		t.Fatal("别的角色没有照常挂记忆")
	}
	if a.memories["nodb"] != nil || a.memories["empty"] != nil {
		t.Fatal("建库失败的角色挂上了记忆")
	}
	if _, err := os.Stat(nodbFile); !os.IsNotExist(err) {
		t.Fatalf("调用前不存在的文件留下了：%v", err)
	}
	if _, err := os.Stat(emptyFile); err != nil {
		t.Fatalf("调用前就在的 0 字节文件被删了：%v", err)
	}
	if got := readFile(t, emptyFile); len(got) != 0 {
		t.Fatalf("0 字节文件失败后有了内容：%d 字节", len(got))
	}
	if !e.logContains("no_memory_db") {
		t.Fatalf("名单里建库失败的角色该记写入只读原因：%v", e.logSnapshot())
	}
	wantReason := map[string]string{"nodb": dbguard.ReasonCreateFailed, "empty": dbguard.ReasonMigrateFailed}
	found := map[string]bool{}
	for _, al := range a.alerts.Open() {
		if strings.HasPrefix(al.Key, "memory:create:") {
			id := strings.TrimPrefix(al.Key, "memory:create:")
			found[id] = true
			if !strings.Contains(al.Body, wantReason[id]) {
				t.Fatalf("提醒没写原因词 %s：%+v", wantReason[id], al)
			}
		}
	}
	for _, id := range []string{"nodb", "empty"} {
		if !found[id] {
			t.Fatalf("角色 %s 没有建库失败提醒：%+v", id, a.alerts.Open())
		}
	}
	a.cleanup()

	b := e.open()
	t.Cleanup(b.cleanup)
	for _, id := range []string{"nodb", "empty"} {
		if b.memories[id] == nil {
			t.Fatalf("去掉注入后角色 %s 没有建成、没挂上记忆", id)
		}
		for _, al := range b.alerts.Open() {
			if al.Key == "memory:create:"+id {
				t.Fatalf("建成后提醒没清掉：%+v", al)
			}
		}
	}
}

// 卡坏的两个分支：没有库 → 不建、不挡别的角色；已经有库 → 整体启动失败（复核 S6）。
func TestBrokenCardMemoryBranches(t *testing.T) {
	t.Parallel()
	brokenCard := func(t *testing.T, e *testEnv, id string) {
		t.Helper()
		cardOnly(t, e, id)
		if err := os.WriteFile(filepath.Join(e.data, "characters", id, "card", "character.yml"), []byte("name: [坏的\n  : :"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("卡坏没有库", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		brokenCard(t, e, "bad")
		a := e.open()
		t.Cleanup(a.cleanup)
		if a.memories["bad"] != nil {
			t.Fatal("卡坏又没有库的角色挂上了记忆")
		}
		if _, err := os.Stat(filepath.Join(e.data, "go", "characters", "bad", "memory.db")); !os.IsNotExist(err) {
			t.Fatalf("卡坏又没有库的角色建了库：%v", err)
		}
		if a.memories[testChar] == nil {
			t.Fatal("卡坏的角色挡住了别的角色挂记忆")
		}
		if !e.logContains("卡读不了") {
			t.Fatalf("卡坏了要在日志里说一声：%v", e.logSnapshot())
		}
	})
	t.Run("卡坏但有库", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		brokenCard(t, e, "bad")
		goMemoryDB(t, e, "bad")
		a, err := Open(context.Background(), e.options())
		if a != nil {
			a.cleanup()
		}
		if err == nil {
			t.Fatal("卡坏但已经有库的角色没有挡住启动")
		}
	})
}

// 角色目录是数据目录外的联接：按建库失败软退、有提醒，数据目录外不建任何文件（验收 5a）。
func TestMemoryCreateLinkFailsSoft(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	parent := filepath.Join(e.data, "go", "characters")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := junction.Create(filepath.Join(parent, testChar), outside); err != nil {
		t.Skipf("这台机器建不了目录联接：%v", err)
	}
	a, err := Open(context.Background(), e.options())
	if err != nil {
		t.Fatalf("联结点拦住了整个启动：%v", err)
	}
	t.Cleanup(a.cleanup)
	if a.memories[testChar] != nil {
		t.Fatal("联结点下的角色挂上了记忆")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("数据目录外多出了文件：%v err=%v", entries, err)
	}
	found := false
	for _, al := range a.alerts.Open() {
		if al.Key == "memory:create:"+testChar {
			found = true
			if !strings.Contains(al.Body, dbguard.ReasonCreateFailed) {
				t.Fatalf("提醒没写原因词：%+v", al)
			}
		}
	}
	if !found {
		t.Fatalf("联接没有建库失败提醒：%+v", a.alerts.Open())
	}
}

// 启动时插件开关不影响建库：配置写 plugins.memory.enabled: false 也照样建、照样挂（验收 6）。
func TestMemoryDBCreateIgnoresPluginSwitch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.configure("plugins:\n  memory:\n    enabled: false\n")
	a := e.open()
	t.Cleanup(a.cleanup)
	file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("插件开关关着时没有建库：%v", err)
	}
	if a.memories[testChar] == nil {
		t.Fatal("插件开关关着时没有挂记忆")
	}
}

// 只看历史模式不走 openMemories，不建库（验收 7）。
func TestArchiveModeDoesNotCreateMemoryDB(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "历史")
	writeCard(t, opt.DataDir, "newchar", "name: 编的角色\ncolor: \"#123456\"\nmodel: m1\n")
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	file := filepath.Join(opt.DataDir, "go", "characters", "newchar", "memory.db")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("只看历史模式建了记忆库：%v", err)
	}
}

func memoryTrackVersion(t *testing.T, file string) int64 {
	t.Helper()
	db := openReadOnlySQL(t, file)
	defer db.Close()
	var v int64
	if err := db.QueryRow(`SELECT version FROM sumika_schema WHERE track = 'memory'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func memoryGooseMax(t *testing.T, file string) int64 {
	t.Helper()
	db := openReadOnlySQL(t, file)
	defer db.Close()
	var v int64
	if err := db.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func memoryShape(t *testing.T, file string) map[string][]string {
	t.Helper()
	db := openReadOnlySQL(t, file)
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
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
	out := map[string][]string{}
	for _, table := range tables {
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for cols.Next() {
			var name string
			if err := cols.Scan(&name); err != nil {
				cols.Close()
				t.Fatal(err)
			}
			names = append(names, name)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			t.Fatal(err)
		}
		out[table] = names
	}
	return out
}

func shapeTableNames(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func openReadOnlySQL(t *testing.T, file string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func execSQLFile(t *testing.T, file, q string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(fmt.Errorf("执行 SQL：%w", err))
	}
}

func readFile(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
