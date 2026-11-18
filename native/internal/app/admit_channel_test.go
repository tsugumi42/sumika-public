package app

import (
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
)

// 准入和分流按渠道查（规格-多平台装配第 1 步）：没有规矩的渠道一律丢、不往下走，不落回 QQ 的规矩。
// 虚构渠道的群号特意放进 QQ 的群白名单、私聊发言人用 QQ 主人的号：要是落回 QQ 的规矩，这几格都会被放行。
// 被踢下线的通知没有会话，照收。
func TestAdmitUnknownChannelDrops(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.QQ.Owner = "200"
	cfg.QQ.Groups = []config.QQID{"400"}
	var logs []string
	a := &App{cfg: cfg, logf: func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }, roster: channel.Roster{
		Owners:   map[string]string{"qq": "200"},
		Accounts: []channel.Account{{Channel: "qq", ID: "100", Character: "编的角色"}},
	}}
	a.channels = []*channelUnit{{channel: "qq", account: "100", policy: a.qqPolicy()}} // 准入按渠道清单查（规格-多平台装配第 3 步）
	group := types.Conversation{Channel: "fakeplat", Scene: types.SceneGroup, ID: "400"}
	private := types.Conversation{Channel: "fakeplat", Scene: types.ScenePrivate, Self: "100", ID: "200"}
	for _, tc := range []struct {
		name string
		got  channel.Admission
	}{
		{"群消息", a.admit(types.Inbound{Kind: types.InboundMessage, Account: "100", Conv: group, From: types.User{ID: "200"}})},
		{"私聊消息", a.admit(types.Inbound{Kind: types.InboundMessage, Account: "100", Conv: private, From: types.User{ID: "200"}})},
		{"群通知", a.admitNotice(types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group, Actor: "200"})},
		{"私聊通知", a.admitNotice(types.Notice{Kind: types.NoticeOther, Account: "100", Conv: private, Actor: "200"})},
		{"私聊通知没说是谁", a.admitNotice(types.Notice{Kind: types.NoticePoke, Account: "100", Conv: private})},
	} {
		if !tc.got.Drop {
			t.Errorf("没登记的渠道 %s 应该丢，得到 %+v", tc.name, tc.got)
		}
	}
	// 对照：同样的号在 QQ 渠道里照 QQ 的规矩放行
	qqGroup, qqPrivate := group, private
	qqGroup.Channel, qqPrivate.Channel = "qq", "qq"
	if got := a.admit(types.Inbound{Kind: types.InboundMessage, Account: "100", Conv: qqGroup, From: types.User{ID: "200"}}); got.Drop {
		t.Errorf("QQ 白名单里的群应该收：%+v", got)
	}
	if got := a.admit(types.Inbound{Kind: types.InboundMessage, Account: "100", Conv: qqPrivate, From: types.User{ID: "200"}}); got.Drop || got.NoObserve {
		t.Errorf("QQ 主人的私聊应该收、记看到：%+v", got)
	}
	if got := a.admitNotice(types.Notice{Kind: types.NoticeKicked, Account: "100"}); got.Drop || got.NoObserve {
		t.Errorf("被踢下线的通知应该照收：%+v", got)
	}
	// 分流：没登记的渠道只记日志，不碰闸门和回合（这里都是 nil，碰了就 panic）
	a.dispatch(channel.Dispatch{Batch: types.Batch{Conv: group}})
	if !strings.Contains(strings.Join(logs, "\n"), "渠道 fakeplat 这一版不处理") {
		t.Errorf("没登记的渠道分流没有记日志：%v", logs)
	}
}
