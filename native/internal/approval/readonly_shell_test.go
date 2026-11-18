package approval

import (
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// PowerShell 只读以拆开命令的分析为准（2026-11-08：Get-PSDrive 不在老的命令名白名单里，auto 下照样弹审批卡）。
// 会改东西、运行别的脚本、手动模式的照旧要问。
func TestAutoModeTrustsShellAnalysisForReadOnly(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	decide := func(mode, command string) policy.Decision {
		args, _ := jsjson.Parse(fmt.Sprintf(`{"command":%q,"purpose":"x"}`, command))
		a := g.Analyzer.Analyze(command, time.Minute)
		if a == nil {
			t.Fatalf("拆不开：%s", command)
		}
		j := policy.Judge(*a)
		env := policy.Env{Mode: mode, Workdir: `D:\AgentWork`, ProtectedDirs: g.ProtectedDirs, Declared: true, Shell: &j,
			Audience: policy.Audience{Channel: "console", PrivateOk: true}, Real: map[string]string{}}
		return policy.Decide("powershell", policy.Specs["powershell"], args, env)
	}
	for _, c := range []string{
		`Get-PSDrive -Name D`,
		`Get-PSDrive -Name D | Select-Object Name, @{n='剩余GB';e={[math]::Round($_.Free/1GB,2)}} | Format-List`,
		`Get-Date`,
	} {
		if d := decide("auto", c); d.Kind != "allow" {
			t.Errorf("auto 下只读命令该放行：%s => %s %q", c, d.Kind, d.Reason)
		}
	}
	for _, c := range []string{
		`Set-Content (Join-Path $env:TEMP 'x.txt') -Value 1`,
		`pwsh -NoProfile -File tools/x.ps1`,
		`Stop-Process -Name notepad`,
	} {
		if d := decide("auto", c); d.Kind == "allow" {
			t.Errorf("会改东西或运行脚本的不能按只读放行：%s => %s %q", c, d.Kind, d.Reason)
		}
	}
	if d := decide("manual", `Get-PSDrive -Name D`); d.Kind == "allow" {
		t.Errorf("手动模式下 PowerShell 照旧要问：%s %q", d.Kind, d.Reason)
	}
}

// 看不清的命令（动态执行、内联代码）分析器不会判只读，不会被这条放行
func TestOpaqueShellNotTreatedAsReadOnly(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, c := range []string{`Invoke-Expression (Get-Content x.txt -Raw)`, `& ([scriptblock]::Create('Get-Date'))`, `[IO.File]::WriteAllText('a.txt','x')`} {
		a := g.Analyzer.Analyze(c, time.Minute)
		if a == nil {
			continue
		}
		if j := policy.Judge(*a); j.ReadOnly {
			t.Errorf("看不清或会改东西的命令被判成只读：%s => %+v", c, j)
		}
	}
}
