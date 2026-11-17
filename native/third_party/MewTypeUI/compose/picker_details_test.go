package compose

import (
	"bytes"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestPickerUnicodeAndLocalizedStates(t *testing.T) {
	state := PickerState{}
	data := PickerData{{Categories: []Category{{ID: "recent", Label: "最近使用", Icon: pickerAllIcon, Items: []PickerItem{{ID: "wave", Label: "挥手", Emoji: "👋", Keywords: "hello"}}}}}}
	selected := ""
	tt := ui.NewTester(view(func(c *ui.Context) {
		Picker(c, &state, data, PickerOptions{OnSelect: func(_ PickerTab, item PickerItem) { selected = item.Emoji }})
	}), 400, 400)
	tt.SetLocale("zh-CN")
	cell := bounds(t, tt, "挥手")
	glyphBox := bounds(t, tt, "👋")
	actual := tt.Image()
	// A Unicode emoji uses the text renderer; compare the actual tile to an
	// independent text control of the source emojiPanArea=32 size.
	calibration := ui.NewTester(view(func(c *ui.Context) {
		ui.Text(c, "👋").Absolute().Left(glyphBox.X).Top(glyphBox.Y).FontSize(theme.Light().Metrics().Picker().ImageSize()).SingleLine()
	}), 400, 400)
	calibration.SetLocale("zh-CN")
	calibrated := calibration.Image()
	crop := image.Rect(int(cell.X), int(cell.Y), int(cell.X+cell.W), int(cell.Y+cell.H))
	var glyph, expected []byte
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			p, q := actual.RGBAAt(x, y), calibrated.RGBAAt(x, y)
			glyph = append(glyph, p.R, p.G, p.B, p.A)
			expected = append(expected, q.R, q.G, q.B, q.A)
		}
	}
	expect(t, "unicode-glyph-matches-text-renderer", bytes.Equal(glyph, expected))
	click(t, tt, "挥手")
	expect(t, "unicode-selection-delivers-sequence", selected == "👋")
	click(t, tt, "搜索表情与媒体")
	tt.Type("missing")
	expect(t, "chinese-empty-result", tt.HasText("没有结果"))
	for _, tc := range []struct {
		state            PickerPageState
		chinese, english string
	}{
		{PageLoading, "加载中…", "Loading…"},
		{PageFailed, "搜索失败，请重试", "Search failed — try again"},
		{PageRestricted, "此聊天禁止发送贴纸", "Stickers restricted in this chat"},
	} {
		data[0].State = tc.state
		tt.Frame()
		expect(t, "chinese-picker-state", tt.HasText(tc.chinese))
		tt.SetLocale("en")
		expect(t, "english-picker-state", tt.HasText(tc.english))
		tt.SetLocale("zh-Hans")
	}
	data[0].Status = "宿主错误详情"
	tt.Frame()
	expect(t, "host-status-overrides-standard-state", tt.HasText("宿主错误详情") && !tt.HasText("此聊天禁止发送贴纸"))
}

func TestPickerFooterIconSlotsAndTextFallback(t *testing.T) {
	state := PickerState{}
	data := PickerData{{Categories: []Category{{ID: "recent", Label: "Recent", HideHeader: true, Icon: pickerAllIcon}, {ID: "nature", Label: "Nature", HideHeader: true, Icon: pickerAllIcon}, {ID: "custom", Label: "A custom category name", HideHeader: true}}}}
	tt := ui.NewTester(view(func(c *ui.Context) { Picker(c, &state, data, PickerOptions{}) }), 500, 400)
	m := theme.Light().Metrics().Picker()
	all, recent, nature, custom := bounds(t, tt, "All"), bounds(t, tt, "Recent"), bounds(t, tt, "Nature"), bounds(t, tt, "A custom category name")
	// chat_helpers.style:806-809; stickers_list_footer.cpp:319-321,1148-1152.
	expect(t, "footer-edge-skip", near(all.X, m.CategoryGap()))
	expect(t, "footer-icon-slots", near(all.W, m.CategoryWidth()) && near(recent.W, m.CategoryWidth()) && near(nature.W, m.CategoryWidth()) && near(all.H, m.FooterHeight()))
	expect(t, "footer-icons-adjoin-without-labels", near(recent.X, all.X+all.W) && near(nature.X, recent.X+recent.W))
	expect(t, "footer-text-fallback-fits", custom.W > m.CategoryWidth()+2*m.CategoryGap())
	click(t, tt, "Nature")
	expect(t, "footer-scrolls-by-category-id", state.Category[0] == "nature")
}

func TestPickerSlideFadesOverlappingPages(t *testing.T) {
	state := PickerState{}
	data := PickerData{
		{Categories: []Category{{ID: "a", HideHeader: true, Items: []PickerItem{{ID: "red", Label: "Red", Image: testArt}}}}},
		{Categories: []Category{{ID: "b", HideHeader: true, Items: []PickerItem{{ID: "gold", Label: "Gold", Image: testArt}}}}},
	}
	tt := ui.NewTester(view(func(c *ui.Context) { Picker(c, &state, data, PickerOptions{}) }), 500, 400)
	state.Tab = StickerTab
	tt.Frame()
	tt.Advance(theme.Light().Motion().PickerSlide().Duration() / 2)
	tt.Frame()
	r := bounds(t, tt, "Gold")
	p := tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
	// tabbed_selector.cpp:148-160: entering alpha=1-sqrt(1-t²), at t=.5.
	expect(t, "picker-arriving-page-is-translucent", p.R > 0xd7 && p.G > 0xa6 && p.B > 0x46)
	tt.Advance(theme.Light().Motion().PickerSlide().Duration()/2 + time.Millisecond)
	tt.Frame()
	r = bounds(t, tt, "Gold")
	p = tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
	expect(t, "picker-arriving-page-finishes-opaque", p.R == 0xd7 && p.G == 0xa6 && p.B == 0x46 && !tt.FramePending())
}
