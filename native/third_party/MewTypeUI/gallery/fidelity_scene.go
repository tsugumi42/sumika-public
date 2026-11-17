package gallery

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// FidelityCase contains only public filenames, crop geometry and invented data.
// Physical crop dimensions and host column anchors are calibration inputs, not
// component acceptance measurements. The reference pixels never enter a scene.
type FidelityCase struct {
	ID            string
	Width, Height int
	Kind          string
	Panel         bool
}

func FidelityCases() []FidelityCase {
	return []FidelityCase{
		{"14-43-11", 1175, 874, "empty", false},
		{"14-43-53", 1175, 874, "history", false},
		{"14-43-59", 1540, 874, "history", true},
		{"14-44-03", 1540, 874, "drawer", true},
		{"14-44-28", 357, 525, "menu", false},
		{"14-44-57", 1540, 874, "short", true},
		{"14-45-27", 669, 874, "loading", false},
		{"14-45-46", 669, 160, "files", false},
		{"14-45-51", 669, 160, "transfer", false},
		{"14-46-06", 788, 452, "reply", false},
		{"14-46-12", 721, 481, "quote", false},
		{"14-46-23", 1540, 874, "stickers", true},
		{"14-46-27", 1540, 874, "emoji", true},
		{"14-46-53", 1540, 874, "gifs", true},
	}
}

func init() {
	for _, sample := range FidelityCases() {
		Register(Page{ID: "fidelity/" + sample.ID, Area: "fidelity", Title: sample.ID, View: NewFidelityScene(sample, nil)})
	}
}

// SetFidelityMenu supplies an actual native capture for comparing the menu
// inside the page layer. Separate native windows cannot inherit that effect.
func (a *App) SetFidelityMenu(bitmap *ui.Bitmap) {
	for _, sample := range FidelityCases() {
		if sample.Kind != "menu" {
			continue
		}
		for i := range a.pages {
			if a.pages[i].ID == "fidelity/"+sample.ID {
				a.pages[i].View = NewFidelityScene(sample, bitmap)
			}
		}
	}
}

type fidelitySceneState struct {
	draft                    string
	picker                   compose.PickerState
	rail                     dialogs.RailState
	quote                    content.QuoteState
	quoteMarkdown, quoteLead content.MarkdownState
	menu                     overlay.MenuState
	data                     compose.PickerData
}

// NewFidelityScene constructs the same component view used by cmd/fidelity and
// the native gallery. menu is a local capture of the real native menu renderer.
func NewFidelityScene(sample FidelityCase, menu *ui.Bitmap) func(*ui.Context) {
	key := "fidelity-state-" + sample.ID
	return func(c *ui.Context) {
		s := ui.Local(c.Root(), key, func() fidelitySceneState {
			state := fidelitySceneState{rail: dialogs.RailState{SelectedID: "folder-3"}}
			if sample.Kind == "stickers" || sample.Kind == "emoji" || sample.Kind == "gifs" {
				state.data = fidelityPickerData(contentBitmap())
			}
			if sample.Kind == "stickers" {
				state.picker.Tab = compose.StickerTab
			}
			if sample.Kind == "gifs" {
				state.picker.Tab = compose.GIFTab
			}
			return state
		})
		media := contentBitmap()
		t := theme.T(c)
		m := t.Metrics()
		ui.Box(c).Size(float32(sample.Width)/1.25, float32(sample.Height)/1.25).Background(t.Colors().Window().Background()).Clip().Children(func() {
			if sample.Kind == "menu" {
				if menu != nil {
					w, h := menu.Size()
					ui.Image(c, menu).Absolute().Left(36).Top(157/1.25).Size(float32(w)/1.25, float32(h)/1.25).Label("菜单")
				} else {
					button := ui.Button(c, "打开消息菜单").Absolute().Left(36).Top(76)
					items := make([]overlay.MenuItem, 8)
					for i := range items {
						items[i] = overlay.MenuItem{ID: fmt.Sprint(i), Label: fmt.Sprintf("菜单区域 %02d", i+1)}
					}
					overlay.PopupMenu(c, button, items, &s.menu, overlay.MenuOptions{Icons: true, Label: "消息菜单"})
				}
				return
			}
			if sample.Kind == "files" || sample.Kind == "transfer" {
				ui.Column(c).Absolute().Left(56).Top(0).Width(430).Height(float32(sample.Height) / 1.25).Background(t.Colors().Message().In().Background()).Children(func() {
					for i := range 2 {
						status := content.MediaRemote
						if i == 1 {
							status = content.MediaReady
						}
						if i == 0 && sample.Kind == "transfer" {
							status = content.MediaLoading
						}
						content.File(c.Key(fmt.Sprintf("file-%d", i)), content.FileData{Name: fmt.Sprintf("Archive sample %02d.7z", i+1), Size: "1000.0 MB", Status: status, Progress: .002}, content.FileOptions{}).Label(fmt.Sprintf("文件行%d", i))
					}
					chat.BottomInfo(c, chat.Info{Label: "12:34", Views: "1000"}, chat.BodyOptions{}).Absolute().Right(m.Message().PaddingRight() - m.Message().TimestampDeltaX()).Bottom(m.Message().PaddingBottom() - m.Message().TimestampDeltaY()).Label("文件时间")
				})
				return
			}
			full := sample.Width > 1000
			title := float32(0)
			left := float32(0)
			if full || sample.Kind == "loading" {
				title = 31 / 1.25
			}
			if full {
				ui.Box(c).Absolute().Left(0).Top(0).Right(0).Height(title).Background(t.Colors().Titlebar().BackgroundActive()).Label("系统标题带")
				ui.Box(c).Absolute().Left(0).Top(title).Bottom(0).Width(m.Sidebar().Width()).Children(func() {
					folders := make([]dialogs.Folder, 10)
					for i := range folders {
						folders[i] = dialogs.Folder{ID: fmt.Sprintf("folder-%d", i), Name: fmt.Sprintf("分类%d", i+1), Badge: fmt.Sprint(i + 1)}
					}
					dialogs.Rail(c, folders, &s.rail, dialogs.RailOptions{}).Label("文件夹栏")
				})
				left = m.Sidebar().Width()
				ui.Column(c).Absolute().Left(left).Top(title).Bottom(0).Width(261.6).Background(t.Colors().Conversation().Background()).Children(func() {
					dialogs.Search(c, &s.draft, dialogs.SearchOptions{Label: "会话搜索"}).Label("搜索框")
					for i := range 10 {
						selected := i == 3 && sample.Kind != "short" && sample.Kind != "empty" || i == 0 && sample.Kind == "short"
						dialogs.Row(c.Key(fmt.Sprintf("dialog-%d", i)), dialogs.Conversation{ID: fmt.Sprint(i), Name: fmt.Sprintf("示例会话%02d", i+1), Initials: "样", Preview: "编造的消息预览", Time: "12:34", Unread: i, Kind: dialogs.Group}, dialogs.RowOptions{Selected: selected}).Label(fmt.Sprintf("会话行%d", i))
					}
				})
				left += 261.6
			} else if sample.Kind == "reply" {
				left = 48 / 1.25
			}
			right := float32(0)
			if sample.Panel {
				right = 364
			}
			if sample.Kind == "reply" {
				right = 71 / 1.25
			}
			bottom := float32(0)
			if sample.Kind == "reply" {
				bottom = 18 / 1.25
			}
			if sample.Kind == "quote" {
				left, right, title, bottom = 20/1.25, 32/1.25, 6/1.25, 7/1.25
			}
			ui.Column(c).Absolute().Left(left).Right(right).Top(title).Bottom(bottom).Children(func() {
				if (full || sample.Kind == "loading") && sample.Kind != "empty" {
					chat.Header(c, chat.HeaderInfo{Title: "示例会话", Subtitle: "离线"}, chat.HeaderOptions{Actions: []chat.Action{{ID: "search", Label: "搜索", Kind: chat.SearchAction}, {ID: "call", Label: "通话", Kind: chat.CallAction}, {ID: "more", Label: "更多", Kind: chat.MenuAction}}}).FillWidth().Label("顶栏")
					if sample.Kind != "empty" {
						compose.ReferenceBar(c, compose.Reference{ID: "pinned", Name: "固定消息", Summary: "用于检查布局的编造摘要"}, compose.ReferenceOptions{}).FillWidth().Label("置顶条")
					}
				}
				ui.Box(c).Grow(1).MinHeight(0).Clip().Background(t.Colors().Window().Background()).Label("历史背景").Children(func() {
					if sample.Kind == "empty" {
						ui.Text(c, "示例提示").Absolute().Attach(ui.AnchorCenter, ui.AnchorCenter).TextColor(t.Colors().Window().MutedText())
						return
					}
					ui.Column(c).Absolute().Left(0).Right(0).Bottom(m.Message().HistoryBottom()).Children(func() {
						drawMessage := func(id, text string, outgoing bool, reactions bool, withQuote bool, photo bool) {
							side := chat.Incoming
							delivery := chat.NoReceipt
							if outgoing {
								side = chat.Outgoing
								delivery = chat.Read
							}
							o := chat.RowOptions{ReserveAvatar: !outgoing, BubbleOptions: chat.BubbleOptions{Side: side}}
							if reactions {
								o.Footer = func(tail ui.Span) {
									content.Reactions(c, []content.Reaction{{ID: "r", Emoji: "👍", Count: 2}}, content.ReactionOptions{Appearance: content.Appearance{Outgoing: outgoing}, InBubble: true, Trailing: tail}).Label("回应" + id)
								}
							}
							var body func()
							if withQuote || photo {
								body = func() {
									if withQuote {
										if (sample.Kind == "reply" || sample.Kind == "quote") && id == "1" {
											content.Markdown(c, "编造的两行说明。  \n比较正文的留白。", &s.quoteLead, content.MarkdownOptions{})
											content.Markdown(c, "> 第一行编造的引用说明。  \n> 第二行编造的引用说明。  \n> 第三行编造的引用说明。", &s.quoteMarkdown, content.MarkdownOptions{}).Label("引用块")
										} else {
											content.Quote(c, content.QuoteData{Name: "示例来源", Summary: "引用中的编造说明", Multiline: true}, &s.quote, content.QuoteOptions{Appearance: content.Appearance{Outgoing: outgoing}}).Label("回复摘要")
										}
									}
									if photo {
										content.Photo(c, content.Media{ID: "media", Label: "生成图像", Bitmap: media, Width: 430, Height: 300}, content.MediaOptions{InBubble: true}).Label("媒体")
									}
								}
							}
							name, views := "", "1000"
							if sample.Kind == "reply" || sample.Kind == "quote" || sample.Kind == "loading" {
								name = "示例作者"
							}
							if sample.Kind == "short" || sample.Kind == "quote" {
								views = ""
							}
							chat.Row(c.Key(id), chat.Message{Text: text, SenderName: name, Avatar: media, Info: chat.Info{Label: "12:34:56", Views: views, Delivery: delivery}}, o, body).FillWidth().Label("消息行" + id)
						}
						switch sample.Kind {
						case "loading":
							content.Photo(c, content.Media{ID: "loading", Width: 430, Height: 308, Status: content.MediaLoading, Progress: .01}, content.MediaOptions{OnCancel: func() {}}).Label("加载图片")
							drawMessage("1", strings.Repeat("这是用于比较换行和段落的编造正文。", 16), false, false, false, false)
						case "short":
							for i, n := range []int{25, 4, 13} {
								drawMessage(fmt.Sprint(i), strings.Repeat("样", n), i != 0, true, false, false)
							}
							content.Sticker(c, content.Media{ID: "sticker", Bitmap: media, Width: 224, Height: 224}, content.MediaOptions{}).AlignSelf(ui.End).Label("贴纸")
						case "reply", "quote":
							drawMessage("1", "比较段落中的补充说明。", false, false, true, false)
							if sample.Kind == "quote" {
								drawMessage("2", "1", true, false, true, false)
							}
						case "gifs":
							drawMessage("1", "编造的说明", true, true, false, true)
							content.Sticker(c, content.Media{ID: "sticker", Bitmap: media, Width: 224, Height: 260}, content.MediaOptions{}).Label("贴纸")
						default:
							drawMessage("1", "示例图片说明", true, false, false, true)
							drawMessage("2", strings.Repeat("样", 18), true, false, false, false)
							drawMessage("3", strings.Repeat("这是比较段落结构的编造内容。", 9), true, false, true, false)
						}
					})
				})
				if sample.Kind != "empty" {
					editor := compose.EditorOptions{}
					if sample.Kind == "reply" {
						editor.Reference = &compose.Reference{ID: "reply", Name: "示例来源", Summary: "编造的引用摘要"}
					}
					compose.Editor(c, &s.draft, editor).FillWidth().Label("输入区")
				}
			})
			if sample.Panel {
				ui.Box(c).Absolute().Right(0).Top(title).Bottom(0).Width(364).Clip().Children(func() {
					switch sample.Kind {
					case "stickers", "emoji", "gifs":
						compose.Picker(c, &s.picker, s.data, compose.PickerOptions{AvailableHeight: float32(sample.Height)/1.25 - title, AvailableWidth: 364, Tabs: [3]string{"表情", "贴纸", "GIF"}, SearchLabel: "侧栏搜索"}).Label("侧栏选择面板")
					}
				})
			}
		})
	}
}

func fidelityPickerData(bitmap *ui.Bitmap) compose.PickerData {
	var data compose.PickerData
	for tab := range data {
		for g := range 3 {
			category := compose.Category{ID: fmt.Sprintf("group-%d", g), Label: fmt.Sprintf("分类%d", g+1)}
			for i := range 40 {
				category.Items = append(category.Items, compose.PickerItem{ID: fmt.Sprint(i), Label: fmt.Sprintf("示例%d", i), Emoji: "🙂", Image: bitmap, AspectRatio: float32(1+i%3) / 2})
			}
			data[tab].Categories = append(data[tab].Categories, category)
		}
	}
	return data
}
