package linkexpand

import (
	"testing"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 识别器不认的链接形态，目录里不能标成“能取数”：Steam 评测页 Recognize 明确拒绝，目录原来却写“只能取数”。
func TestCatalogSteamReviewsMatchesRecognizer(t *testing.T) {
	for _, raw := range []string{"https://store.steampowered.com/app/900001/reviews/", "https://steamcommunity.com/app/900001/reviews/"} {
		if _, ok := linkreader.Recognize(raw); ok {
			t.Fatalf("评测页现在能识别了，记得把目录改回能取数：%s", raw)
		}
	}
	k, found := linktemplate.LookupKind("steam", "reviews")
	if !found || k.Support != linktemplate.SupportNone {
		t.Fatalf("目录里的 steam/reviews：%+v", k)
	}
}
