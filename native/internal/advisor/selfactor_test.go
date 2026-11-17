package advisor

import (
	"strings"
	"testing"

	"sumika-native/internal/kernel"
)

// 审阅请求的身份：发言人不能是接收账号自己；QQ 场合必须带接收账号；本地主窗口没有平台账号，不要求。
func TestValidateRejectsSelfActorAndMissingAccount(t *testing.T) {
	t.Parallel()
	req := func(s kernel.Scope) kernel.ReviewRequest {
		return kernel.ReviewRequest{
			Invocation:   kernel.Invocation{Scope: s, RootID: "root", RunID: "run", Generation: 1, Kind: "foreground"},
			CheckpointID: "cp", Trigger: "failure",
			Work: kernel.WorkState{RootID: "root", Owner: s, Goal: "目标"},
		}
	}
	local := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "grant", AuthorityRevision: 1}
	qq := kernel.Scope{CharacterID: "selfactor-char", Conversation: "qq:private:91001:92001", ScopeID: "qq", Platform: "qq", Channel: "qq", AccountID: "91001", Scene: "private", ActorKind: "human", ActorID: "qq:92001", AuthorityID: "grant", AuthorityRevision: 1}
	localNoAccount := local
	localNoAccount.AccountID = ""
	for name, s := range map[string]kernel.Scope{"本地": local, "QQ": qq, "本地未填账号": localNoAccount} {
		if err := validate(req(s)); err != nil {
			t.Fatalf("%s：正常身份被拒：%v", name, err)
		}
	}
	for name, base := range map[string]kernel.Scope{"本地": local, "QQ": qq} {
		for _, actor := range []string{base.AccountID, base.Channel + ":" + base.AccountID, strings.ToUpper(base.Channel) + ":" + base.AccountID, base.Platform + ":" + base.AccountID, " " + base.AccountID + " "} {
			s := base
			s.ActorID = actor
			if err := validate(req(s)); err == nil {
				t.Errorf("%s：发言人 %q 就是账号，却被受理", name, actor)
			}
		}
	}
	noAccount := qq
	noAccount.AccountID = ""
	if err := validate(req(noAccount)); err == nil {
		t.Error("QQ 缺接收账号被受理")
	}
}

// 账号和发言人是同一串空白：本地场合不算缺账号，旧代码的裸比较把它拒掉，不能因为换成归一比较就放过。
func TestValidateRejectsIdenticalBlankActorAndAccount(t *testing.T) {
	t.Parallel()
	s := kernel.Scope{CharacterID: "selfactor-char", Conversation: "console:selfactor-char", ScopeID: "console", Platform: "local", Channel: "console", AccountID: " ", Scene: "private", ActorKind: "owner", ActorID: " ", AuthorityID: "grant", AuthorityRevision: 1}
	r := kernel.ReviewRequest{
		Invocation:   kernel.Invocation{Scope: s, RootID: "root", RunID: "run", Generation: 1, Kind: "foreground"},
		CheckpointID: "cp", Trigger: "failure",
		Work: kernel.WorkState{RootID: "root", Owner: s, Goal: "目标"},
	}
	if err := validate(r); err == nil {
		t.Fatal("账号和发言人是同一串空白，却被受理")
	}
}
