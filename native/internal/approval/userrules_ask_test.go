package approval

// ask 规则（每次都问）和规则的作用范围（项目目录、会话）：规格-审批分级 8.7。
// 这里测存储读回、手写、门里的效果（Clear、CheckMCP）、子任务、撤销；判定顺序本身在 policy 的 userrules_ask_test.go。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

func TestScopedRulesStore(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tool-grants.yml")
	g := LoadGrants(file)
	put := func(raw, eff, project, session string) {
		t.Helper()
		if _, err := g.PutScopedRule(raw, eff, project, session, 5); err != nil {
			t.Fatalf("%s %s：%v", eff, raw, err)
		}
	}
	put("Write(src/**)", "ask", "", "")
	put("Write(src/**)", "ask", `D:\Work`, "")
	put("Write(src/**)", "ask", `d:/work/`, "") // 同一目录换写法：还是同一条
	put("Write(src/**)", "ask", "", "console:suzu")
	put("Write(src/**)", "allow", `D:\Work`, "console:suzu")
	put("Write(src/**)", "deny", "", "")
	if _, err := g.PutScopedRule("Write(src/**)", "deny", `D:\Work`, "", 5); err == nil {
		t.Fatal("拒绝带范围该报错")
	}
	if _, err := g.PutScopedRule("Write(src/**)", "ask", `relative`, "", 5); err == nil {
		t.Fatal("相对路径该报错")
	}
	back := LoadGrants(file)
	rules := back.UserRules()
	if len(rules) != 5 {
		t.Fatalf("同一原文不同范围各是一条、同范围写法不同只留一条，得到 %d 条：%+v", len(rules), rules)
	}
	list := back.List()
	var sawProject, sawSession, sawBoth bool
	for _, r := range list {
		switch {
		case r.Project != "" && r.Session != "":
			sawBoth = r.Effect == "allow"
		case r.Project != "":
			sawProject = r.Effect == "ask"
		case r.Session != "":
			sawSession = r.Effect == "ask"
		}
	}
	if !sawProject || !sawSession || !sawBoth {
		t.Fatalf("规则页要能读回范围：%+v", list)
	}
	// 撤销只撤那一条
	if ok, err := back.Remove(policy.ScopedRuleKey("ask", "Write(src/**)", `D:\WORK`, "")); !ok || err != nil {
		t.Fatalf("撤销带范围的：%v %v", ok, err)
	}
	if len(LoadGrants(file).UserRules()) != 4 {
		t.Fatal("撤销没落盘或撤多了")
	}
}

func TestScopedRulesHandWritten(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tool-grants.yml")
	text := "grants:\n" +
		"- pattern: Write(a)\n  effect: ask\n  project: D:\\Work\n" + // 好的
		"- pattern: Write(b)\n  effect: ask\n  project: relative\\dir\n" + // 范围写坏的 ask：当全局（多问）
		"- pattern: Write(c)\n  effect: allow\n  project: relative\\dir\n" + // 范围写坏的 allow：整条不要（多问）
		"- pattern: Write(d)\n  effect: deny\n  project: D:\\Work\n  session: x\n" + // 带范围的 deny：当全局（多拦）
		"- pattern: mcp__srv\n  effect: ask\n  project: D:\\Work\n" // MCP 不能限定项目：ask 当全局
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	g := LoadGrants(file)
	by := map[string]policy.UserRule{}
	for _, r := range g.UserRules() {
		by[r.Raw] = r
	}
	if r := by["Write(a)"]; r.Project != `D:\Work` {
		t.Errorf("好的范围要读进来：%+v", r)
	}
	if r, ok := by["Write(b)"]; !ok || r.Project != "" || r.Session != "" {
		t.Errorf("范围写坏的 ask 该当全局：%+v %v", r, ok)
	}
	if _, ok := by["Write(c)"]; ok {
		t.Error("范围写坏的 allow 不该生效")
	}
	if r, ok := by["Write(d)"]; !ok || r.Project != "" || r.Session != "" || r.Effect != "deny" {
		t.Errorf("带范围的 deny 该当全局：%+v %v", r, ok)
	}
	if r, ok := by["mcp__srv"]; !ok || r.Project != "" {
		t.Errorf("MCP 的 ask 带了项目该当全局：%+v %v", r, ok)
	}
	if len(g.BadRules) != 3 {
		t.Errorf("写坏的要留下原因（b、c、mcp）：%v", g.BadRules)
	}
	for _, b := range g.BadRules {
		if strings.Contains(b, "(这条每次都问") {
			t.Errorf("括号用全角：%s", b)
		}
	}
}

type askCapture struct {
	asked   int
	options []string
	why     string
}

func askGate(t *testing.T, mode string, rules ...[4]string) (*Gate, string, *askCapture) {
	t.Helper()
	g := NewGate(t.TempDir())
	t.Cleanup(g.Analyzer.Close)
	if err := g.SetMode(mode); err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if _, err := g.Grants.PutScopedRule(r[0], r[1], r[2], r[3], 1); err != nil {
			t.Fatal(err)
		}
	}
	cap := &askCapture{}
	g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
		cap.asked++
		cap.options, cap.why = req.Options, req.Why
		return Decision{Kind: "approved"}
	}
	return g, lowerTempDir(t), cap
}

func clearWrite(t *testing.T, g *Gate, work, conv string) Clearance {
	t.Helper()
	a, err := jsjson.Parse(`{"path":"notes/a.txt","content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	return g.Clear(context.Background(), work, "suzu", policy.Audience{Channel: "console", PrivateOk: true}, conv, Call{ID: "c1", Name: "write", Args: a})
}

// 每个档位、放行规则、记住都压得过；问的时候只给"这一次"，不给"这个对话""记住"。
func TestAskRuleInGate(t *testing.T) {
	for _, mode := range []string{"manual", "auto", "yolo"} {
		g, work, cap := askGate(t, mode, [4]string{"Write(notes/**)", "allow", "", ""}, [4]string{"Write(notes/**)", "ask", "", ""})
		cl := clearWrite(t, g, work, "console:suzu")
		if cap.asked != 1 || len(cap.options) != 0 || !strings.Contains(cap.why, "每次都问") || cl.Blocked {
			t.Fatalf("%s：ask 规则该问一次且没有范围档：问了 %d 次 options=%v why=%q %+v", mode, cap.asked, cap.options, cap.why, cl)
		}
		if cl.By == policy.ByRule {
			t.Fatalf("%s：被问过以后不该算规则放行：%+v", mode, cl)
		}
		// 批准一次不留下任何东西：下一次还问
		clearWrite(t, g, work, "console:suzu")
		if cap.asked != 2 {
			t.Fatalf("%s：每次都问，第二次没问：%d", mode, cap.asked)
		}
	}
	// 只有 allow：不问
	g, work, cap := askGate(t, "manual", [4]string{"Write(notes/**)", "allow", "", ""})
	if cl := clearWrite(t, g, work, "console:suzu"); cap.asked != 0 || cl.By != policy.ByRule {
		t.Fatalf("只有 allow 该直接放行：%+v 问了 %d", cl, cap.asked)
	}
	// 撤销 ask 规则以后恢复
	g, work, cap = askGate(t, "yolo", [4]string{"Write(notes/**)", "ask", "", ""})
	clearWrite(t, g, work, "console:suzu")
	if cap.asked != 1 {
		t.Fatal("yolo 下 ask 规则该问")
	}
	if ok, err := g.Grants.Remove(policy.RuleKey("ask", "Write(notes/**)")); !ok || err != nil {
		t.Fatal(ok, err)
	}
	clearWrite(t, g, work, "console:suzu")
	if cap.asked != 1 {
		t.Fatalf("撤销以后不该再问：%d", cap.asked)
	}
}

// "记住"、"这个对话"已经有的授权，被 ask 规则压回问。
func TestAskRuleBeatsGrants(t *testing.T) {
	g, work, cap := askGate(t, "manual")
	// 先靠"这个对话"放行
	dir := filepath.Join(work, "notes")
	g.Conv.Merge("console:suzu", Rule{Scope: policy.ByConversation, Key: "write:" + strings.ToLower(dir), Tool: "write", Workdir: work})
	if cl := clearWrite(t, g, work, "console:suzu"); cap.asked != 0 || cl.By != policy.ByConversation {
		t.Fatalf("前提：这个对话的授权该放行：%+v 问了 %d", cl, cap.asked)
	}
	if _, err := g.Grants.PutScopedRule("Write(notes/**)", "ask", "", "", 1); err != nil {
		t.Fatal(err)
	}
	if cl := clearWrite(t, g, work, "console:suzu"); cap.asked != 1 || len(cap.options) != 0 || cl.By == policy.ByConversation {
		t.Fatalf("ask 规则该压过这个对话的授权：%+v 问了 %d options=%v", cl, cap.asked, cap.options)
	}
}

func TestScopedRulesInGate(t *testing.T) {
	g, work, cap := askGate(t, "yolo")
	other := t.TempDir()
	if _, err := g.Grants.PutScopedRule("Write(notes/**)", "ask", work, "", 1); err != nil {
		t.Fatal(err)
	}
	clearWrite(t, g, other, "console:suzu")
	if cap.asked != 0 {
		t.Fatalf("别的项目目录里不该被问：%d", cap.asked)
	}
	clearWrite(t, g, work, "console:suzu")
	if cap.asked != 1 {
		t.Fatalf("项目目录里该问：%d", cap.asked)
	}
	clearWrite(t, g, filepath.Join(work, "sub"), "console:suzu")
	if cap.asked != 2 {
		t.Fatalf("项目的子目录里也该问：%d", cap.asked)
	}
	// 会话范围的 allow：本会话放行，别的会话照旧问
	g, work, cap = askGate(t, "manual", [4]string{"Write(notes/**)", "allow", "", "console:suzu"})
	if cl := clearWrite(t, g, work, "console:suzu"); cap.asked != 0 || cl.By != policy.ByRule {
		t.Fatalf("本会话 allow：%+v 问了 %d", cl, cap.asked)
	}
	clearWrite(t, g, work, "console:other")
	if cap.asked != 1 {
		t.Fatalf("别的会话不能吃：%d", cap.asked)
	}
	// 会话已经没了（规则还在）：永远不匹配，对 allow 是多问
	clearWrite(t, g, work, "console:deleted-session")
	if cap.asked != 2 {
		t.Fatalf("不存在的会话不能吃：%d", cap.asked)
	}
}

// 子任务：ask 规则跟着派它的会话；allow 规则不继承。
func TestSubtaskFollowsParentAskRule(t *testing.T) {
	g, work, cap := askGate(t, "yolo", [4]string{"Write(notes/**)", "ask", "", "console:suzu"})
	g.ParentOf = func(c string) string {
		if c == "internal:t1" {
			return "console:suzu"
		}
		if c == "internal:t2" {
			return "console:another"
		}
		return ""
	}
	clearWrite(t, g, work, "internal:t1")
	if cap.asked != 1 {
		t.Fatalf("子任务替 suzu 的会话办事，ask 要跟着：%d", cap.asked)
	}
	clearWrite(t, g, work, "internal:t2")
	if cap.asked != 1 {
		t.Fatalf("别的会话派的子任务不该被问：%d", cap.asked)
	}
	// 环、断链不卡死
	g.ParentOf = func(c string) string { return c }
	clearWrite(t, g, work, "internal:t3")
	g2, work2, cap2 := askGate(t, "manual", [4]string{"Write(notes/**)", "allow", "", "console:suzu"})
	g2.ParentOf = func(string) string { return "console:suzu" }
	clearWrite(t, g2, work2, "internal:t1")
	if cap2.asked != 1 {
		t.Fatalf("子任务不继承上级会话的 allow：%d", cap2.asked)
	}
}

func TestAskRuleMCP(t *testing.T) {
	g, _, cap := askGate(t, "yolo", [4]string{"mcp__docs", "allow", "", ""}, [4]string{"mcp__docs__delete*", "ask", "", ""})
	owner := ownerCtx(t, "private")
	if cl := g.CheckMCP(owner, "suzu", "console:suzu", "t1", &namedTarget{server: "docs", raw: "search"}); cl.Blocked || cap.asked != 0 {
		t.Fatalf("没命中 ask 的照旧放行：%+v", cl)
	}
	if cl := g.CheckMCP(owner, "suzu", "console:suzu", "t2", &namedTarget{server: "docs", raw: "delete_page"}); cl.Blocked || cap.asked != 1 || len(cap.options) != 0 {
		t.Fatalf("MCP 的 ask 该压过 yolo 和 allow，且只给这一次：%+v 问了 %d options=%v", cl, cap.asked, cap.options)
	}
	// 项目范围的 MCP 规则写不进去
	if _, err := g.Grants.PutScopedRule("mcp__docs", "ask", `D:\Work`, "", 1); err == nil {
		t.Fatal("MCP 规则不能限定项目")
	}
	// 会话范围可以
	if _, err := g.Grants.PutScopedRule("mcp__wiki", "ask", "", "console:suzu", 1); err != nil {
		t.Fatal(err)
	}
	if cl := g.CheckMCP(owner, "suzu", "console:suzu", "t3", &namedTarget{server: "wiki", raw: "x"}); cl.Blocked || cap.asked != 2 {
		t.Fatalf("会话范围的 MCP ask：%+v 问了 %d", cl, cap.asked)
	}
	if g.CheckMCP(owner, "suzu", "console:else", "t4", &namedTarget{server: "wiki", raw: "x"}); cap.asked != 2 {
		t.Fatalf("别的会话不该问：%d", cap.asked)
	}
}

// 群聊、非主人发起：ask 规则不让"只问一次"变成有范围档，也不让不可用的场合冒出卡。
func TestAskRuleNotOwnerStillOnce(t *testing.T) {
	g, work, cap := askGate(t, "yolo", [4]string{"Write(notes/**)", "ask", "", ""})
	cl := clearWrite(t, g, work, "console:suzu:group:g1")
	if cap.asked != 1 || len(cap.options) != 0 || cl.Blocked {
		t.Fatalf("群聊本来就只问一次：%+v 问了 %d options=%v", cl, cap.asked, cap.options)
	}
}
