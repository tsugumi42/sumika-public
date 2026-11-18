package ecosystem

import (
	"context"
	"sumika-native/internal/kernel"
	"testing"
	"time"
)

func TestChildDelegationIntersectionAndRevocation(t *testing.T) {
	auth := NewAuthority()
	scope := kernel.Scope{CharacterID: "fixture", Conversation: "parent", ScopeID: "owner", Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "20002", AccountID: "20001", AuthorityID: "parent-grant", AuthorityRevision: 1}
	parent, err := auth.Issue(scope, []string{"local.read", "local.write", "skill:public", "task:worker"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.Delegate(t.Context(), "internal:fixture", []string{"local.read"}, []string{"public"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := auth.Resolve(child.Scope())
	if err != nil {
		t.Fatal("宿主无法从内部身份找回委托", err)
	}
	if err = resolved.Check(t.Context(), "local.read"); err != nil {
		t.Fatal(err)
	}
	forged := child.Scope()
	forged.Conversation = "unrelated"
	if _, err = auth.Resolve(forged); err == nil {
		t.Fatal("宿主查找接受伪造域")
	}
	if err := child.Check(context.Background(), "local.read"); err != nil {
		t.Fatal(err)
	}
	if err := child.Check(context.Background(), "skill:public"); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"local.write", "task:worker", "skill:private"} {
		if child.Check(context.Background(), cap) == nil {
			t.Fatal("子能力扩大", cap)
		}
		if resolved.Check(context.Background(), cap) == nil {
			t.Fatal("找回句柄时扩大了委托", cap)
		}
	}
	if _, err := child.Delegate(t.Context(), "internal:nested", []string{"local.read"}, nil); err == nil {
		t.Fatal("子授权再次委派")
	}
	auth.Revoke(scope.AuthorityID)
	if child.Check(context.Background(), "local.read") == nil {
		t.Fatal("父撤权未传递")
	}
	if resolved.Check(t.Context(), "local.read") == nil {
		t.Fatal("找回句柄未继承父撤权")
	}
}
