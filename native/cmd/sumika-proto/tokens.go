package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 几何唯一数值表见规格十一节；嵌套半径从父半径减去轮廓距离。
//
// 分两类（审查 B1：外观的字号、圆角、疏密滑块要铺满整窗，不只是聊天气泡）：
//   - 常量：窗口分栏的宽度照 Telegram 固定（导航 72、列表 260～540、聊天至少 380、右栏 292～392），不随外观缩放；
//   - 变量：间距、字号行高、控件高、头像、圆角、字重，每次外观设置一变就由 applyLook 整套重算。
//     比例以默认设置为 1（默认外观下每个数都和下面写的基准一样）：字号和控件高 × 字号/16，
//     间距 × 疏密/8 × 字号/16，圆角 × 圆角/12 × 字号/16（Bun 的 --ts、--g、--r 也是这样整窗一起变，appearance.ts:149-153）。
const (
	splitSize                  = winshell.SplitSize
	listMin                    = winshell.ListMin
	listMax                    = winshell.ListMax
	listCompactDrag            = winshell.ListCompactDrag
	chatMin                    = winshell.ChatMin
	sideDefault                = winshell.SideDefault
	sideMin                    = winshell.SideMin
	sideMax                    = winshell.SideMax
	narrowContentWidth         = winshell.NarrowContentWidth
	sceneHeight        float32 = 360
	scrimAlpha                 = winshell.ScrimAlpha
	contentWidth               = winshell.ContentWidth
	settingsWidth              = winshell.SettingsWidth
	settingsListWidth          = winshell.SettingsListWidth
	bubbleShare        float32 = .8
	narrowBubbleShare  float32 = .9
	overlayAlpha               = winshell.OverlayAlpha
	lineAlpha          float32 = .08
	disabledAlpha              = winshell.DisabledAlpha
	glassHeader                = winshell.GlassHeader
	glassComposer              = winshell.GlassComposer
)

// 动效时长来自令牌（winshell 里是变量，不能放进常量块）。
var motionFast, motionRoute = winshell.MotionFast, winshell.MotionRoute

// 随外观缩放的令牌；值在 applyLook 里算，这里只声明。
var (
	spaceMicro, spaceTight, spaceSmall, spaceMedium, spacePanel, spaceSection, spaceLarge float32
	navInset, paneInset, controlInset, fieldInset, badgeInset                             float32
	radiusPanel, radiusNavItem, radiusControl, radiusSegment                              float32
	radiusComposer, radiusSend                                                            float32
	retryInk                                                                              float32
	headerHeight, hitSmall, hitRegular, rowHeight, avatarSize                             float32
	conversationAvatar, conversationInset, listInset, listCompact                         float32
	searchHeight, searchInset, compactBadge                                               float32
	headerAvatar                                                                          float32
	switchThumbSize, iconSize, iconSmall, navItemHeight, sendSize, composerMin            float32
	controlWide, valueWidth                                                               float32
	bodySize, bodyLine, labelSize, labelLine, metaSize, metaLine                          float32
	conversationLine, conversationSlot, titleSize, titleLine                              float32
	normalWeight, mediumWeight, strongWeight, boldWeight                                  int
	navWidth, breakpointNarrow, breakpointWide                                            float32
)

func init() { applyLook(chatui.Derive(chatui.DefaultSettings())) }

// applyLook 按外观设置重算上面这组令牌（基准是默认设置下的值）。
func applyLook(t chatui.Tokens) {
	g := winshell.NewGeometry(t)
	spaceMicro, spaceTight, spaceSmall, spaceMedium = g.SpaceMicro, g.SpaceTight, g.SpaceSmall, g.SpaceMedium
	spacePanel, spaceSection, spaceLarge = g.SpacePanel, g.SpaceSection, g.SpaceLarge
	navInset, paneInset, controlInset, fieldInset, badgeInset = g.NavInset, g.PaneInset, g.ControlInset, g.FieldInset, g.BadgeInset
	radiusPanel, radiusNavItem, radiusControl, radiusSegment = g.RadiusPanel, g.RadiusNavItem, g.RadiusControl, g.RadiusSegment
	radiusComposer, radiusSend, retryInk = g.RadiusComposer, g.RadiusSend, g.RetryInk
	headerHeight, hitSmall, hitRegular, rowHeight, avatarSize = g.HeaderHeight, g.HitSmall, g.HitRegular, g.RowHeight, g.AvatarSize
	conversationAvatar, conversationInset, listInset, listCompact = g.ConversationAvatar, g.ConversationInset, g.ListInset, g.ListCompactWidth
	compactBadge = g.CompactBadge
	searchHeight = g.HeaderHeight - 2*g.ListInset // 工作台是老版式：搜索框在 56 高的顶行里，新外壳换成了和标题条同高
	searchInset = (searchHeight - g.MetaLine) / 2
	switchThumbSize, iconSize, iconSmall, navItemHeight, sendSize, composerMin = g.SwitchThumbSize, g.IconSize, g.IconSmall, g.NavItemHeight, g.SendSize, g.ComposerMin
	controlWide, valueWidth = g.ControlWide, g.ValueWidth
	bodySize, bodyLine, labelSize, labelLine, metaSize, metaLine = g.BodySize, g.BodyLine, g.LabelSize, g.LabelLine, g.MetaSize, g.MetaLine
	conversationLine, conversationSlot, titleSize, titleLine = g.ConversationLine, g.ConversationSlot, g.TitleSize, g.TitleLine
	normalWeight, mediumWeight, strongWeight, boldWeight = g.NormalWeight, g.MediumWeight, g.StrongWeight, g.BoldWeight
	navWidth = max(72, g.HitSmall+2*g.PaneInset) // 原型自己的导航带文字，比正式界面的只有图标宽
	breakpointNarrow, breakpointWide = g.BreakpointNarrow+navWidth-g.NavWidth, g.BreakpointWide+navWidth-g.NavWidth
	// 圆角令牌不封顶（规格-设计令牌第 3 步，主人定），封顶在控件自己：原型里用控件圆角的东西最矮的是单行字段（rowHeight），
	// 其次是按钮（hitSmall）。控件圆角不超过最矮那档的一半，最大圆角档下也只是胶囊，不会圆角大过半高（几何检查 TestSurfaceGeometry）
	radiusControl = min(radiusControl, min(hitSmall, rowHeight)/2)
	// 标题栏里的头像：栏高减两边骨架内距，和正式外壳的 BarHeight − 2×BarInset 同一个算法
	headerAvatar = headerHeight - 2*paneInset
}

var controlMotion = winshell.ControlMotion
var routeMotion = winshell.RouteMotion

type palette = winshell.Palette

// makeTheme 从 chatui 的令牌推出工作台的配色结构和 mygo 主题；来源在 internal/winshell，正式程序用同一份。
func makeTheme(t chatui.Tokens, font string) (palette, ui.Theme) {
	p := winshell.NewPalette(t)
	return p, winshell.NewTheme(t, p, font)
}
