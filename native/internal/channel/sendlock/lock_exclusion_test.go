//go:build windows || linux || darwin || freebsd || netbsd || openbsd || dragonfly

package sendlock

// 独占锁这一层的契约，Windows（LockFileEx）和 Unix（flock）两份实现各自要满足同一组：
// 两个持有者互斥（同一进程里各自打开一次也互斥）、抢不到立刻失败且能认出是"别人拿着"、释放后能再拿、
// 持锁期间别的句柄照样读得到记录内容。往外发消息的门槛靠它，任何一条不成立都不能放行。

import (
	"io"
	"testing"
)

func TestLockExclusiveMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	a, err := openLockFile(dir)
	if err != nil {
		t.Fatalf("打开锁文件 a：%v", err)
	}
	defer a.Close()
	b, err := openLockFile(dir)
	if err != nil {
		t.Fatalf("打开锁文件 b：%v", err)
	}
	defer b.Close()

	if err := lockExclusive(a); err != nil {
		t.Fatalf("a 第一个抢，应当拿到：%v", err)
	}
	err = lockExclusive(b)
	if err == nil {
		_ = unlockExclusive(b)
		t.Fatal("a 拿着的时候 b 也抢到了：独占锁没有互斥")
	}
	if !isLockBusy(err) {
		t.Fatalf("b 抢不到应当被认成\"别人拿着\"，现在是 %v", err)
	}

	// 持锁期间别的句柄读得到记录内容
	if _, err := b.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("b 定位：%v", err)
	}
	if body, err := io.ReadAll(b); err != nil || len(body) == 0 {
		t.Fatalf("a 持锁时 b 应当读得到记录，读到 %d 字节、错误 %v", len(body), err)
	}

	if err := unlockExclusive(a); err != nil {
		t.Fatalf("a 放锁：%v", err)
	}
	if err := lockExclusive(b); err != nil {
		t.Fatalf("a 放锁以后 b 应当拿得到：%v", err)
	}
	// 反过来：b 拿着，a 抢不到
	if err := lockExclusive(a); err == nil || !isLockBusy(err) {
		_ = unlockExclusive(a)
		t.Fatalf("b 拿着的时候 a 应当抢不到且认成\"别人拿着\"，现在是 %v", err)
	}
	if err := unlockExclusive(b); err != nil {
		t.Fatalf("b 放锁：%v", err)
	}
}

// 关掉句柄也放锁（进程退出时系统就是这样放的）：不调 unlockExclusive、直接 Close，别人接着拿得到
func TestLockExclusiveReleasedByClose(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	a, err := openLockFile(dir)
	if err != nil {
		t.Fatalf("打开锁文件 a：%v", err)
	}
	if err := lockExclusive(a); err != nil {
		t.Fatalf("a 抢锁：%v", err)
	}
	b, err := openLockFile(dir)
	if err != nil {
		t.Fatalf("打开锁文件 b：%v", err)
	}
	defer b.Close()
	if err := lockExclusive(b); err == nil {
		t.Fatal("a 拿着的时候 b 也抢到了")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("关 a：%v", err)
	}
	if err := lockExclusive(b); err != nil {
		t.Fatalf("a 关掉以后 b 应当拿得到：%v", err)
	}
	_ = unlockExclusive(b)
}
