package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

// Recording is explicit and writes a separate candidate, never the checked-in baseline.
var recordBlocksDir = flag.String("record-blocks-dir", "", "write a blocks golden candidate to this directory")

type blocksCase struct {
	Name string
	Segs []seg
	Text *string
}

func blocksCorpus() []blocksCase {
	var cases []blocksCase
	add := func(name string, ds ...api.DisplaySegment) {
		cases = append(cases, blocksCase{Name: name, Segs: displaySegs(ds...)})
	}
	text := func(s string) api.DisplaySegment { return api.DisplaySegment{Kind: "text", Text: s} }
	image := func(hash string) api.DisplaySegment {
		return api.DisplaySegment{Kind: "image", Media: &api.MediaRef{Hash: hash}}
	}
	add("nil segments")
	cases = append(cases, blocksCase{Name: "empty segments", Segs: []seg{}})
	add("merged text", text("前"), text("后"))
	add("whitespace", text("\r\n\t\u3000"), text(" \n"))
	add("trim and preserve interior", text("\u3000\r\n 前\r\n后 \t"))
	add("object replacement character", text("\ufffc前\ufffc后\ufffc"))
	add("inline faces", text(" \n前"), api.DisplaySegment{Kind: "face", Ref: "14"}, api.DisplaySegment{Kind: "superface", Ref: "333"}, text("后 \n"))
	add("empty face at edges", api.DisplaySegment{Kind: "face"}, text(" \n "), api.DisplaySegment{Kind: "superface"})
	add("mentions", api.DisplaySegment{Kind: "at", Target: "all"}, api.DisplaySegment{Kind: "at"}, api.DisplaySegment{Kind: "at", Target: "42", Name: "编造名字"}, api.DisplaySegment{Kind: "at", Target: "12345"}, api.DisplaySegment{Kind: "at", Target: "invalid"}, api.DisplaySegment{Kind: "at", Target: "42", Name: "12345"})
	add("mixed order", text("前"), image("one"), text("后"), api.DisplaySegment{Kind: "voice"}, image("two"))
	add("images across whitespace", image("one"), text(" \r\n"), image("two"))
	cases = append(cases, blocksCase{Name: "twenty one images", Segs: manyImgs(21)})
	add("image dimensions", api.DisplaySegment{Kind: "image", Media: &api.MediaRef{Hash: "wide"}, Width: 640, Height: 360, Animated: true})
	add("image zero dimensions", image("unknown"))
	add("sticker name", api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "[挥手]", Summary: "摘要"})
	add("sticker summary", api.DisplaySegment{Kind: "emoji", Ref: "market", Summary: "[点头]"})
	add("sticker unnamed", api.DisplaySegment{Kind: "emoji", Ref: "market"})
	add("sticker image", api.DisplaySegment{Kind: "emoji", Ref: "market", Media: &api.MediaRef{Hash: "sticker"}, Sticker: true, Animated: true, Width: 128, Height: 128})
	add("sticker empty credential", api.DisplaySegment{Kind: "emoji", Ref: "market", Media: &api.MediaRef{}, Name: "[挥手]", MediaState: "expired"})
	add("emoji without reference", api.DisplaySegment{Kind: "emoji"})
	for _, state := range []string{"", "nomedia", "too-big", "too_big", "expired", "forward_limit", "pending", "blocked", "not_image", "outbound-released", "unknown"} {
		add("image absent/"+state, api.DisplaySegment{Kind: "image", MediaState: state})
		add("image empty hash/"+state, api.DisplaySegment{Kind: "image", Media: &api.MediaRef{}, MediaState: state})
	}
	video := api.DisplaySegment{Kind: "video", Media: &api.MediaRef{Hash: "video"}, Cover: &api.MediaRef{Hash: "cover"}, DurationMS: 12500, Width: 640, Height: 360, Size: 2048, FetchState: "downloaded", Download: &api.VideoDownloadRef{Index: 3, Node: 2, Forward: []string{"outer", "inner"}}}
	add("video cover and duplicate positions", text("前"), video, video)
	add("video empty cover", api.DisplaySegment{Kind: "video", Cover: &api.MediaRef{}, FetchState: "pending"})
	add("video unavailable", api.DisplaySegment{Kind: "video"})
	add("file metadata", api.DisplaySegment{Kind: "file", Name: "编造.txt", Size: 2048, FileKind: "text", FetchState: "failed", Download: &api.VideoDownloadRef{Index: 1}})
	add("file unnamed", api.DisplaySegment{Kind: "file"})
	add("forward summary", api.DisplaySegment{Kind: "forward", Ref: "invented-forward", Forward: &api.ForwardBrief{Title: "编造记录", Lines: [3]string{"甲：第一句", "乙：第二句", "甲：第三句"}, Count: 8, Missing: 2, State: "partial"}})
	add("forward absent", api.DisplaySegment{Kind: "forward"})
	add("card fields", api.DisplaySegment{Kind: "card", Card: &api.CardView{Title: "编造标题", Description: "编造简介", Source: "编造来源", URL: "https://example.com/card"}, Links: []string{"https://example.com/card"}})
	add("card absent", api.DisplaySegment{Kind: "card"})
	add("reply ignored", api.DisplaySegment{Kind: "reply", Ref: "invented-reply"}, text("正文"))
	add("only reply", api.DisplaySegment{Kind: "reply"})
	add("fallback kinds", api.DisplaySegment{}, api.DisplaySegment{Kind: "other"}, api.DisplaySegment{Kind: "invented-kind"}, api.DisplaySegment{Kind: "voice"})
	add("issue splits images", image("one"), api.DisplaySegment{Kind: "image", Media: &api.MediaRef{Hash: "two"}, Issue: "mismatch"})
	add("issue before inline", text("前"), api.DisplaySegment{Kind: "text", Text: "后", Issue: "mismatch"})
	add("links", text(" 前 https://example.com/a?utm_source=demo&x=1 后 https：／／example.com/b "))
	add("link budget across segments", text(strings.Repeat("https://example.com/a ", 19)), text("https://example.com/b https://example.com/c"))
	for i, s := range []string{"", " \r\n\u3000", "\n 纯文字\ufffc https://example.com/text \n"} {
		cases = append(cases, blocksCase{Name: fmt.Sprintf("text fallback/%d", i), Text: &s})
	}
	return cases
}

// Stable rendering semantics: no Go type names, hashes, paths or backend credentials.
// S2 changes only the adapter below, not the recorded JSON or corpus.
type goldenSegment struct {
	Kind, Text, Target, Ref                                string
	Position                                               int
	HasMedia, HasHash, HasCover, HasCoverHash, CanDownload bool
	DurationMS, Size                                       int64
	Width, Height                                          int
	Animated, Sticker                                      bool
	FetchState, FileKind, MediaState, Issue, Summary, Name string
	Card                                                   *segview.Card
	Links                                                  []string
	Forward                                                *segview.Forward
}

func goldenSeg(s segview.Segment) goldenSegment {
	return goldenSegment{
		Kind: s.Kind, Text: s.Text, Target: s.Target, Ref: s.Ref, Position: s.Position,
		HasMedia: s.HasMedia, HasHash: s.HasHash, HasCover: s.HasCover,
		HasCoverHash: s.HasCoverHash, CanDownload: s.CanDownload,
		DurationMS: s.DurationMS, Size: s.Size, Width: s.Width, Height: s.Height,
		Animated: s.Animated, Sticker: s.Sticker, FetchState: s.FetchState, FileKind: s.FileKind,
		MediaState: s.MediaState, Issue: s.Issue, Summary: s.Summary, Name: s.Name,
		Card: s.Card, Links: s.Links, Forward: s.Forward,
	}
}

type goldenBlock struct {
	Kind       string
	Runs       []segview.Run   `json:",omitempty"`
	Images     []goldenSegment `json:",omitempty"`
	Text       string          `json:",omitempty"`
	Segment    *goldenSegment  `json:",omitempty"`
	VideoIndex *int            `json:",omitempty"`
}

func goldenBlocks(bs []segview.Block) []goldenBlock {
	var out []goldenBlock
	for _, b := range bs {
		g := goldenBlock{Text: b.Text}
		switch b.Kind {
		case segview.Inline:
			g.Kind, g.Runs = "inline", b.Runs
		case segview.Images:
			g.Kind = "images"
			for _, s := range b.Imgs {
				g.Images = append(g.Images, goldenSeg(s))
			}
		case segview.Note:
			g.Kind = "note"
		case segview.StickerNote:
			g.Kind = "sticker-note"
		case segview.CardBlock, segview.ForwardBlock:
			g.Kind = "card"
			if b.Kind == segview.ForwardBlock {
				g.Kind = "forward"
			}
			s := goldenSeg(b.Card)
			g.Segment = &s
		case segview.Video, segview.File:
			g.Kind = "video"
			if b.Kind == segview.File {
				g.Kind = "file"
			}
			s, index := goldenSeg(b.Video), b.VideoIndex
			g.Segment, g.VideoIndex = &s, &index
		default:
			panic(fmt.Sprintf("unhandled block kind %d", b.Kind))
		}
		out = append(out, g)
	}
	return out
}

func TestBlocksGolden(t *testing.T) {
	t.Parallel()
	corpus := blocksCorpus()
	var records []struct {
		Name   string
		Blocks []goldenBlock
	}
	for _, c := range corpus {
		bs := segview.BlocksOf(segmentViews(c.Segs))
		if c.Text != nil {
			bs = segview.TextBlocks(*c.Text)
		}
		records = append(records, struct {
			Name   string
			Blocks []goldenBlock
		}{c.Name, goldenBlocks(bs)})
	}
	got, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *recordBlocksDir != "" {
		if err := os.MkdirAll(*recordBlocksDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(*recordBlocksDir, "blocks.json"), got, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d cases (%d bytes)", len(records), len(got))
		return
	}
	want, err := os.ReadFile("testdata/blocks-golden/blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		candidate := filepath.Join(t.TempDir(), "blocks.json")
		if err := os.WriteFile(candidate, got, 0600); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("blocks golden differs; actual: %s (type migration must not re-record baseline)", candidate)
	}
	t.Logf("matched %d cases byte-for-byte (%d bytes)", len(records), len(got))
}
