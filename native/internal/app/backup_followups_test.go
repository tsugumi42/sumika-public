package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/backup"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/policy"
	"sumika-native/internal/testutil/volpath"
)

// bareBackupApp 是只带路径、配置、备份服务和审批门的最小 App：数据目录不当临时目录（TempRoots 给空），这样配置里的 backup.dir 才生效，
// 走的是“非临时数据目录下 Dir() 解出配置路径”这条腿。warns 收 Warn 日志
func bareBackupApp(t *testing.T, mut func(base string, cfg *config.Config)) (a *App, base string, warns *[]string) {
	t.Helper()
	base = t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if mut != nil {
		mut(base, &cfg)
	}
	var logs []string
	a = &App{paths: p, cfg: cfg, now: time.Now, opt: Options{Mode: Receive, BackupTempRoots: []string{}, Logf: func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }}}
	a.openBackup()
	a.approvalGate = approval.NewGate(p.Root)
	t.Cleanup(func() { a.approvalGate.Analyzer.Close() })
	return a, base, &logs
}

// 并进去以后，项目说明的读取批准拒读备份目录里的文件；并进去之前（对照）放行。读取批准拿的是取清单的函数，
// 在建它之后才并进去的目录也算数（不是建它那一刻的快照）
func TestReadApprovalRefusesBackupDirAfterProtect(t *testing.T) {
	a, _, _ := bareBackupApp(t, func(base string, cfg *config.Config) { cfg.Backup.Dir = filepath.Join(base, "独立备份") })
	dir, err := a.backup.Dir()
	if err != nil {
		t.Fatal(err)
	}
	inBackup := filepath.Join(dir, "每天-20261114-0622", "data", "config", "sumika.yml")
	approve := a.projectReadApproval() // 先建好，再并目录
	if err := approve(context.Background(), inBackup); err != nil {
		t.Fatalf("前提：没并进去时读备份目录会放行：%v", err)
	}
	a.protectBackupDir()
	if err := approve(context.Background(), inBackup); err == nil || !strings.Contains(err.Error(), "审批未通过") {
		t.Fatalf("并进去以后读取批准该拒绝备份目录里的文件：%v", err)
	}
	if err := approve(context.Background(), filepath.Join(t.TempDir(), "AGENTS.md")); err != nil {
		t.Fatalf("别处的项目说明不受影响：%v", err)
	}
}

// 非临时数据目录下 Dir() 那条腿：服务在配置读到时建好、之后配置里的路径变了（或被清空），受保护的仍有服务实际在用的目录，
// 不只是配置里现在写的
func TestProtectBackupDirUsesServiceDirOnNonTempData(t *testing.T) {
	a, base, _ := bareBackupApp(t, func(base string, cfg *config.Config) { cfg.Backup.Dir = filepath.Join(base, "独立备份") })
	want := filepath.Join(base, "独立备份")
	dir, err := a.backup.Dir()
	if err != nil || dir != want {
		t.Fatalf("前提：非临时数据目录下 Dir() 该是配置里的路径：%q %v", dir, err)
	}
	a.cfg.Backup.Dir = "" // 配置读到别处去了；服务还在往原来的目录写
	a.protectBackupDir()
	got := a.approvalGate.ProtectedDirs
	if !dirsHave(got, want) {
		t.Fatalf("服务实际在用的目录该受保护：%v", got)
	}
	if !dirsHave(got, a.paths.BackupRoot()) {
		t.Fatalf("默认位置也该受保护：%v", got)
	}
}

// 配置里 backup.dir 写成 \\?\D:\x、\\.\D:\x：并进去的是还原后的 D:\x，不是带前缀的原样（带前缀的和规整过的路径比不上，保护就漏了）
func TestProtectBackupDirStripsDevicePrefix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("设备路径前缀是 Windows 的写法")
	}
	for _, prefix := range []string{`\\?\`, `\\.\`} {
		var plain string
		a, _, _ := bareBackupApp(t, func(base string, cfg *config.Config) {
			plain = filepath.Join(base, "带前缀的备份")
			cfg.Backup.Dir = prefix + plain
		})
		a.protectBackupDir()
		got := a.approvalGate.ProtectedDirs
		if !dirsHave(got, plain) {
			t.Errorf("%s：该并进还原后的 %s：%v", prefix, plain, got)
		}
		for _, d := range got {
			if strings.HasPrefix(d, `\\`) {
				t.Errorf("%s：不该并进带前缀的原样：%s", prefix, d)
			}
		}
		file := filepath.Join(plain, "每天-1", "data", "config", "sumika.yml")
		if !policy.TouchesProtected(file, filepath.Dir(file), got, false) {
			t.Errorf("%s：备份目录里的文件该算碰到受保护目录", prefix)
		}
		if dir, err := a.backup.Dir(); err != nil || dir != plain {
			t.Errorf("%s：服务算出的目录该是还原后的：%q %v", prefix, dir, err)
		}
	}
}

// 配置里 backup.dir 是盘符根或数据目录的上级：备份用不了它（ResolveDir 拒绝：ErrDirTooBroad），也不并进受保护目录
// （并进去整个盘 / 整个项目都变成每次问），记一条 Warn；默认位置照常保护，别处的文件不受影响
func TestProtectBackupDirSkipsTooBroadDirs(t *testing.T) {
	for name, pick := range map[string]func(base string) string{
		"数据目录的上级": func(base string) string { return base },
		"盘符根":     func(base string) string { return filepath.VolumeName(base) + string(filepath.Separator) },
	} {
		t.Run(name, func(t *testing.T) {
			var broad string
			a, base, warns := bareBackupApp(t, func(base string, cfg *config.Config) {
				broad = pick(base)
				cfg.Backup.Dir = broad
			})
			if _, err := a.backup.Dir(); !errors.Is(err, backup.ErrDirTooBroad) {
				t.Fatalf("前提：该拒绝为太宽：%v", err)
			}
			a.protectBackupDir()
			got := a.approvalGate.ProtectedDirs
			if dirsHave(got, broad) {
				t.Fatalf("太宽的目录不该并进受保护目录：%v", got)
			}
			if !dirsHave(got, a.paths.BackupRoot()) {
				t.Fatalf("默认位置照常保护：%v", got)
			}
			if err := a.projectReadApproval()(context.Background(), filepath.Join(base, "别的项目", "AGENTS.md")); err != nil {
				t.Fatalf("盘上别的项目不该因此被拒：%v", err)
			}
			if !strings.Contains(strings.Join(*warns, "\n"), "盘符根或数据目录的上级") {
				t.Fatalf("该记一条说明原因的 Warn：%v", *warns)
			}
		})
	}
}

// 记忆开写前的 memory.db 副本放在 backup.ResolveDir 的结果里：相对路径按数据目录上一级解，不是原样的相对路径；配错了（在数据目录里 / 太宽）算不出来，
// 连同原因交给 memwrite（它不开写，不会把副本放进数据目录或到处乱放）
func TestMemoryWriteInputUsesResolvedBackupDir(t *testing.T) {
	a, base, _ := bareBackupApp(t, func(base string, cfg *config.Config) { cfg.Backup.Dir = "相对备份" })
	dir, err := a.backupDirForCopies()
	if err != nil || dir != filepath.Join(base, "相对备份") {
		t.Fatalf("相对路径该按数据目录的上一级解：%q %v", dir, err)
	}
	for name, cfgDir := range map[string]string{"写进数据目录里": filepath.Join(base, "data", "备份"), "数据目录的上级": base} {
		a.backup = nil // 换一份配置重新建
		a.cfg.Backup.Dir = cfgDir
		a.openBackup()
		if d, err := a.backupDirForCopies(); err == nil {
			t.Errorf("%s：该算不出来：%q", name, d)
		}
	}
}

// 整个后台装起来以后，memwrite 拿到的备份目录就是每天备份在用的那个（配置没写 = 数据目录旁边的默认位置，不是空串）
func TestMemoryWriteInputWiredToBackupService(t *testing.T) {
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	dir, err := a.backup.Dir()
	if err != nil {
		t.Fatal(err)
	}
	in := a.memoryWriteInput("example", a.paths.MemoryDB("example"))
	if in.BackupDir != dir || in.BackupDirErr != nil {
		t.Fatalf("memwrite 该拿到每天备份的目录 %s：%q %v", dir, in.BackupDir, in.BackupDirErr)
	}
}

// backup.dir 写成卷标识写法（\\?\Volume{GUID}\x）：它规整不成 D:\x，审批门自己解不了（不透明路径不解析），
// 所以 protectBackupDir 要把解出来的真实路径明着并进去；读真实路径下的文件、读卷标识写法下的文件都要问
func TestProtectBackupDirVolumeGUIDPath(t *testing.T) {
	var guid, skip string
	a, base, _ := bareBackupApp(t, func(base string, cfg *config.Config) {
		plain := filepath.Join(base, "卷写法备份")
		if err := os.MkdirAll(filepath.Join(plain, "每天-1", "data", "config"), 0o755); err != nil {
			skip = err.Error()
			return
		}
		g, err := volpath.GUIDPath(plain)
		if err != nil {
			skip = err.Error()
			return
		}
		guid = g
		cfg.Backup.Dir = g
	})
	if skip != "" {
		t.Skipf("拼不出卷标识写法：%s", skip)
	}
	baseReal, err := paths.RealPath(base)
	if err != nil {
		t.Fatal(err)
	}
	plainReal := filepath.Join(baseReal, "卷写法备份")
	a.protectBackupDir()
	got := a.approvalGate.ProtectedDirs
	if !dirsHave(got, plainReal) {
		t.Fatalf("卷标识写法的备份目录，解出来的真实路径 %s 该并进受保护目录：%v", plainReal, got)
	}
	file := filepath.Join(plainReal, "每天-1", "data", "config", "sumika.yml")
	if err := a.projectReadApproval()(context.Background(), file); err == nil || !strings.Contains(err.Error(), "审批未通过") {
		t.Fatalf("读备份目录里的文件该被拒：%v", err)
	}
	guidFile := filepath.Join(guid, "每天-1", "data", "config", "sumika.yml")
	if !policy.TouchesProtected(file, filepath.Dir(file), got, false) || !policy.TouchesProtected(guidFile, filepath.Dir(file), got, false) {
		t.Fatal("两种写法读备份目录里的文件，审批门都该算碰到受保护目录")
	}
	if err := a.projectReadApproval()(context.Background(), filepath.Join(baseReal, "别处", "AGENTS.md")); err != nil {
		t.Fatalf("别处的文件不受影响：%v", err)
	}
}

// 备份目录太宽（盘符根、数据目录的上级）时，它本身不并进受保护目录，但里面本程序以前写的备份文件夹逐个并进去
// （旧备份里有聊天库快照和明文配置）；别的文件夹、别的文件不受影响
func TestProtectBackupDirTooBroadStillProtectsOldBackups(t *testing.T) {
	// 先在一个正常的备份目录里做出一份真备份，搬到“太宽的目录”里，当作配置改坏之前留下的旧备份
	a, base, _ := bareBackupApp(t, func(base string, cfg *config.Config) {
		cfg.Backup.Dir = filepath.Join(base, "原来的备份")
		cfg.Backup.MinFreeGB = 0
	})
	res, err := a.backup.Run(context.Background(), backup.Request{Reason: backup.ReasonManual})
	if err != nil {
		t.Fatalf("前提：做一份备份：%v", err)
	}
	old := filepath.Join(base, filepath.Base(res.Folder)) // base 是数据目录的上级
	if err := os.Rename(res.Folder, old); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "别的项目")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	// 配置改成了数据目录的上级
	a.cfg.Backup.Dir = base
	a.backup = nil
	a.openBackup()
	var warns []string
	a.opt.Logf = func(f string, args ...any) { warns = append(warns, fmt.Sprintf(f, args...)) }
	a.protectBackupDir()
	got := a.approvalGate.ProtectedDirs
	if dirsHave(got, base) {
		t.Fatalf("太宽的目录本身不该并进去：%v", got)
	}
	if !dirsHave(got, old) {
		t.Fatalf("里面以前写的备份文件夹 %s 该并进去：%v", old, got)
	}
	appr := a.projectReadApproval()
	if err := appr(context.Background(), filepath.Join(old, "data", "config", "sumika.yml")); err == nil {
		t.Fatal("读旧备份文件夹里的文件该被拒")
	}
	if err := appr(context.Background(), filepath.Join(other, "AGENTS.md")); err != nil {
		t.Fatalf("太宽目录里别的项目不受影响：%v", err)
	}
}

// 备份目录算不出来时，记忆开写前的 Warn 同一个原因只记一次（每个角色、每次装配都会问，原样都记会刷屏），
// 文案写明是哪种原因；换一种原因再记一次
func TestBackupDirForCopiesWarnsOncePerReason(t *testing.T) {
	a, base, warns := bareBackupApp(t, func(base string, cfg *config.Config) { cfg.Backup.Dir = filepath.Join(base, "data", "备份") })
	for range 3 {
		if _, err := a.backupDirForCopies(); err == nil {
			t.Fatal("前提：写进数据目录里该算不出来")
		}
	}
	count := func(sub string) int {
		n := 0
		for _, w := range *warns {
			if strings.Contains(w, sub) {
				n++
			}
		}
		return n
	}
	if count("记忆开写前的备份没地方放") != 1 || count("写在了数据目录里面") != 1 {
		t.Fatalf("同一个原因该只记一次且说明原因：%v", *warns)
	}
	a.backup = nil
	a.cfg.Backup.Dir = base // 数据目录的上级：换了一种原因
	a.openBackup()
	for range 2 {
		_, _ = a.backupDirForCopies()
	}
	if count("记忆开写前的备份没地方放") != 2 || count("是盘符根或数据目录的上级") != 1 {
		t.Fatalf("换一种原因再记一次：%v", *warns)
	}
}
