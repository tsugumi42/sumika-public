package snowluma

import (
	"encoding/json"
	"testing"

	"sumika-native/internal/channel/types"
)

// 序号无效只失去证明，不能阻止手动补录保存消息。
func TestHistoryGroupSequenceBoundary(t *testing.T) {
	for _, c := range []types.Conversation{groupConv(), privateConv()} {
		for _, tc := range []struct {
			name, raw string
			want      int64
		}{
			{"数字", "42", 42}, {"字符串", `"42"`, 42}, {"缺", "", 0}, {"null", "null", 0}, {"零", "0", 0}, {"负", "-1", 0}, {"小数", "1.5", 0}, {"溢出", "9223372036854775808", 0}, {"对象", "{}", 0}, {"布尔", "true", 0}, {"空", `""`, 0},
		} {
			t.Run(c.Scene+"/"+tc.name, func(t *testing.T) {
				var o object
				if err := json.Unmarshal([]byte(historyFrame(c, "-701", 1761643200, false)), &o); err != nil {
					t.Fatal(err)
				}
				delete(o, "message_seq")
				if tc.raw != "" {
					o["message_seq"] = json.RawMessage(tc.raw)
				}
				raw, err := json.Marshal(o)
				if err != nil {
					t.Fatal(err)
				}
				got, err := historyPageInbound(raw, sendAccount, c)
				want := tc.want
				if c.Scene == types.ScenePrivate {
					want = 0
				}
				if err != nil || got.PlatformID != "-701" || got.MessageSeq != want {
					t.Fatalf("有效消息或序号边界错误: %+v %v want=%d", got, err, want)
				}
			})
		}
	}
}
