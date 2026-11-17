package snowluma

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCardDisplayFields(t *testing.T) {
	for _, padding := range []int{300, 5000} {
		raw := `{"padding":"` + strings.Repeat("字", padding) + `","app":"编的应用","meta":{"detail_1":{"title":"编\u0000的\u202e标题\ufffc","desc":"简介","tag":"编的来源","qqdocurl":"https://example.com/page","preview":"https://p.qpic.cn/cover"}}}`
		got, err := segments(json.RawMessage(`[` + cardSegment("json", cardQuote(raw)) + `]`))
		if err != nil {
			t.Fatal(err)
		}
		s := got[0]
		if s.CardTitle != "编的标题" || s.CardDescription != "简介" || s.CardSource != "编的来源" || s.CardURL != "https://example.com/page" || s.URL != "https://p.qpic.cn/cover" {
			t.Fatalf("完整载荷字段：%+v", s)
		}
	}
	for _, value := range []string{`null`, `42`, `[]`, `{}`, `"javascript:alert(1)"`, `"file:///c:/x"`, `"https://a.com@evil.com/x"`, `"https://a.com\\x"`, `"https://"`, `"https://a.com/\u0000"`, cardQuote("https://a.com/" + strings.Repeat("x", 2048))} {
		raw := `{"meta":{"news":{"title":"标题","url":` + value + `,"preview":` + value + `}}}`
		got, err := segments(json.RawMessage(`[` + cardSegment("json", raw) + `]`))
		if err != nil {
			t.Fatal(err)
		}
		if got[0].CardURL != "" || got[0].URL != "" {
			t.Fatalf("敌对字段被接受：%s", value)
		}
	}
	raw := `{"meta":{"music":{"title":"` + strings.Repeat("题", 150) + `","desc":"` + strings.Repeat("介", 400) + `","tag":"` + strings.Repeat("源", 80) + `"}}}`
	got, err := segments(json.RawMessage(`[` + cardSegment("json", raw) + `]`))
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(got[0].CardTitle)) != 120 || len([]rune(got[0].CardDescription)) != 300 || len([]rune(got[0].CardSource)) != 40 {
		t.Fatalf("字段超限：%+v", got[0])
	}
}
