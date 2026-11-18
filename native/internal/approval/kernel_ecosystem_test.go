package approval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

// frozenTarget 是测试用的冻结目标：valid 可以在批准期间被改掉，模拟服务重连、schema 变了。
type frozenTarget struct {
	valid    bool
	binding  string
	digest   string
	readonly bool
}

func (f *frozenTarget) Valid() bool { return f.valid }
func (f *frozenTarget) Target() (string, string, string, string) {
	d := f.digest
	if d == "" {
		d = "fixture-schema"
	}
	return "fixture-server", "原始工具", "mcp__fixture__search", d
}
func (*frozenTarget) Arguments() json.RawMessage { return json.RawMessage(`{"query":"fixture"}`) }
func (f *frozenTarget) Binding() string {
	if f.binding == "" {
		return "bind-1"
	}
	return f.binding
}
func (f *frozenTarget) Fingerprint() string { return f.Binding() }
func (f *frozenTarget) ReadOnlyClaim() bool { return f.readonly }

func mcpGate(t *testing.T, mode string) *Gate {
	return &Gate{Mode: mode, Now: time.Now, ApprovalMinutes: 1, Grants: LoadGrants(filepath.Join(t.TempDir(), "config", "tool-grants.yml")), Conv: NewConvGrants()}
}

func ownerCtx(t *testing.T, scene string) context.Context {
	t.Helper()
	conv := "console:fixture-character"
	if scene == "group" {
		conv = "console:group:fixture"
	}
	return kernel.WithInvocation(t.Context(), kernel.Invocation{Scope: kernel.Scope{Channel: "console", ActorKind: "owner", Scene: scene, Conversation: conv}})
}

// 批准之后、执行之前目标变了：不静默执行，告诉模型重新搜索。
func TestMCPApprovalFrozenTarget(t *testing.T) {
	target := &frozenTarget{valid: true}
	asked := 0
	g := mcpGate(t, "manual")
	g.Ask = func(ctx context.Context, r Request, _ *Pending) Decision {
		asked++
		if !strings.Contains(r.Subject, "fixture-server") || !strings.Contains(r.Subject, "原始工具") || r.Preview == nil || !strings.Contains(r.Preview.Text, "fixture-schema") || !strings.Contains(r.Preview.Text, `"query":"fixture"`) {
			t.Fatal("审批没有展示冻结的目标和参数")
		}
		target.valid = false
		return Decision{Kind: "approved"}
	}
	cl := g.CheckMCP(ownerCtx(t, "private"), "fixture-character", "console:fixture-character", "fixture-tool", target)
	if !cl.Blocked || cl.Reason != whyMCPStale {
		t.Fatalf("审批后目标变化仍批准：%+v", cl)
	}
	if asked != 1 {
		t.Fatal("没有逐次审批")
	}
}

// 卡片上给"这个对话""记住"；点了之后同一服务器加工具名不再问，别的工具照问。
func TestMCPApprovalScopes(t *testing.T) {
	for _, mode := range []string{"manual", "auto", "yolo"} {
		for _, by := range []string{policy.ByOnce, policy.ByConversation, policy.ByAlways} {
			t.Run(mode+"/"+by, func(t *testing.T) {
				g := mcpGate(t, mode)
				asked := 0
				var offered []string
				g.Ask = func(_ context.Context, r Request, p *Pending) Decision {
					asked++
					offered = r.Options
					if by != policy.ByOnce && p != nil {
						return Decision{Kind: "approved", By: by, Rule: p.Key, Label: p.Label}
					}
					return Decision{Kind: "approved", By: by}
				}
				ctx := ownerCtx(t, "private")
				conv := "console:fixture-character"
				first := g.CheckMCP(ctx, "c", conv, "t1", &frozenTarget{valid: true})
				if mode == "yolo" {
					// YOLO 和其他工具一致：不问，记作模式放行
					if first.Blocked || asked != 0 || first.By != policy.ByMode {
						t.Fatalf("YOLO 不该问：%+v asked=%d", first, asked)
					}
					return
				}
				if first.Blocked || asked != 1 || len(offered) != 2 {
					t.Fatalf("第一次要问、给两档：%+v asked=%d offered=%v", first, asked, offered)
				}
				if first.By != by {
					t.Fatalf("放行档位记错：%q", first.By)
				}
				if by != policy.ByOnce {
					// 记下规则（模拟审批方按 Pending 写入）
					r := Rule{Key: "mcp:fixture-server/原始工具", Label: "x", Tool: "mcp_call", Guard: "bind-1:fixture-schema", At: 1}
					if by == policy.ByConversation {
						g.Conv.Merge(conv, r)
					} else if err := g.Grants.Put(r); err != nil {
						t.Fatal(err)
					}
				}
				second := g.CheckMCP(ctx, "c", conv, "t2", &frozenTarget{valid: true})
				if by == policy.ByOnce {
					if asked != 2 {
						t.Fatal("只批一次的第二次要再问")
					}
					return
				}
				if second.Blocked || asked != 1 || second.By != by || second.Recheck == nil {
					t.Fatalf("记住后不该再问：%+v asked=%d", second, asked)
				}
				// 规则被撤销：执行前复查作废
				if by == policy.ByConversation {
					g.Conv.Remove(conv, "mcp:fixture-server/原始工具")
				} else {
					g.Grants.Remove("mcp:fixture-server/原始工具")
				}
				if second.Recheck() != whyRevoked {
					t.Fatal("撤销要立刻生效")
				}
				// schema 变了：规则不算，再问一次并提示
				if by == policy.ByConversation {
					g.Conv.Merge(conv, Rule{Key: "mcp:fixture-server/原始工具", Tool: "mcp_call", Guard: "bind-1:fixture-schema", At: 1})
				} else {
					g.Grants.Put(Rule{Key: "mcp:fixture-server/原始工具", Tool: "mcp_call", Guard: "bind-1:fixture-schema", At: 1})
				}
				third := g.CheckMCP(ctx, "c", conv, "t3", &frozenTarget{valid: true, digest: "changed-schema"})
				if asked != 2 || third.Blocked {
					t.Fatalf("schema 变了要再问：asked=%d %+v", asked, third)
				}
				fourth := g.CheckMCP(ctx, "c", conv, "t4", &frozenTarget{valid: true, binding: "bind-2"})
				if asked != 3 || fourth.Blocked {
					t.Fatalf("服务绑定变了要再问：asked=%d", asked)
				}
			})
		}
	}
}

// 形状 × 入口：不是主人、本地群聊、QQ 群、QQ 私聊的非主人，都不吃授权和模式放行（ADR-269）。
func TestMCPApprovalNotOwnerNeverAutoAllowed(t *testing.T) {
	cases := []struct {
		name  string
		scope kernel.Scope
		conv  string
	}{
		{"本地群聊主人", kernel.Scope{Channel: "console", ActorKind: "owner", Scene: "group", Conversation: "console:group:g"}, "console:group:g"},
		{"本地群聊空身份", kernel.Scope{Channel: "console", Scene: "group", Conversation: "console:group:g"}, "console:group:g"},
		{"角色接角色", kernel.Scope{Channel: "console", ActorKind: "character", Scene: "private", Conversation: "console:c"}, "console:c"},
		{"QQ 群", kernel.Scope{Channel: "qq", ActorKind: "member", Scene: "group", Conversation: "qq:group:1"}, "qq:group:1"},
		{"QQ 私聊非主人", kernel.Scope{Channel: "qq", ActorKind: "member", Scene: "private", Conversation: "qq:private:1"}, "qq:private:1"},
	}
	for _, c := range cases {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				g := mcpGate(t, mode)
				// 预置两种授权：也不能放行
				g.Conv.Merge(c.conv, Rule{Key: "mcp:fixture-server/原始工具", Tool: "mcp_call", Guard: "bind-1:fixture-schema", At: 1})
				g.Grants.Put(Rule{Key: "mcp:fixture-server/原始工具", Tool: "mcp_call", Guard: "bind-1:fixture-schema", At: 1})
				asked, offered := 0, 0
				g.Ask = func(_ context.Context, r Request, _ *Pending) Decision {
					asked++
					offered += len(r.Options)
					return Decision{Kind: "denied"}
				}
				ctx := kernel.WithInvocation(t.Context(), kernel.Invocation{Scope: c.scope})
				cl := g.CheckMCP(ctx, "c", c.conv, "t", &frozenTarget{valid: true})
				// 非 console 渠道在第一步被场合挡掉、不问；console 的要问，而且不给"这个对话""记住"
				if !cl.Blocked || offered != 0 || asked != 0 {
					t.Fatalf("不该放行、不该给授权档：%+v asked=%d offered=%d", cl, asked, offered)
				}
			})
		}
	}
}

// 服务器自称只读不放宽，只写在卡片上。
func TestMCPReadOnlyClaimDoesNotRelax(t *testing.T) {
	g := mcpGate(t, "auto")
	asked, noted := 0, false
	g.Ask = func(_ context.Context, r Request, _ *Pending) Decision {
		asked++
		for _, n := range r.Notes {
			noted = noted || strings.Contains(n, "自称")
		}
		return Decision{Kind: "denied"}
	}
	cl := g.CheckMCP(ownerCtx(t, "private"), "c", "console:fixture-character", "t", &frozenTarget{valid: true, readonly: true})
	if asked != 1 || !cl.Blocked || !noted {
		t.Fatalf("自称只读也要问并标注：asked=%d noted=%v", asked, noted)
	}
}

// 走真的审批方（Approvals）：卡片给的两档点下去，规则带着目标版本记下；同一目标不再问，
// schema 或服务绑定变了再问；之后撤销立刻要问。
func TestMCPApprovalsRoundTrip(t *testing.T) {
	for _, choice := range []string{"conversation", "always"} {
		t.Run(choice, func(t *testing.T) {
			g := mcpGate(t, "manual")
			a := NewApprovals(g)
			ctx := ownerCtx(t, "private")
			conv := "console:fixture-character"
			ask := func(target *frozenTarget, answer string) Clearance {
				out := make(chan Clearance, 1)
				go func() { out <- g.CheckMCP(ctx, "c", conv, "t", target) }()
				if answer == "" { // 不该问：直接等结果
					return <-out
				}
				for i := 0; i < 500; i++ {
					if p := a.Pending(); len(p) == 1 {
						if ok, err := a.Decide(p[0].ID, answer, ""); !ok || err != nil {
							t.Fatalf("答复失败：%v %v", ok, err)
						}
						return <-out
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("等不到审批卡")
				return Clearance{}
			}
			first := ask(&frozenTarget{valid: true}, choice)
			if first.Blocked || first.By != choice || first.Recheck == nil {
				t.Fatalf("%+v", first)
			}
			second := ask(&frozenTarget{valid: true}, "")
			if second.Blocked || second.By != choice {
				t.Fatalf("记住之后不该再问：%+v", second)
			}
			if rules := append(g.Grants.List(), g.Conv.List()...); len(rules) != 1 || rules[0].Guard != "bind-1:fixture-schema" || rules[0].Scope != choice {
				t.Fatalf("规则要带着目标版本记下：%+v", rules)
			}
			if third := ask(&frozenTarget{valid: true, digest: "changed"}, "deny"); !third.Blocked {
				t.Fatalf("schema 变了要再问：%+v", third)
			}
			if choice == "always" {
				if !LoadGrants(g.Grants.file).Has("mcp:fixture-server/原始工具") {
					t.Fatal("记住要写进文件")
				}
				g.Grants.Remove("mcp:fixture-server/原始工具")
			} else {
				g.Conv.Remove(conv, "mcp:fixture-server/原始工具")
			}
			if second.Recheck() != whyRevoked {
				t.Fatal("撤销要立刻让已放行的作废")
			}
			if fourth := ask(&frozenTarget{valid: true}, "deny"); !fourth.Blocked {
				t.Fatalf("撤销后要问：%+v", fourth)
			}
		})
	}
}
