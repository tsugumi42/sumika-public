package linkreader

import "testing"

func TestSteamRecognizeAndClean(t *testing.T) {
	for _, c := range []struct{ raw, kind, id, clean string }{
		{"https://store.steampowered.com/app/3998900/", "app", "3998900", "https://store.steampowered.com/app/3998900"},
		{"store.steampowered.com/app/3998900", "app", "3998900", "https://store.steampowered.com/app/3998900"},
		{"http://store.steampowered.com/app/3998900/Some_Game/?snr=1_5_9__2&l=schinese", "app", "3998900", "https://store.steampowered.com/app/3998900?l=schinese"},
		{"https://STORE.STEAMPOWERED.COM:443/app/3998900", "app", "3998900", "https://store.steampowered.com/app/3998900"},
		{"https://store.steampowered.com/agecheck/app/271590/", "app", "271590", "https://store.steampowered.com/app/271590"},
		{"https://store.steampowered.com/app/3998900?searchtext=x&curator_clanid=1", "app", "3998900", "https://store.steampowered.com/app/3998900?searchtext=x"},
		{"https://store.steampowered.com/sub/54029/", "package", "54029", "https://store.steampowered.com/sub/54029"},
		{"https://store.steampowered.com/sub/54029/Priority_Upgrade?snr=1_x", "package", "54029", "https://store.steampowered.com/sub/54029"},
		{"https://steamcommunity.com/sharedfiles/filedetails/?id=3807028911", "sharedfile", "3807028911", "https://steamcommunity.com/sharedfiles/filedetails/?id=3807028911"},
		{"https://steamcommunity.com/workshop/filedetails/?id=3807028911&searchtext=x", "sharedfile", "3807028911", "https://steamcommunity.com/sharedfiles/filedetails/?id=3807028911&searchtext=x"},
		{"https://www.steamcommunity.com/sharedfiles/filedetails/?id=2157044774&snr=1_y", "sharedfile", "2157044774", "https://steamcommunity.com/sharedfiles/filedetails/?id=2157044774"},
		{"https://steamcommunity.com/id/GabeLogan/", "profile", "id/gabelogan", "https://steamcommunity.com/id/gabelogan"},
		{"https://steamcommunity.com/profiles/76561190000000001/", "profile", "profiles/76561190000000001", "https://steamcommunity.com/profiles/76561190000000001"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != "steam" || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean || Clean(l.Clean) != c.clean {
				t.Fatalf("got %+v %v; want %s %s %s", l, ok, c.kind, c.id, c.clean)
			}
			// 与其他平台一样：文本提取和卡片提取都要能拿到同一个规范链接。
			if got := Extract("分享（" + c.raw + "）"); len(got) != 1 || got[0].Clean != c.clean {
				t.Fatalf("text extraction: %+v", got)
			}
			card := `{"meta":{"music":{"musicUrl":"` + c.raw + `"}}}`
			if got, _ := FromCard(card); len(got) != 1 || got[0].Clean != c.clean {
				t.Fatalf("card extraction: %+v", got)
			}
		})
	}
}

func TestSteamRecognizeRejects(t *testing.T) {
	for _, raw := range []string{
		// 非法的 ID 与子页
		"https://store.steampowered.com/app/0", "https://store.steampowered.com/app/3998900a", "https://store.steampowered.com/app/-1",
		"https://store.steampowered.com/app/3998900/reviews/", "https://store.steampowered.com/app/3998900/DLC",
		"https://store.steampowered.com/app/3998900/a/b", "https://store.steampowered.com/app/3998900//",
		"https://store.steampowered.com/App/3998900", "https://store.steampowered.com/", "https://store.steampowered.com/agecheck/sub/54029",
		"https://store.steampowered.com/sub/0", "https://store.steampowered.com/search/?term=x",
		// 看起来像的主机
		"https://steampowered.com/app/3998900", "https://store.steampowered.com.evil.example/app/3998900",
		"https://store-steampowered.com/app/3998900", "https://steamcommunlty.com/sharedfiles/filedetails/?id=3807028911",
		"https://steamcommunity.com.evil.example/id/gabelogan", "https://evilsteamcommunity.com/profiles/76561190000000001",
		// 社区页的参数与路径
		"https://steamcommunity.com/sharedfiles/filedetails/?id=0", "https://steamcommunity.com/sharedfiles/filedetails/?id=1&id=2",
		"https://steamcommunity.com/sharedfiles/filedetails/", "https://steamcommunity.com/sharedfiles/filedetails/?id=3807028911x",
		"https://steamcommunity.com/id/ab", "https://steamcommunity.com/id/gabelogan/inventory", "https://steamcommunity.com/ID/gabelogan",
		"https://steamcommunity.com/profiles/7656119", "https://steamcommunity.com/profiles/12345678901234567",
		// 凭据与端口
		"https://user:pass@store.steampowered.com/app/3998900", "https://store.steampowered.com:444/app/3998900",
		"https://steamcommunity.com:8443/id/gabelogan",
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %s: %+v", raw, l)
		}
	}
}

func TestSteamAppLinkForOtherPlatforms(t *testing.T) {
	l, ok := SteamAppLink("3998900")
	if !ok || l.Platform != "steam" || l.Kind != "app" || l.ID != "3998900" || l.Clean != "https://store.steampowered.com/app/3998900" {
		t.Fatalf("app link: %+v %v", l, ok)
	}
	if again, ok := Recognize(l.Clean); !ok || again.Clean != l.Clean || again.ID != l.ID {
		t.Fatalf("not idempotent: %+v", again)
	}
	for _, bad := range []string{"", "0", "abc", "3998900/reviews", "0123", "-3998900"} {
		if l, ok := SteamAppLink(bad); ok {
			t.Fatalf("accepted %q: %+v", bad, l)
		}
	}
}
