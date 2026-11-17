package gallery

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Lucide house 1.58.0, ISC/MIT; licensed source recorded in THIRD_PARTY_NOTICES.md.
var glyphHouse = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8"/><path d="M3 10a2 2 0 0 1 .709-1.528l7-6a2 2 0 0 1 2.582 0l7 6A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>`))

func init() {
	Register(Page{ID: "icons/glyphs", Area: "icons", Title: "图标实际大小", View: iconGlyphPage})
}

type iconGlyphState struct {
	rail         dialogs.RailState
	draft, event string
}

func iconGlyphPage(c *ui.Context) {
	s := ui.Local(c.Root(), "icon-glyph", func() iconGlyphState { return iconGlyphState{rail: dialogs.RailState{SelectedID: "home"}} })
	folders := []dialogs.Folder{
		{ID: "home", Name: "本地", Icon: glyphHouse},
		{ID: "all", Name: "全部"},
		{ID: "badge", Name: "未读", Icon: glyphHouse, Badge: "12"},
		{ID: "muted", Name: "静音", Icon: glyphHouse, Muted: true, Badge: "3"},
		{ID: "locked", Name: "锁定", Icon: glyphHouse, Locked: true},
	}
	ui.Row(c).Fill().Children(func() {
		dialogs.Rail(c, folders, &s.rail, dialogs.RailOptions{}).FillHeight()
		ui.Column(c).Grow(1).FillHeight().MinWidth(0).Children(func() {
			chat.Header(c, chat.HeaderInfo{Title: "观测小组", Subtitle: "最近在线"}, chat.HeaderOptions{OnBack: func() { s.event = "返回" }, BackLabel: "返回", Actions: []chat.Action{
				{ID: "search", Label: "搜索", Kind: chat.SearchAction, OnActivate: func() { s.event = "搜索" }},
				{ID: "call", Label: "通话", Kind: chat.CallAction, OnActivate: func() { s.event = "通话" }},
				{ID: "menu", Label: "更多", Kind: chat.MenuAction, OnActivate: func() { s.event = "更多" }},
			}})
			ui.Column(c).Grow(1).FillWidth().Background(theme.T(c).Colors().Window().Background()).Children(func() {
				dialogs.Row(c.Key("archive"), dialogs.Conversation{ID: "archive", Kind: dialogs.Archive, Name: "归档", Preview: "独立头像图形"}, dialogs.RowOptions{})
				dialogs.Row(c.Key("subscription"), dialogs.Conversation{ID: "subscription", Name: "订阅", Initials: "订", Subscribed: true}, dialogs.RowOptions{})
				dialogs.Row(c.Key("verification"), dialogs.Conversation{ID: "verification", Name: "验证码", VerificationCodes: true}, dialogs.RowOptions{})
				ui.Text(c, s.event)
			})
			compose.ReferenceBar(c, compose.Reference{ID: "reply", Name: "小铃", Summary: "今晚在观测台见。"}, compose.ReferenceOptions{OnCancel: func() { s.event = "取消回复" }})
			ui.Row(c).Children(func() {
				for _, a := range []compose.Action{compose.Attach, compose.Emoji, compose.Send, compose.Voice, compose.Save, compose.Cancel} {
					c.Key(a)
					compose.ActionButton(c, a, compose.ActionOptions{})
				}
			})
			compose.Editor(c, &s.draft, compose.EditorOptions{OnSubmit: func(value string) { s.event = value; s.draft = "" }})
		})
	})
}
