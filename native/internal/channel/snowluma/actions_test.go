package snowluma

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

func TestRecordedActions(t *testing.T) {
	// 群引用、群转发尚无成功真帧，群回包形状按私聊真帧推断；这里只验证假端契约。
	for _, conv := range []types.Conversation{privateConv(), groupConv()} {
		for _, kind := range []string{types.PartMessage, types.PartForward} {
			for _, status := range []string{types.ReceiptConfirmed, types.ReceiptFailed, types.ReceiptUnknown} {
				t.Run(conv.Scene+"/"+kind+"/"+status, func(t *testing.T) {
					e := newSendEnv(t, func(sentAction) (string, bool) {
						switch status {
						case types.ReceiptConfirmed:
							return `{"status":"ok","retcode":0,"data":{"message_id":-90010,"res_id":"编的资源","forward_id":"编的资源"}}`, true
						case types.ReceiptFailed:
							return `{"status":"failed","retcode":1400,"data":null,"wording":"编的协议端详情"}`, true
						default:
							return "", false
						}
					})
					p := types.Part{ID: "编的计划:0", Conv: conv, Kind: kind, Body: types.TextBody("编的正文"), Quote: "-90004"}
					if kind == types.PartForward {
						p.Body, p.Quote = nil, ""
						p.Nodes = []types.ForwardNode{{Name: "外部内容·纸鸢", Body: types.TextBody("第一段")}, {Name: "外部内容·石榴", Body: types.TextBody("第二段")}}
					}
					type result struct {
						r   types.Receipt
						err error
					}
					done := make(chan result, 1)
					go func() { r, err := e.ad.Send(context.Background(), sendAccount, p); done <- result{r, err} }()
					act := e.peer.waitActions(t, 1)[0]
					if status == types.ReceiptUnknown {
						e.clk.Advance(callTimeout)
					}
					select {
					case got := <-done:
						if got.err != nil || got.r.Status != status || got.r.PartID != p.ID {
							t.Fatalf("回执：%+v err=%v", got.r, got.err)
						}
						wantID := ""
						if status == types.ReceiptConfirmed {
							wantID = "-90010"
						}
						if got.r.PlatformID != wantID || got.r.Err == "编的协议端详情" {
							t.Fatalf("消息号或错误正文：%+v", got.r)
						}
					case <-time.After(testPatience):
						t.Fatal("动作没有在取消等待后返回")
					}
					key, id, prefix := "user_id", sendFriend, "send_private"
					if conv.Scene == types.SceneGroup {
						key, id, prefix = "group_id", sendGroup, "send_group"
					}
					suffix := "_msg"
					if kind == types.PartForward {
						suffix = "_forward_msg"
					}
					if act.Action != prefix+suffix || string(act.Params[key]) != id {
						t.Fatalf("动作或会话：%s", act.Raw)
					}
					if kind == types.PartMessage {
						var msg []map[string]json.RawMessage
						if err := json.Unmarshal(act.Params["message"], &msg); err != nil || len(msg) != 2 {
							t.Fatalf("引用段：%s %v", act.Raw, err)
						}
						if string(msg[0]["type"]) != `"reply"` || string(msg[0]["data"]) != `{"id":"-90004"}` || string(msg[1]["type"]) != `"text"` || string(msg[1]["data"]) != `{"text":"编的正文"}` {
							t.Fatalf("引用号必须原样字符串且在文字前：%s", act.Raw)
						}
					} else {
						var nodes []struct {
							Type string
							Data struct {
								Name    string
								Uin     string
								Content []struct {
									Type string
									Data struct{ Text string }
								}
							}
						}
						if err := json.Unmarshal(act.Params["messages"], &nodes); err != nil || len(nodes) != 2 {
							t.Fatalf("转发节点：%s %v", act.Raw, err)
						}
						for i, node := range nodes {
							if node.Type != "node" || node.Data.Name != p.Nodes[i].Name || node.Data.Uin != sendAccount || len(node.Data.Content) != 1 || node.Data.Content[0].Type != "text" || node.Data.Content[0].Data.Text != types.BodyTextOf(p.Nodes[i].Body) {
								t.Fatalf("第 %d 节点：%s", i, act.Raw)
							}
						}
					}
					if len(e.peer.actions()) != 1 {
						t.Fatalf("一次调用发送 %d 次", len(e.peer.actions()))
					}
				})
			}
		}
	}
}

// 群回应照 2026-10-30 在主人选的群里录到的真帧：成功 ok/0、data null；消息号不存在 failed/100；不回包是未知。私聊一律不发。
func TestReaction(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"status":"ok","retcode":0,"data":null}`, types.ReceiptConfirmed},
		{`{"status":"failed","retcode":100,"data":null,"wording":"message not found"}`, types.ReceiptFailed},
		{"", types.ReceiptUnknown},
	}
	for _, c := range cases {
		e := newSendEnv(t, func(sentAction) (string, bool) { return c.body, c.body != "" })
		if !e.ad.Caps().ReactGroup {
			t.Fatal("群回应应该打开")
		}
		p := types.Part{ID: "编的计划:0", Conv: privateConv(), Kind: types.PartReact, Ref: "-90004", EmojiID: "76"}
		if _, err := e.ad.Send(context.Background(), sendAccount, p); !errors.Is(err, types.ErrUnsupported) || len(e.peer.actions()) != 0 {
			t.Fatalf("私聊回应没拒：%v，发了 %d 次", err, len(e.peer.actions()))
		}
		p.Conv = groupConv()
		type result struct {
			r   types.Receipt
			err error
		}
		done := make(chan result, 1)
		go func() { r, err := e.ad.Send(context.Background(), sendAccount, p); done <- result{r, err} }()
		e.peer.waitActions(t, 1)
		if c.body == "" {
			e.clk.Advance(callTimeout)
		}
		var got result
		select {
		case got = <-done:
		case <-time.After(testPatience):
			t.Fatal("回应没有返回")
		}
		if got.err != nil || got.r.Status != c.want || got.r.PlatformID != "" {
			t.Fatalf("回包 %q：%+v %v，要 %s 且没有平台号", c.body, got.r, got.err, c.want)
		}
		acts := e.peer.actions()
		if len(acts) != 1 || acts[0].Action != "set_msg_emoji_like" || string(acts[0].Params["message_id"]) != "-90004" || string(acts[0].Params["emoji_id"]) != `"76"` {
			t.Fatalf("回应帧不对：%+v", acts)
		}
	}
}

func TestActionInvalidParameters(t *testing.T) {
	e := newSendEnv(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-90010}}`))
	for _, id := range []string{"0", "-0", "1.5", " 90004", "1e3", "+90004", "90004x"} {
		p := types.Part{ID: "编的计划:0", Conv: privateConv(), Kind: types.PartMessage, Body: types.TextBody("编的正文"), Quote: id}
		r, err := e.ad.Send(context.Background(), sendAccount, p)
		if err != nil || r.Status != types.ReceiptFailed {
			t.Fatalf("引用号 %q：%+v %v", id, r, err)
		}
	}
	for _, p := range []types.Part{
		{Kind: types.PartForward},
		{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "编的名字"}}},
	} {
		p.ID, p.Conv = "编的计划:0", privateConv()
		r, err := e.ad.Send(context.Background(), sendAccount, p)
		if err != nil || r.Status != types.ReceiptFailed {
			t.Fatalf("空转发：%+v %v", r, err)
		}
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("非法参数发送 %d 次", n)
	}
}

// 取消表情回应：带 set:false；贴的时候不带 set（协议端默认 true）
func TestReactUnsetParams(t *testing.T) {
	for _, unset := range []bool{false, true} {
		e := newSendEnv(t, fixedReply(`{"status":"ok","retcode":0,"data":null}`))
		p := types.Part{ID: "编的计划:0", Conv: groupConv(), Kind: types.PartReact, Ref: "-90004", EmojiID: "289", Unset: unset}
		if r, err := e.ad.Send(context.Background(), sendAccount, p); err != nil || r.Status != types.ReceiptConfirmed {
			t.Fatalf("回应：%+v %v", r, err)
		}
		acts := e.peer.actions()
		if len(acts) != 1 || acts[0].Action != "set_msg_emoji_like" {
			t.Fatalf("帧不对：%+v", acts)
		}
		set, has := acts[0].Params["set"]
		if unset && (!has || string(set) != "false") {
			t.Fatalf("撤表情应带 set:false：%+v", acts[0].Params)
		}
		if !unset && has {
			t.Fatalf("贴表情不该带 set：%+v", acts[0].Params)
		}
	}
}

// 失败回执的原因归三类：协议端明说发不了这种段（能力缺失）；有审核、风控等措辞的“协议端拒绝”；没有可识别措辞的是瞬时失败（不当内容拒绝，出口照旧重试）
func TestRejectReasonClassifiesCapabilityOnly(t *testing.T) {
	cases := map[string]string{
		`{"status":"failed","retcode":1400,"wording":"message element \"image\" cannot be sent to this contact"}`: "协议端拒绝：不支持这类消息",
		`{"status":"failed","retcode":1400,"wording":"UNSENDABLE_TYPE video"}`:                                    "协议端拒绝：不支持这类消息",
		`{"status":"failed","retcode":1400,"wording":"this contact only accepts text messages, got image"}`:       "协议端拒绝：不支持这类消息",
		`{"status":"failed","retcode":100,"wording":"send failed"}`:                                               "协议端报发送失败",
		`{"status":"failed","retcode":100}`:                                                                       "协议端报发送失败",
		`{"status":"failed","retcode":100,"wording":"NT busy, try again later"}`:                                  "协议端报发送失败",
		`{"status":"failed","retcode":120,"wording":"risk control: content blocked"}`:                             "协议端拒绝",
	}
	for body, want := range cases {
		r := classifyReply("p", types.PartMessage, []byte(body), time.Unix(1, 0))
		if r.Status != types.ReceiptFailed || r.Err != want {
			t.Fatalf("%s\n得到 %q，应为 %q", body, r.Err, want)
		}
	}
}

// SnowLuma 的“群消息没拿到送达回执”（result=0、sequence 为空）和等不到回包，协议端自己说了“可能已经到了”：
// 必须是结果不明，不能当拒绝（旧代码当拒绝：出口试三遍，旋转又三遍，群里收到重复的图）
func TestUnconfirmedDeliveryIsUnknownNotRejected(t *testing.T) {
	for _, wording := range []string{
		"group message delivery was not confirmed: QQ returned no message receipt; check the conversation before retrying",
		"send group message failed: no response",
		"send private message failed: request timed out",
	} {
		for _, field := range []string{"wording", "message", "msg"} {
			body := `{"status":"failed","retcode":1200,"data":null,"` + field + `":"` + wording + `"}`
			r := classifyReply("p", types.PartMessage, []byte(body), time.Unix(1, 0))
			if r.Status != types.ReceiptUnknown || r.Err == "" {
				t.Fatalf("%s\n应为结果不明，得到 %+v", body, r)
			}
		}
	}
	// 同样是 failed，真拒绝仍是拒绝；能力缺失仍是能力缺失
	for body, want := range map[string]string{
		`{"status":"failed","retcode":120,"wording":"send group message rejected: result=120 err=blocked"}`:        "协议端拒绝",
		`{"status":"failed","retcode":1400,"wording":"message segment \"node\" cannot be sent in a temp session"}`: "协议端拒绝：不支持这类消息",
		`{"status":"failed","retcode":100,"wording":"image format not supported by decoder"}`:                      "协议端拒绝",
	} {
		r := classifyReply("p", types.PartForward, []byte(body), time.Unix(1, 0))
		if r.Status != types.ReceiptFailed || r.Err != want {
			t.Fatalf("%s\n得到 %+v，应为失败 %q", body, r, want)
		}
	}
}

// 没有可识别措辞的 failed 不是内容拒绝：types.IsRejection 为假，出口才会照旧重试、链接插件才不会换形式和记按图的被拒结论
func TestUnrecognizedFailedIsNotRejection(t *testing.T) {
	for _, body := range []string{
		`{"status":"failed","retcode":100,"wording":"send failed"}`,
		`{"status":"failed","retcode":100}`,
		`{"status":"failed","retcode":1200,"msg":"NT busy"}`,
	} {
		r := classifyReply("p", types.PartMessage, []byte(body), time.Unix(1, 0))
		if r.Status != types.ReceiptFailed || types.IsRejection(r.Err) {
			t.Fatalf("%s\n应为可重试的失败（不是拒绝），得到 %+v", body, r)
		}
	}
	r := classifyReply("p", types.PartMessage, []byte(`{"status":"failed","retcode":120,"wording":"risk control: content blocked"}`), time.Unix(1, 0))
	if !types.IsRejection(r.Err) {
		t.Fatalf("风控应是拒绝：%+v", r)
	}
}
