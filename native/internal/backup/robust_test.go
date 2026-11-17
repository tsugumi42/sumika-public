package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/sqlitesnap"
)

func dirEntries(t *testing.T, f *fx) []string {
	t.Helper()
	ents, err := os.ReadDir(f.dir())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// 改名失败：整份清掉，不留一份整库的拷贝等着堵下一次
func TestRunRenameFailureCleansStaging(t *testing.T) {
	f := newFx(t)
	f.svc.rename = func(from, to string) error { return errors.New("模拟：被杀毒软件占着，改不了名") }
	if _, err := f.run(ReasonDaily); err == nil || !strings.Contains(err.Error(), "改不了名") {
		t.Fatalf("该报改不了名：%v", err)
	}
	if got := dirEntries(t, f); len(got) != 0 {
		t.Fatalf("失败以后不该留下 .partial：%v", got)
	}
}

// 写最终清单失败（多半盘满了）：同样整份清掉
func TestRunManifestWriteFailureCleansStaging(t *testing.T) {
	f := newFx(t)
	calls := 0
	f.svc.writeManifest = func(folder string, m Manifest) error {
		calls++
		if calls >= 2 { // 第一次是开工时的 partial 清单
			return errors.New("模拟：磁盘满了")
		}
		return writeManifest(folder, m)
	}
	if _, err := f.run(ReasonDaily); err == nil || !strings.Contains(err.Error(), "写不了清单") {
		t.Fatalf("%v", err)
	}
	if got := dirEntries(t, f); len(got) != 0 {
		t.Fatalf("失败以后不该留下 .partial：%v", got)
	}
}

// 每天备份失败以后退避：不每小时重试
func TestTickBacksOffAfterFailure(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { return errors.New("模拟：取不到快照") }
	if ran, err := f.svc.Tick(context.Background()); !ran || err == nil {
		t.Fatalf("第一次该试并失败：%v %v", ran, err)
	}
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { return sqlitesnap.Into(ctx, db, dst) }
	f.clock.Add(time.Hour)
	if ran, _ := f.svc.Tick(context.Background()); ran {
		t.Fatal("刚失败 1 小时，不该再试")
	}
	f.clock.Add(RetryAfter)
	if ran, err := f.svc.Tick(context.Background()); !ran || err != nil {
		t.Fatalf("退避过了该再试：%v %v", ran, err)
	}
}

// Close 不无限等：正在做的那份不理会取消时，Close 在 CloseWait 以后照样返回
func TestCloseDoesNotWaitForeverOnStuckRun(t *testing.T) {
	f := newFx(t, func(o *Options) { o.CloseWait = 150 * time.Millisecond })
	started := make(chan struct{})
	release := make(chan struct{})
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		close(started)
		<-release // 不看 ctx：模拟卡在一个读不动的文件上
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.run(ReasonDaily); done <- err }()
	<-started
	t0 := time.Now()
	f.svc.Close()
	if took := time.Since(t0); took > 5*time.Second {
		t.Fatalf("Close 等了 %v，不该无限等", took)
	}
	close(release)
	<-done
}

// Close 和一份刚开始的备份赶在一起：登记取消函数和 Close 取消之间的空档里，这份也要被取消
func TestRunStartedJustAfterCloseIsCancelled(t *testing.T) {
	f := newFx(t)
	f.svc.closed.Store(true) // Close 已经记了 closed，但还没来得及看到这份的取消函数
	_, err := f.svc.runLocked(context.Background(), Request{Reason: ReasonDaily})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("该被取消：%v", err)
	}
	if got := dirEntries(t, f); len(got) != 0 {
		t.Fatalf("不该留下东西：%v", got)
	}
}

// 读很大的文件时取消也要马上停：复制和算哈希都按块看 ctx
func TestCopyVerifiedStopsOnCancel(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big.bin")
	write(t, src, strings.Repeat("x", 1<<20))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := filepath.Join(t.TempDir(), "out.bin")
	if _, _, err := copyVerified(ctx, src, dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("该被取消：%v", err)
	}
	if exists(dst) {
		t.Fatal("取消以后不该留下半个文件")
	}
	if _, _, err := sha256FileCtx(ctx, src); !errors.Is(err, context.Canceled) {
		t.Fatalf("算哈希也该被取消：%v", err)
	}
}

func TestRotateAndUsageStopOnCancel(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	fabricate(t, dir, "每天-20260101-0000", ReasonDaily, StatusOK, now.AddDate(0, 0, -100))
	fabricate(t, dir, "每天-20260102-0000", ReasonDaily, StatusOK, now.AddDate(0, 0, -99))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := RotateCtx(ctx, dir, now, false, testSource)
	if err != nil || len(res.Deleted) != 0 || len(res.Failed) == 0 {
		t.Fatalf("取消了就不该再删：%v %+v", err, res)
	}
	if got := remaining(t, dir); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if n := UsageCtx(ctx, dir); n != 0 {
		t.Fatalf("取消了就不该再数：%d", n)
	}
}

// 备份里出了意外（panic）：记成这次失败，不带垮后台
func TestRunPanicBecomesFailure(t *testing.T) {
	f := newFx(t)
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { panic("模拟：驱动里炸了") }
	_, err := f.run(ReasonDaily)
	if err == nil || !strings.Contains(err.Error(), "出了意外") {
		t.Fatalf("%v", err)
	}
	if st := f.svc.Status(); st.Running || st.LastFailure == "" {
		t.Fatalf("状态该是没在做、记着失败：%+v", st)
	}
	if got := dirEntries(t, f); len(got) != 0 {
		t.Fatalf("panic 以后不该留下 .partial：%v", got)
	}
	// 之后照常能做
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { return sqlitesnap.Into(ctx, db, dst) }
	f.clock.Add(time.Minute)
	f.mustRun(ReasonManual)
}

func TestSafeTickRecoversPanic(t *testing.T) {
	var warned []string
	f := newFx(t, func(o *Options) { o.Warnf = func(format string, args ...any) { warned = append(warned, format) } })
	f.svc.opt.Busy = func() bool { panic("模拟：busy 回调炸了") }
	f.svc.safeTick(context.Background())
	if len(warned) != 1 || !strings.Contains(warned[0], "出了意外") {
		t.Fatalf("该只记日志：%v", warned)
	}
}

// 备份前查目标盘剩余空间：不够就不开工，记 bad 点名原因；负数不查
func TestRunRefusesWhenDiskTooSmall(t *testing.T) {
	f := newFx(t, func(o *Options) { o.MinFreeBytes = 1 << 62 })
	res, err := f.run(ReasonManual)
	if err == nil || res.Status != StatusBad || !strings.Contains(strings.Join(res.Problems, ""), "剩余空间不够") {
		t.Fatalf("%v %+v", err, res)
	}
	if exists(filepath.Join(res.Folder, "data")) {
		t.Fatal("没开工，不该有副本")
	}
	g := newFx(t, func(o *Options) { o.MinFreeBytes = -1 })
	g.mustRun(ReasonManual)
}

// 到点了但有角色在回复：状态记“先等”（自检读它，不当异常）
func TestTickMarksDeferredWhileBusy(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	f.mustRun(ReasonDaily)
	f.clock.Add(21 * time.Hour)
	f.busy = true
	if ran, _ := f.svc.Tick(context.Background()); ran {
		t.Fatal("该先等")
	}
	if !f.svc.Status().Deferred {
		t.Fatal("状态该记“先等”")
	}
	f.busy = false
	f.svc.Tick(context.Background())
	if f.svc.Status().Deferred {
		t.Fatal("做了以后该清掉")
	}
}
