package selfcheck_test

import (
	"context"
	"errors"
	"testing"

	"sumika-native/internal/selfcheck"
)

// S2：入库失败当天 ≥ 1 就是 bad；没有失败是 ok；例子只有错误类别，不带原文
func TestS2StoreFailed(t *testing.T) {
	c := findCheck(t, "intake.store_failed", selfcheck.DefaultThresholds())
	live := selfcheck.NewCounters()
	if r := c.Eval(context.Background(), envAt(t0, live)); r.State != selfcheck.Ok || r.Value != 0 {
		t.Fatalf("没有失败应是 ok：%+v", r)
	}
	live.StoreFailed(selfcheck.StoreErrorClass(errors.New("database is locked (5) (SQLITE_BUSY) 会话 qq:group:123456 的内容")))
	r := c.Eval(context.Background(), envAt(t0, live))
	if r.State != selfcheck.Bad || r.Value != 1 || r.Threshold != 1 || r.Example["errorClass"] != selfcheck.StoreBusy {
		t.Fatalf("一次失败就该是 bad，例子只带类别：%+v", r)
	}
	for _, v := range r.Example {
		if v != selfcheck.StoreBusy {
			t.Fatalf("例子里不能带别的东西（会话号、内容）：%v", r.Example)
		}
	}
}

func TestStoreErrorClass(t *testing.T) {
	for _, tc := range []struct{ msg, want string }{
		{"database or disk is full (13)", selfcheck.StoreFull},
		{"write: no space left on device", selfcheck.StoreFull},
		{"database is locked (5) (SQLITE_BUSY)", selfcheck.StoreBusy},
		{"attempt to write a readonly database (8)", selfcheck.StoreReadonly},
		{"constraint failed: UNIQUE", selfcheck.StoreOther},
		{"", selfcheck.StoreOther},
	} {
		if got := selfcheck.StoreErrorClass(errors.New(tc.msg)); got != tc.want {
			t.Errorf("%q → %q，想要 %q", tc.msg, got, tc.want)
		}
	}
	if selfcheck.StoreErrorClass(nil) != "" {
		t.Error("nil 错误没有类别")
	}
}
