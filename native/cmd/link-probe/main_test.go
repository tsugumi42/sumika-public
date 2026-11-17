package main

import (
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

func TestProbeRedactsMediaSignatures(t *testing.T) {
	media := linkreader.Media{URL: linkreader.Field[string]{State: "available", Value: "https://video.twimg.com/a.mp4?token=secret-token"}, Thumbnail: linkreader.Field[string]{State: "available", Value: "https://pbs.twimg.com/a.jpg?signature=secret-signature"}}
	quoted := &linkreader.Result{Media: linkreader.Field[[]linkreader.Media]{State: "available", Value: []linkreader.Media{media}}}
	r := linkreader.Result{CleanURL: "https://www.bilibili.com/video/av170001?p=2", Media: linkreader.Field[[]linkreader.Media]{State: "available", Value: []linkreader.Media{media}}, Quote: linkreader.Field[*linkreader.Result]{State: "available", Value: quoted}, Blocks: []linkreader.Block{{Type: "video", Media: &media}, {Type: "quote", Quote: quoted}}}
	r.Author.Avatar = linkreader.Field[string]{State: "available", Value: "https://i0.hdslb.com/a.jpg?signature=secret-avatar"}
	r.Ugoira = &linkreader.UgoiraInfo{ZIP: linkreader.Field[string]{State: "available", Value: "https://i.pximg.net/a.zip?signature=secret-zip"}}
	r.Media.Value[0].Fallback = &linkreader.MediaFallback{URL: linkreader.Field[string]{State: "available", Value: "https://i.pximg.net/regular.jpg?token=secret-fallback"}}
	variant := linkreader.MediaVariant{URL: linkreader.Field[string]{State: "available", Value: "https://video.twimg.com/variant.mp4?signature=secret-variant"}}
	r.Media.Value[0].Variants = linkreader.Field[[]linkreader.MediaVariant]{State: "available", Value: []linkreader.MediaVariant{variant}}
	r.LinkCard = linkreader.Field[linkreader.LinkPreview]{State: "available", Value: linkreader.LinkPreview{Image: linkreader.Field[linkreader.Media]{State: "available", Value: linkreader.Media{URL: linkreader.Field[string]{State: "available", Value: "https://pbs.twimg.com/card.jpg?signature=secret-card"}}}}}
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "text", Links: []linkreader.ExternalLink{{URL: linkreader.Field[string]{State: "available", Value: "https://example.com/doc?token=secret-link"}}}})
	redact(&r)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-") || r.Media.Value[0].URL.Value != "https://video.twimg.com/a.mp4?redacted" {
		t.Fatalf("signature leaked: %s", b)
	}
}
