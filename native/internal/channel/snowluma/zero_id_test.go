package snowluma

import (
	"fmt"
	"testing"

	"sumika-native/internal/channel/types"
)

// 号码是 0 一类的写法：协议端换不出号码时填 0（SnowLuma 的 user_id 和 sender.user_id 取自同一处，NapCat 也是），
// 0 不是谁的号。每个读身份号码的入口 × 每种 0 的写法：消息的发言人、私聊对方归成空（中间件不收），
// 通知的对方、做事的人、被戳的人也归成空；外层是 0 不回退 sender。不是 0 的照原样，不去空格、不去前导零。
func TestTranslateZeroIDs(t *testing.T) {
	zeros := []struct{ name, json string }{
		{"数字0", `0`}, {"字符串0", `"0"`}, {"负0", `-0`}, {"0.0", `0.0`}, {"指数0", `0e3`},
		{"字符串00", `"00"`}, {"带空格的0", `" 0"`}, {"纯空白", `" "`}, {"带加号", `"+0"`}, {"指数带符号", `0e+3`},
	}
	msgIs := func(from, conv string) func(*types.Inbound, *types.Notice) string {
		return func(in *types.Inbound, n *types.Notice) string {
			if in == nil || n != nil {
				return "应翻成一条消息"
			}
			if in.From.ID != from || in.Conv.ID != conv {
				return fmt.Sprintf("发言人 %q、会话 %q，应为 %q、%q", in.From.ID, in.Conv.ID, from, conv)
			}
			return ""
		}
	}
	noticeIs := func(conv, actor, target string) func(*types.Inbound, *types.Notice) string {
		return func(in *types.Inbound, n *types.Notice) string {
			if in != nil || n == nil {
				return "应翻成一条通知"
			}
			if n.Conv.ID != conv || n.Actor != actor || n.Target != target {
				return fmt.Sprintf("会话 %q、做事的人 %q、被戳的人 %q，应为 %q、%q、%q", n.Conv.ID, n.Actor, n.Target, conv, actor, target)
			}
			return ""
		}
	}
	// 帧里的 %[1]s 填 0 的写法；她的号是 100，别人是 200、201，群是 400
	entries := []struct {
		name, frame string
		check       func(*types.Inbound, *types.Notice) string
	}{
		{"私聊发言人", `{"post_type":"message","time":100,"self_id":100,"user_id":%[1]s,"message_type":"private","message_id":300,"message":[]}`, msgIs("", "")},
		{"外层是0不回退sender", `{"post_type":"message","time":100,"self_id":100,"user_id":%[1]s,"message_type":"private","message_id":300,"sender":{"user_id":200},"message":[]}`, msgIs("", "")},
		{"没有外层时的sender", `{"post_type":"message","time":100,"self_id":100,"message_type":"private","message_id":300,"sender":{"user_id":%[1]s},"message":[]}`, msgIs("", "")},
		{"群聊发言人", `{"post_type":"message","time":100,"self_id":100,"user_id":%[1]s,"group_id":400,"message_type":"group","message_id":300,"message":[]}`, msgIs("", "400")},
		{"她发的target跳过", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":100,"target_id":%[1]s,"peer_id":200,"message_type":"private","message_id":300,"message":[]}`, msgIs("100", "200")},
		{"她发的peer跳过", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":200,"peer_id":%[1]s,"message_type":"private","message_id":300,"message":[]}`, msgIs("100", "200")},
		{"她发的外层", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":%[1]s,"message_type":"private","message_id":300,"message":[]}`, msgIs("100", "")},
		{"私聊通知对方", `{"post_type":"notice","time":100,"self_id":100,"user_id":%[1]s,"target_id":200,"notice_type":"notify","sub_type":"profile_like"}`, noticeIs("200", "", "")},
		{"私聊通知target跳过", `{"post_type":"notice","time":100,"self_id":100,"user_id":100,"target_id":%[1]s,"peer_id":200,"notice_type":"notify","sub_type":"profile_like"}`, noticeIs("200", "100", "")},
		{"私聊通知peer", `{"post_type":"notice","time":100,"self_id":100,"user_id":100,"peer_id":%[1]s,"notice_type":"notify","sub_type":"profile_like"}`, noticeIs("", "100", "")},
		{"操作人也是0", `{"post_type":"notice","time":100,"self_id":100,"user_id":%[1]s,"operator_id":%[1]s,"group_id":400,"notice_type":"group_ban","sub_type":"ban","duration":0}`, noticeIs("400", "", "")},
		{"外层0回退操作人", `{"post_type":"notice","time":100,"self_id":100,"user_id":%[1]s,"operator_id":201,"group_id":400,"notice_type":"group_ban","sub_type":"ban","duration":0}`, noticeIs("400", "201", "")},
		{"撤回操作人不覆盖", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":%[1]s,"group_id":400,"notice_type":"group_recall","message_id":300}`, noticeIs("400", "200", "")},
		{"被戳的人", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"group_id":400,"notice_type":"notify","sub_type":"poke","target_id":%[1]s}`, noticeIs("400", "200", "")},
	}
	for _, e := range entries {
		for _, z := range zeros {
			t.Run(e.name+"/"+z.name, func(t *testing.T) {
				in, n, st, err := translate([]byte(fmt.Sprintf(e.frame, z.json)))
				if err != nil || st != nil {
					t.Fatalf("err=%v status=%v", err, st)
				}
				if msg := e.check(in, n); msg != "" {
					t.Fatal(msg)
				}
			})
		}
	}
	// 不是 0 的照原样：前导零、空格、小数写法都不归一（不然 "0200" 会被认成 200），没有外层时照旧回退 sender
	for _, tc := range []struct{ name, uid, sender, want string }{
		{"前导零", `"0200"`, ``, "0200"},
		{"带空格", `" 200"`, ``, " 200"},
		{"小数写法", `200.0`, ``, "200.0"},
		{"没有外层回退sender", ``, `200`, "200"},
	} {
		t.Run("照原样/"+tc.name, func(t *testing.T) {
			fields := ``
			if tc.uid != "" {
				fields += `"user_id":` + tc.uid + `,`
			}
			if tc.sender != "" {
				fields += `"sender":{"user_id":` + tc.sender + `},`
			}
			in, _, _, err := translate([]byte(`{"post_type":"message","time":100,"self_id":100,` + fields + `"message_type":"private","message_id":300,"message":[]}`))
			if err != nil || in == nil || in.From.ID != tc.want || in.Conv.ID != tc.want {
				t.Fatalf("got %+v err=%v; 发言人和会话应为 %q", in, err, tc.want)
			}
		})
	}
}

// at 段不走 qqID（ADR-221 第 3 条）：at 了 0 现在算"at 了别人"，归成空会让她在群里多接话
func TestTranslateAtZeroKept(t *testing.T) {
	in, _, _, err := translate([]byte(`{"post_type":"message","time":100,"self_id":100,"user_id":200,"group_id":400,"message_type":"group","message_id":300,"message":[{"type":"at","data":{"qq":0}}]}`))
	if err != nil || in == nil || len(in.Segs) != 1 || in.Segs[0].Kind != types.SegAt || in.Segs[0].Target != "0" {
		t.Fatalf("got %+v err=%v; at 段的 0 应原样留成 \"0\"", in, err)
	}
}
