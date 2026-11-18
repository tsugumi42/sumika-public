package channel

import (
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

// 入库一站自己的 Raw 保护仍只裁预览；排队、持久化和流水线历史读取都带完整载荷。
func TestIntakeCardPayload(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	payload := " {\r\n\"meta\": {\"news\": {\"title\": \"编的标题\"}}, \"padding\": \"" + strings.Repeat("字", 300) + "\"}\n "
	in := msg(acctA, group("900"), "5001", friend, "")
	in.Segs = []types.Seg{
		{Kind: types.SegAt, Target: acctA},
		{Kind: types.SegCard, CardType: "json", CardJSON: payload, Raw: strings.Repeat("字", 300)},
		{Kind: types.SegCard, CardType: "json", CardIssue: "card_malformed", Raw: "编的坏卡"},
		{Kind: types.SegText, Text: "后面的文字"},
	}
	r := h.submit(in, Options{})
	row, ok, err := h.st.EventRow(in.Conv.String(), r.Seq)
	if err != nil || !ok {
		t.Fatalf("入库后没找到消息：%v，存在 %v", err, ok)
	}
	segs, _ := segsAndName(row.Segments.String)
	want := append([]types.Seg(nil), in.Segs...)
	want[1].Raw = strings.Repeat("字", 200)
	if len(segs) != len(want) {
		t.Fatalf("读出的段数 %d，应该 %d", len(segs), len(want))
	}
	for i, got := range segs {
		if got != want[i] {
			t.Fatalf("第 %d 段变了：载荷 %d 字节，应该 %d；类型 %q、标记 %q、预览 %d 字", i, len(got.CardJSON), len(want[i].CardJSON), got.CardType, got.CardIssue, len([]rune(got.Raw)))
		}
	}
	// 对照数据库的外部形状，防止写入和读取同时漏字段而相互遮住。
	var body struct {
		Segs []map[string]json.RawMessage `json:"segs"`
	}
	if err := json.Unmarshal([]byte(row.Segments.String), &body); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := json.Unmarshal(body.Segs[1]["card_json"], &stored); err != nil || stored != payload {
		t.Fatalf("card_json 没按完整字符串落库：%v，%d 字节，应该 %d", err, len(stored), len(payload))
	}
}

// 图片的下载地址、文件名随段落库（2026-10-31 切换前先存，以后补下载用），外部形状是 url、file 两个键。
func TestIntakeImageRef(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	in := msg(acctA, group("900"), "5002", friend, "")
	in.Segs = []types.Seg{
		{Kind: types.SegImage, URL: "https://example.invalid/download?fileid=编的", File: "0123ABCD.jpg"},
		{Kind: types.SegEmoji, File: "4567EF.gif"},
	}
	r := h.submit(in, Options{})
	row, ok, err := h.st.EventRow(in.Conv.String(), r.Seq)
	if err != nil || !ok {
		t.Fatalf("入库后没找到消息：%v，存在 %v", err, ok)
	}
	segs, _ := segsAndName(row.Segments.String)
	if len(segs) != 2 || segs[0] != in.Segs[0] || segs[1] != in.Segs[1] {
		t.Fatalf("读出的段变了：%+v", segs)
	}
	var body struct {
		Segs []map[string]json.RawMessage `json:"segs"`
	}
	if err := json.Unmarshal([]byte(row.Segments.String), &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Segs[0]["url"]) != `"https://example.invalid/download?fileid=编的"` || string(body.Segs[0]["file"]) != `"0123ABCD.jpg"` {
		t.Fatalf("地址、文件名没按 url、file 落库：%s", row.Segments.String)
	}
	if _, has := body.Segs[1]["url"]; has {
		t.Fatalf("空地址不该写键：%s", row.Segments.String)
	}
}
