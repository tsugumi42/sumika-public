//go:build windows

package sendlock

// 锁的偏移是规格 5.2 写死、要和 Bun 一字不差的那个数（2^62、1 个字节）。这一条直接按那个数自己锁一遍，
// 换掉 lockOffset 就会红：只靠两边都用同一个常量是查不出来的。

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLockRangeIsOutsideContent(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	f, err := openLockFile(dir)
	if err != nil {
		t.Fatalf("打开锁文件：%v", err)
	}
	defer f.Close()
	// 偏移 2^62：低 32 位是 0，高 32 位是 0x40000000
	ol := new(windows.Overlapped)
	ol.OffsetHigh = 0x40000000
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		t.Fatalf("按规格的偏移锁 2^62 处的一个字节：%v", err)
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)

	// 锁在文件内容之外：别的一方持锁时，记录照样读得到
	if rec := readRecordFile(t, dir); rec.State != StateReleased || rec.Protocol != Protocol {
		t.Fatalf("锁着的时候记录该照样读得到，现在是 %+v", rec)
	}
	if h, err := Take(dir, testAccount); !errors.Is(err, ErrLocked) {
		if h != nil {
			_ = h.Release()
		}
		t.Fatalf("规格写的那一段锁着，Take 拿到的是 %v，要 %v", err, ErrLocked)
	}
}
