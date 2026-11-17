package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
)

// -data 指到临时目录、配置却读到真实的 backup.dir：数据目录的真实路径在系统临时目录下，一律放数据目录旁边，并记一条 Warn
// （测试里的 t.TempDir() 就在系统临时目录下，所以这里不用环境变量也能触发）
func TestBackupDirFollowsDataUnderSystemTemp(t *testing.T) {
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(base, "主人真实的备份")
	cfg := config.Defaults()
	cfg.Backup.Dir = configured
	var logs []string
	a := &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Archive, Logf: func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }}} // BackupTempRoots 不写 = 系统临时目录
	a.openBackup()
	dir, err := a.backup.Dir()
	if err != nil || dir != filepath.Join(base, "Sumika-备份") {
		t.Fatalf("数据目录在系统临时目录下：备份该放旁边，不用配置的：%s %v", dir, err)
	}
	if _, err := a.backup.Dir(); err != nil { // 再取一次：Warn 只记一次
		t.Fatal(err)
	}
	warns := 0
	for _, l := range logs {
		if strings.Contains(l, "数据目录是临时目录") && strings.Contains(l, "不用配置里的 backup.dir") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("该记一条（只一条）Warn，说明配置里的 backup.dir 被盖掉了：%d %v", warns, logs)
	}
}
