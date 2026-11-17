package app

// 本地私聊每个会话选的审批档、“这个对话”的放行，重启后还在（规格-审批分级 8.2）；撤销后重启不回来。
// 数据目录只用 t.TempDir()。

import (
	"context"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/llm/llmtest"
)

func TestDesktopApprovalSessionSurvivesRestart(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctx := context.Background()
	conv := "console:" + testChar
	key := localKey(conv)
	if err := d.Reader.SetConversationApprovalMode(ctx, key, "yolo"); err != nil {
		t.Fatal(err)
	}
	a.approvalGate.Conv.Merge(conv, approval.Rule{Key: "write:" + a.workdir(), Label: "写文件", Tool: "write", Workdir: a.workdir(), At: a.now().UnixMilli()})
	d.Close()
	a.Close()

	a2 := e.open() // 同一个数据目录重新装配 = 重启
	d2 := a2.Desktop()
	t.Cleanup(func() { d2.Close(); a2.Close() })
	mode, err := d2.Reader.ConversationApprovalMode(ctx, key)
	if err != nil || mode.Mode != "yolo" || !mode.Override || mode.Default != "manual" {
		t.Fatalf("重启后这个会话该仍是 yolo：%+v %v", mode, err)
	}
	if m := a2.approvalGate.ModeFor(conv + ":topic:别的"); m != "manual" {
		t.Fatalf("别的话题不受影响：%s", m)
	}
	rules, err := d2.Reader.ListApprovalRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].Tool != "write" || rules[0].Conversation != conv {
		t.Fatalf("规则页该仍有“这个对话”的放行：%+v %v", rules, err)
	}
	if _, ok := a2.approvalGate.Conv.Match(conv, a2.workdir(), rules[0].Key, ""); !ok {
		t.Fatal("重启后同目录里该仍然放行")
	}
	// 回到默认、撤销规则，再重启：都不回来
	if err := d2.Reader.SetConversationApprovalMode(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	if err := d2.Reader.RevokeApprovalRule(ctx, rules[0]); err != nil {
		t.Fatal(err)
	}
	d2.Close()
	a2.Close()
	a3 := e.open()
	d3 := a3.Desktop()
	t.Cleanup(func() { d3.Close(); a3.Close() })
	if mode, _ = d3.Reader.ConversationApprovalMode(ctx, key); mode.Mode != "manual" || mode.Override {
		t.Fatalf("回到默认后重启不该回来：%+v", mode)
	}
	if rules, _ = d3.Reader.ListApprovalRules(ctx); len(rules) != 0 {
		t.Fatalf("撤销后重启不该回来：%+v", rules)
	}
}
