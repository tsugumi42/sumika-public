package approval

// 批准之后目标变了（规格-审批分级第七节 D）：批准写文件后文件被改了、批准后换了联接、规则被撤销，都不静默执行。
// 全部用临时目录，不碰真实数据。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/testutil/junction"
)

func ownerIdentity(conv string) kernel.Invocation {
	return kernel.Invocation{Scope: kernel.Scope{CharacterID: "suzu", Conversation: conv, Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}, ToolID: "fixture-tool"}
}

func writeCall(path string) Call {
	args, _ := jsjson.Parse(`{"path":` + jsjson.Quote(path) + `,"content":"新内容"}`)
	return Call{Identity: ownerIdentity("console:suzu"), ID: "fixture-call", Name: "write", Args: args}
}

func staleGate(t *testing.T) (*Gate, string) {
	t.Helper()
	dir, wd := t.TempDir(), t.TempDir()
	g := NewGateWith(filepath.Join(dir, "data"), nil)
	g.Mode = "manual"
	return g, wd
}

var ownerAud = policy.Audience{Channel: "console", PrivateOk: true}

func TestStaleFileChangedAfterApprovalAsksAgain(t *testing.T) {
	g, wd := staleGate(t)
	file := filepath.Join(wd, "a.txt")
	if err := os.WriteFile(file, []byte("原来的"), 0o644); err != nil {
		t.Fatal(err)
	}
	var retries []int
	var firstNote string
	g.Ask = func(_ context.Context, r Request, _ *Pending) Decision {
		retries = append(retries, r.Retry)
		if r.Retry == 1 && len(r.Notes) > 0 {
			firstNote = r.Notes[0]
		}
		if r.Retry == 0 {
			os.WriteFile(file, []byte("批准时被别人改了"), 0o644) // 第一次批准期间文件变了
		}
		return Decision{Kind: "approved"}
	}
	cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "console:suzu", writeCall(file))
	if cl.Blocked || len(retries) != 2 || retries[1] != 1 {
		t.Fatalf("变了要再问一次再放行：%+v retries=%v", cl, retries)
	}
	if !strings.HasPrefix(firstNote, "批准之后目标变了") {
		t.Fatalf("重问的卡要说明为什么：%q", firstNote)
	}
	if cl.By != policy.ByOnce || cl.Recheck == nil || cl.Recheck() != "" {
		t.Fatalf("新批准应当作数：%+v", cl)
	}
	// 放行之后、真正执行之前又变了：复查作废
	os.WriteFile(file, []byte("执行前又变了"), 0o644)
	if cl.Recheck() != whyStale {
		t.Fatal("执行前复查要发现文件变了")
	}
}

func TestStaleKeepsChangingGivesUp(t *testing.T) {
	g, wd := staleGate(t)
	file := filepath.Join(wd, "log.txt")
	os.WriteFile(file, []byte("0"), 0o644)
	asked := 0
	g.Ask = func(context.Context, Request, *Pending) Decision {
		asked++
		os.WriteFile(file, []byte(strings.Repeat("x", asked)), 0o644) // 日志文件：永远在变
		return Decision{Kind: "approved"}
	}
	cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "console:suzu", writeCall(file))
	if !cl.Blocked || cl.Reason != whyStaleGaveUp || asked != staleAskLimit {
		t.Fatalf("一直在变要放弃并说明：%+v asked=%d", cl, asked)
	}
}

func TestStaleNewFileAppearsAfterApproval(t *testing.T) {
	g, wd := staleGate(t)
	file := filepath.Join(wd, "new.txt") // 批准时还不存在
	n := 0
	g.Ask = func(_ context.Context, r Request, _ *Pending) Decision {
		n++
		if r.Retry == 0 {
			os.WriteFile(file, []byte("别人先建了"), 0o644)
		}
		return Decision{Kind: "approved"}
	}
	cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "console:suzu", writeCall(file))
	if cl.Blocked || n != 2 {
		t.Fatalf("从无到有也算变了：%+v n=%d", cl, n)
	}
}

func TestStaleJunctionRetargetedAfterApproval(t *testing.T) {
	g, wd := staleGate(t)
	a, b := t.TempDir(), t.TempDir()
	link := filepath.Join(wd, "link")
	if err := junction.Create(link, a); err != nil {
		t.Skipf("建不了联接：%v", err)
	}
	n := 0
	g.Ask = func(_ context.Context, r Request, _ *Pending) Decision {
		n++
		if r.Retry == 0 { // 批准时联接指向 a，批完换成 b：批的是 a 里的文件，写到 b 去了
			os.Remove(link)
			if err := junction.Create(link, b); err != nil {
				t.Error(err)
			}
		}
		return Decision{Kind: "approved"}
	}
	cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "console:suzu", writeCall(filepath.Join(link, "f.txt")))
	if n < 2 {
		t.Fatalf("联接换了目标必须再问：%+v n=%d", cl, n)
	}
}

// 没被改的正常批准一次就过，不多问。
func TestStaleUnchangedApprovesOnce(t *testing.T) {
	g, wd := staleGate(t)
	file := filepath.Join(wd, "a.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	n := 0
	g.Ask = func(context.Context, Request, *Pending) Decision { n++; return Decision{Kind: "approved"} }
	cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "console:suzu", writeCall(file))
	if cl.Blocked || n != 1 || cl.Note() == "" || !cl.Traced {
		t.Fatalf("%+v n=%d", cl, n)
	}
}

// 给模型的状态：拒绝不要换个方式重试、过期要先说一声、取消没执行。
func TestDecisionWordsForModel(t *testing.T) {
	g, wd := staleGate(t)
	file := filepath.Join(wd, "a.txt")
	for _, c := range []struct {
		d    Decision
		want []string
	}{
		{Decision{Kind: "denied", Reason: "不要动这个"}, []string{"所有者拒绝", "不要动这个", "不要换个方式重试"}},
		{Decision{Kind: "denied"}, []string{"所有者拒绝", "不要换个方式重试"}},
		{Decision{Kind: "expired"}, []string{"过期", "没有执行"}},
		{Decision{Kind: "cancelled"}, []string{"取消", "没有执行"}},
	} {
		g.Ask = func(context.Context, Request, *Pending) Decision { return c.d }
		cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "console:suzu", writeCall(file))
		if !cl.Blocked {
			t.Fatalf("%s 不该放行", c.d.Kind)
		}
		for _, w := range c.want {
			if !strings.Contains(cl.Reason, w) {
				t.Errorf("%s：模型看到的话里缺 %q：%s", c.d.Kind, w, cl.Reason)
			}
		}
	}
}

// 撤销立刻生效：已放行、还没开始执行的那一步，执行前复查作废；之后同一类重新要问。
func TestRevokedRuleStopsPendingStep(t *testing.T) {
	g, wd := staleGate(t)
	file := filepath.Join(wd, "a.txt")
	conv := "console:suzu"
	kind := policy.KindOf("write", writeCall(file).Args, wd, nil)
	asked := 0
	g.Ask = func(_ context.Context, _ Request, p *Pending) Decision {
		asked++
		if p == nil || p.Key != kind.Key {
			t.Fatalf("要给出记住的范围：%+v", p)
		}
		return Decision{Kind: "approved", By: policy.ByAlways, Rule: p.Key, Label: p.Label}
	}
	first := g.Clear(t.Context(), wd, "suzu", ownerAud, conv, writeCall(file))
	if first.Blocked || first.By != policy.ByAlways {
		t.Fatalf("%+v", first)
	}
	// 模拟审批方点"记住"之后写入规则
	if err := g.Grants.Put(Rule{Key: kind.Key, Label: kind.Label, Tool: "write", At: 1}); err != nil {
		t.Fatal(err)
	}
	second := g.Clear(t.Context(), wd, "suzu", ownerAud, conv, writeCall(file))
	if second.Blocked || asked != 1 || second.By != policy.ByAlways || second.Recheck == nil || second.Recheck() != "" {
		t.Fatalf("记住之后不再问：%+v asked=%d", second, asked)
	}
	if _, err := g.Grants.Remove(kind.Key); err != nil {
		t.Fatal(err)
	}
	if second.Recheck() != whyRevoked {
		t.Fatal("撤销后复查要作废")
	}
	g.Ask = func(context.Context, Request, *Pending) Decision { asked++; return Decision{Kind: "denied"} }
	if third := g.Clear(t.Context(), wd, "suzu", ownerAud, conv, writeCall(file)); !third.Blocked || asked != 2 {
		t.Fatalf("撤销后要重新问：%+v asked=%d", third, asked)
	}
}

// task 的"记住"只管派这一类子任务，不给子任务自己的写、命令放行：子任务权限不会因此超过派它的人。
func TestTaskGrantDoesNotWidenChild(t *testing.T) {
	g, wd := staleGate(t)
	if err := g.Grants.Put(Rule{Key: "task:worker", Label: "x", Tool: "task", At: 1}); err != nil {
		t.Fatal(err)
	}
	asked := 0
	g.Ask = func(context.Context, Request, *Pending) Decision { asked++; return Decision{Kind: "denied"} }
	file := filepath.Join(wd, "a.txt")
	child := writeCall(file)
	child.Identity = ownerIdentity("internal:child-1")
	if cl := g.Clear(t.Context(), wd, "suzu", ownerAud, "internal:child-1", child); !cl.Blocked || asked != 1 {
		t.Fatalf("子任务自己写文件仍要过门：%+v", cl)
	}
}

// task 的同一类：代理名不合法、类型不对的不记，只能批这一次。
func TestTaskKindRejectsOddAgentNames(t *testing.T) {
	for _, agent := range []string{"", "a b", "Worker", "../x", strings.Repeat("a", 40), "worker\n"} {
		args, _ := jsjson.Parse(`{"agent":` + jsjson.Quote(agent) + `}`)
		if k := policy.KindOf("task", args, "", nil); k != nil {
			t.Errorf("agent %q 不该有同一类：%+v", agent, k)
		}
	}
	for _, args := range []string{`{}`, `{"agent":1}`, `{"agent":null}`, `{"agent":["worker"]}`} {
		v, _ := jsjson.Parse(args)
		if k := policy.KindOf("task", v, "", nil); k != nil {
			t.Errorf("%s 不该有同一类：%+v", args, k)
		}
	}
	ok, _ := jsjson.Parse(`{"agent":"worker"}`)
	if k := policy.KindOf("task", ok, "", nil); k == nil || k.Key != "task:worker" {
		t.Fatalf("%+v", k)
	}
}

// 场合放开也不吃自动放行：即使听众判定放行了（PrivateOk 为真），本地群聊、角色接角色这种"不是主人发起的"，
// 默认放行的查网、授权过的 task、YOLO 都不生效，只问一次、不给"这个对话""记住"（ADR-269，深度防御）。
func TestClearNotOwnerNeverAutoAllowed(t *testing.T) {
	scopes := map[string]kernel.Scope{
		"本地群聊主人":  {Channel: "console", ActorKind: "owner", Scene: "group", Conversation: "console:group:g", Platform: "local"},
		"本地群聊空身份": {Channel: "console", Scene: "group", Conversation: "console:group:g", Platform: "local"},
		"角色接角色":   {Channel: "console", ActorKind: "character", Scene: "private", Conversation: "console:suzu", Platform: "local"},
	}
	for name, sc := range scopes {
		for _, tool := range []string{"web_search", "web_fetch", "task"} {
			for _, mode := range []string{"manual", "auto", "yolo"} {
				t.Run(name+"/"+tool+"/"+mode, func(t *testing.T) {
					g, wd := staleGate(t)
					g.Mode = mode
					g.Grants.Put(Rule{Key: "task:reader", Label: "x", Tool: "task", At: 1})
					g.Conv.Merge(sc.Conversation, Rule{Key: "task:reader", Tool: "task", At: 1})
					g.Conv.Merge(sc.Conversation, Rule{Key: "tool:" + tool, Tool: tool, At: 1})
					asked, offered := 0, 0
					g.Ask = func(_ context.Context, r Request, _ *Pending) Decision {
						asked++
						offered += len(r.Options)
						return Decision{Kind: "denied"}
					}
					args, _ := jsjson.Parse(`{"agent":"reader","url":"https://example.invalid","query":"x"}`)
					in := kernel.Invocation{Scope: sc, ToolID: "t"}
					cl := g.Clear(kernel.WithInvocation(t.Context(), in), wd, "suzu", ownerAud, sc.Conversation, Call{Identity: in, ID: "t", Name: tool, Args: args})
					if !cl.Blocked || asked != 1 || offered != 0 {
						t.Fatalf("不是主人发起的要问一次、不给授权档：%+v asked=%d offered=%d", cl, asked, offered)
					}
				})
			}
		}
	}
}
