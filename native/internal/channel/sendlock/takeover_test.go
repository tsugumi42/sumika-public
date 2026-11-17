package sendlock

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"testing"
)

func TestFirstTakeover(t *testing.T) {
	yes := func(context.Context) (bool, error) { return true, nil }
	no := func(context.Context) (bool, error) { return false, nil }
	for _, kind := range []string{"进程还在", "端口还在", "看门未确认", "核对未知", "已有记录", "空记录", "成功"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderBun})
			checks := takeoverChecks{bunStopped: yes, portClosed: yes, watchdogConfirmed: true}
			switch kind {
			case "进程还在":
				checks.bunStopped = no
			case "端口还在":
				checks.portClosed = no
			case "看门未确认":
				checks.watchdogConfirmed = false
			case "核对未知":
				checks.portClosed = func(context.Context) (bool, error) { return false, errors.New("编的错误") }
			case "已有记录":
				writeRecordFile(t, dir, freshReleasedRecord(testAccount))
			case "空记录":
				if err := os.WriteFile(LockFile(dir), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			intentBefore, _ := os.ReadFile(IntentFile(dir))
			recordBefore, recordErr := os.ReadFile(LockFile(dir))
			err := firstTakeover(context.Background(), dir, testAccount, checks)
			if kind != "成功" {
				if err == nil {
					t.Fatal("缺核对或已有记录仍接管了")
				}
				intentAfter, _ := os.ReadFile(IntentFile(dir))
				recordAfter, afterErr := os.ReadFile(LockFile(dir))
				if !bytes.Equal(intentBefore, intentAfter) || !bytes.Equal(recordBefore, recordAfter) || errors.Is(recordErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
					t.Fatal("拒绝时改动了文件")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rec := readRecordFile(t, dir)
			if rec.Holder != HolderGo || rec.State != StateReleased || rec.Origin != "first-takeover" || rec.Takeover == nil || !rec.Takeover.BunStopped || !rec.Takeover.PortClosed || !rec.Takeover.WatchdogConfirmed || rec.Takeover.At == 0 {
				t.Fatalf("首次接管来源或证据丢失：%+v", rec)
			}
			h, err := Take(dir, testAccount)
			if err != nil {
				t.Fatal(err)
			}
			if !h.CanSend() {
				t.Fatal("首次接管以后拿到了锁却不能发")
			}
			if rec := readRecordFile(t, dir); rec.Origin != "first-takeover" || rec.Takeover == nil {
				t.Fatalf("Take 抹掉来源：%+v", rec)
			}
			if err := h.Release(); err != nil {
				t.Fatal(err)
			}
			if h.CanSend() {
				t.Fatal("放锁以后仍能发")
			}
			if rec := readRecordFile(t, dir); rec.Origin != "first-takeover" || rec.Takeover == nil {
				t.Fatalf("Release 抹掉来源：%+v", rec)
			}
		})
	}
}

// 真实系统调用产生拒绝错误，不能只拿注入的 errno 证明 Windows 分支。
func TestTakeoverPortRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", address)
	if err == nil {
		c.Close()
		t.Fatal("刚关闭的端口却连上了")
	}
	if !connectionRefused(err) {
		t.Fatalf("真实拒绝错误未识别：%v", err)
	}
}
