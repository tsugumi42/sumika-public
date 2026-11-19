package chatui

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// Host 是画消息时宿主给的环境：令牌、字体、主人自己的气泡用谁的角色色、重试回调。
type Host struct {
	SquareAvatar     bool // 本地角色的圆角方头像；QQ 上的人是圆的
	T                Tokens
	Colors           *MessageColors // nil 时仍用原型色板；新视图把宿主语义色填进来
	Font             string         // 正文字体家族；空用系统字体
	SelfChar         OKLCH          // 你的气泡用的角色色（私聊是对方角色，群聊用群的主色）
	Group            bool           // 群聊：别人的消息带头像和名字
	OnRetry          func(id string)
	Media            MediaResolver
	AvatarImage      func(messageID string) ui.ImageSource
	MenuTheme        *ui.Theme                          // 右键菜单用的主题；空就跟着当前主题
	ReactorImage     func(avatar string) ui.ImageSource // 表情回应上的小头像；取不到返回 nil，画首字
	OnDownload       func(messageID string, position int)
	OnCancelDownload func(messageID string, position int)
	OnOpenMedia      func(messageID string, position int)
	OnViewImage      func(messageID string, position int)
	OnForward        func(messageID string, position int)
	OnLink           func(url string)
	OnQuote          func(messageID, target string)
	// OnQuotePart 在点分块消息里的引用块时调用：宿主滚回被引的消息并设 Highlight。
	OnQuotePart func(messageID string, q PartQuote)
	// Highlight 是点引用回到原消息后要高亮的那一段（nil 不高亮）；整个窗口一份，宿主持有。
	Highlight *Highlight
	// Flash 给跳转到某条消息后的强调：返回这条消息此刻的强调强度（0 没有，1 最强）；nil 不强调。
	// 强调是气泡外一圈 3px 的强调色（照 Bun 的 su-flash：outline 3px），时序由宿主算，这里只管画。
	Flash func(messageID string) float32
	// Selection 不空时，消息正文里选中文字后浮出"回复 / 复制"小条（selection.go）；整个窗口共用一份状态，宿主持有。
	// OnReply 在点"回复"时调用：keepFocus 为假（按住 Alt 点的）表示只加卡、焦点不动；nil 时不摆"回复"。
	// Usage 给悬停操作条的“本轮用量”：返回这条消息所在那一轮的用量说明（悬停提示用），空表示没有数据，不摆这个钮；
	// OnUsage 在点它时调用（宿主切到右栏的上下文并展开这一轮）。任一为 nil 都不摆。
	Usage     func(messageID string) string
	OnUsage   func(messageID string)
	Selection *SelectionBar
	OnReply   func(sel Selected, keepFocus bool)
	// 悄悄话（whisper.go）：两个都设了，右键是"复制、多选、引用 ▸"，选区条和有选区的右键是"复制、引用 ▸"。
	// WhisperTargets 给出这条消息能指给的角色（宿主按会话取，标出说这句话的）；OnWhisper 在选了对象后调用
	// （ids 是选的角色，all 是"所有人"）。OnMultiSelect 不空时右键有"多选"，从这一条开始多选。
	WhisperTargets func(messageID string) []WhisperTarget
	OnWhisper      func(sel Selected, ids []string, all bool, keepFocus bool)
	OnMultiSelect  func(messageID string)
	// Pick 不空时在多选（pick.go）：每行右边一个勾选圈，点行任意处切换；PickOrder 给会话里消息的先后（Shift 点选范围用）。
	Pick      *Picker
	PickOrder func() []string
	// ViewRect 是聊天列可见范围（窗口坐标）；选区条据此决定放上面还是下面、左右夹到哪。nil 时只受窗口限制。
	ViewRect func() ui.Rect
	// Sticky 不空时群聊里别人的头像由 Sticky.Overlay 那一层画（钉在可见区域里），行里只留位置；nil 时头像画在组末那一行里。
	Sticky  *StickyAvatars
	Column  float32 // 消息列宽；气泡最大宽 = Column × BubbleShare
	NoEnter bool    // 关掉进场动画（首屏、虚拟列表回滚时）
	// Appear 用在虚拟列表里：mygo 的 List 不触发行的 Transition 进场（2026-11-06 实测），
	// 所以由调用方告诉"这条是不是刚到、这一帧是它第一次被画"，第一帧从下移 8、透明开始，之后用弹簧回到原位。
	// 设了它就不再用 Transition 那一套。
	Appear func(id string) bool
	// FollowingEnd 说列表现在是不是贴着末尾跟随（新内容进来就滚到底）。过程气泡、工具卡展开收起要动高度，只有它返回 false、
	// 并且这一行的顶部在 ViewRect 里才动（规格-过渡动画 铁律 4）；贴底时高度直接切换、内容淡入。nil 当成贴底：安全的做法。
	// 宿主一般给 !(list.FollowEnd && list.AtEnd())，ViewRect 给聊天列在窗口里的框。
	FollowingEnd func() bool
	soleSegment  bool  // 这一轮过程只有一段：外层展开就直接看内容，不再折第二层（主人 2026-11-09）
	procChar     OKLCH // 正在画的这一轮过程是哪个角色的（processBody 里设，kindIcon 用）
	motionOK     bool  // 铁律 4 的条件这一帧成立没有（processBubble 里算，展开收起的 disclose 用）

	// MD 是每条消息的 markdown 状态（宿主持有）；不空时她的回复正文用 markdown 画（流式时逐字出现），见 body.go。
	MD *MD
	// StepOpen 给过程里每段的展开状态（按段第一步的 ID，宿主持有）；nil 时过程不能展开。
	StepOpen func(id string) *bool
	// Mono 是等宽字体家族（路径、命令、输出、diff）；空用系统等宽。
	Mono string
	// LiveOpen：设置"进行中展开过程"（ADR-288，默认关）——她这一轮还在进行时过程气泡整段展开，做完收成一行。
	LiveOpen bool
	// 审批：主人允许（范围）、拒绝（why 为 "?" 时宿主弹出写原因的输入框）。
	OnApprove func(stepID string, scope ApprovalScope)
	// OnAnswer 是问题卡上点了第 option 个选项；QuestionSkip 是“先不答”（规格-向主人提问）。回答不是批准，所以不和 OnApprove 共用
	OnAnswer func(id string, option int)
	OnDeny   func(stepID, why string)
	// DenyReason 为真时"⋯"里有"拒绝并说明原因"（宿主能弹写原因的框）。
	DenyReason bool
	// BubbleGlass 让宿主把气泡做成半透明玻璃：e 是气泡，bg 是它实色时的底。宿主决定浓度，调用 e.Material 加模糊再叠 bg 的半透明底。
	// nil 就是实色，也是没有壁纸、系统要求降低透明度、材质设成实色时宿主该给的值。
	// 只用在对方的、自己的、过程气泡；图片气泡、撤回的消息、贴纸不用。
	// radii 是气泡四角半径：模糊材质和底色不会叠着画，填充色那层要宿主自己照这个圆角画在模糊上面。
	BubbleGlass func(e ui.Element, bg ui.Color, radii [4]float32)
	// SelfGlassTint 是玻璃气泡里“自己发的”保留多少主题色，0～1；0 当成 1（不减）。
	// 半透明的主题色叠在带颜色的壁纸上会比实色时更鲜艳，所以玻璃时往对方气泡的底色靠一些（主人 2026-11-15：“底色看起来有点太鲜艳了”）。
	SelfGlassTint float32
}

// MessageColors 是宿主语义色。缺的角色不在这里发明，调用方只填已经有的。
type MessageColors struct {
	InBackground, InText, InTime, InLink     ui.Color
	OutBackground, OutText, OutTime, OutLink ui.Color
}

func (h Host) accentOf(m *Msg) Accent {
	if m.Self {
		return h.T.AccentOf(h.SelfChar)
	}
	if m.Char == (OKLCH{}) {
		a := h.T.AccentOf(DefaultChar)
		a.Text = h.T.NameColor(m.NameID)
		return a
	}
	return h.T.AccentOf(m.Char)
}

// Gap 是 m 上面的间距：首条 0、组内 GapInGroup、组间 GapGroups。
func (h Host) Gap(prev, m *Msg) float32 {
	switch {
	case prev == nil:
		return 0
	case Joined(prev, m, h.T.JoinWindow):
		return h.T.GapInGroup
	default:
		return h.T.GapGroups
	}
}

// layout 是一条消息量出来的版式（纯计算，测试也用它）。
type layout struct {
	MaxW, TextW        float32
	Fit                float32 // 实际最长一行的宽度；正文按它收紧，左右内边距才一样
	ContentW           float32 // 气泡内容宽（正文、表情、时间共用）
	Info               string  // 右下角的时间
	InfoW, SkipW       float32 // 时间宽、占位块宽（时间 + 和前面内容的间距）
	InfoH              float32 // 时间实际渲染的高（量出来的，不按字号公式估）
	InfoBase           float32 // 时间文字框里基线离框顶多远
	LastW              float32 // 正文最后一行的宽
	InfoRight          float32 // 时间文字框右边离气泡右边多远
	InfoBottom         float32 // 时间文字框底边离气泡底边多远
	InfoInline         bool    // 时间挂在最后一行（正文最后一行或表情最后一排）里，没另起一行
	Chips              []chip  // 表情回应按钮的位置
	Userpics           bool    // 表情后面显示头像（整条消息不超过 3 人），否则显示数字
	ReactH             float32 // 表情区高（不含另起的时间行）
	TextH              float32 // 正文高（不含另起的时间行）
	SingleLine         bool
	Blocky             bool // markdown 正文里有段落以外的块（列表、代码块……）
	Lines              int
	Paras              int     // 段数（按空行分）
	Height             float32 // 气泡估算高度（决定多行圆角）
	Corners            Corners
	ShowName           bool
	ShowAvatar         bool // 组末才画头像，其余留位
	AvatarSlot         bool
	JoinPrev, JoinNext bool
	Media              mediaMode // 图片铺满气泡 / 贴纸无气泡（mediabubble.go）
	InfoChip           bool      // 时间是叠在图右下角的牌子，不占行
}

func (h Host) measure(c *ui.Context, m, prev, next *Msg) layout {
	t := h.T
	var l layout
	l.JoinPrev = Joined(prev, m, t.JoinWindow)
	l.JoinNext = Joined(m, next, t.JoinWindow)
	l.AvatarSlot = h.Group && !m.Self
	l.ShowAvatar = l.AvatarSlot && !l.JoinNext     // 头像跟着整组（过程气泡也算组里的一条），只画在组末
	l.ShowName = h.Group && !m.Self && !l.JoinPrev // 名字写在组里第一条的顶上（过程气泡也是气泡，ADR-287）
	l.MaxW = max(t.HLg*3, min(h.Column*t.BubbleShare, h.Column-h.rowReserve(m, l.AvatarSlot)))
	l.TextW = l.MaxW - t.BubblePadH*2
	text := displayText(m)
	var lines []float32
	var extraFit float32 // 非文字块的自然宽度，收紧时不能比它窄
	if len(m.Parts) > 0 {
		// 分块消息：宽度贴合内容（文字、引用块、标签、图各按自己画出来的宽度），和普通消息一个道理；
		// 时间另起一行，只有图在最后时叠在图上。第一块最前面是图时图铺满气泡（mediabubble.go）
		l.Media = partsMediaMode(m)
		flushW := float32(0)
		if l.Media != mediaNone {
			imgs, _ := m.Parts[0].split()
			flushW = albumLayout(imgs, l.MaxW).W
			l.TextW = max(1, flushW-t.BubblePadH*2)
		}
		pm := h.partsMeasure(m, l.TextW, l.MaxW)
		extraFit = pm.Fit
		l.Lines = 2 // 不当成单行胶囊
		l.Paras = len(m.Parts)
		l.TextH = pm.H
		l.LastW = -1
	} else if len(m.Blocks) > 0 {
		// 分段消息：文字块按真实排版量每一行的宽，最后一块是文字时时间挂在它的最后一行（Telegram 的占位块）；
		// 图片、视频、文件、卡片、转发按自己的自然宽度参与收缩，最后一块不是文字时时间另起一行（见 blockMeasure）
		l.Media = mediaModeOf(m)
		flushW := float32(0)
		if l.Media != mediaNone {
			// 图铺满：气泡宽 = 图宽，配文照这个宽度折行（Telegram 同样）
			flushW = mediaWidth(m, l.MaxW)
			l.TextW = max(1, flushW-t.BubblePadH*2)
		}
		bm := h.blockMeasure(m, l.TextW, flushW)
		lines, extraFit = bm.Lines, bm.Fit
		l.Blocky = false
		l.Lines = max(len(lines), 1)
		if bm.NonText {
			l.Lines = max(l.Lines, 2) // 不当成单行胶囊
		}
		l.Paras = len(m.Blocks)
		l.TextH = bm.H
		l.LastW = bm.LastW
	} else if h.useMD(m) {
		// markdown 正文：段落按同一套字体量；有别的块时宽度取满、时间另起一行（body.go mdMeasure）
		md := h.mdMeasure(m, l.TextW)
		lines, l.Paras, l.Blocky = md.Lines, md.Paras, md.Blocky
		l.Lines = len(lines)
		if md.Blocky {
			l.Lines = max(l.Lines, 2) // 不当成单行胶囊
		}
		l.TextH = md.EstH
		l.LastW = md.LastW
	} else {
		// 正文按空行分段（照 Grok：段距一个正文字号，--paragraph-margin:1em；单个换行只换行不加空隙；审查 C1）
		paras := paragraphs(text)
		for _, p := range paras {
			lines = append(lines, shapeLines(p, h.Font, t.Body, 1.6, l.TextW)...)
		}
		l.Lines, l.Paras = len(lines), len(paras)
		l.TextH = float32(l.Lines)*t.BodyLine + float32(l.Paras-1)*t.Body
		l.LastW = lines[len(lines)-1]
	}
	for _, ln := range lines {
		l.Fit = max(l.Fit, ln)
	}
	// 中文换行时最后一个字挤到下一行，按"最多能放多宽"排会在右边空一截；按实际最长一行收紧（Telegram 同样）。
	l.Fit = min(l.TextW, float32(math.Ceil(float64(l.Fit)))+1)
	l.Fit = min(l.TextW, max(l.Fit, extraFit))
	if l.Blocky {
		l.Fit = l.TextW // 列表、代码块不收紧，铺满
	}
	last := l.LastW

	// 右下角时间是一个"占位块"（Telegram skip block）：挂在最后一行末尾，放得下就同一行，放不下另起一行。
	// 已撤回、没发出去、被打断这些状态都和时间同一格，不在正文下面另起一行。
	if l.Info = infoText(m); l.Info != "" {
		w, ih := c.MeasureText(1e6, ui.Span{Text: l.Info, Size: t.FS[0], Font: h.Font, Features: Tabular})
		l.InfoW, l.InfoH = float32(math.Ceil(float64(w))), float32(math.Ceil(float64(ih)))
		l.InfoBase = baseline(l.Info, h.Font, t.FS[0])
		// 时间数字到气泡右、下一样远：基线离气泡底一个横内边距（挂在表情排时另算，见 layoutChips）
		l.InfoRight, l.InfoBottom = t.BubblePadH, t.BubblePadH-(l.InfoH-l.InfoBase)
		l.SkipW = l.InfoW + t.S2
	}
	if len(m.Reactions) == 0 {
		l.ContentW = l.Fit
		l.InfoInline = l.Info == "" || (last >= 0 && last+l.SkipW <= l.TextW) // last < 0：最后一块不是段落，时间另起一行
		if l.Info != "" {
			if l.InfoInline {
				l.ContentW = max(l.Fit, last+l.SkipW)
			} else {
				l.ContentW = max(l.Fit, l.SkipW)
			}
		}
	} else {
		h.layoutChips(c, m, &l)
	}
	l.ContentW = min(l.TextW, float32(math.Ceil(float64(l.ContentW))))
	if l.Media != mediaNone {
		l.ContentW = l.TextW // 气泡宽 = 图宽
		// 最后是图、下面没有表情回应条：时间叠在图上，不另起一行；有表情回应时时间挂在条的行尾（layoutChips 已排）
		if len(l.Chips) == 0 && l.Info != "" {
			if n := len(m.Blocks); n > 0 && isMediaBlock(m.Blocks[n-1]) {
				l.InfoChip, l.InfoInline = true, true
			}
			if len(m.Parts) > 0 && partsEndWithImage(m) {
				l.InfoChip, l.InfoInline = true, true
			}
		}
	}

	l.SingleLine = l.Lines == 1 && !strings.Contains(text, "\n") && !l.ShowName && m.Quote == nil && len(m.Reactions) == 0 && l.InfoInline
	l.Height = t.BubblePadV*2 + l.TextH
	if !l.InfoInline {
		l.Height += l.InfoH
	}
	if len(m.Reactions) > 0 {
		l.Height += t.BubblePadV + l.ReactH
	}
	if l.ShowName {
		l.Height += t.NameLine
	}
	if m.Quote != nil {
		l.Height += t.quoteHeight() + t.BubblePadV
		if l.ShowName {
			l.Height += t.S1
		}
	}
	l.Corners = BubbleCorners(t, m.Self, l.JoinPrev, l.JoinNext, l.SingleLine, l.Height)
	if len(l.Chips) > 0 {
		// 表情排从左下角开始，第一个按钮是胶囊：挨着它的气泡左下角按同心规则 = 按钮半径 + 竖内边距
		// （相贴的小角不动，那一角本来就是有意收小的）
		joinedBL := !m.Self && l.JoinNext
		if !joinedBL {
			l.Corners[3] = max(l.Corners[3], t.ChipH/2+t.BubblePadV)
		}
	}
	return l
}

func (t Tokens) quoteHeight() float32 {
	return t.S1*2 + round1(t.FS[2]*1.45)*2
}

// Message 画一条消息（含上间距、头像位、状态角标、气泡）。prev / next 用来判断合组。
func (h Host) Message(c *ui.Context, m, prev, next *Msg) {
	if m.Service != "" {
		h.ServiceRow(c, m, prev)
		return
	}
	t := h.T
	l := h.measure(c, m, prev, next)
	acc := h.accentOf(m)
	row := ui.Row(c.Key(m.ID)).Label("消息："+m.ID).Margin(h.Gap(prev, m), 0, 0, 0).AlignItems(ui.End)
	if m.Self {
		row.Justify(ui.End).Gap(t.S1)
	} else {
		row.Gap(t.S2)
	}
	if h.Appear != nil {
		// 第一帧给起点（下移 8、透明），之后目标是原位：Spring/AnimateTo 从上一帧的值走向目标；
		// 旧消息、重新挂上的行第一帧目标就是原位，不动；减少动态效果时 mygo 直接给终点（ui/motion.go:300）
		ty, to := float32(0), float32(1)
		if h.Appear(m.ID) {
			ty, to = 8, 0
		}
		spring := t.Spring.Firm
		if m.Self {
			spring = t.Spring.Bouncy // 自己发的用“弹跳”预设（主人要回弹，ADR-285 第 6 条）
		}
		row.Translate(0, row.Spring("appear-y", ty, spring)).Opacity(row.AnimateTo("appear-o", to, ui.Animation{Duration: t.Fast, NonMotion: true}))
	} else if !h.NoEnter && !l.JoinPrev {
		// 进场：透明 + 下移 8 → 原位；自己发的用“弹跳”预设（主人要回弹），别人的用“利落”（不回弹）。
		spring := t.Spring.Firm
		if m.Self {
			spring = t.Spring.Bouncy
		}
		// 只动"出现"这一下：不指定的话 mygo 会把位置、大小、颜色都一起动，回弹弹簧过冲会让消息行在列表重排时冲出边界
		// 系统要求减少动态效果时 mygo 自己只在原地淡入、不位移（ui/transition.go:85-88），这里不用再判断
		row.Transition(ui.ElementTransition{Animation: &ui.Animation{Spring: spring}, Enter: &ui.Motion{Y: 8, Opacity: 0}, Colors: true})
	}
	if h.Selection != nil {
		rowBox := h.Selection.handle("row:" + m.ID)
		row.Bind(rowBox)
		// 拖选拖出正文、在气泡旁边松手：正文自己的观察收不到松手，按住的标记会一直不清，选区条就永远不弹
		row.ObserveInput(func(ev ui.InputEvent) {
			if ev.Kind == ui.InputPointerUp && ev.Button == 0 {
				h.Selection.held = false
				b := rowBox.Bounds(c)
				h.Selection.recordUp(b.X+ev.X, b.Y+ev.Y)
			}
		})
	}
	if h.Pick != nil {
		row.Padding(0, t.pickGutter(), 0, 0) // 右边让出勾选圈的位置（左边是头像浮层）：只加右边距，行高不变
	}
	row.Children(func() {
		if h.Pick != nil {
			defer h.pickOverlay(c, m) // 最后画：盖在整行上，点哪里都是选这一条
		}
		if l.AvatarSlot {
			slot := ui.Box(c.Key("avatar-slot")).Size(t.AvatarMsg, t.AvatarMsg).Shrink(0)
			if h.Sticky != nil {
				// 头像交给 Sticky.Overlay 画：这一行只登记自己的框（行在层之前画）
				var img ui.ImageSource
				if h.AvatarImage != nil {
					img = h.AvatarImage(m.ID)
				}
				rec := avatarRec{id: m.ID, first: !l.JoinPrev, last: !l.JoinNext, size: t.AvatarMsg, pad: t.S2, draw: h.avatarDraw(m, acc, img)}
				row.Draw(func(_ *ui.Painter, r ui.Rect) {
					rec.x, rec.top, rec.bot = r.X, r.Y, r.Y+r.H
					h.Sticky.note(rec)
				})
			} else if l.ShowAvatar {
				slot.Children(func() {
					if h.AvatarImage != nil {
						if img := h.AvatarImage(m.ID); img != nil {
							ui.Image(c.Key("avatar"), img).Size(t.AvatarMsg, t.AvatarMsg).Radius(t.AvatarMsg / 2)
							return
						}
					}
					h.Avatar(c, "avatar", m.Name, acc, t.AvatarMsg)
				})
			}
		}
		if len(m.Steps) > 0 { // 过程气泡：她的一个气泡，里面折着这一轮两段回复之间的过程（ADR-287）
			h.processBubble(c, m, l, acc)
			return
		}
		if m.Self {
			h.actions(c, row, m) // 操作条在最靠聊天中间的一侧，状态位和气泡之间的次序不变（hoverbar.go）
			h.status(c, m)
		}
		h.bubble(c, m, l, acc)
		if !m.Self {
			h.status(c, m)
			h.actions(c, row, m)
		}
	})
}

// rowReserve 是一行里气泡以外的东西占的宽，加上它们和气泡之间的间距：头像格、状态位、操作条。
// 气泡最宽只能是列宽减去它们；列窄（右栏里的转发详情）时放不下，行会把气泡挤窄，而里面的正文已按更宽的排好，字就越出气泡。
func (h Host) rowReserve(m *Msg, avatar bool) float32 {
	t := h.T
	gap := t.S2
	if m.Self {
		gap = t.S1
	}
	var w float32
	if avatar {
		w += t.AvatarMsg + gap
	}
	switch m.State {
	case Failed, Interrupted, Sending:
		w += t.BadgeHit + gap
	}
	if n := h.actionCount(m); n > 0 {
		size := t.actionSize()
		w += size*n + t.S1*(n-1) + gap
	}
	return w
}

// status 是气泡朝聊天中间那一侧、贴底的状态位：自己的消息在气泡左边，是失败角标（可点重试）或发送中的时钟；
// 她的消息在气泡右边，回复被停下时是停止图标（和失败角标同一个大小，第三级色，不写"被打断了"，规格-过程组件 3.6）。
// 其余状态不占位。
func (h Host) status(c *ui.Context, m *Msg) {
	t := h.T
	switch m.State {
	case Failed:
		b := ui.ButtonBase(c.Key("retry")).Label("发送失败，重试").Size(t.BadgeHit, t.BadgeHit).Radius(t.BadgeHit / 2).
			Justify(ui.End).AlignItems(ui.End).OnClick(func() { // 按钮是横排：主轴居中、交叉轴贴底，圆点底边和气泡底边齐平
			if h.OnRetry != nil {
				h.OnRetry(m.ID)
			}
		})
		fill := t.C.Danger
		if b.Hovered() {
			fill = t.C.DangerHover
		}
		b.Children(func() {
			// 一整块图形：实心圆里挖空感叹号（透出底下的颜色），不是"圆 + 字"两样拼起来。
			ui.Icon(c.Key("badge"), Icon("alert", true)).Size(t.Badge, t.Badge).TextColor(b.AnimateColor("badge", fill, t.Fade()))
		})
	case Interrupted:
		ui.Row(c.Key("stopped")).Size(t.BadgeHit, t.BadgeHit).Justify(ui.Start).AlignItems(ui.End).Label("停下了").Children(func() {
			ui.Icon(c.Key("badge"), Icon("stop", false)).Size(t.Badge, t.Badge).TextColor(t.C.Text3)
		})
	case Sending:
		// 和失败角标同位：同一个 24 的格子、图标贴右下（规格-界面令牌和消息气泡 第四节"和失败角标同位"）
		ui.Row(c.Key("sending")).Size(t.BadgeHit, t.BadgeHit).Justify(ui.End).AlignItems(ui.End).Label("正在发送").Children(func() {
			ui.Icon(c, Icon("time", false)).Size(t.FS[1], t.FS[1]).TextColor(t.C.Text3)
		})
	}
}

// lift 让气泡浮起一点：sh-1 两层投影（Bun chat.css:120、529；设计语言第三节）。有壁纸时浮起；用主题配色的浅色气泡浮在聊天底上；
// 深色靠底色比聊天底亮一档，不画阴影。对方的、自己的、过程气泡共用这一条，三者浮得一样。
func (h Host) lift(e ui.Element, m *Msg) {
	t := h.T
	if t.S.Wallpaper || (h.Colors != nil && !t.S.Dark && m.State != Recalled) {
		e.Shadow(0, 0.5, 1, 0, t.C.ShadowA).Shadow(0, 1, 3, 0, t.C.ShadowB)
	}
}

// 玻璃气泡上沿那一道高光的不透明度：浅色用面色，深色用正文色，都只是一道发丝线，不是描边。
const (
	bubbleRimLight float32 = .8
	bubbleRimDark  float32 = .12
)

// fill 给气泡上底：玻璃时是宿主给的模糊材质加半透明底，上沿一道很淡的高光（和 lift 的贴身短阴影配合，边界不靠描边）；否则就是实色。
func (h Host) fill(e ui.Element, bg ui.Color, radii Corners, glass bool) {
	if !glass || h.BubbleGlass == nil {
		e.Background(bg)
		return
	}
	t := h.T
	h.BubbleGlass(e, bg, [4]float32(radii))
	rim := t.C.Surface.Alpha(bubbleRimLight)
	if t.S.Dark {
		rim = t.C.Text.Alpha(bubbleRimDark)
	}
	e.InsetShadow(0, 1, 0, 0, rim)
}

func (h Host) bubble(c *ui.Context, m *Msg, l layout, acc Accent) {
	t := h.T
	bg, ink := t.C.Bubble, t.C.Text
	if m.Self {
		bg = acc.Soft
		if k := h.SelfGlassTint; h.BubbleGlass != nil && k > 0 && k < 1 {
			bg = t.C.Bubble.Mix(acc.Soft, k)
		}
	}
	link := acc
	if h.Colors != nil && m.State != Recalled {
		bg, ink = h.Colors.InBackground, h.Colors.InText
		link.Text = h.Colors.InLink
		if m.Self {
			bg, ink = h.Colors.OutBackground, h.Colors.OutText
			link.Text = h.Colors.OutLink
		}
	}
	pv, ph := t.BubblePadV, t.BubblePadH
	side := ph - pv // 文字比块多缩进的那一截：块到气泡边 = pv，文字到气泡边 = ph
	flush := l.Media != mediaNone
	pad := pv
	if flush {
		pad = 0 // 图铺满：气泡没有内边距，文字块自己缩进
	}
	if l.Media == mediaSticker {
		bg = ui.Color{} // 贴纸没有底
	}
	e := ui.Column(c.Key("bubble")).Label("气泡").MaxWidth(l.MaxW).MinWidth(0).Padding(pad).Radius(l.Corners[:]...)
	h.fill(e, bg, l.Corners, h.Colors == nil && m.State != Recalled && l.Media == mediaNone)
	if flush {
		e.Clip() // 按气泡的圆角裁图：外侧四角跟气泡同心
	}
	// 两边的气泡同一种"浮起来"：同一组投影、都不描边，自己的靠底色（acc.Soft）和对方分开（主人 2026-11-15："一边没边框、一边有边框，边框那边更突出"）
	if l.Media != mediaSticker {
		h.lift(e, m)
	}
	if m.State == Recalled && l.Media != mediaSticker {
		// Bun 是整块 opacity 0.55，字会掉到约 2.3:1；这里只换底和描边表示"已撤回"，字保持实色（审查 A1）
		e.Background(t.C.ChatBg).InsetShadow(0, 0, 0, 1, t.C.LineStrong)
	}
	if h.Flash != nil {
		if a := h.Flash(m.ID); a > 0 {
			e.Shadow(0, 0, 0, 3, acc.Base.Alpha(a))
		}
	}
	e.Children(func() {
		if l.ShowName && l.Media != mediaSticker {
			name := ui.Text(c.Key("name"), m.Name).Font(h.Font).FontSize(t.FS[3]).FontWeight(t.Weight[2]).FixedLineHeight(t.NameLine).
				TextColor(acc.Text).Padding(0, side).SingleLine().Translate(0, textNudge(t.FS[3]))
			if flush {
				name.Margin(pv, pv, 0, pv) // 名字区有内边距，图从名字下面接着铺满
			}
		}
		if m.Quote != nil {
			// 引用块和正文之间隔一个竖内边距（和它到气泡边的距离相等）；名字下面只隔 s1，名字和引用是一组
			top := float32(0)
			if l.ShowName {
				top = t.S1
			}
			q := h.quote(c, m.ID, m.Quote, l, acc).Margin(top, 0, pv, 0)
			if flush {
				// 图铺满、气泡没有内边距：引用卡自己离上、左、右、下各一个竖内边距（名字在上面时上边只隔 s1），宽度按图宽，长引用不撑宽气泡
				if !l.ShowName {
					top = pv
				}
				q = q.Margin(top, pv, pv, pv).Width(l.ContentW + 2*(ph-pv))
			}
			if !m.Quote.Gone && !m.Quote.Preview && m.Quote.Target != "" && h.OnQuote != nil {
				q.OnClick(func() { h.OnQuote(m.ID, m.Quote.Target) })
			}
		}
		if m.State == Recalled {
			ink = t.C.Text3
		}
		// 时间另起一行时，最后一块（正文或表情区）下面让出一行时间高
		extra := float32(0)
		if !l.InfoInline {
			extra = l.InfoH
		}
		var txt, md ui.Element // md：她的 markdown 正文这一帧的元素，问它字出完没有（timeReady）
		blockReady := true
		if len(m.Parts) > 0 {
			txt = h.partsBlock(c, m, l, ink, link)
		} else if len(m.Blocks) > 0 {
			txt, blockReady = h.blocks(c, m, l, ink, link)
		} else if h.useMD(m) {
			// 她的回复用 markdown：还在出字时 Stream（逐字淡入），完成后在同一位置换成 View，没出完的字接着出（mygo view.go:106）
			opts := h.options(c, m, ink, link)
			h.MD.entry(m.ID).width = l.ContentW // 表格排进这么宽
			src := mdSource(m)
			// 文字比气泡的块多缩进 side：用外边距不用内距（几何规则只给"字行"左右内距的例外，这一层是容器）
			// 外层定宽（气泡按它收缩）、不设内距；缩进放在 markdown 自己的外边距上（气泡收缩时不算子元素的外边距）
			txt = ui.Column(c.Key("text")).Width(l.ContentW+side*2).MinWidth(0).Translate(0, textNudge(t.Body))
			txt.Children(func() {
				if strings.TrimSpace(src) == "" { // 还没有字（刚开始出、只有思考段）：占一行，和 mdMeasure 量的一样
					ui.Box(c.Key("md-empty")).Height(t.BodyLine)
					return
				}
				md = h.mdSelectable(c, "md", m, 0, side, func(k *ui.Context) ui.Element {
					if m.State == Streaming {
						return markdown.Stream(k, src, opts)
					}
					return markdown.View(k, src, opts)
				})
			})
		} else if paras := paragraphs(displayText(m)); len(paras) == 1 {
			txt = ui.Text(c.Key("text"), paras[0]).Font(h.Font).FontSize(t.Body).FixedLineHeight(t.BodyLine).TextColor(ink).
				Padding(0, side).Width(l.ContentW+side*2).MinWidth(0).Selectable().Translate(0, textNudge(t.Body))
			h.textMenu(c, txt, m.ID+"#plain", m, nil)
		} else {
			// 几段：段间隔一个正文字号；整块可以连着选
			txt = ui.Column(c.Key("text")).Gap(t.Body).Padding(0, side).Width(l.ContentW+side*2).MinWidth(0).Selectable().Translate(0, textNudge(t.Body))
			txt.Children(func() {
				for i, p := range paras {
					ui.Text(c.Key(i), p).Font(h.Font).FontSize(t.Body).FixedLineHeight(t.BodyLine).TextColor(ink).FillWidth()
				}
			})
			h.textMenu(c, txt, m.ID+"#plain", m, nil)
		}
		if len(l.Chips) == 0 {
			txt.Margin(0, 0, extra, 0)
		} else {
			// 表情排是"块"：离气泡左、下都是竖内边距（和引用块一样），和正文之间也隔竖内边距
			r := h.reactions(c, l, acc, m.Self).Margin(pv, 0, extra, 0)
			if flush {
				r.Margin(pv, pv, pv+extra, pv) // 气泡没有内边距：表情条自己离左、右、下各一个竖内边距，离上面的图或字也是
			}
		}
		// 时间：markdown 正文出字时不显示、全部出完才显示（位置在排版里一直留着，出现时不跳）
		if l.Info != "" && ((len(m.Blocks) > 0 && blockReady) || (len(m.Blocks) == 0 && (len(m.Parts) > 0 || !h.useMD(m) || timeReady(c, m, md)))) {
			if l.InfoChip {
				h.infoChip(c, l, e.Hovered())
			} else {
				h.info(c, l, m.Self, acc, fullTime(m))
			}
		}
	})
}

// infoText 是气泡右下角那一格的字：状态在前、时间在后。
// 正文已经写了“这条消息已撤回”（原文是空的）就不再重复。
func infoText(m *Msg) string {
	saidInBody := m.State == Recalled && m.Text == "" && len(m.Blocks) == 0 && len(m.Parts) == 0
	var parts []string
	for _, note := range m.Notes {
		if note != recalledNote {
			parts = append(parts, note)
		}
	}
	if !saidInBody && (m.State == Recalled || slices.Contains(m.Notes, recalledNote)) {
		parts = append(parts, recalledNote)
	}
	if at := clockText(m.At); at != "" {
		parts = append(parts, at)
	}
	return strings.Join(parts, " ")
}

// clockText 是聊天区显示时间的写法（时:分，零值返回空串不编）；气泡右下角和引用卡头共用。
func clockText(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format("15:04")
}

const recalledNote = "已撤回"

// fullTime 是时间悬停提示里的完整日期时间；没有时间返回空串（不编）。
func fullTime(m *Msg) string {
	if m.At.IsZero() {
		return ""
	}
	full := m.At.Format("2006年1月2日 15:04:05")
	if m.Tip != "" {
		full += "\n" + m.Tip
	}
	return full
}

// info 是右下角的时间：右边和正文右沿对齐；挂在最后一行里时和那一行竖直居中，另起一行时贴底。
func (h Host) info(c *ui.Context, l layout, self bool, acc Accent, full string) {
	t := h.T
	// 时间数字到气泡右边、下边一样远，都是横内边距（主人 11-06：右边近、下边远）。
	// 数字压在基线上，所以把基线放在离气泡底一个横内边距的地方；文字框底下那段留白（框高 − 基线）往下伸。
	bottom, right := l.InfoBottom, l.InfoRight
	ink := t.C.Text3
	if self {
		ink = acc.Text
	}
	if h.Colors != nil {
		ink = h.Colors.InTime
		if self {
			ink = h.Colors.OutTime
		}
	}
	tm := ui.Text(c.Key("time"), l.Info).Font(h.Font).FontSize(t.FS[0]).FontFeatures(Tabular).Height(l.InfoH).TextColor(ink).
		Absolute().Right(right).Bottom(bottom).SingleLine()
	if full != "" {
		tm.Tooltip(full) // 悬停看完整日期时间（气泡里只写时分）
	}
}

// quote 是气泡里顶部的引用块：离气泡上、左、右都是竖内边距，圆角和气泡同心。
func (h Host) quote(c *ui.Context, messageID string, q *Quote, l layout, acc Accent) ui.Element {
	t := h.T
	if q.Owner {
		acc = t.AccentOf(h.SelfChar)
	}
	name, text := q.Name, q.Text
	if name == "" {
		name = "某人"
	}
	if text == "" {
		text = "[消息]"
	}
	if q.Gone {
		text = "原消息不在本地"
	}
	r := QuoteCorners(t, l.Corners, l.ShowName)
	// 内距四边一样，取"竖条和正文落在同一条竖线上"那个值（横内边距 − 竖内边距）：竖条离引用块上、下、左等距；
	// 字离右边至少 s2，多出来的那截是文字列自己的右外边距（2026-11-06 审查 C5 定的口径）
	in := t.BubblePadH - t.BubblePadV
	e := ui.Row(c.Key("quote")).Label("引用").Gap(t.S2).Padding(in).Background(t.C.Hover).Radius(r[:]...)
	e.Children(func() {
		ui.Box(c.Key("bar")).Width(quoteBarBase * t.TS).AlignSelf(ui.Stretch).Radius(quoteBarBase * t.TS / 2).Background(acc.Base)
		h.quoteThumb(c, messageID, q, r, in)
		ui.Column(c.Key("q")).MinWidth(0).Margin(0, t.S2-in, 0, 0).Children(func() {
			ui.Text(c.Key("qn"), name).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).FixedLineHeight(round1(t.FS[2]*1.45)).TextColor(acc.Text).SingleLine().Translate(0, textNudge(t.FS[2]))
			ui.Text(c.Key("qt"), text).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(round1(t.FS[2]*1.45)).TextColor(t.C.Text2).SingleLine().Translate(0, textNudge(t.FS[2]))
		})
	})
	return e
}

// QuoteThumbSize 是引用缩略图的边长：等于引用块里两行字的高（引用块的内高），所以有没有缩略图块高不变。
func (t Tokens) QuoteThumbSize() float32 { return round1(t.FS[2]*1.45) * 2 }

// quoteThumb 画引用块里竖线右边的方形缩略图：圆角和引用块同心（引用块圆角 − 内距）。
// 加载中占位（块宽不跳）；取不到或宿主没实现就什么都不画，引用块退回只有文字。
func (h Host) quoteThumb(c *ui.Context, messageID string, q *Quote, r Corners, in float32) {
	if q.Thumb == "" || q.Gone {
		return
	}
	qm, ok := h.Media.(QuoteMedia)
	if !ok {
		return
	}
	t := h.T
	size := t.QuoteThumbSize()
	res := qm.QuoteThumb(messageID, int(size))
	if res.State == MediaFailed || res.State == MediaMissing {
		return
	}
	radius := t.Inner(min(r[0], r[3]), in)
	box := ui.Box(c.Key("thumb")).Label("引用的图片").Size(size, size).Shrink(0).Radius(radius).Background(t.C.Surface).Clip()
	box.Children(func() {
		if res.State == MediaReady && res.Image != nil {
			ui.Image(c.Key("img"), res.Image).Size(size, size).Fit(ui.Cover)
		}
		if q.Thumb == ThumbVideo {
			ui.Box(c.Key("play")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Children(func() {
				ui.Box(c.Key("disc")).Size(size*0.46, size*0.46).Radius(size).Background(ui.RGBA(0, 0, 0, 0.5)).Center().Children(func() {
					ui.Icon(c, Icon("play", true)).Size(size*0.26, size*0.26).TextColor(ui.RGB(255, 255, 255))
				})
			})
		}
	})
}

// ServiceRow 是消息列里的通知条：进群、退群、禁言、改名片这类群事件，居中一行小字胶囊（照 Telegram 的服务消息）。
// 不属于任何人，所以没有头像、没有名字，不能选中、回复，前后的气泡也不会隔着它连在一起。
func (h Host) ServiceRow(c *ui.Context, m, prev *Msg) {
	t := h.T
	line := round1(t.FS[1] * 1.6)
	ui.Row(c.Key(m.ID)).Label("通知："+m.Service).FillWidth().Margin(h.Gap(prev, m), 0, 0, 0).Justify(ui.Center).Children(func() {
		if m.Icon != "" { // 宿主写的系统行（目标、定期任务、命令）：同一个胶囊，文字前多一个来源小图标，和文字同色、顶着第一行
			size := round1(t.FS[1] * 1.15)
			pill := ui.Row(c.Key("service-pill")).AlignItems(ui.Start).Gap(t.S1).Padding(0, t.S3).Radius(line / 2).Background(t.C.Surface.Alpha(.72))
			if h.Column > 0 {
				pill.MaxWidth(h.Column * .8)
			}
			pill.Children(func() {
				ui.Icon(c.Key("service-icon"), Icon(m.Icon, false)).Size(size, size).Shrink(0).Margin((line-size)/2, 0, 0, 0).TextColor(t.C.Text3)
				ui.Text(c.Key("service-text"), m.Service).Font(h.Font).FontSize(t.FS[1]).FixedLineHeight(line).TextColor(t.C.Text2).Shrink(1).MinWidth(0).Wrap()
			})
			return
		}
		text := ui.Text(c.Key("service-text"), m.Service).Font(h.Font).FontSize(t.FS[1]).FixedLineHeight(line).TextColor(t.C.Text2).
			Padding(0, t.S3).Radius(line / 2).Background(t.C.Surface.Alpha(.72)).Wrap()
		if h.Column > 0 {
			text.MaxWidth(h.Column * .8)
		}
	})
}

// avatarRadius 是头像圆角：本地角色是圆角方（照 Bun .su-avatar，0.32 倍），QQ 上的人是圆的。
func (h Host) avatarRadius(size float32) float32 {
	if h.SquareAvatar {
		return size * .32
	}
	return size / 2
}

// Avatar 是首字头像：角色色实底、它上面的字色（照 Bun .su-avatar-face）。
func (h Host) Avatar(c *ui.Context, key, name string, acc Accent, size float32) {
	t := h.T
	initial := []rune(name)
	s := " "
	if len(initial) > 0 {
		s = string(initial[0])
	}
	ui.Box(c.Key(key)).Size(size, size).Radius(h.avatarRadius(size)).Background(acc.Face).Center().Label(name).Children(func() {
		ui.Text(c, s).Font(h.Font).FontSize(round1(size * AvatarInitial)).FontWeight(t.Weight[3]).TextColor(acc.On)
	})
}

// Typing 是"正在输入"：她那侧一个单行高的气泡，里面三个点呼吸（Bun chat.css:231-237）。
func (h Host) Typing(c *ui.Context, key string, char OKLCH) {
	t := h.T
	_ = char
	height := t.BubblePadV*2 + t.BodyLine
	row := ui.Row(c.Key(key)).Label("正在输入").Margin(t.GapGroups, 0, 0, 0).Gap(t.S2).AlignItems(ui.End)
	row.Children(func() {
		if h.Group {
			ui.Box(c.Key("avatar-slot")).Size(t.AvatarMsg, t.AvatarMsg).Shrink(0)
		}
		// 点的大小和位置和过程气泡摘要行开头那组一样：开始想时同一组点后面长出“正在想”，看不出换了东西
		ui.Row(c.Key("bubble")).Height(height).Padding(0, t.BubblePadV).AlignItems(ui.Center).Background(t.C.Bubble).Radius(height / 2).Children(func() {
			pv := t.BubblePadV
			h.lead(c, pv, 0, func() {
				dot := h.leadSlot(pv) * typingDotBase / (3*typingDotBase + 2*typingGapBase)
				ui.Row(c.Key("dots")).Gap(dot * typingGapBase / typingDotBase).AlignItems(ui.Center).Children(func() { h.dotsSized(c, dot) })
			})
		})
	})
}

// dotsSized 画三个上下浮动的点，点的直径是 dot（浮动幅度跟着按比例缩放）。
func (h Host) dotsSized(c *ui.Context, dot float32) {
	t := h.T
	for i := 0; i < 3; i++ {
		d := ui.Box(c.Key(i)).Size(dot, dot).Radius(dot / 2).Background(t.C.Text3)
		delay := time.Duration(i) * typingStep
		o := d.Keyframes("o", []ui.Keyframe{{At: 0, Value: 0.35}, {At: typingPeak, Value: 1}, {At: typingFall, Value: 0.35}, {At: typingCycle, Value: 0.35}},
			ui.KeyframeOptions{Repeat: ui.RepeatForever, Delay: delay})
		lift := dot * typingLiftBase / typingDotBase
		y := d.Keyframes("y", []ui.Keyframe{{At: 0, Value: 0}, {At: typingPeak, Value: -lift}, {At: typingFall, Value: 0}, {At: typingCycle, Value: 0}},
			ui.KeyframeOptions{Repeat: ui.RepeatForever, Delay: delay})
		if c.Preferences().ReduceMotion {
			o, y = 0.6, 0
		}
		d.Opacity(o).Translate(0, y)
	}
}

// paragraphs 把正文按空行（中间只有空白的换行）分成几段；单个换行留在段里。
func paragraphs(text string) []string {
	parts := paraBreak.Split(text, -1)
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

var paraBreak = regexp.MustCompile(`\n[ \t]*\n\s*`)

// displayText 是气泡里实际显示的正文：撤回的照样显示原文（主人 2026-11-06："QQ 那个提示方式会丢失撤回消息的内容显示"），
// 原文是空的（只撤回了图片之类）才写"这条消息已撤回"；空的给一个空格占行高。
func displayText(m *Msg) string {
	switch {
	case m.State == Recalled && m.Text == "":
		return "这条消息已撤回"
	case m.Text == "":
		return " "
	}
	return m.Text
}

// textNudge 是把字形在行框里挪到上下居中要往上挪的量：行框上面留白比下面多，
// 实测文渊圆体和系统字体都偏下约字号的 6%（气泡单行：墨迹到上 14.25、到下 12.35，字号 16）。
func textNudge(size float32) float32 { return -0.06 * size }
