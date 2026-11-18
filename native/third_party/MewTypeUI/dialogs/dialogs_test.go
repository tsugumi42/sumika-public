package dialogs_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func near(a, b float32) bool                    { return math.Abs(float64(a-b)) <= 1 }
func found(tt *ui.Tester, label string) ui.Rect { r, _ := tt.Find(label); return r }
func pixel(tt *ui.Tester, x, y int) ui.Color {
	c := color.NRGBAModel.Convert(tt.Image().At(x, y)).(color.NRGBA)
	return ui.Color{R: c.R, G: c.G, B: c.B, A: c.A}
}
func colorBounds(img image.Image, area image.Rectangle, want ui.Color) ui.Rect {
	return boundsWhere(img, area, func(c ui.Color) bool { return c == want })
}

func boundsWhere(img image.Image, area image.Rectangle, match func(ui.Color) bool) ui.Rect {
	minx, miny, maxx, maxy := area.Max.X, area.Max.Y, area.Min.X, area.Min.Y
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if match(ui.Color{R: c.R, G: c.G, B: c.B, A: c.A}) {
				minx = min(minx, x)
				maxx = max(maxx, x+1)
				miny = min(miny, y)
				maxy = max(maxy, y+1)
			}
		}
	}
	return ui.Rect{X: float32(minx), Y: float32(miny), W: float32(max(0, maxx-minx)), H: float32(max(0, maxy-miny))}
}

func TestRowGeometryAndInteraction(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Conversation()
	item := dialogs.Conversation{ID: "one", Name: "Rin", Initials: "R", Preview: "Preview", Time: "09:41", Unread: 12}
	selected, clicks := false, 0
	var titleHeight float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		_, titleHeight = c.MeasureText(0, ui.Span{Text: item.Name, Size: th.Typography().ConversationTitle().Size(), Weight: th.Typography().ConversationTitle().Weight()})
		dialogs.Row(c, item, dialogs.RowOptions{Selected: selected, OnActivate: func(id string) {
			if id == item.ID {
				clicks++
			}
		}}).Label("row")
	}, 360, 240)
	// dialogs.style:91-102,78-80: continuous 62 DIP rows,46 DIP avatar.
	check(t, "row height", near(found(tt, "row").H, m.RowHeight()))
	check(t, "title left", near(found(tt, "Rin").X, m.TitleLeft()))
	check(t, "title top", near(found(tt, "Rin").Y, m.TitleTop()))
	check(t, "title type height", near(found(tt, "Rin").H, titleHeight))
	check(t, "preview left", near(found(tt, "Preview").X, m.PreviewLeft()))
	check(t, "preview top", near(found(tt, "Preview").Y, m.PreviewTop()))
	check(t, "time right", near(found(tt, "09:41").X+found(tt, "09:41").W, 360-m.PaddingRight()))
	// Include the antialiased edge, rather than measuring only opaque pixels.
	a := boundsWhere(tt.Image(), image.Rect(0, 0, 65, 62), func(c ui.Color) bool { return c != th.Colors().Conversation().Background() })
	check(t, "avatar diameter", near(a.W, m.AvatarSize()) && near(a.H, m.AvatarSize()))
	check(t, "avatar position", near(a.X, m.PaddingLeft()) && near(a.Y, m.PaddingTop()))
	check(t, "avatar circular corner", pixel(tt, 10, 8) == th.Colors().Conversation().Background())
	b := colorBounds(tt.Image(), image.Rect(290, 25, 355, 62), th.Colors().Conversation().UnreadBackground())
	check(t, "unread height", near(b.H, m.BadgeHeight()))
	check(t, "unread right", near(b.X+b.W, 360-m.PaddingRight()))
	check(t, "badge capsule corner", pixel(tt, int(b.X), int(b.Y)) != th.Colors().Conversation().UnreadBackground())
	tt.Move(200, 20)
	check(t, "hover background", pixel(tt, 1, 1) == th.Colors().Conversation().BackgroundHover())
	tt.Press(200, 20)
	tt.Advance(th.Motion().RippleShow().Duration())
	check(t, "pressed ripple", pixel(tt, 200, 20) == th.Colors().Conversation().PressedBackground())
	tt.Release(200, 20)
	tt.Advance(th.Motion().RippleHide().Duration() + time.Millisecond)
	check(t, "row activate", clicks == 1)
	check(t, "released ripple", pixel(tt, 1, 1) == th.Colors().Conversation().BackgroundHover())
	selected = true
	tt.Frame()
	check(t, "selected background", pixel(tt, 1, 1) == th.Colors().Conversation().BackgroundActive())
	item.Muted = true
	tt.Frame()
	b = colorBounds(tt.Image(), image.Rect(290, 25, 355, 62), th.Colors().Conversation().UnreadBackgroundMutedActive())
	check(t, "selected muted counter", near(b.H, m.BadgeHeight()))
}

func TestRowContentPrecedenceAndCompact(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Conversation()
	item := dialogs.Conversation{ID: "one", Name: "Name", Initials: "N", Preview: "Message", Draft: "Draft body", DraftLabel: "Draft: ", Activity: "typing…", Online: true}
	compact := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		dialogs.Row(c, item, dialogs.RowOptions{Compact: compact}).Label("row")
	}, 360, 240)
	check(t, "activity replaces draft", tt.HasText("typing…") && !tt.HasText("Draft body") && !tt.HasText("Message"))
	item.Activity = ""
	tt.Frame()
	check(t, "draft replaces preview", tt.HasText("Draft body") && !tt.HasText("Message"))
	item.Unread = 42
	tt.Frame()
	check(t, "unread message overrides draft", tt.HasText("Message") && !tt.HasText("Draft body") && tt.HasText("42"))
	compact = true
	tt.SetSize(66, 240)
	check(t, "compact hides title preview", !tt.HasText("Name") && !tt.HasText("Message"))
	b := colorBounds(tt.Image(), image.Rect(0, 25, 66, 62), th.Colors().Conversation().UnreadBackground())
	check(t, "compact badge overlay", near(b.X+b.W, m.PaddingLeft()+m.AvatarSize()) && near(b.Y, m.PaddingTop()+m.AvatarSize()-m.BadgeHeight()))
	check(t, "compact avatar width", near(found(tt, "row").W, m.PaddingLeft()+m.AvatarSize()+m.PaddingRight()))
	item.Unread = 1000
	item.Mention = true
	tt.Frame()
	check(t, "compact combined counter caps at boundary", tt.HasText("99+") && !tt.HasText("1000"))
	item.Poll = true
	tt.Frame()
	check(t, "compact personal indicator precedes poll", tt.HasText("@") && !tt.HasText("▥"))
	item.Unread = 0
	item.Draft = ""
	tt.Frame()
	check(t, "compact allows two indicators without unread", tt.HasText("@") && tt.HasText("▥"))
	item.Poll = false
	item.Mention = false
	item.Unread = 1000000
	tt.Frame()
	check(t, "compact large counter caps at boundary", tt.HasText("99999+"))
	item.Unread = 0
	item.Draft = ""
	item.Online = false
	tt.Frame()
	tt.Advance(th.Motion().Online().Duration() + time.Millisecond)
	tt.Frame() // Render at the requested timestamp between display refreshes.
	check(t, "online fade ends", !tt.FramePending())
	item.Online = true
	tt.Frame()
	tt.Advance(th.Motion().Online().Duration() / 2)
	tt.Frame()
	// Include the antialiased green edge; the other layers here are blue/gray.
	online := boundsWhere(tt.Image(), image.Rect(40, 35, 60, 57), func(c ui.Color) bool { return c.G > c.R && c.G > c.B })
	check(t, "online intermediate", online.W > 0 && online.W < m.OnlineSize())
	tt.Advance(th.Motion().Online().Duration()/2 + time.Millisecond)
	tt.Frame()
	online = boundsWhere(tt.Image(), image.Rect(40, 35, 60, 57), func(c ui.Color) bool { return c.G > c.R && c.G > c.B })
	check(t, "online final diameter", near(online.W, m.OnlineSize()) && near(online.H, m.OnlineSize()))
	check(t, "online animation settles", !tt.FramePending())
}

func TestRowVariantsAndTextScale(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Conversation()
	item := dialogs.Conversation{ID: "one", Name: "Forum", Initials: "F", Preview: "Message"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, th); dialogs.Row(c, item, dialogs.RowOptions{}).Label("row") }, 360, 280)
	item.Tags = []dialogs.Tag{{ID: "tag", Label: "Work", Color: th.Colors().Conversation().TextForegroundService()}}
	tt.Frame()
	check(t, "tagged row height", near(found(tt, "row").H, m.TaggedHeight()))
	check(t, "tagged preview offset", near(found(tt, "Message").Y, m.TaggedPreviewTop()))
	item.Forum = true
	item.Topics = "General"
	tt.Frame()
	check(t, "tagged forum height", near(found(tt, "row").H, m.TaggedForumHeight()))
	check(t, "forum topics offset", near(found(tt, "General").Y, m.ForumPreviewTop()))
	item.Tags = nil
	tt.Frame()
	check(t, "forum row height", near(found(tt, "row").H, m.ForumHeight()))
	item.Forum = false
	tt.Frame()
	base := found(tt, "Message")
	tt.SetPreferences(ui.Preferences{TextScale: 2})
	scaled := found(tt, "Message")
	check(t, "text scales once", near(scaled.H, base.H*2))
	check(t, "large text row expands", found(tt, "row").H > m.RowHeight() && scaled.Y+scaled.H <= found(tt, "row").H)
}

func TestSearchEditingClearAndTheme(t *testing.T) {
	query := ""
	changes := []string{}
	submitted := ""
	th := theme.Light()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		dialogs.Search(c, &query, dialogs.SearchOptions{Label: "Search", Placeholder: "Find", ClearLabel: "Clear", OnChange: func(s string) { changes = append(changes, s) }, OnSubmit: func(s string) { submitted = s }}).Label("search-root")
	}, 360, 180)
	check(t, "search height", near(found(tt, "search-root").H, th.Metrics().Search().Height()))
	check(t, "search corner radius", pixel(tt, 0, 0) != th.Colors().Control().FilterInputInactiveBackground() && pixel(tt, 25, 2) == th.Colors().Control().FilterInputInactiveBackground())
	tt.ClickAt(80, 17)
	tt.Type("rehearsal")
	check(t, "search persists edit", query == "rehearsal" && reflect.DeepEqual(changes, []string{"rehearsal"}))
	tt.Key(0, ui.KeyEnter)
	check(t, "search enter submits", submitted == "rehearsal")
	check(t, "clear button size", near(found(tt, "Clear").W, th.Metrics().Search().ClearSize()) && near(found(tt, "Clear").H, th.Metrics().Search().ClearSize()))
	tt.Click("Clear")
	check(t, "clear edit notification", query == "" && reflect.DeepEqual(changes, []string{"rehearsal", ""}))
	tt.Type("次の検索")
	check(t, "clear restores text focus", query == "次の検索")
	th = theme.Dark()
	tt.Frame()
	check(t, "theme switch retains query", query == "次の検索" && tt.Focused("Search"))
}

func TestVirtualListIdentityNavigationAndMenu(t *testing.T) {
	entries := make([]dialogs.Entry, 50000)
	for i := range entries {
		entries[i].Conversation = dialogs.Conversation{ID: fmt.Sprint(i), Name: fmt.Sprintf("Room %d", i), Initials: "R", Preview: "Preview"}
	}
	entries[0] = dialogs.Entry{HeadingID: "section", Heading: "Recent"}
	model := dialogs.NewModel(entries)
	var state dialogs.ListState
	chosen, activated, menuID := "", "", ""
	activations := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		dialogs.List(c, model, &state, dialogs.ListOptions{Label: "Rooms", OnSelect: func(id string) { chosen = id }, OnActivate: func(id string) { activated = id; activations++ }, Menu: func(m *ui.Menu, id string) {
			if m.Item("Inspect").Chosen() {
				menuID = id
			}
		}}).Fill()
	}, 360, 400)
	_, last := state.View.Visible()
	check(t, "virtual list bounds construction", last < 15)
	tt.Click("Room 1")
	check(t, "pointer selects ID", state.SelectedID == "1" && chosen == "1")
	tt.Click("Room 1")
	check(t, "double click activates ID", activated == "1" && activations == 1)
	tt.Key(0, ui.KeyDown)
	check(t, "down selects next ID", state.SelectedID == "2")
	tt.Key(0, ui.KeyHome)
	check(t, "home skips group heading", state.SelectedID == "1")
	tt.Key(0, ui.KeyEnd)
	check(t, "end reaches last ID", state.SelectedID == "49999")
	first, last := state.View.Visible()
	check(t, "far navigation virtualized", first > 49980 && last == 49999)
	tt.Key(0, ui.KeyEnter)
	check(t, "enter activates ID", activated == "49999" && activations == 2)
	state.SelectedID = "2"
	state.ScrollTo(model, "2", ui.Start)
	tt.Frame()
	check(t, "host selects and scrolls ID", state.SelectedID == "2" && tt.HasText("Room 2"))
	// Move the chosen item thousands of rows without moving its identity.
	entries[2], entries[45000] = entries[45000], entries[2]
	model = dialogs.NewModel(entries)
	tt.Frame()
	state.ScrollTo(model, "2", ui.Start)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	check(t, "selection survives far reorder", state.SelectedID == "45001")
	state.ScrollTo(model, "2", ui.Start)
	tt.Frame()
	tt.RightClick("Room 2")
	check(t, "right click opens menu", reflect.DeepEqual(tt.Menu(), []string{"Inspect"}))
	tt.ChooseMenuItem("Inspect")
	check(t, "context action carries ID", menuID == "2")
	// Filtering out the current item preserves the host ID, with no visible pick.
	model = dialogs.NewModel(entries[100:120])
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	check(t, "filtered selection starts on visible ID", state.SelectedID == entries[100].Conversation.ID)
}

func TestRailStatesAndGroupHeading(t *testing.T) {
	th := theme.Light()
	var state dialogs.RailState
	selected, locked := "", ""
	folders := []dialogs.Folder{{ID: "all", Name: "All chats", Badge: "12"}, {ID: "quiet", Name: "Quiet", Badge: "4", Muted: true}, {ID: "locked", Name: "Locked", Locked: true}}
	mode := dialogs.IconsAndText
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		dialogs.Rail(c, folders, &state, dialogs.RailOptions{Mode: mode, OnSelect: func(id string) { selected = id }, OnLocked: func(id string) { locked = id }}).FillHeight()
	}, 240, 400)
	check(t, "rail item width", near(found(tt, "All chats").W, th.Metrics().Sidebar().Width()))
	check(t, "rail item height", near(found(tt, "All chats").H, th.Metrics().Sidebar().ItemMinHeight()))
	tt.ClickAt(30, 30)
	check(t, "rail selects folder ID", state.SelectedID == "all" && selected == "all")
	tt.Key(0, ui.KeyDown)
	check(t, "rail keyboard next folder", state.SelectedID == "quiet")
	tt.Key(0, ui.KeyDown)
	check(t, "locked folder retains selection", state.SelectedID == "quiet" && locked == "locked")
	mode = dialogs.IconsOnly
	tt.Frame()
	check(t, "icons only item height", near(found(tt, "All chats").H, th.Metrics().Sidebar().IconsOnlyHeight()))
	mode = dialogs.TextOnly
	tt.Frame()
	check(t, "text only item height", near(found(tt, "All chats").H, th.Metrics().Sidebar().TextOnlyHeight()))
	tt = ui.NewTester(func(c *ui.Context) { theme.Apply(c, th); dialogs.GroupHeading(c, "Recent").Label("heading") }, 360, 80)
	check(t, "group heading height", near(found(tt, "heading").H, th.Metrics().Search().GroupHeight()))
	check(t, "group heading position", near(found(tt, "Recent").X, th.Metrics().Search().GroupLeft()) && near(found(tt, "Recent").Y, th.Metrics().Search().GroupTop()))
}

func TestListDragAndDisabledRow(t *testing.T) {
	model := dialogs.NewModel([]dialogs.Entry{
		{Conversation: dialogs.Conversation{ID: "a", Name: "First", Initials: "F"}},
		{Conversation: dialogs.Conversation{ID: "b", Name: "Second", Initials: "S"}},
		{Conversation: dialogs.Conversation{ID: "c", Name: "Third", Initials: "T"}},
	})
	var state dialogs.ListState
	var moved []string
	before := "unset"
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		dialogs.List(c, model, &state, dialogs.ListOptions{OnReorder: func(ids []string, to string) { moved, before = ids, to }}).Fill()
	}, 360, 300)
	tt.Press(100, 30)
	tt.Move(100, 50)
	tt.Move(100, 180)
	tt.Release(100, 180)
	check(t, "drag emits stable IDs", reflect.DeepEqual(moved, []string{"a"}) && before == "")
	check(t, "drag leaves host snapshot owned", found(tt, "First").Y < found(tt, "Second").Y)
	clicks := 0
	tt = ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		dialogs.Row(c, dialogs.Conversation{ID: "disabled", Name: "Disabled"}, dialogs.RowOptions{Disabled: true, OnActivate: func(string) { clicks++ }})
	}, 360, 100)
	tt.ClickAt(80, 30)
	tt.Key(0, ui.KeyEnter)
	check(t, "disabled row does not activate", clicks == 0)
}

func TestHostSelectionChangesWithReorder(t *testing.T) {
	a := dialogs.Entry{Conversation: dialogs.Conversation{ID: "a", Name: "First", Initials: "F"}}
	b := dialogs.Entry{Conversation: dialogs.Conversation{ID: "b", Name: "Second", Initials: "S"}}
	model := dialogs.NewModel([]dialogs.Entry{a, b})
	state := dialogs.ListState{SelectedID: "a"}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		dialogs.List(c, model, &state, dialogs.ListOptions{}).Fill()
	}, 360, 180)
	tt.Click("First")
	model = dialogs.NewModel([]dialogs.Entry{b, a})
	state.SelectedID = "b"
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	check(t, "host selection follows ID across a simultaneous reorder", state.SelectedID == "a")
}

func TestTypingPeriodAndMotionPreference(t *testing.T) {
	th := theme.Light()
	item := dialogs.Conversation{ID: "activity", Name: "Typing", Initials: "T", Preview: "Message", Activity: "typing…"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, th); dialogs.Row(c, item, dialogs.RowOptions{}) }, 360, 120)
	m := th.Metrics().Conversation()
	activity := th.Metrics().Activity()
	crop := image.Rect(int(m.PreviewLeft()), int(m.PreviewTop()), int(m.PreviewLeft()+activity.DotLeft()+3*activity.DotGap()), int(m.RowHeight()-m.PaddingBottom()))
	frame := func() []byte {
		img := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		draw.Draw(img, img.Bounds(), tt.Image(), crop.Min, draw.Src)
		return img.Pix
	}
	start := frame()
	// widgets.style:1337-1339; send_action_animations.cpp:122-160.
	tt.Advance(th.Motion().Typing().Duration())
	tt.Frame()
	check(t, "typing repeats at sourced period", bytes.Equal(start, frame()))
	tt.Advance(th.Motion().TypingHalfPeriod().Duration())
	tt.Frame()
	check(t, "typing changes within the cycle", !bytes.Equal(start, frame()))
	item.Activity = ""
	tt.Frame()
	check(t, "clearing activity stops animation frames", !tt.FramePending())
	item.Activity = "typing…"
	tt.Frame()
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	check(t, "reduced motion makes typing static", !tt.FramePending())
}

func TestPeerTypeTitleOffsets(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Conversation()
	item := dialogs.Conversation{ID: "kind", Name: "Title", Initials: "T", Kind: dialogs.Group}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, th); dialogs.Row(c, item, dialogs.RowOptions{}).Label("row") }, 360, 100)
	// dialogs.style:389,394; dialogs_layout.style:41: bitmap width + x offset + type skip.
	check(t, "group title respects native icon slot", near(found(tt, "Title").X, m.TitleLeft()+m.GroupIcon().Left()+m.GroupIcon().Width()+m.TypeSkip()))
	item.Kind = dialogs.Channel
	tt.Frame()
	check(t, "channel title respects native icon slot", near(found(tt, "Title").X, m.TitleLeft()+m.ChannelIcon().Left()+m.ChannelIcon().Width()+m.TypeSkip()))
	item.Kind = dialogs.Bot
	tt.Frame()
	check(t, "bot title respects native icon slot", near(found(tt, "Title").X, m.TitleLeft()+m.BotIcon().Left()+m.BotIcon().Width()+m.TypeSkip()))
}

func TestConversationSelectionRemainsSingle(t *testing.T) {
	th := theme.Light()
	model := dialogs.NewModel([]dialogs.Entry{
		{Conversation: dialogs.Conversation{ID: "a", Name: "First"}},
		{Conversation: dialogs.Conversation{ID: "b", Name: "Second"}},
	})
	state := dialogs.ListState{SelectedID: "a"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, th); dialogs.List(c, model, &state, dialogs.ListOptions{}).Fill() }, 360, 200)
	tt.Click("First")
	tt.Key(ui.Cmd, ui.KeyA)
	check(t, "conversation selection remains single after command A", state.SelectedID == "a" && pixel(tt, 1, int(th.Metrics().Conversation().RowHeight()+1)) == th.Colors().Conversation().Background())
}

func TestFolderHostSelectionChangesWithReorder(t *testing.T) {
	a := dialogs.Folder{ID: "a", Name: "Same name"}
	b := dialogs.Folder{ID: "b", Name: "Same name"}
	folders := []dialogs.Folder{a, b}
	state := dialogs.RailState{SelectedID: "a"}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		dialogs.Rail(c, folders, &state, dialogs.RailOptions{}).FillHeight()
	}, 240, 240)
	tt.ClickAt(30, 30)
	folders = []dialogs.Folder{b, a}
	state.SelectedID = "b"
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	check(t, "folder selection follows ID through equal names and simultaneous reorder", state.SelectedID == "a")
}
