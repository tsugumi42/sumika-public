package memory

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 开写入前的备份和 memory_meta（规格-记忆写入上线 W5 验收 4）。库在临时目录里造，事实、名字都是编的

func seedFacts(n int) func(db *sql.DB) {
	return func(db *sql.DB) {
		for i := 0; i < n; i++ {
			if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES (?, 'fact', ?, 'owner', 'private', 'active', ?)`,
				"F"+string(rune('A'+i)), "编的事实"+string(rune('甲'+i)), 1000+i); err != nil {
				panic(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at) VALUES ('E1', 'qq:group:5001001', '编的片段', 'private', 1, 3, 2000)`); err != nil {
			panic(err)
		}
	}
}

func rowsOf(t *testing.T, file, query string) (n int) {
	t.Helper()
	db, err := openReadOnly(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestBackupBeforeWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := migratedDB(t, seedFacts(3))
	dir := filepath.Join(t.TempDir(), "备份", "记忆")
	at := time.Date(2026, 11, 2, 3, 4, 5, 0, time.Local)
	srcHash := fileHash(t, src)

	r, err := BackupMemoryDB(ctx, src, dir, "suzune", BackupBeforeWrite, at)
	if err != nil {
		t.Fatal(err)
	}
	if r.Existed || r.Facts != 3 || r.Episodes != 1 {
		t.Fatalf("第一次该新做、3 条事实 1 个片段：%+v", r)
	}
	if !strings.HasPrefix(filepath.Base(r.File), "memory-suzune-20261102-030405-before-write-") || !strings.HasSuffix(r.File, ".db") || filepath.Dir(r.File) != dir {
		t.Fatalf("文件名不对：%s", r.File)
	}
	if rowsOf(t, r.File, `SELECT count(*) FROM facts`) != 3 || rowsOf(t, r.File, `SELECT count(*) FROM episodes`) != 1 {
		t.Fatal("备份里的行数和原库不一样")
	}
	if fileHash(t, src) != srcHash {
		t.Fatal("备份改了原库")
	}
	if names := dirFiles(t, dir); len(names) != 1 {
		t.Fatalf("目录里该只有一份备份（没有 .partial）：%v", names)
	}

	// 同一个库、同一个指纹：跳过，还是那一份
	r2, err := BackupMemoryDB(ctx, src, dir, "suzune", BackupBeforeWrite, at.Add(time.Hour))
	if err != nil || !r2.Existed || r2.File != r.File || len(dirFiles(t, dir)) != 1 {
		t.Fatalf("同指纹该跳过：%+v %v %v", r2, err, dirFiles(t, dir))
	}

	// 另一种备份（开整理前）是另一份
	r3, err := BackupMemoryDB(ctx, src, dir, "suzune", BackupBeforeSummarize, at.Add(2*time.Hour))
	if err != nil || r3.Existed || r3.File == r.File || !strings.Contains(r3.File, "-before-summarize-") {
		t.Fatalf("另一种备份该新做：%+v %v", r3, err)
	}

	// 别的角色的备份不算：同一个库、同一个指纹也要做自己的
	r4, err := BackupMemoryDB(ctx, src, dir, "kiri", BackupBeforeWrite, at)
	if err != nil || r4.Existed {
		t.Fatalf("别的角色该新做：%+v %v", r4, err)
	}

	// 库变了（指纹变了）：新做一份，旧的留着
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES ('FZ', 'fact', '又一条', 'owner', 'private', 'active', 3000)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	r5, err := BackupMemoryDB(ctx, src, dir, "suzune", BackupBeforeWrite, at.Add(3*time.Hour))
	if err != nil || r5.Existed || r5.File == r.File || r5.Facts != 4 {
		t.Fatalf("库变了该新做：%+v %v", r5, err)
	}
	if rowsOf(t, r.File, `SELECT count(*) FROM facts`) != 3 {
		t.Fatal("旧的那一份被改了")
	}
}

func TestBackupFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := migratedDB(t, seedFacts(2))
	at := time.Date(2026, 11, 2, 3, 4, 5, 0, time.Local)

	t.Run("备份目录建不了", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "占位的文件")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := BackupMemoryDB(ctx, src, filepath.Join(blocker, "备份"), "suzune", BackupBeforeWrite, at); err == nil {
			t.Fatal("目录建不了该报错")
		}
	})
	t.Run("备份目录是空串", func(t *testing.T) {
		if _, err := BackupMemoryDB(ctx, src, "  ", "suzune", BackupBeforeWrite, at); err == nil {
			t.Fatal("空目录该报错")
		}
	})
	t.Run("原库不存在，也不替它建出来", func(t *testing.T) {
		gone := filepath.Join(t.TempDir(), "没有.db")
		if _, err := BackupMemoryDB(ctx, gone, t.TempDir(), "suzune", BackupBeforeWrite, at); err == nil {
			t.Fatal("原库不存在该报错")
		}
		if _, err := os.Stat(gone); err == nil {
			t.Fatal("备份把不存在的原库建出来了")
		}
	})
	t.Run("角色 id 和种类不对", func(t *testing.T) {
		dir := t.TempDir()
		for _, c := range []struct{ char, kind string }{{"../x", BackupBeforeWrite}, {"", BackupBeforeWrite}, {"a:b", BackupBeforeWrite}, {"suzune", "before-everything"}, {"suzune", ""}} {
			if _, err := BackupMemoryDB(ctx, src, dir, c.char, c.kind, at); err == nil {
				t.Errorf("%q %q 该报错", c.char, c.kind)
			}
		}
		if names := dirFiles(t, dir); len(names) != 0 {
			t.Fatalf("报错的调用留下了文件：%v", names)
		}
	})
	t.Run("上次崩在半路留下的 .partial 和空文件不算备份", func(t *testing.T) {
		dir := t.TempDir()
		first, err := BackupMemoryDB(ctx, src, dir, "suzune", BackupBeforeWrite, at)
		if err != nil {
			t.Fatal(err)
		}
		// 把真备份换成一个空文件（崩在改名之后写之前不会出现，但别的原因也会留空文件），再留一个 .partial
		if err := os.Remove(first.File); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(first.File, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		later := at.Add(time.Minute)
		leftover := filepath.Join(dir, "memory-suzune-"+later.Format("20060102-150405")+"-before-write-"+strings.TrimSuffix(strings.Split(filepath.Base(first.File), "before-write-")[1], ".db")+".db.partial")
		if err := os.WriteFile(leftover, []byte("半截"), 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := BackupMemoryDB(ctx, src, dir, "suzune", BackupBeforeWrite, later)
		if err != nil || r.Existed {
			t.Fatalf("空文件不算备份，该重新做：%+v %v", r, err)
		}
		if rowsOf(t, r.File, `SELECT count(*) FROM facts`) != 2 {
			t.Fatal("重做的备份内容不对")
		}
		if _, err := os.Stat(leftover); err == nil {
			t.Fatal(".partial 没清掉")
		}
	})
	t.Run("没迁移的 Bun 库也能备份（缺的表按 0 算）", func(t *testing.T) {
		bun := newBunDB(t, seedFacts(2))
		r, err := BackupMemoryDB(ctx, bun, t.TempDir(), "suzune", BackupBeforeWrite, at)
		if err != nil || r.Facts != 2 {
			t.Fatalf("%+v %v", r, err)
		}
	})
}

func TestCheckSchemaAndMeta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := CheckSchema(ctx, migratedDB(t, nil)); err != nil {
		t.Fatalf("迁移过的库该通过：%v", err)
	}
	if err := CheckSchema(ctx, newBunDB(t, nil)); !errors.Is(err, ErrNeedsMigration) {
		t.Fatalf("没迁移的库该报 ErrNeedsMigration，得到 %v", err)
	}
	gone := filepath.Join(t.TempDir(), "没有.db")
	if err := CheckSchema(ctx, gone); err == nil {
		t.Fatal("库不存在该报错")
	}
	if _, err := os.Stat(gone); err == nil {
		t.Fatal("检查把库建出来了")
	}

	file := migratedDB(t, nil)
	if _, ok, err := MetaValue(ctx, file, MetaWriteEnabledAt); ok || err != nil {
		t.Fatalf("还没记过：%v %v", ok, err)
	}
	if _, ok, err := MetaValue(ctx, newBunDB(t, nil), MetaWriteEnabledAt); ok || err != nil {
		t.Fatalf("没有 memory_meta 表的库按没记过算：%v %v", ok, err)
	}
	m, _ := openW(t, file, nil)
	if w, err := m.SetMetaIfAbsent(ctx, MetaWriteEnabledAt, 1762041600000); !w || err != nil {
		t.Fatalf("第一次该写进去：%v %v", w, err)
	}
	if w, err := m.SetMetaIfAbsent(ctx, MetaWriteEnabledAt, 1762999999999); w || err != nil {
		t.Fatalf("第二次不该动：%v %v", w, err)
	}
	if v, ok, err := m.Meta(ctx, MetaWriteEnabledAt); v != 1762041600000 || !ok || err != nil {
		t.Fatalf("读回来：%d %v %v", v, ok, err)
	}
	if v, ok, err := m.Meta(ctx, MetaSummarizeEnabledAt); ok || err != nil || v != 0 {
		t.Fatalf("没记过的键：%d %v %v", v, ok, err)
	}
	m.Close()
	if v, ok, err := MetaValue(ctx, file, MetaWriteEnabledAt); v != 1762041600000 || !ok || err != nil {
		t.Fatalf("重新只读打开读回来：%d %v %v", v, ok, err)
	}
	// 只读打开的记忆写不了 meta
	ro, err := Open(ctx, file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.SetMetaIfAbsent(ctx, MetaSummarizeEnabledAt, 1); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("只读的该报 ErrNotWritable：%v", err)
	}
}

// 校验本身要拦得住：行数对不上的、不是库的、坏了的备份都不能算数
func TestVerifyBackupCatchesBadFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := migratedDB(t, seedFacts(3))
	db, err := openBackupSource(src)
	if err != nil {
		t.Fatal(err)
	}
	want, err := takeProbes(ctx, db)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	good, err := BackupMemoryDB(ctx, src, t.TempDir(), "suzune", BackupBeforeWrite, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(ctx, good.File, want); err != nil {
		t.Fatalf("好的备份该通过：%v", err)
	}
	// 少一条事实的"备份"
	short := migratedDB(t, seedFacts(2))
	if err := verifyBackup(ctx, short, want); err == nil || !strings.Contains(err.Error(), "backup_mismatch") {
		t.Fatalf("少一条该报 backup_mismatch，得到 %v", err)
	}
	// 不是库的文件
	junk := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("不是数据库。", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(ctx, junk, want); err == nil {
		t.Fatal("不是库的文件该报错")
	}
	// 半截的库：把好备份截掉后半
	b, err := os.ReadFile(good.File)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(t.TempDir(), "cut.db")
	if err := os.WriteFile(cut, b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(ctx, cut, want); err == nil {
		t.Fatal("截掉一半的备份该报错")
	}
}
