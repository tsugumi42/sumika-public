package approval

// 主人自己写的放行、拒绝规则（B11）：和"记住"共用 tool-grants.yml，这里测存储、读回、门里的效果、撤销立刻生效。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

func TestUserRulesStoreRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tool-grants.yml")
	g := LoadGrants(file)
	if _, err := g.PutRule("Bash(git status:*)", policy.EffectAllow, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PutRule("Bash(git push:*)", policy.EffectDeny, 11); err != nil {
		t.Fatal(err)
	}
	// 同一原文同一效果不重复；点"记住"记下的和规则放在同一份文件里
	if _, err := g.PutRule("Bash(git status:*)", policy.EffectAllow, 12); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(Rule{Key: "write:d:\\notes", Label: "「D:\\Notes 里的文件」", Tool: "write", At: 13}); err != nil {
		t.Fatal(err)
	}
	// 写错的不写进去，说清楚错在哪
	if _, err := g.PutRule("Rm(x)", policy.EffectDeny, 14); err == nil || !strings.Contains(err.Error(), "不认识的工具名") {
		t.Fatalf("写错的规则：%v", err)
	}
	if _, err := g.PutRule("Bash", "maybe", 14); err == nil {
		t.Fatal("效果写错也该拒绝")
	}
	back := LoadGrants(file) // 重启读回来
	rules := back.UserRules()
	if len(rules) != 2 || rules[0].Raw != "Bash(git status:*)" || rules[0].Effect != "allow" || rules[1].Effect != "deny" {
		t.Fatalf("读回的规则：%+v", rules)
	}
	list := back.List()
	if len(list) != 3 || list[1].Effect != policy.EffectDeny || list[1].Pattern != "Bash(git push:*)" || list[2].Effect != "" || list[2].Pattern != "" {
		t.Fatalf("列出来的：%+v", list)
	}
	// 撤销
	if ok, err := back.Remove(policy.RuleKey("deny", "Bash(git push:*)")); !ok || err != nil {
		t.Fatalf("撤销：%v %v", ok, err)
	}
	if len(LoadGrants(file).UserRules()) != 1 {
		t.Fatal("撤销没落盘")
	}
}

// 手写 yaml：只写 pattern、effect，不用自己编 key；写错的规则被跳过并留在 BadRules，不让整份读不了。
func TestUserRulesHandWritten(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tool-grants.yml")
	text := "grants:\n- pattern: Bash(go test:*)\n- pattern: Write(.env)\n  effect: deny\n- pattern: Frobnicate(x)\n  effect: deny\n- pattern: Bash(a)\n  effect: maybe\n- key: write:d:\\old\n  label: 旧的记住\n  tool: write\n"
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	g := LoadGrants(file)
	rules := g.UserRules()
	if len(rules) != 2 || rules[0].Effect != "allow" || rules[0].Key != "rule:allow:Bash(go test:*)" || rules[1].Effect != "deny" {
		t.Fatalf("手写规则：%+v", rules)
	}
	if len(g.BadRules) != 2 || !strings.Contains(strings.Join(g.BadRules, "|"), "Frobnicate") {
		t.Fatalf("写错的要留下原因：%v", g.BadRules)
	}
	if len(g.List()) != 5 || !g.Has("write:d:\\old") {
		t.Fatalf("旧的记住不该被动：%+v", g.List())
	}
	// 写错的规则也留在列表里（规则页能看到、能撤销）；再加一条：手写的原样留着（别的条目、别的字段不丢）
	if _, err := g.PutRule("Read(x/**)", policy.EffectDeny, 1); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	for _, want := range []string{"Bash(go test:*)", "Write(.env)", "旧的记住"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("写回丢了 %q：\n%s", want, b)
		}
	}
}

func ruleGate(t *testing.T, rules ...[2]string) (*Gate, string) {
	t.Helper()
	g := NewGate(t.TempDir())
	t.Cleanup(g.Analyzer.Close)
	for _, r := range rules {
		if _, err := g.Grants.PutRule(r[0], r[1], 1); err != nil {
			t.Fatal(err)
		}
	}
	return g, lowerTempDir(t)
}

func ruleCheck(t *testing.T, g *Gate, work, conv, tool, args string) (Clearance, int) {
	t.Helper()
	a, err := jsjson.Parse(args)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	g.Ask = func(context.Context, Request, *Pending) Decision { asked++; return Decision{Kind: "denied"} }
	cl := g.Clear(context.Background(), work, "suzu", policy.Audience{Channel: "console", PrivateOk: true}, conv, Call{ID: "c1", Name: tool, Args: a})
	return cl, asked
}

func TestUserRulesInGate(t *testing.T) {
	an := policy.NewAnalyzer()
	defer an.Close()
	if an.Analyze("Get-Date", time.Minute) == nil {
		t.Skip("这台机器起不来拆命令的 pwsh")
	}
	g, work := ruleGate(t, [2]string{"Bash(git status:*)", "allow"}, [2]string{"Bash(git push:*)", "deny"}, [2]string{"Write(notes/**)", "allow"}, [2]string{"Read(secret/**)", "deny"})
	g.Analyzer.Close()
	g.Analyzer = an
	conv := "console:suzu"
	// 放行规则：手动档也不问，留痕（By 是 rule，带规则 key 和原文）
	cl, asked := ruleCheck(t, g, work, conv, "powershell", `{"command":"git status"}`)
	if cl.Blocked || asked != 0 || cl.By != policy.ByRule || cl.Label != "Bash(git status:*)" || !cl.Traced || cl.Recheck == nil {
		t.Fatalf("放行规则：%+v 问了 %d 次", cl, asked)
	}
	if !strings.Contains(cl.Note(), "放行规则") || RuleText(cl.By, cl.Label, cl.Reason) == "" {
		t.Fatalf("留痕说法：%q %q", cl.Note(), RuleText(cl.By, cl.Label, cl.Reason))
	}
	if why := cl.Recheck(); why != "" {
		t.Fatalf("规则还在，不该作废：%s", why)
	}
	// 拒绝规则：不弹卡，直接把原因交给模型
	cl, asked = ruleCheck(t, g, work, conv, "powershell", `{"command":"git push origin main"}`)
	if !cl.Blocked || asked != 0 || !strings.Contains(cl.Reason, "git push:*") {
		t.Fatalf("拒绝规则：%+v 问了 %d 次", cl, asked)
	}
	cl, asked = ruleCheck(t, g, work, conv, "read", `{"path":"secret/a.txt"}`)
	if !cl.Blocked || asked != 0 {
		t.Fatalf("Read 拒绝规则：%+v", cl)
	}
	// 没有规则的照旧问
	if cl, asked = ruleCheck(t, g, work, conv, "powershell", `{"command":"git pull"}`); asked != 1 {
		t.Fatalf("没有规则的命令照旧问：%+v 问了 %d 次", cl, asked)
	}
	// 写文件放行规则
	if cl, asked = ruleCheck(t, g, work, conv, "write", `{"path":"notes/a.txt","content":"x"}`); cl.Blocked || asked != 0 || cl.By != policy.ByRule {
		t.Fatalf("Write 放行规则：%+v 问了 %d", cl, asked)
	}
	// 受保护路径：Write 放行规则盖住也要问（只一次）
	g.Grants.PutRule("Write("+g.ProtectedDirs[0]+`\**)`, policy.EffectAllow, 2)
	if cl, asked = ruleCheck(t, g, work, conv, "write", `{"path":`+quote(filepath.Join(g.ProtectedDirs[0], "config", "x.yml"))+`,"content":"x"}`); asked != 1 {
		t.Fatalf("规则越过了受保护路径：%+v 问了 %d", cl, asked)
	}
	// 不是主人发起的（群聊会话）：放行规则不吃
	if cl, asked = ruleCheck(t, g, work, "console:suzu:group:g", "powershell", `{"command":"git status"}`); asked != 1 {
		t.Fatalf("群聊吃了放行规则：%+v 问了 %d", cl, asked)
	}
	// 撤销立刻生效：放行之后还没执行的那一步也作废
	cl, _ = ruleCheck(t, g, work, conv, "powershell", `{"command":"git status"}`)
	if ok, err := g.Grants.Remove(policy.RuleKey("allow", "Bash(git status:*)")); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if why := cl.Recheck(); why == "" {
		t.Fatal("撤销以后还没执行的那一步该作废")
	}
	if cl, asked = ruleCheck(t, g, work, conv, "powershell", `{"command":"git status"}`); asked != 1 {
		t.Fatalf("撤销后又要问：%+v 问了 %d", cl, asked)
	}
}

// MCP：mcp__服务器__工具 的 deny 先于一切、allow 不越过身份。
func TestUserRulesMCP(t *testing.T) {
	g, _ := ruleGate(t, [2]string{"mcp__docs__delete*", "deny"}, [2]string{"mcp__docs", "allow"})
	asked := 0
	g.Ask = func(context.Context, Request, *Pending) Decision { asked++; return Decision{Kind: "denied"} }
	owner := ownerCtx(t, "private")
	tgt := &namedTarget{server: "docs", raw: "delete_page"}
	if cl := g.CheckMCP(owner, "suzu", "console:suzu", "t1", tgt); !cl.Blocked || asked != 0 {
		t.Fatalf("mcp deny：%+v", cl)
	}
	tgt = &namedTarget{server: "docs", raw: "search"}
	if cl := g.CheckMCP(owner, "suzu", "console:suzu", "t2", tgt); cl.Blocked || asked != 0 || cl.By != policy.ByRule {
		t.Fatalf("mcp allow：%+v 问了 %d", cl, asked)
	}
	tgt = &namedTarget{server: "wiki", raw: "search"}
	if cl := g.CheckMCP(owner, "suzu", "console:suzu", "t3", tgt); asked != 1 {
		t.Fatalf("别的服务器照旧问：%+v 问了 %d", cl, asked)
	}
}

// namedTarget 是服务器、原始工具名可以指定的冻结目标。
type namedTarget struct {
	server, raw string
}

func (*namedTarget) Valid() bool { return true }
func (n *namedTarget) Target() (string, string, string, string) {
	return n.server, n.raw, "mcp__" + n.server + "__" + n.raw, "digest-1"
}
func (*namedTarget) Arguments() json.RawMessage { return json.RawMessage(`{}`) }
func (*namedTarget) Binding() string            { return "bind-1" }
func (*namedTarget) Fingerprint() string        { return "fp-1" }
func (*namedTarget) ReadOnlyClaim() bool        { return false }
