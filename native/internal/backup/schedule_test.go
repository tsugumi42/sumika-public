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
)

func TestDue(t *testing.T) {
	now := at(10, 12, 0)
	cases := []struct {
		name string
		last time.Time
		have bool
		busy bool
		want bool
	}{
		{"从没有过好的备份", time.Time{}, false, false, true},
		{"从没有过好的备份，忙也做（什么备份都没有最危险）", time.Time{}, false, true, true},
		{"刚做过", now.Add(-time.Hour), true, false, false},
		{"19 小时前", now.Add(-19 * time.Hour), true, false, false},
		{"21 小时前，不忙", now.Add(-21 * time.Hour), true, false, true},
		{"21 小时前，有角色在回复：等", now.Add(-21 * time.Hour), true, true, false},
		{"35 小时前，忙：还等", now.Add(-35 * time.Hour), true, true, false},
		{"37 小时前，忙：不再等，免得饿死", now.Add(-37 * time.Hour), true, true, true},
	}
	for _, c := range cases {
		if got := Due(now, c.last, c.have, c.busy); got != c.want {
			t.Errorf("%s：想要 %v，实际 %v", c.name, c.want, got)
		}
	}
}

// 时钟往回拨：上一份好的比现在还晚，不当“刚做过”
func TestDueClockMovedBack(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	ctx := context.Background()
	f.clock.Set(at(20, 5, 30))
	if ran, err := f.svc.Tick(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	f.clock.Set(at(20, 5, 30).AddDate(0, 0, -3)) // 时钟往回拨了 3 天
	if ran, err := f.svc.Tick(ctx); !ran || err != nil {
		t.Fatalf("上一份好的备份“在未来”，不能当刚做过：%v %v", ran, err)
	}
	if st := f.svc.Status(); !st.HaveGood || !st.LastGood.Equal(at(17, 5, 30)) {
		t.Fatalf("最近一份该是回拨后做的那份：%+v", st)
	}
}

func TestStatusIgnoresFutureBackup(t *testing.T) {
	f := newFx(t)
	fabricate(t, f.dir(), "每天-20261201-0530", ReasonDaily, StatusOK, at(1, 5, 30).AddDate(0, 2, 0))
	f.svc.refresh()
	if st := f.svc.Status(); st.HaveGood {
		t.Fatalf("时间在未来的备份不算最近一份：%+v", st)
	}
}

func TestLastGoodIgnoresForeignFolders(t *testing.T) {
	f := newFx(t)
	dir := f.dir()
	write(t, filepath.Join(dir, "每天-20261001-0500", ManifestFile), `{"version":"bun","reason":"daily","status":"ok"}`) // Bun 时代
	write(t, filepath.Join(dir, "每天-20261001-0400", "x.txt"), "没有清单")
	fabricate(t, dir, "每天-20261001-0300", ReasonDaily, StatusBad, at(1, 3, 0))
	fabricate(t, dir, "每天-20261001-0200", ReasonDaily, StatusPartial, at(1, 2, 0))
	fabricate(t, dir, "手动-20261001-0100", ReasonManual, StatusOK, at(1, 1, 0))
	f.svc.refresh()
	if st := f.svc.Status(); st.HaveGood || !st.Checked {
		t.Fatalf("没有清单的、bad、partial、手动的都不算每天的好备份：%+v", st)
	}
	fabricate(t, dir, "每天-20261001-0030", ReasonDaily, StatusOK, at(1, 0, 30))
	f.svc.refresh()
	if st := f.svc.Status(); !st.HaveGood || !st.LastGood.Equal(at(1, 0, 30)) {
		t.Fatalf("%+v", st)
	}
}

func TestListSkipsForeignAndBroken(t *testing.T) {
	f := newFx(t)
	dir := f.dir()
	write(t, filepath.Join(dir, "主人的东西", "a.txt"), "x")
	write(t, filepath.Join(dir, "每天-20261001-0500", ManifestFile), "")
	fabricate(t, dir, "每天-20261001-0300", ReasonDaily, StatusOK, at(1, 3, 0))
	fabricate(t, dir, "手动-20261001-0400", ReasonManual, StatusOK, at(1, 4, 0))
	es, err := f.svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := names(es); len(got) != 2 || got[0] != "手动-20261001-0400" || got[1] != "每天-20261001-0300" {
		t.Fatalf("只列认得出来的，新的在前：%v", got)
	}
}

// .partial 里就算清单写着 ok（改名前崩了），也不算好的
func TestPartialWithOKManifestIsNotGood(t *testing.T) {
	f := newFx(t)
	folder := fabricate(t, f.dir(), "每天-20261001-0300", ReasonDaily, StatusOK, at(1, 3, 0))
	if err := os.Rename(folder, folder+".partial"); err != nil {
		t.Fatal(err)
	}
	es, _ := Scan(f.dir())
	if len(es) != 1 || es[0].Manifest.Status != StatusPartial || !es[0].Partial {
		t.Fatalf("%+v", es)
	}
	f.svc.refresh()
	if f.svc.Status().HaveGood {
		t.Fatal("不算好的")
	}
}

func TestTickRunsOnlyWhenDue(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	ctx := context.Background()
	if ran, err := f.svc.Tick(ctx); !ran || err != nil {
		t.Fatal("第一次该做", ran, err)
	}
	f.clock.Add(time.Hour)
	if ran, _ := f.svc.Tick(ctx); ran {
		t.Fatal("1 小时后不该再做")
	}
	f.clock.Add(18 * time.Hour) // 19 小时
	if ran, _ := f.svc.Tick(ctx); ran {
		t.Fatal("19 小时不该再做")
	}
	f.clock.Add(2 * time.Hour) // 21 小时
	if ran, err := f.svc.Tick(ctx); !ran || err != nil {
		t.Fatal("21 小时该做", ran, err)
	}
}

// 重启不会让它多做：新建一个 Service（相当于重启）看备份目录里的清单，不看进程内的计时
func TestTickAfterRestartDoesNotRepeat(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	if ran, _ := f.svc.Tick(context.Background()); !ran {
		t.Fatal("第一次该做")
	}
	f.clock.Add(3 * time.Hour)
	restarted := New(f.opt)
	if ran, _ := restarted.Tick(context.Background()); ran {
		t.Fatal("重启以后 3 小时，不该再做一份")
	}
	if st := restarted.Status(); !st.HaveGood {
		t.Fatal("重启后该从目录里认出上一份")
	}
}

func TestTickDefersWhileAnswering(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	ctx := context.Background()
	f.mustRun(ReasonDaily)
	f.clock.Add(21 * time.Hour)
	f.busy = true
	if ran, _ := f.svc.Tick(ctx); ran {
		t.Fatal("有角色正在回复，先等")
	}
	f.busy = false
	if ran, err := f.svc.Tick(ctx); !ran || err != nil {
		t.Fatal("不忙了该做", ran, err)
	}
}

func TestTickDisabledDoesNothing(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Daily = false })
	if ran, _ := f.svc.Tick(context.Background()); ran {
		t.Fatal("backup.daily 关着不该自动做")
	}
	if st := f.svc.Status(); st.Enabled {
		t.Fatal("状态该是没开")
	}
	// 手动备份照常
	f.mustRun(ReasonManual)
}

func TestStatusFailedAttempt(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Dir = filepath.Join(t.TempDir(), "占着") })
	write(t, f.opt.Dir, "我是个文件") // 备份目录那个路径是个文件
	f.svc = New(f.opt)
	ran, err := f.svc.Tick(context.Background())
	if !ran || err == nil {
		t.Fatalf("该试过并失败：%v %v", ran, err)
	}
	st := f.svc.Status()
	if st.LastFailure == "" || st.HaveGood || st.FailedAt.IsZero() {
		t.Fatalf("状态该记着失败：%+v", st)
	}
}

func TestStartStopLoop(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone; o.StartDelay = time.Millisecond; o.Every = time.Hour })
	f.svc.Start()
	deadline := time.Now().Add(30 * time.Second)
	for !f.svc.Status().HaveGood {
		if time.Now().After(deadline) {
			t.Fatal("调度没有做出备份")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.svc.Stop()
	f.svc.Stop() // 可以重复停
}

// 关机：Close 取消正在做的那份，没做完的整个删掉
func TestCloseCancelsRunningBackup(t *testing.T) {
	f := newFx(t)
	started := make(chan struct{})
	f.holderSnapshot = func(ctx context.Context, _ *sql.DB, dst string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := f.run(ReasonDaily); done <- err }()
	<-started
	f.svc.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("该报被取消：%v", err)
	}
	if ents, _ := os.ReadDir(f.dir()); len(ents) != 0 {
		t.Fatalf("没做完的该删干净：%v", ents)
	}
}

// backup.daily 关着：启动时记一行日志，不起调度；扫一遍目录让状态“已检查”
func TestStartDailyOffLogsOneLine(t *testing.T) {
	var logs []string
	f := newFx(t, func(o *Options) {
		o.Daily = false
		o.Logf = func(format string, args ...any) { logs = append(logs, format) }
	})
	f.svc.Start()
	f.svc.Stop()
	if len(logs) != 1 || !strings.Contains(logs[0], "每天自动备份已关闭") {
		t.Fatalf("该记一行：%v", logs)
	}
	if es, _ := f.svc.List(); len(es) != 0 {
		t.Fatal("关着不该做备份")
	}
}
