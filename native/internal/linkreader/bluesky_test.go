package linkreader

import "testing"

const (
	bskyPostRKey = "3mzhhvhjfwk22"
	bskyTestUser = "example-lab.bsky.social"
	bskyDIDPLC   = "did:plc:samplelabsamplelabsample"
)

func TestBlueskyRecognizeShapes(t *testing.T) {
	for _, c := range []struct{ raw, kind, id, clean string }{
		{"https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey, "post", bskyTestUser + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey},
		{"https://bsky.app/profile/Example-Lab.bsky.social/post/" + bskyPostRKey, "post", bskyTestUser + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey},
		{"https://www.bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey + "/", "post", bskyTestUser + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey},
		{"HTTPS://BSKY.APP/profile/" + bskyTestUser + "/post/" + bskyPostRKey, "post", bskyTestUser + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey},
		{"bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey + "?utm_source=qq&ref=keep", "post", bskyTestUser + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey + "?ref=keep"},
		{"http://bsky.app:80/profile/" + bskyTestUser + "/post/" + bskyPostRKey, "post", bskyTestUser + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey},
		{"https://bsky.app/profile/" + bskyDIDPLC + "/post/" + bskyPostRKey, "post", bskyDIDPLC + "/" + bskyPostRKey, "https://bsky.app/profile/" + bskyDIDPLC + "/post/" + bskyPostRKey},
		{"https://bsky.app/profile/" + bskyTestUser, "profile", bskyTestUser, "https://bsky.app/profile/" + bskyTestUser},
		{"https://bsky.app/profile/" + bskyTestUser + "/", "profile", bskyTestUser, "https://bsky.app/profile/" + bskyTestUser},
		{"https://BSKY.app/profile/Example-Lab.bsky.social/", "profile", bskyTestUser, "https://bsky.app/profile/" + bskyTestUser},
		{"https://bsky.app/profile/" + bskyDIDPLC, "profile", bskyDIDPLC, "https://bsky.app/profile/" + bskyDIDPLC},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != "bluesky" || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean || Clean(l.Clean) != c.clean {
				t.Fatalf("got %+v %v; want %s %s %s", l, ok, c.kind, c.id, c.clean)
			}
		})
	}
}

func TestBlueskyRejectsBadShapes(t *testing.T) {
	for _, raw := range []string{
		// 错误的 rkey：长度不对、含 TID 字母表以外的字符（0、1、大写）。
		"https://bsky.app/profile/" + bskyTestUser + "/post/3mzhhvhjfwk2",
		"https://bsky.app/profile/" + bskyTestUser + "/post/3mzhhvhjfwk220",
		"https://bsky.app/profile/" + bskyTestUser + "/post/3MZHHVHJFWK22",
		// 尾段和其他页面：不做前缀匹配。
		"https://bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey + "/liked-by",
		"https://bsky.app/profile/" + bskyTestUser + "/lists/3lvpca43j5z26",
		"https://bsky.app/profile/" + bskyTestUser + "/feed/whats-hot",
		"https://bsky.app/profile/" + bskyTestUser + "/post",
		"https://bsky.app/feed/whats-hot",
		"https://bsky.app/starter-pack/bsky.app/3mzdvli2fkq23",
		"https://bsky.app/s/whatever",
		// handle 形状不合法：没有点、数字 TLD、IP、空段。
		"https://bsky.app/profile/localhost/post/" + bskyPostRKey,
		"https://bsky.app/profile/1.2.3.4/post/" + bskyPostRKey,
		"https://bsky.app/profile/did:plc:short/post/" + bskyPostRKey,
		"https://bsky.app/profile//post/" + bskyPostRKey,
		// 不认的主机：移动端、域名拼接、同名后缀、其他服务。
		"https://m.bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey,
		"https://bsky.app.evil.example/profile/" + bskyTestUser + "/post/" + bskyPostRKey,
		"https://evilbsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey,
		"https://mybsky.app/profile/" + bskyTestUser,
		"https://bsky.social/profile/" + bskyTestUser,
		// 凭据和非默认端口由统一入口拒绝。
		"https://user:pass@bsky.app/profile/" + bskyTestUser + "/post/" + bskyPostRKey,
		"https://bsky.app:8443/profile/" + bskyTestUser + "/post/" + bskyPostRKey,
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %s: %+v", raw, l)
		}
	}
}
