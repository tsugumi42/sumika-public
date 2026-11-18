package proactive

import (
	"testing"
	"time"
)

// 醒来行带回“最近说过什么”要用的两个读取（规格-主动联系 第四节）。角色、会话、话都是编的。

// 只取这个角色真发给指定会话的话（sent、unknown），新的在前；
// 失败、只想不说、还在途的、发往别的会话和别的角色的都不进来
func TestRecentSentToOnlyRealSendsToThatConversation(t *testing.T) {
	e := newMgr(t)
	e.p.MaxSendsPerThread = 20
	begin := func(id, conv, text, state string) {
		t.Helper()
		e.advance(time.Minute)
		s, err := e.m.BeginSend(id, conv, "地点", "场合", text)
		if err != nil {
			t.Fatal(err)
		}
		if state != SendPending {
			if _, err := e.m.FinishSend(s.ID, state, 1, "ev"); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.open("t1")
	begin("t1", "console:aa", "第一句", SendSent)
	begin("t1", "console:aa", "没发出去", SendFailed)
	begin("t1", "console:aa", "只是想想", SendThought)
	begin("t1", "console:aa", "在途", SendPending)
	begin("t1", "qq:private:10001", "发往别处", SendSent)
	begin("t1", "console:aa", "第二句", SendUnknown)
	if _, err := e.m.Create(Thread{ID: "t2", Character: "bb", Conversation: "console:bb:topic:thread-t2", OpenedAt: e.now}); err != nil {
		t.Fatal(err)
	}
	begin("t2", "console:aa", "别人的", SendSent)

	got, err := e.m.RecentSentTo("aa", "console:aa", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Summary != "第二句" || got[1].Summary != "第一句" {
		t.Fatalf("只该有两条真发出去的，新的在前：%+v", got)
	}
	if got, _ := e.m.RecentSentTo("aa", "console:aa", 1); len(got) != 1 || got[0].Summary != "第二句" {
		t.Fatalf("条数上限：%+v", got)
	}
	if got, _ := e.m.RecentSentTo("cc", "console:aa", 5); len(got) != 0 {
		t.Fatalf("没有这个角色的线：%+v", got)
	}
}

// 只要已关、发过话、有结论的线，新的在前；开着的、没发过话的、没写结论的都不算
func TestRecentConcludedOnlyClosedWithSendsAndConclusion(t *testing.T) {
	e := newMgr(t)
	closeWith := func(id string, sends bool, concl string) {
		t.Helper()
		e.advance(time.Hour)
		e.open(id)
		if sends {
			e.send(id)
		}
		if _, err := e.m.Apply(id, Event{Kind: EvEnd, At: e.now, Conclusion: concl}); err != nil {
			t.Fatal(err)
		}
	}
	closeWith("c1", true, "聊了周末的展")
	closeWith("c2", false, "没开口也有结论")
	closeWith("c3", true, "")
	closeWith("c4", true, "问了他睡得怎样")
	e.advance(time.Hour)
	e.open("c5")

	got, err := e.m.RecentConcluded("aa", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "c4" || got[1].ID != "c1" {
		ids := []string{}
		for _, g := range got {
			ids = append(ids, g.ID)
		}
		t.Fatalf("只该有 c4、c1：%v", ids)
	}
	if got, _ := e.m.RecentConcluded("aa", 1); len(got) != 1 || got[0].ID != "c4" {
		t.Fatalf("条数上限：%+v", got)
	}
}
