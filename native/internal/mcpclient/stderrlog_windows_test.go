package mcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 真起一个往 stderr 说话的程序：日志文件单独一份，凭据抹掉，握手失败的原因照旧带 stderr 末尾；没设日志目录就不写文件。
func TestStdioStderrGoesToLogFile(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(t.TempDir(), "logs", "mcp")
	boom := filepath.Join(dir, "boom.cmd")
	if err := os.WriteFile(boom, []byte("@echo off\r\necho loading 1>&2\r\necho token=SECRETVALUE rejected by upstream 1>&2\r\necho Authorization: Bearer abc123xyz 1>&2\r\nexit /b 3\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ServerConfig{ID: "boom", Command: boom, Env: map[string]string{"API_KEY": "SECRETVALUE"}, Sumika: ServerPolicy{Enabled: true}}

	off := New(mcpFixtureStore(t))
	defer off.Close()
	if err := off.Connect(t.Context(), cfg); err == nil {
		t.Fatal("该握手失败")
	}
	if _, err := os.Stat(logs); !os.IsNotExist(err) {
		t.Fatalf("没设日志目录不该写文件：%v", err)
	}

	client := New(mcpFixtureStore(t))
	defer client.Close()
	client.SetLogDir(logs)
	err := client.Connect(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "它最后说的是") || !strings.Contains(err.Error(), "rejected by upstream") {
		t.Fatalf("握手失败的原因照旧带 stderr：%v", err)
	}
	b, rerr := os.ReadFile(filepath.Join(logs, "boom.log"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	got := string(b)
	for _, want := range []string{"loading", "rejected by upstream", "Authorization: Bearer ***"} {
		if !strings.Contains(got, want) {
			t.Fatalf("日志里该有 %q：\n%s", want, got)
		}
	}
	for _, bad := range []string{"SECRETVALUE", "abc123xyz"} {
		if strings.Contains(got, bad) {
			t.Fatalf("凭据 %q 写进了日志文件：\n%s", bad, got)
		}
	}
}
