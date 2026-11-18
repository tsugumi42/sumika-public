package embed

import (
	"reflect"
	"testing"
	"time"
)

func TestLocalStatusNotifiesOutsideLock(t *testing.T) {
	var local *Local
	var got []Status
	local = NewLocal(LocalConfig{OnStatus: func(s Status) {
		current, _ := local.Status()
		if current != s {
			t.Errorf("状态通知=%s，当前=%s", s, current)
		}
		got = append(got, s)
	}}, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		local.Start() // 没配全也要通知 off，宿主才能提醒。
		local.Start() // 同状态、同原因也只通知一次。
		local.set(StatusStarting, "")
		local.set(StatusReady, "")
		local.set(StatusDown, "编的退出原因")
		local.set(StatusDown, "编的另一次退出原因")
		if _, why := local.Status(); why != "编的另一次退出原因" {
			t.Errorf("重复 down 仍应更新最近原因：%q", why)
		}
		local.set(StatusReady, "")
		local.set(StatusOff, "")
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("状态回调在锁内调用 Status，死锁了")
	}
	want := []Status{StatusOff, StatusStarting, StatusReady, StatusDown, StatusReady, StatusOff}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("启动失败、退出、恢复通知=%v，要 %v", got, want)
	}
}
