# 窗口布局与原生画廊

基线版本见 [README.md](README.md)。尺寸声明见 [theme-metrics.md](theme-metrics.md)，本页记录 C++ 中实际采用的条件。所有宽度指 DIP；`B` 为容器宽度扣除文件夹竖栏后的主体宽度。布局按自身容器测量，不按整个窗口宽度判断。

| 状态 / 行为 | Telegram 来源 | 本库行为与 token |
|---|---|---|
| 窗口内容最小 380×480，默认 800×600，大窗口 1024×768 | window/window.style:13-18 | `Metrics.Layout`；原生画廊按内容尺寸创建，并将平台报告的外框差值加到最小跟踪尺寸 |
| 文件夹竖栏 72 | window/window.style:229 | `Metrics.Sidebar.Width`；`Options.Folders` 开启 |
| 总宽至少 380+72 才显示竖栏 | window/window_session_controller.cpp:3445-3446 | 452 为默认边界；更窄时仅列表顶部提供 `FolderTabs` 槽 |
| 横向文件夹行 33 | dialogs/dialogs.style:764-785 | `layout.folderTabsHeight`；内容由导航组件提供 |
| 单栏 / 双栏 | window/window_session_controller.cpp:2772-2799 | `B<260+380=640` 单栏，640 起双栏；有竖栏时整宽断点 712 |
| 单栏列表、聊天、资料 | mainwidget.cpp:2031-2067,2140-2143 | `State.Active` 决定列表或聊天；`ThirdOpen` 决定资料；Escape 先关闭资料，再回列表 |
| 列表初始宽度 | core/core_settings.h:1073；window/window_session_controller.cpp:2816-2826 | 四舍五入 `B×5/14`，至少 260；`layout.listRatio` 为无单位比例 |
| 列表最大声明 540 | window/window.style:35；window/window_session_controller.cpp:2825 | 附件要求本库采用 540 硬上限；上游当前计算处的夹取被注释。差异见 exceptions.md |
| 拖到 130 以下收头像 | mainwidget.cpp:2907-2925 | `ListMinWidth/2`；保存 `Compact`，不因窗口缩窄自动收头像 |
| 头像列表 66 | window/window_session_controller.cpp:2754-2757；mainwidget.cpp:3230-3237 | `2×Conversation.PaddingLeft+AvatarSize`；`ForceWideList` 在搜索期间临时展开，不改偏好 |
| 收头像仍受单栏断点限制 | mainwidget.cpp:3230-3237 | 先判断 640，再决定是否用 66；不将头像宽当作双栏的最小宽 |
| 聊天最小 380 | window/window.style:36 | `layout.chatMinWidth`；并排、拖动及过渡期间都保留 |
| 宽聊天阈值 880 | window/window.style:40 | `Info.WideChat`；内容的居中、最大宽由聊天组件处理 |
| 第三栏 292～392 | window/window.style:37-38；window/window_session_controller.cpp:2829-2833 | `ThirdWidth=0` 经夹取后为 292；宿主保存宽度偏好 |
| 三栏至少 B=932 | window/window_session_controller.cpp:2760-2763,2781-2811 | 第三栏开启且 `B≥260+380+292`；有竖栏时整宽至少 1004 |
| 三栏不足空间时按比例压缩侧栏 | window/window_session_controller.cpp:2836-2857 | 从列表、第三栏比例分摊，分别保留下限；聊天保留 380 |
| 第三栏扩窗及退化 | window/window_session_controller.cpp:2876-2967 | 输入回调调用 `State.SetThird(c, open, host)`：优先请求偏好第三栏宽度，无移动空间不足时请求最小宽，主体已能放三栏则不移动；单栏额外补齐三栏缺口。保存实际扩宽量，关闭时收回；固定窗口或宿主拒绝时在当前主槽显示资料 |
| 分隔线命中 6、绘制 1 | ui/chat/chat.style:203；lib_ui/ui/basic.style:62；mainwidget.cpp:2863-2881 | `ResizeHitWidth` / `Control.BorderWidth`；命中区位于边界右侧，不从栏宽额外扣 6 |
| 列表收窄 120 ms、easeOutCirc | dialogs/dialogs.style:217；lib_ui/ui/basic.style:135；mainwidget.cpp:2968-2973 | `Motion.ListCollapse`；拖动原始位置独立于吸附宽度累计 |
| 页面滑动 240 ms、easeOutCirc | lib_ui/ui/basic.style:96-98；window/window_slide_animation.cpp:114-151,196-211 | `Motion.ThirdPanel` 控制主槽列表→聊天→资料的缓存图切换，返回时反向；底图移100 DIP，叠加 slideFadeOutBg 遮罩。第三栏首次并排开合直接重排，来源 mainwidget.cpp:2084-2118 |
| 隐藏系统标题栏 | mygo v0.7.4 ui/context.go:413-427；window.go:98-125 | 顶部仅预留与 `c.TitleBar().Height` 相交的高度；`Title` 槽按 Left/Right 避让；空白带 `DragWindow` |

API 示例（所有回调在当前构建中使用同一个 Context）：

```go
var shell layout.State // 宿主持有；Active 的零值显示列表

layout.Window(c, &shell, layout.Options{Folders: true}, func() {
    layout.Pane(c, &shell, layout.Title, func() { ui.Text(c, "Example") })
    layout.Pane(c, &shell, layout.Folders, folders)
    layout.Pane(c, &shell, layout.FolderTabs, horizontalFolders)
    layout.Pane(c, &shell, layout.Conversations, conversations)
    layout.Pane(c, &shell, layout.Chat, chat)
    layout.Pane(c, &shell, layout.Third, details)
})
```

`State` 只能对应一个窗口实例。可持久化的字段是 `ListRatio`、`Compact`、`ThirdWidth`、`ThirdOpen`、`Active`；几何由库维护。`Info()` 提供当前栏宽、目标模式、头像状态、竖栏状态、资料是否占主槽。隐藏槽仍构建并保留身份；昂贵列表遵循 mygo 的隐藏列表行为。宿主应提供明确返回 / 关闭按钮，不能只依靠 Escape。

隐藏或关闭持有焦点的区域时，焦点交给窗口壳，使 Escape 继续有效；仍可见的编辑器在缩放、断点切换和旁边打开第三栏时保留焦点。此交接使用 `ui.Handle`，不保存构建元素。

`Options.Host` 供 Escape 收回第三栏扩窗；显式按钮也必须调用 `SetThird`。`WindowHost.Fixed` 表达最大化/全屏，`CanExtendWithoutMove` 查询工作区空间，`ExtendWidth` 返回实际有符号宽度差；不允许扩窗可省略 Host。原生画廊接入真实工作区与窗口 API。直接改 `ThirdOpen` 仍表达外部恢复偏好，不请求窗口副作用。

页面截图由 mygo 的 Cache/Effect 层保留，离开页隐藏后不再重画，不复制 CPU 位图。改变容器尺寸结束当前滑动；ReduceMotion/Instant 直接显示目标。Pane 的身份与隐藏状态保持原有契约。阴影使用程序化渐变，来源和素材限制见 exceptions.md。

`Divider(c, value, onResize, options)` 交付请求宽度。指针手势持续累加原始位移，因此调用方做 260 吸附后仍能继续拖到 130 以下。方向键步长取 `Conversation.PaddingLeft`（默认 10）；Home / End 到边界；`OnCommit` 用于持久化。键盘操作、强调色反馈和 Home / End 是本库的可访问性选择。

宿主创建窗口时使用 `WindowOptions.UseContentSize=true`。mygo v0.7.4 的 `SetMinimumSize` 设置外框尺寸（window.go:1037；internal/windows/window.go:477-489），所以创建后用 `Bounds` 与 `ContentSize` 的差值补上原生外框，再设置内容最小值；画廊不写死 Windows 边框厚度。隐藏标题栏顶部仍有系统缩放边缘，`DragWindow` 的拖动带位于它下面，原生验收从标题带中间拖动。

原生画廊：`go run ./cmd/gallery`。左侧由 `gallery.Pages()` 列出登记页；新页必须提供稳定 `Page.ID`，各路在自己的文件中 `Register`。窄窗用“页面”菜单切页。明暗切换保留页面状态。

- `-page layout/window`：交互宽度预览，加单栏列表、单栏聊天、单栏资料、文件夹栏、双栏、资料占主区域、头像列表、三栏并排状态。
- `-page layout/divider`：实际拖动、悬停、焦点、键盘及提交次数。
- `-page layout/third`：宿主扩窗、关闭收回与窄窗资料页面滑动。
- `-page layout/chat -focus`：完整聊天组合使用真实的 dialogs 会话列表/搜索/文件夹栏、chat 顶栏/消息气泡、content 引用/Markdown/图片/回应、compose 编辑器/选择面板、overlay 消息菜单/查看器/toast。宿主示例保存稳定 ID、筛选快照、草稿、回复及选中状态，提交后追加消息并清空草稿；按mygo的ScrollState合同将Y设为MaxFloat32，由新内容布局夹取到末尾，避免ScrollTo按旧MaxY夹取；所有数据与图像均编造。
- `-dark`、`-width 1004 -height 768`：启动时主题与尺寸。
- `-reference path/to/capture.png`：入口读取并解码宿主提供的截图，画廊并排展示；截图不进入库、不随仓库分发。

验收以 `ui.NewTester` 的实际布局、点击、拖动、键盘与时钟推进为准；原生系统按钮和窗口拖动需另验真实窗口。验证记录在交付提交正文与本次结果中。

2026-11-03 验收记录：在共享重任务锁和低优先级下通过 `CGO_ENABLED=0 go test -count=1 ./...`、mygo vet 和原生画廊构建。136 项断言逐一扰动全部变红；10 项独立实现变异全部被测试捕获，覆盖单栏/文件夹/收头像边界、原始拖动累加、分隔线命中宽度、第三栏方向、标题栏交叠、可见编辑器焦点、第三栏身份及亚毫秒时长。四个登记页均完成 Tester 渲染与交互烟测。

Windows 原生验收（125% 缩放）：四页及暗色聊天已截图；标题栏报告高 32、右侧按钮预留 138 DIP，聊天从标题栏下方开始。真实鼠标从标题带中间拖动，窗口移动 48×32 DIP；按钮打开第三栏到 292 DIP，再关闭；内容区 380×480 时聊天宽 380、模式为单栏。原生最小尺寸、三栏与完整聊天截图已检查。

集成验收补充：原生窗口先隐藏创建，配置外框修正后的最小跟踪尺寸，再在首帧就绪时显示。直接显示会让外部观察者在 `NewWindow` 返回前读到尚未修正的最小尺寸；验收探针不延长等待或放宽 1 DIP 门槛。
