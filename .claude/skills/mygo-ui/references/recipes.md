# 配方

使用时机：会话列表、消息气泡、工具卡、审批卡、设置行、右栏/抽屉、全局待办。代号/固定版本/许可见 sources.md。下列 Go 块可放进同一个 package，统一 import fmt、strings、github.com/egoist/mygo/ui；宿主提供 ui.View。代码是 M 的 MIT API 独立组合，GPL 来源只影响文字描述的结构和取值，没有复制实现。

前三个编号中的「会话列表」「气泡+浮动聊天」「审批卡」为本次实跑对象；记录在 testing.md。其他局部配方按固定版本核对，不能因此说已经过真窗口验收。

## 1. 会话列表：ID 选择，索引只在边界

输入为 ID/标题/预览/未读；SetItems 在数据变化时调用，不在每帧复制集合。SelectedID 是业务身份，index 只是 List 的受控选择。无记录显示空态。出处：M ui/list.go:42–97、ui/list_builder.go:20–88、docs/ui/list.md:20–35；行结构参考 L desktop/sidebar.go:437–561（GPL，仅结构）；[源码→独立组合]。

```go
type Conversation struct {
    ID, Title, Preview string
    Unread int
}
type Conversations struct {
    Items []Conversation
    SelectedID string
    list ui.ListState
    index int
}
func (a *Conversations) SetItems(items []Conversation) {
    a.Items, a.index = items, -1
    found := false
    for i := range items {
        if items[i].ID == a.SelectedID { a.index, found = i, true; break }
    }
    if !found { a.SelectedID = "" }
}
func (a *Conversations) View(c *ui.Context) ui.Element {
    a.list.Selected = &a.index
    a.list.Label = func(i int) string { return a.Items[i].Title }
    root := ui.List(c.Key("conversations"), &a.list, len(a.Items)).
        Grow(1).MinHeight(0).ItemKey(func(i int) any { return a.Items[i].ID })
    root.Rows(func(r ui.ListRow) {
        item, t := a.Items[r.Index], r.Context.Theme()
        ui.Row(r.Context).Padding(8, 12).Gap(8).Children(func() {
            ink, muted := t.Text, t.TextMuted
            if r.Selected() && r.ListFocused() { ink, muted = t.AccentText, t.AccentText }
            ui.Column(r.Context).Grow(1).MinWidth(0).Children(func() {
                ui.Text(r.Context, item.Title).SingleLine().TextColor(ink)
                ui.Text(r.Context, item.Preview).SingleLine().TextColor(muted)
            })
            if item.Unread > 0 { ui.Text(r.Context, fmt.Sprint(item.Unread)).Shrink(0) }
        })
    })
    if root.Changed() && a.index >= 0 && a.index < len(a.Items) {
        a.SelectedID = a.Items[a.index].ID
    }
    if len(a.Items) == 0 { root.Children(func() { ui.Text(c, "还没有会话").Padding(12) }) }
    return root
}
```

验收：点 B，头插/排序/过滤后仍选 B；B 被删时清选择；空→有→空；右键目标另取被点行 ID。大量集合不要每帧 SetItems；UI 虚拟化不豁免数据侧全表处理。依据：M docs/ui/list.md:54–82、124–138；[源码→验收]。

## 2. 消息气泡与浮动聊天

MessageBubble 只画给定记录，Chat 持有记录、草稿、ListState、composer 高度和已提交几何。消息模型 ID 不用 slice 下标。字体/颜色由 Theme，580/22/10/14 为下列配方的参考参数；产品应归入布局令牌。出处：M docs/ui/list.md:54–82、176–199，docs/ui/text-input.md:74–85；L desktop/chat.go:21–31、175–212、382–385（GPL，仅结构/取值）；[源码→独立组合]。

```go
type ChatMessage struct {
    ID, Text string
    Mine bool
}
func MessageBubble(c *ui.Context, m ChatMessage, limit float32) ui.Element {
    t := c.Theme()
    row := ui.Row(c).Padding(4, 22)
    if m.Mine { row.Justify(ui.End) }
    return row.Children(func() {
        bg, ink := t.Surface, t.Text
        if m.Mine { bg, ink = t.Accent, t.AccentText }
        ui.Text(c, m.Text).MaxWidth(limit).MinWidth(0).Padding(10, 14).
            Radius(14).Background(bg).TextColor(ink).Selectable()
    })
}
type Chat struct {
    Messages []ChatMessage
    Draft string
    Next int
    List ui.ListState
    area, composer ui.Handle
    composerHeight float32
    layoutDraft string
    layoutWidth, viewportWidth, viewportHeight float32
}
func (a *Chat) send(text string) {
    text = strings.TrimSpace(text)
    if text == "" { return }
    a.Next++
    a.Messages = append(a.Messages, ChatMessage{ID: fmt.Sprintf("sent:%d", a.Next), Text: text, Mine: true})
    a.Draft = ""
}
func (a *Chat) View(c *ui.Context) ui.Element {
    a.List.Key = func(i int) any { return a.Messages[i].ID }
    a.List.FollowEnd = true
    height := a.composerHeight
    if height <= 0 { height = 66 }
    root := ui.Box(c.Key("chat")).Fill().Bind(&a.area)
    width := a.area.Bounds(c).W
    if width <= 0 { width, _ = c.Size() }
    limit := max(float32(1), min(float32(580), width-44))
    root.Children(func() {
        ui.List(c.Key("messages"), &a.List, len(a.Messages), func(i int) {
            MessageBubble(c, a.Messages[i], limit)
        }).Fill().Padding(8, 0, height+14, 0).
            ScrollbarInsets(0, 0, height, 0).Justify(ui.End)
        ui.Row(c.Key("composer-frame")).Absolute().Left(0).Right(0).Bottom(0).
            Padding(8, 20, 14, 20).Gap(8).AlignItems(ui.End).
            Background(c.Theme().Background).Bind(&a.composer).Children(func() {
                ui.TextArea(c.Key("draft"), &a.Draft).Grow(1).MinWidth(0).
                    Lines(1, 8).MinHeight(0).Label("消息").OnTextKey(func(k ui.TextKey) bool {
                        if k.Key == ui.KeyEnter && k.Mods == 0 {
                            a.send(k.Text())
                            k.SetText("")
                            return true
                        }
                        return false
                    })
                ui.PrimaryButton(c.Key("send"), "发送").Shrink(0).
                    Disabled(strings.TrimSpace(a.Draft) == "").
                    OnClick(func() { a.send(a.Draft) })
            })
    })
    viewportWidth, viewportHeight := c.Size()
    if a.layoutDraft != a.Draft || a.layoutWidth != width ||
        a.viewportWidth != viewportWidth || a.viewportHeight != viewportHeight {
        a.layoutDraft, a.layoutWidth = a.Draft, width
        a.viewportWidth, a.viewportHeight = viewportWidth, viewportHeight
        c.AnimationFrame()
    }
    if h := a.composer.Bounds(c).H; h > 0 && h != a.composerHeight {
        a.composerHeight = h
        c.AnimationFrame()
    } else if h <= 0 { c.AnimationFrame() }
    return root
}
```

这是纯本地消息动作，尚未连接网络；接服务时由领域模型分配稳定 ID、维护发送失败/重试和流式状态，不将按钮点击当服务成功。send 的 sent: 命名空间只用于这份临时样例，初始数据不得用同一 ID；生产改为业务 ID。依据：M docs/ui/views.md:168–184；[源码→配方边界]。

v0.7.4 的 TextArea 默认还有 t.Space(20) 的最小高度，Lines(1,8) 不会清掉它；紧凑 composer 显式 MinHeight(0)，或以 TextAreaBase 统一画样式。M ui/textinput.go:15–22、216–224；[源码/本次渲染核对]。

Markdown 将完成消息交 markdown.View、未完成消息交 markdown.Stream，持久 Options 属于会话；不要在 worker 直接修改被 UI 读取的文档。媒体宽高先留占位。出处：M examples/markdown-chat/main.go:45–95；S native/images.go:35–41（保留预算）；[源码→扩展路径]。

验收：上翻追加不回尾、回尾继续跟随、前插旧页锚点、长中文换行、八行草稿发送后无额外 Frame 自动收高，IME Enter不发，Shift+Enter换行；两会话分别持有 Chat。依据：M docs/ui/list.md:68–73、docs/ui/text-input.md:139–145；L desktop/composer_test.go:32–76（GPL，仅验收机制）；[源码→验收]。

## 3. 审批卡：版本与请求状态

展示层只提交 ID + 看见的版本 + 决定；领域层确认版本与权限，成功回填才显示「已允许」。长命令可展开/复制全文，不能只凭截断摘要批准。L 将即时 PermissionRequest 与持久 review 分开，并处理编辑中远端改版、busy 和 closed。出处：L desktop/cards.go:117–209、sheet_review.go:175–223；GPL，仅结构；M docs/ui/views.md:74–97；[源码→独立组合]。

```go
type Approval struct {
    ID, Command, Reason, Status, Error string
    Revision int
    Submit func(id string, revision int, allow bool)
}
func (a *Approval) choose(allow bool) {
    if a.Submit == nil || (a.Status != "待审批" && a.Status != "提交失败") { return }
    id, revision := a.ID, a.Revision
    a.Status, a.Error = "提交中", ""
    a.Submit(id, revision, allow)
}
func (a *Approval) View(c *ui.Context) ui.Element {
    t := c.Theme()
    return ui.Column(c.Key(a.ID)).Padding(t.Space(4)).Gap(t.Space(2)).
        Radius(t.Radius).Background(t.Surface).Children(func() {
            ui.Text(c, "执行审批").Bold()
            ui.Text(c, a.Command).Selectable()
            if a.Reason != "" { ui.Text(c, a.Reason).TextColor(t.TextMuted) }
            ui.Text(c, a.Status)
            if a.Error != "" { ui.Text(c, a.Error).TextColor(t.Danger) }
            if a.Status == "待审批" || a.Status == "提交失败" {
                ui.Row(c).Gap(t.Space(2)).Children(func() {
                    ui.Button(c.Key("deny"), "拒绝").Disabled(a.Submit == nil).
                        OnClick(func() { a.choose(false) })
                    ui.PrimaryButton(c.Key("allow"), "允许一次").Disabled(a.Submit == nil).
                        OnClick(func() { a.choose(true) })
                })
            }
        })
}
```

宿主应经 UI 发布结果：同 ID/请求版本才更新当前卡，失败→提交失败并保留错误，服务确认→已允许/已拒绝，过期→已过期；关闭后的回调可更新领域投影，不能复活已关浮层。编辑后审批必须先保存新内容再批准返回的新版本。以上为领域契约说明，不声称此 UI helper 实现了持久授权或执行。依据：L desktop/sheet_review.go:175–223、model/reviews.go:271–350；GPL，仅机制；[源码→接入要求]。

验收：允许/拒绝只提交一次；busy 不接受第二次；失败可以重试；改版拒旧；关闭后迟到结果不重开；无 Submit 不可操作。数据均编造，不执行真实工具。[项目验收]，依据同上。

## 4. 工具卡：完整过程与结果

用同一个卡框显示 tool 名、简短参数、running/待审批/done/error、耗时和可展开输出；审批卡只在需要人决策时出现。工具完成不等于整轮回复完成；保留用户手动展开和正在选择文本的阅读状态。L 按消息体类型分发卡，不把所有工具日志画成普通气泡。L desktop/chat.go:237–296、cards.go:378–480；GPL，仅结构；[源码→配方]。

MIT API 组合；open、status、output 为宿主持久状态，输出非常长时改为有界 TextAreaBuffer/独立查看，不在聊天里构建所有文本行。

```go
func ToolCard(c *ui.Context, title, status, output string, open *bool) ui.Element {
    t := c.Theme()
    return ui.Column(c).Padding(t.Space(3)).Gap(t.Space(2)).
        Background(t.Surface).Radius(t.Radius).Children(func() {
            ui.Text(c, title).Bold()
            ui.Text(c, status).TextColor(t.TextMuted)
            ui.Button(c, "展开或收起输出").OnClick(func() { *open = !*open })
            if *open { ui.Text(c, output).Selectable() }
        })
}
```

出处：M docs/ui/views.md:74–97、docs/ui/layout.md:16–25；MIT 独立组合；[源码]。验收失败输出可读、取消后不继续 busy、状态变更不丢手动展开、同 ID 离屏返回不重放完成动作。[推断→项目验收]，依据同上。

## 5. 设置行：可缩文字与不缩操作

K settings_view.go:443–457 的 MIT 布局模式改写，不抄全局 groupRows。label 不是输入框的可访问关联；表单需用 Field，纯布局行用于描述+独立有 Label 的控件。

```go
func SettingRow(c *ui.Context, label, detail string, control func()) ui.Element {
    t := c.Theme()
    return ui.Row(c).Padding(t.Space(3), 0).Gap(t.Space(4)).Children(func() {
        ui.Column(c).Grow(1).MinWidth(0).Gap(t.Space(.75)).Children(func() {
            ui.Text(c, label)
            if detail != "" { ui.Text(c, detail).TextColor(t.TextMuted) }
        })
        ui.Row(c).Shrink(0).Children(control)
    })
}
```

整个布尔设置行需要可点时，用单个 SwitchBase 或既有 ToggleRow 统一交互，不让父行和子开关各翻一次。M docs/ui/custom-widgets.md:9–23；[文档→配方]。验收窄窗长中文、TextScale、键盘切换一次只改一次；常驻说明最多一行，细节折叠。S docs/设计语言.md:459–466；[项目约定]。

## 6. 右栏 / 抽屉

宽窗右栏参与 Row 的布局，固定宽并 Shrink(0)，正文 MinWidth(0)；窄窗同一数据/动作放抽屉，不另造详情模型。L 的 inspector 是常驻栏，sheet 是居中栈，不能称它已有滑入抽屉。L desktop/mainwindow.go:643–741、overlay.go:74–98；GPL，仅结构；[源码]。

M docs/ui/overlays.md:88–105 的 MIT 抽屉模式改写。top 是按下本帧时的顶层所有者；多层宿主只给最上层传 true，禁止上层刚关后把同一事件再交下层。

```go
func Drawer(c *ui.Context, open *bool, top bool, body func()) {
    if !*open { return }
    width, _ := c.Size()
    ui.Overlay(c, func() {
        back := ui.Row(c.Key("drawer-back")).Absolute().Left(0).Top(0).
            Right(0).Bottom(0).Justify(ui.End).Modal()
        back.Children(func() {
            panel := ui.Column(c.Key("drawer-panel")).Width(min(float32(320), width)).
                FillHeight().Padding(c.Theme().Space(4)).Background(c.Theme().Background).
                Role(ui.RoleDialog).Label("会话详情").Children(body)
            if top && (panel.PressedOutside() || back.OverlayShortcut(0, ui.KeyEscape)) { *open = false }
        })
    })
}
```

需要遮罩色和动画时从 palette 提供，并在长期存在父层上做 Transition；退场留最后内容，不能立刻清业务数据。M docs/ui/transitions.md:113–123；S LESSONS.md:394–396；[源码/项目经验]。验收看 interaction.md 的层矩阵，包括图片盖在抽屉上、Esc只关顶层、焦点恢复和背后快捷键停止。

## 7. 全局待办：来源跳转与完成分开

待办模型至少 ID、Revision、Title、NextAction、来源 chatID/messageID/approvalID、状态；窗口持 open/selection，数据在领域层。全局按钮打开聚合列表，点击来源加载并定位消息；「完成待办」仅提交待办 ID/expected revision，不顺带批准关联审批。L Attention 保留来源、版本，原生 flyout 开前用 Tester 测高；浮窗宽380、内容 Scroll 上限480只是其参考参数。出处：L desktop/attention.go:174–222、378–439、model/attention.go:56–73；GPL，仅机制/取值；[源码→配方]。

MIT API 组合，业务字段和操作为参数；没有完成/审批按钮的隐式联动。

```go
func TodoRow(c *ui.Context, title, next string, reveal, resolve func()) ui.Element {
    return ui.Row(c).Gap(c.Theme().Space(2)).Children(func() {
        ui.Column(c).Grow(1).MinWidth(0).Children(func() {
            ui.Text(c, title)
            ui.Text(c, next).TextColor(c.Theme().TextMuted)
        })
        ui.Button(c, "查看来源").Disabled(reveal == nil).OnClick(func() { if reveal != nil { reveal() } })
        ui.Button(c, "标记完成").Disabled(resolve == nil).OnClick(func() { if resolve != nil { resolve() } })
    })
}
```

出处：M docs/ui/views.md:74–97、docs/ui/layout.md:16–42；MIT 独立组合；[源码]。集合可增长时外层用 List。验收来源尚未加载、消息被过滤/删除、待办改版、动作失败和重复点击；计数表示待处理项，不把查看当完成。审批独立走第3个配方。[推断→项目验收]，依据 L 上述来源。
