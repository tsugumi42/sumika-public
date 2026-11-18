package snowluma

import (
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

func cardQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func cardSegment(kind, data string) string {
	return `{"type":"` + kind + `","data":{"data":` + data + `}}`
}

// 对照规格-链接解析 3.1：只解入口的一层字符串，字段缺失或不认识时也保留段的位置。
func TestCardSegments(t *testing.T) {
	card := "{\n  \"app\": \"com.tencent.miniapp_01\", \"meta\": {\"detail_1\": {\"title\": \"编的标题\", \"qqdocurl\": \"https://b23.tv/invented\"}}\n}"
	wrapped := `{"data":` + cardQuote(card) + `}`
	escaped := `{"meta":{"news":{"jumpUrl":"https:\/\/example.invalid\/%252F","title":"编的标题"}}}`
	xml := `<msg brief="编的卡片"/>`
	unknown := " {\n  \"app\": \"invented.app\", \"meta\": {\"news\": {\"title\": \"编的标题\"}}, \"padding\": \"" + strings.Repeat("字", 300) + "\"\n} "
	for _, c := range []struct {
		name, kind, data, preview, payload, issue string
	}{
		{"对象", "json", card, card, card, ""},
		{"字符串里套对象", "json", cardQuote(card), card, card, ""},
		{"两层字符串只解一层", "json", cardQuote(cardQuote(card)), cardQuote(card), "", "card_malformed"},
		{"外包对象的data字符串留给下游", "json", wrapped, wrapped, wrapped, ""},
		{"转义斜杠和百分号原样", "json", escaped, escaped, escaped, ""},
		{"xml", "xml", cardQuote(xml), xml, "", ""},
		{"xml即使长得像JSON也不当JSON", "xml", cardQuote(card), card, "", ""},
		{"别的app也存全", "json", cardQuote(unknown), clip(unknown), unknown, ""},
		{"坏JSON", "json", cardQuote(`{"meta":`), `{"meta":`, "", "card_malformed"},
		{"不是JSON", "json", cardQuote("编的坏卡"), "编的坏卡", "", "card_malformed"},
		{"空串", "json", `""`, "", "", "card_malformed"},
		{"null", "json", `null`, "", "", "card_malformed"},
		{"数字", "json", `42`, "42", "", "card_malformed"},
		{"数组", "json", `[]`, "[]", "", "card_malformed"},
		{"meta缺失", "json", `{}`, `{}`, `{}`, ""},
		{"meta为null", "json", `{"meta":null}`, `{"meta":null}`, `{"meta":null}`, ""},
		{"节点形状留给下游", "json", `{"meta":{"detail_1":[],"news":{"url":42},"music":null}}`, `{"meta":{"detail_1":[],"news":{"url":42},"music":null}}`, `{"meta":{"detail_1":[],"news":{"url":42},"music":null}}`, ""},
		{"只有封面不在适配器提链", "json", `{"meta":{"news":{"preview":"https://example.invalid/cover"}}}`, `{"meta":{"news":{"preview":"https://example.invalid/cover"}}}`, `{"meta":{"news":{"preview":"https://example.invalid/cover"}}}`, ""},
		{"图文app", "json", `{"app":"com.tencent.tuwen.lua"}`, `{"app":"com.tencent.tuwen.lua"}`, `{"app":"com.tencent.tuwen.lua"}`, ""},
		{"结构消息app", "json", `{"app":"com.tencent.structmsg"}`, `{"app":"com.tencent.structmsg"}`, `{"app":"com.tencent.structmsg"}`, ""},
		{"app是数字", "json", `{"app":42}`, `{"app":42}`, `{"app":42}`, ""},
		{"app是对象", "json", `{"app":{"name":"invented.app"}}`, `{"app":{"name":"invented.app"}}`, `{"app":{"name":"invented.app"}}`, ""},
		{"app是数组", "json", `{"app":[]}`, `{"app":[]}`, `{"app":[]}`, ""},
		{"app是布尔值", "json", `{"app":true}`, `{"app":true}`, `{"app":true}`, ""},
		{"app是null", "json", `{"app":null}`, `{"app":null}`, `{"app":null}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			frame := `{"post_type":"message","self_id":100,"user_id":200,"message_id":300,"message":[` + cardSegment(c.kind, c.data) + `]}`
			in, _, _, err := translate([]byte(frame))
			if err != nil || in == nil || len(in.Segs) != 1 {
				t.Fatalf("卡片所在消息没交出来：%v，%+v", err, in)
			}
			want := types.Seg{Kind: types.SegCard, Raw: c.preview, CardType: c.kind, CardJSON: c.payload, CardIssue: c.issue}
			got := in.Segs[0]
			if got.Kind != want.Kind || got.Raw != want.Raw || got.CardType != want.CardType || got.CardJSON != want.CardJSON || got.CardIssue != want.CardIssue {
				t.Fatalf("卡片字段不对：\n得到 %+v\n应该 %+v", got, want)
			}
		})
	}
}

func TestCardPayloadLimit(t *testing.T) {
	// 数字独立于实现常量；长度按解包后 UTF-8 字节算，不按 rune 或线上的转义长度算。
	for _, c := range []struct {
		name  string
		size  int
		issue string
	}{
		{"上限前一字节", 256*1024 - 1, ""},
		{"正好上限", 256 * 1024, ""},
		{"超一字节", 256*1024 + 1, "card_too_large"},
	} {
		for _, quoted := range []bool{false, true} {
			name := c.name + "/对象"
			if quoted {
				name = c.name + "/字符串"
			}
			t.Run(name, func(t *testing.T) {
				prefix, suffix := `{"padding":"`, `"}`
				padding := c.size - len(prefix) - len(suffix)
				raw := prefix + strings.Repeat("字", padding/3) + strings.Repeat("x", padding%3) + suffix
				data := raw
				if quoted {
					data = cardQuote(raw)
				}
				segs, err := segments(json.RawMessage(`[` + cardSegment("json", data) + `,{"type":"text","data":{"text":"后面的文字"}}]`))
				if err != nil || len(segs) != 2 {
					t.Fatalf("卡片不该遮住后面的文字：段数 %d，%v", len(segs), err)
				}
				got := segs[0]
				wantPayload := raw
				if c.issue != "" {
					wantPayload = ""
				}
				if got.CardJSON != wantPayload || got.CardIssue != c.issue || got.CardType != "json" {
					t.Fatalf("%d 字节卡片：载荷 %d 字节、标记 %q、类型 %q；应该 %d 字节、%q、json", len(raw), len(got.CardJSON), got.CardIssue, got.CardType, len(wantPayload), c.issue)
				}
				if got.Raw != string([]rune(raw)[:200]) || segs[1].Text != "后面的文字" {
					t.Fatal("200 字预览或后续文字变了")
				}
			})
		}
	}
}
