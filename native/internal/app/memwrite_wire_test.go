package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/memory"
	"sumika-native/internal/memory/memtest"
)

// 记忆写入的装配（规格-记忆写入上线 W5）：配置 → openMemories → memwrite.Open。
// memwrite 自己的测试用手拼的 Input，管不到 app 这一层：备份目录传错、"有没有后台模型"误判、
// 配置里的角色不存在 / 没有记忆库的提醒写错，都只能在这里看出来。库、卡、配置都在临时目录里造，不碰真实数据

// goMemoryDB 在 <data>/go/characters/<id>/memory.db 造一个当前结构的空库，不需要导入标记。
func goMemoryDB(t *testing.T, e *testEnv, id string) string {
	t.Helper()
	file := filepath.Join(e.data, "go", "characters", id, "memory.db")
	memtest.Fresh(t, e.data, file)
	return file
}

// cardOnly 给角色放一张卡（拷示例卡），不放记忆库
func cardOnly(t *testing.T, e *testEnv, id string) {
	t.Helper()
	if err := copyCard(e.root, e.data); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(e.data, "characters", testChar, "card")
	dst := filepath.Join(e.data, "characters", id, "card")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// configure 把 memory.write、backup、后台模型这些追加进配置再装配
func (e *testEnv) configure(extra string) {
	e.t.Helper()
	e.extraConfig = extra
	e.writeConfig(testToken)
}

func yamlPath(p string) string { return filepath.ToSlash(p) }

func TestOpenMemoriesWritesOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	t.Run("默认配置：只开 remember、整理关", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		goMemoryDB(t, e, testChar)
		a := e.open()
		t.Cleanup(a.cleanup)
		m := a.memories[testChar]
		if m == nil || !m.Writable() || m.SummarizeEnabled() {
			t.Fatalf("默认配置该只开 remember：%+v", m)
		}
		if !e.logContains("写入已开") {
			t.Errorf("默认配置该有一行写入已开：%v", e.logSnapshot())
		}
		if e.logContains("写入保持只读") {
			t.Errorf("默认配置不该说写入保持只读：%v", e.logSnapshot())
		}
	})

	t.Run("两档都开：备份落在 backup.dir、可写、整理开、日志不带本机路径", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		file := goMemoryDB(t, e, testChar)
		backup := filepath.Join(t.TempDir(), "备份")
		e.configure("defaults:\n  backgroundModel: " + testModel + "\nbackup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: latest\n")
		a := e.open()
		t.Cleanup(a.cleanup)
		m := a.memories[testChar]
		if m == nil || !m.Writable() || !m.SummarizeEnabled() {
			t.Fatalf("两档都该开：%+v", m)
		}
		var names []string
		entries, err := os.ReadDir(backup)
		if err != nil {
			t.Fatalf("备份该落在 backup.dir（%v）：%v", backup, err)
		}
		for _, en := range entries {
			names = append(names, en.Name())
		}
		joined := strings.Join(names, "|")
		if !strings.Contains(joined, "before-write") || !strings.Contains(joined, "before-summarize") {
			t.Errorf("该有开写前和开整理前两份备份：%v", names)
		}
		if _, err := os.Stat(a.paths.BackupRoot()); err == nil {
			t.Errorf("备份不该落到数据目录旁边的默认位置 %s（读了 paths.BackupRoot 而不是 cfg.Backup.Dir）", a.paths.BackupRoot())
		}
		if _, ok, err := memory.MetaValue(context.Background(), file, memory.MetaWriteEnabledAt); err != nil || !ok {
			t.Errorf("该记下开写时间：%v %v", ok, err)
		}
		logs := strings.Join(e.logSnapshot(), "\n")
		if !strings.Contains(logs, "写入已开") {
			t.Errorf("该有一行写入已开：%s", logs)
		}
		for _, bad := range []string{e.data, backup, filepath.Base(e.data)} {
			for _, l := range e.logSnapshot() {
				if strings.Contains(l, "写入已开") && strings.Contains(l, bad) {
					t.Errorf("写入已开那一行带了本机路径 %q：%q", bad, l)
				}
			}
		}
	})

	t.Run("没配后台模型：整理不开、remember 照开", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		goMemoryDB(t, e, testChar)
		backup := filepath.Join(t.TempDir(), "备份")
		e.configure("backup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: latest\n")
		a := e.open()
		t.Cleanup(a.cleanup)
		m := a.memories[testChar]
		if m == nil || !m.Writable() || m.SummarizeEnabled() {
			t.Fatalf("该只开 remember：%+v", m)
		}
		if !e.logContains("no_background_model") {
			t.Errorf("日志该说原因 no_background_model：%v", e.logSnapshot())
		}
	})

	t.Run("配置里的角色不存在、没有记忆库：缺库的自动建并挂上", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		goMemoryDB(t, e, testChar)
		cardOnly(t, e, "nodb")
		backup := filepath.Join(t.TempDir(), "备份")
		e.configure("backup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    characters: [" + testChar + ", nodb, ghost]\n")
		a := e.open()
		t.Cleanup(a.cleanup)
		if m := a.memories["nodb"]; m == nil {
			t.Fatal("缺库的角色该自动建库并挂上记忆")
		}
		if _, err := os.Stat(filepath.Join(e.data, "go", "characters", "nodb", "memory.db")); err != nil {
			t.Errorf("缺库的角色没有自动建库：%v", err)
		}
		if !e.logContains("写入已开") {
			t.Errorf("名单里的缺库角色建成后该开写：%v", e.logSnapshot())
		}
		if e.logContains("no_memory_db") {
			t.Errorf("自动建库以后不该再有 no_memory_db：%v", e.logSnapshot())
		}
		if !e.logContains("unknown_character") {
			t.Errorf("不存在的角色该有一行 unknown_character：%v", e.logSnapshot())
		}
		for _, l := range e.logSnapshot() {
			if strings.Contains(l, "unknown_character") && strings.Contains(l, e.data) {
				t.Errorf("提醒里不该带本机路径：%q", l)
			}
		}
	})
}

// 桌面记忆页说"攒够就会自己提升"要和写入侧实际做的一致（复核 2026-11-02）：配置写着 autoConfirm 开，
// 但整理没开成（只开 remember、没配后台模型）或根本没开写入时，详情里 Auto 为假。三格各开一次真的装配，经桌面读服务读详情
func TestDesktopMemoryAutoConfirmFollowsWriteSide(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, write string
		model       bool
		want        bool
	}{
		{"两档都开且 autoConfirm：会提升", "characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: latest\n    autoConfirm: true\n", true, true},
		{"autoConfirm 开但只开了 remember：不会提升", "characters: [" + testChar + "]\n    autoConfirm: true\n", true, false},
		{"整理列了但没配后台模型退成只开 remember：不会提升", "characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: latest\n    autoConfirm: true\n", false, false},
		{"整理开了但 autoConfirm 没开：不会提升", "characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    catchUp: latest\n", true, false},
		{"写入一档都没开：不会提升", "autoConfirm: true\n", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			file := goMemoryDB(t, e, testChar)
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO facts (id, kind, text, subject, visibility, status, created_at) VALUES ('C1', 'fact', '编的待确认事实', 'owner', 'private', 'candidate', 2000)`); err != nil {
				t.Fatal(err)
			}
			db.Close()
			backup := filepath.Join(t.TempDir(), "备份")
			cfg := "backup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    " + c.write
			if c.model {
				cfg = "defaults:\n  backgroundModel: " + testModel + "\n" + cfg
			}
			e.configure(cfg)
			a := e.open()
			t.Cleanup(a.cleanup)
			if got := a.autoConfirming(testChar); got != c.want {
				t.Errorf("a.autoConfirming = %v，该是 %v", got, c.want)
			}
			d := a.Desktop()
			t.Cleanup(d.Close)
			r, err := d.Memory.ReadMemoryDetail(context.Background(), testChar, "f:C1")
			if err != nil || r.Value.Occasions == nil {
				t.Fatalf("读详情：%v %+v", err, r.Value.Occasions)
			}
			if r.Value.Occasions.Auto != c.want {
				t.Errorf("详情的 Auto = %v，该是 %v", r.Value.Occasions.Auto, c.want)
			}
		})
	}
	// 没挂记忆的角色 / 没打开过记忆的 App（只看历史）：否
	if (&App{}).autoConfirming("nobody") {
		t.Error("没挂记忆该是否")
	}
}

// 真实形状（复核 2026-11-16）：两张卡，memory.write 的 characters / summarize 都只列 A，places 只有
// owner-private。A 的 remember 开；另一张卡挂上但只读，日志写 not_listed；A 的整理没开，因为测试里没配后台模型
func TestOpenMemoriesRealShapeOnlyListedWritable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	goMemoryDB(t, e, testChar)
	cardOnly(t, e, "other")
	backup := filepath.Join(t.TempDir(), "备份")
	e.configure("backup:\n  dir: '" + yamlPath(backup) + "'\nmemory:\n  write:\n    characters: [" + testChar + "]\n    summarize: [" + testChar + "]\n    places: [owner-private]\n")
	a := e.open()
	t.Cleanup(a.cleanup)
	if m := a.memories[testChar]; m == nil || !m.Writable() || m.SummarizeEnabled() {
		t.Fatalf("列了的角色该开 remember、整理因没有后台模型关：%+v", m)
	}
	if !e.logContains("no_background_model") {
		t.Errorf("整理没开该说原因 no_background_model：%v", e.logSnapshot())
	}
	if m := a.memories["other"]; m == nil || m.Writable() {
		t.Fatalf("没列的角色该挂上但只读：%+v", m)
	}
	found := false
	for _, l := range e.logSnapshot() {
		if strings.Contains(l, "记忆[other]：写入保持只读") && strings.Contains(l, "not_listed") {
			found = true
		}
	}
	if !found {
		t.Errorf("没列的角色该有一行 not_listed：%v", e.logSnapshot())
	}
}
