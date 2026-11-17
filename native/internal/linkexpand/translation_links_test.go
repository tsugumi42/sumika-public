package linkexpand

import (
	"strings"
	"testing"
)

// #36 X-09：模型把链接原样保留，原文有一遍、译文又有一遍。
func TestDropRepeatedLinks(t *testing.T) {
	orig := "She listed it on my example shop !\nhttps://example-shop.booth.pm/items/10001"
	for name, c := range map[string]struct{ in, want string }{
		"整行只有链接":  {"她现在已在编造的示例店铺上架啦！\nhttps://example-shop.booth.pm/items/10001", "她现在已在编造的示例店铺上架啦！"},
		"链接在行尾":   {"详见页面 https://example-shop.booth.pm/items/10001", "详见页面"},
		"链接带句号":   {"详见页面 https://example-shop.booth.pm/items/10001。", "详见页面"},
		"句子中间不动":  {"请看 https://example-shop.booth.pm/items/10001 的商品页", "请看 https://example-shop.booth.pm/items/10001 的商品页"},
		"原文没有的链接": {"另见 https://other.example.invalid/x", "另见 https://other.example.invalid/x"},
		"链接后有空行":  {"已在示例店铺上架！\nhttps://example-shop.booth.pm/items/10001\n\n", "已在示例店铺上架！"},
	} {
		got := dropRepeatedLinks(c.in, orig)
		if got != c.want {
			t.Errorf("%s：%q → %q，要 %q", name, c.in, got, c.want)
		}
		if again := dropRepeatedLinks(got, orig); again != got {
			t.Errorf("%s：去两遍不等于去一遍：%q → %q", name, got, again)
		}
	}
	if got := dropRepeatedLinks("没有链接的译文", "no link"); got != "没有链接的译文" {
		t.Fatalf("原文没链接不该动：%q", got)
	}
}

// 译文去掉重复链接后正好整段空了（模型只回了链接）：不出译文块，也不留一条“——————”。
func TestTranslationOnlyLinkIsDropped(t *testing.T) {
	r := xPost("https://example-shop.booth.pm/items/10001")
	d := Build(&r, "", Options{Translation: "https://example-shop.booth.pm/items/10001"}, 0)
	if got := strings.Join(d.Describe(), "\n"); strings.Contains(got, "——————") {
		t.Fatalf("译文只剩链接不该出分隔线：\n%s", got)
	}
}

func TestTweetTranslationDoesNotRepeatLink(t *testing.T) {
	r := xPost("She listed it on my example shop !\nhttps://example-shop.booth.pm/items/10001")
	d := Build(&r, "", Options{Translation: "她现在已在编造的示例店铺上架啦！\nhttps://example-shop.booth.pm/items/10001"}, 0)
	got := strings.Join(d.Describe(), "\n")
	// 只放译文时原文不出现，链接就留在译文里（一份）；模型把链接丢了也要补回来
	if !strings.Contains(got, "她现在已在编造的示例店铺上架啦！\n") || strings.Count(got, "https://example-shop.booth.pm/items/10001") != 1 || strings.Contains(got, "She listed") {
		t.Fatalf("译文里的链接留一份、原文不出现：\n%s", got)
	}
	d = Build(&r, "", Options{Translation: "她现在已在编造的示例店铺上架啦！"}, 0)
	if got := strings.Join(d.Describe(), "\n"); strings.Count(got, "https://example-shop.booth.pm/items/10001") != 1 {
		t.Fatalf("译文丢了链接要补回：\n%s", got)
	}
}
