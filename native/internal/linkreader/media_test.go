package linkreader

import (
	"context"
	"net/netip"
	"testing"
)

func TestMediaCandidateGuards(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://127.0.0.1/a", "https://video.twimg.com.evil.example/a", "https://a:b@pbs.twimg.com/a", "https://pbs.twimg.com:444/a"} {
		r := Result{Media: available([]Media{{URL: available(raw)}})}
		(&Fetcher{}).inspectMedia(context.Background(), &r)
		if r.Media.Value[0].URL.State != "failed" || r.Media.Value[0].URL.Reason != "unsafe_target" {
			t.Fatalf("unsafe media accepted: %s", raw)
		}
	}
	r := Result{Media: available([]Media{{URL: available("https://pbs.twimg.com/media/a.jpg")}})}
	f := &Fetcher{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")}, nil
	}}
	f.inspectMedia(context.Background(), &r)
	if r.Media.Value[0].URL.State != "failed" || r.Media.Value[0].URL.Reason != "unsafe_target" {
		t.Fatal("mixed media DNS accepted")
	}
}
