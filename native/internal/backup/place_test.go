package backup

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/sqlitesnap"
)

// 数据目录在“临时目录”里：备份放旁边，不用配置里的 backup.dir，记一条 Warn（只一条）；不在里面就用配置的
func TestDirOverriddenWhenDataUnderTempRoot(t *testing.T) {
	p, base := newPaths(t)
	configured := filepath.Join(t.TempDir(), "主人真实的备份")
	var warns []string
	mk := func(roots []string) *Service {
		return New(Options{Paths: p, Dir: configured, TempRoots: roots, Warnf: func(f string, a ...any) { warns = append(warns, f) }})
	}
	if d, err := mk([]string{}).Dir(); err != nil || d != configured || len(warns) != 0 {
		t.Fatalf("没有临时目录：用配置的：%s %v %v", d, err, warns)
	}
	if d, err := mk([]string{t.TempDir()}).Dir(); err != nil || d != configured {
		t.Fatalf("数据目录不在这个临时目录里：用配置的：%s %v", d, err)
	}
	s := mk([]string{filepath.Dir(base)}) // 数据目录在 base 里，base 在它的上一级里
	for i := 0; i < 2; i++ {
		d, err := s.Dir()
		if err != nil || d != filepath.Join(base, "Sumika-备份") {
			t.Fatalf("数据目录在临时目录里：该放旁边：%s %v", d, err)
		}
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "不用配置里的 backup.dir") {
		t.Fatalf("该只记一条 Warn：%v", warns)
	}
	// TempData（SUMIKA_DATA_DIR）照旧
	if d, err := New(Options{Paths: p, Dir: configured, TempData: true, TempRoots: []string{}}).Dir(); err != nil || d != filepath.Join(base, "Sumika-备份") {
		t.Fatalf("%s %v", d, err)
	}
}

// .usage.json 带 source：别的数据目录写的占用，这边总览不认，按清单估算
func TestUsageFileCarriesSource(t *testing.T) {
	f := newFx(t)
	f.mustRun(ReasonManual)
	u, ok := readUsage(f.dir(), testSource)
	if !ok || u.Source != testSource {
		t.Fatalf("写的时候该带上 source：%+v %v", u, ok)
	}
	if _, ok := readUsage(f.dir(), "别的数据目录"); ok {
		t.Fatal("source 对不上不该认")
	}
	if err := writeUsage(f.dir(), 999, f.clock.Now(), "别的数据目录"); err != nil {
		t.Fatal(err)
	}
	o, _ := f.svc.Overview()
	if !o.UsedEstimated || o.UsedBytes == 999 {
		t.Fatalf("别处写的占用不认，该按清单估算：%+v", o)
	}
}

// 占用只数带清单的备份文件夹和 .usage.json：备份目录里别的东西（主人放的、别的程序的）不算，也不走一遍
func TestUsageOnlyCountsBackupFolders(t *testing.T) {
	f := newFx(t, func(o *Options) { o.Media = MediaNone })
	f.mustRun(ReasonManual)
	before := Usage(f.dir())
	write(t, filepath.Join(f.dir(), "主人的大文件夹", "big.bin"), strings.Repeat("x", 500_000))
	write(t, filepath.Join(f.dir(), "memory-a-20261001-000000-before-write-abc.db"), strings.Repeat("y", 500_000))
	if after := Usage(f.dir()); after != before {
		t.Fatalf("备份目录里别的东西不该算进占用：%d -> %d", before, after)
	}
}

// 副本里的库 integrity_check 不过（打得开、但结构有毛病）：这份记 bad 点名库（改坏 rep.OK() 的判断要红）
func TestRunDBIntegrityFailureMakesBackupBad(t *testing.T) {
	f := newFx(t)
	real := f.svc.inspect
	f.svc.inspect = func(ctx context.Context, file string) (sqlitesnap.Report, error) {
		rep, err := real(ctx, file)
		if strings.HasSuffix(filepath.ToSlash(file), "go/chat/qq.db") {
			rep.Integrity = "row 5 missing from index events_idx" // integrity_check 报的不是 ok
		}
		return rep, err
	}
	res, err := f.run(ReasonManual)
	if err == nil || res.Status != StatusBad {
		t.Fatalf("该是 bad：%v %+v", err, res)
	}
	if !strings.Contains(strings.Join(res.Problems, ""), "go/chat/qq.db 的副本没通过 integrity_check") {
		t.Fatalf("原因该点名这个库：%v", res.Problems)
	}
	if st := f.svc.Status(); st.HaveGood {
		t.Fatal("坏的不算好的")
	}
}

// 改名失败后退避、清掉等已经在 robust_test 里；这里补：一份备份没做完的 Running 状态带开始时间
func TestStatusRunningSince(t *testing.T) {
	f := newFx(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.holderSnapshot = func(ctx context.Context, db *sql.DB, dst string) error {
		once.Do(func() { close(started) })
		<-release
		return sqlitesnap.Into(ctx, db, dst)
	}
	done := make(chan error, 1)
	go func() { _, err := f.run(ReasonManual); done <- err }()
	<-started
	st := f.svc.Status()
	if !st.Running || !st.RunningSince.Equal(f.clock.Now()) {
		t.Fatalf("在做时该带开始时间：%+v", st)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := f.svc.Status(); st.Running || !st.RunningSince.IsZero() {
		t.Fatalf("做完以后清掉：%+v", st)
	}
}
