package linkreader

import (
	"strconv"
	"strings"
	"testing"
)

// 切在链接中间的回归：截断、分段、缩短显示都不能留下半条链接。
func TestSafeSplitNeverCutsInsideLink(t *testing.T) {
	const link = "https://example.com/aaaaaaaaaaaaaaaaaaaa/bbbbbbbbbbbbbbbbbbbbbbbb.html"
	text := strings.Repeat("字", 10) + link + "结束"
	start := strings.Index(text, link)
	for at := start + 1; at < start+len(link); at++ {
		if got := SafeSplit(text, at); got != start {
			t.Fatalf("at=%d: 切点应退到链接开头 %d，得到 %d", at, start, got)
		}
	}
	if got := SafeSplit(text, start); got != start {
		t.Fatalf("切点正好在链接开头应不动，得到 %d", got)
	}
	if got := SafeSplit(text, start+len(link)); got != start+len(link) {
		t.Fatalf("切点正好在链接结尾应不动，得到 %d", got)
	}
	// 链接从开头起就跨过切点：退不了，整条留在前一段
	if got := SafeSplit(link+"尾", 10); got != len(link) {
		t.Fatalf("开头的长链接应整条保留，得到 %d", got)
	}
	// 没有链接时原样
	if got := SafeSplit("没有链接的一段话", 9); got != 9 {
		t.Fatalf("无链接应原样，得到 %d", got)
	}
}

func TestCutRunesKeepsLinkWhole(t *testing.T) {
	link := "https://example.com/" + strings.Repeat("p", 60)
	text := strings.Repeat("字", 795) + link
	head, cut := CutRunes(text, 800)
	if !cut || strings.Contains(head, "https://") || head != strings.Repeat("字", 795) {
		t.Fatalf("链接跨过第 800 字时应整条丢给后面: cut=%v head=%q", cut, head)
	}
	if _, cut := CutRunes(text, 2000); cut {
		t.Fatal("没超长不应截")
	}
	// 链接在切点之前完整：照常保留
	head, cut = CutRunes(link+strings.Repeat("字", 50), 100)
	if !cut || !strings.HasPrefix(head, link) {
		t.Fatalf("切点前完整的链接要保留: %q", head)
	}
}

// 正文取数阶段的 800 字截取不能把链接切一半。
func TestPreviewFieldDoesNotSplitLink(t *testing.T) {
	link := "https://example.com/article/" + strings.Repeat("q", 80)
	text := strings.Repeat("字", 790) + link
	f := previewField(&text)
	if !f.Truncated || strings.Contains(f.Value, "https://") {
		t.Fatalf("链接跨过截取点应整条丢给后面: truncated=%v tail=%q", f.Truncated, f.Value[len(f.Value)-20:])
	}
}

// Bluesky 正文里写的是缩短后的显示文字，真地址在 facet：要换回完整地址。
func TestBlueskyShortenedLinkExpanded(t *testing.T) {
	post := "https://bsky.app/profile/example-lab.bsky.social/post/3mzhhvhjfwk22"
	full := "https://example.com/very/long/path/to/page.html?x=1"
	text := "前言 🙂 example.com/very/long/pa… 后记 点这里 结束"
	s1 := strings.Index(text, "example.com/very")
	e1 := s1 + len("example.com/very/long/pa…")
	s2 := strings.Index(text, "点这里")
	e2 := s2 + len("点这里")
	facets := `[{"index":{"byteStart":` + strconv.Itoa(s1) + `,"byteEnd":` + strconv.Itoa(e1) + `},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"` + full + `"}]},` +
		`{"index":{"byteStart":` + strconv.Itoa(s2) + `,"byteEnd":` + strconv.Itoa(e2) + `},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.org/other"}]}]`
	record := `{"text":"` + text + `","createdAt":"2026-11-01T00:00:00Z","facets":` + facets + `}`
	body := `{"thread":{"$type":"app.bsky.feed.defs#threadViewPost","post":{"uri":"at://` + bskySampleDID + `/app.bsky.feed.post/3mzhhvhjfwk22","author":{"did":"` + bskySampleDID + `","handle":"example-lab.bsky.social"},"record":` + record + `}}}`
	r, err := bskyFetch(t, post, body, 200)
	if err != nil {
		t.Fatal(err)
	}
	want := "前言 🙂 " + full + " 后记 点这里 结束"
	if r.Text.Value != want {
		t.Fatalf("缩短显示的链接应换回完整地址:\n got %q\nwant %q", r.Text.Value, want)
	}
}
