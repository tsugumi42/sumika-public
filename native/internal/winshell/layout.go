package winshell

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// Panes 是要存进桌面设置的分栏偏好。三个字段都用“0 = 默认”，读到的坏值由调用方先夹住。
type Panes struct {
	ListRatio float32 // 会话列表宽 / 主体宽（窗口宽减导航宽）；0 = 默认宽 ListDefault，拖过才有值
	Compact   bool    // 会话列表收成头像列
	SideWidth float32 // 右侧栏宽；0 = 默认 320，夹在 292～392
	SideOpen  bool    // 右侧栏开着
}

// Layer 是窄窗口（窄于 BreakpointNarrow）三层里当前显示哪一层。
type Layer int

const (
	LayerList Layer = iota // 导航 + 会话列表
	LayerChat              // 聊天（没有导航）
	LayerSide              // 资料与活动
)

// State 是外壳自己持有的状态：分栏、层、抽屉动画、搜索框焦点。
// 宽度由 Plan 每帧按窗口宽重算；会进设置的只有 Panes。
type State struct {
	Panes Panes

	Layer      Layer
	layerShown Layer
	layerSeen  bool
	layerDir   float32 // 最近一次换层的方向：1 往里走、-1 返回、0 还没换过
	// 浮动右栏（PanelFloat）：live 是浮层还在树里（含退场动画中）；entered 是进过场了（打开的第一帧才从起点滑）；
	// spec 是最后一次开着时的右栏内容，退场动画结束前照它画，不跟着宿主的新状态变空
	float struct {
		live, entered bool
		spec          SideSpec
	}

	ListWidth, SideShown, ChatWidth float32 // 这一帧实际的列表宽、右栏宽、聊天宽
	listDragged                     bool
	// 列表分隔条的拖动：按住时 raw 跟着指针走（不夹，夹过的值才是显示宽），松手那一帧才决定收成头像列还是展开。
	listDrag struct {
		active, moved bool
		raw           float32
	}
	listShown   float32   // 上一帧列表实际显示的宽（含过渡动画中的值）
	compactView bool      // 这一帧列表画成头像列：按显示宽判断，所以拖动和过渡中内容跟着宽度换
	SearchField ui.Handle // 搜索框；头像列点放大镜后把焦点给它
	Convs       ui.ListState
	menuOpen    bool // “+”菜单开着

	// 换内容的淡入（fadeIn）：page 是上一帧的页，jump 是这一帧换了页；chat、cat 是上一次画的会话、设置分类，换了就淡入
	fade struct {
		page, chat, cat, tab                 string
		pageSeen, chatSeen, catSeen, tabSeen bool
		jump                                 bool
	}

	// 聊天区自己量：消息列版心、输入区高度（输入区浮在消息上，消息列底边要让出这块）
	Area, Composer       ui.Handle
	ComposerH            float32
	lastW, lastH, lastVW float32
	// 聊天区上下两个浮层（标题栏、输入区含引用卡和附件条）各占多高，实测；消息列的可见区是它的框去掉这两块
	overlayTop, overlayBottom float32
	lastChatW                 float32 // 聊天栏上一次量到的宽
}

// Wide 是窗口宽度所处的档位。
type Wide int

const (
	Narrow Wide = iota // 窄于 705：三层
	Medium             // 705～997：导航 + 列表 + 聊天，右栏浮在聊天上（PanelFloat）
	Full               // 998 以上：三栏并排
)

// PanelMode 是右栏怎么呈现：聊天区给右栏预留多少宽由它决定（SideReserve），别的面板以后也照这个分。
type PanelMode int

const (
	PanelPush  PanelMode = iota // 挤开：并排在聊天右边，聊天让出右栏宽，两边各有分隔条
	PanelFloat                  // 浮动：盖在聊天区上，聊天区宽度不变，不压暗
	PanelLayer                  // 整层：窄窗口三层里的第三层
)

// SideMode 是这个窗口宽度下右栏的呈现方式：放得下三栏并排挤开，放不下浮在聊天上，窄窗口是整层。
func (k *Kit) SideMode(w float32) PanelMode {
	switch k.Mode(w) {
	case Full:
		return PanelPush
	case Medium:
		return PanelFloat
	}
	return PanelLayer
}

// SideReserve 是聊天区为右栏让出的宽（含分隔条）：只有挤开且开着才让，浮动和整层不让。
func (k *Kit) SideReserve(s *State, w float32) float32 {
	if k.SideMode(w) == PanelPush && s.Panes.SideOpen {
		return s.SideShown + SplitSize
	}
	return 0
}

// SideFloatLive 说浮动右栏还在画（含退场动画中）：这时 Esc 归它，宿主别拿去停回复。
func (s *State) SideFloatLive() bool { return s.float.live }

// SideFloating 说右栏现在要不要画成浮动面板（开着，且窗口放不下三栏并排）。
func (k *Kit) SideFloating(s *State, w float32) bool {
	return k.SideMode(w) == PanelFloat && s.Panes.SideOpen && s.Layer == LayerSide
}

// Mode 返回窗口宽度所处的档位。
func (k *Kit) Mode(w float32) Wide {
	switch {
	case w < k.G.BreakpointNarrow:
		return Narrow
	case w < k.G.BreakpointWide:
		return Medium
	}
	return Full
}

// Plan 定会话列表和右栏宽，照 Telegram（window_session_controller.cpp:2766-2858；ADR-285 第 1、2 条）：
// 列表默认 ListDefault，至少 260（拖过就用拖出来的比例）；两栏时给聊天留够 380。
// 三栏时右栏取设定宽（默认 320，夹在 292～392）；列表 + 右栏放不下就按两者现在的比例一起收，
// 右栏收到 292 或列表收到 260 就停在那，剩下的都给聊天。settings 为真时左栏只放分类，固定宽。
func (k *Kit) Plan(s *State, w float32, settings bool) {
	g := k.G
	s.compactView = s.Panes.Compact
	body := w - g.NavWidth
	if settings && !s.Panes.Compact {
		s.ListWidth = min(SettingsListWidth, body-ChatMin-SplitSize)
		s.SideShown = min(max(s.sideWant(), SideMin), SideMax)
		return
	}
	list := ListDefault
	if s.Panes.ListRatio > 0 {
		list = min(s.Panes.ListRatio*body, ListMax)
	}
	list = max(list, ListMin)
	if s.Panes.Compact {
		list = g.ListCompactWidth
	}
	// 从三栏拖窄到抽屉档：开着的右栏要接着显示，Layer 跟上 SideOpen（抽屉靠 Layer 判断）
	if k.Mode(w) == Medium && s.Panes.SideOpen && s.Layer == LayerChat {
		s.Layer = LayerSide
	}
	side := min(max(s.sideWant(), SideMin), SideMax)
	if k.SideMode(w) == PanelPush && s.Panes.SideOpen {
		room := body - ChatMin - 2*SplitSize
		if list+side > room {
			l := float32(math.Floor(float64(room * list / (list + side))))
			r := float32(math.Floor(float64(room * side / (list + side))))
			switch {
			case r < SideMin:
				r, l = SideMin, room-SideMin
			case l < ListMin && !s.Panes.Compact:
				l, r = ListMin, room-ListMin
			}
			list, side = l, r
		}
		s.ListWidth, s.SideShown = list, side
		return
	}
	if !s.Panes.Compact {
		list = max(ListMin, min(list, body-ChatMin-SplitSize))
	}
	s.ListWidth, s.SideShown = list, side
}

func (s *State) sideWant() float32 {
	if s.Panes.SideWidth > 0 {
		return s.Panes.SideWidth
	}
	return SideDefault
}

// DragList 处理拖分隔条：拖过就记下比例、不再用默认宽；拖到比 130 窄收成头像列，从头像列拖宽过 130 就恢复。
func (k *Kit) DragList(s *State, w float32, settings bool) {
	body := w - k.G.NavWidth
	if body > 0 && s.ListWidth >= ListCompactDrag { // 收起时不改比例：展开回到原来的宽
		s.Panes.ListRatio = min(max(s.ListWidth, ListMin), ListMax) / body
	}
	s.listDragged = true
	switch {
	case s.ListWidth < ListCompactDrag:
		s.Panes.Compact = true
	case s.Panes.Compact && s.ListWidth > ListCompactDrag:
		s.Panes.Compact = false
	}
	k.Plan(s, w, settings)
}

// listUpper 是拖动时列表宽的上限：不超过 ListMax，也给聊天留够 ChatMin（右栏并排时再给右栏留最小宽）。
func (k *Kit) listUpper(s *State, w float32) float32 {
	room := w - k.G.NavWidth - ChatMin - SplitSize
	if k.SideMode(w) == PanelPush && s.Panes.SideOpen {
		room -= SideMin + SplitSize
	}
	return max(ListMin, min(ListMax, room))
}

// ToggleList 收起或展开会话列表（Ctrl+B）：收起是头像列，展开回到原来的宽。
func (s *State) ToggleList() { s.Panes.Compact = !s.Panes.Compact }

// ToggleSide 开关右侧栏（Ctrl+\）。窄窗口里右栏是第三层，开关等于进出这一层。
func (k *Kit) ToggleSide(s *State, w float32) {
	if k.Mode(w) == Narrow {
		if s.Layer == LayerSide {
			s.CloseSide()
		} else {
			s.Layer = LayerSide
		}
		return
	}
	if k.Mode(w) == Medium {
		// 中等宽度右栏是盖在聊天上的抽屉：开着时 Layer 记成第三层，Esc、遮罩、返回钮都落回聊天
		if s.Layer == LayerSide && s.Panes.SideOpen {
			s.CloseSide()
		} else {
			s.Panes.SideOpen, s.Layer = true, LayerSide
		}
		return
	}
	s.Panes.SideOpen = !s.Panes.SideOpen
}

// OpenSide 打开右栏且不看窗口宽：SideOpen 和 Layer 一起设，三种档位都能显示
// （三栏看 SideOpen，抽屉和窄窗口看 Layer；三栏时 Layer 多出来的值无害）。
func (s *State) OpenSide() {
	s.Panes.SideOpen = true
	s.Layer = LayerSide
}

// CloseSide 关掉右栏或抽屉（抽屉的 Esc、点遮罩、返回钮都走这里）。
func (s *State) CloseSide() {
	s.Layer = LayerChat
	s.Panes.SideOpen = false
}

// OpenChat 点会话后进聊天层。
func (s *State) OpenChat() { s.Layer = LayerChat }

// ShowList 回到列表层（导航切页、返回钮）。
func (s *State) ShowList() { s.Layer = LayerList }

// OverlayInsets 是聊天区上下两个浮层各占多高（实测，随输入区内容变化，不写死）。
// 消息列铺满聊天区，真正看得见、不被浮层盖住的区域是列表框去掉这两块：粘性头像、“跳到最新”按钮、
// 判断有没有滚到底都按这个算，而不是按列表框的上下沿。
func (s *State) OverlayInsets() (top, bottom float32) { return s.overlayTop, s.overlayBottom }

// ChatColumnWidth 是聊天栏上一次量到的宽（0 表示还没画过聊天区）。
func (s *State) ChatColumnWidth() float32 { return s.lastChatW }
