package approval

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// Sumika 自己的遥控器 sumika-ctl：会改程序状态的子命令，任何档位、任何写法都要主人看过，只能批一次，不吃 yolo、不吃已有授权；
// 只读的子命令照原来的档位走。非主人引起的仍然问（不变）。（ADR-382）

func ctlDecide(t *testing.T, g *Gate, mode, command string, notOwner bool, granted string) (policy.Decision, *policy.Verdict) {
	t.Helper()
	args, _ := jsjson.Parse(fmt.Sprintf(`{"command":%q,"purpose":"x"}`, command))
	a := g.Analyzer.Analyze(command, time.Minute)
	var shell *policy.Verdict
	failed := false
	if a == nil {
		failed = true
	} else {
		j := policy.Judge(*a)
		shell = &j
	}
	env := policy.Env{Mode: mode, Workdir: `D:\AgentWork`, ProtectedDirs: g.ProtectedDirs, Declared: true, Shell: shell, ShellFailed: failed,
		NotOwner: notOwner, Granted: granted, GrantDirs: []string{`D:\AgentWork`},
		Audience: policy.Audience{Channel: "console", PrivateOk: true}, Real: map[string]string{}}
	return policy.Decide("powershell", policy.Specs["powershell"], args, env), shell
}

func isCtlAsk(d policy.Decision) bool {
	return d.Kind == "ask" && d.Sticky && d.Limit == "once" && strings.Contains(d.Reason, "sumika-ctl")
}

// 写法 × 档位：每个会改状态的子命令都被拦成“只能一次”的问；只读的不被这条规则拦。
func TestSelfCtlWriteAlwaysAsksAnyModeAnySpelling(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	spellings := []string{
		`sumika-ctl %s`, `sumika-ctl.exe %s`, `SUMIKA-CTL.EXE %s`, `D:\Tools\sumika-ctl.exe %s`, `.\sumika-ctl.exe %s`,
		`& 'D:\Tools\sumika-ctl.exe' %s`, `& .\sumika-ctl.exe --json %s`, `sumika-ctl --demo %s`,
	}
	writes := []string{
		`mcp release docs --reason 旧调用已结束 --accept-unknown-risk`, `mcp reconnect docs`, `gate set qq:group:10001 dry`, `quit`,
		`receive start`, `receive stop`, `backup now`, `checkpoint restore c1 id1 v1`, `checkpoint cancel c1 id1`, `backfill retry h1`, `backfill run`,
		`goal set 做点事`, `goal edit 改一下`, `goal pause`, `goal resume`, `goal clear`, `loop add 30m 做事`, `loop edit 1 30m`, `loop pause 1`,
		`loop resume 1`, `loop remove 1`, `chat 你好`, `trust`, `window show`, `whisper create --selftest`, `artist confirm 1`,
		`kernel retry`, `goal`, `loop`, `autostart on`, `auto-receive off`, `someunknowncommand`, `mcp`, `mcp unquarantine docs`, `--weird quit`, ``,
	}
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, sp := range spellings {
			for _, w := range writes {
				command := fmt.Sprintf(sp, w)
				for _, granted := range []string{"", "conversation", "always"} {
					d, _ := ctlDecide(t, g, mode, command, false, granted)
					if !isCtlAsk(d) {
						t.Errorf("%s 档、授权[%s]：%q 该是只能一次的问，得到 %+v", mode, granted, command, d)
					}
				}
			}
		}
	}
}

func TestSelfCtlReadOnlyIsNotCaughtByThisRule(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	reads := []string{`status`, `status --json`, `--json status`, `logs -n 50`, `gate list`, `backfill status`, `backfill gaps -n 100`, `checkpoint list c1`,
		`checkpoint preview c1 id1`, `checkpoint status c1 id1`, `link preview https://example.com/a`, `link template`, `stats usage --days 7`,
		`stats quality`, `stats spans`, `stats rebuild`, `stats resources`, `stats storage`, `mcp status`, `kernel faults`, `backup list`, `goal show`, `loop list`,
		`proactive log -n 5`, `artist list`, `whisper status b1`}
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, sp := range []string{`sumika-ctl %s`, `sumika-ctl.exe %s`, `D:\Tools\sumika-ctl.exe %s`, `.\sumika-ctl.exe %s`} {
			for _, r := range reads {
				command := fmt.Sprintf(sp, r)
				if d, _ := ctlDecide(t, g, mode, command, false, ""); isCtlAsk(d) {
					t.Errorf("%s 档：只读的 %q 不该被 sumika-ctl 这条规则拦：%+v", mode, command, d)
				}
			}
		}
	}
	// 目录名里带 sumika-ctl、别的程序名带这几个字，都不是在运行它
	for _, c := range []string{`Get-Content native\cmd\sumika-ctl\run.go`, `Get-ChildItem native/cmd/sumika-ctl/`, `Get-Date # sumika-ctlx quit`} {
		if d, _ := ctlDecide(t, g, "yolo", c, false, ""); isCtlAsk(d) {
			t.Errorf("%q 不是在运行 sumika-ctl：%+v", c, d)
		}
	}
}

// 串着写的：只要有一条是会改状态的 sumika-ctl，整条要问；只读的串在一起不拦。
func TestSelfCtlChainedAndWrapped(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, c := range []string{
		`sumika-ctl status; sumika-ctl quit`,
		`sumika-ctl status && sumika-ctl mcp release docs --reason x --accept-unknown-risk`,
		`Get-Date | sumika-ctl quit`,
		`sumika-ctl status || sumika-ctl quit`,
		`echo $(sumika-ctl quit)`,
		`bash -c "sumika-ctl quit"`,
		`cmd /c sumika-ctl quit`,
		`cmd /c "D:\Tools\sumika-ctl.exe mcp release docs --reason x --accept-unknown-risk"`,
		`pwsh -c "sumika-ctl quit"`,
		`powershell -NoProfile -Command "& 'sumika-ctl.exe' quit"`,
		`$c = 'sumika-ctl'; & $c quit`,
		`Invoke-Expression 'sumika-ctl quit'`,
		`Set-Alias c sumika-ctl; c quit`,
		`sumika-ctl status` + "\n" + `sumika-ctl quit`,
		`'sumika-' + 'ctl' | Out-Null; sumika-ctl quit`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			d, _ := ctlDecide(t, g, mode, c, false, "")
			if d.Kind != "ask" || d.Limit != "once" {
				t.Errorf("%s 档：%q 该只能问一次，得到 %+v", mode, c, d)
			}
		}
	}
	for _, c := range []string{`sumika-ctl status; sumika-ctl logs -n 5`, `sumika-ctl mcp status | Select-Object -First 3`} {
		if d, _ := ctlDecide(t, g, "yolo", c, false, ""); isCtlAsk(d) {
			t.Errorf("只读串在一起不该被拦：%q %+v", c, d)
		}
	}
}

// 非主人引起的（群聊、QQ）：原来就问，仍然问（不变）。
func TestSelfCtlNotOwnerStillAsks(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, c := range []string{`sumika-ctl status`, `sumika-ctl quit`, `sumika-ctl mcp release docs --reason x --accept-unknown-risk`} {
			if d, _ := ctlDecide(t, g, mode, c, true, ""); d.Kind != "ask" {
				t.Errorf("%s 档 NotOwner：%q 该问，得到 %+v", mode, c, d)
			}
		}
	}
}

// 授权键：会改状态的不产生键（不能被“这个对话”“始终允许”授权，也不和只读的共用键）；只读的照旧有键。
func TestSelfCtlKindOf(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for command, wantKind := range map[string]bool{`sumika-ctl status`: true, `sumika-ctl mcp status`: true, `sumika-ctl mcp release docs --reason x --accept-unknown-risk`: false, `sumika-ctl quit`: false, `sumika-ctl status; sumika-ctl quit`: false} {
		a := g.Analyzer.Analyze(command, time.Minute)
		if a == nil {
			t.Fatalf("拆不开：%s", command)
		}
		j := policy.Judge(*a)
		args, _ := jsjson.Parse(fmt.Sprintf(`{"command":%q}`, command))
		if k := policy.KindOf("powershell", args, `D:\AgentWork`, &j); (k != nil) != wantKind {
			t.Errorf("%q 的授权键：有=%v，应有=%v（%+v）", command, k != nil, wantKind, k)
		}
	}
}
