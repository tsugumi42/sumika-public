package snowluma

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

func historyFrame(c types.Conversation, id string, sec int64, self bool) string {
	uid, target := sendFriend, sendAccount
	if self {
		uid, target = sendAccount, sendFriend
	}
	if c.Scene == types.SceneGroup {
		return fmt.Sprintf(`{"message_type":"group","group_id":%s,"message_id":%s,"real_id":0,"message_seq":99,"user_id":%s,"time":%d,"sender":{"nickname":"编的主人"},"message":[{"type":"text","data":{"text":"编的旧话"}}]}`, c.ID, id, uid, sec)
	}
	return fmt.Sprintf(`{"message_type":"private","message_id":%s,"real_id":0,"message_seq":1,"user_id":%s,"target_id":%s,"time":%d,"message":[{"type":"text","data":{"text":"编的旧话"}}]}`, id, uid, target, sec)
}

// B4及形状表：字段缺/空/0/类型错分开；伪造post_type、昵称不能改变身份。
func TestHistoryPageBoundary(t *testing.T) {
	sec := int64(1761643200)
	base := historyFrame(privateConv(), "7001", sec, false)
	for _, tc := range []struct {
		name, key, value string
		remove           bool
		good             bool
	}{
		{"缺self", "self_id", "", true, true}, {"正确self", "self_id", sendAccount, false, true},
		{"空self", "self_id", `""`, false, false}, {"零self", "self_id", "0", false, false}, {"nullself", "self_id", "null", false, false}, {"假self", "self_id", "true", false, false},
		{"他号", "self_id", "12345", false, false}, {"伪造post", "post_type", `"message_sent"`, false, true},
		{"缺id", "message_id", "", true, false}, {"零id", "message_id", "0", false, false}, {"空id", "message_id", `""`, false, false}, {"nullid", "message_id", "null", false, false}, {"对象id", "message_id", "{}", false, false}, {"小数id", "message_id", "1.5", false, false}, {"溢出id", "message_id", "9223372036854775808", false, false},
		{"缺time", "time", "", true, false}, {"空time", "time", `""`, false, false}, {"零time", "time", "0", false, false}, {"负time", "time", "-1", false, false}, {"毫秒time", "time", "1761643200000", false, false}, {"小数time", "time", "1.5", false, false}, {"nulltime", "time", "null", false, false}, {"对象time", "time", "{}", false, false},
		{"零sender", "user_id", "0", false, false}, {"错sender", "user_id", "true", false, false}, {"错target", "user_id", "90009", false, false}, {"假段", "message", "{}", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var o object
			if err := json.Unmarshal([]byte(base), &o); err != nil {
				t.Fatal(err)
			}
			if tc.remove {
				delete(o, tc.key)
			} else {
				o[tc.key] = json.RawMessage(tc.value)
			}
			raw, _ := json.Marshal(o)
			in, err := historyPageInbound(raw, sendAccount, privateConv())
			if (err == nil) != tc.good {
				t.Fatalf("good=%v in=%+v err=%v", tc.good, in, err)
			}
			if tc.good && (in.Kind != types.InboundMessage || in.Account != sendAccount || in.Time.Unix() != sec) {
				t.Fatalf("伪造身份或时间被接受：%+v", in)
			}
		})
	}
	for _, c := range []types.Conversation{privateConv(), groupConv()} {
		for _, self := range []bool{false, true} {
			in, err := historyPageInbound([]byte(historyFrame(c, "-7001", sec, self)), sendAccount, c)
			if err != nil || in.PlatformID != "-7001" || in.Conv != c || (in.Kind == types.InboundSelf) != self {
				t.Fatalf("两场合自己/别人：%+v %v", in, err)
			}
		}
	}
	wrong := privateConv()
	wrong.ID = "99999"
	if _, err := historyPageInbound([]byte(base), sendAccount, wrong); err == nil {
		t.Fatal("跨会话消息不得返回")
	}
}

// 真查询通道＋假WS：群传message_id/reverse_order，私聊不带锚点，坏页不能装作空历史。
func TestHistoryPageProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bad        bool
	}{
		{"有效页", `{"status":"ok","retcode":0,"data":{"messages":[` + historyFrame(groupConv(), "-7001", 1761643200, false) + `]}}`, false},
		{"空页", `{"status":"ok","retcode":0,"data":{"messages":[]}}`, false},
		{"坏页", `{"status":"ok","retcode":0,"data":{"messages":[{}]}}`, true},
		{"混合页", `{"status":"ok","retcode":0,"data":{"messages":[{},` + historyFrame(groupConv(), "-7001", 1761643200, false) + `]}}`, true},
		{"无数组", `{"status":"ok","retcode":0,"data":{}}`, true},
		{"null数组", `{"status":"ok","retcode":0,"data":{"messages":null}}`, true},
		{"对象数组", `{"status":"ok","retcode":0,"data":{"messages":{}}}`, true},
		{"拒绝", `{"status":"failed","retcode":100,"data":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anchor := "-7000"
			if tc.name == "空页" {
				anchor = ""
			}
			e := newSendEnv(t, func(a sentAction) (string, bool) { return tc.body, a.Action == "get_group_msg_history" })
			got, err := e.ad.HistoryPage(context.Background(), sendAccount, groupConv(), anchor, 100)
			if got.Latest || got.Continuous {
				t.Fatal("群缓存锚点不能证明最新尾段或连续性")
			}
			if (err != nil) != tc.bad {
				t.Fatalf("bad=%v err=%v", tc.bad, err)
			}
			if tc.name == "混合页" && (len(got.Messages) != 1 || got.Messages[0].PlatformID != "-7001") {
				t.Fatalf("坏页里的有效消息应保留：%+v", got)
			}
			for _, a := range e.peer.rawActions() {
				if a.Action == "get_group_msg_history" && (anchor != "" && string(a.Params["message_id"]) != anchor || string(a.Params["reverse_order"]) != "true" || string(a.Params["count"]) != "100") {
					t.Fatalf("分页参数错误：%+v", a)
				}
			}
		})
	}
	e := newSendEnv(t, func(a sentAction) (string, bool) {
		return `{"status":"ok","retcode":0,"data":{"messages":[` + historyFrame(privateConv(), "7001", 1761643200, true) + `]}}`, a.Action == "get_friend_msg_history"
	})
	got, err := e.ad.HistoryPage(context.Background(), sendAccount, privateConv(), "", 100)
	if err != nil || !got.Latest || got.Continuous || len(got.Messages) != 1 || got.Messages[0].Kind != types.InboundSelf {
		t.Fatalf("私聊第一页：%+v %v", got, err)
	}
	for _, a := range e.peer.rawActions() {
		if a.Action == "get_friend_msg_history" {
			if _, ok := a.Params["message_id"]; ok {
				t.Fatal("私聊首页不该带锚点")
			}
		}
	}
	if _, err := e.ad.HistoryPage(context.Background(), sendAccount, privateConv(), "7001", 100); err == nil {
		t.Fatal("私聊不得用带锚点的接口")
	}
}

// B8：历史和get_msg共用在途、2秒间隔、轮次预算；预算耗尽不会让图片被当成过期。
func TestHistorySharedQueryBudget(t *testing.T) {
	e := newSendEnv(t, func(a sentAction) (string, bool) {
		switch a.Action {
		case "get_group_msg_history":
			return "", false
		case "get_msg":
			return qMsgImage, true
		}
		return "", false
	})
	e.ad.BeginQueryRound(2)
	defer e.ad.EndQueryRound()
	first := make(chan error, 1)
	go func() {
		_, err := e.ad.HistoryPage(context.Background(), sendAccount, groupConv(), "", 100)
		first <- err
	}()
	waitFor(t, func() bool {
		for _, a := range e.peer.rawActions() {
			if a.Action == "get_group_msg_history" {
				return true
			}
		}
		return false
	})
	second := make(chan error, 1)
	go func() { _, err := askMessage(e, privateConv(), "7001"); second <- err }()
	// 让第二调用进到等待串行的位置；假钟没拨时不能出现get_msg。
	time.Sleep(20 * time.Millisecond)
	if len(getMsgs(e)) != 0 {
		t.Fatal("历史在途时又发了get_msg")
	}
	e.clk.Advance(queryTimeout)
	if err := <-first; err == nil {
		t.Fatal("历史超时应返回错误")
	}
	e.clk.WaitTimer(t, queryGap)
	e.clk.Advance(queryGap - time.Nanosecond)
	if len(getMsgs(e)) != 0 {
		t.Fatal("两秒前发出查询")
	}
	e.clk.Advance(time.Nanosecond)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(queryGap)
	if _, err := e.ad.HistoryPage(context.Background(), sendAccount, groupConv(), "", 100); !errors.Is(err, types.ErrQueryBudget) {
		t.Fatalf("预算错误：%v", err)
	}
	if _, err := askMessage(e, privateConv(), "7001"); !errors.Is(err, types.ErrQueryBudget) {
		t.Fatalf("get_msg也须受预算限制：%v", err)
	}
	if len(getMsgs(e)) != 1 {
		t.Fatal("预算耗尽仍然发get_msg")
	}
	e.mu.Lock()
	logs := strings.Join(e.logs, "\n")
	e.mu.Unlock()
	if strings.Contains(logs, "编的旧话") {
		t.Fatal("日志泄露历史正文")
	}
}
