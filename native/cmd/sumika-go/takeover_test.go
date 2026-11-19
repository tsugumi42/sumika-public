package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/channel/sendlock"
)

func TestFirstTakeoverCLI(t *testing.T) {
	for _, kind := range []string{"缺显式确认", "缺数据目录", "归档模式", "已有记录"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			args := []string{"-first-takeover", "-account", "90001", "-data", dir}
			switch kind {
			case "缺数据目录":
				args = []string{"-first-takeover", "-account", "90001", "-confirm-bun-watchdog-stopped"}
			case "归档模式":
				args = append(args, "-confirm-bun-watchdog-stopped", "-mode", "archive")
			case "已有记录":
				args = append(args, "-confirm-bun-watchdog-stopped")
				if err := os.MkdirAll(filepath.Join(dir, "go"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sendlock.LockFile(dir), []byte("编的已有记录"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), args, &stdout, &stderr, refuseLegacy)
			want := exitUsage
			if kind == "已有记录" {
				want = exitError
			}
			if code != want || stdout.Len() != 0 {
				t.Fatalf("首次接管拒绝结果不对：code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if kind == "已有记录" && !strings.Contains(stderr.String(), "已有交接记录") {
				t.Fatalf("没有走首次接管入口：%s", stderr.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
				t.Fatalf("接管入口启动了后台日志：%v", err)
			}
			if kind == "已有记录" {
				b, err := os.ReadFile(sendlock.LockFile(dir))
				if err != nil || string(b) != "编的已有记录" {
					t.Fatalf("覆盖了记录：%s %v", b, err)
				}
			}
		})
	}
}
