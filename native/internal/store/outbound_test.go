package store

import (
	"reflect"
	"strings"
	"testing"
)

// 测试里的会话、号、名字都是编的（LESSONS：不用真实群名、昵称、号码）

const (
	tConv = "qq:group:900"
	tAcct = "20001"
	t0    = int64(1_700_000_000_000) // 计划创建的时刻
)

// sendPlan 造一条 send 计划：几段文字，带她这句话的 events 行
func sendPlan(id, conv, account string, texts ...string) NewPlan {
	p := NewPlan{
		ID: id, CharacterID: "ca", Conversation: conv, Account: account, Origin: "character", Kind: PlanKindSend, At: t0,
		Event:     &NewEvent{Channel: "qq", Conversation: conv, ActorKind: "character", ActorID: "ca", Origin: "character", Kind: "message", Text: strings.Join(texts, "\n\n"), CreatedAt: t0},
		Observers: []string{"ca"},
	}
	for _, tx := range texts {
		p.Parts = append(p.Parts, NewPart{Kind: "message", Text: tx})
	}
	return p
}

func mustPlan(t *testing.T, s *Store, id string) Plan {
	t.Helper()
	p, err := s.GetPlan(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func states(p Plan) []string {
	var out []string
	for _, pt := range p.Parts {
		out = append(out, pt.State)
	}
	return out
}

// 计划、她这句话的 events、各段在一个事务里：成功时三样都在，observations 记了她自己；
// 中途失败（计划编号撞了）时 events 那一行也不在——不会留下一句没有计划的话
func TestAppendPlanIsOneTransaction(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	seq, err := s.AppendPlan(sendPlan("P1", tConv, tAcct, "第一条", "第二条"))
	if err != nil || seq == 0 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
	p := mustPlan(t, s, "P1")
	if p.EventSeq != seq || p.State != PlanSending || p.Kind != PlanKindSend || p.DoneAt != 0 || len(p.Parts) != 2 {
		t.Fatalf("落库的样子不对：%+v", p)
	}
	if !reflect.DeepEqual(states(p), []string{PartPending, PartPending}) {
		t.Fatalf("段该是待发：%v", states(p))
	}
	if n := count(t, s, `SELECT count(*) FROM observations WHERE event_seq = ? AND character_id = 'ca'`, seq); n != 1 {
		t.Fatalf("她自己该看到这一行：%d", n)
	}

	events := count(t, s, `SELECT count(*) FROM events`)
	if _, err := s.AppendPlan(sendPlan("P1", tConv, tAcct, "撞编号")); err == nil {
		t.Fatal("计划编号撞了该失败")
	}
	if got := count(t, s, `SELECT count(*) FROM events`); got != events {
		t.Fatalf("计划没写进去，events 那一行不该留下：之前 %d 行，现在 %d 行", events, got)
	}
	if got := count(t, s, `SELECT count(*) FROM outbound_parts`); got != 2 {
		t.Fatalf("段不该多：%d", got)
	}
}

// 五种计划：send 待发；dry-run 的段是 dry-run、创建就是终态；拦下、已打断、出错没有段、不写 events、状态就是种类本身；
// 后三种带了段或事件要被拒（它们不存原文）
func TestAppendPlanKinds(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	dry := sendPlan("D1", tConv, tAcct, "要说的话")
	dry.Kind = PlanKindDryRun
	if _, err := s.AppendPlan(dry); err != nil {
		t.Fatal(err)
	}
	p := mustPlan(t, s, "D1")
	if p.State != PlanKindDryRun || p.DoneAt != t0 || p.Parts[0].State != PartDryRun || p.EventSeq == 0 {
		t.Fatalf("试运行：%+v", p)
	}

	for _, k := range []string{PlanKindBlocked, PlanKindInterrupted, PlanKindError} {
		np := NewPlan{ID: "X-" + k, CharacterID: "ca", Conversation: tConv, Account: tAcct, Origin: "character", Kind: k, Reason: "原因", At: t0}
		if seq, err := s.AppendPlan(np); err != nil || seq != 0 {
			t.Fatalf("%s：seq=%d err=%v", k, seq, err)
		}
		p := mustPlan(t, s, np.ID)
		if p.State != k || p.EventSeq != 0 || len(p.Parts) != 0 || p.Reason != "原因" || p.DoneAt != t0 {
			t.Fatalf("%s：%+v", k, p)
		}
		withParts := np
		withParts.ID, withParts.Parts = np.ID+"-p", []NewPart{{Kind: "message", Text: "原文"}}
		withEvent := np
		withEvent.ID, withEvent.Event = np.ID+"-e", &NewEvent{Channel: "qq", Conversation: tConv, ActorKind: "character", ActorID: "ca", Origin: "character", Kind: "message", Text: "原文", CreatedAt: t0}
		for _, bad := range []NewPlan{withParts, withEvent} {
			if _, err := s.AppendPlan(bad); err == nil {
				t.Errorf("%s 的计划不存原文，带段或事件该被拒：%s", k, bad.ID)
			}
		}
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE text = '原文'`); n != 0 {
		t.Fatalf("拦下的原文进了库：%d 行", n)
	}

	bads := map[string]NewPlan{
		"没有段":     {ID: "B1", CharacterID: "ca", Conversation: tConv, Account: tAcct, Origin: "character", Kind: PlanKindSend, At: t0},
		"种类不认识":   {ID: "B2", CharacterID: "ca", Conversation: tConv, Account: tAcct, Origin: "character", Kind: "mystery", At: t0},
		"没有号":     func() NewPlan { p := sendPlan("B3", tConv, "", "x"); return p }(),
		"文字段没有文字": {ID: "B4", CharacterID: "ca", Conversation: tConv, Account: tAcct, Origin: "character", Kind: PlanKindSend, At: t0, Parts: []NewPart{{Kind: "message"}}},
	}
	for name, bad := range bads {
		if _, err := s.AppendPlan(bad); err == nil {
			t.Errorf("%s 该被拒", name)
		}
	}
}

// 计划状态怎么由段汇总：字面的表，不拿实现自己的函数对自己
func TestSummarizeParts(t *testing.T) {
	t.Parallel()
	c, f, u, a, pe, se, re := PartConfirmed, PartFailed, PartUnknown, PartAborted, PartPending, PartSending, PartRetrying
	for _, tc := range []struct {
		name  string
		parts []string
		want  string
	}{
		{"全确认", []string{c, c, c}, PlanDone},
		{"一段确认", []string{c}, PlanDone},
		{"确认后失败，后面中止", []string{c, f, a}, PlanPartial},
		{"确认后结果未知，后面中止", []string{c, u, a}, PlanUnknown},
		{"确认后被关闭中止", []string{c, a, a}, PlanPartial},
		{"第一段就失败", []string{f, a, a}, PlanFailed},
		{"一段没发就被中止", []string{a, a}, PlanAborted},
		{"第一段结果未知", []string{u, a}, PlanUnknown},
		{"还有待发", []string{c, pe}, PlanSending},
		{"还有发送中", []string{c, se}, PlanSending},
		{"还有等重试", []string{re}, PlanSending},
		{"在途的盖过未知", []string{u, se}, PlanSending},
		{"没有段", nil, PlanSending},
	} {
		if got := SummarizeParts(tc.parts); got != tc.want {
			t.Errorf("%s：%v 汇总成 %q，应该是 %q", tc.name, tc.parts, got, tc.want)
		}
	}
}

// 转移是条件更新：BeginSend 只从待发和等重试转，次数加一，sent_at 只记第一次；EndPart 当前状态不在 from 里就不动
func TestPartTransitionsAreConditional(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	if _, err := s.AppendPlan(sendPlan("P1", tConv, tAcct, "甲", "乙")); err != nil {
		t.Fatal(err)
	}
	n, ok, err := s.BeginSend("P1", 0, t0+10)
	if err != nil || !ok || n != 1 {
		t.Fatalf("第一次开始发送：n=%d ok=%v err=%v", n, ok, err)
	}
	if _, ok, _ := s.BeginSend("P1", 0, t0+20); ok {
		t.Fatal("发送中的段不能再开始发送")
	}
	if ok, err := s.EndPart("P1", 0, []string{PartPending}, PartConfirmed, PartEnd{}, t0+30); err != nil || ok {
		t.Fatalf("当前是发送中，from 只写待发：不该转（ok=%v err=%v）", ok, err)
	}
	if ok, err := s.EndPart("P1", 0, []string{PartSending}, PartRetrying, PartEnd{Err: "没连上"}, t0+40); err != nil || !ok {
		t.Fatalf("发送中 → 等重试：ok=%v err=%v", ok, err)
	}
	n, ok, err = s.BeginSend("P1", 0, t0+1000)
	if err != nil || !ok || n != 2 {
		t.Fatalf("重试：n=%d ok=%v err=%v", n, ok, err)
	}
	pt := mustPlan(t, s, "P1").Parts[0]
	if pt.SentAt != t0+10 || pt.Attempts != 2 || pt.State != PartSending {
		t.Fatalf("sent_at 记第一次，次数是 2：%+v", pt)
	}
	if ok, _ := s.EndPart("P1", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7001", Via: "reply"}, t0+1100); !ok {
		t.Fatal("发送中 → 确认")
	}
	if _, ok, _ := s.BeginSend("P1", 0, t0+2000); ok {
		t.Fatal("已确认的段不能再开始发送")
	}
	pt = mustPlan(t, s, "P1").Parts[0]
	if pt.PlatformID != "7001" || pt.Via != "reply" || pt.Err != "" {
		t.Fatalf("确认的样子：%+v", pt)
	}
}

// 计划状态在每次转移的事务里重算，所以任何提交过的库里它都等于段的汇总；
// 结果未知的段被回声认领以后，汇总跟着变（unknown → partial），这是 state 唯一会被改第二次的情形
func TestPlanStateFollowsParts(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	if _, err := s.AppendPlan(sendPlan("P1", tConv, tAcct, "甲", "乙", "丙")); err != nil {
		t.Fatal(err)
	}
	step := func(wantState string, wantDone bool) {
		t.Helper()
		p := mustPlan(t, s, "P1")
		if p.State != wantState || (p.DoneAt != 0) != wantDone {
			t.Fatalf("计划状态 %q done_at=%d，应该是 %q（done=%v）；段 %v", p.State, p.DoneAt, wantState, wantDone, states(p))
		}
	}
	step(PlanSending, false)
	s.BeginSend("P1", 0, t0+1)
	s.EndPart("P1", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "9001"}, t0+2)
	step(PlanSending, false)
	s.BeginSend("P1", 1, t0+3)
	s.EndPart("P1", 1, []string{PartSending}, PartUnknown, PartEnd{Err: "超时"}, t0+4)
	step(PlanSending, false) // 第三段还是待发
	if n, err := s.AbortPlanParts("P1", "前面的段没发出去", t0+5); err != nil || n != 1 {
		t.Fatalf("中止剩下的：n=%d err=%v", n, err)
	}
	step(PlanUnknown, true)
	if n, _ := s.AbortPlanParts("P1", "再来一次", t0+6); n != 0 {
		t.Fatalf("没有待发的了：%d", n)
	}
	p := mustPlan(t, s, "P1")
	if p.DoneAt != t0+5 {
		t.Fatalf("done_at 只记第一次到终态的时刻：%d", p.DoneAt)
	}
	// 回声认领第二段 → 确认，汇总变成 partial（第三段是中止的）
	id, idx, found, err := s.ClaimEcho(EchoClaim{Channel: "qq", Account: tAcct, Conversation: tConv, PlatformID: "9002", Since: t0, Match: func(s string) bool { return s == "乙" }}, t0+7)
	if err != nil || !found || id != "P1" || idx != 1 {
		t.Fatalf("认领：%s %d %v %v", id, idx, found, err)
	}
	step(PlanPartial, true)
	if p := mustPlan(t, s, "P1"); p.DoneAt != t0+5 {
		t.Fatalf("重算汇总不改 done_at：%d", p.DoneAt)
	}
}

// 确认的段带着平台消息号，同一个事务里记 channel_msgs → 这次回复的事件；已经映射给别的事件的号不覆盖；没有事件的计划（纯戳一戳）不映射
func TestConfirmedPartWritesMapping(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	seq, err := s.AppendPlan(sendPlan("P1", tConv, tAcct, "甲"))
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSend("P1", 0, t0+1)
	if ok, err := s.EndPart("P1", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7001", Via: "reply"}, t0+2); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var got int64
	if err := s.Events.QueryRow(`SELECT event_seq FROM channel_msgs WHERE channel = 'qq' AND conversation = ? AND platform_id = '7001'`, tConv).Scan(&got); err != nil || got != seq {
		t.Fatalf("映射该指向这次回复的事件 %d：%d %v", seq, got, err)
	}

	// 号已经映射给别的事件：不覆盖（INSERT OR IGNORE）
	other, err := s.AppendPlan(sendPlan("P2", tConv, tAcct, "乙"))
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSend("P2", 0, t0+3)
	if ok, err := s.EndPart("P2", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7001"}, t0+4); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.Events.QueryRow(`SELECT event_seq FROM channel_msgs WHERE platform_id = '7001'`).Scan(&got); err != nil || got != seq || got == other {
		t.Fatalf("先到的映射该留着：%d（另一个是 %d）%v", got, other, err)
	}

	// 全是 0 的号不能用来去重，也就不映射
	s.AppendPlan(sendPlan("P3", tConv, tAcct, "丙"))
	s.BeginSend("P3", 0, t0+5)
	s.EndPart("P3", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "0"}, t0+6)
	if n := count(t, s, `SELECT count(*) FROM channel_msgs WHERE platform_id = '0'`); n != 0 {
		t.Fatalf("号为 0 不映射：%d", n)
	}

	// 只有戳一戳的计划没有事件
	poke := NewPlan{ID: "K1", CharacterID: "ca", Conversation: tConv, Account: tAcct, Origin: "automation:poke", Kind: PlanKindSend, At: t0,
		Parts: []NewPart{{Kind: "poke", Ref: "30001"}}}
	if seq, err := s.AppendPlan(poke); err != nil || seq != 0 {
		t.Fatal(seq, err)
	}
	s.BeginSend("K1", 0, t0+7)
	if ok, err := s.EndPart("K1", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "8001"}, t0+8); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if n := count(t, s, `SELECT count(*) FROM channel_msgs WHERE platform_id = '8001'`); n != 0 {
		t.Fatalf("没有事件就没有可映射的：%d", n)
	}
	if p := mustPlan(t, s, "K1"); p.State != PlanDone || p.EventSeq != 0 {
		t.Fatalf("纯戳一戳的计划：%+v", p)
	}
}

// 空类型仍只认文字；转发按原来的账号、会话、窗口、先发先认和平台号去重规则认领，不比文字。
func TestClaimEchoForwardCandidates(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	for _, row := range []struct {
		id, kind, account, conv string
		sent                    int64
	}{
		{"过期", "forward", tAcct, tConv, t0 + 1},
		{"别的号", "forward", "20002", tConv, t0 + 2},
		{"别的会话", "forward", tAcct, "qq:group:901", t0 + 3},
		{"转发一", "forward", tAcct, tConv, t0 + 6},
		{"转发二", "forward", tAcct, tConv, t0 + 7},
		{"文字", "message", tAcct, tConv, t0 + 8},
	} {
		p := sendPlan(row.id, row.conv, row.account, "编的正文")
		p.Parts[0].Kind = row.kind
		if _, err := s.AppendPlan(p); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.BeginSend(row.id, 0, row.sent); err != nil || !ok {
			t.Fatalf("%s 没有开始发送：%v %v", row.id, ok, err)
		}
	}
	c := EchoClaim{Channel: "qq", Account: tAcct, Conversation: tConv, Since: t0 + 5, PlatformID: "7000", Match: func(string) bool { return true }}
	if id, _, found, err := s.ClaimEcho(c, t0+100); err != nil || !found || id != "文字" {
		t.Fatalf("省略类型仍该只认文字：%q %v %v", id, found, err)
	}
	c.Kind = "forward"
	c.Match = nil // 纯转发由类型匹配；出口另提供拒绝媒体候选的谓词。
	for i, want := range []string{"转发一", "转发二"} {
		c.PlatformID = []string{"7001", "7002"}[i]
		id, idx, found, err := s.ClaimEcho(c, t0+100)
		if err != nil || !found || id != want || idx != 0 {
			t.Fatalf("该按发送顺序认领 %s：%q %d %v %v", want, id, idx, found, err)
		}
		p := mustPlan(t, s, want)
		if pt := p.Parts[0]; p.State != PlanDone || pt.State != PartConfirmed || pt.Via != "echo" || pt.PlatformID != c.PlatformID {
			t.Fatalf("转发没有被回声确认：%+v", p)
		}
		if n := count(t, s, `SELECT count(*) FROM channel_msgs WHERE conversation = ? AND platform_id = ? AND event_seq = ?`, tConv, c.PlatformID, p.EventSeq); n != 1 {
			t.Fatalf("转发的号该指向计划事件 %d：%d 条映射", p.EventSeq, n)
		}
		if id, _, found, err := s.ClaimEcho(c, t0+100); err != nil || found {
			t.Fatalf("重复回声不能再认一段：%q %v %v", id, found, err)
		}
	}
	c.PlatformID = "7003"
	if id, _, found, err := s.ClaimEcho(c, t0+100); err != nil || found {
		t.Fatalf("没有同号同会话且在窗口内的转发可认：%q %v %v", id, found, err)
	}
	for _, id := range []string{"过期", "别的号", "别的会话"} {
		if p := mustPlan(t, s, id); p.State != PlanSending || p.Parts[0].PlatformID != "" {
			t.Fatalf("%s 的转发不该被认领：%+v", id, p)
		}
	}
}

// 回声认领的条件：键是（号、会话、文字）；只认发送中、等重试、结果未知；窗口内；发得最早的先认；每段只认一次
func TestClaimEchoConditions(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	text := func(want string) func(string) bool { return func(s string) bool { return s == want } }
	claim := func(account, conv, pid, want string, since int64) (string, int, bool) {
		t.Helper()
		id, idx, found, err := s.ClaimEcho(EchoClaim{Channel: "qq", Account: account, Conversation: conv, PlatformID: pid, Since: since, Match: text(want)}, t0+100)
		if err != nil {
			t.Fatal(err)
		}
		return id, idx, found
	}
	// 三条计划，同样的文字"哈"：P1 号 20001 发送中（sent_at 早），P2 号 20001 结果未知（晚），P3 号 20002 发送中
	s.AppendPlan(sendPlan("P1", tConv, tAcct, "哈"))
	s.AppendPlan(sendPlan("P2", tConv, tAcct, "哈"))
	s.AppendPlan(sendPlan("P3", tConv, "20002", "哈"))
	s.BeginSend("P1", 0, t0+10)
	s.BeginSend("P2", 0, t0+20)
	s.EndPart("P2", 0, []string{PartSending}, PartUnknown, PartEnd{Err: "超时"}, t0+21)
	s.BeginSend("P3", 0, t0+5)

	if _, _, found := claim(tAcct, "qq:group:另一个群", "5001", "哈", 0); found {
		t.Fatal("会话对不上不该认")
	}
	if _, _, found := claim(tAcct, tConv, "5001", "别的字", 0); found {
		t.Fatal("文字对不上不该认")
	}
	if _, _, found := claim(tAcct, tConv, "0", "哈", 0); found {
		t.Fatal("没有平台消息号没法映射，不该认")
	}
	// 窗口从这一段第一次调 Send 算：Since 在 P1（t0+10）之后，P1 过期了，窗口里只剩 P2（t0+20，结果未知）
	if id, _, found := claim(tAcct, tConv, "5001", "哈", t0+11); !found || id != "P2" {
		t.Fatalf("P1 过窗口了，窗口里只有 P2 该被认：%s %v", id, found)
	}
	// 窗口放开：号 20001 同文字，剩下的 P1 认下
	id, idx, found := claim(tAcct, tConv, "5002", "哈", 0)
	if !found || id != "P1" || idx != 0 {
		t.Fatalf("P1：%s %d %v", id, idx, found)
	}
	// 号 20001 的两段都认过了，再来一个回声没有可认的；号 20002 的 P3 还在，且不被 20001 的回声串走
	if _, _, found := claim(tAcct, tConv, "5003", "哈", 0); found {
		t.Fatal("每段只认一次")
	}
	id, _, found = claim("20002", tConv, "5004", "哈", 0)
	if !found || id != "P3" {
		t.Fatalf("另一个号的那段该由另一个号的回声认领：%s %v", id, found)
	}
	// 映射都记下了
	for _, pid := range []string{"5001", "5002", "5004"} {
		if n := count(t, s, `SELECT count(*) FROM channel_msgs WHERE platform_id = ?`, pid); n != 1 {
			t.Errorf("回声 %s 该映射到事件：%d", pid, n)
		}
	}
	for _, pt := range []struct{ plan, state, via string }{{"P1", PartConfirmed, "echo"}, {"P2", PartConfirmed, "echo"}, {"P3", PartConfirmed, "echo"}} {
		p := mustPlan(t, s, pt.plan)
		if p.Parts[0].State != pt.state || p.Parts[0].Via != pt.via || p.State != PlanDone {
			t.Errorf("%s：%+v", pt.plan, p)
		}
	}
}

// 不能认领的状态：待发（还没发过，回声不可能是它）、已确认、已中止、段失败
func TestClaimEchoIgnoresOtherStates(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	s.AppendPlan(sendPlan("P1", tConv, tAcct, "甲", "甲", "甲", "甲"))
	// 0 待发（sent_at 空）；1 已确认；2 已中止（发过一次失败、被切档中止，sent_at 有值）；3 段失败
	s.BeginSend("P1", 1, t0+1)
	s.EndPart("P1", 1, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7001"}, t0+2)
	s.BeginSend("P1", 2, t0+3)
	s.EndPart("P1", 2, []string{PartSending}, PartRetrying, PartEnd{Err: "没连上"}, t0+4)
	s.BeginSend("P1", 3, t0+5)
	s.EndPart("P1", 3, []string{PartSending}, PartFailed, PartEnd{Err: "拒绝"}, t0+6)
	if _, err := s.AbortPlanParts("P1", "切档", t0+7); err != nil { // 0 待发、2 等重试 → 已中止
		t.Fatal(err)
	}
	if got := states(mustPlan(t, s, "P1")); !reflect.DeepEqual(got, []string{PartAborted, PartConfirmed, PartAborted, PartFailed}) {
		t.Fatalf("准备的状态不对：%v", got)
	}
	_, _, found, err := s.ClaimEcho(EchoClaim{Channel: "qq", Account: tAcct, Conversation: tConv, PlatformID: "5001", Since: 0, Match: func(string) bool { return true }}, t0+8)
	if err != nil || found {
		t.Fatalf("待发、已确认、已中止、段失败的都不认领：found=%v err=%v", found, err)
	}
	if n := count(t, s, `SELECT count(*) FROM channel_msgs WHERE platform_id = '5001'`); n != 0 {
		t.Fatalf("没认领就不该映射：%d", n)
	}
}

// 启动和关闭时的清理：发送中 → 结果未知，待发和等重试 → 已中止，计划状态相应重算；已经结束的计划不碰
func TestSweep(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	s.AppendPlan(sendPlan("A", tConv, tAcct, "1", "2", "3")) // 1 发送中，2、3 待发
	s.AppendPlan(sendPlan("B", tConv, tAcct, "1", "2"))      // 全待发
	s.AppendPlan(sendPlan("C", tConv, tAcct, "1", "2"))      // 1 确认，2 等重试
	s.AppendPlan(sendPlan("D", tConv, tAcct, "1"))           // 已经完成
	s.AppendPlan(NewPlan{ID: "E", CharacterID: "ca", Conversation: tConv, Account: tAcct, Origin: "character", Kind: PlanKindBlocked, At: t0})
	s.BeginSend("A", 0, t0+1)
	s.BeginSend("C", 0, t0+2)
	s.EndPart("C", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7001"}, t0+3)
	s.BeginSend("C", 1, t0+4)
	s.EndPart("C", 1, []string{PartSending}, PartRetrying, PartEnd{Err: "没连上"}, t0+5)
	s.BeginSend("D", 0, t0+6)
	s.EndPart("D", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7002"}, t0+7)

	sw, err := s.Sweep("不知道发出去没有", "没发", t0+100)
	if err != nil {
		t.Fatal(err)
	}
	if sw != (Swept{Plans: 3, Unknown: 1, Aborted: 5}) {
		t.Fatalf("清理的数：%+v（A、B、C 三条在途，1 段发送中，A 的 2 段 + B 的 2 段 + C 的 1 段待发或等重试）", sw)
	}
	want := map[string]struct {
		state string
		parts []string
	}{
		"A": {PlanUnknown, []string{PartUnknown, PartAborted, PartAborted}},
		"B": {PlanAborted, []string{PartAborted, PartAborted}},
		"C": {PlanPartial, []string{PartConfirmed, PartAborted}},
		"D": {PlanDone, []string{PartConfirmed}},
		"E": {PlanKindBlocked, nil},
	}
	for id, w := range want {
		p := mustPlan(t, s, id)
		if p.State != w.state || !reflect.DeepEqual(states(p), w.parts) {
			t.Errorf("%s：状态 %q 段 %v，应该是 %q %v", id, p.State, states(p), w.state, w.parts)
		}
	}
	if pt := mustPlan(t, s, "A").Parts[0]; pt.Err != "不知道发出去没有" {
		t.Errorf("原因要记在段上：%q", pt.Err)
	}
	// 再清一次什么也没有
	if sw, _ := s.Sweep("x", "y", t0+200); sw != (Swept{}) {
		t.Fatalf("没有在途的了：%+v", sw)
	}
}

// SweepPlan 只清理指定的那一条：别的在途的计划原样不动
func TestSweepPlanOnlyTouchesThatPlan(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	s.AppendPlan(sendPlan("A", tConv, tAcct, "1", "2"))
	s.AppendPlan(sendPlan("B", tConv, tAcct, "1", "2"))
	s.BeginSend("A", 0, t0+1)
	s.BeginSend("B", 0, t0+2)
	sw, err := s.SweepPlan("A", "未知", "中止", t0+3)
	if err != nil || sw != (Swept{Plans: 1, Unknown: 1, Aborted: 1}) {
		t.Fatalf("%+v %v", sw, err)
	}
	if got := states(mustPlan(t, s, "A")); !reflect.DeepEqual(got, []string{PartUnknown, PartAborted}) {
		t.Fatalf("A：%v", got)
	}
	if p := mustPlan(t, s, "B"); p.State != PlanSending || !reflect.DeepEqual(states(p), []string{PartSending, PartPending}) {
		t.Fatalf("B 不该被动：%+v", p)
	}
}

// 这个平台消息号已经认识了（回包确认时记过映射，或者前面的回声认领过）就不认领，哪怕还有同文字、结果未知的段：
// 它是同一个回声的重复到达，或者是别的段的回声。号在别的会话里认识不算（平台消息号只在一个会话里唯一）
func TestClaimEchoSkipsKnownPlatformID(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	same := func(string) bool { return true }
	claim := func(conv, pid string) (string, bool) {
		t.Helper()
		id, _, found, err := s.ClaimEcho(EchoClaim{Channel: "qq", Account: tAcct, Conversation: conv, PlatformID: pid, Since: 0, Match: same}, t0+100)
		if err != nil {
			t.Fatal(err)
		}
		return id, found
	}
	const other = "qq:group:901"
	// P0 回包确认了（号 7001，映射已记）；P1、P2 同文字结果未知；P3 在另一个群里也是结果未知
	s.AppendPlan(sendPlan("P0", tConv, tAcct, "哈"))
	s.BeginSend("P0", 0, t0+1)
	s.EndPart("P0", 0, []string{PartSending}, PartConfirmed, PartEnd{PlatformID: "7001", Via: "reply"}, t0+2)
	for i, id := range []string{"P1", "P2"} {
		s.AppendPlan(sendPlan(id, tConv, tAcct, "哈"))
		s.BeginSend(id, 0, t0+int64(10+i))
		s.EndPart(id, 0, []string{PartSending}, PartUnknown, PartEnd{Err: "超时"}, t0+int64(20+i))
	}
	s.AppendPlan(sendPlan("P3", other, tAcct, "哈"))
	s.BeginSend("P3", 0, t0+30)
	s.EndPart("P3", 0, []string{PartSending}, PartUnknown, PartEnd{Err: "超时"}, t0+31)

	if id, found := claim(tConv, "7001"); found {
		t.Fatalf("7001 是 P0 回包确认过的号，不该拿去认领 %s", id)
	}
	if got := states(mustPlan(t, s, "P1")); !reflect.DeepEqual(got, []string{PartUnknown}) {
		t.Fatalf("P1 不该变：%v", got)
	}
	if n := count(t, s, `SELECT count(*) FROM channel_msgs WHERE platform_id = '7001'`); n != 1 {
		t.Fatalf("映射不增不减：%d", n)
	}

	// 7002 第一次到：认领最早的 P1；同一个 7002 再到一次：已认识，不认领 P2
	if id, found := claim(tConv, "7002"); !found || id != "P1" {
		t.Fatalf("7002 该认领 P1：%s %v", id, found)
	}
	if id, found := claim(tConv, "7002"); found {
		t.Fatalf("7002 重复到达，不该认领 %s", id)
	}
	if got := states(mustPlan(t, s, "P2")); !reflect.DeepEqual(got, []string{PartUnknown}) {
		t.Fatalf("P2 不该变：%v", got)
	}

	// 邻居：7001 在另一个群里不认识，那里的 P3 照常认领
	if id, found := claim(other, "7001"); !found || id != "P3" {
		t.Fatalf("别的群里的 7001 是另一条消息：%s %v", id, found)
	}
}

// 渠道是键的一部分，没填就报错，不悄悄跳过"号认识了没有"的检查
func TestClaimEchoRequiresChannel(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	s.AppendPlan(sendPlan("P1", tConv, tAcct, "哈"))
	s.BeginSend("P1", 0, t0+1)
	_, _, found, err := s.ClaimEcho(EchoClaim{Account: tAcct, Conversation: tConv, PlatformID: "7001", Since: 0, Match: func(string) bool { return true }}, t0+2)
	if err == nil || found {
		t.Fatalf("没填渠道该报错：found=%v err=%v", found, err)
	}
	if st := mustPlan(t, s, "P1").Parts[0].State; st != PartSending {
		t.Fatalf("报错就什么也不动：%s", st)
	}
}

// Score 给了就按分挑：高分的先认，0 分的不认，同分取发得最早的（出口靠它让重发段不被已放弃的原段抢走回声）
func TestClaimEchoScorePrefersHigher(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	for i, id := range []string{"原段", "重发段", "不对"} {
		if _, err := s.AppendPlan(sendPlan(id, tConv, tAcct, "编的正文")); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.BeginSend(id, 0, t0+int64(i)); err != nil || !ok {
			t.Fatalf("%s 没有开始发送：%v %v", id, ok, err)
		}
	}
	score := map[string]int{"原段": 1, "重发段": 2, "不对": 0}
	c := EchoClaim{Channel: "qq", Account: tAcct, Conversation: tConv, Since: 0, PlatformID: "7101",
		Match: func(string) bool { return true }, // Score 给了就不看 Match
		Score: func(pid string, _ int, _ string) int { return score[pid] }}
	for i, want := range []string{"重发段", "原段"} {
		c.PlatformID = []string{"7101", "7102"}[i]
		if id, _, found, err := s.ClaimEcho(c, t0+100); err != nil || !found || id != want {
			t.Fatalf("第 %d 个回声该认 %s：%q %v %v", i+1, want, id, found, err)
		}
	}
	c.PlatformID = "7103"
	if id, _, found, err := s.ClaimEcho(c, t0+100); err != nil || found {
		t.Fatalf("0 分的不认：%q %v %v", id, found, err)
	}
}
