package app

import (
	"testing"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
)

// 私聊通知里做事的人没说是谁（协议端没给，或者给的 0 归成了空，ADR-221）时按会话对方认陌生人：
// 陌生人私聊里的这种通知照存不观察、不进记忆；主人和私聊白名单里的人照常观察。做事的人有号时照旧按做事的人认。
func TestAdmitNoticeUnknownActorUsesPeer(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.QQ.Owner = "200"
	cfg.QQ.PrivateAllow = []config.QQID{"300"}
	a := &App{cfg: cfg, roster: channel.Roster{
		Owners:   map[string]string{"qq": "200"},
		Accounts: []channel.Account{{Channel: "qq", ID: "100", Character: "编的角色"}},
	}}
	a.channels = []*channelUnit{{channel: "qq", account: "100", policy: a.qqPolicy()}} // 准入按渠道清单查（规格-多平台装配第 3 步）
	for _, tc := range []struct {
		name, actor, peer string
		noObserve         bool
	}{
		{"没说是谁、对方是陌生人", "", "999", true},
		{"没说是谁、对方是主人", "", "200", false},
		{"没说是谁、对方在私聊白名单", "", "300", false},
		{"做事的是陌生人、会话是主人的", "999", "200", true},
		{"做事的是白名单里的人", "300", "999", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := types.Notice{Kind: types.NoticeOther, Account: "100", Actor: tc.actor, Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: tc.peer}}
			if got := a.admitNotice(n); got.NoObserve != tc.noObserve || got.Drop {
				t.Fatalf("准入 %+v，NoObserve 应为 %v", got, tc.noObserve)
			}
		})
	}
}
