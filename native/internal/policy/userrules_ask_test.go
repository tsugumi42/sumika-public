package policy

import (
	"strings"
	"testing"
)

// 规格-审批分级 8.7：ask 规则（匹配到的调用每次都问）和规则的作用范围（项目目录、会话）。

func scoped(t *testing.T, raw, effect, project, session string) UserRule {
	t.Helper()
	r, err := ParseScopedRule(raw, effect, project, session)
	if err != nil {
		t.Fatalf("%s：%v", raw, err)
	}
	return r
}

func askBase(rules ...UserRule) Env {
	return Env{Mode: "manual", Workdir: `D:\Work`, ProtectedDirs: []string{`D:\Data`}, Audience: ownerConsole, Declared: true, Rules: rules, Conversation: "local:console:a"}
}

func TestAskRuleOverridesEveryAllow(t *testing.T) {
	ask := mustRules(t, [2]string{"Write(src/**)", "ask"})
	write := ruleArgs("path", "src/a.go")
	// 档位放行、放行规则、记住、这个对话：ask 规则都压得过，而且只给"这一次"
	cases := map[string]Env{
		"yolo": func() Env { e := askBase(ask...); e.Mode = "yolo"; return e }(),
		"auto": func() Env { e := askBase(ask...); e.Mode = "auto"; return e }(),
		"记住":   func() Env { e := askBase(ask...); e.Granted = "always"; return e }(),
		"这个对话": func() Env { e := askBase(ask...); e.Granted = "conversation"; return e }(),
		"放行规则同时命中": func() Env {
			e := askBase(append(mustRules(t, [2]string{"Write(src/**)", "allow"}), ask...)...)
			return e
		}(),
	}
	for name, env := range cases {
		d := Decide("write", Specs["write"], write, env)
		if d.Kind != "ask" || d.Limit != "once" || !d.Sticky || d.RuleKey != "rule:ask:Write(src/**)" || !strings.Contains(d.Reason, "Write(src/**)") {
			t.Errorf("%s：ask 规则没压住放行：%+v", name, d)
		}
	}
	// 没盖住的调用不受影响
	if d := Decide("write", Specs["write"], ruleArgs("path", "docs/a.md"), func() Env { e := askBase(ask...); e.Mode = "yolo"; return e }()); d.Kind != "allow" {
		t.Errorf("没命中的调用该照旧放行：%+v", d)
	}
	// 默认放行的查网也能被 ask 规则拦成问
	web := mustRules(t, [2]string{"WebFetch(domain:example.com)", "ask"})
	if d := Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://a.example.com/x"), askBase(web...)); d.Kind != "ask" || d.Limit != "once" {
		t.Errorf("默认放行档该被 ask 规则拦住：%+v", d)
	}
	if d := Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://other.org/x"), askBase(web...)); d.Kind != "allow" {
		t.Errorf("别的域名不该受影响：%+v", d)
	}
}

func TestAskRulePrecedence(t *testing.T) {
	// deny > ask > allow
	rs := mustRules(t, [2]string{"Bash(git:*)", "allow"}, [2]string{"Bash(git push:*)", "ask"}, [2]string{"Bash(git push:*)", "deny"})
	env := askBase(rs...)
	env.Shell = &Verdict{Names: []string{"git push"}}
	if d := Decide("powershell", Specs["powershell"], ruleArgs("command", "git push"), env); d.Kind != "deny" {
		t.Fatalf("deny 要压过 ask：%+v", d)
	}
	env.Rules = mustRules(t, [2]string{"Bash(git:*)", "allow"}, [2]string{"Bash(git push:*)", "ask"})
	if d := Decide("powershell", Specs["powershell"], ruleArgs("command", "git push"), env); d.Kind != "ask" || d.Limit != "once" {
		t.Fatalf("ask 要压过 allow：%+v", d)
	}
	env.Shell = &Verdict{Names: []string{"git status"}}
	if d := Decide("powershell", Specs["powershell"], ruleArgs("command", "git status"), env); d.Kind != "allow" || d.By != ByRule {
		t.Fatalf("没命中 ask 的照旧放行：%+v", d)
	}
}

// ask 规则只会让"放行"变成"问"：本来就问的、本来就拒的，结论、范围档一个字不动。
func TestAskRuleNeverLoosens(t *testing.T) {
	ask := mustRules(t, [2]string{"Write(**)", "ask"}, [2]string{"Bash(format:*)", "ask"}, [2]string{"Task", "ask"})
	// 受保护路径：本来就是只问一次
	d := Decide("write", Specs["write"], ruleArgs("path", `D:\Data\x.yml`), askBase(ask...))
	if d.Kind != "ask" || d.Limit != "once" || !d.Sticky || d.RuleKey != "" {
		t.Errorf("受保护路径的结论被改了：%+v", d)
	}
	// 灾难性命令：本来问、给"这个对话"——ask 规则盖住以后还是问，范围档收成只这一次（只会更严），原因保留
	env := askBase(ask...)
	env.Shell = &Verdict{Names: []string{"format"}}
	if d := Decide("powershell", Specs["powershell"], ruleArgs("command", "format C: /q"), env); d.Kind != "ask" || d.Limit != "once" || !d.Sticky || d.RuleKey != "rule:ask:Bash(format:*)" || d.Reason != whyCatastrophe {
		t.Errorf("灾难性命令该收成只问一次、保留原因：%+v", d)
	}
	// 手动档下普通的问（本来有范围档）：被 ask 规则盖住以后也只给这一次
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), askBase(ask...)); d.Kind != "ask" || d.Limit != "once" || d.RuleKey != "rule:ask:Write(**)" {
		t.Errorf("本来就问的该收成只问一次：%+v", d)
	}
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), askBase()); d.Kind != "ask" || d.Limit == "once" {
		t.Errorf("前提：没有 ask 规则时手动档的问是有范围档的：%+v", d)
	}
	// 不是主人发起的：只问一次
	env = askBase(ask...)
	env.NotOwner = true
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.Kind != "ask" || d.Limit != "once" {
		t.Errorf("非主人发起的结论被改了：%+v", d)
	}
	// 场合不对：照旧 deny（不会变成 ask 把卡弹给不该看的人）
	env = askBase(ask...)
	env.Audience = Audience{Channel: "qq", PrivateOk: true}
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.Kind != "deny" {
		t.Errorf("场合挡掉的该照旧 deny：%+v", d)
	}
	// 先于一切的 deny 规则
	env = askBase(append(ask, mustRules(t, [2]string{"Write(a.go)", "deny"})...)...)
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.Kind != "deny" {
		t.Errorf("deny 规则丢了：%+v", d)
	}
}

func TestAskRuleMatchesLooselyLikeDeny(t *testing.T) {
	ask := mustRules(t, [2]string{"Bash(rm:*)", "ask"})
	env := askBase(ask...)
	env.Mode = "yolo"
	// 复合命令里任何一段命中、别名、分析器给的名字都算
	env.Shell = &Verdict{Names: []string{"echo", "Remove-Item"}}
	if d := Decide("powershell", Specs["powershell"], ruleArgs("command", "echo hi; Remove-Item a.txt"), env); d.Kind != "ask" {
		t.Errorf("别名、复合命令里的一段该命中 ask：%+v", d)
	}
	env.Shell = &Verdict{Names: []string{"echo"}}
	if d := Decide("powershell", Specs["powershell"], ruleArgs("command", `echo "rm -rf x"`), env); d.Kind != "ask" {
		t.Errorf("原文里出现该命中 ask（宁多问）：%+v", d)
	}
}

func TestScopedRuleKeysAndParse(t *testing.T) {
	if RuleKey("allow", "Bash(ls)") != ScopedRuleKey("allow", "Bash(ls)", "", "") {
		t.Fatal("没有范围的 key 要和旧的一样，旧文件不能失效")
	}
	k1 := ScopedRuleKey("ask", "Bash(ls)", `D:\Work`, "")
	k2 := ScopedRuleKey("ask", "Bash(ls)", `d:/work/`, "")
	k3 := ScopedRuleKey("ask", "Bash(ls)", `D:\Other`, "")
	k4 := ScopedRuleKey("ask", "Bash(ls)", "", "c1")
	if k1 != k2 {
		t.Errorf("大小写、斜杠、结尾分隔符不同的同一目录该是同一个 key：%q %q", k1, k2)
	}
	if k1 == k3 || k1 == k4 || k1 == RuleKey("ask", "Bash(ls)") {
		t.Errorf("不同范围的 key 不能相同：%q %q %q", k1, k3, k4)
	}
	for _, c := range []struct{ effect, project, session, want string }{
		{"deny", `D:\Work`, "", "拒绝规则不分范围"},
		{"deny", "", "c1", "拒绝规则不分范围"},
		{"ask", `src`, "", "绝对路径"},
		{"ask", `D:`, "", "绝对路径"},
		{"allow", `\x`, "", "绝对路径"},
		{"ask", "", "a@b", "会话号"},
		{"ask", "", "a\nb", "会话号"},
	} {
		if _, err := ParseScopedRule("Write(x)", c.effect, c.project, c.session); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v 该报 %q：%v", c, c.want, err)
		}
	}
	if _, err := ParseScopedRule("mcp__srv", "ask", `D:\Work`, ""); err == nil || !strings.Contains(err.Error(), "MCP") {
		t.Errorf("MCP 规则不能限定项目：%v", err)
	}
	if _, err := ParseScopedRule("mcp__srv", "ask", "", "c1"); err != nil {
		t.Errorf("MCP 规则可以限定会话：%v", err)
	}
	if _, err := ParseScopedRule("Write(x)", "ask", `\\server\share\proj`, ""); err != nil {
		t.Errorf("UNC 绝对路径该能写：%v", err)
	}
}

func TestProjectScope(t *testing.T) {
	write := ruleArgs("path", "a.go")
	for _, effect := range []string{"allow", "ask"} {
		rule := scoped(t, "Write", effect, `D:\Work`, "")
		for _, c := range []struct {
			workdir string
			hit     bool
			why     string
		}{
			{`D:\Work`, true, "目录本身"},
			{`d:/work/`, true, "大小写、斜杠、结尾分隔符"},
			{`D:\Work\sub\deep`, true, "子目录"},
			{`D:\Work2`, false, "前缀相同的另一个目录"},
			{`D:\Other`, false, "别的目录"},
			{`D:\`, false, "上级目录"},
			{`D:\Work\..\Other`, false, "带 .. 绕出去"},
		} {
			env := askBase(rule)
			env.Mode = "yolo" // yolo 下写工作目录里的文件本来放行，ask 规则才有东西可拦
			env.Workdir = c.workdir
			d := Decide("write", Specs["write"], write, env)
			hit := d.RuleKey == rule.Key
			if hit != c.hit {
				t.Errorf("%s 规则在 %q（%s）：命中=%v 想要 %v：%+v", effect, c.workdir, c.why, hit, c.hit, d)
			}
		}
	}
	// 目录不存在：只是字符串比较，不报错、不放大
	ghost := scoped(t, "Write", "allow", `Z:\不存在的\项目`, "")
	env := askBase(ghost)
	env.Workdir = `Z:\不存在的\项目\src`
	if d := Decide("write", Specs["write"], write, env); d.By != ByRule {
		t.Errorf("目录不存在也按字符串比：%+v", d)
	}
	// 工作目录看不出（空）：allow 不吃，ask 照样问
	empty := askBase(scoped(t, "Write", "allow", `D:\Work`, ""))
	empty.Workdir = ""
	if d := Decide("write", Specs["write"], write, empty); d.By == ByRule {
		t.Errorf("看不出工作目录时项目范围的 allow 不能放行：%+v", d)
	}
	askRule := scoped(t, "WebFetch", "ask", `D:\Work`, "")
	empty = askBase(askRule)
	empty.Workdir = ""
	if d := Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://a.org/"), empty); d.Kind != "ask" || d.RuleKey != askRule.Key {
		t.Errorf("看不出工作目录时项目范围的 ask 要照样问：%+v", d)
	}
}

func TestSessionScope(t *testing.T) {
	write := ruleArgs("path", "a.go")
	allow := scoped(t, "Write", "allow", "", "local:console:a")
	ask := scoped(t, "Write", "ask", "", "local:console:a")
	run := func(rule UserRule, conv string, anc ...string) Decision {
		env := askBase(rule)
		env.Mode = "yolo"
		env.Conversation, env.Ancestors = conv, anc
		return Decide("write", Specs["write"], write, env)
	}
	if d := run(allow, "local:console:a"); d.By != ByRule {
		t.Errorf("本会话的 allow 该放行：%+v", d)
	}
	if d := run(allow, "local:console:b"); d.By == ByRule {
		t.Errorf("别的会话不能吃 allow：%+v", d)
	}
	if d := run(allow, "local:console:A"); d.By == ByRule {
		t.Errorf("会话号区分大小写，近似的不算：%+v", d)
	}
	if d := run(allow, ""); d.By == ByRule {
		t.Errorf("看不出会话时 allow 不放行：%+v", d)
	}
	if d := run(allow, "internal:t1", "local:console:a"); d.By == ByRule {
		t.Errorf("子任务不继承上级会话的 allow：%+v", d)
	}
	if d := run(ask, "local:console:a"); d.Kind != "ask" || d.RuleKey != ask.Key {
		t.Errorf("本会话的 ask 该问：%+v", d)
	}
	if d := run(ask, "local:console:b"); d.Kind == "ask" {
		t.Errorf("别的会话不该被问：%+v", d)
	}
	if d := run(ask, ""); d.Kind != "ask" {
		t.Errorf("看不出会话时 ask 照样问：%+v", d)
	}
	if d := run(ask, "internal:t1", "local:console:a"); d.Kind != "ask" {
		t.Errorf("子任务替那个会话办事，ask 要跟着：%+v", d)
	}
	if d := run(ask, "internal:t1", "local:console:z"); d.Kind == "ask" {
		t.Errorf("别人派的子任务不该被问：%+v", d)
	}
	// 项目和会话都写：要同时满足
	both := scoped(t, "Write", "ask", `D:\Work`, "local:console:a")
	env := askBase(both)
	env.Mode = "yolo"
	env.Workdir = `D:\Other`
	if d := Decide("write", Specs["write"], write, env); d.Kind == "ask" {
		t.Errorf("两个范围要同时满足：%+v", d)
	}
}

// 拒绝不分范围：手写带了范围的 deny 也是全局的（上层把范围丢掉再解析）
func TestDenyIgnoresScope(t *testing.T) {
	deny := mustRules(t, [2]string{"Write(a.go)", "deny"})
	env := askBase(deny...)
	env.Workdir, env.Conversation = `Z:\别处`, "other"
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.Kind != "deny" {
		t.Errorf("deny 在任何目录、会话都生效：%+v", d)
	}
}
