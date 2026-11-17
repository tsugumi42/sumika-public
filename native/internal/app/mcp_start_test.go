package app

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// MCP 服务连不上不能拖垮启动，而且连开两次都要起得来（2026-11-08：写死的 RunID 第二次启动撞 kernel_activities 主键，QQ 接收整个起不来）
func TestMCPServerDownDoesNotBlockStartupTwice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l, err := net.Listen("tcp", "127.0.0.1:0") // 占一个端口再关掉：这个地址确定连不上
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String() + "/mcp"
	l.Close()
	cfg := `{"sumika":{"enabled":true},"mcpServers":{"gone":{"type":"http","url":"` + dead + `","sumika":{"enabled":true,"include":["search"]}}}}`
	if err := os.MkdirAll(filepath.Join(e.data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.data, "config", "mcp.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		a := e.open()
		if _, ok := openAlert(a, "mcp:gone"); !ok {
			a.cleanup()
			t.Fatalf("第 %d 次启动：MCP 没连上要有提醒", i)
		}
		a.cleanup()
	}
}
