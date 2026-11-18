package storagestat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func newData(t *testing.T) paths.Paths {
	t.Helper()
	p, err := paths.Init(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mk(t *testing.T, root, rel string, n int) {
	t.Helper()
	f := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cat(t *testing.T, r ctlproto.StorageReport, key string) ctlproto.StorageCategory {
	t.Helper()
	for _, c := range r.Categories {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("报告里没有类别 %s", key)
	return ctlproto.StorageCategory{}
}

func item(c ctlproto.StorageCategory, name string) ctlproto.StorageItem {
	for _, it := range c.Items {
		if it.Name == name {
			return it
		}
	}
	return ctlproto.StorageItem{}
}

func dirTotal(t *testing.T, root string) int64 {
	t.Helper()
	var n int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, _ := d.Info()
			n += fi.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func traceStore(t *testing.T, p paths.Paths) TraceStore {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p.GoTraceDB()), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenTraceFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return TraceStore{File: p.GoTraceDB()}
}

func rowCount(t *testing.T, p paths.Paths) int {
	t.Helper()
	db, err := store.OpenReadOnlyFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM maintenance_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 验收①：每类字节数；没被认领的归"其他"，总数等于目录里所有文件之和
func TestScanCategoryBytes(t *testing.T) {
	p := newData(t)
	mk(t, p.Root, "go/media/qq/ab/h1", 100)
	mk(t, p.Root, "go/media/qq/.tmp/put-1", 11)
	mk(t, p.Root, "go/media/local/cd/h2", 50)
	mk(t, p.Root, "go/media-thumbs/qq/ab/t1.jpg", 7)
	mk(t, p.Root, "cache/thumbs/t2", 3)
	mk(t, p.Root, "cache/other.bin", 2) // cache 里不是缩略图的 → 其他
	mk(t, p.Root, "go/chat/qq.db", 10)
	mk(t, p.Root, "go/chat/qq.db-wal", 5)
	mk(t, p.Root, "go/chat/local.db", 4)
	mk(t, p.Root, "go/trace.db", 21)
	mk(t, p.Root, "go/trace.db-wal", 2)
	mk(t, p.Root, "go/upgrade-snapshots/go-chat-qq.db-chat-v16-to-v17-20261001-053000.sqlite", 6)
	mk(t, p.Root, "go/characters/c1/memory.db", 9)
	mk(t, p.Root, "go/characters/c2/memory.db", 8)
	mk(t, p.Root, "go/import/x", 30)
	mk(t, p.Root, "import/y", 31)
	mk(t, p.Root, "go/work/results/r1", 6)
	mk(t, p.Root, "go/work/checkpoints/c/record.json", 5)
	mk(t, p.Root, "backups/b1", 12)
	mk(t, p.Root, "logs/desk-1.log", 7)
	mk(t, p.Root, "config/sumika.yml", 13)
	mk(t, p.Root, "go/settings-history/s1.json", 14)
	mk(t, p.Root, "characters/c1/card/card.yml", 15)
	mk(t, p.Root, "dev/d1", 16)
	mk(t, p.Root, "reports/r1.md", 17)
	mk(t, p.Root, "shared/media/m1", 20)
	mk(t, p.Root, "shared/events.db", 40)
	mk(t, p.Root, "shared/owner-memory.db", 5)
	mk(t, p.Root, "shared/owner-memory.db-wal", 2)
	mk(t, p.Root, "go/stray.json", 3)
	mk(t, p.Root, "go/qq-sender.lock", 1)

	backup := filepath.Join(t.TempDir(), "extra-backup")
	models := filepath.Join(t.TempDir(), "models")
	mk(t, backup, "mem.db", 60)
	mk(t, models, "bge/model.onnx", 70)
	s := New(p, Options{Extra: func() Extra { return Extra{ModelsRoot: models, BackupDir: backup} }})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Storage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		ctlproto.StoreMedia: 161, ctlproto.StoreLegacyMedia: 20, ctlproto.StoreThumbs: 10,
		ctlproto.StoreChat: 19, ctlproto.StoreTrace: 23, ctlproto.StoreUpgradeSnapshots: 6,
		ctlproto.StoreMemory: 17, ctlproto.StoreImport: 61,
		ctlproto.StoreWork: 11, ctlproto.StoreBackups: 12 + 60, ctlproto.StoreLogs: 7,
		ctlproto.StoreConfig: 27, ctlproto.StoreCards: 15, ctlproto.StoreDev: 16, ctlproto.StoreReports: 17,
		ctlproto.StoreLegacyOther: 40, ctlproto.StoreOwnerMemory: 7, ctlproto.StoreModels: 70,
	}
	for k, v := range want {
		if got := cat(t, rep, k).Bytes; got != v {
			t.Errorf("%s：%d 字节，要 %d", k, got, v)
		}
	}
	// 分行：聊天库按平台（-wal 并进同一个库）、记忆库按角色、媒体按平台
	chat := cat(t, rep, ctlproto.StoreChat)
	if item(chat, "qq").Bytes != 15 || item(chat, "local").Bytes != 4 {
		t.Errorf("聊天库分行不对：%+v", chat.Items)
	}
	if item(cat(t, rep, ctlproto.StoreMemory), "c1").Bytes != 9 {
		t.Errorf("记忆库分行不对")
	}
	if m := cat(t, rep, ctlproto.StoreMedia); item(m, "qq").Bytes != 111 || item(m, "qq").Files != 2 || item(m, "local").Bytes != 50 {
		t.Errorf("媒体分行不对：%+v", m.Items)
	}
	// 没被认领的：sumika-data.json、cache/other.bin、go/stray.json、go/qq-sender.lock
	other := cat(t, rep, ctlproto.StoreOther)
	insideTotal := rep.Total - 60 - 70 // 去掉目录外的备份和模型
	if insideTotal != dirTotal(t, p.Root) {
		t.Errorf("数据目录里的总数 %d，磁盘上实际 %d：有文件被漏了或算了两次", insideTotal, dirTotal(t, p.Root))
	}
	if other.Bytes < 2+3+1 {
		t.Errorf("其他只有 %d 字节，应该包含 cache、go 下没认领的文件", other.Bytes)
	}
	if rep.NeverScanned || rep.ScannedAtMS == 0 {
		t.Errorf("统计过了，不该写没统计过")
	}
}

// 位置不存在不是出错：备份目录还没建
func TestAbsentIsNotError(t *testing.T) {
	p := newData(t)
	s := New(p, Options{})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.Storage(context.Background())
	b := cat(t, rep, ctlproto.StoreBackups)
	if !b.Absent || b.Bytes != 0 || b.Errors != 0 {
		t.Errorf("备份目录不存在应该是 absent：%+v", b)
	}
	if !cat(t, rep, ctlproto.StoreModels).Absent {
		t.Errorf("没配模型目录应该是 absent")
	}
}

func TestNeverScanned(t *testing.T) {
	p := newData(t)
	mk(t, p.Root, "dev/d1", 16)
	mk(t, p.Root, "logs/a.log", 5)
	s := New(p, Options{Store: traceStore(t, p)})
	rep, err := s.Storage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.NeverScanned {
		t.Errorf("没统计过要写出来")
	}
	if c := cat(t, rep, ctlproto.StoreDev); c.Source != ctlproto.SourceNone || c.Bytes != 0 {
		t.Errorf("大目录没统计过，不许现量也不许写 0 当数：%+v", c)
	}
	if c := cat(t, rep, ctlproto.StoreLogs); c.Source != ctlproto.SourceLive || c.Bytes != 5 {
		t.Errorf("小目录要现量：%+v", c)
	}
	if rowCount(t, p) != 0 {
		t.Errorf("只读 Storage 不许写 maintenance_runs")
	}
}

// 验收②：增量。新文件加、同哈希不加、清理扣减、重启后快照加增量一致
func TestMediaDelta(t *testing.T) {
	p := newData(t)
	mk(t, p.Root, "go/media/qq/ab/seed", 100)
	ts := traceStore(t, p)
	s := New(p, Options{Store: ts, Watch: true})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	put := func(plat, body string) {
		t.Helper()
		if _, _, err := mediastore.Put(p.Root, plat, strings.NewReader(body), 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	put("qq", "aaaa")        // 4
	put("qq", "bbbbbb")      // 6
	put("local", "cccccccc") // 8
	put("qq", "aaaa")        // 同哈希：不加
	rep, _ := s.Storage(context.Background())
	m := cat(t, rep, ctlproto.StoreMedia)
	if m.Bytes != 118 || m.Files != 4 || m.Source != ctlproto.SourceDelta || !m.Approx || rep.DeltaBytes != 18 {
		t.Fatalf("加了三个新文件、一个重复的：%+v delta=%d", m, rep.DeltaBytes)
	}
	if item(m, "qq").Bytes != 110 || item(m, "local").Bytes != 8 {
		t.Errorf("增量要按平台记：%+v", m.Items)
	}
	// 另一个数据目录的写不算在这个服务头上
	other := newData(t)
	if _, _, err := mediastore.Put(other.Root, "qq", strings.NewReader("zzzzzzzzzz"), 1<<20); err != nil {
		t.Fatal(err)
	}
	s.SubMedia("qq", 6, 1) // 清理扣减
	rep, _ = s.Storage(context.Background())
	if m = cat(t, rep, ctlproto.StoreMedia); m.Bytes != 112 || m.Files != 3 {
		t.Fatalf("扣减之后：%+v", m)
	}
	before := rowCount(t, p)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, p) != before+1 {
		t.Fatalf("退出时要把快照加增量写成新的一行")
	}
	s2 := New(p, Options{Store: ts})
	rep2, _ := s2.Storage(context.Background())
	m2 := cat(t, rep2, ctlproto.StoreMedia)
	if m2.Bytes != 112 || m2.Files != 3 || rep2.DeltaBytes != 0 {
		t.Fatalf("重启后和退出前一致：%+v", m2)
	}
	if rep2.ScannedAtMS != rep.ScannedAtMS {
		t.Errorf("折起来的行沿用原来的统计时间")
	}
	// 没有增量时关闭不写行
	n := rowCount(t, p)
	if err := s2.Close(); err != nil || rowCount(t, p) != n {
		t.Errorf("没有增量不该写行")
	}
}

// 从没统计过时累计的增量没有意义，退出也不写行
func TestNoFlushWithoutSnapshot(t *testing.T) {
	p := newData(t)
	s := New(p, Options{Store: traceStore(t, p), Watch: true})
	if _, _, err := mediastore.Put(p.Root, "qq", strings.NewReader("abc"), 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || rowCount(t, p) != 0 {
		t.Fatalf("没有快照不该写行：%v", err)
	}
}

// 验收③：重扫可取消，取消后不写半行
func TestCancelWritesNothing(t *testing.T) {
	p := newData(t)
	mk(t, p.Root, "dev/d1", 16)
	ts := traceStore(t, p)
	s := New(p, Options{Store: ts})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Scan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消了要返回取消：%v", err)
	}
	// 走完了目录、写之前被取消
	ctx, cancel = context.WithCancel(context.Background())
	s.beforeSave = cancel
	if err := s.Scan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("写之前取消要返回取消：%v", err)
	}
	s.beforeSave = nil
	if n := rowCount(t, p); n != 0 {
		t.Fatalf("取消后写了 %d 行", n)
	}
	rep, _ := s.Storage(context.Background())
	if !rep.NeverScanned || rep.Scan.Running {
		t.Errorf("取消后状态要回到没统计过、没在跑：%+v", rep.Scan)
	}
	// 取消后能再来一次，写一整行
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, p) != 1 {
		t.Errorf("再统计一次应该写一行")
	}
}

func TestRescanRunsInBackgroundAndCancels(t *testing.T) {
	p := newData(t)
	for i := 0; i < 20; i++ {
		mk(t, p.Root, fmt.Sprintf("dev/d%d/f", i), 1)
	}
	s := New(p, Options{Store: traceStore(t, p)})
	gate := make(chan struct{})
	s.beforeSave = func() { <-gate }
	if err := s.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Rescan(context.Background()); !errors.Is(err, ErrScanning) {
		t.Fatalf("已经在统计，再开要拒绝：%v", err)
	}
	// 扫描在跑的时候读存储不被挡住
	rep, err := s.Storage(context.Background())
	if err != nil || !rep.Scan.Running {
		t.Fatalf("应该能读到进行中的状态：%v %+v", err, rep.Scan)
	}
	s.CancelScan()
	close(gate)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, p); n != 0 {
		t.Fatalf("取消的统计写了 %d 行", n)
	}
}

// 验收⑤：扫描期间别的协程不被拖住，扫完不留协程
func TestScanDoesNotStall(t *testing.T) {
	if testing.Short() {
		t.Skip("压力和规模测试，放在每日整套")
	}
	p := newData(t)
	for d := 0; d < 20; d++ {
		for f := 0; f < 200; f++ {
			mk(t, p.Root, fmt.Sprintf("dev/d%d/f%d", d, f), 1)
		}
	}
	time.Sleep(100 * time.Millisecond)
	before := runtime.NumGoroutine()
	s := New(p, Options{Store: traceStore(t, p)})
	if err := s.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	var worst time.Duration
	last := time.Now()
	for s.running.Load() {
		time.Sleep(2 * time.Millisecond)
		if gap := time.Since(last); gap > worst {
			worst = gap
		}
		last = time.Now()
	}
	if worst > 300*time.Millisecond {
		t.Errorf("扫描期间别的协程最长被拖住 %v", worst)
	}
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	<-done
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("扫完协程数 %d → %d，留了协程", before, after)
	}
	rep, _ := s.Storage(context.Background())
	if c := cat(t, rep, ctlproto.StoreDev); c.Files != 4000 || c.Bytes != 4000 {
		t.Errorf("开发数据：%+v", c)
	}
}

// Bun 时代的 maintenance_runs 行不认：读不出来就当没有
func TestBunRowsIgnored(t *testing.T) {
	p := newData(t)
	ts := traceStore(t, p)
	db, err := store.OpenTraceFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO maintenance_runs VALUES (1, 5, 999, '{"media":999}', '{}'), (2, 5, 1, 'not json', '{}')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	sn, err := ts.Latest(context.Background())
	if err != nil || sn != nil {
		t.Fatalf("Bun 的行不该被当成快照：%v %+v", err, sn)
	}
	s := New(p, Options{Store: ts})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	sn, err = ts.Latest(context.Background())
	if err != nil || sn == nil || sn.Format != snapshotFormat {
		t.Fatalf("写过一行以后要读得到：%v %+v", err, sn)
	}
	if rowCount(t, p) != 3 {
		t.Errorf("Bun 的旧行一行都不能动")
	}
}
