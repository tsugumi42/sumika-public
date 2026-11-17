# 令牌与主题

使用时机：颜色、明暗、字阶、圆角、密度、主题预览。出处代号和固定版本见 sources.md。

## 一条消费链

语义 palette → ui.Theme → 有样式控件 → 页面组合。palette 补产品角色，Theme 覆盖内置控件；页面不能只换自画背景，却让按钮/输入框留在另一套颜色。L 用 Content/Sidebar/Inspector、BotBubble/Code/OutputCard、Composer、Selection/SelectionInactive、Scrim/Sheet，applyTheme 再映射基础控件。出处：L desktop/theme.go:13–37、263–286；GPL-3.0，仅结构；[源码]。

优先扩展既有系统，不新建第二套 theme manager。以下是 M ui/theme.go:7–66 的 MIT API 独立组合；palette 由宿主按明暗和用户选择生成。只在 palette/密度修订或系统 Dark、Accent、TextScale、HighContrast 改变时编译到窗口持有的 ui.Theme；根构建每次 c.SetTheme(&a.theme)，避免每帧复制并分配整份主题。S native/theme.go:35–72 为应保留的按变化缓存现状；[源码→项目选择]。

```go
type Palette struct {
    Canvas, Panel, Hover, Pressed, Ink, Muted ui.Color
    Primary, PrimaryHover, PrimaryPressed, OnPrimary ui.Color
}
func PaletteTheme(t ui.Theme, p Palette) ui.Theme {
    t.Background, t.Surface = p.Canvas, p.Panel
    t.SurfaceHover, t.SurfacePressed = p.Hover, p.Pressed
    t.Text, t.TextMuted = p.Ink, p.Muted
    t.Accent, t.AccentText = p.Primary, p.OnPrimary
    t.AccentHover, t.AccentPressed = p.PrimaryHover, p.PrimaryPressed
    return t
}
```

主按钮与面板各有 hover/pressed 角色。避免字段缺失时用白字或 Alpha 猜色；AccentText 是强调底上的字，不是普通背景上的强调字。依据：M ui/theme.go:11–36；[源码→推断]。

## 角色和刻度

| 内容 | 项目写法 | 来源与可信度 |
| --- | --- | --- |
| 普通/悬停/按下 | Surface、SurfaceHover、SurfacePressed | M ui/theme.go:11–15；[源码] |
| 强调底和其文字 | Accent/Hover/Pressed + AccentText | M ui/theme.go:22–27；[源码] |
| 次级字、边线、危险/成功/警告 | TextMuted、Border、Danger/Success/Warning | M ui/theme.go:16–32；[源码] |
| 产品语义 | 进出消息、工具/审批卡、代码、查看器、遮罩、非活动选中 | L desktop/theme.go:13–37；GPL，仅角色；[源码→项目建议] |
| 间距 | Spacing 默认 4；Space(n) 按单位缩放；3 紧、5 松 | M ui/theme.go:49–66；[源码] |
| 字阶 | Rem(n) 按 Theme.FontSize；不乘设备像素比 | M ui/theme.go:89–103；[源码→推断] |
| 局部预览 | ui.Themed(c, theme).Children(...) | M ui/api_gen.go:2375–2390；[源码] |

本项目建议常用间距 4/8/12/16/20/24/32 DIP，使用现有密度单位表达；个别头像、图标和精确几何允许有出处的常量。这是项目写法，不是 mygo 官方完整设计系统。L 的尺寸散在各表面，不能声称它已有统一 token 表。依据：M ui/theme.go:49–66；L desktop/chat.go:21–31、mainwindow.go:18–25；[推断]。

保留系统 Dark、Accent、HighContrast、TextScale 的输入；文本缩放与 DIP→物理像素是两件事。若复制 Theme 再设置 FontSize，处理 TextScale 一次；不要每帧在上轮已放大的值上再乘。依据：L desktop/theme.go:179–199、284–295；M ui/headless.go:377–388、412–418；[源码→推断]。

## 覆盖与验收

把普通、hover、pressed、focus、selected、disabled、loading、error 各状态的前景/背景列成表；新增角色必须在浅深两套 palette 中成对出现。验收看真实控件文字、焦点环和禁用状态；主题值对了不代表子控件用到了。依据：M ui/theme.go:7–57、docs/ui/inspector.md:59–71；[源码→项目验收]。

S native/viewer.go:281–301 的局部颜色公式是应统一的反例；S native/images.go:35–41、65–90 的图片预算和 DPI 档位是要保留的现状，不是颜色主题重构的删改对象。[源码→推断]。v0.7.4 没有 Theme.FocusRingWidth/Offset、InactiveAccent、TooltipDelay；不要按 A 路 main 资料补不存在的字段。依据：M ui/theme.go:7–57；[源码]。
