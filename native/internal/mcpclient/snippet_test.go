package mcpclient

// B5：社区的 mcpServers 片段（Claude Desktop、Claude Code）贴进来能用：不写 sumika 段也行，${VAR} 能展开，
// 光写 npx、uvx 按 PATH 找，Windows 上 .cmd 垫片能启动，启动失败写明原因（凭据值抹掉）。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func parseSnippet(t *testing.T, data string, env map[string]string) (Configuration, error) {
	t.Helper()
	return ParseConfiguration([]byte(data), filepath.Join(t.TempDir(), "mcp.json"), func(kind, name string) (string, error) {
		if v, ok := env[name]; ok {
			return v, nil
		}
		return "", os.ErrNotExist
	})
}

func TestParseCommunitySnippet(t *testing.T) {
	c, err := parseSnippet(t, `{
  "mcpServers": {
    "filesystem": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "D:\\编的目录"], "env": {"TOKEN": "${COMMUNITY_TOKEN}", "MODE": "${MODE_X:-fast}"}},
    "remote": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${COMMUNITY_TOKEN}"}},
    "typed": {"type": "stdio", "command": "uvx", "args": ["mcp-server-time"]},
    "off": {"command": "node", "sumika": {"enabled": false}}
  }
}`, map[string]string{"COMMUNITY_TOKEN": "编的令牌-abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Sumika.Enabled || len(c.MCPServers) != 4 {
		t.Fatalf("没有 sumika 段也该整体开启：%+v", c)
	}
	fs := c.MCPServers["filesystem"]
	if !fs.Sumika.Enabled || len(fs.Sumika.Include) != 1 || fs.Sumika.Include[0] != "*" || fs.Command != "npx" || fs.Type != "" {
		t.Fatalf("filesystem：%+v", fs)
	}
	if fs.Env["TOKEN"] != "编的令牌-abc" || fs.Env["MODE"] != "fast" {
		t.Fatalf("${VAR}、${VAR:-默认} 没展开：%v", fs.Env)
	}
	remote := c.MCPServers["remote"]
	if remote.Type != "http" || remote.Headers["Authorization"] != "Bearer 编的令牌-abc" {
		t.Fatalf("只写 url 该认成 http：%+v", remote)
	}
	if typed := c.MCPServers["typed"]; typed.Command != "uvx" || typed.Type != "" {
		t.Fatalf("typed：%+v", typed)
	}
	if off := c.MCPServers["off"]; off.Sumika.Enabled {
		t.Fatalf("写了 enabled:false 的照旧是关：%+v", off)
	}
	if !included(fs, "任何工具") || included(ServerConfig{Sumika: ServerPolicy{Include: []string{"*"}, Exclude: []string{"drop"}}}, "drop") {
		t.Fatal("include 缺省是全部，exclude 仍然生效")
	}
	// 老写法（显式 sumika 段、include 白名单）照旧
	old, err := parseSnippet(t, `{"sumika":{"enabled":true},"mcpServers":{"docs":{"command":"C:\\编的\\docs.exe","sumika":{"enabled":true,"include":["search"]}}}}`, nil)
	if err != nil || !old.MCPServers["docs"].Sumika.Enabled || old.MCPServers["docs"].Sumika.Include[0] != "search" {
		t.Fatalf("老写法：%+v %v", old, err)
	}
	// 总开关写了 false：整份不生效
	if off, err := parseSnippet(t, `{"sumika":{"enabled":false},"mcpServers":{"a":{"command":"node"}}}`, nil); err != nil || off.Sumika.Enabled {
		t.Fatalf("总开关 false：%+v %v", off, err)
	}
	// 相对路径（带分隔符）仍相对配置文件；光写程序名不拼目录
	rel, err := parseSnippet(t, `{"mcpServers":{"a":{"command":"tools\\a.exe"},"b":{"command":"b"}}}`, nil)
	if err != nil || !filepath.IsAbs(rel.MCPServers["a"].Command) || rel.MCPServers["b"].Command != "b" {
		t.Fatalf("相对路径：%+v %v", rel, err)
	}
}

func TestParseSnippetErrorsAreExplained(t *testing.T) {
	for data, want := range map[string]string{
		`{"mcpServers":{"a":{"type":"sse","url":"https://x"}}}`:          "sse",
		`{"mcpServers":{"a":{"type":"websocket","url":"wss://x"}}}`:      "只认 stdio 或 http",
		`{"mcpServers":{"a":{}}}`:                                        "既没写 command",
		`{"mcpServers":{"a":{"command":"npx","env":{"K":"${NOPE_X}"}}}}`: "NOPE_X",
		`{"mcpServers":{"a":{"command":"npx","args":["${NOPE_Y}"]}}}`:    "NOPE_Y",
	} {
		_, err := parseSnippet(t, data, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s：想要提到 %q，得到 %v", data, want, err)
		}
	}
	// 关着的占位条目不因为写得不全拖垮整份
	if _, err := parseSnippet(t, `{"mcpServers":{"a":{"sumika":{"enabled":false}},"b":{"command":"node"}}}`, nil); err != nil {
		t.Errorf("关着的占位条目：%v", err)
	}
}

func TestResolveProgramReasons(t *testing.T) {
	if _, err := resolveProgram("编的不存在的命令-xyz"); err == nil || !strings.Contains(err.Error(), "PATH 里没有它") {
		t.Fatalf("找不到命令：%v", err)
	}
	if _, err := resolveProgram(filepath.Join(t.TempDir(), "nope.exe")); err == nil || !strings.Contains(err.Error(), "找不到程序") {
		t.Fatalf("绝对路径不存在：%v", err)
	}
	dir := t.TempDir()
	name := "shimtool"
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := resolveProgram("shimtool")
	if err != nil || !filepath.IsAbs(got) || !strings.EqualFold(filepath.Base(got), name) {
		t.Fatalf("按 PATH、PATHEXT 找到：%q %v", got, err)
	}
}

func TestRedact(t *testing.T) {
	cfg := ServerConfig{Env: map[string]string{"K": "SECRETVALUE"}, Headers: map[string]string{"H": "Bearer-xyz-123"}}
	out := redact("加载中\nerror: SECRETVALUE rejected\nAuthorization: Bearer abc.def.ghi\n\x1b[31m红字\x1b[0m\n其余 Bearer-xyz-123 也要抹", cfg)
	for _, leak := range []string{"SECRETVALUE", "abc.def.ghi", "Bearer-xyz-123"} {
		if strings.Contains(out, leak) {
			t.Fatalf("凭据泄露 %q：%s", leak, out)
		}
	}
	if !strings.Contains(out, "红字") || strings.ContainsRune(out, 0x1b) {
		t.Fatalf("控制字符该去掉、正文留着：%q", out)
	}
	long := redact(strings.Repeat("很长的一行", 500), cfg)
	if len([]rune(long)) > 402 {
		t.Fatalf("太长：%d", len([]rune(long)))
	}
}
