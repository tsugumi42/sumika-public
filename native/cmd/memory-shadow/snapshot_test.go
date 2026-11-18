package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/datalock"
	"sumika-native/internal/paths"
)

// snapFixture：造好数据目录、塞几条事件、关掉聊天库，再准备好副本目录
func snapFixture(t *testing.T) (*fixture, snapOpts) {
	t.Helper()
	f := newFixture(t)
	f.ev(convOwner, "owner", "我下周三考试")
	f.ev(convGroupA, "qq:5", "群里一句话")
	f.ev(convLocal, "owner", "本地一句话")
	f.closeStores()
	cp := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(cp, 0o755); err != nil {
		t.Fatal(err)
	}
	return f, snapOpts{Data: f.Data, Copy: cp, Char: testChar}
}

// 拷贝：副本的记忆库和源一字节不差；Bun 的原库、memory-v2.db 都不在副本里；锁放了
func TestSnapshotCopiesGoSideOnly(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	f.write(filepath.Join(f.Data, "go", "characters", testChar, "memory-v2.db"), "派生的索引，不该被拷")
	snap, err := takeSnapshot(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Attempts != 1 || !snap.IntegrityOK {
		t.Fatalf("应该一次拷到、integrity_check 过：%+v", snap)
	}
	src, _ := sha256File(f.memDB())
	got, err := sha256File(filepath.Join(o.Copy, "go", "characters", testChar, "memory.db"))
	if err != nil || got == "" {
		t.Fatalf("副本的记忆库：%v", err)
	}
	// integrity_check 打开副本可能会合并 WAL，所以拷完那一刻的一致用 snapshot.Before 里的哈希证明
	if snap.Before["go/characters/suzune/memory.db"].SHA != src {
		t.Fatal("拷贝前的哈希应该等于源的哈希")
	}
	for _, rel := range []string{"go/chat/qq.db", "go/chat/local.db", "go/trace.db"} {
		if _, err := os.Stat(filepath.Join(o.Copy, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("副本里应该有 %s：%v", rel, err)
		}
	}
	for _, rel := range []string{"go/characters/suzune/memory-v2.db", "characters/suzune/memory.db", "go/backend.lock"} {
		if _, err := os.Stat(filepath.Join(o.Copy, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("副本里不该有 %s", rel)
		}
	}
	// 拷完锁已经放了：桌面程序或 Go 后台又能拿到。
	lock, err := datalock.LockData(f.Data)
	if err != nil {
		t.Fatalf("拷完应该放了数据锁：%v", err)
	}
	lock.Close()
}

// 数据锁被别人拿着（桌面程序在跑）：拒绝，副本里一个文件都没有
func TestSnapshotRefusesWhenDataLockHeld(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	lock, err := datalock.LockData(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = takeSnapshot(context.Background(), o)
	if !errors.Is(err, datalock.ErrDataInUse) || !strings.Contains(err.Error(), "退出桌面程序") {
		t.Fatalf("应该拒绝并提示先退出桌面程序：%v", err)
	}
	entries, _ := os.ReadDir(o.Copy)
	if len(entries) != 0 {
		t.Fatalf("没拿到锁，不该拷任何东西：%v", entries)
	}
}

// 锁文件不存在：不为了拿锁去建文件（LockExisting），拒绝
func TestSnapshotRefusesWithoutLockFile(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	if err := os.Remove(filepath.Join(f.Data, "go", "backend.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := takeSnapshot(context.Background(), o); err == nil || !strings.Contains(err.Error(), "拿不到数据锁") {
		t.Fatalf("没有锁文件应该拒绝：%v", err)
	}
	if _, err := os.Stat(filepath.Join(f.Data, "go", "backend.lock")); err == nil {
		t.Fatal("不能为了拿锁把锁文件建出来")
	}
}

// 固定位置缺库时拒绝，不能回落到 Bun 原库。
func TestSnapshotRefusesBunOriginal(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	if err := os.Remove(f.memDB()); err != nil {
		t.Fatal(err)
	}
	_, err := takeSnapshot(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("Bun 的原库应该拒绝：%v", err)
	}
	if entries, _ := os.ReadDir(o.Copy); len(entries) != 0 {
		t.Fatalf("不该拷任何东西：%v", entries)
	}
}

// Go 副本的名字下其实是 Bun 原库的硬链接：路径一眼看上去没问题，W5 的 CheckMemoryPath 靠硬链接数认出来，一个文件也不拷
func TestSnapshotRefusesHardLinkedBunOriginal(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("CheckMemoryPath 只在 Windows 上查硬链接数")
	}
	f, o := snapFixture(t)
	if err := os.Remove(f.memDB()); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(f.Data, "characters", testChar, "memory.db"), f.memDB()); err != nil {
		t.Skipf("建不了硬链接：%v", err)
	}
	_, err := takeSnapshot(context.Background(), o)
	if err == nil || !errors.Is(err, paths.ErrMemoryPath) {
		t.Fatalf("Bun 原库的硬链接应该被 CheckMemoryPath 拒绝：%v", err)
	}
	if entries, _ := os.ReadDir(o.Copy); len(entries) != 0 {
		t.Fatalf("不该拷任何东西：%v", entries)
	}
}

// 拷的时候有人在写：前后指纹对不上就整个重拷；第二次静止了才算数；一直在变就报错、不交出副本
func TestSnapshotRetriesWhenSourceChanges(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	chat := filepath.Join(f.Data, "go", "chat", "qq.db")
	bump := func() {
		// 改修改时间，等于"有人动过这个文件"
		fi, _ := os.Stat(chat)
		if err := os.Chtimes(chat, fi.ModTime(), fi.ModTime().Add(7_000_000_000)); err != nil {
			t.Fatal(err)
		}
	}
	o.hookMid = func(attempt int) {
		if attempt == 1 {
			bump()
		}
	}
	snap, err := takeSnapshot(context.Background(), o)
	if err != nil || snap.Attempts != 2 {
		t.Fatalf("第一次变了，第二次应该拷到：%+v %v", snap, err)
	}

	o2 := o
	o2.Copy = filepath.Join(t.TempDir(), "copy2")
	if err := os.MkdirAll(o2.Copy, 0o755); err != nil {
		t.Fatal(err)
	}
	o2.hookMid = func(int) { bump() }
	if _, err := takeSnapshot(context.Background(), o2); !errors.Is(err, errNoStill) {
		t.Fatalf("一直在变应该报 errNoStill：%v", err)
	}
	// 记忆库在拷的时候被改了（application_id 在文件头，改它不坏库）：哈希对不上，同样重拷
	o3 := o
	o3.Copy = filepath.Join(t.TempDir(), "copy3")
	if err := os.MkdirAll(o3.Copy, 0o755); err != nil {
		t.Fatal(err)
	}
	o3.hookMid = func(attempt int) {
		if attempt != 1 {
			return
		}
		fh, err := os.OpenFile(f.memDB(), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		if _, err := fh.WriteAt([]byte{0, 0, 0, 7}, 68); err != nil {
			t.Fatal(err)
		}
	}
	snap, err = takeSnapshot(context.Background(), o3)
	if err != nil || snap.Attempts != 2 {
		t.Fatalf("记忆库被改了应该重拷一次：%+v %v", snap, err)
	}
}

// 拷出来的副本和源对不上（拷坏了）：不继续，不交出副本
func TestSnapshotDetectsBadCopy(t *testing.T) {
	t.Parallel()
	_, o := snapFixture(t)
	o.hookMid = func(int) {
		p := filepath.Join(o.Copy, "go", "characters", testChar, "memory.db")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b[len(b)-1] ^= 0xff
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := takeSnapshot(context.Background(), o); err == nil || !strings.Contains(err.Error(), "和源对不上") {
		t.Fatalf("副本被改了应该报对不上：%v", err)
	}
}

// 副本的 integrity_check 没过（聊天库的副本拷坏了，哈希只核对记忆库，聊天库靠它）：不是直接报错，而是重拷；
// 重拷以后过了就交出副本，一直不过才报错，说的是 integrity_check 而不是"库一直在变"
func TestSnapshotRecopiesWhenCopyFailsIntegrity(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	breakCopy := func() {
		p := filepath.Join(o.Copy, "go", "chat", "qq.db")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		copy(b[:16], []byte("not a database!!")) // 文件头的魔数坏了，打开就说不是库
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("第一次坏、第二次好：重拷一次", func(t *testing.T) {
		o1 := o
		o1.hookMid = func(attempt int) {
			if attempt == 1 {
				breakCopy()
			}
		}
		snap, err := takeSnapshot(context.Background(), o1)
		if err != nil || snap.Attempts != 2 || !snap.IntegrityOK {
			t.Fatalf("第一次坏了应该重拷一次再交出副本：%+v %v", snap, err)
		}
		if err := checkCopies(o.Copy, snap.Files); err != nil {
			t.Fatalf("交出去的副本该是好的：%v", err)
		}
	})
	t.Run("一直坏：报 integrity_check，不是库一直在变", func(t *testing.T) {
		o2 := o
		o2.Copy = filepath.Join(t.TempDir(), "copy2")
		if err := os.MkdirAll(o2.Copy, 0o755); err != nil {
			t.Fatal(err)
		}
		o2.hookMid = func(int) {
			p := filepath.Join(o2.Copy, "go", "chat", "qq.db")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			copy(b[:16], []byte("not a database!!"))
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := takeSnapshot(context.Background(), o2)
		if err == nil || !strings.Contains(err.Error(), "integrity_check 试了 3 次都没过") || errors.Is(err, errNoStill) {
			t.Fatalf("一直坏该报 integrity_check 试了 3 次都没过：%v", err)
		}
	})
	// 源库本身没动过，数据锁拿过又放了：再拷一次不会被"数据在用"拒绝
	if _, err := takeSnapshot(context.Background(), snapOpts{Data: f.Data, Copy: func() string {
		d := filepath.Join(t.TempDir(), "copy3")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}(), Char: testChar}); err != nil {
		t.Fatalf("前面几次失败以后数据锁该已经放了：%v", err)
	}
}

// Bun 在写本地私聊和记忆（它拿着 go/local.lock，Go 不在）：拒绝，副本里一个文件都没有（复核 R3）
func TestSnapshotRefusesWhenLocalLockHeld(t *testing.T) {
	t.Parallel()
	f, o := snapFixture(t)
	bun, err := datalock.LockLocal(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer bun.Close()
	_, err = takeSnapshot(context.Background(), o)
	if !errors.Is(err, datalock.ErrLocalInUse) || !strings.Contains(err.Error(), "Bun") {
		t.Fatalf("应该拒绝并提示先退出 Bun 后台：%v", err)
	}
	entries, _ := os.ReadDir(o.Copy)
	if len(entries) != 0 {
		t.Fatalf("没拿到锁，不该拷任何东西：%v", entries)
	}
	if l, err := datalock.LockData(f.Data); err != nil {
		t.Fatalf("拒绝之后不该占着 backend.lock：%v", err)
	} else {
		l.Close()
	}
}
