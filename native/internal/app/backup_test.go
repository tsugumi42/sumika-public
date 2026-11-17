package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/backup"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 装配：真的 store.Set 开在临时数据目录里，备份服务登记它的各平台聊天库、trace 库和表情包来源；
// 手动备份做出来的清单里这些库是“持有方登记”的，表情包只带 emoji 段、头像登记里的文件，普通图片不带。
func TestBackupWiringWithRealStores(t *testing.T) {
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
	const (
		hEmoji = "aa11111111111111111111111111111111111111111111111111111111111111"
		hImage = "bb22222222222222222222222222222222222222222222222222222222222222"
	)
	qq, ok := set.Platform("qq")
	if !ok {
		t.Fatal("该有 qq 平台的库")
	}
	if _, err := qq.Events.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, segments_json, created_at) VALUES ('e1', 'qq', 'qq:group:1', 'user', 'u', 'message', '', ?, 1)`,
		`{"segs":[{"kind":"emoji","hash":"`+hEmoji+`"},{"kind":"image","hash":"`+hImage+`"}]}`); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{hEmoji, hImage} {
		f := filepath.Join(p.GoMediaDir(), "qq", h[:2], h)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"+h[:2]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults()
	cfg.Backup.Dir = filepath.Join(base, "备份")
	cfg.Backup.DailyMedia = "stickers"
	a := &App{paths: p, cfg: cfg, set: set, now: time.Now, opt: Options{Mode: Receive, BackupTempRoots: []string{}, Logf: func(string, ...any) {}}}
	a.openBackup()
	defer a.closeBackup()

	out, err := a.BackupNow(context.Background(), "", 60*time.Second)
	if err != nil || !out.Settled || out.Status != "ok" {
		t.Fatalf("手动备份该做完并通过：%+v %v", out, err)
	}
	dir, _ := a.backup.Dir()
	if dir != cfg.Backup.Dir {
		t.Fatalf("备份目录该用配置里的：%s", dir)
	}
	entries, _ := backup.Scan(dir)
	if len(entries) != 1 {
		t.Fatalf("%v", entries)
	}
	m, err := backup.ReadManifest(entries[0].Folder)
	if err != nil {
		t.Fatal(err)
	}
	if m.Reason != backup.ReasonManual {
		t.Fatalf("原因该是手动：%s", m.Reason)
	}
	methods := map[string]string{}
	for _, d := range m.DBs {
		methods[d.Path] = d.Method
		if d.Integrity != "ok" {
			t.Errorf("%s 没通过：%s", d.Path, d.Integrity)
		}
	}
	for _, rel := range []string{"go/chat/qq.db", "go/chat/local.db", "go/trace.db"} {
		if methods[rel] != backup.MethodHolder {
			t.Errorf("%s 该是持有方登记的快照：%v", rel, methods)
		}
	}
	var media []string
	for _, f := range m.Files {
		if f.Class == backup.ClassMedia {
			media = append(media, f.Path)
		}
	}
	if len(media) != 1 || !strings.HasSuffix(media[0], hEmoji) {
		t.Fatalf("stickers 档只该带 emoji 段的那个文件：%v", media)
	}

	// 总览和列表
	l, err := a.BackupList(context.Background())
	if err != nil || len(l.Rows) != 1 || l.Overview.Total != 1 || l.Overview.Manual != 1 || l.Overview.UsedBytes <= 0 || !l.Overview.FreeKnown || l.Rows[0].Reason != "manual" || l.Rows[0].DBs != len(m.DBs) {
		t.Fatalf("%+v %v", l, err)
	}
	// 自检读的状态：每天备份开着，没做过每天备份，进程刚起：不报警
	if st := a.backupSelfcheck(); !st.Enabled {
		t.Fatalf("%+v", st)
	}
}

func TestBackupNowReportsBusyAndBadDir(t *testing.T) {
	base := t.TempDir()
	p, _ := paths.Init(filepath.Join(base, "data"))
	cfg := config.Defaults()
	cfg.Backup.Dir = filepath.Join(p.Root, "里面") // 指到数据目录里面
	a := &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Archive, BackupTempRoots: []string{}, Logf: func(string, ...any) {}}}
	a.openBackup()
	if _, err := a.BackupNow(context.Background(), "", time.Second); err == nil || !strings.Contains(err.Error(), "数据目录里面") {
		t.Fatalf("备份目录指进数据目录该拒绝并说明：%v", err)
	}
	l, err := a.BackupList(context.Background())
	if err != nil || l.Overview.DirError == "" {
		t.Fatalf("列表该报目录有问题：%+v %v", l.Overview, err)
	}
	var nilApp *App
	if _, err := nilApp.BackupNow(context.Background(), "", 0); err != ErrNoBackup {
		t.Fatal(err)
	}
}

// SUMIKA_DATA_DIR 有值（测试、冒烟检查指向了临时数据目录）：备份一律放在数据目录旁边，不管配置里 backup.dir 写的是哪
// （免得临时数据混进真实的备份目录）；没设就用配置里的。注意 -data 参数、Options.DataDir 不算：这条规则只认环境变量，
// 用 -data 指向临时目录的人要自己保证配置里的 backup.dir 不是真实的备份目录
func TestBackupDirFollowsTempDataEnv(t *testing.T) {
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(base, "主人真实的备份")
	cfg := config.Defaults()
	cfg.Backup.Dir = configured
	mk := func() string {
		a := &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Archive, BackupTempRoots: []string{}, Logf: func(string, ...any) {}}}
		a.openBackup()
		dir, err := a.backup.Dir()
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if got := mk(); got != configured {
		t.Fatalf("没设环境变量时用配置里的：%s", got)
	}
	t.Setenv("SUMIKA_DATA_DIR", p.Root)
	if got := mk(); got != filepath.Join(base, "Sumika-备份") {
		t.Fatalf("设了环境变量时一律放数据目录旁边：%s", got)
	}
}
