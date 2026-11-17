package memory

import (
	"context"
	"testing"
)

const (
	ownerDM      = "qq:private:20001:10001"
	peerDM       = "qq:private:20001:60001"
	peerA        = "qq:60001"
	consoleScope = "owner"
)

func qqGroup(g string, ownerAsked bool, speakers ...string) Audience {
	return Audience{Channel: "qq", Conversation: g, Scene: "group", PersonIDs: speakers, OwnerAsked: ownerAsked}
}

func hasRef(hs []Hit, ref string) bool {
	for _, h := range hs {
		if h.Ref == ref {
			return true
		}
	}
	return false
}

// 主人问后群友再问：都想得起，表达判断和实例档仍按真实发言身份分开。
func TestF02_OwnerAskedThenPeerThenOwner(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	res := mustApply(t, m, remember(ownerFact("主人下周参加星桥考试")))
	ref := res.Outcomes[0].Ref
	owner, peer := qqGroup(GA, true, "owner"), qqGroup(GA, false, peerA)
	for i, a := range []Audience{owner, peer, owner} {
		r := m.Recall(context.Background(), a, Query{Text: "星桥考试", Addressed: true})
		if !hasRef(r.Hits, ref) {
			t.Fatalf("第 %d 轮忘记了：%+v", i, r)
		}
		blocked, err := m.CheckOutbound(context.Background(), "主人下周参加星桥考试", a)
		if err != nil || (len(blocked) > 0) != (i == 1) {
			t.Fatalf("第 %d 轮披露=%v %v", i, blocked, err)
		}
	}
	if ScopeKey(owner) == ScopeKey(peer) {
		t.Fatal("主人与群友共用表达档")
	}
}

func TestScopeKey(t *testing.T) {
	t.Parallel()
	a := Audience{Channel: "qq", Conversation: peerDM, Scene: "private", PersonIDs: []string{"qq:2", "qq:1"}}
	b := a
	b.PersonIDs = []string{"qq:1", "qq:2"}
	if ScopeKey(a) != ScopeKey(b) || a.PersonIDs[0] != "qq:2" {
		t.Fatal("档受顺序影响或改了输入")
	}
	if ScopeKey(ownerHere) == consoleScope {
		t.Fatal("档撞上主窗口")
	}
	if ScopeKey(qqGroup(GA, true)) == ScopeKey(qqGroup(GA, false)) {
		t.Fatal("群友与主人同档")
	}
}
