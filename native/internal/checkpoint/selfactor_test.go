package checkpoint

import (
	"testing"

	"sumika-native/internal/kernel"
)

// 本地主人的身份：发言人不能是接收账号自己，裸号、带前缀、大小写和空白变体都不行（kernel.Scope.ActorIsAccount 归一）。
func TestOwnerScopeRejectsActorEqualToAccount(t *testing.T) {
	t.Parallel()
	base := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "grant", AuthorityRevision: 1}
	if !ownerScope(base) {
		t.Fatal("正常的本地主人身份被拒")
	}
	for _, actor := range []string{"desktop", "console:desktop", "CONSOLE:Desktop", " desktop ", "local:desktop", "console:console:desktop"} {
		s := base
		s.ActorID = actor
		if ownerScope(s) {
			t.Errorf("发言人 %q 就是账号，却被认成主人", actor)
		}
	}
}

// 账号和发言人是同一串空白：归一后账号是空，但两边原样相同，旧代码（裸比较）拒，不能因为换成归一比较就放过。
func TestOwnerScopeRejectsIdenticalBlankActorAndAccount(t *testing.T) {
	t.Parallel()
	s := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: " ", Scene: "private", ActorKind: "owner", ActorID: " ", AuthorityID: "grant", AuthorityRevision: 1}
	if ownerScope(s) {
		t.Fatal("账号和发言人是同一串空白，却被认成主人")
	}
}
