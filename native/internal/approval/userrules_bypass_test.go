package approval

import (
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// 用真分析器走一遍 Decide：放行规则不能被括号子表达式带出规则，拒绝规则不能被换写法绕过（复核发现）。
func TestUserRulesAgainstRealAnalyzer(t *testing.T) {
	g := NewGate(t.TempDir())
	<-g.Analyzer.Warmup()
	decide := func(mode, command string, rules ...[2]string) policy.Decision {
		var rs []policy.UserRule
		for _, r := range rules {
			u, err := policy.ParseUserRule(r[0], r[1])
			if err != nil {
				t.Fatal(err)
			}
			rs = append(rs, u)
		}
		args, _ := jsjson.Parse(fmt.Sprintf(`{"command":%q,"purpose":"x"}`, command))
		a := g.Analyzer.Analyze(command, 8*time.Second)
		if a == nil {
			t.Fatalf("拆不开：%s", command)
		}
		j := policy.Judge(*a)
		env := policy.Env{Mode: mode, Workdir: `D:\AgentWork`, ProtectedDirs: g.ProtectedDirs, Declared: true, Shell: &j, Rules: rs,
			Audience: policy.Audience{Channel: "console", PrivateOk: true}, Real: map[string]string{}}
		return policy.Decide("powershell", policy.Specs["powershell"], args, env)
	}
	for _, c := range []struct{ rule, cmd string }{
		{"Bash(Get-ChildItem:*)", "Get-ChildItem (Remove-Item -Recurse -Force src)"},
		{"Bash(dir:*)", `dir (Remove-Item src\a.txt)`},
		{"Bash(cat:*)", "cat (Set-Content a.txt hi)"},
		{"Bash(echo:*)", "echo (Remove-Item a.txt)"},
	} {
		if d := decide("manual", c.cmd, [2]string{c.rule, "allow"}); d.Kind != "ask" {
			t.Errorf("%s 下 %q 该问，得到 %+v", c.rule, c.cmd, d)
		}
	}
	if d := decide("manual", "dir src", [2]string{"Bash(dir:*)", "allow"}); d.Kind != "allow" || d.By != policy.ByRule {
		t.Errorf("正常的放行规则照旧生效：%+v", d)
	}
	for _, c := range []struct{ rule, cmd string }{
		{"Bash(git push:*)", "git -C . push"},
		{"Bash(git push:*)", "git.exe push"},
		{"Bash(git push:*)", "& 'git' push"},
		{"Bash(git push:*)", "git `push"},
		{"Bash(rm:*)", "ri x"},
		{"Bash(rm:*)", "Remove-Item x"},
		{"Bash(rm:*)", "del x"},
	} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			if d := decide(mode, c.cmd, [2]string{c.rule, "deny"}); d.Kind != "deny" {
				t.Errorf("%s 下 %s %q 该拒绝，得到 %+v", c.rule, mode, c.cmd, d)
			}
		}
	}
	if d := decide("yolo", "git -C . pull", [2]string{"Bash(git push:*)", "deny"}); d.Kind == "deny" {
		t.Errorf("别的 git 子命令不该被拒：%+v", d)
	}
}
