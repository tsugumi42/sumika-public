package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/backup"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// 恢复成新目录和抽验的装配（规格-数据目录与备份 10.10）

// 只看历史（archive）模式也能恢复和抽验：读的是备份目录，写的是新目录，不碰那份历史；恢复前后现在的数据目录和配置一个字节没动，
// App 的数据目录、配置也没换（命令不切换）；做完以后总览里有“上次抽验”
func TestBackupVerifyAndRestoreInArchiveMode(t *testing.T) {
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	qq, _ := set.Platform("qq")
	if _, err := qq.Events.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, segments_json, created_at) VALUES ('e1', 'qq', 'qq:group:1', 'user', 'u', 'message', '你好', '{}', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("backup:\n  daily: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Backup.Dir = filepath.Join(base, "备份")
	a := &App{paths: p, cfg: cfg, set: set, now: time.Now, opt: Options{Mode: Archive, BackupTempRoots: []string{}, Logf: func(string, ...any) {}}}
	a.openBackup()
	defer a.closeBackup()
	ctx := context.Background()

	if out, err := a.BackupNow(ctx, "", time.Minute); err != nil || !out.Settled || out.Status != "ok" {
		t.Fatalf("先做一份手动备份：%+v %v", out, err)
	}
	// 备份期间入库的等待：装配把各平台聊天库的写锁探针登记进去了，清单里有这一项，backup list 的行里也带着
	entries, _ := a.backup.List()
	if len(entries) != 1 {
		t.Fatalf("%v", entries)
	}
	m, err := backup.ReadManifest(entries[0].Folder)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]backup.WriteWaitEntry{}
	for _, w := range m.WriteWait {
		got[w.Path] = w
	}
	for _, rel := range []string{"go/chat/qq.db", "go/chat/local.db"} {
		if w, ok := got[rel]; !ok || w.Samples < 1 || w.Failed != 0 {
			t.Errorf("清单里该有 %s 的写锁等待采样：%+v", rel, m.WriteWait)
		}
	}
	if _, ok := got["go/trace.db"]; ok {
		t.Error("只探聊天库（入库写的是它），trace 库不探")
	}
	if l, _ := a.BackupList(context.Background()); len(l.Rows) != 1 || l.Rows[0].WriteWaitSamples < 2 || l.Rows[0].WriteWaitFailed != 0 {
		t.Errorf("backup list 的行里该带写锁等待：%+v", l.Rows)
	}
	confBefore, _ := os.ReadFile(p.ConfigFile())
	rootBefore := a.paths.Root

	v, err := a.BackupVerify(ctx, "", time.Minute)
	if err != nil || !v.Settled || !v.OK || !v.Counts || v.Files == 0 || len(v.DBs) == 0 {
		t.Fatalf("archive 模式也能抽验：%+v %v", v, err)
	}
	for _, d := range v.DBs {
		if d.Integrity != "ok" || d.Rows != "match" {
			t.Errorf("库 %s 该核对通过：%+v", d.Path, d)
		}
	}
	to := filepath.Join(base, "恢复出的数据目录")
	r, err := a.BackupRestore(ctx, v.Name, to, time.Minute)
	if err != nil || !r.Settled || !r.OK || r.Target == "" || r.Files != v.Files {
		t.Fatalf("archive 模式也能恢复：%+v %v", r, err)
	}
	if _, err := paths.Open(to); err != nil {
		t.Fatalf("恢复出的目录该能当数据目录打开：%v", err)
	}
	if now, _ := os.ReadFile(p.ConfigFile()); string(now) != string(confBefore) || a.paths.Root != rootBefore || a.cfg.Backup.Dir != cfg.Backup.Dir {
		t.Fatal("恢复不许改现在的数据目录、配置，也不许切换")
	}
	if rr, err := a.BackupRestore(ctx, v.Name, to, time.Minute); err == nil {
		t.Fatalf("目标目录已经有东西了该拒绝：%+v", rr)
	}
	if _, err := a.BackupRestore(ctx, "不存在", filepath.Join(base, "又一个"), time.Minute); err == nil {
		t.Fatal("备份名不对该拒绝")
	}
	l, err := a.BackupList(ctx)
	if err != nil || !l.Overview.Drill.Have || !l.Overview.Drill.OK || !l.Overview.Drill.HavePass || l.Overview.Drill.Backup != v.Name || l.Overview.Drill.Kind != backup.DrillManual {
		t.Fatalf("总览里该有上次抽验：%+v %v", l.Overview.Drill, err)
	}
	// 整个后台没建好
	var nilApp *App
	if _, err := nilApp.BackupRestore(ctx, "x", to, 0); err != ErrNoBackup {
		t.Fatalf("%v", err)
	}
	if _, err := nilApp.BackupVerify(ctx, "", 0); err != ErrNoBackup {
		t.Fatalf("%v", err)
	}
}

// 抽验的临时目录里是数据目录的完整副本：和备份目录一样并进审批门的受保护目录（目录固定，装配时就并得进去）
func TestDrillRootIsProtectedInApprovalGate(t *testing.T) {
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	root, err := a.backup.DrillRoot()
	if err != nil {
		t.Fatal(err)
	}
	g := a.approvalGate.ProtectedDirs
	if !dirsHave(g, root) {
		t.Fatalf("受保护目录里该有抽验的临时目录 %s：%v", root, g)
	}
	if dir, _ := a.backup.Dir(); strings.EqualFold(filepath.Clean(root), filepath.Clean(dir)) {
		t.Fatalf("抽验目录不能就是备份目录：%s", root)
	}
}

// 每周抽验跟着每天自动备份：装配把 Options.Drill 接到了 backup.daily（改了它，抽验的开关会变）
func TestDrillOptionFollowsDailyConfig(t *testing.T) {
	for _, daily := range []bool{true, false} {
		base := t.TempDir()
		p, _ := paths.Init(filepath.Join(base, "data"))
		cfg := config.Defaults()
		cfg.Backup.Daily = daily
		cfg.Backup.Dir = filepath.Join(base, "备份")
		a := &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Receive, BackupTempRoots: []string{}, Logf: func(string, ...any) {}}}
		a.openBackup()
		l, err := a.BackupList(context.Background())
		a.closeBackup()
		if err != nil || l.Overview.Drill.On != daily || l.Overview.DailyOn != daily {
			t.Fatalf("backup.daily=%v：抽验开关该跟着：%+v %v", daily, l.Overview, err)
		}
		if ds := a.drillSelfcheck(); ds.Enabled != daily {
			t.Fatalf("backup.daily=%v：自检读到的抽验开关：%+v", daily, ds)
		}
	}
}

// 恢复、抽验的入口只在主人的本地命令行（sumika-ctl，要主人点）：模型能调的工具清单里没有它们，四种场合都看一遍
func TestBackupRestoreIsNotAModelTool(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.cleanup()
	names := map[string]bool{}
	for name := range policy.Specs {
		names[name] = true
	}
	for _, aud := range []audience.Audience{
		{Channel: "console", Conversation: "internal:probe", Scene: "private", PrivateOK: true, OwnerAsked: true},
		{Channel: "console", Conversation: "local:group:x", Scene: "group", PrivateOK: true},
		{Channel: "qq", Conversation: "qq:private:10001", Scene: "private", PrivateOK: true, OwnerAsked: true},
		{Channel: "qq", Conversation: "qq:group:10001", Scene: "group"},
	} {
		for _, n := range a.hub.ToolNames(aud) {
			names[n] = true
		}
	}
	if len(names) < 10 {
		t.Fatalf("工具清单只认出 %d 个，枚举多半坏了：%v", len(names), names)
	}
	for n := range names {
		l := strings.ToLower(n)
		if strings.Contains(l, "drill") || (strings.Contains(l, "backup") && (strings.Contains(l, "restore") || strings.Contains(l, "verify"))) || strings.Contains(l, "restore_data") {
			t.Errorf("工具 %q 看起来能恢复备份或抽验：这两个入口只能在主人的本地命令行，要主人点", n)
		}
	}
}
