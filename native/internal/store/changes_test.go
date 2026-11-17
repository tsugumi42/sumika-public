package store

import (
	"testing"
)

func TestCommittedChangesKeepDirtyDuringRead(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	sub := st.SubscribeChanges()
	defer sub.Close()
	add := func(text string) {
		t.Helper()
		_, err := st.AppendEvent(NewEvent{Channel: "qq", Conversation: "qq:group:90002", ActorKind: "human", ActorID: "90001", Kind: "message", Text: text, CreatedAt: 1}, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("before")
	<-sub.Wake()
	first := sub.Take()
	if !first["qq:group:90002"] {
		t.Fatal("提交没有失效身份")
	}
	add("during")
	<-sub.Wake()
	second := sub.Take()
	if !second["qq:group:90002"] {
		t.Fatal("读取期间的新提交丢失")
	}
	if len(sub.Take()) != 0 {
		t.Fatal("已消费的失效重复保留")
	}
	sub.Close()
	sub.Close()
	add("closed")
	select {
	case <-sub.Wake():
		t.Fatal("关闭后仍通知")
	default:
	}
}

func TestFailedCommitDoesNotNotify(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	sub := st.SubscribeChanges()
	defer sub.Close()
	if _, err := st.Events.Exec(`CREATE TABLE commit_probe (id INTEGER REFERENCES events(seq) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO commit_probe(id) VALUES(12345)`); err != nil {
		t.Fatal(err)
	}
	if err := st.commitChanged(tx, "qq:group:90002"); err == nil {
		t.Fatal("应当因外键失败")
	}
	select {
	case <-sub.Wake():
		t.Fatal("失败提交发出了通知")
	default:
	}
	if len(sub.Take()) != 0 {
		t.Fatal("失败事务留下 dirty")
	}
}

func TestDuplicateInboundObservationNotifies(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	in := NewInbound{Event: NewEvent{Channel: "qq", Conversation: "qq:group:90002", ActorKind: "human", ActorID: "90001", Kind: "message", CreatedAt: 1}, PlatformID: "1", Observers: []string{"a"}}
	first, err := st.AppendInbound(in)
	if err != nil {
		t.Fatal(err)
	}
	sub := st.SubscribeChanges()
	defer sub.Close()
	in.Observers = []string{"b"}
	second, err := st.AppendInbound(in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Seq != first.Seq || second.NewObservations != 1 {
		t.Fatalf("重复入站: %+v", second)
	}
	select {
	case <-sub.Wake():
	default:
		t.Fatal("只补观察没有通知")
	}
	if !sub.Take()[in.Event.Conversation] {
		t.Fatal("缺少会话 dirty")
	}
}

func TestOutboundCommittedChangePaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"end", "abort", "echo", "sweep", "sweep-plan"} {
		t.Run(path, func(t *testing.T) {
			set, _ := goSet(t)
			st, _ := set.Platform("qq")
			sub := st.SubscribeChanges()
			defer sub.Close()
			p := sendPlan("plan", tConv, tAcct, "test")
			if _, err := st.AppendPlan(p); err != nil {
				t.Fatal(err)
			}
			assertDirty := func() {
				t.Helper()
				select {
				case <-sub.Wake():
				default:
					t.Fatal("成功提交没有唤醒")
				}
				if !sub.Take()[tConv] {
					t.Fatal("成功提交没有会话身份")
				}
			}
			assertDirty()
			if _, err := st.AppendPlan(p); err == nil {
				t.Fatal("重复计划应失败")
			}
			select {
			case <-sub.Wake():
				t.Fatal("失败计划仍通知")
			default:
			}
			switch path {
			case "end":
				ok, err := st.EndPart(p.ID, 0, []string{PartPending}, PartConfirmed, PartEnd{PlatformID: "123"}, t0+1)
				if err != nil || !ok {
					t.Fatalf("EndPart: %v %v", ok, err)
				}
			case "abort":
				n, err := st.AbortPlanParts(p.ID, "test", t0+1)
				if err != nil || n != 1 {
					t.Fatalf("AbortPlanParts: %d %v", n, err)
				}
			case "echo":
				if _, ok, err := st.BeginSend(p.ID, 0, t0); err != nil || !ok {
					t.Fatalf("BeginSend: %v %v", ok, err)
				}
				_, _, ok, err := st.ClaimEcho(EchoClaim{Channel: "qq", Account: tAcct, Conversation: tConv, PlatformID: "123", Since: t0, Match: func(s string) bool { return s == "test" }}, t0+1)
				if err != nil || !ok {
					t.Fatalf("ClaimEcho: %v %v", ok, err)
				}
			case "sweep":
				out, err := st.Sweep("unknown", "abort", t0+1)
				if err != nil || out.Aborted != 1 {
					t.Fatalf("Sweep: %+v %v", out, err)
				}
			case "sweep-plan":
				out, err := st.SweepPlan(p.ID, "unknown", "abort", t0+1)
				if err != nil || out.Aborted != 1 {
					t.Fatalf("SweepPlan: %+v %v", out, err)
				}
			}
			assertDirty()
		})
	}
}
