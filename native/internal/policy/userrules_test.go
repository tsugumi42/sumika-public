package policy

import (
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

func ruleArgs(kv ...string) jsjson.Value {
	o := map[string]jsjson.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		o[kv[i]] = jsjson.Value{Kind: jsjson.String, Str: kv[i+1]}
	}
	return jsjson.Value{Kind: jsjson.Object, Obj: o}
}

func mustRules(t *testing.T, rules ...[2]string) []UserRule {
	t.Helper()
	var out []UserRule
	for _, r := range rules {
		u, err := ParseUserRule(r[0], r[1])
		if err != nil {
			t.Fatalf("%s：%v", r[0], err)
		}
		out = append(out, u)
	}
	return out
}

func TestParseUserRule(t *testing.T) {
	good := map[string]string{
		"Bash(git status:*)": "powershell", "Bash": "powershell", "PowerShell(npm test)": "powershell", "powershell(dir *)": "powershell",
		"Write(src/**)": "write", "Edit(D:/Work/a.txt)": "write", "Read(secrets/**)": "read",
		"WebFetch(domain:example.com)": "web_fetch", "WebSearch": "web_search", "Task(reader)": "task",
		"mcp__docs": "mcp", "mcp__docs__search": "mcp", "mcp__docs__*": "mcp",
	}
	for raw, tool := range good {
		r, err := ParseUserRule(raw, EffectAllow)
		if err != nil || r.Tool != tool || r.Raw != raw || r.Key != "rule:allow:"+raw {
			t.Errorf("%s：%+v %v", raw, r, err)
		}
	}
	for _, raw := range []string{"", "(x)", "Bash(", "Rm(x)", "WebFetch(example.com)", "mcp__", "mcp__a(b)", "git status", "Bash(a) extra"} {
		if _, err := ParseUserRule(raw, EffectDeny); err == nil {
			t.Errorf("%q 该报错", raw)
		}
	}
	if _, err := ParseUserRule("Bash", "maybe"); err == nil {
		t.Error("效果不是 allow、deny 要报错")
	}
}

// headNames 模拟分析器给出的命令名：每一段的开头命令。
func headNames(cmd string) []string {
	var out []string
	for _, seg := range commandSegments(cmd) {
		out = append(out, headWord(seg))
	}
	return out
}

func TestBashRuleMatching(t *testing.T) {
	allow := mustRules(t, [2]string{"Bash(git status:*)", "allow"}, [2]string{"Bash(go test *)", "allow"}, [2]string{"Bash(npm run build)", "allow"})
	for cmd, want := range map[string]bool{
		"git status":                    true,
		"git status --short":            true,
		"GIT   STATUS":                  true,
		"git statusx":                   false,
		"git push":                      false,
		"go test ./...":                 true,
		"go test":                       true, // "go test *" 的 * 可以是空，和 Claude Code 一样
		"npm run build":                 true,
		"npm run build -- --watch":      false, // 没写 * 就是整条
		"git status && go test ./...":   true,
		"git status && git push":        false, // 每一段都要被盖住
		"git status; go test ./a":       true,
		"git status | Select-String a":  false, // Select-String 没有放行规则
		"git status > out.txt":          false, // 重定向拆不稳
		"git status $(whoami)":          false,
		"git status " + "`whoami`":      false,
		"git status & calc":             false,
		"git status\r\ngit push origin": false,
	} {
		got := MatchAllow(allow, "powershell", ruleArgs("command", cmd), Env{Shell: &Verdict{Names: headNames(cmd)}}) != nil
		if got != want {
			t.Errorf("放行 %q：得到 %v，想要 %v", cmd, got, want)
		}
	}
	// 拆不开、动态执行、看不清：放行规则不生效
	for name, sh := range map[string]*Verdict{"没拆开": nil, "opaque": {Opaque: true}, "看不清": {Unclear: []string{"x"}}, "方法": {MethodRisk: []string{"x"}}} {
		if MatchAllow(allow, "powershell", ruleArgs("command", "git status"), Env{Shell: sh}) != nil {
			t.Errorf("%s 不该吃放行规则", name)
		}
	}
	deny := mustRules(t, [2]string{"Bash(git push:*)", "deny"}, [2]string{"Bash(Remove-Item:*)", "deny"})
	for cmd, want := range map[string]bool{
		"git push":                   true,
		"git push origin main":       true,
		"git status && git push -f":  true, // 任何一段命中就拒
		`pwsh -c "git push"`:         true, // 嵌在引号里也不放过
		"echo hi; Remove-Item a.txt": true,
		"git pull":                   false,
		"git pushx":                  false,
		"echo git-push":              false,
	} {
		got := MatchDeny(deny, "powershell", ruleArgs("command", cmd), Env{}) != nil
		if got != want {
			t.Errorf("拒绝 %q：得到 %v，想要 %v", cmd, got, want)
		}
	}
	// 不带括号的 Bash 拒绝是所有命令
	if MatchDeny(mustRules(t, [2]string{"Bash", "deny"}), "powershell", ruleArgs("command", "dir"), Env{}) == nil {
		t.Error("Bash 不带括号该拒绝所有命令")
	}
}

func TestPathRuleMatching(t *testing.T) {
	env := Env{Workdir: `D:\Work`}
	allow := mustRules(t, [2]string{"Write(src/**)", "allow"}, [2]string{"Edit(docs)", "allow"}, [2]string{`Write(D:\Other\*.md)`, "allow"})
	for path, want := range map[string]bool{
		"src/a.go": true, "src/deep/er/b.go": true, `D:\Work\src\c.go`: true, "docs/x.md": true, "docs": true,
		"other/a.go": false, "srcx/a.go": false, `D:\Other\n.md`: true, `D:\Other\sub\n.md`: false, `D:\Other\n.txt`: false,
		"../Work2/src/a.go": false,
	} {
		for _, tool := range []string{"write", "edit"} {
			if got := MatchAllow(allow, tool, ruleArgs("path", path), env) != nil; got != want {
				t.Errorf("放行 %s %q：得到 %v，想要 %v", tool, path, got, want)
			}
		}
	}
	deny := mustRules(t, [2]string{"Read(secrets/**)", "deny"}, [2]string{"Write(.env)", "deny"})
	for _, c := range []struct {
		tool, path string
		want       bool
	}{{"read", "secrets/a.txt", true}, {"grep", "secrets/b/c", true}, {"read", "public/a.txt", false}, {"write", ".env", true}, {"edit", ".env", true}, {"write", "x/.env", false}, {"read", ".env", false}} {
		if got := MatchDeny(deny, c.tool, ruleArgs("path", c.path), env) != nil; got != c.want {
			t.Errorf("拒绝 %s %q：得到 %v，想要 %v", c.tool, c.path, got, c.want)
		}
	}
	// 联接绕出去：路径字面不在规则里，真实路径在——拒绝照样命中；放行则不行
	if MatchDeny(deny, "read", ruleArgs("path", `alias\a.txt`), Env{Workdir: `D:\Work`, Real: map[string]string{linkKey(`D:\Work\alias\a.txt`): `D:\Work\secrets\a.txt`}}) == nil {
		t.Error("联接绕过了拒绝规则")
	}
	if MatchAllow(mustRules(t, [2]string{"Write(alias/**)", "allow"}), "write", ruleArgs("path", `alias\a.txt`), Env{Workdir: `D:\Work`, Real: map[string]string{linkKey(`D:\Work\alias\a.txt`): `D:\Elsewhere\a.txt`}}) != nil {
		t.Error("放行规则被联接带到了规则之外")
	}
}

// 路径里有中文：按字节拼正则会把多字节字符拆坏，放行规则永远对不上（端到端测试里查出来的）。
func TestPathRuleChinese(t *testing.T) {
	env := Env{Workdir: `D:\工作`}
	allow := mustRules(t, [2]string{"Write(文档/**)", "allow"}, [2]string{"Write(笔记/?.md)", "allow"})
	for path, want := range map[string]bool{"文档/甲.txt": true, "文档/深/乙.txt": true, "笔记/a.md": true, "笔记/ab.md": false, "别的/甲.txt": false} {
		if got := MatchAllow(allow, "write", ruleArgs("path", path), env) != nil; got != want {
			t.Errorf("放行 %q：得到 %v，想要 %v", path, got, want)
		}
	}
}

func TestWebAndTaskAndMCPRules(t *testing.T) {
	deny := mustRules(t, [2]string{"WebFetch(domain:evil.test)", "deny"}, [2]string{"Task(worker)", "deny"})
	for url, want := range map[string]bool{"https://evil.test/a": true, "http://sub.evil.test:8080/x?y": true, "https://notevil.test/": false, "https://evil.test.example.com/": false, "https://user@evil.test/": true} {
		if got := MatchDeny(deny, "web_fetch", ruleArgs("url", url), Env{}) != nil; got != want {
			t.Errorf("WebFetch %q：%v", url, got)
		}
	}
	if MatchDeny(deny, "task", ruleArgs("agent", "worker"), Env{}) == nil || MatchDeny(deny, "task", ruleArgs("agent", "reader"), Env{}) != nil {
		t.Error("Task 规则按代理名")
	}
	mcp := mustRules(t, [2]string{"mcp__docs", "deny"}, [2]string{"mcp__wiki__del*", "deny"})
	for _, c := range []struct {
		server, tool string
		want         bool
	}{{"docs", "search", true}, {"docs", "anything", true}, {"wiki", "delete_page", true}, {"wiki", "read", false}, {"other", "search", false}} {
		got := false
		for _, r := range mcp {
			got = got || r.MCPMatches(c.server, c.tool)
		}
		if got != c.want {
			t.Errorf("mcp %s/%s：%v", c.server, c.tool, got)
		}
	}
}

// Decide 的顺序：拒绝先于一切放行；放行不越过受保护路径、灾难性命令、不是主人发起的；deny 不越过场合（场合先判）。
func TestDecideUserRules(t *testing.T) {
	clean := &Verdict{Names: []string{"git status"}}
	rules := mustRules(t, [2]string{"Bash(git status:*)", "allow"}, [2]string{"Bash(git push:*)", "deny"}, [2]string{"Write(src/**)", "allow"}, [2]string{"Write(src/locked/**)", "deny"})
	base := func() Env {
		return Env{Mode: "manual", Workdir: `D:\Work`, ProtectedDirs: []string{`D:\Data`}, Audience: ownerConsole, Declared: true, Rules: rules, Shell: clean}
	}
	d := Decide("powershell", Specs["powershell"], ruleArgs("command", "git status"), base())
	if d.Kind != "allow" || d.By != ByRule || d.RuleLabel != "Bash(git status:*)" || d.RuleKey != "rule:allow:Bash(git status:*)" {
		t.Fatalf("放行规则没生效：%+v", d)
	}
	d = Decide("powershell", Specs["powershell"], ruleArgs("command", "git push"), base())
	if d.Kind != "deny" || !strings.Contains(d.Reason, "git push:*") {
		t.Fatalf("拒绝规则没生效：%+v", d)
	}
	// 拒绝压过放行：同一条命令两边都盖住
	both := base()
	both.Rules = mustRules(t, [2]string{"Bash(git:*)", "allow"}, [2]string{"Bash(git push:*)", "deny"})
	both.Shell = &Verdict{Names: []string{"git push"}}
	if d = Decide("powershell", Specs["powershell"], ruleArgs("command", "git push"), both); d.Kind != "deny" {
		t.Fatalf("deny 要优先：%+v", d)
	}
	// 路径：放行、deny 压过放行（src/locked 在 src/** 里面）
	if d = Decide("write", Specs["write"], ruleArgs("path", "src/a.go"), base()); d.Kind != "allow" || d.By != ByRule {
		t.Fatalf("Write 放行：%+v", d)
	}
	if d = Decide("write", Specs["write"], ruleArgs("path", "src/locked/a.go"), base()); d.Kind != "deny" {
		t.Fatalf("Write deny：%+v", d)
	}
	// 受保护路径：放行规则盖住也只问一次
	prot := base()
	prot.Rules = mustRules(t, [2]string{`Write(D:\Data\**)`, "allow"})
	if d = Decide("write", Specs["write"], ruleArgs("path", `D:\Data\config\x.yml`), prot); d.Kind != "ask" || d.Limit != "once" || !d.Sticky {
		t.Fatalf("放行规则越过了受保护路径：%+v", d)
	}
	// 灾难性命令：放行规则盖住也要问（且只给这个对话）
	cat := base()
	cat.Rules = mustRules(t, [2]string{"Bash(format:*)", "allow"})
	cat.Shell = &Verdict{Names: []string{"format"}}
	if d = Decide("powershell", Specs["powershell"], ruleArgs("command", "format C: /q"), cat); d.Kind != "ask" || d.Limit != "conversation" {
		t.Fatalf("放行规则越过了灾难性命令：%+v", d)
	}
	// 不是主人发起的：放行规则不吃，只问一次
	not := base()
	not.NotOwner = true
	if d = Decide("write", Specs["write"], ruleArgs("path", "src/a.go"), not); d.Kind != "ask" || d.Limit != "once" {
		t.Fatalf("放行规则越过了身份：%+v", d)
	}
	// 场合先判：不是主人 × 主窗口，只回"这个工具不能用"（不泄露规则）
	qq := base()
	qq.Audience = Audience{Channel: "qq"}
	if d = Decide("powershell", Specs["powershell"], ruleArgs("command", "git push"), qq); d.Kind != "deny" || strings.Contains(d.Reason, "git push") {
		t.Fatalf("QQ 渠道的回答：%+v", d)
	}
	// 命令拆不开：放行规则不生效，照模式问
	blind := base()
	blind.Shell = nil
	if d = Decide("powershell", Specs["powershell"], ruleArgs("command", "git status"), blind); d.Kind != "ask" {
		t.Fatalf("拆不开的命令吃了放行规则：%+v", d)
	}
	// 查网：拒绝规则能挡住默认放行
	web := base()
	web.Rules = mustRules(t, [2]string{"WebFetch(domain:evil.test)", "deny"})
	if d = Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://evil.test/a"), web); d.Kind != "deny" {
		t.Fatalf("默认放行的查网没被拒绝规则挡住：%+v", d)
	}
	if d = Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://ok.test/a"), web); d.Kind != "allow" || d.By != ByDefault {
		t.Fatalf("别的域名照旧放行：%+v", d)
	}
}

// 放行规则只盖住"每段开头的命令"：括号里先执行的、夹在参数里的命令不能顺带放行（复核：Get-ChildItem (Remove-Item x)）。
func TestAllowRuleNotBypassedByNestedCommands(t *testing.T) {
	allow := mustRules(t, [2]string{"Bash(Get-ChildItem:*)", "allow"}, [2]string{"Bash(dir:*)", "allow"}, [2]string{"Bash(cat:*)", "allow"})
	for _, c := range []struct {
		cmd   string
		names []string
	}{
		{"Get-ChildItem (Remove-Item -Recurse -Force src)", []string{"get-childitem", "remove-item"}},
		{`dir (Remove-Item src\a.txt)`, []string{"dir", "remove-item"}},
		{"cat (Set-Content a.txt hi)", []string{"cat", "set-content"}},
		{"cat a.txt", []string{"cat", "remove-item"}}, // 括号被别的路径挡不住时，名字检查也要拦
	} {
		if r := MatchAllow(allow, "powershell", ruleArgs("command", c.cmd), Env{Shell: &Verdict{Names: c.names}}); r != nil {
			t.Errorf("%q 不该被放行规则盖住：%s", c.cmd, r.Raw)
		}
	}
	if MatchAllow(allow, "powershell", ruleArgs("command", "dir src"), Env{Shell: &Verdict{Names: []string{"dir"}}}) == nil {
		t.Error("正常的只读命令照旧放行")
	}
}

// 拒绝规则按分析器给出的命令名再比一遍：-C、.exe、引号、别名换个写法不能绕过。
func TestDenyRuleSeesAnalyzerNames(t *testing.T) {
	deny := mustRules(t, [2]string{"Bash(git push:*)", "deny"}, [2]string{"Bash(rm:*)", "deny"})
	for _, c := range []struct {
		cmd   string
		names []string
		want  bool
	}{
		{"git -C . push", []string{"git push"}, true},
		{"git.exe push", []string{"git push"}, true},
		{"& 'git' push", []string{"git push"}, true},
		{"ri x", []string{"ri"}, true},
		{"Remove-Item x", []string{"remove-item"}, true},
		{"del x", []string{"del"}, true},
		{"git -C . pull", []string{"git pull"}, false},
		{"Get-Content x", []string{"get-content"}, false},
	} {
		if got := MatchDeny(deny, "powershell", ruleArgs("command", c.cmd), Env{Shell: &Verdict{Names: c.names}}) != nil; got != c.want {
			t.Errorf("拒绝 %q：得到 %v，想要 %v", c.cmd, got, c.want)
		}
	}
}

// 拒绝读 secrets 里的内容：对上级目录递归 grep、find 也要拒；ls 和读别的目录不受影响。
func TestDenyReadCoversRecursiveSearchFromParent(t *testing.T) {
	env := Env{Workdir: `D:\Work`}
	deny := mustRules(t, [2]string{"Read(secrets/**)", "deny"})
	for _, c := range []struct {
		tool, path string
		want       bool
	}{
		{"grep", ".", true}, {"grep", `D:\Work`, true}, {"find", ".", true}, {"grep", "secrets", true}, {"grep", "secrets/a", true},
		{"grep", "src", false}, {"find", "public", false}, {"read", ".", false}, {"ls", ".", false},
		{"grep", `D:\`, true}, {"grep", `D:\Work2`, false},
	} {
		if got := MatchDeny(deny, c.tool, ruleArgs("path", c.path), env) != nil; got != c.want {
			t.Errorf("拒绝 %s %q：得到 %v，想要 %v", c.tool, c.path, got, c.want)
		}
	}
	file := mustRules(t, [2]string{"Read(notes/private.md)", "deny"})
	if MatchDeny(file, "grep", ruleArgs("path", "notes"), env) == nil || MatchDeny(file, "grep", ruleArgs("path", "docs"), env) != nil {
		t.Error("规则是单个文件时，搜它所在的目录要拒，搜别处不拒")
	}
}
