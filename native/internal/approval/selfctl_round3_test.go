package approval

import (
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// 第三次复核补的写法：cd 进包目录再 go run .、env / xargs / timeout 这类包装程序后面接 sumika-ctl、改名副本、扩展名、不认识的选项。
// 三档都只能问一次；带已有授权（这个对话、始终允许）也一样；不产生授权键。
func TestSelfCtlRound3WritesAlwaysAsk(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	commands := []string{
		`cd native/cmd/sumika-ctl; go run . quit`, `Set-Location native\cmd\sumika-ctl; go run . quit`, `sl native/cmd/sumika-ctl; go run . quit`,
		`pushd native/cmd/sumika-ctl; go run . mcp release docs --reason x --accept-unknown-risk`, `Push-Location native\cmd\sumika-ctl; go run . quit`,
		`cd native/cmd/sumika-ctl && go run main.go quit`, `go run -C native/cmd/sumika-ctl . quit`,
		`env sumika-ctl quit`, `env FOO=1 sumika-ctl quit`, `xargs sumika-ctl quit`, `time sumika-ctl quit`, `nohup sumika-ctl quit`, `timeout 1 sumika-ctl quit`,
		`conhost sumika-ctl quit`, `forfiles /c "sumika-ctl quit"`, `wt sumika-ctl quit`, `runas sumika-ctl quit`, `sudo sumika-ctl quit`, `nice sumika-ctl quit`,
		`watch sumika-ctl quit`, `command sumika-ctl quit`, `gsudo sumika-ctl quit`, `stdbuf sumika-ctl quit`, `winpty sumika-ctl quit`,
		`.\sumika-ctl-new.exe quit`, `sumika-ctl-old-abc.exe quit`, `sumika-ctl.cmd quit`, `sumika-ctl.ps1 quit`, `sumika-ctl.exe. quit`,
		`sumika-ctl status --bogus`, `sumika-ctl logs --follow`, `sumika-ctl mcp status --fix`,
	}
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, c := range commands {
			for _, granted := range []string{"", "conversation", "always"} {
				d, _ := ctlDecide(t, g, mode, c, false, granted)
				if !isCtlAsk(d) {
					t.Errorf("%s 档、授权[%s]：%q 该只能问一次，得到 %+v", mode, granted, c, d)
				}
			}
		}
	}
	for _, c := range commands {
		a := g.Analyzer.Analyze(c, time.Minute)
		if a == nil {
			t.Fatalf("拆不开：%s", c)
		}
		j := policy.Judge(*a)
		args, _ := jsparse(fmt.Sprintf(`{"command":%q}`, c))
		if k := policy.KindOf("powershell", args, `D:\AgentWork`, &j); k != nil {
			t.Errorf("%q 不该产生授权键：%+v", c, k)
		}
	}
}

// 只读的、只是提到的、文件操作里出现名字的：不被这条规则多问（期望值同“没有这条规则”）。
func TestSelfCtlRound3NotCaught(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, c := range []string{
		`cd native/cmd/sumika-ctl; go test .`, `cd native/cmd/sumika-ctl; go build -o x.exe .`, `cd native/cmd/sumika-ctl; go run . status`,
		`cd native/cmd/logotool; go run . quit`, `go run ./cmd/sumika-ctl status`, `go run ./cmd/sumika-ctl --json status`,
		`env sumika-ctl status`, `timeout 1 sumika-ctl status`, `xargs rg sumika-ctl`, `env rg sumika-ctl docs`, `forfiles /c "rg sumika-ctl docs"`,
		`go build -o sumika-ctl-new.exe ./cmd/sumika-ctl`, `Copy-Item sumika-ctl.exe sumika-ctl-old-abc.exe`, `Move-Item sumika-ctl-new.exe sumika-ctl.exe`,
		`Remove-Item sumika-ctl-old-abc.exe`, `ls native/sumika-ctl*.exe`, `Get-Process sumika-ctl`, `rg sumika-ctl docs`,
		`git commit -m 'docs: sumika-ctl quit 要点卡'`, `git log --grep sumika-ctl`, `Set-Content docs/x.md "...sumika-ctl quit..."`,
		`go build ./cmd/sumika-ctl`, `go test ./cmd/sumika-ctl`, `go vet ./cmd/sumika-ctl`, `git diff -- native/cmd/sumika-ctl`,
		`sumika-ctl logs -n 20`, `sumika-ctl stats usage --days 3 --by model`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			if d, _ := ctlDecide(t, g, mode, c, false, ""); isCtlAsk(d) {
				t.Errorf("%s 档：%q 不该被这条规则拦：%+v", mode, c, d)
			}
		}
	}
}

func jsparse(s string) (jsjson.Value, error) { return jsjson.Parse(s) }
