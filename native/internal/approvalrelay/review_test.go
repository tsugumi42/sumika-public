package approvalrelay

// 独立复核补的回归测试：结果未知不留墓碑、写库失败说真话、发卡途中回复、平台时间、重连窗口、参数截短。
// 号、角色、命令都是编的。

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/approval"
)

var cardIDRe = regexp.MustCompile(`待批准 (\d{4})`)

func cardIDFrom(t *testing.T, f *fixture) string {
	t.Helper()
	for _, s := range f.sentTexts() {
		if m := cardIDRe.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	t.Fatal("没找到发出的卡")
	return ""
}

func (f *fixture) setDelivery(confirm, attempted bool) {
	f.mu.Lock()
	f.confirm, f.attempted = confirm, attempted
	f.mu.Unlock()
}

// A 的卡发送结果未知（可能已到手机）：之后 B 开票，主人回裸“同意”（本意是 A）不能批 B；回 A 的编号得到“没有这一单”。
func TestUnknownDeliveryLeavesTombstone(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	f.setDelivery(false, true)
	chA := f.ask("reqA", "echo 甲")
	f.advance(61 * time.Second)
	f.r.Step(context.Background())
	f.settleQuiet()
	idA := cardIDFrom(t, f)
	if f.r.OpenCount() != 0 {
		t.Fatal("没确认送达不能开票")
	}
	f.setDelivery(true, true)
	idB, chB := f.open("reqB", "echo 乙")
	f.advance(5 * time.Second)

	if !f.r.Control(inbound("同意")) {
		t.Fatal("A 的卡可能已在手机上：裸同意要被截走并要求带号")
	}
	mustPending(t, f, chB)
	mustPending(t, f, chA)
	if !f.r.Control(inbound("同意 " + idA)) {
		t.Fatal("A 的编号要有墓碑回话")
	}
	f.settleQuiet()
	if got := f.lastSent(); !strings.Contains(got, idA) || !strings.Contains(got, "没确认送达") {
		t.Fatalf("要说明这一单没确认送达：%q", got)
	}
	mustPending(t, f, chB)
	if !f.r.Control(inbound("同意 " + idB)) {
		t.Fatal("B 用自己的号")
	}
	mustDecide(t, f, chB, "approved")
}

// 明确一段都没发出去的失败：不留墓碑，之后的裸“同意”仍是普通聊天（本机的卡照常等）。
func TestNothingSentLeavesNoTombstone(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	f.setDelivery(false, false)
	f.ask("reqA", "echo 甲")
	f.advance(61 * time.Second)
	f.r.Step(context.Background())
	f.settleQuiet()
	if f.r.Control(inbound("同意")) {
		t.Fatal("什么都没发出去，同意只是普通聊天")
	}
}

// 回复被接受后写库失败：QQ 不能出现“已批准”，要说没生效。
func TestPersistFailureNotReportedAsApproved(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	f.ap.Persist = func(_ approval.Request, d *approval.Decision) error {
		if d != nil {
			return errors.New("磁盘满")
		}
		return nil
	}
	id, ch := f.open("req1", "rm 编的")
	f.advance(5 * time.Second)
	if !f.r.Control(inbound("同意 " + id)) {
		t.Fatal("应被接受")
	}
	mustDecide(t, f, ch, "denied")
	waitUntil(t, "没生效的说明", func() bool { f.settleQuiet(); return strings.Contains(f.lastSent(), "没能生效") })
	for _, s := range f.sentTexts() {
		if strings.Contains(s, "已批准") {
			t.Fatalf("写库失败时不能说已批准：%q", s)
		}
	}
	if f.r.OpenCount() != 0 {
		t.Fatal("结算完票要收掉")
	}
}

// 正常批准：确认话在结局回调里说，仍是“已批准”。
func TestApprovedConfirmationComesFromResolution(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo 编的")
	f.advance(5 * time.Second)
	f.r.Control(inbound("同意 " + id))
	mustDecide(t, f, ch, "approved")
	waitUntil(t, "已批准确认话", func() bool { f.settleQuiet(); return strings.Contains(f.lastSent(), "已批准") })
	if f.r.OpenCount() != 0 {
		t.Fatal("结算完票要收掉")
	}
}

// 发卡途中（第一段已到手机、后面的段还在发）回编号：不进模型、不结算，告诉主人发完再回。
func TestReplyWhileSendingIsHeld(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	ch := f.ask("req1", "echo 编的")
	f.advance(61 * time.Second)
	var intercepted bool
	once := false
	f.sendHook = func() {
		if once {
			return
		}
		once = true
		f.r.mu.Lock()
		var id string
		for k := range f.r.byID {
			id = k
		}
		f.r.mu.Unlock()
		intercepted = f.r.Control(inbound("同意 " + id))
	}
	f.r.Step(context.Background())
	if !intercepted {
		t.Fatal("发卡途中带编号的回复要被截走，不能进模型")
	}
	mustPending(t, f, ch)
	// 提示是异步发的：等它真的记进 sent，再按调用顺序看它在卡之后；不能只看最后一条（发完之前提示可能先返回）。
	waitUntil(t, "“还在发”的提示已发出", func() bool {
		for _, s := range f.sentTexts() {
			if strings.Contains(s, "还在发") {
				return true
			}
		}
		return false
	})
	f.settleQuiet()
	card, hint := -1, -1
	for i, s := range f.sentTexts() {
		if cardIDRe.MatchString(s) {
			card = i
		}
		if strings.Contains(s, "还在发") {
			hint = i
		}
	}
	if card < 0 {
		t.Fatal("卡没发出去")
	}
	if hint <= card {
		t.Fatalf("“还在发”的提示要排在卡之后，实际第 %d 条（卡是第 %d 条）：%q", hint, card, f.sentTexts())
	}
}

// 平台时间比这张卡开始发送还早（断线期间说的话重连后才推来）：裸“同意”不结算，只提示带号。
func TestStalePlatformTimeBareReplyNeedsID(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	_, ch := f.open("req1", "echo 编的")
	f.advance(5 * time.Second)
	old := inbound("同意")
	old.Time = f.now().Add(-30 * time.Second)
	if !f.r.Control(old) {
		t.Fatal("要提示带号")
	}
	mustPending(t, f, ch)
	f.settleQuiet()
	if !strings.Contains(f.lastSent(), "编号") {
		t.Fatalf("要提示带号：%q", f.lastSent())
	}
	fresh := inbound("同意")
	fresh.Time = f.now()
	if !f.r.Control(fresh) {
		t.Fatal("发卡之后说的裸同意仍能批")
	}
	mustDecide(t, f, ch, "approved")
}

// 协议端重连后一段时间里，裸“同意”要带号。
func TestReconnectOpensAmbiguityWindow(t *testing.T) {
	f := newFixture(t)
	f.pastStartup()
	id, ch := f.open("req1", "echo 编的")
	f.advance(5 * time.Second)
	f.r.NoteReconnect()
	f.r.Control(inbound("同意"))
	mustPending(t, f, ch)
	if !f.r.Control(inbound("同意 " + id)) {
		t.Fatal("带号仍能批")
	}
	mustDecide(t, f, ch, "approved")
}

// 参数被截短的卡不开票。
func TestRenderCardRefusesTruncatedSubject(t *testing.T) {
	_, why := RenderCard(approval.Request{ID: "r", Tool: "some_mcp_tool", Title: "运行工具", Subject: "前半段", SubjectTruncated: true}, "1234")
	if why == "" {
		t.Fatal("Subject 被截短的卡不能在 QQ 批")
	}
}

type silentApprover struct{ onReq func(approval.Request) }

func (s *silentApprover) Subscribe(onRequest func(approval.Request), _ func(approval.Request, approval.Decision)) {
	s.onReq = onRequest
}
func (s *silentApprover) DecideVia(string, string, string) (bool, error) { return true, nil }

// 回复被接受但结局回调一直不来：巡检在 30 秒后告诉主人“不知道有没有生效”，并收票，不永远挂着。
func TestDecidingWithoutResolutionTimesOut(t *testing.T) {
	var clock = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var sends []string
	ap := &silentApprover{}
	r, err := New(Deps{
		Approver: ap,
		Binding: func(string) (Binding, bool) {
			return Binding{Conv: tConv, Character: tChar, Owner: tOwner, Account: tAccount}, true
		},
		Eligible: func(approval.Request) bool { return true },
		Send: func(_ context.Context, _ Binding, texts []string) (Delivery, error) {
			sends = append(sends, strings.Join(texts, "\n"))
			return Delivery{Confirmed: true, Attempted: true, MessageIDs: []string{"m1"}}, nil
		},
		Now:  func() time.Time { return clock },
		Tick: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	req := approval.Request{ID: "req1", CharacterID: tChar, Tool: "powershell", Title: "t", Subject: "echo", CreatedAt: clock.UnixMilli(), ExpiresAt: clock.Add(time.Hour).UnixMilli()}
	ap.onReq(req)
	clock = clock.Add(11 * time.Minute)
	r.Step(context.Background())
	clock = clock.Add(10 * time.Second)
	id := ""
	for k := range r.byID {
		id = k
	}
	if !r.Control(inbound("同意 " + id)) {
		t.Fatal("应被接受")
	}
	r.Idle()
	clock = clock.Add(decideLimit + time.Second)
	r.Step(context.Background())
	r.Idle()
	if got := sends[len(sends)-1]; !strings.Contains(got, "不知道有没有生效") {
		t.Fatalf("要说明不知道有没有生效：%q", got)
	}
	if r.OpenCount() != 0 {
		t.Fatal("超时后票要收掉")
	}
}
