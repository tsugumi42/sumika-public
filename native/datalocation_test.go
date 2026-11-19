package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	backend "sumika-native/internal/app"
)

func TestDataLocationSurface(t *testing.T) {
	for _, width := range []int{900, 2048} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			exe := t.TempDir()
			root := filepath.Join(exe, "当前根目录")
			target := filepath.Join(exe, "新根目录")
			for _, dir := range []string{root, target} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "sumika.example.yml"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := backend.WriteRootFile(exe, root); err != nil {
				t.Fatal(err)
			}
			q := make(chan func(), 8)
			a := &app{realTab: tabData, postFn: func(fn func()) { q <- fn }}
			sh := newShell(a, backend.Options{}, false)
			sh.host = newHost(sh.opt, func(hostState) {})
			defer sh.host.cancel()
			sh.initDataLocationFrom(exe)
			(<-q)()
			tt := testerOf(width, 1030, a.realSettingsView)
			if !tt.HasText("来源：sumika.root") || !tt.HasText("当前根目录："+root) {
				t.Fatalf("缺根和来源：%v", tt.Texts())
			}
			if err := tt.Click("根目录路径"); err != nil {
				t.Fatal(err)
			}
			tt.Key(ui.Cmd, ui.KeyA)
			tt.Type("新根目录")
			if err := tt.Click("保存数据位置"); err != nil {
				t.Fatal(err)
			}
			select {
			case fn := <-q:
				fn()
			case <-time.After(uiWaitLimit):
				t.Fatal("保存没有返回")
			}
			tt.Frame()
			if b, err := os.ReadFile(filepath.Join(exe, "sumika.root")); err != nil || string(b) != "新根目录\n" {
				t.Fatalf("实际文件 %q %v", b, err)
			}
			if !tt.HasText("已保存，重启生效") || a.location.current.Dir != root || sh.opt.Root != root || sh.host.opt.Root != root {
				t.Fatalf("保存提前切根或提示不对：%+v %v", a.location, tt.Texts())
			}
			loc, err := backend.ResourceRootFrom("", exe)
			if err != nil || loc.Dir != target {
				t.Fatalf("重启后没有选新根：%+v %v", loc, err)
			}
			if err := os.Remove(filepath.Join(exe, "sumika.root")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(exe, "sumika.root"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("保存数据位置"); err != nil {
				t.Fatal(err)
			}
			select {
			case fn := <-q:
				fn()
			case <-time.After(uiWaitLimit):
				t.Fatal("失败没有返回")
			}
			tt.Frame()
			if !strings.Contains(a.location.notice, "保存失败：") || !strings.Contains(a.location.notice, filepath.Join(exe, "sumika.root")) || !tt.HasText(a.location.notice) {
				t.Fatalf("写失败没有显示：%v", tt.Texts())
			}
			r, ok := tt.Find("保存数据位置")
			if !ok || r.W < 24 || r.H < 24 || r.X < 0 || r.X+r.W > float32(width) {
				t.Fatalf("按钮尺寸或溢出：%+v width=%v", r, width)
			}
			t.Logf("width=%d save-button=(%.1f,%.1f %.1fx%.1f)", width, r.X, r.Y, r.W, r.H)
		})
	}
}

func TestDataLocationErrorAndDemo(t *testing.T) {
	exe := t.TempDir()
	file := filepath.Join(exe, "sumika.root")
	if err := os.WriteFile(file, []byte("\nmissing"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{realTab: tabData, postFn: func(fn func()) { fn() }}
	sh := newShell(a, backend.Options{}, false)
	sh.host = newHost(sh.opt, func(hostState) {})
	defer sh.host.cancel()
	sh.initDataLocationFrom(exe)
	for _, view := range []func(*ui.Context){a.realSettingsView, a.statusView} {
		tt := testerOf(2048, 1030, view)
		if !tt.HasText(a.location.err.Error()) || !strings.Contains(a.location.err.Error(), "第 2 行") || !strings.Contains(a.location.err.Error(), file) {
			t.Fatalf("解析错误没有文件、行号：%v", tt.Texts())
		}
	}
	sh.opt.Root = exe
	sh.initDataLocationFrom(exe)
	sh.writeDataLocation(exe, "")
	if a.location.current.Source != "命令行 -root" || !strings.Contains(a.location.notice, "下次启动请移除 -root") {
		t.Fatalf("命令行优先提示缺失：%+v", a.location)
	}
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 即使误接了保存回调，演示页也不能显示编辑入口。
	a.demoMode = true
	tt := testerOf(2048, 1030, a.pageDataLocation)
	if _, ok := tt.Find("保存数据位置"); ok {
		t.Error("演示显示保存入口")
	}
	demo := newShell(a, backend.Options{}, true)
	demo.writeDataLocation(exe, "")
	if b, err := os.ReadFile(file); err != nil || string(b) != "original" {
		t.Fatalf("演示改了文件：%q %v", b, err)
	}
}
