package backup

import (
	"path/filepath"
	"testing"
)

func TestOverview(t *testing.T) {
	f := newFx(t, func(o *Options) { o.AlertGB = 7 })
	dir := f.dir()
	f.mustRun(ReasonDaily)
	f.mustRun(ReasonManual)
	fabricate(t, dir, "改动前-重判-20261001-0100", ReasonPreChange, StatusOK, at(1, 1, 0))
	fabricate(t, dir, "每天-20260901-0100", ReasonDaily, StatusBad, at(1, 1, 0).AddDate(0, -1, 0))
	fabricate(t, dir, "每天-20260902-0100", ReasonDaily, StatusPartial, at(1, 1, 0).AddDate(0, -1, 0))
	write(t, filepath.Join(dir, "别人的文件夹", "x.txt"), "不算备份")
	o, _ := f.svc.Overview()
	if o.Dir != dir || o.DirError != "" {
		t.Fatalf("目录不对：%+v", o)
	}
	if o.Total != 5 || o.Daily != 3 || o.Manual != 1 || o.PreChange != 1 || o.Bad != 1 || o.Partial != 1 {
		t.Fatalf("份数不对：%+v", o)
	}
	if o.UsedBytes <= 0 || o.AlertGB != 7 || o.Rules == "" || o.Media != MediaStickers || !o.DailyOn {
		t.Fatalf("占用、规则、提醒线不对：%+v", o)
	}
	if !o.FreeKnown || o.FreeBytes <= 0 {
		t.Fatalf("所在盘剩余空间该能取到：%+v", o)
	}
}

func TestOverviewDirError(t *testing.T) {
	p, _ := newPaths(t)
	svc := New(Options{Paths: p, Dir: filepath.Join(p.Root, "x"), TempRoots: []string{}})
	o, _ := svc.Overview()
	if o.DirError == "" || o.Total != 0 {
		t.Fatalf("备份目录指进数据目录时该报原因：%+v", o)
	}
}
