package main

import (
	"runtime"
	"testing"
)

// 退路的每一级：按 DPI 取到就用它；取不到（0）退到不跟缩放的；再取不到用 16
func TestSmallIconSizeFallbacks(t *testing.T) {
	t.Parallel()
	val := func(n int) func() int { return func() int { return n } }
	for _, c := range []struct {
		name    string
		m       iconMetrics
		want    int
		wantTry int // 期望挑出的托盘图
	}{
		{"按 DPI 取到 20（125%）", iconMetrics{val(20), val(16)}, 20, 20},
		{"按 DPI 取到 32（200%）", iconMetrics{val(32), val(16)}, 32, 32},
		{"按 DPI 取不到，用不跟缩放的", iconMetrics{val(0), val(24)}, 24, 24},
		{"两个都取不到", iconMetrics{val(0), val(0)}, 16, 16},
		{"负数当取不到", iconMetrics{val(-1), val(-1)}, 16, 16},
		{"按 DPI 取到 30，挑 32", iconMetrics{val(30), val(0)}, 30, 32},
	} {
		got := smallIconSize(c.m)
		if got != c.want {
			t.Errorf("%s：尺寸 %d，要 %d", c.name, got, c.want)
		}
		if pick := pickTraySize(got); pick != c.wantTry {
			t.Errorf("%s：挑了 %d，要 %d", c.name, pick, c.wantTry)
		}
	}
}

// 真去问 user32：两级各自都取得到，而且是 16 的整数缩放档之间（100%～400% 缩放），不会是 0 或负数
func TestSystemIconMetricsAskedFromWindows(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]func() int{"按 DPI": systemIconMetrics.forDPI, "不跟缩放": systemIconMetrics.plain} {
		if n := f(); n < 16 || n > 64 {
			t.Errorf("%s取到系统小图标边长 %d，应该在 16～64 之间", name, n)
		}
	}
	if n := traySmallIconSize(); n != systemIconMetrics.forDPI() {
		t.Errorf("这台机器是 Windows 10 以上，应该走按 DPI 的那一级：%d 对 %d", n, systemIconMetrics.forDPI())
	}
}

// 这个包的 .syso 真的链进了测试程序（和发出去的 exe 同样的链接方式）：mygo 的窗口类用资源 1 当窗口图标，
// 没有它 setWindowIcon 就会退到 SetIcon。只有 amd64、arm64 提交了资源
func TestExeCarriesIconResource(t *testing.T) {
	t.Parallel()
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("%s 没有提交图标资源", runtime.GOARCH)
	}
	if !exeHasIconResource() {
		t.Fatalf("exe 里没有图标资源 1：rsrc_windows_%s.syso 没链进来（文件名、位置不对，或者被 .gitignore 挡了）", runtime.GOARCH)
	}
}
