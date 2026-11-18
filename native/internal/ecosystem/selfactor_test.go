package ecosystem

import (
	"testing"

	"sumika-native/internal/kernel"
)

// 授权只给本地主人：发言人不能是接收账号自己，写法变体也不行（kernel.Scope.ActorIsAccount 归一）。
func TestValidScopeRejectsActorEqualToAccount(t *testing.T) {
	t.Parallel()
	base := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "80001", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "grant", AuthorityRevision: 1}
	if !validScope(base) {
		t.Fatal("正常的本地主人身份被拒")
	}
	for _, actor := range []string{"80001", "console:80001", "CONSOLE:80001", " 80001 ", "local:80001", "８０００１", "80001\u200b"} {
		s := base
		s.ActorID = actor
		if validScope(s) {
			t.Errorf("发言人 %q 就是账号，却被收下", actor)
		}
	}
}

// 账号和发言人是同一串空白：归一后账号是空，但两边原样相同，旧代码（裸比较）拒，不能因为换成归一比较就放过。
func TestValidScopeRejectsIdenticalBlankActorAndAccount(t *testing.T) {
	t.Parallel()
	s := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: " ", Scene: "private", ActorKind: "owner", ActorID: " ", AuthorityID: "grant", AuthorityRevision: 1}
	if validScope(s) {
		t.Fatal("账号和发言人是同一串空白，却被收下")
	}
}
