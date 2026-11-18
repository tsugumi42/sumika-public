package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func remaining(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// 验收：用注入的时钟模拟 40 天，每天一份，最后剩 7 份每日 + 4 份每周；手动的一份没删
func TestRotate40Days(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	for _, d := range []string{"shared", "go/characters", "go/media", "dev", "reports", "characters"} { // 这条测的是轮换，数据目录里留得越少 40 次备份越快
		if err := os.RemoveAll(filepath.Join(f.data, filepath.FromSlash(d))); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	start := at(1, 5, 30)
	var manual string
	// 前 30 天：和上面的逐周测试一样，每天放一份带清单的备份再轮换（真轮换，只是备份内容是假的）；
	// 最后 10 天走真的 Tick → Run → 轮换（41 次整套备份在机器忙时要好几秒，规范-测试耗时）。两段的轮换是同一个函数，窗口从假的天数滑进真的天数
	const fakeDays = 30
	for day := 0; day < fakeDays; day++ {
		now := start.AddDate(0, 0, day)
		fabricate(t, f.dir(), now.Format("每天-20060102-1504"), ReasonDaily, StatusOK, now)
		if day == 3 {
			manual = now.Format("手动-20060102-1504")
			fabricate(t, f.dir(), manual, ReasonManual, StatusOK, now)
		}
		if _, err := Rotate(f.dir(), now, false, testSource); err != nil {
			t.Fatal(err)
		}
	}
	for day := fakeDays; day < 40; day++ {
		f.clock.Set(start.AddDate(0, 0, day))
		ran, err := f.svc.Tick(ctx)
		if err != nil || !ran {
			t.Fatalf("第 %d 天该做一份每天备份：%v %v", day, ran, err)
		}
	}
	// 同一天里第二次看不该再做
	if ran, _ := f.svc.Tick(ctx); ran {
		t.Fatal("刚做过，不该再做")
	}
	entries, err := Scan(f.dir())
	if err != nil {
		t.Fatal(err)
	}
	var daily []string
	for _, e := range entries {
		switch e.Manifest.Reason {
		case ReasonDaily:
			daily = append(daily, e.Name)
		case ReasonManual:
			if e.Name != manual {
				t.Errorf("多出一份手动：%s", e.Name)
			}
		}
	}
	if !contains(names(entries), manual) {
		t.Fatalf("手动备份被删了：%v", names(entries))
	}
	if len(daily) != 11 {
		t.Fatalf("40 天以后该剩 7 份每日 + 4 份每周 = 11 份，实际 %d：%v", len(daily), daily)
	}
	// 具体是哪几份：今天（第 39 天，2026-11-09 周一）往回 7 天每天一份；再往前每个日历周（周一起）里最新的一份
	want := map[string]bool{}
	for d := 33; d <= 39; d++ {
		want[start.AddDate(0, 0, d).Format("每天-20060102-1504")] = true
	}
	for _, d := range []int{31, 24, 17, 10} { // 11-01、10-25、10-18、10-11：各周的周日
		want[start.AddDate(0, 0, d).Format("每天-20060102-1504")] = true
	}
	for _, n := range daily {
		if !want[n] {
			t.Errorf("不该留 %s", n)
		}
		delete(want, n)
	}
	for n := range want {
		t.Errorf("该留却没了 %s", n)
	}
}

// 不管今天是周几，40 天以后都是 7 份每日 + 4 份每周（逐天轮换，不是一次算出来的）
func TestRotateWeeklyStableForEveryWeekday(t *testing.T) {
	for shift := 0; shift < 7; shift++ {
		t.Run(fmt.Sprintf("多走%d天", shift), func(t *testing.T) {
			t.Parallel() // 七种起点各用自己的临时目录，互不相干，并着跑
			dir := t.TempDir()
			start := at(1, 5, 30)
			var now time.Time
			for day := 0; day < 40+shift; day++ {
				now = start.AddDate(0, 0, day)
				fabricate(t, dir, now.Format("每天-20060102-1504"), ReasonDaily, StatusOK, now)
				if _, err := Rotate(dir, now, false, testSource); err != nil {
					t.Fatal(err)
				}
			}
			es, _ := Scan(dir)
			if len(es) != 11 {
				t.Fatalf("今天是 %s 时该剩 11 份，实际 %d：%v", now.Weekday(), len(es), names(es))
			}
		})
	}
}

func TestRotateKeepsManual(t *testing.T) {
	dir := t.TempDir()
	now := at(20, 12, 0)
	fabricate(t, dir, "手动-20260101-0000", ReasonManual, StatusOK, at(1, 0, 0).AddDate(0, -2, 0))
	fabricate(t, dir, "手动-20260102-0000", ReasonManual, StatusOK, now.AddDate(-1, 0, 0))
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 2 {
		t.Fatalf("手动备份一份都不能删：%v", got)
	}
}

func TestRotatePreChange(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	fabricate(t, dir, "改动前-重判-20261001-1000", ReasonPreChange, StatusOK, now.AddDate(0, 0, -31))
	fabricate(t, dir, "改动前-重判-20261005-1000", ReasonPreChange, StatusOK, now.AddDate(0, 0, -29))
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 1 || got[0] != "改动前-重判-20261005-1000" {
		t.Fatalf("改动前的留 30 天：%v", got)
	}
}

// 只动带本程序清单的文件夹：别的一概不碰（里面的哨兵文件还在）
func TestRotateIgnoresFoldersWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	// 两份很旧的好每天备份：最老的那份该被删（证明轮换真的跑了），最新的那份永远留着
	fabricate(t, dir, "每天-20260101-0000", ReasonDaily, StatusOK, now.AddDate(0, 0, -100))
	fabricate(t, dir, "每天-20260102-0000", ReasonDaily, StatusOK, now.AddDate(0, 0, -99))
	foreign := []string{"主人的照片", "每天-20260103-0000", "Sumika-备份-旧版", "memory-a-20261001-before-write-ab.db.partial", "手动-20260104-0000"}
	for _, name := range foreign {
		write(t, filepath.Join(dir, name, "sentinel.txt"), "别动")
	}
	write(t, filepath.Join(dir, "memory-a-20261001-000000-before-write-abc.db"), "记忆开写前的备份")
	write(t, filepath.Join(dir, "孤零零的文件.txt"), "x")
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	got := remaining(t, dir)
	if contains(got, "每天-20260101-0000") {
		t.Fatalf("前提：最老的那份有清单、该被删：%v", got)
	}
	for _, name := range append(foreign, "memory-a-20261001-000000-before-write-abc.db", "孤零零的文件.txt", "每天-20260102-0000") {
		if !contains(got, name) {
			t.Errorf("%s 不该被碰：%v", name, got)
		}
	}
	for _, name := range foreign {
		if !exists(filepath.Join(dir, name, "sentinel.txt")) {
			t.Errorf("%s 里的哨兵文件没了", name)
		}
	}
}

func TestRotateIgnoresEmptyOrCorruptManifest(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	for name, content := range map[string]string{
		"每天-20260101-0000": "",                             // 空
		"每天-20260102-0000": "{不是 json",                     // 坏
		"每天-20260103-0000": `{"program":"sumika-native-b`,  // 截断
		"每天-20260104-0000": `{"program":123,"format":"x"}`, // 字段类型不对
	} {
		write(t, filepath.Join(dir, name, ManifestFile), content)
		write(t, filepath.Join(dir, name, "sentinel.txt"), "别动")
	}
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 4 {
		t.Fatalf("清单是空的、坏的就不是我们的，一份都不能删：%v", got)
	}
	if es, _ := Scan(dir); len(es) != 0 {
		t.Fatalf("Scan 不该认出它们：%v", names(es))
	}
}

// Bun 时代的清单（没有 program）、别的格式版本、别的程序：不认
func TestRotateIgnoresForeignProgramAndFormat(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	old := now.AddDate(0, 0, -100).Format(time.RFC3339)
	for name, content := range map[string]string{
		"每天-20260101-0000": `{"version":"abc","createdAt":"` + old + `","reason":"daily","status":"ok","files":[]}`,                                       // Bun 时代，没有 program
		"每天-20260102-0000": `{"program":"sumika-native-backup","format":2,"name":"每天-20260102-0000","time":"` + old + `","reason":"daily","status":"ok"}`, // 更新的格式
		"每天-20260103-0000": `{"program":"别的程序","format":1,"name":"每天-20260103-0000","time":"` + old + `","reason":"daily","status":"ok"}`,
		"每天-20260104-0000": `{"program":"sumika-native-backup","format":1,"name":"每天-20260104-0000","time":"` + old + `","reason":"daily","status":"神秘"}`,
	} {
		write(t, filepath.Join(dir, name, ManifestFile), content)
	}
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 4 {
		t.Fatalf("不认识的版本、标记一份都不能删：%v", got)
	}
}

// 文件夹被拷贝、改过名：清单里的名字对不上，不认
func TestRotateIgnoresRenamedFolder(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	folder := fabricate(t, dir, "每天-20260101-0000", ReasonDaily, StatusOK, now.AddDate(0, 0, -100))
	if err := os.Rename(folder, filepath.Join(dir, "我改的名字")); err != nil {
		t.Fatal(err)
	}
	// 另有一份更新的好备份：改名的那份要是被认成备份，早就过期了该被删（不然它是唯一一份好的，永远留着，断言就空转了）
	fabricate(t, dir, "每天-20261030-0530", ReasonDaily, StatusOK, now.Add(-30*time.Hour))
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 2 || !contains(got, "我改的名字") {
		t.Fatalf("改过名的不认、不碰：%v", got)
	}
	if !exists(filepath.Join(dir, "我改的名字", "data", "x.txt")) {
		t.Fatal("改过名的文件夹里的东西被动了")
	}
}

// 备份目录里的联接：不认、不跟随，目标里的哨兵文件还在
func TestRotateDoesNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	now := at(31, 12, 0)
	outside := t.TempDir()
	// 目标里放一份看起来该被删的合法备份
	fabricate(t, outside, "每天-20260101-0000", ReasonDaily, StatusOK, now.AddDate(0, 0, -100))
	write(t, filepath.Join(outside, "每天-20260101-0000", "sentinel.txt"), "别动")
	makeLink(t, filepath.Join(outside, "每天-20260101-0000"), filepath.Join(dir, "每天-20260101-0000"))
	// 再来一个：链接指向的目录里直接有清单，名字也对
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(outside, "每天-20260101-0000", "sentinel.txt")) || !exists(filepath.Join(outside, "每天-20260101-0000", ManifestFile)) {
		t.Fatal("链接目标里的东西被动了")
	}
	if es, _ := Scan(dir); len(es) != 0 {
		t.Fatalf("链接不该被当成备份：%v", names(es))
	}
}

// 删一份备份时，里面如果有联接，只删联接本身
func TestRemoveTreeDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "要删的")
	outside := filepath.Join(root, "外面")
	write(t, filepath.Join(outside, "重要.txt"), "不能删")
	write(t, filepath.Join(victim, "a.txt"), "a")
	makeLink(t, outside, filepath.Join(victim, "链接"))
	if err := removeTree(victim); err != nil {
		t.Fatal(err)
	}
	if exists(victim) {
		t.Fatal("文件夹该删掉")
	}
	if !exists(filepath.Join(outside, "重要.txt")) {
		t.Fatal("链接目标里的文件被删了")
	}
}

// 时间比现在还晚的（时钟往回拨过）：一律不动
func TestRotateFutureDatedKept(t *testing.T) {
	dir := t.TempDir()
	now := at(10, 12, 0)
	fabricate(t, dir, "每天-20261201-0000", ReasonDaily, StatusOK, at(10, 12, 0).AddDate(0, 2, 0))
	fabricate(t, dir, "每天-20261202-0000", ReasonDaily, StatusBad, at(10, 12, 0).AddDate(0, 2, 1))
	fabricate(t, dir, "每天-20261203-0000", ReasonDaily, StatusPartial, at(10, 12, 0).AddDate(0, 2, 2))
	// 同一个未来的日子里再来一份更新的好备份，加一份现在的好备份：要是按正常的算，同一天里更老的那份会被删
	fabricate(t, dir, "每天-20261201-0600", ReasonDaily, StatusOK, at(10, 12, 0).AddDate(0, 2, 0).Add(6*time.Hour))
	fabricate(t, dir, "每天-20261010-0530", ReasonDaily, StatusOK, now.Add(-6*time.Hour))
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 5 {
		t.Fatalf("未来时间的备份不动：%v", got)
	}
}

// bad 和 .partial 不当作“留着的那份”：同一天里 bad 更新也不能把好的挤掉
func TestRotateBadNotCountedAsKept(t *testing.T) {
	dir := t.TempDir()
	now := at(10, 12, 0)
	fabricate(t, dir, "每天-20261010-0530", ReasonDaily, StatusOK, at(10, 5, 30))
	fabricate(t, dir, "每天-20261010-0800", ReasonDaily, StatusBad, at(10, 8, 0))
	fabricate(t, dir, "每天-20261010-0900", ReasonDaily, StatusPartial, at(10, 9, 0))
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	got := remaining(t, dir)
	if !contains(got, "每天-20261010-0530") {
		t.Fatalf("好的那份被挤掉了：%v", got)
	}
	if !contains(got, "每天-20261010-0800") || !contains(got, "每天-20261010-0900.partial") {
		t.Fatalf("不到 1 天的 bad 和 partial 先留着（给人看原因）：%v", got)
	}
	// 一天以后清掉
	if _, err := Rotate(dir, now.Add(25*time.Hour), false, testSource); err != nil {
		t.Fatal(err)
	}
	got = remaining(t, dir)
	if contains(got, "每天-20261010-0800") || contains(got, "每天-20261010-0900.partial") {
		t.Fatalf("超过 1 天的 bad 和 .partial 该清掉：%v", got)
	}
	if !contains(got, "每天-20261010-0530") {
		t.Fatalf("好的不该动：%v", got)
	}
}

// 最新的一份好的每天备份永远不删：关机很久再开机，旧备份全部“过期”时不能一份不剩
func TestRotateKeepsNewestGood(t *testing.T) {
	dir := t.TempDir()
	fabricate(t, dir, "每天-20260101-0530", ReasonDaily, StatusOK, at(1, 5, 30).AddDate(0, -9, 0))
	fabricate(t, dir, "每天-20260102-0530", ReasonDaily, StatusOK, at(2, 5, 30).AddDate(0, -9, 0))
	now := at(1, 5, 30)
	if _, err := Rotate(dir, now, false, testSource); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, dir); len(got) != 1 || got[0] != "每天-20260102-0530" {
		t.Fatalf("该留下最新的那一份：%v", got)
	}
}

// 新备份没成功时只清坏的和 .partial，不拿“过期”去删好的
func TestRotateOnlyAfterSuccess(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	dir := f.dir()
	old1 := fabricate(t, dir, "每天-20240101-0530", ReasonDaily, StatusOK, at(1, 5, 30).AddDate(-2, 0, 0))
	old2 := fabricate(t, dir, "每天-20250101-0530", ReasonDaily, StatusOK, at(1, 5, 30).AddDate(-1, 0, 0))
	oldBad := fabricate(t, dir, "每天-20250102-0530", ReasonDaily, StatusBad, at(1, 5, 30).AddDate(-1, 0, 0))
	// 让这一次备份失败：trace 库的持有方快照报错
	failing := New(f.opt)
	failing.Register(func() Contribution {
		return Contribution{DBs: []DBSource{{Rel: "go/trace.db", Snapshot: func(context.Context, string) error { return os.ErrPermission }}}}
	})
	if res, err := failing.Run(context.Background(), Request{Reason: ReasonDaily}); err == nil || res.Status != StatusBad {
		t.Fatalf("前提：这次该失败：%v %+v", err, res)
	}
	if !exists(old1) || !exists(old2) {
		t.Fatal("新备份失败时，好的旧备份不能被轮换删掉（哪怕已经过期）")
	}
	if exists(oldBad) {
		t.Fatal("超过 1 天的 bad 每次都该清（不然一直失败时越积越多）")
	}
	// 成功以后才按过期删
	f.clock.Add(time.Minute)
	f.mustRun(ReasonDaily)
	if exists(old1) || exists(old2) {
		t.Fatal("新备份成功以后，过期的旧备份该删")
	}
}

func TestRotateMissingDir(t *testing.T) {
	res, err := Rotate(filepath.Join(t.TempDir(), "没有这个目录"), at(1, 0, 0), false, testSource)
	if err != nil || len(res.Deleted) != 0 {
		t.Fatalf("目录不存在什么都不做：%v %+v", err, res)
	}
}

func TestPlanRotationWhyIsFilled(t *testing.T) {
	now := at(10, 12, 0)
	es := []Entry{{Name: "a", Manifest: Manifest{Time: at(10, 5, 0), Reason: ReasonDaily, Status: StatusOK}}}
	d := PlanRotation(now, es)
	if len(d) != 1 || d[0].Delete || !strings.Contains(d[0].Why, "最新") {
		t.Fatalf("%+v", d)
	}
}

func TestDayIndexAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("没有时区库")
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, loc) // 夏令时 11-01 结束，11-01 那天有 25 小时
	if i := dayIndex(now, time.Date(2026, 11, 1, 1, 0, 0, 0, loc)); i != 1 {
		t.Fatalf("昨天该是 1：%d", i)
	}
	if i := dayIndex(now, time.Date(2026, 10, 31, 23, 0, 0, 0, loc)); i != 2 {
		t.Fatalf("前天该是 2：%d", i)
	}
	if i := dayIndex(now, time.Date(2026, 11, 3, 1, 0, 0, 0, loc)); i != -1 {
		t.Fatalf("明天该是 -1：%d", i)
	}
}

// 别的数据目录写进同一个备份目录的备份（临时数据目录的副本带着真实的 backup.dir 跑起来）：
// 不轮换、不算“最近一份”、不当硬链接来源、总览里单独数
func TestOtherDataDirBackupsAreNotOurs(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	dir := f.dir()
	now := f.clock.Now()
	// 别的数据目录的：一份很旧（该被轮换删掉的样子）、一份很新的好每天备份（不能让这边以为刚做过）
	oldFolder := fabricateAs(t, "别的数据目录", dir, "每天-20240101-0530", ReasonDaily, StatusOK, now.AddDate(-2, 0, 0))
	fabricateAs(t, "别的数据目录", dir, "每天-20261001-0500", ReasonDaily, StatusOK, now.Add(-30*time.Minute))
	badFolder := fabricateAs(t, "别的数据目录", dir, "每天-20240102-0530", ReasonDaily, StatusBad, now.AddDate(-2, 0, 0))
	f.svc.refresh()
	if st := f.svc.Status(); st.HaveGood {
		t.Fatalf("别的数据目录的备份不能算这边的最近一份：%+v", st)
	}
	if ran, err := f.svc.Tick(context.Background()); !ran || err != nil {
		t.Fatalf("这边一份都没有，该做：%v %v", ran, err)
	}
	if !exists(oldFolder) || !exists(badFolder) {
		t.Fatal("别的数据目录的备份不该被这边的轮换删掉")
	}
	if es, _ := f.svc.List(); len(es) != 1 {
		t.Fatalf("List 只列这边的：%v", names(es))
	}
	if o, _ := f.svc.Overview(); o.Foreign != 3 || o.Total != 1 {
		t.Fatalf("总览该单独数出别处的 3 份：%+v", o)
	}
}

// 没通过校验的只留最新一份：磁盘满、库打不开时每小时重试一次，不能堆出一排
func TestRotateKeepsOnlyNewestBad(t *testing.T) {
	dir := t.TempDir()
	now := at(10, 12, 0)
	fabricate(t, dir, "每天-20261010-0500", ReasonDaily, StatusBad, at(10, 5, 0))
	fabricate(t, dir, "每天-20261010-0600", ReasonDaily, StatusBad, at(10, 6, 0))
	fabricate(t, dir, "每天-20261010-0700", ReasonDaily, StatusBad, at(10, 7, 0))
	fabricate(t, dir, "每天-20261010-0400", ReasonDaily, StatusOK, at(10, 4, 0))
	if _, err := Rotate(dir, now, true, testSource); err != nil {
		t.Fatal(err)
	}
	got := remaining(t, dir)
	if len(got) != 2 || !contains(got, "每天-20261010-0700") || !contains(got, "每天-20261010-0400") {
		t.Fatalf("坏的只该剩最新的一份，好的不动：%v", got)
	}
}

// 没通过校验的这份只留清单：副本留着只会占地方，磁盘满时它自己就堵着下一次备份
func TestRunBadKeepsOnlyManifest(t *testing.T) {
	f := newFx(t)
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error { return errors.New("模拟：取不到快照") }
	res, err := f.run(ReasonDaily)
	if err == nil || res.Status != StatusBad {
		t.Fatalf("前提：该是 bad：%v %+v", err, res)
	}
	if exists(filepath.Join(res.Folder, "data")) {
		t.Fatal("坏的备份该把副本清掉")
	}
	m, err := ReadManifest(res.Folder)
	if err != nil || m.Status != StatusBad || len(m.Problems) == 0 {
		t.Fatalf("清单该还在并写着原因：%v %+v", err, m)
	}
}
