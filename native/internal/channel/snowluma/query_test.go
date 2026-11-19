package snowluma

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 查询测试的号、群、地址都是编的。SnowLuma 的 get_msg 回包没有 self_id、post_type，这里照这个形状造

const (
	qMsgImage = `{"status":"ok","retcode":0,"data":{"message_type":"private","message_id":7001,"real_id":7001,"user_id":90003,"target_id":90001,"time":1761643200,"sender":{"user_id":90003,"nickname":"编的昵称"},` +
		`"message":[{"type":"text","data":{"text":"编的话"}},{"type":"image","data":{"file":"编的甲.jpg","url":"https://multimedia.nt.qq.com.cn/download?fileid=甲&rkey=新的rkey"}},` +
		`{"type":"image","data":{"file":"编的乙.png","url":"https://multimedia.nt.qq.com.cn/download?fileid=乙&rkey=新的rkey"}}]}}`
)

func getMsgs(e *sendEnv) []sentAction {
	var out []sentAction
	for _, a := range e.peer.rawActions() {
		if a.Action == "get_msg" {
			out = append(out, a)
		}
	}
	return out
}

func askMessage(e *sendEnv, conv types.Conversation, id string) (types.Inbound, error) {
	return e.ad.Message(context.Background(), sendAccount, conv, id)
}

func TestMessageTranslatesGetMsgReply(t *testing.T) {
	e := newSendEnv(t, func(act sentAction) (string, bool) {
		if act.Action != "get_msg" {
			return "", false
		}
		return qMsgImage, true
	})
	in, err := askMessage(e, privateConv(), "7001")
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != types.InboundMessage || in.Account != sendAccount || in.Conv != privateConv() || in.PlatformID != "7001" || in.From.ID != sendFriend {
		t.Fatalf("翻出来的不对：%+v", in)
	}
	if len(in.Segs) != 3 || in.Segs[1].Kind != types.SegImage || in.Segs[1].File != "编的甲.jpg" || !strings.Contains(in.Segs[1].URL, "rkey=新的rkey") || in.Segs[2].File != "编的乙.png" {
		t.Fatalf("段不对：%+v", in.Segs)
	}
	acts := getMsgs(e)
	if len(acts) != 1 || string(acts[0].Params["message_id"]) != "7001" {
		t.Fatalf("动作不对（message_id 要是 JSON 整数）：%+v", acts)
	}
	if len(e.peer.actions()) != 0 {
		t.Fatalf("查询不能发出任何消息动作：%+v", e.peer.actions())
	}
}

// 她自己发的（user_id 是她的号、target_id 是对方）：按自己发的翻，会话是和对方的私聊；群里的负数消息号照原样
func TestMessageSelfAndNegativeGroupID(t *testing.T) {
	e := newSendEnv(t, func(act sentAction) (string, bool) {
		if string(act.Params["message_id"]) == "-5001" {
			return `{"status":"ok","retcode":0,"data":{"message_type":"group","group_id":90002,"message_id":-5001,"user_id":90010,"time":1761643200,"sender":{"user_id":90010,"card":"编的群名片"},"message":[{"type":"text","data":{"text":"编的"}}]}}`, true
		}
		return `{"status":"ok","retcode":0,"data":{"message_type":"private","message_id":7002,"user_id":90001,"target_id":90003,"time":1761643200,"message":[{"type":"image","data":{"file":"自己的.jpg","url":"https://gchat.qpic.cn/x"}}]}}`, true
	})
	in, err := askMessage(e, privateConv(), "7002")
	if err != nil || in.Kind != types.InboundSelf || in.Conv != privateConv() || in.From.ID != sendAccount {
		t.Fatalf("自己发的：%+v %v", in, err)
	}
	e.clk.Advance(queryGap)
	in, err = askMessage(e, groupConv(), "-5001")
	if err != nil || in.Conv != groupConv() || in.PlatformID != "-5001" || in.From.ID != "90010" {
		t.Fatalf("群里的负数消息号：%+v %v", in, err)
	}
	if got := string(getMsgs(e)[1].Params["message_id"]); got != "-5001" {
		t.Fatalf("负数消息号要原样当整数发：%s", got)
	}
}

// 回来的东西对不上请求的：会话不同、消息号不同、帧自带的号和配置的不一致、协议端拒绝、回包不是对象，都是错误，不返回半条
func TestMessageRejectsMismatchedReplies(t *testing.T) {
	bodies := map[string]string{
		"换了会话":    `{"status":"ok","retcode":0,"data":{"message_type":"group","group_id":99999,"message_id":7001,"user_id":90003,"time":1761643200,"message":[]}}`,
		"换了消息号":   `{"status":"ok","retcode":0,"data":{"message_type":"private","message_id":8888,"user_id":90003,"target_id":90001,"time":1761643200,"message":[]}}`,
		"别人的号":    `{"status":"ok","retcode":0,"data":{"self_id":12345,"message_type":"private","message_id":7001,"user_id":90003,"time":1761643200,"message":[]}}`,
		"没有消息号":   `{"status":"ok","retcode":0,"data":{"message_type":"private","user_id":90003,"time":1761643200,"message":[]}}`,
		"协议端拒绝":   `{"status":"failed","retcode":100,"data":null,"wording":"message not found"}`,
		"retcode": `{"status":"ok","retcode":1400,"data":{}}`,
		"没有数据":    `{"status":"ok","retcode":0,"data":null}`,
		"数据是数组":   `{"status":"ok","retcode":0,"data":[1,2]}`,
		"对方是我":    `{"status":"ok","retcode":0,"data":{"message_type":"private","message_id":7001,"user_id":90001,"target_id":90001,"time":1761643200,"message":[]}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			e := newSendEnv(t, func(act sentAction) (string, bool) { return body, act.Action == "get_msg" })
			in, err := askMessage(e, privateConv(), "7001")
			if err == nil {
				t.Fatalf("不该成功：%+v", in)
			}
			// 协议端答了、只是答案不能用：不是"没拿到回包"，调用方不能把它当成暂时问不到而反复再问
			if errors.Is(err, ErrNoReply) {
				t.Fatalf("协议端回了包，不该算没拿到回包：%v", err)
			}
		})
	}
}

// 连接没起来就问：是"没拿到回包"（调用方过一阵再问），不发任何动作
func TestMessageNotConnectedIsNoReply(t *testing.T) {
	clk := fake.NewClock(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	ad, err := NewWS(Config{URL: "ws://127.0.0.1:1", Token: sendToken, Account: sendAccount}, clk, newTestAlerts())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testPatience)
	defer cancel()
	// 连问两次，假钟不动：没发出去的请求不占查询间隔，第二次也立刻得到同样的答案，不在间隔上等
	for i := 0; i < 2; i++ {
		_, err = ad.Message(ctx, sendAccount, privateConv(), "7001")
		if !errors.Is(err, ErrNoReply) || !strings.Contains(err.Error(), "没连上") {
			t.Fatalf("第 %d 次：要报没连上，并且是“没拿到回包”：%v", i+1, err)
		}
	}
}

// 不合法的请求不发任何动作：号不是十进制整数、是 0、号不是这条连接的、会话不合法或不是 QQ 的
func TestMessageRefusesBadRequestsWithoutSending(t *testing.T) {
	e := newSendEnv(t, func(sentAction) (string, bool) { return qMsgImage, true })
	for _, id := range []string{"", "abc", "0", "-0", "1.5", "１２", "7001 ", "9223372036854775808"} {
		if _, err := askMessage(e, privateConv(), id); err == nil {
			t.Errorf("消息号 %q 不该通过", id)
		}
	}
	if _, err := e.ad.Message(context.Background(), "12345", privateConv(), "7001"); err == nil {
		t.Error("别的号不该通过")
	}
	bad := privateConv()
	bad.Self = ""
	if _, err := askMessage(e, bad, "7001"); err == nil {
		t.Error("不合法的会话不该通过")
	}
	other := groupConv()
	other.Channel = "telegram"
	if _, err := askMessage(e, other, "7001"); err == nil {
		t.Error("别的渠道的会话不该通过")
	}
	if n := len(getMsgs(e)); n != 0 {
		t.Fatalf("不合法的请求发出去了 %d 次", n)
	}
}

// 没回包：到 queryTimeout 才放弃（不是发送的 10 秒），之后协议端迟到的回包不影响下一次
func TestMessageTimeoutIsLongerThanSendTimeout(t *testing.T) {
	e := newSendEnv(t, func(sentAction) (string, bool) { return "", false })
	done := make(chan error, 1)
	go func() { _, err := askMessage(e, privateConv(), "7001"); done <- err }()
	waitFor(t, func() bool { return len(getMsgs(e)) == 1 })
	e.clk.Advance(callTimeout + time.Second)
	select {
	case err := <-done:
		t.Fatalf("发送的时限到了就放弃了，查询要等更久：%v", err)
	case <-time.After(100 * time.Millisecond):
	}
	e.clk.Advance(queryTimeout)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "超时") || !errors.Is(err, ErrNoReply) {
			t.Fatalf("要报超时，并且是“没拿到回包”：%v", err)
		}
	case <-time.After(testPatience):
		t.Fatal("到时限没有放弃")
	}
}

// 同一时刻只放一个查询在途，和上一次结束至少隔 queryGap
func TestMessageSerialAndSpaced(t *testing.T) {
	e := newSendEnv(t, func(act sentAction) (string, bool) {
		if act.Action != "get_msg" || string(act.Params["message_id"]) == "7001" {
			return "", false // 第一个查询不回，占着
		}
		return `{"status":"ok","retcode":0,"data":{"message_type":"private","message_id":7002,"user_id":90003,"target_id":90001,"time":1761643200,"message":[]}}`, true
	})
	errA := make(chan error, 1)
	go func() { _, err := askMessage(e, privateConv(), "7001"); errA <- err }()
	waitFor(t, func() bool { return len(getMsgs(e)) == 1 })
	type result struct {
		in  types.Inbound
		err error
	}
	resB := make(chan result, 1)
	go func() { in, err := askMessage(e, privateConv(), "7002"); resB <- result{in, err} }()
	time.Sleep(100 * time.Millisecond)
	if n := len(getMsgs(e)); n != 1 {
		t.Fatalf("A 还没结束，B 就发出去了（在途 %d 个）", n)
	}
	e.clk.Advance(queryTimeout) // A 超时放弃
	if err := <-errA; err == nil {
		t.Fatal("A 没回包，应该超时")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(getMsgs(e)); n != 1 {
		t.Fatalf("A 刚结束，间隔没到，B 就发出去了（共 %d 个）", n)
	}
	e.clk.Advance(queryGap)
	select {
	case r := <-resB:
		if r.err != nil || r.in.PlatformID != "7002" {
			t.Fatalf("B：%+v %v", r.in, r.err)
		}
	case <-time.After(testPatience):
		t.Fatal("间隔过了 B 还没发出去")
	}
	if n := len(getMsgs(e)); n != 2 {
		t.Fatalf("共应该发 2 个，发了 %d", n)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(testPatience)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等不到")
}
