package chatui

import (
	"fmt"
	"testing"
)

func cardQuote(n int) Quoted {
	return Quoted{MessageID: fmt.Sprintf("m%d", n), Name: "甲", Text: fmt.Sprintf("被引的第 %d 句", n)}
}

// 卡区超过最大高度后，新加的引用卡要滚进视野（主人 2026-11-09）；再引一次已有的那段，也回到那张卡。
func TestComposerRevealsAddedQuoteCard(t *testing.T) {
	r := newComposerRigSized(t, 640, 900)
	visible := func(n int) {
		t.Helper()
		r.tester.Frame()
		r.tester.Frame()
		area, ok := r.tester.Find("引用卡")
		if !ok {
			t.Fatalf("没找到卡区：%v", r.tester.Texts())
		}
		card, ok := r.tester.Find(fmt.Sprintf("引用 %d：甲", n))
		if !ok {
			t.Fatalf("没找到引用 %d：%v", n, r.tester.Texts())
		}
		if card.Y < area.Y-1 || card.Y+card.H > area.Y+area.H+1 {
			t.Fatalf("引用 %d 不在卡区视野里：卡 %+v，卡区 %+v", n, card, area)
		}
	}
	const total = 8
	for n := 1; n <= total; n++ {
		r.p.AddQuote(cardQuote(n), false)
		visible(n)
	}
	r.p.AddQuote(cardQuote(1), false) // 已经有这张：不重复加，滚回去
	visible(1)
	if len(r.p.Cards) != total {
		t.Fatalf("不该重复加卡：%d", len(r.p.Cards))
	}
}

// 只在加卡那一下滚；之后用户自己往回翻，不能被拉回去。
func TestComposerRevealOnlyOnce(t *testing.T) {
	r := newComposerRigSized(t, 640, 900)
	for n := 1; n <= 8; n++ {
		r.p.AddQuote(cardQuote(n), false)
		r.tester.Frame()
		r.tester.Frame()
	}
	if r.p.reveal != 0 {
		t.Fatalf("滚过以后不该还留着请求：%d", r.p.reveal)
	}
}
