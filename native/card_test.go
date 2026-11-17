package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"testing"
)

func TestShareCardClick(t *testing.T) {
	a := &app{}
	s := segFromDisplay(api.DisplaySegment{Kind: "card", Card: &api.CardView{Title: "编的卡片标题", Description: "编的简介", Source: "编的来源", URL: "https://example.com/page"}})
	var opened string
	old := openBrowser
	openBrowser = func(_ *ui.Context, raw string, _ func(error)) { opened = raw }
	t.Cleanup(func() { openBrowser = old })
	tt := testerOf(400, 300, func(c *ui.Context) { a.cardBlock(c, s, false) })
	for _, text := range []string{"编的卡片标题", "编的简介", "编的来源"} {
		if !tt.HasText(text) {
			t.Fatalf("缺少 %s：%v", text, tt.Texts())
		}
	}
	click(t, tt, "编的卡片标题")
	if opened != "https://example.com/page" {
		t.Fatalf("打开 %q", opened)
	}
	for _, raw := range []string{"javascript:alert(1)", "file:///c:/x", "mailto:a@b", "https://a.com\\x", "https://a.com/\x00"} {
		opened = ""
		testerOf(100, 100, func(c *ui.Context) { a.openLink(c, raw) })
		if opened != "" {
			t.Fatalf("打开了 %q", raw)
		}
	}
}

func TestShareCardCoverKeepsGeometry(t *testing.T) {
	for _, known := range []bool{false, true} {
		name := "unknown"
		if known {
			name = "known"
		}
		t.Run(name, func(t *testing.T) {
			a, q := imgApp(t, &imgNet{orig: map[string][2]int{"card-cover": {900, 300}}})
			s := segFromDisplay(api.DisplaySegment{Media: &api.MediaRef{Hash: "card-cover"}, Card: &api.CardView{Title: "封面异步到达", Description: "编造的简介", Source: "编造来源", URL: "https://example.com/cover"}})
			if known {
				s.Width, s.Height = 900, 300
			}
			tt := testerOf(400, 700, func(c *ui.Context) { a.cardBlock(c, s, false) })
			before, ok := tt.Find("卡片封面")
			if !ok || before.W <= 0 || before.H <= 0 {
				t.Fatal("加载时没有预留封面")
			}
			rootBefore, _ := tt.Find("封面异步到达")
			settleImgs(a, q, tt)
			after, ok := tt.Find("卡片封面")
			rootAfter, _ := tt.Find("封面异步到达")
			if !ok || before != after || rootBefore != rootAfter {
				t.Fatalf("到图改变几何: cover %+v→%+v card %+v→%+v", before, after, rootBefore, rootAfter)
			}
			click(t, tt, "卡片封面")
			if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/cover" {
				t.Fatalf("封面没有沿用链接目标: %v", got)
			}
			t.Logf("known=%v cover=%+v card=%+v 到图矩形差=0", known, after, rootAfter)
		})
	}
}
