package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
)

func dirsHave(dirs []string, want string) bool {
	for _, d := range dirs {
		if strings.EqualFold(filepath.Clean(d), filepath.Clean(want)) {
			return true
		}
	}
	return false
}

// 装配：后台建好以后，审批门的受保护目录里有备份目录（连真实路径）。备份目录里是数据目录的完整副本，读它不能比读数据目录容易
func TestBackupDirIsProtectedInApprovalGate(t *testing.T) {
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	dir, err := a.backup.Dir()
	if err != nil {
		t.Fatal(err)
	}
	g := a.approvalGate.ProtectedDirs
	if !dirsHave(g, dir) || !dirsHave(g, approval.RealOf(dir)) {
		t.Fatalf("受保护目录里该有备份目录 %s 和它的真实路径：%v", dir, g)
	}
	if !dirsHave(g, a.paths.Root) {
		t.Fatalf("数据目录照旧受保护：%v", g)
	}
}

// 配置里写的 backup.dir：就算被临时数据目录的规则盖掉了（备份实际放在旁边）、写进了数据目录里被拒绝、相对路径、还不存在，
// 配置里写的原样路径和默认位置都照样受保护
func TestProtectBackupDirCoversConfiguredAndDefaultPaths(t *testing.T) {
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	for name, cfgDir := range map[string]string{
		"不存在的绝对路径":    filepath.Join(base, "主人真实的备份"),
		"相对路径":        "相对备份",
		"配错：写进了数据目录里": filepath.Join(p.Root, "里面"),
		"没写":          "",
	} {
		cfg := config.Defaults()
		cfg.Backup.Dir = cfgDir
		a := &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Receive, Logf: func(string, ...any) {}}}
		a.openBackup()
		a.approvalGate = approval.NewGate(p.Root)
		a.protectBackupDir()
		got := a.approvalGate.ProtectedDirs
		a.approvalGate.Analyzer.Close()
		if !dirsHave(got, p.BackupRoot()) {
			t.Errorf("%s：默认位置该受保护：%v", name, got)
		}
		switch {
		case cfgDir == "":
		case filepath.IsAbs(cfgDir):
			if !dirsHave(got, cfgDir) {
				t.Errorf("%s：配置里写的路径该受保护：%v", name, got)
			}
		default:
			if !dirsHave(got, filepath.Join(base, cfgDir)) {
				t.Errorf("%s：相对路径按数据目录的上一级解，该受保护：%v", name, got)
			}
		}
	}
}
