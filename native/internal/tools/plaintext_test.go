package tools

// PowerShell 输出纯文本（ADR-182）：不管后台从哪个环境起来（设没设 NO_COLOR），给她看的结果都一样、没有颜色码；
// 纯文本模式顺带的代价也钉在这里，pwsh 哪天改了行为这里会红，不会悄悄变

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

func runTool(t *testing.T, wd, tool string, args map[string]any) Result {
	t.Helper()
	b, _ := json.Marshal(args)
	v, err := jsjson.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Execute(context.Background(), wd, tool, Prepare(tool, v))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// withoutNoColor 在这个测试里去掉 NO_COLOR，测完恢复（t.Setenv 负责恢复）
func withoutNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
}

func TestPowerShellPlainTextAnyEnv(t *testing.T) {
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 报错、表格、文件列表、警告：pwsh 自己会上色的几种
	cmd := "Remove-Item nope-not-here; Get-ChildItem; Get-Item a.txt | Format-Table Name,Length; Write-Warning hi"
	var outs [2]string
	for i, set := range []bool{false, true} {
		if set {
			t.Setenv("NO_COLOR", "1")
		} else {
			withoutNoColor(t)
		}
		r := runTool(t, wd, "powershell", map[string]any{"command": cmd})
		if strings.Contains(r.Text, "\x1b") {
			t.Errorf("NO_COLOR 设了 %v：结果里有颜色码 %q", set, r.Text)
		}
		outs[i] = mask(r.Text, wd)
	}
	if outs[0] != outs[1] {
		t.Errorf("设不设 NO_COLOR 结果不一样\n没设：%q\n 设了：%q", outs[0], outs[1])
	}
}

// 已知的代价：纯文本模式把 PowerShell 自己输出的字符串里的转义字符也去掉（Get-Content、Write-Output）；
// 外部程序的输出、read 工具读文件都原样
func TestPowerShellPlainTextKnownEffects(t *testing.T) {
	withoutNoColor(t)
	wd := t.TempDir()
	raw := "A\x1b[31mRED\x1b[0mB"
	if err := os.WriteFile(filepath.Join(wd, "esc.txt"), []byte(raw+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modern := runTool(t, wd, "powershell", map[string]any{"command": "if ($PSStyle) { 'has-psstyle' }"})
	if !strings.Contains(modern.Text, "has-psstyle") {
		t.Skip("Windows PowerShell 5.1 没有纯文本模式，本来就不上色")
	}
	if r := runTool(t, wd, "powershell", map[string]any{"command": "Get-Content -LiteralPath esc.txt"}); !strings.Contains(r.Text, "AREDB") || strings.Contains(r.Text, "\x1b") {
		t.Errorf("Get-Content 读带转义字符的文件：预期转义字符被去掉（AREDB），实际 %q", r.Text)
	}
	if r := runTool(t, wd, "powershell", map[string]any{"command": "cmd /c type esc.txt"}); !strings.Contains(r.Text, raw) {
		t.Errorf("外部程序的输出应该原样，实际 %q", r.Text)
	}
	if r := runTool(t, wd, "read", map[string]any{"path": "esc.txt"}); !strings.Contains(r.Text, raw) {
		t.Errorf("read 读文件应该原样，实际 %q", r.Text)
	}
}
