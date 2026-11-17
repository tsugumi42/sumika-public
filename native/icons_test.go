package main

import (
	"bytes"
	"fmt"
	"image/png"
	"io/fs"
	"slices"
	"testing"
)

// 挑选规则：有一样大的用它，没有就用比它大的里最小的，都比它小用最大的；取不到尺寸（0、负数）用最小的
func TestPickTraySize(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ want, got int }{
		{16, 16}, {20, 20}, {24, 24}, {30, 32}, {32, 32}, {36, 40}, {40, 40}, {48, 48}, {64, 64},
		{80, 64}, {1000, 64}, {17, 20}, {21, 24}, {49, 64},
		{0, 16}, {-1, 16}, {-100, 16},
	} {
		if got := pickTraySize(c.want); got != c.got {
			t.Errorf("系统要 %d 像素的小图标：挑了 %d，要 %d", c.want, got, c.got)
		}
	}
}

// 嵌进来的每张托盘图：文件名里的边长和图真正的边长一致，traySizes 和嵌进来的文件一一对上
func TestEmbeddedTrayIconsMatchTheirNames(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(trayFS, "assets/tray")
	if err != nil {
		t.Fatal(err)
	}
	var have []int
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "tray-%d.png", &n); err != nil || e.Name() != fmt.Sprintf("tray-%d.png", n) {
			t.Errorf("托盘图的文件名不是 tray-<边长>.png：%s", e.Name())
			continue
		}
		have = append(have, n)
		data, err := fs.ReadFile(trayFS, "assets/tray/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Errorf("%s 不是 PNG：%v", e.Name(), err)
			continue
		}
		if cfg.Width != n || cfg.Height != n {
			t.Errorf("%s 解出来是 %d×%d，文件名说是 %d", e.Name(), cfg.Width, cfg.Height, n)
		}
	}
	slices.Sort(have)
	if !slices.Equal(have, traySizes) {
		t.Errorf("嵌进来的托盘图边长 %v，traySizes 是 %v，两边要一样", have, traySizes)
	}
	if !slices.IsSorted(traySizes) {
		t.Errorf("traySizes 要从小到大：%v", traySizes)
	}
}

// trayIcon 挑出来的每一档都能取到，而且是对应尺寸的图
func TestTrayIconReturnsThePickedSize(t *testing.T) {
	t.Parallel()
	for _, n := range traySizes {
		data, err := trayFS.ReadFile(fmt.Sprintf("assets/tray/tray-%d.png", pickTraySize(n)))
		if err != nil {
			t.Fatalf("取不到 %d 那张：%v", n, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width != n {
			t.Errorf("要 %d 的，拿到 %v %v", n, cfg, err)
		}
	}
	// 当前机器上实际会用哪一张：能解码，边长是 traySizes 里的一个
	cfg, err := png.DecodeConfig(bytes.NewReader(trayIcon()))
	if err != nil {
		t.Fatalf("trayIcon() 不是 PNG：%v", err)
	}
	if !slices.Contains(traySizes, cfg.Width) {
		t.Errorf("trayIcon() 的边长 %d 不在 %v 里", cfg.Width, traySizes)
	}
	if want := pickTraySize(traySmallIconSize()); cfg.Width != want {
		t.Errorf("系统小图标 %d 像素：trayIcon() 用了 %d，要 %d", traySmallIconSize(), cfg.Width, want)
	}
}

// 窗口图标是一张 256×256 的 PNG
func TestWindowIconIs256Square(t *testing.T) {
	t.Parallel()
	cfg, err := png.DecodeConfig(bytes.NewReader(windowIconPNG))
	if err != nil {
		t.Fatalf("窗口图标不是 PNG：%v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Errorf("窗口图标 %d×%d，要 256×256", cfg.Width, cfg.Height)
	}
}
