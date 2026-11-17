package approval

import "testing"

// 只是提到 sumika-ctl 的日常命令不能被这条规则多问：期望值和“没有这条规则”时一样（复核给的那张表，三档都测）。
func TestSelfCtlMentionOnlyCommandsAreNotCaught(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, c := range []string{
		`rg sumika-ctl docs`, `git log --grep sumika-ctl`, `Get-Process sumika-ctl`, `ls native/sumika-ctl*.exe`, `git diff -- native/cmd/sumika-ctl`,
		`go build ./cmd/sumika-ctl`, `go test ./cmd/sumika-ctl`, `go vet ./cmd/sumika-ctl`, `go build -o sumika-ctl.exe ./cmd/sumika-ctl`,
		`git commit -m 'docs: sumika-ctl quit 要点卡'`, `Set-Content docs/x.md "...sumika-ctl quit..."`, `Remove-Item native/sumika-ctl.exe`,
		`Get-Content native\cmd\sumika-ctl\run.go`, `Select-String -Path docs\*.md -Pattern sumika-ctl`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			if d, _ := ctlDecide(t, g, mode, c, false, ""); isCtlAsk(d) {
				t.Errorf("%s 档：%q 只是提到 sumika-ctl，不该被这条规则拦：%+v", mode, c, d)
			}
		}
	}
}

// 分析器看得清、被执行的是别的程序时，串联里另有 sumika-ctl 照样整条要问；go run 那个包要认（含末尾斜杠、反斜杠）；
// 起进程或存别名的命令参数里有它也认。
func TestSelfCtlStructuredRuleCatchesRealRuns(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, c := range []string{
		`rg x docs; sumika-ctl quit`, `git status && .\sumika-ctl.exe quit`,
		`go run ./cmd/sumika-ctl quit`, `go run ./cmd/sumika-ctl/ quit`, `go run .\cmd\sumika-ctl\ quit`, `go run sumika-native/cmd/sumika-ctl quit`,
		`schtasks /create /tr "sumika-ctl quit" /sc once /tn x`, `Set-Alias c sumika-ctl`, `New-Alias -Name c -Value sumika-ctl.exe`,
		`1..2 | ForEach-Object { sumika-ctl quit }`, `function f { sumika-ctl quit }; f`, `echo $(sumika-ctl quit)`,
		`Start-Process sumika-ctl -ArgumentList quit`, `cmd /c "sumika-ctl quit"`, `bash -c "go run ./cmd/sumika-ctl/ quit"`,
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			if d, _ := ctlDecide(t, g, mode, c, false, ""); !isCtlAsk(d) {
				t.Errorf("%s 档：%q 实际会运行 sumika-ctl，该只能问一次：%+v", mode, c, d)
			}
		}
	}
}

// 续行：反引号加换行（PowerShell）、反斜杠加换行（bash）在实际运行时是一个空格；接在光写的 goal、loop 后面不能绕过只读判断。
func TestSelfCtlContinuationLinesDoNotBypass(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	for _, c := range []string{
		"sumika-ctl goal `\nclear", "sumika-ctl loop `\nadd 1h x", "sumika-ctl goal `\n--character a set x",
		"sumika-ctl goal `\r\n  clear", "sumika-ctl status; sumika-ctl goal `\nclear",
		"bash -c \"sumika-ctl goal \\\nclear\"", "sumika-ctl goal", "sumika-ctl loop",
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			if d, _ := ctlDecide(t, g, mode, c, false, ""); !isCtlAsk(d) {
				t.Errorf("%s 档：%q 实际是 goal clear / loop add 之类，该只能问一次：%+v", mode, c, d)
			}
		}
	}
	// 续行接在只读子命令后面的照旧只读
	for _, c := range []string{"sumika-ctl goal show", "sumika-ctl mcp `\nstatus"} {
		if d, _ := ctlDecide(t, g, "yolo", c, false, ""); isCtlAsk(d) {
			t.Errorf("%q 是只读，不该被拦：%+v", c, d)
		}
	}
}
