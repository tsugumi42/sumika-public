package gallery

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "content/quote", Area: "content", Title: "Reply quote", View: contentQuotes})
	Register(Page{ID: "content/reactions", Area: "content", Title: "Reactions", View: contentReactions})
	Register(Page{ID: "content/forward", Area: "content", Title: "Forward source", View: contentForwards})
	Register(Page{ID: "content/preview", Area: "content", Title: "Link preview", View: contentPreviews})
	Register(Page{ID: "content/markdown", Area: "content", Title: "Markdown", View: contentMarkdown})
	Register(Page{ID: "content/photo", Area: "content", Title: "Photo", View: contentPhotos})
	Register(Page{ID: "content/album", Area: "content", Title: "Album", View: contentAlbums})
	Register(Page{ID: "content/sticker", Area: "content", Title: "Sticker", View: contentStickers})
	Register(Page{ID: "content/gif", Area: "content", Title: "GIF", View: contentGIFs})
	Register(Page{ID: "content/video", Area: "content", Title: "Video cover", View: contentVideos})
	Register(Page{ID: "content/file", Area: "content", Title: "File", View: contentFiles})
	Register(Page{ID: "content/voice", Area: "content", Title: "Voice", View: contentVoices})
	Register(Page{ID: "content/composed", Area: "content", Title: "Composed message", View: contentComposed})
	Register(Page{ID: "content/particles", Area: "content", Title: "Spoiler particles", View: contentParticles})
	Register(Page{ID: "content/crop", Area: "content", Title: "Caption media cropping", View: contentCrop})
	Register(Page{ID: "content/transfers", Area: "content", Title: "Transfer catchup", View: contentTransfers})
}

type contentDemoKey struct{}
type contentDemo struct {
	event   string
	quotes  [8]content.QuoteState
	md      [4]content.MarkdownState
	voice   [4]content.VoiceState
	playing [4]bool
	spoiler bool
}

func contentPage(c *ui.Context, view func(*contentDemo)) {
	root := ui.Scroll(c).Fill()
	state := ui.Local(root, contentDemoKey{}, func() contentDemo { return contentDemo{spoiler: true} })
	t := theme.T(c)
	m := t.Metrics().Message()
	root.Children(func() {
		ui.Column(c).FillWidth().Padding(m.PaddingTop(), m.PaddingLeft()).Gap(m.PaddingTop()).AlignItems(ui.Start).Children(func() {
			ui.Text(c, state.event).TextColor(t.Colors().Window().MutedText())
			view(state)
		})
	})
}
func contentSample(c *ui.Context, id string, a content.Appearance, view func()) {
	c.Key(id)
	t := theme.T(c)
	m := t.Metrics().Message()
	bg := t.Colors().Message().In().Background()
	if a.Outgoing {
		bg = t.Colors().Message().Out().Background()
	}
	if a.Selected {
		bg = t.Colors().Message().In().BackgroundSelected()
		if a.Outgoing {
			bg = t.Colors().Message().Out().BackgroundSelected()
		}
	}
	ui.Column(c).Width(m.MaxWidth()).MaxWidthPercent(100).Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Radius(m.RadiusLarge()).Background(bg).Gap(m.PaddingTop()).Children(view)
}

var contentBitmap = sync.OnceValue(func() *ui.Bitmap {
	// Invented color ramps; no downloaded or third-party artwork.
	img := image.NewRGBA(image.Rect(0, 0, 640, 400))
	for y := range 400 {
		for x := range 640 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / 640), G: uint8(y * 255 / 400), B: 160, A: 255})
		}
	}
	return ui.NewBitmap(img)
})
var contentSticker = sync.OnceValue(func() *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, 224, 224))
	for y := range 224 {
		for x := range 224 {
			dx, dy := x-112, y-112
			if dx*dx+dy*dy < 100*100 {
				img.SetRGBA(x, y, color.RGBA{R: 92, G: 175, B: 220, A: 255})
			}
		}
	}
	return ui.NewBitmap(img)
})

func demoMedia(id string) content.Media {
	return content.Media{ID: id, Label: id, Bitmap: contentBitmap()}
}
func contentQuotes(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}, {Disabled: true}} {
			contentSample(c, fmt.Sprint("reply", i), a, func() {
				content.Quote(c, content.QuoteData{Name: "Aoi", Summary: "The rehearsal starts after lunch.", Preview: contentBitmap()}, &s.quotes[i], content.QuoteOptions{Appearance: a, Label: "Reply " + fmt.Sprint(i), OnActivate: func() { s.event = "Jump to quoted message" }})
			})
		}
		contentSample(c, "expanded", content.Appearance{}, func() {
			content.Quote(c, content.QuoteData{Name: "Long quotation", Multiline: true, Summary: "first line\nsecond line\nthird line\nfourth line\nfifth line\nsixth line"}, &s.quotes[5], content.QuoteOptions{Label: "Expandable reply", OnActivate: func() { s.event = "Open expanded quote" }})
		})
		for i, status := range []string{"Loading original message…", "Original message deleted"} {
			contentSample(c, fmt.Sprint("unavailable", i), content.Appearance{}, func() {
				content.Quote(c, content.QuoteData{Name: "Aoi", Status: status}, &s.quotes[i+6], content.QuoteOptions{})
			})
		}
	})
}
func contentReactions(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}, {Disabled: true}} {
			contentSample(c, fmt.Sprint("reactions", i), a, func() {
				content.Reactions(c, []content.Reaction{{ID: "heart", Emoji: "♥", Count: 12, Own: true}, {ID: "like", Emoji: "👍", Count: 3}, {ID: "image", Emoji: "Custom", Count: 2, Image: contentSticker()}, {ID: "recent", Emoji: "😊", Count: 2, Userpics: []content.ReactionUserpic{{ID: "aoi", Image: contentSticker()}, {ID: "rin", Image: contentBitmap()}}}}, content.ReactionOptions{Appearance: a, OnActivate: func(id string) { s.event = "Reaction intent: " + id }, OnContextMenu: func(id string, m *ui.Menu) {
					if m.Item("View reactors").Chosen() {
						s.event = "Reactors for " + id
					}
				}})
			})
		}
	})
}
func contentForwards(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}} {
			contentSample(c, fmt.Sprint("forward", i), a, func() {
				content.Forward(c, content.ForwardData{Label: "Forwarded from", Source: "Aoi", Via: "via @music_bot"}, content.ForwardOptions{Appearance: a, OnOpen: func() { s.event = "Open source" }})
			})
		}
		contentSample(c, "hidden", content.Appearance{}, func() {
			content.Forward(c, content.ForwardData{Label: "Forwarded from", Source: "Hidden account", Hidden: true}, content.ForwardOptions{})
		})
	})
}
func contentPreviews(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}} {
			contentSample(c, fmt.Sprint("preview", i), a, func() {
				content.LinkPreview(c, content.PreviewData{Site: "Example Journal", Title: "A new rehearsal room", Description: "An invented article about preparing a small concert and arranging instruments.", URL: "https://example.invalid/article", Image: contentBitmap(), SmallImage: i%2 == 0}, content.PreviewOptions{Appearance: a, OnOpen: func(url string) { s.event = "Open " + url }})
			})
		}
		contentSample(c, "loading", content.Appearance{}, func() {
			content.LinkPreview(c, content.PreviewData{URL: "https://example.invalid", Status: "Loading preview…"}, content.PreviewOptions{})
		})
		contentSample(c, "text-only", content.Appearance{}, func() {
			content.LinkPreview(c, content.PreviewData{Site: "Example", Title: "Text preview", Description: "This link has no thumbnail."}, content.PreviewOptions{})
		})
	})
}

const contentMarkdownSource = "**Bold**, *italic*, ~~strike~~, `inline code` and [a link](https://example.invalid).\n\n> Quoted paragraph\n> with another line.\n\n>^ Collapsed first line\n> second line\n> third line\n> fourth line\n> fifth line\n\n```go\nfmt.Println(\"hello\")\n```\n\nA ||concealed **detail**|| in the message."

func contentMarkdown(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		services := c.Services()
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}} {
			contentSample(c, fmt.Sprint("markdown", i), a, func() {
				content.Markdown(c, contentMarkdownSource, &s.md[i], content.MarkdownOptions{Appearance: a, ExpandLabel: "Expand quote", CollapseLabel: "Collapse quote", RevealLabel: "Reveal spoiler", CopyLabel: "Copy code", OnLink: func(url string) { s.event = "Link intent: " + url }, OnCopyCode: func(code string) { services.WriteClipboard(code); s.event = "Code copied" }})
			})
		}
	})
}
func contentPhotos(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, status := range []content.MediaStatus{content.MediaReady, content.MediaRemote, content.MediaLoading, content.MediaFailed} {
			m := demoMedia(fmt.Sprint("Photo ", i))
			m.Status = status
			m.StatusText, m.Progress = []string{"", "Download", "42%", "Retry"}[i], .42
			content.Photo(c, m, content.MediaOptions{OnOpen: func() { s.event = "Open photo" }, OnDownload: func() { s.event = "Download photo" }, OnCancel: func() { s.event = "Cancel download" }, OnRetry: func() { s.event = "Retry photo" }})
		}
		content.Photo(c, demoMedia("Selected photo"), content.MediaOptions{Appearance: content.Appearance{Selected: true}, OnOpen: func() { s.event = "Open selected photo" }})
		spoiler := demoMedia("Spoiler photo")
		spoiler.StatusText = "Reveal photo"
		content.Photo(c, spoiler, content.MediaOptions{Spoiler: &s.spoiler})
	})
}
func contentAlbums(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for n := 2; n <= 10; n++ {
			items := make([]content.Media, n)
			for i := range items {
				items[i] = demoMedia(fmt.Sprintf("Album %d photo %d", n, i+1))
				items[i].Width = float32(320 + (i%3)*160)
				items[i].Height = float32(320 + ((i+n)%3)*160)
			}
			c.Key(n)
			content.Album(c, items, content.AlbumOptions{OnOpen: func(id string) { s.event = id }})
		}
	})
}
func contentStickers(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		content.Sticker(c, content.Media{ID: "sticker", Label: "Sticker", Bitmap: contentSticker()}, content.MediaOptions{OnOpen: func() { s.event = "Open sticker set" }})
		content.Sticker(c, content.Media{ID: "selected-sticker", Label: "Selected sticker", Bitmap: contentSticker()}, content.MediaOptions{Appearance: content.Appearance{Selected: true}, OnOpen: func() { s.event = "Open selected sticker set" }})
		content.Sticker(c, content.Media{ID: "remote-sticker", Label: "Remote sticker", Width: 224, Height: 224, Status: content.MediaRemote}, content.MediaOptions{OnDownload: func() { s.event = "Download sticker" }})
	})
}
func contentGIFs(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		content.GIF(c, demoMedia("Hover GIF"), content.MediaOptions{OnOpen: func() { s.event = "Play GIF" }})
		m := demoMedia("Transferring GIF")
		m.Status = content.MediaLoading
		m.StatusText, m.Progress = "42%", .42
		content.GIF(c, m, content.MediaOptions{OnCancel: func() { s.event = "Cancel GIF" }})
	})
}
func contentVideos(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, status := range []content.MediaStatus{content.MediaReady, content.MediaRemote, content.MediaLoading, content.MediaFailed} {
			m := demoMedia(fmt.Sprint("Video ", i))
			m.Status = status
			content.Video(c, m, "1:24", content.VideoOptions{ShowPlay: true, MediaOptions: content.MediaOptions{OnOpen: func() { s.event = "Play video" }, OnDownload: func() { s.event = "Download video" }, OnCancel: func() { s.event = "Cancel video" }, OnRetry: func() { s.event = "Retry video" }}})
		}
		ui.Text(c, "未知时长保留播放标记；角标与标记可分别开关。")
		content.Video(c, demoMedia("无时长，显示播放"), "", content.VideoOptions{ShowPlay: true, MediaOptions: content.MediaOptions{OnOpen: func() { s.event = "播放未知时长的视频" }}})
		content.Video(c, demoMedia("有角标，隐藏播放"), "视频", content.VideoOptions{ShowPlay: false})
	})
}
func contentFiles(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}} {
			contentSample(c, fmt.Sprint("file", i), a, func() {
				content.File(c, content.FileData{Name: "Rehearsal notes.pdf", Size: "2.4 MB", Status: content.MediaStatus(i), Progress: .42}, content.FileOptions{Appearance: a, OnOpen: func() { s.event = "Open file" }, OnDownload: func() { s.event = "Download file" }, OnCancel: func() { s.event = "Cancel file" }, OnRetry: func() { s.event = "Retry file" }})
			})
		}
		contentSample(c, "thumb-file", content.Appearance{}, func() {
			content.File(c, content.FileData{Name: "Stage sketch.png", Size: "842 KB", Thumbnail: contentBitmap(), Link: "Save as…"}, content.FileOptions{OnOpen: func() { s.event = "Open sketch" }, OnLink: func() { s.event = "Save sketch" }})
		})
	})
}
func contentVoices(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}} {
			contentSample(c, fmt.Sprint("voice", i), a, func() {
				samples := []float32{.1, .4, .8, .2, 1, .3, .6, .9, .4, .2}
				if i == 0 {
					samples = nil
				}
				content.Voice(c, content.VoiceData{Duration: "0:42", Samples: samples, Playing: s.playing[i]}, &s.voice[i], content.VoiceOptions{Appearance: a, OnToggle: func() { s.playing[i] = !s.playing[i]; s.event = "Toggle voice" }, OnSeek: func(pos float64) { s.event = fmt.Sprintf("Seek %.2f", pos) }})
			})
		}
		for i, status := range []content.MediaStatus{content.MediaRemote, content.MediaLoading, content.MediaFailed} {
			c.Key(status)
			content.Voice(c, content.VoiceData{Duration: "0:42", StatusText: []string{"Download voice", "Waiting for voice", "Retry voice"}[i], Status: status}, &s.voice[i], content.VoiceOptions{OnDownload: func() { s.event = "Download voice" }, OnCancel: func() { s.event = "Cancel voice" }, OnRetry: func() { s.event = "Retry voice" }})
		}
	})
}
func contentComposed(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		contentSample(c, "message", content.Appearance{Outgoing: true}, func() {
			content.Forward(c, content.ForwardData{Label: "Forwarded from", Source: "Aoi"}, content.ForwardOptions{Appearance: content.Appearance{Outgoing: true}, OnOpen: func() { s.event = "Open source" }})
			content.Quote(c, content.QuoteData{Name: "Rin", Summary: "Have you seen the room?"}, &s.quotes[0], content.QuoteOptions{Appearance: content.Appearance{Outgoing: true}, Label: "Composed reply", OnActivate: func() { s.event = "Jump to reply" }})
			content.Markdown(c, "**Ready for rehearsal.** [Room details](https://example.invalid)", &s.md[0], content.MarkdownOptions{Appearance: content.Appearance{Outgoing: true}, OnLink: func(url string) { s.event = url }})
			content.Photo(c, demoMedia("Room photo"), content.MediaOptions{InBubble: true, Caption: "Ready for rehearsal. Room details", OnOpen: func() { s.event = "Open room photo" }})
			content.Reactions(c, []content.Reaction{{ID: "yes", Emoji: "👍", Count: 3, Own: true}}, content.ReactionOptions{Appearance: content.Appearance{Outgoing: true}, OnActivate: func(id string) { s.event = id }})
		})
	})
}

func contentParticles(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		ui.Button(c, "Conceal again").OnClick(func() {
			for i := range s.md {
				s.md[i].SpoilersRevealed = false
			}
		})
		for i, a := range []content.Appearance{{}, {Outgoing: true}, {Selected: true}, {Outgoing: true, Selected: true}} {
			contentSample(c, fmt.Sprint("particle", i), a, func() {
				content.Markdown(c, "A ||concealed paragraph with moving particles and 中文换行。 "+strings.Repeat("Prepared hidden text. ", 8)+"||", &s.md[i], content.MarkdownOptions{Appearance: a, RevealLabel: "Reveal particles"})
			})
		}
	})
}

func contentCrop(c *ui.Context) {
	contentPage(c, func(s *contentDemo) {
		caption := "A portrait with a caption that needs the full message width."
		for _, h := range []float32{600, 1600} {
			content.Photo(c, content.Media{ID: fmt.Sprint("portrait", h), Label: fmt.Sprint("Portrait ", h), Bitmap: contentBitmap(), Width: 400, Height: h}, content.MediaOptions{Width: theme.T(c).Metrics().Media().MaxSize(), InBubble: true, Caption: caption})
			ui.Text(c, caption)
		}
		content.GIF(c, content.Media{ID: "portrait-gif", Label: "GIF crop cap", Bitmap: contentBitmap(), Width: 400, Height: 1600}, content.MediaOptions{InBubble: true, Caption: caption})
	})
}

func contentTransfers(c *ui.Context) {
	progress := ui.Local(c.Root(), "content.transfer.target", func() float64 { return .1 })
	contentPage(c, func(s *contentDemo) {
		ui.Row(c).Gap(theme.T(c).Metrics().Control().Spacing()).Children(func() {
			for _, v := range []float64{0, .25, .8, 1} {
				ui.Button(c, fmt.Sprintf("Target %.0f%%", v*100)).OnClick(func() { *progress = v })
			}
		})
		content.File(c, content.FileData{Name: "Prepared archive.zip", Size: "Target progress", Status: content.MediaLoading, Progress: float32(*progress)}, content.FileOptions{OnCancel: func() { s.event = "Cancel transfer" }})
		m := demoMedia("Transfer photo")
		m.Status = content.MediaLoading
		m.Progress = float32(*progress)
		content.Photo(c, m, content.MediaOptions{OnCancel: func() { s.event = "Cancel photo transfer" }})
	})
}
