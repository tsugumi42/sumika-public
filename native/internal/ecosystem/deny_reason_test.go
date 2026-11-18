package ecosystem

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/kernel"
)

// 拒绝原因只进日志：对外同一句话、errors.Is 照旧，DenyReason 能分清是哪种。
func TestDenyReasonTellsWhich(t *testing.T) {
	a := NewAuthority()
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	s := kernel.Scope{CharacterID: "fixture", Conversation: "fixture", ScopeID: "fixture", Platform: "local", Channel: "console", AccountID: "80001", Scene: "private", ActorKind: "owner", ActorID: "80002", AuthorityID: "fixture-grant", AuthorityRevision: 1}
	access, err := a.Issue(s, []string{"read"}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	check := func(name string, err error, want string) {
		t.Helper()
		if !errors.Is(err, ErrDenied) || err.Error() != ErrDenied.Error() {
			t.Fatalf("%s：对外该是同一个拒绝：%v", name, err)
		}
		if got := DenyReason(err); got != want {
			t.Fatalf("%s：原因 %q，应为 %q", name, got, want)
		}
	}
	missing := s
	missing.AuthorityID = "不存在"
	_, err = a.Resolve(missing)
	check("不存在", err, "授权不存在")
	stale := s
	stale.AuthorityRevision = 2
	_, err = a.Resolve(stale)
	check("修订号", err, "范围对不上")
	check("没能力", access.Check(context.Background(), "write"), "没有这项能力：write")
	now = now.Add(2 * time.Hour)
	_, err = a.Resolve(s)
	check("过期", err, "授权已过期")
	if DenyReason(errors.New("别的错")) != "" {
		t.Fatal("别的错不该有原因")
	}
}
