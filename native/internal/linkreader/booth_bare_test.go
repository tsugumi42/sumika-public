package linkreader

import "testing"

// 裸写的店铺子域（没有 https://）也要认出来；多级子域、外站路径里夹带的不认。
func TestExtractBareBoothShopSubdomain(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"看这个 shop-a.booth.pm/items/1234567 很好", "https://booth.pm/ja/items/1234567"},
		{"shop-a.booth.pm/items/1234567", "https://booth.pm/ja/items/1234567"},
		{"SHOP-A.booth.pm/items/1234567", "https://booth.pm/ja/items/1234567"},
		{"booth.pm/en/items/1234567", "https://booth.pm/en/items/1234567"},
		{"a.b.booth.pm/items/1234567", ""},
		{"example.com/shop-a.booth.pm/items/1234567", ""},
		{"-bad.booth.pm/items/1234567", ""},
		{"shop-a.booth.pm/search/abc", ""},
	}
	for _, c := range cases {
		got := Extract(c.text)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%q 不该认出，却得到 %+v", c.text, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Clean != c.want {
			t.Errorf("%q: 得到 %+v，想要 %s", c.text, got, c.want)
		}
	}
}
