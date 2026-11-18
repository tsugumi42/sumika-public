package catalog

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 聊天气泡的材质试验：实色 / 玻璃（只有模糊加半透明底，不折射），能调浓度，压在壁纸和一条玻璃浮条下面看。

// 模糊滑块的范围和起点（起点同磨砂材质的模糊半径）。
const (
	bubbleBlurMin     float32 = 1
	bubbleBlurMax     float32 = 48
	bubbleBlurDefault float32 = 24
)

// bubbleSelfDefault 是玻璃时自己的气泡保留主题色的起点。
const bubbleSelfDefault float32 = .6

// SelfTint 是自己的气泡保留多少主题色。
func (l BubbleLook) SelfTint() float32 {
	if l.Self <= 0 {
		return bubbleSelfDefault
	}
	return l.Self
}

// BlurRadius 是这个选择的模糊半径。
func (l BubbleLook) BlurRadius() float32 {
	if l.Blur <= 0 {
		return bubbleBlurDefault
	}
	return l.Blur
}

// bubbleAlphaCustom 是“自定”档滑块的起点。
const bubbleAlphaCustom float32 = .9

// BubbleLook 是气泡材质的当前选择。Mode：0 实色、1 玻璃淡、2 玻璃浓、3 玻璃自定（浓度用 Custom）。
type BubbleLook struct {
	Mode   int
	Custom float32 // 自定档的底色浓度，winshell.BubbleAlphaMin～Max
	Blur   float32 // 模糊的程度；0 表示还没调过，用 bubbleBlurDefault
	Self   float32 // 自己的气泡保留多少主题色，0～1；0 表示还没调过，用 bubbleSelfDefault
	Bar    bool    // 长对话上面压一条玻璃浮条
	Solo   bool    // 只留长对话那一张卡（量滚动耗时时不让另外两张静止的卡掺进去）
}

// Alpha 是这个选择的底色浓度；实色是 1（Kit.BubbleGlass 对 1 返回 nil，气泡就是实色）。
func (l BubbleLook) Alpha() float32 {
	switch l.Mode {
	case 1:
		return winshell.BubbleAlphaThin
	case 2:
		return winshell.BubbleAlphaThick
	case 3:
		return l.Custom
	}
	return 1
}

// 玻璃试验场的尺寸：长对话卡片宽高、一屏静止那两张卡的消息数。
const (
	longCardW  float32 = 520 // 自有尺寸：长对话卡片宽（接近正式聊天列的宽度）
	longCardH  float32 = 560 // 自有尺寸：长对话卡片高（能看到十来条消息）
	longCount          = 48  // 长对话的消息条数
	staticEach         = 14  // 静止的卡里放的消息条数（放不下的被卡片裁掉）
)

// wallCard 是带壁纸（色晕加波点，和正式聊天区同一个画法）的聊天卡：玻璃要有东西可透。height 为 0 时按内容高。
func (e Env) wallCard(c *ui.Context, name string, width, height float32, body func(col, inset float32)) {
	g := e.Kit.G
	inset := g.SpacePanel
	wall := Local(e, name+"-wall", func() chatui.Wallpaper { return chatui.Wallpaper{} })
	card := ui.Column(c.Key(e.Key(name))).Width(width).Shrink(0).Padding(inset).Radius(inset).Clip().Background(e.Kit.T.C.ChatBg)
	if height > 0 {
		card.Height(height)
	}
	card.Draw(func(pt *ui.Painter, r ui.Rect) { wall.Paint(pt, r, e.Kit.T, chatui.CharColors[0]) })
	card.Children(func() { body(width-2*inset, inset) })
}

// glassBar 是压在消息上面的那条玻璃浮条：和正式标题栏同一个材质、同一圈阴影，用来看“玻璃压玻璃”。
func (e Env) glassBar(c *ui.Context, name string, inset float32) {
	k := e.Kit
	g, p := k.G, k.P
	bar := ui.Row(c.Key(e.Key(name))).Absolute().Left(inset).Right(inset).Top(inset).Height(g.BarHeight).Radius(g.RadiusBar).Padding(g.BarInset).AlignItems(ui.Center)
	k.Glass(bar, winshell.LevelHeader, p.Canvas.Alpha(winshell.OverlayAlpha))
	k.RaisedShadow(bar)
	bar.Children(func() {
		ui.Text(c, "晴枝").FontSize(g.LabelSize).FontWeight(g.BoldWeight).FixedLineHeight(g.BarLine).SingleLine()
	})
}

// chatLines 是编的一段长对话：长短不一，夹着换行和 markdown，对方、自己交替，偶尔连着两条。
func chatLines(n int) []*chatui.Msg {
	texts := []string{
		"早，今天想从哪件事开始？",
		"先把纸灯那篇笔记补完。",
		"好呀，那我把昨天停下的地方找出来。",
		"停在窗边那一段。\n灯罩的纸还没换，写到一半。",
		"嗯。",
		"那就先写灯罩：纸的颜色、光穿过去之后的样子，再写窗外的天。",
		"写完我们顺便把 `todo.md` 里挂了很久的两条清掉。",
		"可以，先把能一口气做完的挑出来。",
		"这条要查资料，先放着。",
		"那条只要改两个字，现在就能弄完。",
		"弄完啦。",
		"这么快？给我看看。",
		"在这里，改的是第三段和第五段。顺手把标点也统一了。",
		"好，没问题。",
	}
	out := make([]*chatui.Msg, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("gl-%d", i)
		sec := i * 25
		text := texts[i%len(texts)]
		if i%7 == 6 {
			text += "\n再补一句：" + texts[(i+3)%len(texts)]
		}
		if i%3 == 1 || i%5 == 4 {
			out = append(out, mine(id, sec, text))
		} else {
			out = append(out, her(id, sec, text))
		}
	}
	return out
}

// bubbleGlassDemo 是气泡试验场：控件在上，下面三张壁纸卡并排——两张静止的、一张压着玻璃浮条能滚的长对话。
// 三张卡等高，一屏里一共能看到三十来条玻璃气泡。
func bubbleGlassDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	look := &e.Store.Look
	e.Section(c, "glass", "材质", func() {
		ui.Row(c.Key(e.Key("glass-ctl"))).FillWidth().Wrap().Gap(g.SpacePanel).AlignItems(ui.Center).Children(func() {
			ui.Row(c.Key(e.Key("glass-mode-wrap"))).Shrink(0).Children(func() {
				k.SegmentWith(c, e.Key("glass-mode"), &look.Mode, []string{"实色", "玻璃（淡）", "玻璃（浓）", "自定"}, winshell.SegmentOptions{Fit: true})
			})
			bl := float64(look.BlurRadius())
			ui.Row(c.Key(e.Key("glass-blur-bit"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "模糊").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted).SingleLine()
				k.Slider(c, e.Key("glass-blur"), &bl, bubbleBlurMin, bubbleBlurMax, 1, "模糊的程度", false).Width(g.ControlWide / 2)
				ui.Text(c, fmt.Sprintf("%.0f", bl)).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted).MinWidth(g.ValueWidth)
			})
			look.Blur = float32(bl)
			sf := float64(look.SelfTint())
			ui.Row(c.Key(e.Key("glass-self-bit"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "我的气泡的主题色").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted).SingleLine()
				k.Slider(c, e.Key("glass-self"), &sf, .1, 1, .05, "我的气泡保留多少主题色", false).Width(g.ControlWide / 2)
				ui.Text(c, fmt.Sprintf("%.2f", sf)).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted).MinWidth(g.ValueWidth)
			})
			look.Self = float32(sf)
			f := float64(look.Custom)
			ui.Row(c.Key(e.Key("glass-alpha-bit"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "填充色不透明度").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted).SingleLine()
				k.Slider(c, e.Key("glass-alpha"), &f, winshell.BubbleAlphaMin, winshell.BubbleAlphaMax, .02, "填充色不透明度", false).Width(g.ControlWide / 2)
				ui.Text(c, fmt.Sprintf("%.2f", f)).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted).MinWidth(g.ValueWidth)
			})
			if float32(f) != look.Custom {
				look.Custom, look.Mode = float32(f), 3
			}
			ui.Row(c.Key(e.Key("glass-bar-bit"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
				k.Switch(c, e.Key("glass-bar-sw"), "压玻璃浮条", &look.Bar)
				ui.Text(c, "压玻璃浮条").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine()
			})
		})
		e.Line(c, "glass-note", "玻璃只有模糊加半透明底，不折射。面板材质选“实色”、或聊天背景图案关掉时，气泡退回实色。")
	})

	e.Section(c, "glass-cards", "壁纸上的气泡", func() {
		e.Cells(c, "glass-cards-row", func() {
			if !look.Solo {
				for half := 0; half < 2; half++ {
					ms := chatLines(staticEach * 2)[half*staticEach : (half+1)*staticEach]
					e.Cell(c, fmt.Sprintf("glass-static-%d", half), "静止的", func() {
						e.wallCard(c, fmt.Sprintf("glass-static-card-%d", half), longCardW, longCardH, func(col, _ float32) {
							messages(c, e.ChatHost(col, false), ms)
						})
					})
				}
			}
			e.Cell(c, "glass-long-cell", fmt.Sprintf("%d 条，滚轮在卡片里滚", longCount), func() { e.longChat(c) })
		})
	})
}

// longChat 是长对话那张卡：虚拟列表，上面可以压一条玻璃浮条。
func (e Env) longChat(c *ui.Context) {
	g := e.Kit.G
	look := &e.Store.Look
	ls := Local(e, "glass-long-list", func() ui.ListState { return ui.ListState{} })
	ms := Local(e, "glass-long-msgs", func() []*chatui.Msg { return chatLines(longCount) })
	inset := g.SpacePanel
	wall := Local(e, "glass-long-wall", func() chatui.Wallpaper { return chatui.Wallpaper{} })
	card := ui.Box(c.Key(e.Key("glass-long-card"))).Size(longCardW, longCardH).Shrink(0).Radius(inset).Clip().Background(e.Kit.T.C.ChatBg)
	card.Draw(func(pt *ui.Painter, r ui.Rect) { wall.Paint(pt, r, e.Kit.T, chatui.CharColors[0]) })
	card.Children(func() {
		h := e.ChatHost(longCardW-2*inset, false)
		list := *ms
		ui.List(c.Key(e.Key("glass-long-rows")), ls, len(list), func(i int) {
			var prev, next *chatui.Msg
			if i > 0 {
				prev = list[i-1]
			}
			if i+1 < len(list) {
				next = list[i+1]
			}
			h.Message(c, list[i], prev, next)
		}).Fill().Padding(g.BarHeight+2*inset, inset, inset, inset).Label("长对话")
		if look.Bar {
			e.glassBar(c, "glass-long-bar", inset)
		}
	})
}
