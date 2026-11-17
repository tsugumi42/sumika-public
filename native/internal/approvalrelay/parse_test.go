package approvalrelay

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

func text(s string) []types.Seg { return []types.Seg{{Kind: types.SegText, Text: s}} }

func TestParseReply(t *testing.T) {
	cases := []struct {
		name string
		segs []types.Seg
		want Verdict
		id   string
	}{
		{"同意", text("同意"), VerdictApprove, ""},
		{"两端空白", text("  同意\n"), VerdictApprove, ""},
		{"全角空格", text("\u3000同意\u3000"), VerdictApprove, ""},
		{"带号", text("同意 1234"), VerdictApprove, "1234"},
		{"带号无空格", text("同意1234"), VerdictApprove, "1234"},
		{"拒绝带号", text("拒绝 0007"), VerdictDeny, "0007"},
		{"拒绝", text("拒绝"), VerdictDeny, ""},
		{"批准", text("批准"), VerdictApprove, ""},
		{"不同意", text("不同意"), VerdictDeny, ""},
		{"零宽", text("同\u200b意"), VerdictInvisible, ""},
		{"零宽带号", text("同意\u200b 1234"), VerdictInvisible, "1234"},
		{"双向", text("\u202e同意"), VerdictInvisible, ""},
		{"BOM", text("\ufeff拒绝"), VerdictInvisible, ""},
		{"变体选择符", text("同意\ufe0f"), VerdictInvisible, ""},
		{"子串", text("我同意"), VerdictNone, ""},
		{"后缀", text("同意了"), VerdictNone, ""},
		{"三位号", text("同意 123"), VerdictNone, ""},
		{"五位号", text("同意 12345"), VerdictNone, ""},
		{"全角号", text("同意 １２３４"), VerdictNone, ""},
		{"空", text(""), VerdictNone, ""},
		{"只有零宽", text("\u200b"), VerdictNone, ""},
		{"好的", text("好的"), VerdictWeak, ""},
		{"OK 大小写", text("OK"), VerdictWeak, ""},
		{"可以", text("可以"), VerdictWeak, ""},
		{"夹图", append(text("同意"), types.Seg{Kind: types.SegImage}), VerdictMixed, ""},
		{"转发", []types.Seg{{Kind: types.SegForward}, {Kind: types.SegText, Text: "同意"}}, VerdictMixed, ""},
		{"at", []types.Seg{{Kind: types.SegAt, Target: "1"}, {Kind: types.SegText, Text: "同意"}}, VerdictMixed, ""},
		{"表情", append(text("同意"), types.Seg{Kind: types.SegFace}), VerdictMixed, ""},
		{"两个引用", []types.Seg{{Kind: types.SegReply, Target: "1"}, {Kind: types.SegReply, Target: "2"}, {Kind: types.SegText, Text: "同意"}}, VerdictMixed, ""},
		{"超长", text("同意" + strings.Repeat(" ", maxReplyBytes)), VerdictNone, ""},
		{"非法 UTF-8", text("同意\xff"), VerdictNone, ""},
		{"分两段拼起来", []types.Seg{{Kind: types.SegText, Text: "同"}, {Kind: types.SegText, Text: "意"}}, VerdictApprove, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReply(tc.segs)
			if got.Verdict != tc.want || got.ID != tc.id {
				t.Fatalf("得到 %+v，想要 %v %q", got, tc.want, tc.id)
			}
		})
	}
}

func TestParseReplyQuote(t *testing.T) {
	got := ParseReply([]types.Seg{{Kind: types.SegReply, Target: "9001"}, {Kind: types.SegText, Text: "同意"}})
	if got.Verdict != VerdictApprove || got.Quote != "9001" {
		t.Fatalf("%+v", got)
	}
}

// 解析是纯函数：同一输入两次结果一样（防止状态泄漏）
func TestParseReplyStable(t *testing.T) {
	a := ParseReply(text("同意 1234"))
	b := ParseReply(text("同意 1234"))
	if a != b {
		t.Fatal("不稳定")
	}
}
