package store

import (
	"fmt"
	"testing"
)

// 闸门要读写的库（gate.go）：攒着的话怎么取、最近 5 分钟的窗口、窗口状态、判断记录、频率规则、档位。会话、号、话都是编的。

func gateMsg(t *testing.T, s *Store, conv, kind, actor, origin, text string, at int64, segs any) (seq int64, id string) {
	t.Helper()
	seq, err := s.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: kind, ActorID: actor, Origin: origin, Kind: "message", Text: text, Segments: segs, CreatedAt: at}, []string{"ca"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Events.QueryRow(`SELECT id FROM events WHERE seq = ?`, seq).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return seq, id
}

func TestPendingMessages(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	const conv = "qq:group:5001"
	base := int64(1_790_000_000_000)
	var seqs []int64
	for i := 0; i < 8; i++ {
		seq, _ := gateMsg(t, s, conv, "human", "qq:1", "peer", fmt.Sprintf("话%d", i), base+int64(i)*1000, nil)
		seqs = append(seqs, seq)
	}
	// 不算的几种：她自己的（含手动发的）、插件自动发的、补录的、撤回的、别的会话的、通知
	gateMsg(t, s, conv, "character", "ca", "character", "她说的", base+9000, nil)
	gateMsg(t, s, conv, "character", "ca", "human", "手动发的", base+9100, nil)
	gateMsg(t, s, conv, "character", "ca", "automation:repeat", "草", base+9200, nil)
	gateMsg(t, s, conv, "human", "qq:2", "peer", "补录的", base+9300, map[string]any{"segs": []any{}, "tags": []string{"backfill"}})
	_, rid := gateMsg(t, s, conv, "human", "qq:2", "peer", "撤回的", base+9400, nil)
	if _, err := s.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:2", Origin: "peer", Kind: "notice", Segments: map[string]any{"notice": "recall"}, ReplyTo: &rid, CreatedAt: base + 9500}, []string{"ca"}); err != nil {
		t.Fatal(err)
	}
	gateMsg(t, s, "qq:group:5002", "human", "qq:1", "peer", "别的群", base+9600, nil)
	// 另一个角色的号发的话，对这个角色是别人说的，要算
	gateMsg(t, s, conv, "character", "cb", "character", "另一个角色", base+9700, nil)

	rows, earlier, err := s.PendingMessages(conv, "ca", 0, 50, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.Text)
	}
	want := []string{"话0", "话1", "话2", "话3", "话4", "话5", "话6", "话7", "另一个角色"}
	if fmt.Sprint(got) != fmt.Sprint(want) || earlier != 0 {
		t.Fatalf("攒着的话：%v 更早 %d，想要 %v", got, earlier, want)
	}
	// 上次看完以后
	rows, _, _ = s.PendingMessages(conv, "ca", seqs[5], 50, 10000)
	if len(rows) != 3 || rows[0].Text != "话6" {
		t.Fatalf("seq 之后的：%+v", rows)
	}
	// 只取最近 3 条，更早的数个数（撤回没逐条核，补录、自己的不数）
	rows, earlier, _ = s.PendingMessages(conv, "ca", 0, 3, 10000)
	if len(rows) != 3 || rows[0].Text != "话6" || rows[2].Text != "另一个角色" || earlier != 6 {
		t.Fatalf("最近 3 条：%+v 更早 %d", rows, earlier)
	}
	// 数个数有上限：最多数到 cap+1
	_, earlier, _ = s.PendingMessages(conv, "ca", 0, 3, 4)
	if earlier != 5 {
		t.Fatalf("上限 4 时数到 5 就停：%d", earlier)
	}
	if rows, _, _ := s.PendingMessages(conv, "ca", 0, 0, 10); rows != nil {
		t.Fatal("limit 0 什么也不取")
	}
}

func TestRecentWindow(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	const conv = "qq:group:5001"
	base := int64(1_790_000_000_000)
	gateMsg(t, s, conv, "character", "ca", "character", "很早以前她说的", base-3_600_000, nil)
	gateMsg(t, s, conv, "human", "qq:1", "peer", "窗口外", base-400_000, nil)
	gateMsg(t, s, conv, "human", "qq:1", "peer", "窗口里", base-100_000, nil)
	gateMsg(t, s, conv, "character", "ca", "character", "窗口里她说的", base-50_000, nil)
	gateMsg(t, s, conv, "human", "qq:1", "peer", "还没发生", base+1000, nil)
	rows, last, err := s.RecentWindow(conv, "ca", base-300_000, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Text != "窗口里" || rows[1].Text != "窗口里她说的" {
		t.Fatalf("窗口：%+v", rows)
	}
	if last == nil || last.Text != "窗口里她说的" {
		t.Fatalf("上次说话：%+v", last)
	}
	// 窗口里没有她说的：last 是窗口外最近的一条
	rows, last, _ = s.RecentWindow(conv, "ca", base-60_000-100_000, base-60_000)
	if len(rows) != 1 || last == nil || last.Text != "很早以前她说的" {
		t.Fatalf("窗口外的上次说话：%+v %+v", rows, last)
	}
	if _, last, _ := s.RecentWindow(conv, "cb", base-300_000, base); last != nil {
		t.Fatal("另一个角色没说过话")
	}
	if r, ok, _ := s.EventRow(conv, 1); !ok || r.Text != "很早以前她说的" {
		t.Fatalf("按 seq 取：%+v %v", r, ok)
	}
	if _, ok, _ := s.EventRow(conv, 9999); ok {
		t.Fatal("没有这个 seq")
	}
}

func TestGateTablesRoundTrip(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	if _, _, ok, err := s.LoadGateState("ca|x"); ok || err != nil {
		t.Fatalf("没存过：%v %v", ok, err)
	}
	if err := s.SaveGateState("ca|x", `{"a":1}`, 7, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGateState("ca|x", `{"a":2}`, 9, 200); err != nil {
		t.Fatal(err)
	}
	if st, since, ok, _ := s.LoadGateState("ca|x"); !ok || st != `{"a":2}` || since != 9 {
		t.Fatalf("覆盖：%s %d %v", st, since, ok)
	}

	p := 0.5
	id, err := s.AddGateDecision(GateDecisionRow{At: 5, CharacterID: "ca", Conversation: "c1", Seq: 3, Batch: 2, Probability: &p, Frequency: 0.3, Verdict: "enter", Reason: "理由"})
	if err != nil {
		t.Fatal(err)
	}
	s.AddGateDecision(GateDecisionRow{At: 6, CharacterID: "ca", Conversation: "c1", Seq: 4, Batch: 1, Features: `{"x":1}`, Frequency: 0.3, Verdict: "skip", Reason: "理由二"})
	rows, _ := s.GateDecisions("c1", 10)
	if len(rows) != 2 || rows[0].Verdict != "skip" || rows[0].Features != `{"x":1}` || rows[0].Probability != nil || rows[1].Probability == nil || *rows[1].Probability != 0.5 {
		t.Fatalf("判断记录：%+v", rows)
	}
	if err := s.SetGateOutcome(id, "spoke", "说了"); err != nil {
		t.Fatal(err)
	}
	if rows, _ = s.GateDecisions("c1", 10); rows[1].Outcome != "spoke" || rows[1].OutcomeReason != "说了" {
		t.Fatalf("结果：%+v", rows[1])
	}

	if err := s.SetGateRule(GateRule{Conversation: "c1", Frequency: 0.5}, false); err != nil {
		t.Fatal(err)
	}
	s.SetGateRule(GateRule{Conversation: "*", Slot: "22:00-07:00", Frequency: 0.1}, false)
	s.SetGateRule(GateRule{Conversation: "c1", Slot: "temp", Frequency: 0, Until: 999}, false)
	s.SetGateRule(GateRule{Conversation: "c2", Frequency: 0.9}, false)
	rules, _ := s.GateRules("c1")
	if len(rules) != 3 {
		t.Fatalf("这个群和所有群的规则：%+v", rules)
	}
	s.SetGateRule(GateRule{Conversation: "c1", Slot: "temp"}, true)
	if rules, _ = s.GateRules("c1"); len(rules) != 2 {
		t.Fatalf("删掉临时的：%+v", rules)
	}

	if _, ok, _ := s.GateModeOf("c1"); ok {
		t.Fatal("没设过档位")
	}
	s.SetGateMode("*", "dry", 1)
	if m, ok, _ := s.GateModeOf("c1"); !ok || m != "dry" {
		t.Fatalf("所有群的：%s %v", m, ok)
	}
	s.SetGateMode("c1", "speak", 2)
	if m, _, _ := s.GateModeOf("c1"); m != "speak" {
		t.Fatalf("这个群的盖过所有群的：%s", m)
	}
	s.SetGateMode("c1", "", 3)
	if m, _, _ := s.GateModeOf("c1"); m != "dry" {
		t.Fatalf("删掉这个群的以后回到所有群的：%s", m)
	}
}
