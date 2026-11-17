package content

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func expect(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func closeTo(a, b float32) bool { return math.Abs(float64(a-b)) <= 1 }
func box(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("missing target %q, texts=%q", label, tt.Texts())
	}
	return r
}
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}
func fixture(w, h int) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: 240, G: 80, B: 40, A: 255})
		}
	}
	return ui.NewBitmap(img)
}
func surface(c *ui.Context, fn func()) {
	theme.Apply(c, theme.Light())
	ui.Column(c).Fill().Background(theme.T(c).Colors().Window().Background()).Children(fn)
}
func pixel(tt *ui.Tester, x, y float32) color.RGBA { return tt.Image().RGBAAt(int(x), int(y)) }
func composite(fg ui.Color, bg ui.Color) color.RGBA {
	alpha := float32(fg.A) / 255
	return color.RGBA{R: uint8(math.Round(float64(float32(fg.R)*alpha + float32(bg.R)*(1-alpha)))), G: uint8(math.Round(float64(float32(fg.G)*alpha + float32(bg.G)*(1-alpha)))), B: uint8(math.Round(float64(float32(fg.B)*alpha + float32(bg.B)*(1-alpha)))), A: 255}
}
func sameColor(a, b color.RGBA) bool {
	return math.Abs(float64(a.R)-float64(b.R)) <= 1 && math.Abs(float64(a.G)-float64(b.G)) <= 1 && math.Abs(float64(a.B)-float64(b.B)) <= 1 && a.A == b.A
}

func TestQuoteGeometryExpansionAndDirection(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Quote()
	state := QuoteState{}
	opened := 0
	out, selected, disabled := false, false, false
	data := QuoteData{Name: "Aoi", Summary: "first\nsecond\nthird\nfourth\nfifth\nsixth", Multiline: true}
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Quote(c, data, &state, QuoteOptions{Appearance: Appearance{Outgoing: out, Selected: selected, Disabled: disabled}, Label: "quote", OnActivate: func() { opened++ }})
		})
	}, 300, 300)
	root, name, body := box(t, tt, "quote"), box(t, tt, "Aoi"), box(t, tt, data.Summary)
	// chat.style:73; reply.cpp:46; chat_helpers.style:1285/1287.
	expect(t, "quote left padding", closeTo(name.X-root.X, m.ReplyPaddingLeft()))
	expect(t, "quote top padding", closeTo(name.Y-root.Y, m.ReplyPaddingTop()))
	expect(t, "quote summary stacking", closeTo(body.Y, name.Y+name.H))
	expect(t, "quote five collapsed lines", closeTo(body.H, name.H*m.CollapsedLines()))
	click(t, tt, "quote")
	expect(t, "quote expand before navigation", state.Expanded && opened == 0)
	expect(t, "quote expansion grows", box(t, tt, "quote").H > root.H)
	click(t, tt, "quote")
	expect(t, "quote navigation", opened == 1)
	out = true
	selected = true
	tt.Frame()
	r := box(t, tt, "quote")
	bar := th.Colors().Quote().Out().BarSelected().Alpha(m.OutlineOpacity())
	bg := th.Colors().Quote().Out().BarSelected().Alpha(m.BackgroundOpacity())
	expect(t, "quote outgoing selected bar", sameColor(pixel(tt, r.X+1, r.Y+r.H/2), composite(bar, ui.Color{R: composite(bg, th.Colors().Window().Background()).R, G: composite(bg, th.Colors().Window().Background()).G, B: composite(bg, th.Colors().Window().Background()).B, A: 255})))
	expect(t, "quote rounded outer corner", sameColor(pixel(tt, r.X, r.Y), composite(ui.Color{}, th.Colors().Window().Background())))
	disabled = true
	tt.Frame()
	click(t, tt, "quote")
	expect(t, "quote disabled intent", opened == 1)
	data.Status = "Deleted original"
	tt.Frame()
	expect(t, "quote missing original status", tt.HasText("Deleted original") && !tt.HasText("sixth"))
}

func TestReactionLayoutIdentityAndInput(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Reaction()
	items := []Reaction{{ID: "heart", Emoji: "♥", Count: 12, Own: true}, {ID: "like", Emoji: "+", Count: 3}}
	var intents []string
	disabled := false
	menuID := ""
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Reactions(c, items, ReactionOptions{Appearance: Appearance{Disabled: disabled}, OnActivate: func(id string) { intents = append(intents, id) }, OnContextMenu: func(id string, menu *ui.Menu) {
				if menu.Item("Reactors").Chosen() {
					menuID = id
				}
			}}).FillWidth()
		})
	}, 300, 200)
	a, b := box(t, tt, "♥ 12"), box(t, tt, "+ 3")
	// chat.style:673-687; reactions.cpp:708.
	expect(t, "reaction height", closeTo(a.H, m.IconSize()+m.PaddingTop()+m.PaddingBottom()))
	expect(t, "reaction gap", closeTo(b.X-a.X-a.W, m.Gap()))
	expect(t, "reaction own fill", sameColor(pixel(tt, a.X+a.W/2, a.Y+1), composite(th.Colors().Reaction().In().OwnBackground(), th.Colors().Window().Background())))
	tt.Move(a.X+a.W/2, a.Y+a.H/2)
	expect(t, "reaction hover cursor", tt.Cursor() == ui.CursorPointer)
	click(t, tt, "♥ 12")
	expect(t, "reaction controlled intent", slices.Equal(intents, []string{"heart"}) && items[0].Count == 12 && items[0].Own)
	slices.Reverse(items)
	tt.Frame()
	tt.Key(0, ui.KeySpace)
	expect(t, "reaction reordered keyboard identity", slices.Equal(intents, []string{"heart", "heart"}))
	if err := tt.RightClick("+ 3"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Reactors"); err != nil {
		t.Fatal(err)
	}
	expect(t, "reaction context ID", menuID == "like")
	disabled = true
	tt.Frame()
	click(t, tt, "♥ 12")
	expect(t, "reaction disabled intent", len(intents) == 2)
	disabled = false
	tt.SetSize(50, 200)
	a, b = box(t, tt, "♥ 12"), box(t, tt, "+ 3")
	expect(t, "reaction wrapped spacing", closeTo(a.Y-b.Y-b.H, m.Gap()))
}

func TestRippleClockStops(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Reactions(c, []Reaction{{ID: "r", Emoji: "+", Count: 1}}, ReactionOptions{OnActivate: func(string) {}})
		})
	}, 200, 100)
	r := box(t, tt, "+ 1")
	base := pixel(tt, r.X+r.W/2, r.Y+1)
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(theme.Light().Motion().RippleShow().Duration())
	expect(t, "ripple spreads", pixel(tt, r.X+r.W/2, r.Y+1) != base)
	tt.Release(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(theme.Light().Motion().RippleHide().Duration() + time.Millisecond)
	expect(t, "ripple returns to rest", pixel(tt, r.X+r.W/2, r.Y+1) == base && !tt.FramePending())
}

func TestForwardAndPreviewActions(t *testing.T) {
	hidden := false
	opened := 0
	url := ""
	thumb := fixture(80, 120)
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Forward(c, ForwardData{Label: "Forwarded from", Source: "Aoi", Hidden: hidden}, ForwardOptions{OnOpen: func() { opened++ }})
			LinkPreview(c, PreviewData{Site: "Journal", Title: "A rehearsal", Description: "An invented description", URL: "https://example.invalid", Image: thumb, SmallImage: true}, PreviewOptions{OnOpen: func(u string) { url = u }}).Label("preview")
		})
	}, 350, 300)
	f := box(t, tt, "Forwarded from Aoi")
	p := box(t, tt, "preview")
	site := box(t, tt, "Journal")
	expect(t, "forward compact header", f.H < p.H)
	// chat.style:108,535; web_page.cpp:805.
	expect(t, "preview text inset", closeTo(site.X-p.X, theme.Light().Metrics().Preview().PaddingLeft()))
	expect(t, "preview top inset", closeTo(site.Y-p.Y, theme.Light().Metrics().Preview().PaddingTop()))
	click(t, tt, "Forwarded from Aoi")
	click(t, tt, "preview")
	expect(t, "forward and preview host intents", opened == 1 && url == "https://example.invalid")
	hidden = true
	tt.Frame()
	click(t, tt, "Forwarded from Aoi")
	expect(t, "forward hidden author inert", opened == 1)
}

func TestMarkdownFoldingSpoilersLinksAndCopy(t *testing.T) {
	state := MarkdownState{}
	shown := true
	source := "**Bold** and *italic*, `code`, [open](https://example.invalid).\n\n>^ one\n> two\n> three\n> four\n> five\n\n```go\nhello()\n```\n\n||secret **detail**||"
	link, code := "", ""
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			if shown {
				c.Key("message")
				Markdown(c, source, &state, MarkdownOptions{ExpandLabel: "Expand", CollapseLabel: "Collapse", RevealLabel: "Reveal", CopyLabel: "Copy", OnLink: func(u string) { link = u }, OnCopyCode: func(s string) { code = s }}).Label("body")
			}
		})
	}, 400, 500)
	collapsed := box(t, tt, "body").H
	expect(t, "markdown concealed plaintext absent", !tt.HasText("secret") && !tt.HasText("detail"))
	click(t, tt, "Expand")
	expect(t, "markdown expanded quote", box(t, tt, "body").H > collapsed)
	click(t, tt, "open")
	expect(t, "markdown link destination", link == "https://example.invalid")
	click(t, tt, "Copy")
	expect(t, "markdown code exact copy", code == "hello()\n")
	click(t, tt, "Reveal")
	tt.Advance(theme.Light().Motion().Fade().Duration())
	expect(t, "markdown spoiler reveal", state.SpoilersRevealed && tt.HasText("secret") && tt.HasText("detail"))
	shown = false
	tt.Frame()
	shown = true
	tt.Frame()
	expect(t, "markdown state survives removal", tt.HasText("Collapse") && tt.HasText("detail"))
	click(t, tt, "Collapse")
	expect(t, "markdown quote collapses again", closeTo(box(t, tt, "body").H, collapsed))
	source = "Updated `||literal||`"
	tt.Frame()
	expect(t, "markdown code delimiters literal", tt.HasText("||literal||") && !tt.HasText("hello()"))
}

func TestMediaSizesCornersAndIntents(t *testing.T) {
	bitmap := fixture(640, 400)
	status := MediaReady
	var calls []string
	selected := false
	corners := Corners{TopLeft: 0, TopRight: 16, BottomRight: 6, BottomLeft: 16}
	o := MediaOptions{Corners: &corners, OnOpen: func() { calls = append(calls, "open") }, OnDownload: func() { calls = append(calls, "download") }, OnCancel: func() { calls = append(calls, "cancel") }, OnRetry: func() { calls = append(calls, "retry") }}
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			o.Selected = selected
			Photo(c, Media{ID: "photo", Label: "photo", Bitmap: bitmap, Status: status}, o)
		})
	}, 500, 400)
	r := box(t, tt, "photo")
	m := theme.Light().Metrics().Media()
	// chat.style:169 and bubble radii chat.style:326-327.
	expect(t, "photo maximum size", closeTo(r.W, m.MaxSize()) && closeTo(r.H, m.MaxSize()*400/640))
	expect(t, "photo follows sharp corner", pixel(tt, r.X, r.Y).R == 240)
	expect(t, "photo follows rounded corner", pixel(tt, r.X+r.W-1, r.Y).R == 255)
	for _, s := range []MediaStatus{MediaReady, MediaRemote, MediaLoading, MediaFailed} {
		status = s
		tt.Frame()
		click(t, tt, "photo")
	}
	expect(t, "media status intents", slices.Equal(calls, []string{"open", "download", "cancel", "retry"}))
	selected = true
	tt.Frame()
	expect(t, "media selected overlay", pixel(tt, r.X+r.W/2, r.Y+r.H/2).R != 240)
	tt.SetSize(180, 400)
	r = box(t, tt, "photo")
	expect(t, "photo responsive ratio", closeTo(r.W, 180) && closeTo(r.H, r.W*400/640))
}

func TestStickerGIFVideoGeometry(t *testing.T) {
	bm := fixture(640, 400)
	shown := "sticker"
	opened := 0
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			media := Media{ID: "m", Label: "media", Bitmap: bm}
			o := MediaOptions{OnOpen: func() { opened++ }}
			switch shown {
			case "sticker":
				Sticker(c, media, o)
			case "gif":
				GIF(c, media, o)
			case "video":
				Video(c, media, "1:24", VideoOptions{MediaOptions: o, ShowPlay: true})
			}
		})
	}, 500, 400)
	m := theme.Light().Metrics().Media()
	r := box(t, tt, "media")
	expect(t, "sticker size bound", closeTo(r.W, m.MaxStickerSize()) && closeTo(r.H, m.MaxStickerSize()*400/640))
	shown = "gif"
	tt.Frame()
	r = box(t, tt, "media")
	expect(t, "gif size bound", closeTo(r.W, m.MaxGifSize()))
	tt.Move(499, 399)
	expect(t, "gif badge hidden at rest", !tt.HasText("GIF"))
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	expect(t, "gif hover badge", tt.HasText("GIF"))
	badge := box(t, tt, "GIF")
	expect(t, "gif badge inset", closeTo(badge.X-r.X, m.BadgeInset()+m.BadgePaddingX()) && closeTo(badge.Y-r.Y, m.BadgeInset()+m.BadgePaddingY()))
	shown = "video"
	tt.Frame()
	r = box(t, tt, "media")
	expect(t, "video duration and cover bound", tt.HasText("1:24") && closeTo(r.W, m.MaxSize()))
	click(t, tt, "media")
	expect(t, "video play intent", opened == 1)
}

func TestSelectedStickerPreservesTransparentMargins(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 20; y < 80; y++ {
		for x := 20; x < 80; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 240, G: 80, B: 40, A: 255})
		}
	}
	bm := ui.NewBitmap(img)
	selected := false
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Sticker(c, Media{ID: "sticker", Label: "sticker", Bitmap: bm}, MediaOptions{Appearance: Appearance{Selected: selected}})
		})
	}, 200, 200)
	selected = true
	tt.Frame()
	r := box(t, tt, "sticker")
	expect(t, "selected sticker tint", sameColor(pixel(tt, r.X+50, r.Y+50), composite(theme.Light().Colors().Message().StickerOverlay(), ui.Color{R: 240, G: 80, B: 40, A: 255})))
	expect(t, "selected sticker transparent margin", sameColor(pixel(tt, r.X+1, r.Y+1), composite(ui.Color{}, theme.Light().Colors().Window().Background())))
}

func TestAlbumGeometryAndStableTileID(t *testing.T) {
	items := []Media{{ID: "a", Label: "a", Width: 640, Height: 400, Bitmap: fixture(64, 40)}, {ID: "b", Label: "b", Width: 640, Height: 400, Bitmap: fixture(64, 40)}}
	opened := ""
	width := float32(430)
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Album(c, items, AlbumOptions{Width: width, OnOpen: func(id string) { opened = id }}).Label("album")
		})
	}, 500, 600)
	a, b := box(t, tt, "a"), box(t, tt, "b")
	m := theme.Light().Metrics().Media()
	// grouped_layout.cpp:151-154 chooses two wide photographs above/below.
	expect(t, "album two wide topology", a.X == b.X && closeTo(a.W, m.MaxSize()) && closeTo(b.Y-a.Y-a.H, m.AlbumGap()))
	click(t, tt, "b")
	expect(t, "album tile intent", opened == "b")
	slices.Reverse(items)
	tt.Frame()
	tt.Key(0, ui.KeySpace)
	expect(t, "album reordered focused ID", opened == "b")
	width = 215
	tt.Frame()
	a, b = box(t, tt, "a"), box(t, tt, "b")
	expect(t, "album proportional gap", closeTo(a.Y-b.Y-b.H, m.AlbumGap()/2))
	for n := 3; n <= 10; n++ {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			photos := make([]Media, n)
			for i := range photos {
				photos[i] = Media{ID: string(rune('a' + i)), Width: float32(320 + (i%3)*100), Height: float32(320 + ((i+1)%3)*100)}
			}
			cache := albumCache{}
			cache.update(photos, m.MaxSize(), m.MinPhotoSize(), m.AlbumGap())
			valid := cache.width <= m.MaxSize()+1 && cache.height > 0
			for i, r := range cache.tiles {
				valid = valid && r.W > 0 && r.H > 0
				for j := range i {
					other := cache.tiles[j]
					overlap := min(r.X+r.W, other.X+other.W) > max(r.X, other.X) && min(r.Y+r.H, other.Y+other.H) > max(r.Y, other.Y)
					valid = valid && !overlap
				}
			}
			expect(t, "album positive nonoverlapping crops", valid)
		})
	}
}

func TestFileVoiceLayoutPlaybackSeekAndHover(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().File()
	data := FileData{Name: "notes.pdf", Size: "2.4 MB"}
	voice := VoiceState{}
	toggled, opened := 0, 0
	linkClicks := 0
	seek := -1.0
	disabled := false
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			onOpen := func() { opened++ }
			if disabled {
				onOpen = nil
			}
			File(c, data, FileOptions{Appearance: Appearance{Disabled: disabled}, OnOpen: onOpen, OnLink: func() { linkClicks++ }}).Label("file")
			Voice(c, VoiceData{Duration: "0:42", Samples: []float32{0, .5, 1, .2}}, &voice, VoiceOptions{OnToggle: func() { toggled++ }, OnSeek: func(p float64) { seek = p }}).Label("voice")
		})
	}, 350, 200)
	f, n, size := box(t, tt, "file"), box(t, tt, "notes.pdf"), box(t, tt, "2.4 MB")
	// chat.style:380-385; waveform bar/min/max:429-432.
	expect(t, "file height", closeTo(f.H, m.PaddingTop()+m.IconSize()+m.PaddingBottom()))
	expect(t, "file name placement", closeTo(n.X-f.X, m.PaddingLeft()+m.IconSize()+m.IconGap()) && closeTo(n.Y-f.Y, m.NameTop()))
	expect(t, "file size placement", closeTo(size.Y-f.Y, m.StatusTop()))
	before := pixel(tt, f.X+m.PaddingLeft()+m.IconSize()/2, f.Y+m.PaddingTop()+1)
	tt.Move(f.X+f.W/2, f.Y+f.H/2)
	tt.Advance(th.Motion().FileHover().Duration() / 2)
	mid := pixel(tt, f.X+m.PaddingLeft()+m.IconSize()/2, f.Y+m.PaddingTop()+1)
	tt.Advance(th.Motion().FileHover().Duration()/2 + time.Millisecond)
	after := pixel(tt, f.X+m.PaddingLeft()+m.IconSize()/2, f.Y+m.PaddingTop()+1)
	expect(t, "file hover midpoint and completion", mid != before && mid != after && sameColor(after, composite(th.Colors().Message().FileInBackgroundHover(), th.Colors().Window().Background())))
	click(t, tt, "file")
	click(t, tt, "0:42")
	expect(t, "file and voice host intents", opened == 1 && toggled == 1)
	wave := box(t, tt, "Seek voice")
	tt.Press(wave.X+wave.W/4, wave.Y+wave.H/2)
	tt.Move(wave.X+wave.W*3/4, wave.Y+wave.H/2)
	tt.Release(wave.X+wave.W*3/4, wave.Y+wave.H/2)
	expect(t, "voice drag seek", math.Abs(voice.Position-.75) < .02 && math.Abs(seek-.75) < .02)
	tt.Key(0, ui.KeyHome)
	expect(t, "voice keyboard seek boundary", voice.Position == 0 && seek == 0)
	tt.Key(0, ui.KeyEnd)
	expect(t, "voice keyboard seek end", voice.Position == 1 && seek == 1)
	data.Thumbnail = fixture(72, 72)
	data.Link = "Save as"
	tt.Frame()
	f, n, size = box(t, tt, "file"), box(t, tt, "notes.pdf"), box(t, tt, "2.4 MB")
	// chat.style:387-393: thumbnail uses its own padding, baselines and link row.
	expect(t, "file thumbnail dimensions", closeTo(f.H, m.ThumbPaddingTop()+m.ThumbSize()+m.ThumbPaddingBottom()))
	expect(t, "file thumbnail name baseline", closeTo(n.X-f.X, m.ThumbPaddingLeft()+m.ThumbSize()+m.ThumbGap()) && closeTo(n.Y-f.Y, m.ThumbNameTop()))
	expect(t, "file thumbnail status baseline", closeTo(size.Y-f.Y, m.ThumbStatusTop()))
	expect(t, "file thumbnail link baseline", closeTo(box(t, tt, "Save as").Y-f.Y, m.ThumbLinkTop()))
	click(t, tt, "Save as")
	expect(t, "file thumbnail link intent", linkClicks == 1 && opened == 1)
	disabled = true
	tt.Frame()
	click(t, tt, "Save as")
	expect(t, "file passive disabled link", linkClicks == 1)
}

func TestMarkdownTypographyUsesThemeOnce(t *testing.T) {
	definition := theme.Definition{Version: theme.Version, Base: theme.LightBase, Typography: map[string]theme.TextDefinition{"message": {Size: new(float32(20))}}}
	th, err := theme.Compile(definition)
	if err != nil {
		t.Fatal(err)
	}
	state := MarkdownState{}
	height := float32(0)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		Markdown(c, "Plain text", &state, MarkdownOptions{})
		_, height = c.MeasureText(0, ui.Span{Text: "Plain text", Size: th.Typography().Message().Size() * c.Preferences().TextScale, Font: th.Typography().Font()})
	}, 300, 100)
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	expect(t, "markdown single text scale", closeTo(box(t, tt, "Plain text").H, height))
}

func TestMarkdownSelectionVisibleAndCopyable(t *testing.T) {
	state := MarkdownState{}
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() { Markdown(c, "Alpha Beta Gamma", &state, MarkdownOptions{}) })
	}, 300, 100)
	r := box(t, tt, "Alpha Beta Gamma")
	tt.ClickAt(r.X+2, r.Y+r.H/2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	expect(t, "markdown selected text copy", tt.Clipboard() == "Alpha Beta Gamma")
	// colors.palette: msgInBgSelected is the incoming text selection fill.
	expect(t, "markdown visible selection fill", sameColor(pixel(tt, r.X+1, r.Y+r.H-1), composite(theme.Light().Colors().Message().In().BackgroundSelected(), theme.Light().Colors().Window().Background())))
}

func TestTransferRingMotionAndReducedMotion(t *testing.T) {
	status := MediaLoading
	bitmap := fixture(64, 40)
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Photo(c, Media{ID: "photo", Bitmap: bitmap, Status: status, Progress: .25}, MediaOptions{})
		})
	}, 200, 200)
	period := theme.Light().Motion().MediaTransferRotate().Duration()
	initial := append([]byte(nil), tt.Image().Pix...)
	tt.Advance(period / 4)
	expect(t, "transfer ring rotates", !bytes.Equal(initial, tt.Image().Pix))
	tt.Advance(period * 3 / 4)
	expect(t, "transfer ring period", bytes.Equal(initial, tt.Image().Pix))
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	still := append([]byte(nil), tt.Image().Pix...)
	tt.Advance(period / 4)
	expect(t, "transfer ring reduced motion static", bytes.Equal(still, tt.Image().Pix) && !tt.FramePending())
	tt.SetPreferences(ui.Preferences{})
	status = MediaReady
	tt.Frame()
	expect(t, "transfer ring stops after ready", !tt.FramePending())
}
