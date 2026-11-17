package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

// 全新的后台没有故障：faults 是空的；retry 全部什么也不做且不报错；点名一个没故障的会话重试，明说没有故障，不说成已恢复。
func TestKernelCtlWithoutFaults(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	faults, err := a.KernelFaults(context.Background())
	if err != nil || len(faults.Faults) != 0 {
		t.Fatalf("%+v %v", faults, err)
	}
	out, err := a.KernelRetry(context.Background(), "", "")
	if err != nil || len(out.Cleared) != 0 || len(out.Remaining) != 0 {
		t.Fatalf("没有故障时重试全部该什么也不做：%+v %v", out, err)
	}
	_, err = a.KernelRetry(context.Background(), "c1", "console:c1")
	var ce *ctlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctlproto.CodeFailed || !strings.Contains(ce.Msg, "没有要重试的故障") {
		t.Fatalf("点名没有故障的会话该明说没有：%v", err)
	}
	var nilApp *App
	if _, err := nilApp.KernelFaults(context.Background()); !errors.Is(err, ErrNoKernel) {
		t.Fatalf("内核没建好该报没准备好：%v", err)
	}
}
