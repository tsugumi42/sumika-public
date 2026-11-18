package linkreader

import (
	"testing"
)

func TestSpotifyRecognize(t *testing.T) {
	const track = "4cOdK2wGLETKBW3PvgPWqT"
	cases := []struct{ raw, kind, id, clean string }{
		{"https://open.spotify.com/track/" + track, "track", track, "https://open.spotify.com/track/" + track},
		{"https://open.spotify.com/track/" + track + "/", "track", track, "https://open.spotify.com/track/" + track},
		{"https://open.spotify.com/intl-de/album/1DFixLWuPkv3KT3TnV35m3/", "album", "1DFixLWuPkv3KT3TnV35m3", "https://open.spotify.com/album/1DFixLWuPkv3KT3TnV35m3"},
		{"https://open.spotify.com/intl-pt-br/playlist/37i9dQZF1DXcBWIGoYBM5M", "playlist", "37i9dQZF1DXcBWIGoYBM5M", "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"},
		{"https://open.spotify.com/embed/playlist/37i9dQZF1DXcBWIGoYBM5M", "playlist", "37i9dQZF1DXcBWIGoYBM5M", "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"},
		{"https://open.spotify.com/artist/0gxyHStUsqpMadRV0Di1Qt?si=abc123", "artist", "0gxyHStUsqpMadRV0Di1Qt", "https://open.spotify.com/artist/0gxyHStUsqpMadRV0Di1Qt"},
		{"https://open.spotify.com/episode/4u6vthbPvMXcSwR95gYvY4?si=x&utm_source=copy-link&t=30", "episode", "4u6vthbPvMXcSwR95gYvY4", "https://open.spotify.com/episode/4u6vthbPvMXcSwR95gYvY4?t=30"},
		{"https://OPEN.SPOTIFY.COM/track/" + track + "?utm_medium=x&si=y", "track", track, "https://open.spotify.com/track/" + track},
		{"https://open.spotify.com/show/4rOoJ6Egrf8K2IrywzwOMk", "show", "4rOoJ6Egrf8K2IrywzwOMk", "https://open.spotify.com/show/4rOoJ6Egrf8K2IrywzwOMk"},
		{"https://spotify.link/AbC123xyz", "short", "", "https://spotify.link/AbC123xyz"},
		{"https://spotify.link/AbC123xyz?si=share", "short", "", "https://spotify.link/AbC123xyz"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok {
				t.Fatalf("rejected %q", c.raw)
			}
			if l.Platform != "spotify" || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean {
				t.Fatalf("got %+v, want kind=%s id=%s clean=%s", l, c.kind, c.id, c.clean)
			}
		})
	}
}

func TestSpotifyRejectsShapesAndLookalikes(t *testing.T) {
	const track = "4cOdK2wGLETKBW3PvgPWqT"
	for _, raw := range []string{
		"https://open.spotify.com.evil.example/track/" + track,
		"https://open.spotify.com@evil.example/track/" + track,
		"https://evil.example/open.spotify.com/track/" + track,
		"https://user:pw@open.spotify.com/track/" + track,
		"https://open.spotify.com:8443/track/" + track,
		"https://open.spotify.com/track/short",
		"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWq!",
		"https://open.spotify.com/track/" + track + "/extra",
		"https://open.spotify.com/user/spotify",
		"https://open.spotify.com/search/" + track,
		"https://open.spotify.com/track/%34cOdK2wGLETKBW3PvgPWqT/x",
		"https://spotify.link/a/b",
		"https://spotify.link.evil.example/AbC123xyz",
		"https://evilspotify.link/AbC123xyz",
		"spotify:track:" + track,
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %q as %+v", raw, l)
		}
	}
}

func TestSpotifyExtractAndClean(t *testing.T) {
	const track = "4cOdK2wGLETKBW3PvgPWqT"
	links := Extract("听这首 https://open.spotify.com/track/" + track + "?si=a，然后 https://open.spotify.com/track/" + track + "?si=b&utm_source=x 再看")
	if len(links) != 1 || links[0].Platform != "spotify" || links[0].Clean != "https://open.spotify.com/track/"+track {
		t.Fatalf("extract: %+v", links)
	}
	got, err := CleanAny("https://open.spotify.com/track/" + track + "?si=abc&x=keep")
	if err != nil || got != "https://open.spotify.com/track/"+track+"?x=keep" {
		t.Fatalf("clean: %q %v", got, err)
	}
}
