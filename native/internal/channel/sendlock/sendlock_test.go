package sendlock

// 第 3 步的单元测试（规格-QQ发送上线 第十一节第 3 步）：接发送权要同时满足意图是 go、记录是 protocol 1 的 released
// （或上一次是自己没交出）、号对得上、锁拿得到；接过来了写 holding，交出去先写 released 再放锁。
// 号一律编的，目录只用 t.TempDir()。两个进程的那几条在 handover_test.go。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testAccount  = "100" // 编的小号
	otherAccount = "200"
)

// writeIntent 写意图文件（qq-sender.json）
func writeIntent(t *testing.T, dir string, in Intent) {
	t.Helper()
	path := IntentFile(dir)
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRecord 直接把交接记录写进锁文件（模拟上一次持有的一方放锁以后留下的那份）
func writeRecordFile(t *testing.T, dir string, rec Record) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LockFile(dir), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// freshReleasedRecord 是正常交出来以后的那份记录
func freshReleasedRecord(account string) Record {
	return Record{Protocol: Protocol, Account: account, Holder: HolderBun, State: StateReleased, PID: 999999, ReleasedAt: 1}
}

// readRecordFile 从另一个句柄读记录（锁在内容之外，不挡读）
func readRecordFile(t *testing.T, dir string) Record {
	t.Helper()
	b, err := os.ReadFile(LockFile(dir))
	if err != nil {
		t.Fatalf("读锁文件：%v", err)
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("锁文件里不是 JSON：%q", b)
	}
	return rec
}

// assertLockFree 说锁没被谁拿着：新开一个句柄锁得上，然后放开
func assertLockFree(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(LockFile(dir), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("打开锁文件：%v", err)
	}
	defer f.Close()
	if err := lockExclusive(f); err != nil {
		t.Fatalf("没接过发送权，锁却还拿着：%v", err)
	}
	if err := unlockExclusive(f); err != nil {
		t.Fatalf("放测试自己的锁：%v", err)
	}
}

// 接发送权要四条都齐：意图是 go、号对得上、记录是 protocol 1 的 released（或上一次是自己）、锁拿得到。
// 缺哪一条都返回对应的错误，而且不留下拿着的锁
func TestTakeRefusesWithoutHandover(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  error
	}{
		{"没有意图文件、记录是另一边交出的", func(t *testing.T, dir string) {
			writeRecordFile(t, dir, freshReleasedRecord(testAccount))
		}, ErrIntentMissing},
		{"意图是 bun", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderBun})
			writeRecordFile(t, dir, freshReleasedRecord(testAccount))
		}, ErrIntentNotGo},
		{"意图文件读不懂", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(IntentFile(dir), []byte("{不是 JSON"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrIntent},
		{"意图里有不认识的键", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(IntentFile(dir), []byte(`{"account":"100","holder":"go","多写的一个":1}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrIntent},
		{"意图里的号不是这个号", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: otherAccount, Holder: HolderGo})
			writeRecordFile(t, dir, freshReleasedRecord(testAccount))
		}, ErrAccount},
		{"只有意图、没有锁文件", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
		}, ErrNoRecord},
		{"锁文件是空的（上一次刚建出来就被打断）", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.WriteFile(LockFile(dir), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrNoRecord},
		{"记录不是 protocol 1", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			rec := freshReleasedRecord(testAccount)
			rec.Protocol = 2
			writeRecordFile(t, dir, rec)
		}, ErrProtocol},
		{"记录是 released 却没有放开的时刻", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			rec := freshReleasedRecord(testAccount)
			rec.ReleasedAt = 0
			writeRecordFile(t, dir, rec)
		}, ErrProtocol},
		{"记录里有不认识的键", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			b := []byte(`{"protocol":1,"account":"100","holder":"bun","state":"released","pid":1,"releasedAt":1,"多写的一个":1}`)
			if err := os.WriteFile(LockFile(dir), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrProtocol},
		{"意图里的号不是十进制串", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(IntentFile(dir), []byte(`{"account":"１００","holder":"go"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrIntent},
		{"记录里的号写成了数字", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			b := []byte(`{"protocol":1,"account":100,"holder":"bun","state":"released","pid":1,"releasedAt":1}`)
			if err := os.WriteFile(LockFile(dir), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrProtocol},
		{"记录里的号不是十进制串", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			b := []byte(`{"protocol":1,"account":"100a","holder":"bun","state":"released","pid":1,"releasedAt":1}`)
			if err := os.WriteFile(LockFile(dir), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrProtocol},
		{"记录里的号不是这个号", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			writeRecordFile(t, dir, freshReleasedRecord(otherAccount))
		}, ErrAccount},
		{"记录还是 holding，持有的是另一边（被强杀不算交出）", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			rec := freshReleasedRecord(testAccount)
			rec.State = StateHolding
			rec.ReleasedAt = 0
			writeRecordFile(t, dir, rec)
		}, ErrUnreleasedOther},
		{"记录读不懂", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(LockFile(dir), []byte("不是交接记录"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrTorn},
		{"记录写了一半（截断在键中间）", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.WriteFile(LockFile(dir), []byte(`{"protocol":1,"acc`), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrTorn},
		{"记录是完整的 JSON 但 protocol 是 2（不是半截）", func(t *testing.T, dir string) {
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
			if err := os.WriteFile(LockFile(dir), []byte(`{"protocol":2}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrProtocol},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.setup(t, dir)
			h, err := Take(dir, testAccount)
			if h != nil {
				t.Fatalf("没交接也接过了发送权")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("拿到的是 %v，要 %v", err, c.want)
			}
			if strings.Contains(err.Error(), testAccount) || strings.Contains(err.Error(), otherAccount) {
				t.Fatalf("错误里带了号：%v", err)
			}
			if _, statErr := os.Stat(LockFile(dir)); statErr == nil {
				assertLockFree(t, dir)
			}
		})
	}
}

// 只有意图、没有锁文件时，连空的锁文件都不建：Bun 靠"文件本来不存在"识别第一次建立协议
func TestTakeNoRecordDoesNotCreateLockFile(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	if _, err := Take(dir, testAccount); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("没有记录也接了：%v", err)
	}
	if _, err := os.Stat(LockFile(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没接过来却建出了锁文件（Bun 会当成不是第一次）：%v", err)
	}
}

// 接过来了：记录写成 holding（protocol 1、holder go、本进程 pid、没有 releasedAt）；持有期间别的句柄拿不到；
// 交出去：先写 released（带毫秒时刻）再放锁，之后才接得回来
func TestTakeWritesHoldingAndReleaseWritesReleased(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	before := time.Now().UnixMilli()
	h, err := Take(dir, testAccount)
	if err != nil {
		t.Fatalf("该接得过来：%v", err)
	}
	if h == nil {
		t.Fatal("接过来了却没给 holder")
	}
	if h.Resumed {
		t.Fatal("正常交接过来的，不该报成接着上一次")
	}
	rec := readRecordFile(t, dir)
	if rec.Protocol != Protocol || rec.Account != testAccount || rec.Holder != HolderGo || rec.State != StateHolding || rec.PID != os.Getpid() || rec.ReleasedAt != 0 {
		t.Fatalf("接手以后的记录是 %+v", rec)
	}
	// 持有期间另一个句柄拿不到（同一进程的第二个句柄也会被 Windows 拦下）
	if _, err := Take(dir, testAccount); !errors.Is(err, ErrLocked) {
		t.Fatalf("有人拿着锁，第二个接手拿到的是 %v，要 %v", err, ErrLocked)
	}

	if err := h.Release(); err != nil {
		t.Fatalf("交出去：%v", err)
	}
	rec = readRecordFile(t, dir)
	if rec.State != StateReleased || rec.Holder != HolderGo || rec.PID != os.Getpid() || rec.ReleasedAt < before || rec.ReleasedAt > time.Now().UnixMilli() {
		t.Fatalf("交出去以后的记录是 %+v", rec)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("重复交出应该是空操作：%v", err)
	}
	h2, err := Take(dir, testAccount)
	if err != nil {
		t.Fatalf("记录是 released、锁也放开了，应该接得回来：%v", err)
	}
	if err := h2.Release(); err != nil {
		t.Fatalf("第二次交出去：%v", err)
	}
}

// 记录停在 holding、记的是自己（崩溃或断电以后重启），意图也指向自己：直接接着拿，日志那边靠 Resumed 认出来。
// 中间没有另一边插进来，不该让日常回复停住（规格 5.2 锁的细节，2026-10-28 改）
func TestTakeResumesAfterOwnCrash(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, Record{Protocol: Protocol, Account: testAccount, Holder: HolderGo, State: StateHolding, PID: 999999})

	h, err := Take(dir, testAccount)
	if err != nil {
		t.Fatalf("上一次是自己持有的，该接着拿：%v", err)
	}
	if !h.Resumed {
		t.Fatal("接着上一次拿的，没说是 Resumed")
	}
	if !h.CanSend() {
		t.Fatal("自己的 holding 重启后拿到了锁，却不能发送")
	}
	rec := readRecordFile(t, dir)
	if rec.State != StateHolding || rec.Holder != HolderGo || rec.PID != os.Getpid() {
		t.Fatalf("接着拿以后的记录是 %+v", rec)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("交出去：%v", err)
	}
	if h.CanSend() {
		t.Fatal("重启接过的锁交出后仍允许发送")
	}
	if rec = readRecordFile(t, dir); rec.State != StateReleased || rec.ReleasedAt == 0 {
		t.Fatalf("接着拿以后交出去的记录是 %+v", rec)
	}
}

// 意图是别的号：不接。两种写法（意图和记录都写别的号、只有意图写别的号）都不接，错误里不带号
func TestTakeRefusesOtherAccountsIntent(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: otherAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(otherAccount))
	_, err := Take(dir, testAccount)
	if !errors.Is(err, ErrAccount) {
		t.Fatalf("意图是别的号，拿到的是 %v，要 %v", err, ErrAccount)
	}
	if err != nil && (strings.Contains(err.Error(), testAccount) || strings.Contains(err.Error(), otherAccount)) {
		t.Fatalf("错误里带了号：%v", err)
	}
}

// fillRecordOnRetry 让读的一方第一次等待重读时，写的一方正好把整段记录写完（模拟写在截断和写完之间）；
// 不靠定时器对时序，机器再忙也一样。返回记录下来的每次等待
func fillRecordOnRetry(t *testing.T, content []byte, path string) *[]time.Duration {
	t.Helper()
	w, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	orig := recordRetrySleep
	t.Cleanup(func() { recordRetrySleep = orig })
	var slept []time.Duration
	recordRetrySleep = func(d time.Duration) {
		slept = append(slept, d)
		if _, err := w.WriteAt(content, 0); err != nil {
			t.Errorf("补全记录：%v", err)
		}
		if err := w.Truncate(int64(len(content))); err != nil {
			t.Errorf("补全记录：%v", err)
		}
	}
	return &slept
}

// 读到写了一半的记录：隔 50 毫秒重读。写的一方把整段换上来以后就读得到。
// 空文件也是"写到一半"（刚截断、还没写进来），同样重读
func TestReadRecordRetriesTornWrite(t *testing.T) {
	full := []byte(`{"protocol":1,"account":"100","holder":"go","state":"released","pid":1,"releasedAt":7}`)
	cases := []struct {
		name  string
		start []byte
	}{
		{"半截 JSON", []byte(`{"protocol":1,"account":"100","holder":"go","state":"rel`)},
		{"空文件", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(LockFile(dir), c.start, 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := openLockFile(dir)
			if err != nil {
				t.Fatalf("打开锁文件：%v", err)
			}
			defer f.Close()
			slept := fillRecordOnRetry(t, full, LockFile(dir))

			rec, err := readRecord(f)
			if err != nil {
				t.Fatalf("写的一方补全以后该读得到：%v", err)
			}
			if rec.State != StateReleased || rec.ReleasedAt != 7 {
				t.Fatalf("读到的是 %+v", rec)
			}
			if len(*slept) != 1 || (*slept)[0] != recordRetryDelay {
				t.Fatalf("第一遍读到一半就该隔 %v 再读一次，实际等了 %v", recordRetryDelay, *slept)
			}
		})
	}
}

// 一直是半截：读满 3 次重读（4 遍）就放弃，按 ErrTorn（没写完整）处理，不当成 protocol 1
func TestReadRecordGivesUpAfterRetries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LockFile(dir), []byte(`{"protocol":1,"acco`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openLockFile(dir)
	if err != nil {
		t.Fatalf("打开锁文件：%v", err)
	}
	defer f.Close()
	before := time.Now()
	if _, err := readRecord(f); !errors.Is(err, ErrTorn) {
		t.Fatalf("一直是半截，拿到的是 %v，要 %v", err, ErrTorn)
	}
	if d := time.Since(before); d < recordRetries*recordRetryDelay {
		t.Fatalf("%d 次重读一共该等 %v，实际只用了 %v", recordRetries, recordRetries*recordRetryDelay, d)
	}
}

// 意图文件不见了，但交接记录显示之前是 Go 持有或 Go 交出的：照常接手，补回意图文件，标出来让宿主提醒（ADR-311 第 3 条）
func TestTakeRecoversMissingIntentFromGoRecord(t *testing.T) {
	for _, state := range []string{StateReleased, StateHolding} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			writeRecordFile(t, dir, Record{Protocol: Protocol, Account: testAccount, Holder: HolderGo, State: state, PID: 999999, ReleasedAt: 1})
			h, err := Take(dir, testAccount)
			if err != nil {
				t.Fatalf("Go 接管过的应能接手：%v", err)
			}
			defer h.Release()
			if !h.IntentRecovered || h.IntentWriteErr != nil || !h.CanSend() {
				t.Fatalf("要标出补回了意图：recovered=%v err=%v", h.IntentRecovered, h.IntentWriteErr)
			}
			in, err := ReadIntent(dir)
			if err != nil || in.Holder != HolderGo || in.Account != testAccount {
				t.Fatalf("意图文件要补成这个号的 go：%+v %v", in, err)
			}
		})
	}
}
