package types

import (
	"strings"
	"testing"
)

// 会话的字符串形式要和 Bun 的旧串一样（旧库不重算 session_id），而且四种形态（群/私聊 × 带不带她的号）各不相同：
// 号用编的占位，不用真实的
func TestConversationStringAndValid(t *testing.T) {
	for _, c := range []struct {
		name  string
		conv  Conversation
		str   string
		wants string // 空表示合法；否则 Valid 的报错里要有这几个字
	}{
		{"群", Conversation{Channel: "qq", Scene: SceneGroup, ID: "g1"}, "qq:group:g1", ""},
		{"私聊带她的号", Conversation{Channel: "qq", Scene: ScenePrivate, ID: "u1", Self: "bot-a"}, "qq:private:bot-a:u1", ""},
		{"同一个人私聊另一个号", Conversation{Channel: "qq", Scene: ScenePrivate, ID: "u1", Self: "bot-b"}, "qq:private:bot-b:u1", ""},
		{"换一个渠道名，串跟着变", Conversation{Channel: "other", Scene: SceneGroup, ID: "g1"}, "other:group:g1", ""},
		{"私聊没带她的号", Conversation{Channel: "qq", Scene: ScenePrivate, ID: "u1"}, "qq:private::u1", "她的号"},
		{"群带了她的号", Conversation{Channel: "qq", Scene: SceneGroup, ID: "g1", Self: "bot-a"}, "qq:group:g1", "不带她的号"},
		{"没有渠道", Conversation{Scene: SceneGroup, ID: "g1"}, ":group:g1", "渠道"},
		{"没有会话号", Conversation{Channel: "qq", Scene: SceneGroup}, "qq:group:", "会话号"},
		{"形式不认识", Conversation{Channel: "qq", Scene: "channel", ID: "g1"}, "qq:private::g1", "形式"},
		{"零值", Conversation{}, ":private::", "渠道"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.conv.String(); got != c.str {
				t.Errorf("String() = %q，要 %q", got, c.str)
			}
			err := c.conv.Valid()
			switch {
			case c.wants == "" && err != nil:
				t.Errorf("应该合法，Valid() 报了 %v", err)
			case c.wants != "" && (err == nil || !strings.Contains(err.Error(), c.wants)):
				t.Errorf("应该不合法并提到 %q，Valid() = %v", c.wants, err)
			}
		})
	}
}

// 号是字符串：超过 2^53 的号原样穿过会话串，一个字不变（用编的 17 位数，不是真号）
func TestConversationKeepsBigNumbers(t *testing.T) {
	big := "90071992547409931"
	c := Conversation{Channel: "qq", Scene: ScenePrivate, ID: big, Self: big + "0"}
	if got, want := c.String(), "qq:private:"+big+"0:"+big; got != want {
		t.Fatalf("大号码变样了：%q，要 %q", got, want)
	}
}

// 回执的三种状态各自合法，零值和乱写的状态不合法；没确认的回执不能带平台消息号
func TestReceiptValidate(t *testing.T) {
	for _, c := range []struct {
		name  string
		r     Receipt
		wants string
	}{
		{"确认", Receipt{PartID: "p1", Status: ReceiptConfirmed, PlatformID: "m1", Via: ViaReply}, ""},
		{"回声确认", Receipt{PartID: "p1", Status: ReceiptConfirmed, PlatformID: "m1", Via: ViaEcho}, ""},
		{"明确失败", Receipt{PartID: "p1", Status: ReceiptFailed, Err: "协议端拒绝"}, ""},
		{"结果未知", Receipt{PartID: "p1", Status: ReceiptUnknown, Err: "超时"}, ""},
		{"零值", Receipt{}, "零值"},
		{"只有段号", Receipt{PartID: "p1"}, "零值"},
		{"状态乱写", Receipt{PartID: "p1", Status: "ok"}, "三种之一"},
		{"Via 乱写", Receipt{PartID: "p1", Status: ReceiptConfirmed, Via: "poll"}, "Via"},
		{"失败却带着消息号", Receipt{PartID: "p1", Status: ReceiptFailed, PlatformID: "m1"}, "消息号"},
		{"未知却带着消息号", Receipt{PartID: "p1", Status: ReceiptUnknown, PlatformID: "m1"}, "消息号"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.r.Validate()
			switch {
			case c.wants == "" && err != nil:
				t.Errorf("应该合法，Validate() 报了 %v", err)
			case c.wants != "" && (err == nil || !strings.Contains(err.Error(), c.wants)):
				t.Errorf("应该不合法并提到 %q，Validate() = %v", c.wants, err)
			}
		})
	}
}

// ParseConversation 是 String 的反过程：合法的会话来回不变；串不对、还原出来不合法的都报错
func TestParseConversation(t *testing.T) {
	for _, c := range []Conversation{
		{Channel: "qq", Scene: SceneGroup, ID: "g1"},
		{Channel: "qq", Scene: ScenePrivate, ID: "u1", Self: "bot-a"},
		{Channel: "other", Scene: SceneGroup, ID: "-90002"},
	} {
		got, err := ParseConversation(c.String())
		if err != nil || got != c {
			t.Errorf("%q 还原成 %+v：%v", c.String(), got, err)
		}
	}
	for _, bad := range []string{"", "qq", "qq:group", "qq:group:", "qq:group:g1:extra", "qq:private:u1", "qq:private::u1", "qq:private:bot:", "qq:channel:g1", ":group:g1", "qq:group:g1:"} {
		if got, err := ParseConversation(bad); err == nil {
			t.Errorf("%q 不该还原成功：%+v", bad, got)
		}
	}
}

// 值得等回声的结果不明（规格-链接发送改版 A1 的 c）：没确认送达、没回包、超时、写出后断线、回包缺号；别的（没配号、取消了等待）不等
func TestAwaitsEcho(t *testing.T) {
	for _, s := range []string{UnconfirmedErr, NoReplyErr, ReceiptTimeoutErr, ConnLostErr, NoIDErr} {
		if !AwaitsEcho(s) {
			t.Errorf("%s 应该等回声", s)
		}
	}
	for _, s := range []string{"", "不是这条连接的号", "取消了等待", "回包不能确认"} {
		if AwaitsEcho(s) {
			t.Errorf("%q 不该等回声", s)
		}
	}
}
