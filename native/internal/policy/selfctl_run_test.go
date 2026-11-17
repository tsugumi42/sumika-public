package policy

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSelfCtlProgramBaseStripsExtensionsAndDots(t *testing.T) {
	for in, want := range map[string]string{
		"sumika-ctl":              "sumika-ctl",
		`D:\Tools\SUMIKA-CTL.EXE`: "sumika-ctl",
		"sumika-ctl.cmd":          "sumika-ctl",
		"sumika-ctl.ps1":          "sumika-ctl",
		"sumika-ctl.bat":          "sumika-ctl",
		"sumika-ctl.com":          "sumika-ctl",
		"sumika-ctl.exe.":         "sumika-ctl",
		"sumika-ctl.exe ":         "sumika-ctl",
		"'.\\sumika-ctl.exe'":     "sumika-ctl",
		"sumika-ctl.exe.cmd":      "sumika-ctl",
		"./sumika-ctl-new.exe":    "sumika-ctl-new",
		"sumika-ctl-old-abc.exe":  "sumika-ctl-old-abc",
		"mysumika-ctl.exe":        "mysumika-ctl",
		"go":                      "go",
	} {
		if got := programBase(in); got != want {
			t.Errorf("programBase(%q) = %q，应为 %q", in, got, want)
		}
	}
	for _, in := range []string{"sumika-ctl", "sumika-ctl.exe.", "sumika-ctl.ps1", "sumika-ctl-new.exe", "sumika-ctl-old-abc.exe", `D:\x\sumika-ctl-1.cmd`} {
		if !isCtlProgram(in) {
			t.Errorf("%q 该认成 sumika-ctl", in)
		}
	}
	for _, in := range []string{"sumika-ctlx", "mysumika-ctl", "sumika-notes.exe", "rg", "sumika"} {
		if isCtlProgram(in) {
			t.Errorf("%q 不是 sumika-ctl", in)
		}
	}
}

// loadUsage 读 cmd/sumika-ctl/run.go 里的用法文字
func loadUsage(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../cmd/sumika-ctl/run.go")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(src, "const usage = `")
	if i < 0 {
		t.Fatal("找不到 sumika-ctl 的用法文字")
	}
	usage := src[i+len("const usage = `"):]
	return usage[:strings.Index(usage, "`")]
}

func cmdv(cmds ...CmdLine) *Verdict          { return &Verdict{Cmds: cmds} }
func cl(name string, args ...string) CmdLine { return CmdLine{Name: name, Args: args} }

// cd 进 sumika-ctl 的目录再 go run：按 go run 那个包判（子命令只读的照原档位）
func TestSelfCtlCdThenGoRun(t *testing.T) {
	cases := []struct {
		name string
		v    *Verdict
		want bool
	}{
		{"cd 后 go run . quit", cmdv(cl("cd", "native/cmd/sumika-ctl"), cl("go", "run", ".", "quit")), true},
		{"Set-Location", cmdv(cl("Set-Location", `native\cmd\sumika-ctl`), cl("go", "run", ".", "quit")), true},
		{"sl", cmdv(cl("sl", "native/cmd/sumika-ctl"), cl("go", "run", ".", "quit")), true},
		{"pushd", cmdv(cl("pushd", "native/cmd/sumika-ctl"), cl("go", "run", ".", "quit")), true},
		{"Push-Location", cmdv(cl("Push-Location", "native/cmd/sumika-ctl"), cl("go", "run", ".", "quit")), true},
		{"chdir", cmdv(cl("chdir", "native/cmd/sumika-ctl"), cl("go", "run", "main.go", "quit")), true},
		{"cd 后 go run . status 只读", cmdv(cl("cd", "native/cmd/sumika-ctl"), cl("go", "run", ".", "status")), false},
		{"cd 后 go run . --json status 只读", cmdv(cl("cd", "native/cmd/sumika-ctl"), cl("go", "run", ".", "--json", "status")), false},
		{"cd 别的目录 go run .", cmdv(cl("cd", "native/cmd/logotool"), cl("go", "run", ".", "quit")), false},
		{"cd 后 go test .", cmdv(cl("cd", "native/cmd/sumika-ctl"), cl("go", "test", ".")), false},
		{"cd 后 go build", cmdv(cl("cd", "native/cmd/sumika-ctl"), cl("go", "build", "-o", "x.exe", ".")), false},
		{"go run -C 目录", cmdv(cl("go", "run", "-C", "native/cmd/sumika-ctl", ".", "quit")), true},
		{"go run -C=目录", cmdv(cl("go", "run", "-C=native/cmd/sumika-ctl", ".", "quit")), true},
		{"go run -tags x 包 quit", cmdv(cl("go", "run", "-tags", "x", "./cmd/sumika-ctl", "quit")), true},
		{"go run 包 status 只读", cmdv(cl("go", "run", "./cmd/sumika-ctl", "status")), false},
		{"go run 包 --json status 只读", cmdv(cl("go", "run", "./cmd/sumika-ctl/", "--json", "status")), false},
		{"go run 包 不认识的选项", cmdv(cl("go", "run", "./cmd/sumika-ctl", "status", "--bogus")), true},
		{"go run 别的包", cmdv(cl("go", "run", "./cmd/logotool", "quit")), false},
	}
	for _, c := range cases {
		if got := SelfCtlNeedsAsk("x", c.v); got != c.want {
			t.Errorf("%s：得到 %v，应为 %v", c.name, got, c.want)
		}
	}
}

// 包装程序后面接 sumika-ctl：名单里每个名字都测“接 quit 要问、接 status 照原档位、接别的程序（参数里提到它）不问”
func TestSelfCtlArgRunners(t *testing.T) {
	for name := range argRunners {
		if name == "forfiles" {
			continue
		}
		if !SelfCtlNeedsAsk("x", cmdv(cl(name, "sumika-ctl", "quit"))) {
			t.Errorf("%s sumika-ctl quit 该问", name)
		}
		if !SelfCtlNeedsAsk("x", cmdv(cl(name, `D:\x\sumika-ctl.exe`, "mcp", "release", "docs"))) {
			t.Errorf("%s 带路径 sumika-ctl mcp release 该问", name)
		}
		if SelfCtlNeedsAsk("x", cmdv(cl(name, "sumika-ctl", "status"))) {
			t.Errorf("%s sumika-ctl status 是只读，照原档位", name)
		}
		if SelfCtlNeedsAsk("x", cmdv(cl(name, "rg", "sumika-ctl", "docs"))) {
			t.Errorf("%s rg sumika-ctl docs：被执行的是 rg，不该问", name)
		}
	}
	cases := []struct {
		name string
		v    *Verdict
		want bool
	}{
		{"forfiles /c 整条命令", cmdv(cl("forfiles", "/c", "sumika-ctl quit")), true},
		{"forfiles 带别的开关", cmdv(cl("forfiles", "/p", ".", "/m", "*", "/c", "cmd /c sumika-ctl quit")), false}, // cmd 在 /c 后面：由看不清的路径管，这里不判
		{"forfiles /c status", cmdv(cl("forfiles", "/c", "sumika-ctl status")), false},
		{"forfiles /c rg", cmdv(cl("forfiles", "/c", "rg sumika-ctl docs")), false},
		{"timeout 时长", cmdv(cl("timeout", "1", "sumika-ctl", "quit")), true},
		{"timeout 带 -s", cmdv(cl("timeout", "-s", "KILL", "5s", "sumika-ctl", "quit")), true},
		{"env 变量赋值", cmdv(cl("env", "FOO=1", "BAR=2", "sumika-ctl", "quit")), true},
		{"env -u", cmdv(cl("env", "-u", "X", "sumika-ctl", "quit")), true},
		{"套着用", cmdv(cl("env", "timeout", "1", "nohup", "sumika-ctl", "quit")), true},
		{"套着用只读", cmdv(cl("env", "timeout", "1", "sumika-ctl", "status")), false},
		{"xargs rg", cmdv(cl("xargs", "rg", "sumika-ctl")), false},
		{"xargs -n 1", cmdv(cl("xargs", "-n", "1", "sumika-ctl", "quit")), true},
		{"改名副本也认", cmdv(cl("env", `.\sumika-ctl-new.exe`, "quit")), true},
		{"time 带 .cmd", cmdv(cl("time", "sumika-ctl.cmd", "quit")), true},
		{"名单外的程序提到", cmdv(cl("Select-String", "sumika-ctl", "x.txt")), false},
		{"conhost", cmdv(cl("conhost", "sumika-ctl", "quit")), true},
	}
	for _, c := range cases {
		if got := SelfCtlNeedsAsk("x", c.v); got != c.want {
			t.Errorf("%s：得到 %v，应为 %v", c.name, got, c.want)
		}
	}
}

// 改名副本按前缀认，但只认“被执行的程序名”：文件操作的参数里出现这些名字不算
func TestSelfCtlRenamedCopiesOnlyWhenExecuted(t *testing.T) {
	for _, c := range []struct {
		name string
		v    *Verdict
		want bool
	}{
		{"运行 -new", cmdv(cl(`.\sumika-ctl-new.exe`, "quit")), true},
		{"运行 -old-abc", cmdv(cl("sumika-ctl-old-abc.exe", "quit")), true},
		{"运行 -new 只读", cmdv(cl("sumika-ctl-new.exe", "status")), false},
		{"运行 .cmd", cmdv(cl("sumika-ctl.cmd", "quit")), true},
		{"运行 .ps1", cmdv(cl("sumika-ctl.ps1", "quit")), true},
		{"运行 末尾带点", cmdv(cl("sumika-ctl.exe.", "quit")), true},
		{"Copy-Item", cmdv(cl("Copy-Item", "sumika-ctl.exe", "sumika-ctl-old-abc.exe")), false},
		{"Move-Item", cmdv(cl("Move-Item", "sumika-ctl-new.exe", "sumika-ctl.exe")), false},
		{"Remove-Item", cmdv(cl("Remove-Item", "sumika-ctl-old-abc.exe")), false},
		{"go build -o", cmdv(cl("go", "build", "-o", "sumika-ctl-new.exe", "./cmd/sumika-ctl")), false},
		{"xargs rg", cmdv(cl("xargs", "rg", "sumika-ctl")), false},
		{"ls 通配符", cmdv(cl("ls", "native/sumika-ctl*.exe")), false},
	} {
		if got := SelfCtlNeedsAsk("x", c.v); got != c.want {
			t.Errorf("%s：得到 %v，应为 %v", c.name, got, c.want)
		}
	}
}

// 只读子命令后面带不认识的选项：不当只读；认识的（含全局选项、=写法、数字值）照原档位
func TestSelfCtlReadOnlyWithUnknownOptionAsks(t *testing.T) {
	for args, want := range map[string]bool{
		"status": false, "status --json": false, "--json status": false, "status --bogus": true, "status -x": true,
		"logs -n 50": false, "logs -n=50": false, "logs --level warn --module app": false, "logs --errors": false, "logs --follow": true,
		"gate list": false, "gate list --all": true,
		"stats usage --days 7 --by model --full": false, "stats usage --days=7": false, "stats usage --bogus": true,
		"stats quality --task foreground --failures 5": false, "stats spans --turn last": false,
		"backfill gaps -n 100 --cursor abc": false, "backfill gaps --limit 5": true,
		"link preview http://example.com --template @a --site x --sample": false, "link preview http://example.com --download": true,
		"goal show --character a": false, "goal show --all": true, "loop list --character a": false,
		"mcp status": false, "mcp status --fix": true, "kernel faults": false, "kernel faults --repair": true,
		"artist list --status pending -n 100": false, "artist list --delete": true, "proactive log -n 5": false,
		"logs -n -5": false,
	} {
		if got := !ctlReadOnlyInvocation(strings.Fields(args)); got != want {
			t.Errorf("%q：要问 = %v，应为 %v", args, got, want)
		}
	}
}

// 守卫：用法文字里每个只读子命令那一行出现的选项，都得登记在 ctlKnownOpts 或全局选项里；新增选项没登记就红
// （只查命令那一行，续行说明不查）。
func TestSelfCtlKnownOptionsCoverUsageLines(t *testing.T) {
	usage := loadUsage(t)
	optRe := regexp.MustCompile(`(?:^|[ \[|])(--[a-z][a-z-]*|-n)(?:[ \]|=]|$)`)
	lineRe := regexp.MustCompile(`^ {2,8}([a-z][a-z-]*)(?: |$)`)
	checked := 0
	for _, line := range strings.Split(usage, "\n") {
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		keys := ctlKeysOfUsageLine(strings.TrimLeft(line, " "))
		text := strings.TrimLeft(line, " ")
		// 命令那一行里描述文字之前的部分（描述前至少两个空格）
		if i := strings.Index(text, "  "); i > 0 {
			text = text[:i]
		}
		for _, o := range optRe.FindAllStringSubmatch(text, -1) {
			opt := o[1]
			if ctlGlobalOpts[opt] {
				continue
			}
			usedByReadOnly, known := false, false
			for _, k := range keys {
				if ctlReadOnly[k] {
					usedByReadOnly = true
					for _, kn := range ctlKnownOpts[k] {
						known = known || kn == opt
					}
				}
			}
			checked++
			if usedByReadOnly && !known && !onlyWriteKeysHaveIt(keys, opt) {
				t.Errorf("用法文字里 %q 的选项 %s 没登记在 ctlKnownOpts：只读子命令不认识它会被当成要问", keys, opt)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("只核对了 %d 个选项，解析多半坏了", checked)
	}
}

// onlyWriteKeysHaveIt：这一行里带这个选项的只有会改状态的子命令（例如 goal 那行里的 --character 属于 show 也属于 set，show 有登记）
func onlyWriteKeysHaveIt(keys []string, opt string) bool {
	for _, k := range keys {
		if ctlReadOnly[k] {
			return false
		}
	}
	return true
}
