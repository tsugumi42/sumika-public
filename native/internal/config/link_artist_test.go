package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// 画师设置（规格-画师原图 第六节）：默认值、非法值、只写一项时另一项保持默认。
func TestLinkArtistDefaultsAndChecks(t *testing.T) {
	d := DefaultLinks()
	if d.Expand.Artist.Mode != ArtistCardAndOriginals || !d.Expand.Artist.AutoDetect || len(d.check()) != 0 {
		t.Fatalf("默认值：%+v %v", d.Expand.Artist, d.check())
	}
	for src, ok := range map[string]bool{
		"expand: {artist: {mode: cardAndOriginals}}": true,
		"expand: {artist: {mode: originalsOnly}}":    true,
		"expand: {artist: {mode: off}}":              true,
		"expand: {artist: {mode: \"\"}}":             true, // 空当默认
		"expand: {artist: {mode: cards}}":            false,
		"expand: {artist: {mode: OFF}}":              false,
	} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(src), &v); err != nil {
			t.Fatal(src, err)
		}
		bad := strings.Join(v.check(), ";")
		if (bad == "") != ok || (!ok && !strings.Contains(bad, "linkReader.expand.artist.mode")) {
			t.Fatalf("%s：%q", src, bad)
		}
	}
	// 只写 mode，autoDetect 保持默认开；只写 autoDetect，mode 保持默认
	v := DefaultLinks()
	if err := yaml.Unmarshal([]byte("expand: {artist: {mode: originalsOnly}}"), &v); err != nil || !v.Expand.Artist.AutoDetect || v.Expand.Artist.Mode != ArtistOriginalsOnly {
		t.Fatalf("%+v %v", v.Expand.Artist, err)
	}
	v = DefaultLinks()
	if err := yaml.Unmarshal([]byte("expand: {artist: {autoDetect: false}}"), &v); err != nil || v.Expand.Artist.AutoDetect || v.Expand.Artist.Mode != ArtistCardAndOriginals {
		t.Fatalf("%+v %v", v.Expand.Artist, err)
	}
	v = DefaultLinks()
	if err := yaml.Unmarshal([]byte("expand: {artist: {mode: \"\"}}"), &v); err != nil || v.Expand.Artist.EffectiveMode() != ArtistCardAndOriginals {
		t.Fatalf("空 mode 应该按默认：%+v %v", v.Expand.Artist, err)
	}
}
