package artifact

import (
	"strings"
	"testing"

	"sumika-native/internal/kernel"
)

// 文件产物只收宿主完整身份：发言人不能是接收账号自己，本地和 QQ 两个分支一样，写法变体也不行。
func TestValidScopeRejectsActorEqualToAccount(t *testing.T) {
	t.Parallel()
	local := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "grant", AuthorityRevision: 1}
	qq := kernel.Scope{CharacterID: "selfactor-char", Conversation: "qq:private:91001:92001", ScopeID: "qq", Platform: "qq", Channel: "qq", AccountID: "91001", Scene: "private", ActorKind: "human", ActorID: "qq:92001", AuthorityID: "grant", AuthorityRevision: 1}
	for name, base := range map[string]kernel.Scope{"本地": local, "QQ": qq} {
		if !ValidScope(base) {
			t.Fatalf("%s：正常身份被拒", name)
		}
		for _, actor := range []string{base.AccountID, base.Channel + ":" + base.AccountID, strings.ToUpper(base.Channel) + ":" + base.AccountID, base.Platform + ":" + base.AccountID, " " + base.AccountID + " ", base.AccountID + "\u200b"} {
			s := base
			s.ActorID = actor
			if ValidScope(s) {
				t.Errorf("%s：发言人 %q 就是账号，却被收下", name, actor)
			}
		}
	}
}

// 账号和发言人是同一串空白：ValidScope 的空值检查只拦 "" 和 "0"，旧代码的裸比较把它拒掉，不能因为换成归一比较就放过。
func TestValidScopeRejectsIdenticalBlankActorAndAccount(t *testing.T) {
	t.Parallel()
	s := kernel.Scope{CharacterID: "selfactor-char", Conversation: "qq:private:91001:92001", ScopeID: "qq", Platform: "qq", Channel: "qq", AccountID: " ", Scene: "private", ActorKind: "human", ActorID: " ", AuthorityID: "grant", AuthorityRevision: 1}
	if ValidScope(s) {
		t.Fatal("账号和发言人是同一串空白，却被收下")
	}
}
