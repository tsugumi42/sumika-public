package desktopread

import (
	"encoding/json"
	"testing"

	"sumika-native/internal/store"
)

func noticeRow(t *testing.T, notice, raw string) store.DisplayRow {
	t.Helper()
	b, err := json.Marshal(map[string]string{"notice": notice, "raw": raw})
	if err != nil {
		t.Fatal(err)
	}
	return store.DisplayRow{Kind: "notice", Segments: string(b)}
}

func TestServiceText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, raw, who, want string
	}{
		{"进群", `{"notice_type":"group_increase","sub_type":"approve","user_id":200}`, "虚构甲", "虚构甲 加入了群聊"},
		{"退群", `{"notice_type":"group_decrease","sub_type":"leave","user_id":200}`, "虚构甲", "虚构甲 退出了群聊"},
		{"被踢", `{"notice_type":"group_decrease","sub_type":"kick","user_id":200}`, "虚构甲", "虚构甲 被移出了群聊"},
		{"没见过的退群方式", `{"notice_type":"group_decrease","sub_type":"别的","user_id":200}`, "虚构甲", "虚构甲 离开了群聊"},
		{"禁言分钟", `{"notice_type":"group_ban","sub_type":"ban","user_id":200,"duration":600}`, "虚构甲", "虚构甲 被禁言 10 分钟"},
		{"禁言小时", `{"notice_type":"group_ban","sub_type":"ban","user_id":200,"duration":7200}`, "虚构甲", "虚构甲 被禁言 2 小时"},
		{"禁言天", `{"notice_type":"group_ban","sub_type":"ban","user_id":200,"duration":86400}`, "虚构甲", "虚构甲 被禁言 1 天"},
		{"禁言秒", `{"notice_type":"group_ban","sub_type":"ban","user_id":200,"duration":30}`, "虚构甲", "虚构甲 被禁言 30 秒"},
		{"解禁", `{"notice_type":"group_ban","sub_type":"lift_ban","user_id":200}`, "虚构甲", "虚构甲 被解除禁言"},
		{"全员禁言", `{"notice_type":"group_ban","sub_type":"ban","user_id":0,"duration":-1}`, "管理员", "开启了全员禁言"},
		{"全员解禁，号码是字符串", `{"notice_type":"group_ban","sub_type":"lift_ban","user_id":"0"}`, "管理员", "关闭了全员禁言"},
		{"改名片", `{"notice_type":"group_card","user_id":200,"card_old":"旧名","card_new":"新名"}`, "虚构甲", "旧名 把群名片改成了 新名"},
		{"改名片没有旧名", `{"notice_type":"group_card","user_id":200,"card_new":"新名"}`, "虚构甲", "虚构甲 把群名片改成了 新名"},
		{"清空名片", `{"notice_type":"group_card","user_id":200,"card_old":"旧名","card_new":""}`, "虚构甲", "旧名 清空了群名片"},
		{"没有名字不露号码", `{"notice_type":"group_increase","user_id":200}`, "", "一位群友 加入了群聊"},
		{"认不出的类型", `{"notice_type":"别的类型"}`, "虚构甲", ""},
		{"正在输入不显示", `{"notice_type":"notify","sub_type":"input_status"}`, "虚构甲", ""},
		{"禁言缺子类型", `{"notice_type":"group_ban","user_id":200}`, "虚构甲", ""},
		{"改名片缺字段", `{"notice_type":"group_card","user_id":200}`, "虚构甲", ""},
		{"原始帧被截断", `{"notice_type":"group_incr`, "虚构甲", ""},
		{"空", ``, "虚构甲", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := serviceText(noticeRow(t, "other", tc.raw), tc.who); got != tc.want {
				t.Fatalf("%q，应为 %q", got, tc.want)
			}
		})
	}
}

// 只翻“认不出的通知”：戳一戳、撤回、回应有自己的显示方式；普通消息的段不是这个形状。
func TestServiceTextOnlyOtherNotices(t *testing.T) {
	t.Parallel()
	raw := `{"notice_type":"group_increase","user_id":200}`
	if got := serviceText(noticeRow(t, "recall", raw), "虚构甲"); got != "" {
		t.Fatalf("撤回不该翻：%q", got)
	}
	row := noticeRow(t, "other", raw)
	row.Kind = "message"
	if got := serviceText(row, "虚构甲"); got != "" {
		t.Fatalf("消息不该翻：%q", got)
	}
	if got := serviceText(store.DisplayRow{Kind: "notice", Segments: "不是 JSON"}, "虚构甲"); got != "" {
		t.Fatalf("段坏了不该翻：%q", got)
	}
}
