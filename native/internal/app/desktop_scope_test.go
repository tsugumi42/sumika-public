package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

// 写文件的本地卡可以选"这个对话"：记下以后同一目录的第二次写不再弹卡；设置页的规则里看得到。
func TestDesktopWriteConversationScope(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	arg := func(name string) string {
		b, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": name, "content": "内容"})
		return string(b)
	}
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("w1", "write", arg("第一份.txt")), llmtest.Call("w2", "write", arg("第二份.txt")), llmtest.Text("写完了"),
	}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	sub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "写两份文件", nil) }()
	var card api.LocalApproval
	for card.ID == "" {
		if snap := localRecv(t, sub.Updates()); len(snap.Value) > 0 {
			card = snap.Value[0]
		}
	}
	if !hasChoice(card.Options, api.ApproveConversation) {
		t.Fatalf("卡上没有这个对话档：%v", card.Options)
	}
	if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveConversation); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done) // 第二份文件没有再弹卡，整轮就结束了
	for _, n := range []string{"第一份.txt", "第二份.txt"} {
		if _, err := os.Stat(filepath.Join(a.workdir(), n)); err != nil {
			t.Fatalf("%s 没写成：%v", n, err)
		}
	}
	rules, err := d.Reader.ListApprovalRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].Scope != string(api.ApproveConversation) || rules[0].Tool != "write" {
		t.Fatalf("规则页：%+v %v", rules, err)
	}
	if err := d.Reader.RevokeApprovalRule(ctx, rules[0]); err != nil {
		t.Fatal(err)
	}
	if rules, _ = d.Reader.ListApprovalRules(ctx); len(rules) != 0 {
		t.Fatalf("撤销后还在：%+v", rules)
	}
}

// 受保护路径（数据目录）的卡只有一次：伪造"这个对话""记住"要被拒，不能悄悄变成放行。
func TestDesktopProtectedWriteStaysOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	target := filepath.Join(e.data, "config", "编的.yml")
	b, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": target, "content": "内容"})
	_, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("w1", "write", string(b)), llmtest.Text("好")}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	sub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "写数据目录", nil) }()
	var card api.LocalApproval
	for card.ID == "" {
		if snap := localRecv(t, sub.Updates()); len(snap.Value) > 0 {
			card = snap.Value[0]
		}
	}
	if len(card.Options) != 0 {
		t.Fatalf("受保护路径的卡不该带范围档：%v", card.Options)
	}
	for _, wide := range []api.ApprovalChoice{api.ApproveConversation, api.ApproveAlways} {
		if err := d.Reader.DecideLocal(ctx, key, card.ID, wide); err == nil || errors.Is(err, api.ErrApprovalGone) {
			t.Fatalf("受保护路径的卡接受了 %s：%v", wide, err)
		}
	}
	if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveDeny); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝后仍写了：%v", err)
	}
}

// 输入框切档：只改这个会话、当场生效（写文件不再弹卡），不写配置；全局默认写设置时也当场生效。
func TestDesktopApprovalModeSwitchesNow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": "切档后写.txt", "content": "内容"})
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("w1", "write", string(b)), llmtest.Text("写完了")}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	mode, err := d.Reader.ConversationApprovalMode(ctx, key)
	if err != nil || mode.Mode != "manual" || mode.Override {
		t.Fatalf("起始：%+v %v", mode, err)
	}
	for _, bad := range []api.ConversationKey{localKey("console:" + testChar + ":group:x"), localKey("console:owner"), {Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:1:2"}} {
		if err := d.Reader.SetConversationApprovalMode(ctx, bad, "yolo"); err == nil {
			t.Fatalf("伪造会话 %+v 也能切档", bad)
		}
	}
	if err := d.Reader.SetConversationApprovalMode(ctx, key, "bogus"); err == nil {
		t.Fatal("不认识的档被接受了")
	}
	if err := d.Reader.SetConversationApprovalMode(ctx, key, "yolo"); err != nil {
		t.Fatal(err)
	}
	if mode, _ = d.Reader.ConversationApprovalMode(ctx, key); mode.Mode != "yolo" || !mode.Override || mode.Default != "manual" {
		t.Fatalf("切档后：%+v", mode)
	}
	if a.approvalGate.DefaultMode() != "manual" {
		t.Fatal("会话切档不该动全局默认")
	}
	// 免审批：工作目录里写文件当场不弹卡，整轮直接结束
	localRecv(t, func() chan error {
		done := make(chan error, 1)
		go func() { done <- d.Reader.SendLocal(ctx, key, "写一份", nil) }()
		return done
	}())
	if _, err := os.Stat(filepath.Join(a.workdir(), "切档后写.txt")); err != nil {
		t.Fatalf("免审批下没写成：%v", err)
	}
	// 全局默认：写设置当场改运行中的门，不用重启接收
	v, err := d.Reader.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Reader.WriteSetting(ctx, api.SettingChange{Key: "tools.approvalMode", Value: json.RawMessage(`"bogus"`)}, v.Version); err == nil {
		t.Fatal("不认识的审批方式写进了配置")
	}
	v, err = d.Reader.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view, err := d.Reader.WriteSetting(ctx, api.SettingChange{Key: "tools.approvalMode", Value: json.RawMessage(`"auto"`)}, v.Version)
	if err != nil {
		t.Fatal(err)
	}
	if a.approvalGate.DefaultMode() != "auto" || view.Effect != "立即" {
		t.Fatalf("默认档 %s，生效说法 %q", a.approvalGate.DefaultMode(), view.Effect)
	}
	if mode, _ = d.Reader.ConversationApprovalMode(ctx, key); mode.Mode != "yolo" || mode.Default != "auto" {
		t.Fatalf("会话自己的覆盖不该被默认档冲掉：%+v", mode)
	}
}

// 主人自己写的规则：允许的路径写文件不弹卡、拒绝的路径直接被拦、规则页看得到、写错的说清楚、撤销后恢复问。
func TestDesktopUserRulesEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	arg := func(name string) string {
		b, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": name, "content": "内容"})
		return string(b)
	}
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("w1", "write", arg("允许的/a.txt")), llmtest.Call("w2", "write", arg("拒绝的/b.txt")), llmtest.Text("处理完了"),
	}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	var editor api.ApprovalRuleEditor = &d.Reader
	if err := editor.AddApprovalRule(ctx, "Write(允许的/**)", "allow", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := editor.AddApprovalRule(ctx, "Write(拒绝的/**)", "deny", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := editor.AddApprovalRule(ctx, "Frobnicate(x)", "deny", "", ""); err == nil || !strings.Contains(err.Error(), "不认识的工具名") {
		t.Fatalf("写错的规则：%v", err)
	}
	if err := editor.AddApprovalRule(ctx, "Write(x)", "maybe", "", ""); err == nil {
		t.Fatal("效果写错也该拒绝")
	}
	rules, err := d.Reader.ListApprovalRules(ctx)
	if err != nil || len(rules) != 2 || rules[0].Effect != "allow" || rules[1].Effect != "deny" || rules[1].Pattern != "Write(拒绝的/**)" || rules[1].Label != "Write(拒绝的/**)" {
		t.Fatalf("规则页：%+v %v", rules, err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "写两份", nil) }()
	localRecv(t, done) // 一张卡都没弹，整轮就结束了：第一份靠放行规则、第二份被拒绝规则拦下
	if _, err := os.Stat(filepath.Join(a.workdir(), "允许的", "a.txt")); err != nil {
		t.Fatalf("放行规则没让它写成：%v", err)
	}
	if _, err := os.Stat(filepath.Join(a.workdir(), "拒绝的", "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝规则没拦住：%v", err)
	}
	// 撤销放行规则：下一次要问
	if err := d.Reader.RevokeApprovalRule(ctx, rules[0]); err != nil {
		t.Fatal(err)
	}
	if rules, _ = d.Reader.ListApprovalRules(ctx); len(rules) != 1 || rules[0].Effect != "deny" {
		t.Fatalf("撤销后：%+v", rules)
	}
}

// ask 规则加作用范围：放行规则盖住的路径，限定到这个项目和这个会话的 ask 规则让它每次都弹卡，卡上只有“这一次”；
// 别的项目、别的会话的 ask 规则不管它；规则页显示范围，撤销后恢复放行。
func TestDesktopAskRuleEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	arg := func(name string) string {
		b, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": name, "content": "内容"})
		return string(b)
	}
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("w1", "write", arg("问我/a.txt")), llmtest.Text("写完了"),
	}})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	var editor api.ApprovalRuleEditor = &d.Reader
	if err := editor.AddApprovalRule(ctx, "Write(问我/**)", "allow", "", ""); err != nil {
		t.Fatal(err)
	}
	// 别的项目、别的会话的 ask：不该管到这里
	if err := editor.AddApprovalRule(ctx, "Write(问我/**)", "ask", `Z:\别的项目`, ""); err != nil {
		t.Fatal(err)
	}
	if err := editor.AddApprovalRule(ctx, "Write(问我/**)", "ask", "", "console:别的会话"); err != nil {
		t.Fatal(err)
	}
	// 这个项目加这个会话的 ask
	if err := editor.AddApprovalRule(ctx, "Write(问我/**)", "ask", a.workdir(), key.Conversation); err != nil {
		t.Fatal(err)
	}
	if err := editor.AddApprovalRule(ctx, "Write(问我/**)", "deny", a.workdir(), ""); err == nil {
		t.Fatal("拒绝带范围该报错")
	}
	rules, err := d.Reader.ListApprovalRules(ctx)
	if err != nil || len(rules) != 4 {
		t.Fatalf("规则页：%+v %v", rules, err)
	}
	var mine api.ApprovalRule
	for _, r := range rules {
		if r.Effect == "ask" && r.Project == a.workdir() && r.Session == key.Conversation {
			mine = r
		}
	}
	if mine.Key == "" {
		t.Fatalf("规则页没带范围：%+v", rules)
	}
	sub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "写一份", nil) }()
	var card api.LocalApproval
	for card.ID == "" {
		if snap := localRecv(t, sub.Updates()); len(snap.Value) > 0 {
			card = snap.Value[0]
		}
	}
	if len(card.Options) != 0 {
		t.Fatalf("ask 规则的卡只能批这一次：%v", card.Options)
	}
	if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveAlways); err == nil {
		t.Fatal("伪造“记住”该被拒")
	}
	if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	if _, err := os.Stat(filepath.Join(a.workdir(), "问我", "a.txt")); err != nil {
		t.Fatalf("批准一次以后该写成：%v", err)
	}
	if err := d.Reader.RevokeApprovalRule(ctx, mine); err != nil {
		t.Fatal(err)
	}
	if rules, _ = d.Reader.ListApprovalRules(ctx); len(rules) != 3 {
		t.Fatalf("撤销后：%+v", rules)
	}
}

// B7：打开新目录、发现 AGENTS.md 时问一次，答"记住"以后不再问、规则页能撤、撤了再问；拒绝了这次运行里不再问。
func TestDesktopProjectTrustAskOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好的")})
	if err := os.WriteFile(filepath.Join(a.workdir(), "AGENTS.md"), []byte("编的项目规矩：先说再做"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := localKey("console:" + testChar)
	sub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bodies := func() string {
		e.mu.Lock()
		defer e.mu.Unlock()
		return strings.Join(e.bodies, "\n")
	}
	send := func(text string) chan error {
		done := make(chan error, 1)
		go func() { done <- d.Reader.SendLocal(ctx, key, text, nil) }()
		return done
	}
	card := func() api.LocalApproval {
		for {
			if snap := localRecv(t, sub.Updates()); len(snap.Value) > 0 {
				return snap.Value[0]
			}
		}
	}
	// 第一次：发现 AGENTS.md，问一次；没答之前不读
	done := send("第一句")
	c := card()
	if c.Tool != "project_trust" || !strings.Contains(c.Subject, filepath.Base(a.workdir())) || len(c.Options) != 1 || c.Options[0] != api.ApproveAlways {
		t.Fatalf("信任卡：%+v", c)
	}
	if strings.Contains(bodies(), "编的项目规矩") {
		t.Fatal("没答之前就读了说明文件")
	}
	if err := d.Reader.DecideLocal(ctx, key, c.ID, api.ApproveAlways); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	if !strings.Contains(bodies(), "编的项目规矩") {
		t.Fatal("答了记住以后这一轮就该带上说明文件")
	}
	rules, err := d.Reader.ListApprovalRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].Tool != "project_trust" || !strings.Contains(rules[0].Label, "项目说明") || rules[0].Scope != string(api.ApproveAlways) {
		t.Fatalf("规则页：%+v %v", rules, err)
	}
	// 第二次：不再问
	localRecv(t, send("第二句"))
	// 撤销：下一次又要问；这次拒绝，之后这次运行里不再问
	if err := d.Reader.RevokeApprovalRule(ctx, rules[0]); err != nil {
		t.Fatal(err)
	}
	done = send("第三句")
	c = card()
	if c.Tool != "project_trust" {
		t.Fatalf("撤销后该再问：%+v", c)
	}
	if err := d.Reader.DecideLocal(ctx, key, c.ID, api.ApproveDeny); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	localRecv(t, send("第四句")) // 拒绝过：这次运行里不再问，整轮直接结束
	if rules, _ = d.Reader.ListApprovalRules(ctx); len(rules) != 0 {
		t.Fatalf("拒绝不该留下规则：%+v", rules)
	}
}

// 项目说明信任答"允许一次"：这次运行里算数（读得到、不再问），但不留规则（规则页没有、重启就没了）。
func TestDesktopProjectTrustOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好的")})
	if err := os.WriteFile(filepath.Join(a.workdir(), "AGENTS.md"), []byte("编的一次性规矩：只这次运行"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := localKey("console:" + testChar)
	sub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, "第一句", nil) }()
	var c api.LocalApproval
	for c.ID == "" {
		if snap := localRecv(t, sub.Updates()); len(snap.Value) > 0 {
			c = snap.Value[0]
		}
	}
	if c.Tool != "project_trust" {
		t.Fatalf("信任卡：%+v", c)
	}
	if err := d.Reader.DecideLocal(ctx, key, c.ID, api.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	e.mu.Lock()
	got := strings.Join(e.bodies, "\n")
	e.mu.Unlock()
	if !strings.Contains(got, "编的一次性规矩") {
		t.Fatal("答了允许一次，这一轮就该带上说明文件")
	}
	if rules, _ := d.Reader.ListApprovalRules(ctx); len(rules) != 0 {
		t.Fatalf("允许一次不该留下规则：%+v", rules)
	}
	second := make(chan error, 1)
	go func() { second <- d.Reader.SendLocal(ctx, key, "第二句", nil) }()
	localRecv(t, second) // 不再问
}
