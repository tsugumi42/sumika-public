package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestLinkXDefaultsAndChecks(t *testing.T) {
	d := DefaultLinks()
	if d.X.FXTimeoutSeconds != 4 || !d.X.Syndication || len(d.check()) != 0 {
		t.Fatalf("默认值：%+v %v", d.X, d.check())
	}
	for _, c := range []struct {
		src string
		bad string
	}{
		{"x: {fxTimeoutSeconds: 0}", "fxTimeoutSeconds"},
		{"x: {fxTimeoutSeconds: 11}", "fxTimeoutSeconds"},
		{"x: {fxTimeoutSeconds: 10, syndication: false}", ""},
	} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(c.src), &v); err != nil {
			t.Fatal(c.src, err)
		}
		got := strings.Join(v.check(), ";")
		if (c.bad == "") != (got == "") || !strings.Contains(got, c.bad) {
			t.Fatalf("%s：%q", c.src, got)
		}
		if c.bad == "" && (v.X.Syndication || v.X.FXTimeoutSeconds != 10) {
			t.Fatalf("没读进来：%+v", v.X)
		}
	}
	// 只写了一项时另一项保持默认
	v := DefaultLinks()
	if err := yaml.Unmarshal([]byte("x: {fxTimeoutSeconds: 6}"), &v); err != nil || !v.X.Syndication {
		t.Fatalf("没写的 syndication 应保持默认开：%+v %v", v.X, err)
	}
	for _, src := range []string{"x: {fxTimeoutSeconds: \"4\"}", "x: {syndication: \"yes\"}"} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(src), &v); err == nil {
			t.Fatalf("类型错误该报错：%s", src)
		}
	}
}

func TestLinkTweetCardTheme(t *testing.T) {
	if d := DefaultLinks(); d.Expand.TweetCard.Theme != "auto" {
		t.Fatalf("默认 auto：%q", d.Expand.TweetCard.Theme)
	}
	for src, ok := range map[string]bool{"expand: {tweetCard: {theme: light}}": true, "expand: {tweetCard: {theme: dark}}": true, "expand: {tweetCard: {theme: dim}}": false, "expand: {tweetCard: {theme: \"\"}}": true} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(src), &v); err != nil {
			t.Fatal(src, err)
		}
		if bad := v.check(); (len(bad) == 0) != ok {
			t.Fatalf("%s：%v", src, bad)
		}
	}
}

// 用户 2026-11-13：推特卡片高度封顶可配，默认 0（不封顶），其余 480～8000。
func TestLinkTweetCardMaxHeight(t *testing.T) {
	if d := DefaultLinks(); d.Expand.TweetCard.MaxHeight != 0 {
		t.Fatalf("默认不封顶（0）：%d", d.Expand.TweetCard.MaxHeight)
	}
	for src, ok := range map[string]bool{
		"expand: {tweetCard: {maxHeight: 0}}":    true,
		"expand: {tweetCard: {maxHeight: 960}}":  true,
		"expand: {tweetCard: {maxHeight: 480}}":  true,
		"expand: {tweetCard: {maxHeight: 8000}}": true,
		"expand: {tweetCard: {maxHeight: 479}}":  false,
		"expand: {tweetCard: {maxHeight: 8001}}": false,
		"expand: {tweetCard: {maxHeight: -1}}":   false,
	} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(src), &v); err != nil {
			t.Fatal(src, err)
		}
		if bad := v.check(); (len(bad) == 0) != ok {
			t.Fatalf("%s：%v", src, bad)
		}
	}
	// 只改配色不写 maxHeight：保持默认（不封顶）；写了 960 的照写的封顶
	v := DefaultLinks()
	if err := yaml.Unmarshal([]byte("expand: {tweetCard: {theme: dark}}"), &v); err != nil {
		t.Fatal(err)
	}
	if v.Expand.TweetCard.MaxHeight != 0 {
		t.Fatalf("没写 maxHeight 不该变：%d", v.Expand.TweetCard.MaxHeight)
	}
	if err := yaml.Unmarshal([]byte("expand: {tweetCard: {maxHeight: 960}}"), &v); err != nil || v.Expand.TweetCard.MaxHeight != 960 {
		t.Fatalf("写了 960 就是 960：%v %d", err, v.Expand.TweetCard.MaxHeight)
	}
}
