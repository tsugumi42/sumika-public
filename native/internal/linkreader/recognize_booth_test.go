package linkreader

import (
	"strings"
	"testing"
)

const boothCanonical = "https://booth.pm/ja/items/3022619"

func TestBoothRecognitionShapes(t *testing.T) {
	cases := []struct{ name, raw, clean string }{
		{"canonical", boothCanonical, boothCanonical},
		{"bare_host", "booth.pm/ja/items/3022619", boothCanonical},
		{"no_lang", "https://booth.pm/items/3022619", boothCanonical},
		{"trailing_slash", "https://booth.pm/ja/items/3022619/", boothCanonical},
		{"shop_subdomain", "https://k-s.booth.pm/items/3022619", boothCanonical},
		{"shop_with_lang", "https://k-s.booth.pm/ja/items/3022619", boothCanonical},
		{"www_host", "https://www.booth.pm/ja/items/3022619", boothCanonical},
		{"upper_host", "https://BOOTH.PM/ja/items/3022619", boothCanonical},
		{"upper_shop", "https://K-S.Booth.PM/items/3022619", boothCanonical},
		{"upper_lang", "https://booth.pm/JA/items/3022619", boothCanonical},
		{"english", "https://booth.pm/en/items/3022619", "https://booth.pm/en/items/3022619"},
		{"korean", "https://booth.pm/ko/items/3022619", "https://booth.pm/ko/items/3022619"},
		{"zh_tw", "https://booth.pm/zh-tw/items/3022619", "https://booth.pm/zh-tw/items/3022619"},
		{"default_port", "https://booth.pm:443/ja/items/3022619", boothCanonical},
		{"fullwidth", " ｂｏｏｔｈ．ｐｍ／ja／items／3022619 ", boothCanonical},
		{"utm_noise", boothCanonical + "?utm_source=share&utm_medium=x", boothCanonical},
		{"unknown_query_kept", boothCanonical + "?referrer=a&b=&b=2", boothCanonical + "?referrer=a&b=&b=2"},
		{"max_id", "https://booth.pm/ja/items/12345678901234567890", "https://booth.pm/ja/items/12345678901234567890"},
		{"look_alike_suffix", "https://booth.pm.evil.example/ja/items/3022619", ""},
		{"look_alike_prefix", "https://xbooth.pm/ja/items/3022619", ""},
		{"look_alike_dash", "https://booth.pm-evil.example/ja/items/3022619", ""},
		{"nested_shop", "https://a.b.booth.pm/items/3022619", ""},
		{"underscore_shop", "https://k_s.booth.pm/items/3022619", ""},
		{"leading_dash_shop", "https://-ks.booth.pm/items/3022619", ""},
		{"empty_shop", "https://.booth.pm/items/3022619", ""},
		{"credentials", "https://user@booth.pm/ja/items/3022619", ""},
		{"credentials_password", "https://user:pw@booth.pm/ja/items/3022619", ""},
		{"nonstandard_port", "https://booth.pm:8443/ja/items/3022619", ""},
		{"empty_port", "https://booth.pm:/ja/items/3022619", ""},
		{"zero_id", "https://booth.pm/ja/items/0", ""},
		{"leading_zero", "https://booth.pm/ja/items/03022619", ""},
		{"letters_id", "https://booth.pm/ja/items/30226x", ""},
		{"id_too_long", "https://booth.pm/ja/items/123456789012345678901", ""},
		{"unknown_lang", "https://booth.pm/fr/items/3022619", ""},
		{"no_id", "https://booth.pm/ja/items", ""},
		{"extra_segment", "https://booth.pm/ja/items/3022619/photo", ""},
		{"json_suffix", "https://booth.pm/ja/items/3022619.json", ""},
		{"upper_items_segment", "https://booth.pm/ja/ITEMS/3022619", ""},
		{"shop_listing", "https://k-s.booth.pm/items", ""},
		{"search", "https://booth.pm/ja/search/VRChat", ""},
		{"root", "https://booth.pm/", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if ok != (c.clean != "") {
				t.Fatalf("recognized=%v: %+v", ok, l)
			}
			if !ok {
				if links := Extract("看看 " + c.raw); len(links) != 0 {
					t.Fatalf("text extracted a rejected shape: %+v", links)
				}
				return
			}
			if l.Platform != "booth" || l.Kind != "item" || l.Clean != c.clean || Clean(c.clean) != c.clean {
				t.Fatalf("identity/clean: %+v", l)
			}
			if path, _, _ := strings.Cut(c.clean, "?"); !strings.HasSuffix(path, "/items/"+l.ID) {
				t.Fatalf("id mismatch: %+v", l)
			}
			links := Extract("看看 " + c.raw)
			if len(links) != 1 || links[0].Clean != c.clean {
				t.Fatalf("text: %+v", links)
			}
		})
	}
}

func TestBoothExtractsOnceFromText(t *testing.T) {
	links := Extract("买了 https://k-s.booth.pm/items/3022619。另外 booth.pm/ja/items/3022619 也是同一个")
	if len(links) != 1 || links[0].Clean != boothCanonical || links[0].ID != "3022619" {
		t.Fatalf("expected one deduplicated booth item: %+v", links)
	}
}
