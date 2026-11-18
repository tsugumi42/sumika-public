package mcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdQuote(t *testing.T) {
	for in, want := range map[string]string{`-y`: `"-y"`, `@scope/pkg`: `"@scope/pkg"`, `D:\带 空格\目录`: `"D:\带 空格\目录"`, `D:\dir\`: `"D:\dir\\"`, `a&b|c<d>e^f`: `"a&b|c<d>e^f"`} {
		if got, err := cmdQuote(in); err != nil || got != want {
			t.Errorf("%q：得到 %q %v，想要 %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`a"b`, `100%`, "a\nb", "a\x00b"} {
		if _, err := cmdQuote(bad); err == nil {
			t.Errorf("%q 该拒绝", bad)
		}
	}
	if _, err := launchCommand(`C:\x\tool.cmd`, []string{`%PATH%`}); err == nil || !strings.Contains(err.Error(), "tool.cmd") {
		t.Errorf("带百分号的参数经 cmd 启动要拒绝并说是哪个程序：%v", err)
	}
	if _, err := launchCommand(`C:\x\tool.ps1`, nil); err == nil || !strings.Contains(err.Error(), "解释器") {
		t.Errorf("不能直接启动的后缀要说怎么写：%v", err)
	}
}

// 光写 command 名（PATH 里是 .cmd），参数里有空格和 ^ $：经 cmd 垫片启动成功、完成握手、拿到目录。
func TestStdioViaCmdShimOnPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	shim := "@echo off\r\n\"" + exe + "\" %*\r\n"
	if err := os.WriteFile(filepath.Join(dir, "mcpshimfixture.cmd"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	work := t.TempDir()
	cfg := ServerConfig{ID: "docs", Command: "mcpshimfixture", CWD: work,
		Args:   []string{"-test.run=^TestMCPStdioHelper$", "带 空格 的参数"},
		Env:    map[string]string{"SUMIKA_K4_MCP_HELPER": "1", "TEMP": work, "TMP": work},
		Sumika: ServerPolicy{Enabled: true}} // include 不写：全部工具
	client := New(mcpFixtureStore(t))
	defer client.Close()
	if err := client.Connect(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(client.servers["docs"].entries); got != 1 {
		t.Fatalf("目录里该有 1 个工具：%d", got)
	}
}

// 启动失败写明原因：找不到命令、启动后立刻退出带 stderr（凭据抹掉）、超时提示怎么调。
func TestStartFailureReasons(t *testing.T) {
	client := New(mcpFixtureStore(t))
	defer client.Close()
	err := client.Connect(t.Context(), ServerConfig{ID: "gone", Command: "编的不存在的命令-xyz", Sumika: ServerPolicy{Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "MCP 握手失败（gone）") || !strings.Contains(err.Error(), "PATH 里没有它") {
		t.Fatalf("找不到命令：%v", err)
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	boom := write("boom.cmd", "@echo off\r\necho loading 1>&2\r\necho token=SECRETVALUE rejected by upstream 1>&2\r\nexit /b 3\r\n")
	err = client.Connect(t.Context(), ServerConfig{ID: "boom", Command: boom, Env: map[string]string{"API_KEY": "SECRETVALUE"}, Sumika: ServerPolicy{Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "它最后说的是") || !strings.Contains(err.Error(), "rejected by upstream") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("启动后退出要带 stderr 和退出码：%v", err)
	}
	if strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("凭据泄露到错误里：%v", err)
	}
	slow := write("slow.cmd", "@echo off\r\nping -n 30 127.0.0.1 >nul\r\n")
	err = client.Connect(t.Context(), ServerConfig{ID: "slow", Command: slow, Sumika: ServerPolicy{Enabled: true, StartupTimeoutMs: 700}})
	if err == nil || !strings.Contains(err.Error(), "startupTimeoutMs") {
		t.Fatalf("超时要提示怎么调：%v", err)
	}
	err = client.Connect(t.Context(), ServerConfig{ID: "py", Command: write("srv.py", "print(1)"), Sumika: ServerPolicy{Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "解释器") {
		t.Fatalf("不能直接启动的脚本：%v", err)
	}
}
