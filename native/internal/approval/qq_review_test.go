package approval

// 主人 QQ 私聊审批的复核补洞（规格-主人QQ私聊审批）：写库失败不丢来源、批准后复查、没声明工具的参数不静默截断。
// 路径、名字都是编的，只用 t.TempDir()。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

// 结局写不进库时按拒绝处理，但订阅方（QQ）必须仍看到回答来自 QQ，才能说“没生效”而不是“已批准”。
func TestPersistFailureKeepsVia(t *testing.T) {
	a := newApprovals(t)
	a.Persist = func(_ Request, d *Decision) error {
		if d != nil {
			return errors.New("磁盘满")
		}
		return nil
	}
	resolved := make(chan Decision, 1)
	a.Subscribe(func(Request) {}, func(_ Request, d Decision) { resolved <- d })
	got := make(chan Decision, 1)
	go func() { got <- a.Ask(context.Background(), card("x"), nil) }()
	waitPending(t, a, "x")
	if ok, err := a.DecideVia("x", "once", "qq"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if d := approvalRecv(t, got, "结局"); d.Kind != "denied" {
		t.Fatalf("写库失败要按拒绝处理：%+v", d)
	}
	d := approvalRecv(t, resolved, "订阅方的结局")
	if d.Kind != "denied" || d.Via != "qq" || !strings.Contains(d.Reason, "磁盘满") {
		t.Fatalf("订阅方要看到被改成拒绝的原因和来源 qq：%+v", d)
	}
}

func qqGate(t *testing.T, ask func(Request) Decision) (*Gate, string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	g := NewGateWith(dir, policy.NewAnalyzer())
	g.Mode = "manual"
	g.Now = func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }
	g.Ask = func(_ context.Context, req Request, _ *Pending) Decision { return ask(req) }
	return g, wd
}

func qqCheck(t *testing.T, g *Gate, wd, tool, argsJSON string) (bool, string) {
	t.Helper()
	args, err := jsjson.Parse(argsJSON)
	if err != nil {
		t.Fatal(err)
	}
	id := kernel.Invocation{Scope: kernel.Scope{CharacterID: "suzu", Conversation: "console:suzu", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "fixture-desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "fixture-authority", AuthorityRevision: 1}, InputID: "in", RootID: "root", RunID: "run", StepID: "step", ToolID: "tool", Generation: 1, Kind: kernel.KindForeground}
	return g.Check(context.Background(), wd, "suzu", policy.Audience{Channel: "console", PrivateOk: true}, "console:suzu", Call{Identity: id, ID: "call_1", Name: tool, Args: args})
}

// 批准后复查：主人在 QQ 里看的是建卡时的 diff；批准到达时文件已经被改，不能按旧 diff 执行。
func TestQQApprovalRechecksFileBeforeRunning(t *testing.T) {
	for _, via := range []string{"qq", ""} {
		var file string
		g, wd := qqGate(t, func(req Request) Decision {
			// 主人看卡、回复的这段时间里，文件被别的东西改了
			if err := os.WriteFile(file, []byte("别处写进来的新内容\n"), 0o644); err != nil {
				t.Error(err)
			}
			return Decision{Kind: "approved", Via: via}
		})
		file = filepath.Join(wd, "编的文件.txt")
		if err := os.WriteFile(file, []byte("原内容\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		block, reason := qqCheck(t, g, wd, "write", `{"path":"编的文件.txt","content":"新内容\n"}`)
		if via == "qq" && (!block || !strings.Contains(reason, "没有执行")) {
			t.Fatalf("QQ 批准后文件变了必须拦下：block=%v %q", block, reason)
		}
		if via == "" && block {
			t.Fatalf("本机点的批准看的是当前卡，不在复查范围：%q", reason)
		}
	}
}

// 没变就放行：复查不能误伤正常的 QQ 批准。
func TestQQApprovalUnchangedStillRuns(t *testing.T) {
	g, wd := qqGate(t, func(Request) Decision { return Decision{Kind: "approved", Via: "qq"} })
	if err := os.WriteFile(filepath.Join(wd, "编的文件.txt"), []byte("原内容\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if block, reason := qqCheck(t, g, wd, "write", `{"path":"编的文件.txt","content":"新内容\n"}`); block {
		t.Fatalf("没变应放行：%q", reason)
	}
	if block, reason := qqCheck(t, g, wd, "write", `{"path":"新建的编的文件.txt","content":"x"}`); block {
		t.Fatalf("新建文件没变应放行：%q", reason)
	}
}

// 没声明的工具参数超过上限：卡上标明截短，不能让不在本机的入口只凭前半段批准。
func TestUndeclaredToolSubjectTruncatedFlag(t *testing.T) {
	g, _ := qqGate(t, func(Request) Decision { return Decision{Kind: "cancelled"} })
	long := strings.Repeat("无害说明", 1600) + "TAIL_DANGEROUS"
	args, err := jsjson.Parse(`{"payload":"` + long + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	r := g.Describe(t.TempDir(), "suzu", "c1", "some_mcp_tool", args)
	if !r.SubjectTruncated || strings.Contains(r.Subject, "TAIL_DANGEROUS") {
		t.Fatalf("要标明截短：%v", r.SubjectTruncated)
	}
	short, _ := jsjson.Parse(`{"payload":"短"}`)
	if r := g.Describe(t.TempDir(), "suzu", "c2", "some_mcp_tool", short); r.SubjectTruncated {
		t.Fatal("没超上限不能标截短")
	}
}
