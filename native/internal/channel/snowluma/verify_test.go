package snowluma

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 登录号核验。号、群号、句子都是编的。心跳帧的 self_id 用配置的号，用来证明不能拿帧里的号冒充核验结果。
func heartbeatFrame() string {
	return `{"post_type":"meta_event","meta_event_type":"heartbeat","time":1761643200,"self_id":` + sendAccount + `,"interval":30000}`
}

func textPart() []types.Part {
	return []types.Part{types.Message(sendText)}
}

func confirmReply() replyFunc {
	return fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-7654321}}`)
}

func (e *sendEnv) waitNamed(name string, n int) {
	e.t.Helper()
	deadline := time.Now().Add(testPatience)
	for time.Now().Before(deadline) {
		if e.peer.count(name) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	fail(e.t, "5 秒内 %s 只有 %d 次，要 %d 次", name, e.peer.count(name), n)
}

func (e *sendEnv) waitSending() {
	e.t.Helper()
	deadline := time.Now().Add(testPatience)
	for time.Now().Before(deadline) {
		if e.ad.sending() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	fail(e.t, "核验没有通过")
}

func (p *actionPeer) waitConns(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(testPatience)
	for time.Now().Before(deadline) {
		if p.nConns() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	fail(t, "连接数 %d，要 %d", p.nConns(), n)
}

func assertNoLeak(t *testing.T, logs string, extra ...string) {
	t.Helper()
	bad := append([]string{sendToken, sendText, sendAccount, "90009", "90008"}, extra...)
	for _, s := range bad {
		if strings.Contains(logs, s) {
			t.Fatalf("日志里有 %s：%s", s, logs)
		}
	}
}

func TestLoginMismatchBlocksSendUntilMatch(t *testing.T) {
	e := newSendEnvSetup(t, confirmReply(), func(p *actionPeer) {
		// 心跳 self_id 仍是 90001。登录号回 90009，不许拿帧里的号对上。
		p.setLoginQ("90009", "90009", "90001")
	})
	recvStatus(t, e.sink, types.StatusDisconnected)
	x := e.alerts.waitRaises(t, 1)
	if x.Key != "qq:"+sendAccount+":login" || x.Title != "协议端登录的号和配置不一致" || x.Body != "对不上" || x.Detail != "" {
		t.Fatalf("提醒：%#v", x)
	}
	if strings.Contains(x.Title+x.Body+x.Detail, "90009") || strings.Contains(x.Title+x.Body+x.Detail, sendAccount) {
		t.Fatalf("提醒正文里有号：%#v", x)
	}
	login := e.peer.rawActions()
	if len(login) == 0 || login[0].Action != "get_login_info" || !echoRe.MatchString(login[0].Echo) || !strings.Contains(login[0].Raw, `"params":{}`) {
		t.Fatalf("登录查询：%+v", login)
	}
	if e.peer.count("get_status") != 0 {
		t.Fatalf("号不对还去查了状态 %d 次", e.peer.count("get_status"))
	}
	r, err := e.ad.Send(context.Background(), sendAccount, types.Part{
		ID: "p-block", Conv: groupConv(), Kind: types.PartMessage, Body: types.TextBody(sendText),
	})
	if err != nil || r.Status != types.ReceiptUnknown || r.Err != "登录号和配置不一致" || r.Validate() != nil {
		t.Fatalf("回执：%#v err=%v", r, err)
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("号不对仍发出 %d 次 send_*", n)
	}

	e.clk.WaitTimer(t, 60*time.Second)
	e.clk.Advance(60 * time.Second)
	e.peer.waitConns(t, 2)
	recvStatus(t, e.sink, types.StatusDisconnected)
	if raises, _ := e.alerts.counts(); raises != 1 {
		t.Fatalf("第二次仍不对，Raise 了 %d 次", raises)
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("第二次仍发出 %d 次 send_*", n)
	}

	e.clk.WaitTimer(t, 60*time.Second)
	e.clk.Advance(60 * time.Second)
	e.peer.waitConns(t, 3)
	e.waitSending()
	key, _ := e.alerts.waitClears(t, 1)
	if key != "qq:"+sendAccount+":login" {
		t.Fatalf("Clear 的键：%s", key)
	}
	id2 := e.sendParts(groupConv(), textPart())
	e.waitPlan(id2, store.PlanDone)
	acts := e.peer.waitActions(t, 1)
	if acts[0].Action != "send_group_msg" || !echoRe.MatchString(acts[0].Echo) {
		t.Fatalf("对上以后的发送：%+v", acts[0])
	}
	assertNoLeak(t, e.logsText())
}

func TestStatusMissingGoodDoesNotSend(t *testing.T) {
	e := newSendEnvSetup(t, confirmReply(), func(p *actionPeer) {
		p.setStatusQ(`{"online":true}`)
	})
	e.waitNamed("get_status", 1)
	if e.ad.sending() {
		t.Fatal("缺 good 却可以发")
	}
	x := e.alerts.waitRaises(t, 1)
	if x.Key != "qq:"+sendAccount+":status" || x.Title != "协议端报收发不健康" || x.Detail != "" {
		t.Fatalf("提醒：%#v", x)
	}
	if strings.Contains(x.Title+x.Body+x.Detail, sendAccount) {
		t.Fatalf("提醒里有号：%#v", x)
	}
	id := e.sendParts(groupConv(), textPart())
	part := e.waitPart(id, store.PartUnknown, 1)
	if part.Err != "协议端报收发不健康" {
		t.Fatalf("段错误：%q", part.Err)
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("缺 good 仍发出 %d 次", n)
	}
	if e.peer.nConns() != 1 {
		t.Fatalf("缺 good 不该拆连接，连接数 %d", e.peer.nConns())
	}
	assertNoLeak(t, e.logsText())
}

func TestUnhealthyStatusPollsUntilGood(t *testing.T) {
	e := newSendEnvSetup(t, confirmReply(), func(p *actionPeer) {
		p.setStatusQ(`{"online":true,"good":false}`)
	})
	e.waitNamed("get_status", 1)
	if e.ad.sending() {
		t.Fatal("good 为 false 却可以发")
	}
	x := e.alerts.waitRaises(t, 1)
	if x.Key != "qq:"+sendAccount+":status" || x.Title != "协议端报收发不健康" || x.Detail != "" {
		t.Fatalf("提醒：%#v", x)
	}
	r, err := e.ad.Send(context.Background(), sendAccount, types.Part{
		ID: "p-block", Conv: groupConv(), Kind: types.PartMessage, Body: types.TextBody(sendText),
	})
	if err != nil || r.Status != types.ReceiptUnknown || r.Err != "协议端报收发不健康" || r.Validate() != nil {
		t.Fatalf("回执：%#v err=%v", r, err)
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("不健康仍发出 %d 次", n)
	}
	recvStatus(t, e.sink, types.StatusHeartbeat) // 连上时协议端自己发的那一拍，先取走
	if _, clears := e.alerts.counts(); clears != 0 {
		t.Fatalf("初次不健康心跳后 Clear=%d，要 0", clears)
	}
	e.peer.send(t, heartbeatFrame())
	recvStatus(t, e.sink, types.StatusHeartbeat)
	if e.peer.nConns() != 1 || e.ad.sending() {
		t.Fatalf("心跳不该当成恢复，sending=%v conns=%d", e.ad.sending(), e.peer.nConns())
	}
	if _, clears := e.alerts.counts(); clears != 0 {
		t.Fatalf("不健康心跳处理后 Clear=%d，要 0", clears)
	}

	e.clk.WaitTimer(t, 30*time.Second)
	e.clk.Advance(30 * time.Second)
	e.waitNamed("get_status", 2)
	// 计时器在不健康回包处理以后重建，不能只等假端收到查询。
	e.clk.WaitTimer(t, 30*time.Second)
	if e.ad.sending() || e.peer.nConns() != 1 || len(e.peer.actions()) != 0 {
		t.Fatalf("30 秒后仍不该发，sending=%v conns=%d sends=%d", e.ad.sending(), e.peer.nConns(), len(e.peer.actions()))
	}
	if raises, clears := e.alerts.counts(); raises != 1 || clears != 0 {
		t.Fatalf("30 秒仍不健康，Raise=%d Clear=%d，要 1/0", raises, clears)
	}

	e.peer.send(t, heartbeatFrame())
	recvStatus(t, e.sink, types.StatusHeartbeat)
	if _, clears := e.alerts.counts(); clears != 0 {
		t.Fatalf("恢复前最后一拍心跳后 Clear=%d，要 0", clears)
	}
	e.peer.setStatusQ(`{"online":true,"good":true}`)
	e.clk.WaitTimer(t, 30*time.Second)
	e.clk.Advance(30 * time.Second)
	e.waitSending()
	key, _ := e.alerts.waitClears(t, 1)
	if key != "qq:"+sendAccount+":status" {
		t.Fatalf("Clear 的键：%s", key)
	}
	if _, clears := e.alerts.counts(); clears != 1 {
		t.Fatalf("恢复以后 Clear=%d，要恰好 1", clears)
	}
	id2 := e.sendParts(groupConv(), textPart())
	e.waitPlan(id2, store.PlanDone)
	acts := e.peer.waitActions(t, 1)
	if acts[0].Action != "send_group_msg" || !echoRe.MatchString(acts[0].Echo) {
		t.Fatalf("恢复后的发送：%+v", acts[0])
	}
	assertNoLeak(t, e.logsText())
}

func TestLoginTimeoutIsNotAMatch(t *testing.T) {
	e := newSendEnvSetup(t, confirmReply(), func(p *actionPeer) {
		p.metaMu.Lock()
		p.loginSilence = true
		p.metaMu.Unlock()
	})
	e.waitNamed("get_login_info", 1)
	e.clk.WaitTimer(t, callTimeout)
	e.clk.Advance(callTimeout)
	recvStatus(t, e.sink, types.StatusDisconnected)
	if e.ad.sending() {
		t.Fatal("核验超时当成号对了")
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("超时以后发出 %d 次", n)
	}
	if raises, _ := e.alerts.counts(); raises != 0 {
		t.Fatalf("超时不该报登录号不一致，Raise %d 次", raises)
	}
	// 查询失败走原来的退避，不是号不对的 60 秒。
	e.clk.WaitTimer(t, time.Second)
	e.clk.Advance(time.Second)
	e.peer.waitConns(t, 2)
	assertNoLeak(t, e.logsText())
}

func TestLoginRetcodeMissingIsNotAMatch(t *testing.T) {
	e := newSendEnvSetup(t, confirmReply(), func(p *actionPeer) {
		p.setLoginBody(`{"status":"ok","data":{"user_id":90001}}`)
	})
	recvStatus(t, e.sink, types.StatusDisconnected)
	if e.ad.sending() {
		t.Fatal("缺 retcode 当成核验过了")
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("缺 retcode 仍发出 %d 次", n)
	}
	if raises, _ := e.alerts.counts(); raises != 0 {
		t.Fatalf("缺 retcode 不该报登录号不一致，Raise %d 次", raises)
	}
	assertNoLeak(t, e.logsText())
}

func TestSelfIDMismatchStopsSend(t *testing.T) {
	e := newSendEnv(t, confirmReply())
	e.waitSending()
	e.peer.send(t, `{"post_type":"message","message_type":"private","time":1761643200,"self_id":90008,"user_id":90003,"message_id":"m-self","sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":{"text":"旁路"}}]}`)
	in := recvInbound(t, e.sink)
	if in.Account != "90008" {
		t.Fatalf("帧里的号被改成配置的号了：%q", in.Account)
	}
	if e.ad.sending() {
		t.Fatal("self_id 和登录号不一致仍可发")
	}
	id := e.sendParts(groupConv(), textPart())
	part := e.waitPart(id, store.PartUnknown, 1)
	if part.Err != "帧里的号和登录号不一致" {
		t.Fatalf("段错误：%q", part.Err)
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("仍发出 %d 次", n)
	}
	if e.peer.nConns() != 1 {
		t.Fatalf("self_id 不一致不该拆连接，连接数 %d", e.peer.nConns())
	}
	logs := e.logsText()
	if !strings.Contains(logs, "ERROR 帧里的 self_id 和登录号不一致，停发") { // 停发要人处理：ERROR，正文不带 `snowluma:` 前缀
		t.Fatalf("没有停发日志：%s", logs)
	}
	if strings.Contains(logs, "snowluma:") {
		t.Fatalf("模块名在行首标签里，正文不再自带前缀：%s", logs)
	}
	if !strings.Contains(logs, "帧里的 self_id 和登录号不一致") {
		t.Fatalf("没有停发日志：%s", logs)
	}
	assertNoLeak(t, logs)
}
