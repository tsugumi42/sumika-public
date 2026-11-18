package snowluma

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 撤回（规格-链接发送改版 A4）：发 delete_msg，参数是平台消息号（数字）；协议端回失败、号不对、会话不对都返回错误
func TestRecallSendsDeleteMsg(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		ok    bool
	}{
		"成功":    {`{"status":"ok","retcode":0,"data":null}`, true},
		"协议端拒绝": {`{"status":"failed","retcode":100,"data":null,"wording":"message not found or not retractable"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSendEnv(t, func(sentAction) (string, bool) { return tc.reply, true })
			done := make(chan error, 1)
			go func() { done <- e.ad.Recall(context.Background(), sendAccount, groupConv(), "-90077") }()
			act := e.peer.waitActions(t, 1)[0]
			if act.Action != "delete_msg" || string(act.Params["message_id"]) != "-90077" {
				t.Fatalf("动作或参数：%s", act.Raw)
			}
			select {
			case err := <-done:
				if (err == nil) != tc.ok {
					t.Fatalf("结果：%v", err)
				}
			case <-time.After(testPatience):
				t.Fatal("撤回没有返回")
			}
		})
	}
	e := newSendEnv(t, func(sentAction) (string, bool) { return `{"status":"ok","retcode":0}`, true })
	for name, call := range map[string]func() error{
		"别的号":   func() error { return e.ad.Recall(context.Background(), "99999", groupConv(), "-90077") },
		"号为空":   func() error { return e.ad.Recall(context.Background(), sendAccount, groupConv(), "") },
		"号为 0":  func() error { return e.ad.Recall(context.Background(), sendAccount, groupConv(), "0") },
		"号不是数字": func() error { return e.ad.Recall(context.Background(), sendAccount, groupConv(), "abc") },
		"别的渠道": func() error {
			c := groupConv()
			c.Channel = "telegram"
			return e.ad.Recall(context.Background(), sendAccount, c, "-90077")
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s 应该不撤回", name)
		}
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("不合法的撤回不该写出任何动作：%d", n)
	}
}

// 转发节点里带引用（ReplyTo）：SnowLuma 没有指向同一转发里另一节点的写法，不写出，回协议端拒绝（不支持），链接插件按旧排法重发
func TestForwardNodeReplyIsUnsupported(t *testing.T) {
	e := newSendEnv(t, func(sentAction) (string, bool) { return `{"status":"ok","retcode":0,"data":{"message_id":-1}}`, true })
	p := types.Part{ID: "编的计划:0", Conv: groupConv(), Kind: types.PartForward, Nodes: []types.ForwardNode{
		{Name: "被引的", Body: types.TextBody("原推")}, {Name: "引用的", Body: types.TextBody("评论"), ReplyTo: 1}}}
	r, err := e.ad.Send(context.Background(), sendAccount, p)
	if err != nil || r.Status != types.ReceiptFailed || !types.IsRejection(r.Err) || !strings.Contains(r.Err, "不支持") {
		t.Fatalf("回执：%+v %v", r, err)
	}
	if n := len(e.peer.actions()); n != 0 {
		t.Fatalf("不该写出：%d", n)
	}
}
