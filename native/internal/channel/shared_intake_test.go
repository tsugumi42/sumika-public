package channel

// 共用场景（testdata/shared/qq-intake，格式见该目录 README.md）：协议端交来的消息怎么入库、怎么去重、算谁说的。
// 用真实 Intake 和临时库。SUMIKA_SHARED_UPDATE=1 把实际结果写回各文件的 expect。

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/sharedtest"
)

type siItem struct {
	Account    string `json:"account"` // a | b：收到它的号
	Conv       string `json:"conv"`    // g1 | g2（群）| pa（A 号和朋友的私聊）| pb（B 号和朋友的私聊）
	Kind       string `json:"kind"`    // message（默认）| self（她自己的号发的）
	PlatformID string `json:"platform_id"`
	From       string `json:"from"` // friend | owner | a | b | stranger
	Text       string `json:"text"`
	TimeS      int64  `json:"time_s"` // 相对基准时刻的秒数，可以是负的
	Backfill   bool   `json:"backfill"`
	Expect     siOut  `json:"expect"`
}
type siOut struct {
	Duplicate bool   `json:"duplicate"` // 键已经在库里，只补了"看到了"
	Dropped   bool   `json:"dropped"`
	Character string `json:"character"` // 这一份是给哪个角色看的
	Origin    string `json:"origin"`
	ActorKind string `json:"actor_kind"`
	Observed  bool   `json:"observed"`
}
type siEvent struct {
	Conv       string `json:"conv"`
	PlatformID string `json:"platform_id"` // 空：没有进映射表
	ActorKind  string `json:"actor_kind"`
	ActorID    string `json:"actor_id"`
	Origin     string `json:"origin"`
	Text       string `json:"text"`
	TimeS      int64  `json:"time_s"`
}
type siObs struct {
	Character string `json:"character"`
	Event     int    `json:"event"` // 第几个事件（从 1 起，按入库先后）
}
type siFinal struct {
	Events       []siEvent `json:"events"`
	Observations []siObs   `json:"observations"`
}
type siScenario struct {
	ID          string   `json:"id"`
	About       string   `json:"about"`
	SourceTests []string `json:"source_tests"`
	Items       []siItem `json:"items"`
	Final       siFinal  `json:"final"`
}

func TestSharedQQIntake(t *testing.T) {
	t.Parallel()
	for _, path := range sharedtest.Files(t, "qq-intake") {
		var sc siScenario
		sharedtest.Load(t, path, &sc)
		t.Run(sc.ID, func(t *testing.T) {
			items, final := runIntakeScenario(t, sc)
			if sharedtest.Update() {
				for i := range sc.Items {
					sc.Items[i].Expect = items[i]
				}
				sc.Final = final
				sharedtest.Save(t, path, sc)
				return
			}
			for i, it := range sc.Items {
				if ok, want, got := sharedtest.Same(it.Expect, items[i]); !ok {
					t.Errorf("第 %d 条（%s %s）不同\n期望：%s\n实际：%s", i+1, it.Conv, it.PlatformID, want, got)
				}
			}
			if ok, want, got := sharedtest.Same(sc.Final, final); !ok {
				t.Errorf("最终库不同\n期望：%s\n实际：%s", want, got)
			}
		})
	}
}

func runIntakeScenario(t *testing.T, sc siScenario) ([]siOut, siFinal) {
	h := newHarness(t, nil)
	conv := func(n string) types.Conversation {
		switch n {
		case "g1":
			return group("900")
		case "g2":
			return group("901")
		case "pa":
			return private(acctA, friend)
		case "pb":
			return private(acctB, friend)
		}
		t.Fatalf("conv 不认：%q", n)
		return types.Conversation{}
	}
	acct := func(n string) string {
		switch n {
		case "a":
			return acctA
		case "b":
			return acctB
		}
		t.Fatalf("account 不认：%q", n)
		return ""
	}
	user := func(n string) string {
		switch n {
		case "friend":
			return friend
		case "owner":
			return owner
		case "a":
			return acctA
		case "b":
			return acctB
		case "stranger":
			return "30999"
		}
		t.Fatalf("from 不认：%q", n)
		return ""
	}
	var outs []siOut
	for _, it := range sc.Items {
		in := msg(acct(it.Account), conv(it.Conv), it.PlatformID, user(it.From), it.Text)
		in.Time = baseTime.Add(time.Duration(it.TimeS) * time.Second)
		switch it.Kind {
		case "", "message":
		case "self":
			in.Kind = types.InboundSelf
		default:
			t.Fatalf("kind 不认：%q", it.Kind)
		}
		before := len(h.stored())
		r := h.submit(in, Options{Backfill: it.Backfill})
		// 凭据先有结果、OnStored 随后才在队列里跑（intake.go 的 process）：等队列空下来再读通知，不然偶尔读到上一条
		idleCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := h.in.Idle(idleCtx); err != nil {
			cancel()
			t.Fatalf("等入库通知：%v", err)
		}
		cancel()
		out := siOut{Duplicate: r.Duplicate, Dropped: r.Dropped}
		if got := h.stored(); len(got) > before {
			s := got[len(got)-1]
			out.Character, out.Origin, out.ActorKind, out.Observed = s.Character, s.Origin, s.Actor.Kind, s.Observed
		}
		outs = append(outs, out)
	}
	final := siFinal{Events: []siEvent{}, Observations: []siObs{}}
	idx := map[int64]int{}
	pids := map[int64]string{}
	rows, err := h.st.Events.Query(`SELECT event_seq, platform_id FROM channel_msgs`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		var pid string
		if err := rows.Scan(&seq, &pid); err != nil {
			t.Fatal(err)
		}
		pids[seq] = pid
	}
	rows.Close()
	for i, e := range h.events() {
		idx[e.Seq] = i + 1
		final.Events = append(final.Events, siEvent{e.Conversation, pids[e.Seq], e.ActorKind, e.ActorID, e.Origin, e.Text, (e.CreatedAt - baseTime.UnixMilli()) / 1000})
	}
	orows, err := h.st.Events.Query(`SELECT character_id, event_seq FROM observations ORDER BY event_seq, character_id`)
	if err != nil {
		t.Fatal(err)
	}
	for orows.Next() {
		var c string
		var seq int64
		if err := orows.Scan(&c, &seq); err != nil {
			t.Fatal(err)
		}
		final.Observations = append(final.Observations, siObs{c, idx[seq]})
	}
	orows.Close()
	return outs, final
}
