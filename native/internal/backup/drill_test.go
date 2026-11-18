package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 每周抽验和手动 verify 的测试（规格-数据目录与备份 10.10.4）

func newDrillFx(t *testing.T, mut ...func(*Options)) *fx {
	t.Helper()
	return newFx(t, append([]func(*Options){func(o *Options) { o.Drill = true }}, mut...)...)
}

func (f *fx) drillRoot() string {
	f.t.Helper()
	r, err := f.svc.DrillRoot()
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fx) drillRuns() []string {
	ents, _ := os.ReadDir(f.drillRoot())
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func (f *fx) makeDrillRun(name string, created time.Time) string {
	f.t.Helper()
	run := filepath.Join(f.drillRoot(), name)
	if err := writeJSONFile(drillMarkerPath(run), drillMarker{Program: Program, Path: run, Source: f.svc.source(), CreatedAt: created}); err != nil {
		f.t.Fatal(err)
	}
	write(f.t, filepath.Join(run, "x.txt"), "x")
	return run
}

func TestDrillDue(t *testing.T) {
	now := at(20, 12, 0)
	start := now.Add(-1000 * time.Hour)
	cases := []struct {
		name string
		h    DrillHistory
		busy bool
		want bool
	}{
		{"从没抽验过", DrillHistory{Started: start}, false, true},
		{"从没抽验过，但有角色在回复", DrillHistory{Started: start.Add(990 * time.Hour)}, true, false},
		{"从没抽验过，有角色在回复，服务建好已经超过 9 天", DrillHistory{Started: start}, true, true},
		{"3 天前通过", DrillHistory{Have: true, LastAt: now.Add(-72 * time.Hour), LastOK: true, HavePass: true, LastPassAt: now.Add(-72 * time.Hour), Started: start}, false, false},
		{"8 天前通过", DrillHistory{Have: true, LastAt: now.Add(-8 * 24 * time.Hour), LastOK: true, HavePass: true, LastPassAt: now.Add(-8 * 24 * time.Hour), Started: start}, false, true},
		{"8 天前通过，有角色在回复，还没到 9 天", DrillHistory{Have: true, LastAt: now.Add(-8 * 24 * time.Hour), LastOK: true, HavePass: true, LastPassAt: now.Add(-8 * 24 * time.Hour), Started: start}, true, false},
		{"10 天前通过，有角色在回复，超过 9 天不再等", DrillHistory{Have: true, LastAt: now.Add(-10 * 24 * time.Hour), LastOK: true, HavePass: true, LastPassAt: now.Add(-10 * 24 * time.Hour), Started: start}, true, true},
		{"2 小时前失败（退避）", DrillHistory{Have: true, LastAt: now.Add(-2 * time.Hour), LastOK: false, HavePass: true, LastPassAt: now.Add(-9 * 24 * time.Hour), Started: start}, false, false},
		{"25 小时前失败（可以再试）", DrillHistory{Have: true, LastAt: now.Add(-25 * time.Hour), LastOK: false, HavePass: true, LastPassAt: now.Add(-9 * 24 * time.Hour), Started: start}, false, true},
		{"从没通过过，2 小时前失败", DrillHistory{Have: true, LastAt: now.Add(-2 * time.Hour), LastOK: false, Started: start}, false, false},
		{"时钟往回拨过：上一次通过的比现在还晚，不当刚做过", DrillHistory{Have: true, LastAt: now.Add(time.Hour), LastOK: true, HavePass: true, LastPassAt: now.Add(time.Hour), Started: start}, false, true},
		{"失败的记录比现在晚（时钟往回拨过），不拿它退避", DrillHistory{Have: true, LastAt: now.Add(time.Hour), LastOK: false, Started: start}, false, true},
	}
	for _, c := range cases {
		if got := DrillDue(now, c.h, c.busy); got != c.want {
			t.Errorf("%s：想要 %v，得到 %v", c.name, c.want, got)
		}
	}
}

func TestVerifyRecordsPass(t *testing.T) {
	f := newDrillFx(t)
	res := f.mustRun(ReasonDaily)
	dataBefore := treeHashes(t, f.data)
	out, err := f.svc.Verify(context.Background(), "", DrillManual)
	if err != nil || !out.Record.OK || out.Record.Backup != res.Name || out.Record.Kind != DrillManual {
		t.Fatalf("抽验该通过：%v %+v", err, out)
	}
	if out.Record.Files == 0 || out.Record.DBs == 0 || !out.Record.RowsRecorded || len(out.Report.DBs) != out.Record.DBs {
		t.Fatalf("记录里该有文件数、库数、行数核对：%+v", out.Record)
	}
	if runs := f.drillRuns(); len(runs) != 0 {
		t.Fatalf("抽验完临时目录该删掉：%v", runs)
	}
	if !sameTree(dataBefore, treeHashes(t, f.data)) {
		t.Fatal("抽验动了数据目录")
	}
	st := f.svc.DrillStatus()
	if !st.Have || !st.HavePass || st.LastPassBackup != res.Name || !st.Checked {
		t.Fatalf("状态没更新：%+v", st)
	}
	// 记录落在备份目录里，换一个新服务（重启）读得到，进总览
	s2 := New(f.opt)
	o, _ := s2.Overview()
	if !o.Drill.HavePass || o.Drill.LastPassBackup != res.Name || !o.DrillOn {
		t.Fatalf("重启后总览该读到上次抽验：%+v", o.Drill)
	}
}

func TestVerifyRecordsFailure(t *testing.T) {
	f := newDrillFx(t)
	res := f.mustRun(ReasonDaily)
	victim := filepath.Join(res.Folder, "data", "config", "sumika.yml")
	b, _ := os.ReadFile(victim)
	b[0] ^= 0xff
	if err := os.WriteFile(victim, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Verify(context.Background(), "", DrillWeekly)
	if err != nil || out.Record.OK || len(out.Record.Problems) == 0 || !strings.Contains(out.Record.Problems[0], "config/sumika.yml") {
		t.Fatalf("抽验该记成失败并点名文件：%v %+v", err, out.Record)
	}
	if runs := f.drillRuns(); len(runs) != 0 {
		t.Fatalf("失败的抽验临时目录也该删掉：%v", runs)
	}
	st := f.svc.DrillStatus()
	if !st.Have || st.Last.OK || st.HavePass {
		t.Fatalf("%+v", st)
	}
	recs, ok := readDrills(f.dir(), f.svc.source())
	if !ok || len(recs) != 1 || recs[0].OK {
		t.Fatalf("记录文件里该有一条失败的：%v %+v", ok, recs)
	}
}

func TestVerifyNoBackupOrBadNameIsNotRecorded(t *testing.T) {
	f := newDrillFx(t)
	if _, err := f.svc.Verify(context.Background(), "", DrillManual); !errors.Is(err, ErrNoDrillBackup) {
		t.Fatalf("一份备份都没有该回 ErrNoDrillBackup：%v", err)
	}
	f.mustRun(ReasonDaily)
	if _, err := f.svc.Verify(context.Background(), "手动-不存在", DrillManual); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := f.svc.Verify(context.Background(), "../x", DrillManual); !errors.Is(err, ErrBadBackupName) {
		t.Fatalf("%v", err)
	}
	if st := f.svc.DrillStatus(); st.Have {
		t.Fatalf("这些都不是抽验的结果，不该记：%+v", st)
	}
}

func TestVerifyDefaultPicksNewestDaily(t *testing.T) {
	f := newDrillFx(t)
	older := f.mustRun(ReasonDaily)
	f.clock.Add(26 * time.Hour)
	newer := f.mustRun(ReasonDaily)
	f.clock.Add(time.Hour)
	manual := f.mustRun(ReasonManual)
	got, err := f.svc.pickDrillBackup()
	if err != nil || got != newer.Name {
		t.Fatalf("该挑最新的每天备份 %s（不是更新的手动备份 %s、也不是更早的 %s）：%q %v", newer.Name, manual.Name, older.Name, got, err)
	}
	// 一份每天备份都没有：退到最新的好备份
	for _, e := range []string{older.Folder, newer.Folder} {
		rewriteManifest(t, e, func(m *Manifest) { m.Status = StatusBad })
	}
	got, err = f.svc.pickDrillBackup()
	if err != nil || got != manual.Name {
		t.Fatalf("没有好的每天备份时该退到手动的：%q %v", got, err)
	}
}

func TestDrillOnlyOurOwnBackups(t *testing.T) {
	f := newDrillFx(t)
	mine := f.mustRun(ReasonDaily)
	f.clock.Add(26 * time.Hour)
	other := f.mustRun(ReasonDaily)
	rewriteManifest(t, other.Folder, func(m *Manifest) { m.Source = "别的数据目录" })
	got, err := f.svc.pickDrillBackup()
	if err != nil || got != mine.Name {
		t.Fatalf("别的数据目录的备份不抽：%q %v", got, err)
	}
}

// 抽验的临时目录固定在备份目录的上一级里：不在数据目录里，也不在备份目录里（所以轮换和 Scan 看不见它）
func TestDrillRootNeverInsideData(t *testing.T) {
	for _, dir := range []string{"", "上级/子级/备份"} {
		f := newDrillFx(t, func(o *Options) { o.Dir = dir })
		root := f.drillRoot()
		dataReal, _ := realExisting(f.data)
		backupReal, _ := realExisting(f.dir())
		rootReal, _ := realExisting(root)
		if within(dataReal, rootReal) || within(rootReal, dataReal) {
			t.Errorf("备份目录 %q：抽验目录 %s 和数据目录有包含关系", dir, root)
		}
		if within(backupReal, rootReal) || within(rootReal, backupReal) {
			t.Errorf("备份目录 %q：抽验目录 %s 和备份目录有包含关系", dir, root)
		}
		if _, _, err := f.svc.checkRestoreTarget(f.dir(), filepath.Join(root, "20261101-000000")); err != nil {
			t.Errorf("备份目录 %q：抽验的子目录该通过恢复目标的检查：%v", dir, err)
		}
		f.mustRun(ReasonDaily)
		if out, err := f.svc.Verify(context.Background(), "", DrillManual); err != nil || !out.Record.OK {
			t.Fatalf("%v %+v", err, out)
		}
	}
}

// 只删自己建的：路径重新算一遍，每一条都满足才删
func TestDrillRemoveOnlyOwn(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	ws := f.drillRoot()
	now := f.clock.Now()

	ours := f.makeDrillRun("20261101-000001", now)
	if f.svc.removeDrillRun(ours) != nil || exists(ours) {
		t.Fatal("自己建的该删掉")
	}

	// 没有标记
	noMarker := filepath.Join(ws, "20261101-000002")
	write(t, filepath.Join(noMarker, "别人的.txt"), "x")
	// 标记里的路径和这个目录对不上（标记是从别处拷来的）
	wrongPath := f.makeDrillRun("20261101-000003", now)
	write(t, filepath.Join(ws, "20261101-000004", ".restore", drillMarkerFile), "")
	b, _ := os.ReadFile(drillMarkerPath(wrongPath))
	copied := filepath.Join(ws, "20261101-000004")
	if err := os.WriteFile(drillMarkerPath(copied), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// 标记的 program 不对
	wrongProgram := f.makeDrillRun("20261101-000005", now)
	if err := writeJSONFile(drillMarkerPath(wrongProgram), drillMarker{Program: "别的程序", Path: wrongProgram, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// 名字不是抽验建的格式
	badName := f.makeDrillRun("我的资料", now)
	// 不在 Sumika-抽验 直接下面：在别处、在数据目录里、在子目录里
	elsewhere := filepath.Join(f.base, "20261101-000006")
	_ = writeJSONFile(drillMarkerPath(elsewhere), drillMarker{Program: Program, Path: elsewhere, CreatedAt: now})
	inData := filepath.Join(f.data, "20261101-000007")
	_ = writeJSONFile(drillMarkerPath(inData), drillMarker{Program: Program, Path: inData, CreatedAt: now})
	nested := filepath.Join(ws, "20261101-000008", "20261101-000009")
	_ = writeJSONFile(drillMarkerPath(nested), drillMarker{Program: Program, Path: nested, CreatedAt: now})
	// 联接：指向一个带着合法标记的别处目录，不能顺着删到别处
	outside := filepath.Join(f.base, "联接指向的目录")
	_ = writeJSONFile(drillMarkerPath(outside), drillMarker{Program: Program, Path: filepath.Join(ws, "20261101-000010"), CreatedAt: now})
	write(t, filepath.Join(outside, "重要文件.txt"), "不能丢")
	link := filepath.Join(ws, "20261101-000010")
	makeLink(t, outside, link)

	for name, p := range map[string]string{"没有标记": noMarker, "标记路径对不上": copied, "program 不对": wrongProgram, "名字不对": badName,
		"在别处": elsewhere, "在数据目录里": inData, "嵌套": nested, "联接": link, "数据目录本身": f.data, "备份目录本身": f.dir(), "抽验根目录": ws} {
		if f.svc.removeDrillRun(p) == nil {
			t.Errorf("%s：不该删", name)
		}
		if !exists(p) {
			t.Errorf("%s：被删了", name)
		}
	}
	if !exists(filepath.Join(outside, "重要文件.txt")) {
		t.Fatal("顺着联接删到了别处")
	}
	if !exists(filepath.Join(f.data, "config", "sumika.yml")) {
		t.Fatal("数据目录被动了")
	}
}

// 上次进程被杀留下的旧抽验目录：下次抽验前清掉，只清满足全部条件的、超过 1 天的
func TestDrillSweepsOldRuns(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	f.clock.Add(2 * time.Hour) // 服务是两小时前建的：下面“刚建的”那条要比服务启动晚
	now := f.clock.Now()
	old := f.makeDrillRun("20261001-000000", now.Add(-48*time.Hour))
	fresh := f.makeDrillRun("20261002-000000", now.Add(-time.Hour))
	future := f.makeDrillRun("20261003-000000", now.Add(48*time.Hour)) // 时钟往回拨过
	foreign := filepath.Join(f.drillRoot(), "20261004-000000")
	write(t, filepath.Join(foreign, "别人的.txt"), "x") // 名字像、没有标记
	f.svc.sweepDrills(context.Background())
	if exists(old) {
		t.Error("超过 1 天的旧抽验目录该清掉")
	}
	for name, p := range map[string]string{"刚建的": fresh, "时钟往回拨": future, "没有标记的": foreign} {
		if !exists(p) {
			t.Errorf("%s不该清", name)
		}
	}
	// Verify 开始前也清
	old2 := f.makeDrillRun("20261005-000000", now.Add(-72*time.Hour))
	if _, err := f.svc.Verify(context.Background(), "", DrillManual); err != nil {
		t.Fatal(err)
	}
	if exists(old2) {
		t.Error("抽验开始前该先清旧的")
	}
}

func TestVerifyCancelDoesNotRecord(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	ctx, cancel := context.WithCancel(context.Background())
	real := f.svc.restoreCopy
	calls := 0
	f.svc.restoreCopy = func(c context.Context, src, dst string) (int64, string, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return real(c, src, dst)
	}
	_, err := f.svc.Verify(ctx, "", DrillManual)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("被取消该回取消：%v", err)
	}
	if st := f.svc.DrillStatus(); st.Have {
		t.Fatalf("被取消的不记（下次接着做）：%+v", st)
	}
	if runs := f.drillRuns(); len(runs) != 0 {
		t.Fatalf("被取消后临时目录也该删掉：%v", runs)
	}
	if st := f.svc.DrillStatus(); st.Running {
		t.Fatal("做完了 Running 该清掉")
	}
}

func TestDrillRecordIgnoresForeignOrBroken(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	dir := f.dir()
	for name, content := range map[string]string{
		"不是 JSON":  "{坏了",
		"别的数据目录写的": `{"program":"` + Program + `","source":"别处","records":[{"at":"2026-10-01T00:00:00Z","ok":true,"backup":"x"}]}`,
		"不是本程序写的":  `{"program":"别的","source":"` + f.svc.source() + `","records":[{"at":"2026-10-01T00:00:00Z","ok":true,"backup":"x"}]}`,
	} {
		write(t, filepath.Join(dir, drillFile), content)
		s := New(f.opt)
		if o, _ := s.Overview(); o.Drill.Have {
			t.Errorf("%s：该当作从没抽验过：%+v", name, o.Drill)
		}
	}
}

func TestDrillKeepsLast20(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	for i := 0; i < 25; i++ {
		f.svc.recordDrill(DrillRecord{At: f.clock.Now().Add(time.Duration(i) * time.Minute), Kind: DrillWeekly, Backup: "x", OK: true})
	}
	recs, ok := readDrills(f.dir(), f.svc.source())
	if !ok || len(recs) != drillKeep {
		t.Fatalf("只留最近 %d 条：%v %d", drillKeep, ok, len(recs))
	}
	if !recs[len(recs)-1].At.Equal(f.clock.Now().Add(24 * time.Minute)) {
		t.Fatalf("留下的该是最新的：%v", recs[len(recs)-1].At)
	}
}

// Tick：每天备份那段之后才做抽验，这一次刚做过每天备份就先跳过；不到一周不再做；有角色在回复先等；备份在做就让路，下次补
func TestTickRunsDrillAfterDaily(t *testing.T) {
	f := newDrillFx(t)
	ran, err := f.svc.Tick(context.Background())
	if err != nil || !ran {
		t.Fatalf("第一次 Tick 该做每天备份：%v %v", ran, err)
	}
	if st := f.svc.DrillStatus(); st.Have {
		t.Fatalf("刚做过每天备份的这一次 Tick 不该抽验（错开）：%+v", st)
	}
	f.clock.Add(time.Hour)
	ran, err = f.svc.Tick(context.Background())
	if err != nil || ran {
		t.Fatalf("每天备份还新鲜，不该再做：%v %v", ran, err)
	}
	st := f.svc.DrillStatus()
	if !st.Have || !st.Last.OK || st.Last.Kind != DrillWeekly {
		t.Fatalf("这一次 Tick 该做抽验并通过：%+v", st)
	}
	if recs, ok := readDrills(f.dir(), f.svc.source()); !ok || len(recs) != 1 {
		t.Fatalf("记录文件里该有一条：%v %v", ok, recs)
	}
	// 再过几个小时、几天：一周以内不再做
	for _, d := range []time.Duration{time.Hour, 5 * time.Hour} {
		f.clock.Add(d)
		_, _ = f.svc.Tick(context.Background())
	}
	if recs, _ := readDrills(f.dir(), f.svc.source()); len(recs) != 1 {
		t.Fatalf("一周以内不该再抽验：%d", len(recs))
	}
	// 8 天以后：当天的 Tick 先做每天备份（错开），下一个小时的 Tick 抽验
	f.clock.Add(8 * 24 * time.Hour)
	if ran, _ := f.svc.Tick(context.Background()); !ran {
		t.Fatal("8 天后该先做每天备份")
	}
	if recs, _ := readDrills(f.dir(), f.svc.source()); len(recs) != 1 {
		t.Fatalf("做每天备份的那一次不该抽验：%d", len(recs))
	}
	f.clock.Add(time.Hour)
	_, _ = f.svc.Tick(context.Background())
	if recs, _ := readDrills(f.dir(), f.svc.source()); len(recs) != 2 {
		t.Fatalf("下一个小时该抽验：%d", len(recs))
	}
}

func TestTickWithoutDrillOption(t *testing.T) {
	f := newFx(t) // Drill 默认关
	f.mustRun(ReasonDaily)
	f.clock.Add(time.Hour)
	if _, err := f.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.svc.DrillStatus(); st.Have {
		t.Fatalf("没开抽验不该做：%+v", st)
	}
	if exists(f.drillRoot()) {
		t.Fatal("没开抽验不该建临时目录")
	}
}

func TestTickDrillWaitsForBusyRoles(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	f.clock.Add(time.Hour)
	f.busy = true
	_, _ = f.svc.Tick(context.Background())
	if st := f.svc.DrillStatus(); st.Have {
		t.Fatalf("有角色在回复，抽验先等：%+v", st)
	}
	f.busy = false
	_, _ = f.svc.Tick(context.Background())
	if st := f.svc.DrillStatus(); !st.Have {
		t.Fatal("没有角色在回复了该抽验")
	}
}

func TestTickDrillSkipsWhenBusy(t *testing.T) {
	f := newDrillFx(t)
	res := f.mustRun(ReasonDaily)
	f.clock.Add(time.Hour)
	entered := make(chan struct{})
	release := make(chan struct{})
	real := f.svc.restoreCopy
	once := make(chan struct{}, 1)
	once <- struct{}{}
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		select {
		case <-once:
			close(entered)
			<-release
		default:
		}
		return real(ctx, src, dst)
	}
	done, err := f.svc.GoRestore(res.Name, f.target("占着锁的恢复"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := f.svc.Tick(context.Background()); err != nil {
		t.Fatalf("让路不是错误：%v", err)
	}
	if st := f.svc.DrillStatus(); st.Have {
		t.Fatalf("恢复在做，抽验该让路、不记结果：%+v", st)
	}
	close(release)
	<-done
	if _, err := f.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.svc.DrillStatus(); !st.Have || !st.Last.OK {
		t.Fatalf("下次 Tick 该补上：%+v", st)
	}
}

func TestDrillFailureBacksOff(t *testing.T) {
	f := newDrillFx(t)
	res := f.mustRun(ReasonDaily)
	victim := filepath.Join(res.Folder, "data", "config", "sumika.yml")
	b, _ := os.ReadFile(victim)
	b[0] ^= 0xff
	_ = os.WriteFile(victim, b, 0o644)
	f.clock.Add(time.Hour)
	_, _ = f.svc.Tick(context.Background())
	if recs, _ := readDrills(f.dir(), f.svc.source()); len(recs) != 1 || recs[0].OK {
		t.Fatalf("第一次该记一条失败的：%+v", recs)
	}
	for i := 0; i < 5; i++ {
		f.clock.Add(time.Hour)
		_, _ = f.svc.Tick(context.Background())
	}
	if recs, _ := readDrills(f.dir(), f.svc.source()); len(recs) != 1 {
		t.Fatalf("失败以后 24 小时内不再试（退避）：%d", len(recs))
	}
}

// ───────────── 复核后补的（返工） ─────────────

// “上次通过”的规则：只有本数据目录的、抽验当时最新一份好备份，抽验通过才计入；别的数据目录的、旧的备份手动抽验
// 照样核对、记录，但不计入——不推迟每周抽验、不让 S12 转绿
func TestDrillCountingRules(t *testing.T) {
	f := newDrillFx(t)
	older := f.mustRun(ReasonDaily)
	f.clock.Add(26 * time.Hour)
	newer := f.mustRun(ReasonDaily)
	f.clock.Add(time.Hour)

	// 很旧的那份：核对通过，但不计入
	out, err := f.svc.Verify(context.Background(), older.Name, DrillManual)
	if err != nil || !out.Record.OK || out.Record.Counts || !strings.Contains(out.Record.NotCountedWhy, "不是最新一份好的每天备份") {
		t.Fatalf("旧备份手动抽验该通过但不计入：%v %+v", err, out.Record)
	}
	st := f.svc.DrillStatus()
	if st.HavePass || st.Have || !st.HaveAny || st.LastAny.Backup != older.Name {
		t.Fatalf("不计入的不能顶替“上次通过”：%+v", st)
	}
	if !DrillDue(f.clock.Now(), drillHistory(st, f.svc.Status().Started), false) {
		t.Fatal("不计入的抽验不能让每周抽验推迟")
	}
	// 别的数据目录的备份：同样
	rewriteManifest(t, older.Folder, func(m *Manifest) { m.Source = "别的数据目录" })
	out, err = f.svc.Verify(context.Background(), older.Name, DrillManual)
	if err != nil || !out.Record.OK || out.Record.Counts || out.Record.NotCountedWhy != "别的数据目录的备份" {
		t.Fatalf("别的数据目录的备份该通过但不计入：%v %+v", err, out.Record)
	}
	if st := f.svc.DrillStatus(); st.HavePass {
		t.Fatalf("%+v", st)
	}
	// 不计入的失败也不能让自检变红、让每周抽验退避
	b, _ := os.ReadFile(filepath.Join(older.Folder, "data", "config", "sumika.yml"))
	b[0] ^= 0xff
	_ = os.WriteFile(filepath.Join(older.Folder, "data", "config", "sumika.yml"), b, 0o644)
	out, err = f.svc.Verify(context.Background(), older.Name, DrillManual)
	if err != nil || out.Record.OK || out.Record.Counts {
		t.Fatalf("%v %+v", err, out.Record)
	}
	if st := f.svc.DrillStatus(); st.Have || !DrillDue(f.clock.Now(), drillHistory(st, f.svc.Status().Started), false) {
		t.Fatalf("不计入的失败不该影响计入的状态：%+v", st)
	}
	// 每周抽验自己挑最新的那份，计入
	if _, err := f.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = f.svc.DrillStatus()
	if !st.HavePass || !st.Last.Counts || st.Last.Backup != newer.Name || st.Last.Kind != DrillWeekly {
		t.Fatalf("每周抽验该计入最新那份：%+v", st)
	}
	// 计入以后，再手动抽别的：上次通过的时间不变
	passAt := st.LastPassAt
	f.clock.Add(time.Hour)
	if _, err := f.svc.Verify(context.Background(), older.Name, DrillManual); err != nil {
		t.Fatal(err)
	}
	if st := f.svc.DrillStatus(); !st.LastPassAt.Equal(passAt) || !st.HavePass {
		t.Fatalf("不计入的手动抽验不能改“上次通过”：%+v", st)
	}
	// 手动抽最新那份：计入
	f.clock.Add(time.Hour)
	if out, err := f.svc.Verify(context.Background(), newer.Name, DrillManual); err != nil || !out.Record.Counts {
		t.Fatalf("手动抽最新一份自己的备份该计入：%v %+v", err, out.Record)
	}
}

// 先 verify 后 list：历史还在。每日备份关着时调度不起、refresh 不跑，不先读记录就写会把以前的记录覆盖掉
func TestVerifyBeforeListKeepsHistory(t *testing.T) {
	f := newDrillFx(t, func(o *Options) { o.Daily = false })
	f.mustRun(ReasonDaily)
	var old []DrillRecord
	for i := 0; i < 3; i++ {
		old = append(old, DrillRecord{At: f.clock.Now().Add(-time.Duration(10-i) * 24 * time.Hour), Kind: DrillWeekly, Backup: "旧的", OK: true, Counts: true})
	}
	if err := writeDrills(f.dir(), f.svc.source(), old); err != nil {
		t.Fatal(err)
	}
	s := New(f.opt) // 新进程：还没读过记录文件
	if _, err := s.Verify(context.Background(), "", DrillManual); err != nil {
		t.Fatal(err)
	}
	recs, ok := readDrills(f.dir(), s.source())
	if !ok || len(recs) != 4 {
		t.Fatalf("以前的 3 条历史该还在、再加这一条：%v %d", ok, len(recs))
	}
	if st := s.DrillStatus(); !st.HavePass || !st.Checked {
		t.Fatalf("%+v", st)
	}
}

// 记录文件暂时读不了（盘休眠、掉线）：不当成“没有记录”，更不能之后拿只有这几条的内存列表把真实文件覆盖掉
func TestDrillRecordFileIOErrorDoesNotOverwrite(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	file := filepath.Join(f.dir(), drillFile)
	if err := os.Mkdir(file, 0o755); err != nil { // 把它变成目录：ReadFile 报的不是“不存在”
		t.Fatal(err)
	}
	s := New(f.opt)
	if _, err := s.Verify(context.Background(), "", DrillManual); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || !fi.IsDir() {
		t.Fatalf("读不了记录文件时不该写回（把它覆盖掉）：%v", err)
	}
	if st := s.DrillStatus(); st.Checked || !st.HaveAny {
		t.Fatalf("没读到文件时不算读过（自检当“没读过”），这次的结果仍留在内存里：%+v", st)
	}
	// 盘好了：下次读到文件，和内存里的合在一起，以后再写
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	prior := []DrillRecord{{At: f.clock.Now().Add(-48 * time.Hour), Kind: DrillWeekly, Backup: "旧的", OK: true, Counts: true}}
	if err := writeDrills(f.dir(), s.source(), prior); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(time.Minute)
	if _, err := s.Verify(context.Background(), "", DrillManual); err != nil {
		t.Fatal(err)
	}
	recs, ok := readDrills(f.dir(), s.source())
	if !ok || len(recs) != 3 {
		t.Fatalf("文件里的 1 条、内存里的 2 条都该在：%v %d", ok, len(recs))
	}
}

// 抽验目录本身是联接：副本会写到联接另一头而我们不删联接里的东西，所以建任何东西之前就拒绝，联接另一头没有任何文件
func TestDrillWorkspaceIsLinkRefused(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	outside := filepath.Join(f.base, "联接另一头")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	makeLink(t, outside, f.drillRoot())
	out, err := f.svc.Verify(context.Background(), "", DrillWeekly)
	if err != nil || out.Record.OK || len(out.Record.Problems) == 0 || !strings.Contains(out.Record.Problems[0], "是联接") {
		t.Fatalf("抽验目录是联接该失败并说明：%v %+v", err, out.Record)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("联接另一头不该有任何东西：%v", ents)
	}
	if st := f.svc.DrillStatus(); !st.Have || st.Last.OK || st.HavePass || !st.Last.Counts {
		t.Fatalf("该记成一次计入的失败（S12 会报）：%+v", st)
	}
}

// 抽验超时（RunMax）和关机取消是两回事：超时记成失败，走退避，不然每个整点再复制 6 小时
func TestDrillTimeoutIsRecordedAsFailure(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	f.svc.opt.RunMax = 50 * time.Millisecond
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		<-ctx.Done()
		return 0, "", ctx.Err()
	}
	out, err := f.svc.Verify(context.Background(), "", DrillWeekly)
	if err != nil || out.Record.OK || len(out.Record.Problems) == 0 || !strings.Contains(out.Record.Problems[0], "超过") {
		t.Fatalf("超时该记成失败并写“超过”：%v %+v", err, out.Record)
	}
	if runs := f.drillRuns(); len(runs) != 0 {
		t.Fatalf("超时以后临时目录也该删掉：%v", runs)
	}
	st := f.svc.DrillStatus()
	if !st.Have || st.Last.OK || DrillDue(f.clock.Now().Add(time.Hour), drillHistory(st, f.svc.Status().Started), false) {
		t.Fatalf("记成失败、24 小时内不再试：%+v", st)
	}
}

// 临时目录没删掉不能记成通过：副本还留在盘上，S12 要能看到
func TestDrillRemovalFailureIsFailure(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	real := f.svc.restoreCopy
	f.svc.restoreCopy = func(ctx context.Context, src, dst string) (int64, string, error) {
		// 第一个文件复制前，把“这是我建的”标记删了：之后核对一切正常，但删目录那一步认不出它
		runs, _ := os.ReadDir(f.drillRoot())
		for _, r := range runs {
			_ = os.Remove(drillMarkerPath(filepath.Join(f.drillRoot(), r.Name())))
		}
		return real(ctx, src, dst)
	}
	out, err := f.svc.Verify(context.Background(), "", DrillWeekly)
	if err != nil || out.Record.OK || !strings.Contains(strings.Join(out.Record.Problems, "；"), "没删") {
		t.Fatalf("删不掉该记成失败：%v %+v", err, out.Record)
	}
}

// 上次进程被杀、关机留下的副本：建在本进程启动以前的，进程里第一次 Tick 就清（不等 24 小时、也不等下一次抽验）
func TestDrillFirstTickSweepsEarlierProcessLeftovers(t *testing.T) {
	f := newDrillFx(t)
	f.mustRun(ReasonDaily)
	now := f.clock.Now()
	left := f.makeDrillRun("20261001-000001", now.Add(-10*time.Minute)) // 比服务启动早，不到 1 天
	mine := f.makeDrillRun("20261001-000002", now)                      // 本进程启动以后建的、还不到 1 天：留着
	if _, err := f.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exists(left) {
		t.Error("建在本进程启动以前的残留，第一次 Tick 就该清")
	}
	if !exists(mine) {
		t.Error("本进程启动以后建的、不到 1 天的不能清（可能就是正在做的那个）")
	}
}

// 抽验记录文件所在目录名放在 .gitignore 里：默认布局下它在仓库根下，进程被杀会残留一整份数据副本（含明文配置和聊天库），不能被 git add -A 带进提交
func TestDrillRootIsGitignored(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n/"+DrillRootName+"/\n") {
		t.Fatalf(".gitignore 里该有 /%s/", DrillRootName)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("没有 git")
	}
	var env []string
	for _, e := range os.Environ() { // 清掉 GIT_*：钩子里继承来的 GIT_DIR、GIT_INDEX_FILE 会让它去看别的仓库
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	for _, p := range []string{DrillRootName + "/20261101-000000/go/chat/qq.db", DrillRootName + "/.restore", "Sumika-备份/x"} {
		cmd := exec.Command(git, "check-ignore", "-q", p)
		cmd.Dir, cmd.Env = root, env
		if err := cmd.Run(); err != nil {
			t.Errorf("git check-ignore %s 该是被忽略：%v", p, err)
		}
	}
}
