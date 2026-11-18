package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 子进程里真的崩一次（另一个协程里没接住的 panic），看 crash.log 里有没有出错信息和调用栈。
// 旧代码（没有 SetCrashOutput）下 crash.log 不存在，这条会红。
func TestCrashLogCapturesPanic(t *testing.T) {
	if dir := os.Getenv("SUMIKA_CRASH_CHILD"); dir != "" {
		if _, err := openCrashLog(dir, time.Now()); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			panic("编造的崩溃：crashlog 测试")
		}()
		<-done
		return
	}
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashLogCapturesPanic$")
	cmd.Env = append(os.Environ(), "SUMIKA_CRASH_CHILD="+data)
	if err := cmd.Run(); err == nil {
		t.Fatal("子进程应该崩溃退出，却正常结束了")
	}
	b, err := os.ReadFile(filepath.Join(data, "logs", crashLogName))
	if err != nil {
		t.Fatalf("没有崩溃记录：%v", err)
	}
	got := string(b)
	for _, want := range []string{"桌面程序启动", "panic", "编造的崩溃：crashlog 测试", "goroutine"} {
		if !strings.Contains(got, want) {
			t.Fatalf("崩溃记录里缺 %q：\n%s", want, got)
		}
	}
}

// 数据目录还没初始化（没有 logs/）时不替它建目录
func TestCrashLogSkipsWithoutLogsDir(t *testing.T) {
	data := t.TempDir()
	f, err := openCrashLog(data, time.Now())
	if err != nil || f != nil {
		t.Fatalf("没有 logs/ 时应该什么都不做，得到 f=%v err=%v", f, err)
	}
	if _, err := os.Stat(filepath.Join(data, "logs")); !os.IsNotExist(err) {
		t.Fatal("不该替它建 logs/")
	}
}
