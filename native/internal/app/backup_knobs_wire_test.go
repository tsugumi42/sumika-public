package app

import (
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
)

// 配置到服务、服务到自检的接线：runMaxHours: 12、closeWaitSeconds: 10、minFreeGB: 2 建出来的服务就是这些值，
// S9 拿到的 RunMax 也是 12 小时（配置 → Options → 服务 → 自检状态 四步里错一步都要红）
func TestBackupKnobsWiredFromConfigToServiceAndSelfcheck(t *testing.T) {
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Backup.RunMaxHours = 12
	cfg.Backup.CloseWaitSeconds = 10
	cfg.Backup.MinFreeGB = 2
	a := &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Receive, Logf: func(string, ...any) {}}}
	a.openBackup()
	runMax, closeWait, minFree := a.backup.Limits()
	if runMax != 12*time.Hour || closeWait != 10*time.Second || minFree != 2<<30 {
		t.Fatalf("服务拿到的限制不对：runMax=%v closeWait=%v minFree=%d", runMax, closeWait, minFree)
	}
	if st := a.backupSelfcheck(); st.RunMax != 12*time.Hour {
		t.Fatalf("S9 拿到的 RunMax 该是 12 小时：%v", st.RunMax)
	}
}
