package ecosystem

import (
	"context"
	"sumika-native/internal/kernel"
	"testing"
	"time"
)

func TestEcosystemFourSceneAuthority(t *testing.T) {
	base := kernel.Scope{CharacterID: "fixture", Conversation: "fixture", ScopeID: "fixture", Platform: "local", Channel: "console", AccountID: "80001", Scene: "private", ActorKind: "owner", ActorID: "80002", AuthorityID: "fixture-grant", AuthorityRevision: 1}
	shapes := []struct {
		name   string
		change func(*kernel.Scope)
	}{
		{"empty-character", func(s *kernel.Scope) { s.CharacterID = "" }}, {"empty-actor", func(s *kernel.Scope) { s.ActorID = "" }}, {"zero-actor", func(s *kernel.Scope) { s.ActorID = "0" }}, {"self", func(s *kernel.Scope) { s.ActorID = s.AccountID }}, {"pretend-owner-name", func(s *kernel.Scope) { s.ActorKind = "user"; s.ActorID = "主人" }}, {"zero-width-name", func(s *kernel.Scope) { s.ActorKind = "user"; s.ActorID = "主\u200b人" }}, {"no-grant", func(s *kernel.Scope) { s.AuthorityID = "" }}, {"stale-revision", func(s *kernel.Scope) { s.AuthorityRevision = 0 }},
	}
	scenes := []struct {
		name, platform, channel, scene string
		allowed                        bool
	}{{"local-private", "local", "console", "private", true}, {"local-group", "local", "console", "group", false}, {"qq-private", "qq", "qq", "private", false}, {"qq-group", "qq", "qq", "group", false}}
	for _, scene := range scenes {
		t.Run(scene.name, func(t *testing.T) {
			s := base
			s.Platform = scene.platform
			s.Channel = scene.channel
			s.Scene = scene.scene
			a := NewAuthority()
			access, err := a.Issue(s, []string{"skill:builtin", "mcp:fixture", "web:search", "task:research"}, time.Now().Add(time.Minute))
			if (err == nil) != scene.allowed {
				t.Fatal("四场合默认授权错误")
			}
			if scene.allowed {
				for _, cap := range []string{"skill:builtin", "mcp:fixture", "web:search", "task:research"} {
					if err := access.Check(context.Background(), cap); err != nil {
						t.Fatal(err)
					}
				}
				a.Revoke(s.AuthorityID)
				if access.Check(context.Background(), "skill:builtin") == nil {
					t.Fatal("撤权无效")
				}
			}
			for _, shape := range shapes {
				bad := s
				shape.change(&bad)
				if _, err := a.Issue(bad, []string{"local.exec"}, time.Now().Add(time.Minute)); err == nil {
					t.Fatalf("敌对身份获准 %s", shape.name)
				}
			}
		})
	}
	if (Access{}).Check(context.Background(), "skill:builtin") == nil {
		t.Fatal("零句柄获准")
	}
}
