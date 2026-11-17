# 聊天主体对照与 API

源码版本沿用README.md：tdesktop `250fc73`，lib_ui `1ff7be7`。尺寸为100%缩放DIP；字体实际行高由系统排版取得。尺寸登记在`internal/cmd/themegen/metrics.tsv`并生成到theme-metrics.md；完整主题包含614个颜色、806个数值、34个字阶和74个动效，快照格式为JSON v9。消息底部以 [画法规格](paint/message-bottom.md) 为准，头像与名字色以 [身份规格](paint/chat-identity.md) 为准，替代图形尺寸以 [图标规格](paint/icon-glyph.md) 为准。

## 组件与状态

| 组件 | 覆盖 | Telegram 来源 |
|---|---|---|
| `chat.Header` | 名称、状态；offline/online/typing/connecting；长名称省略；返回；普通/选中消息标题；可插标题徽记；搜索、通话、更多、取消、自定义按钮；active/disabled/hover/press/键盘 | `info/info.style:916-976`；`history/view/history_view_top_bar_widget.cpp:546-557,687-727,1215-1227` |
| `chat.Bubble`、`chat.Row` | 进站/出站、普通/选中；四种分组角；尾巴与下阴影；组首名字、组末头像，底部可见行跟随视口 | ui/chat/chat.style:14-25,52-54,326-329；history/history_inner_widget.cpp:1064-1119；ui/chat/message_bubble.cpp:188-204 |
| `chat.Body`、`BottomInfo`、`ImageInfo` | 正文末行 S×K 占位；回应最后一行共享时间；24收据槽、20浏览/回复槽+8间距；Prefix/Signature/Views/Replies分项，签名上限144；Failed时钟；方向不同时独立占位；媒体右下底板 | [消息底部规格](paint/message-bottom.md#底部信息) |
| `chat.RetainedBody`、`BodyState`、`BodySelection` | 宿主持有的可见正文排版；字形/对象/命中/链接/选区共用布局；跨段复制和 rune 范围；窗口与虚拟行寿命由宿主决定 | mygo v0.7.4 `ui/text_layout.go`、`textselection.go`；按消息和宽度失效参考 `history/view/history_view_element.cpp:1884-1919,2290-2327`，只借状态寿命和失效方式 |
| `chat.ServiceMessage`、`chat.DateMarker` | 居中提示、普通/选中；日期胶囊；随字号增长；可点击日期 | `ui/chat/chat.style:18,95-96`；`history/view/history_view_service_message.cpp:149-187` |
| `chat.UnreadMarker` | 整行未读条、居中文字、下边线；32 条高 + 8 外留白 | `ui/chat/chat.style:353-355`；`history/view/history_view_element.cpp:769-820` |
| `chat.FloatingDate` | 滚动后浮在顶部、当前日期、当天底部推走、胶囊内悬停停留、1000 ms 延时和 200 ms 淡出；向下跨日停留 3000 ms | `history/history_inner_widget.cpp:142-143,1154-1162,4709-4767`；`ui/chat/chat.style:312-313` |
| `chat.LatestButton`、`chat.LatestVisible` | 零未读也可显示；任意位数计数胶囊；外框 52×62、圆形 42；右 12、下 10；480 距底阈值；未加载到底；hover/press/disabled；150 ms 位置插值、隐藏时不接收点击 | `chat_helpers/chat_helpers.style:1195-1227`；`ui/controls/jump_down_button.cpp:36-56`；`history/view/history_view_corner_buttons.cpp:451-455,553-560` |
| `chat.Reveal` | 显式新消息事件；150 ms 布局空间展开；当前聊天 easeOutCirc、另一列表 easeOutQuint；宿主飞行进度可线性驱动；离屏后不重播；取消、减少动效、零时长 | `history/history_widget.cpp:8899-8958`；`history/view/history_view_list_widget.cpp:3091-3093`；`ui/chat/chat.style:58` |
| `chat.CaptureDraft`、`DraftCapture.Take`、`SendingTarget`、`SendingFlight` | 接受发送时保留实际输入框图层；裁掉编辑器按钮/回复条；清稿后旧快照继续飞行；恢复输入焦点；动态目标位置；减少动效、取消和1000ms等待超时 | ui/effects/message_sending_text_flight.cpp:39-44,129-135,159-160,580-655,735-741,803-830 |

## 分组与边界

`Group.Previous` 使发送侧上角变 6；`Group.Next` 使发送侧下角变 6；其他外角 16。没有后续连接且允许尾巴时，下方发送侧接尾巴。`SuppressTail` 表达媒体、行内键盘、说明消息或评论根等不带尾巴的情况。宿主按自己的身份/时间/服务消息边界提供 Group；库不读取消息数据库或推断渠道能力。

`RowOptions.ReserveAvatar` 使用40 DIP头像步进，头像33 DIP，左沿14 DIP；气泡左沿仍独立取16+40 DIP，来源 history_view_message.cpp:6559-6565。名字只在组首。头像在组末，或带后续连接的最底部可见行显示；下沿取消息底与可见区底减 historyPaddingBottom(8)+msgMargin.bottom(2) 的较小值，再由组顶夹住。虚拟历史通过 `AvatarGroupTop` 传组顶的窗口坐标；非虚拟组可由组容器的 Draw 记录该坐标，画廊 chat/avatars 给出组合。

头像组顶和发送起止矩形均以绑定句柄取得窗口坐标；缓存父层临时移动Painter原点时再转换绘制坐标，不能把局部图层矩形当窗口位置。发送Effect与Draw一样以当前帧回调读取句柄，持久State只保留句柄、位图及时间，不保存Context。

`Message.AvatarInitials` 是宿主准备的短首字，`AvatarLabel` 是完整可访问名；`AvatarColor ui.Color` 非零时使用宿主纯色，零时首字用peer4主题渐变。`Avatar` 非空取已解码位图，否则首字覆盖缺图/加载中/失败，不另设加载状态或做I/O。Row在原来的组尾与视口底圆框中绘制，首字不参与气泡测量。语义名字属于头像预留列RoleImage；列边界与动态绘制圆框的区别见规格。

`Message.SenderNameColor ui.Color` 非零只覆盖本条名字色，Row与Bubble共用；零值沿用方向/选中态主题默认。宿主颜色在选中时保持，如需选中变色由宿主传相应主题角色。`SenderName==""` 或 `Group.Previous` 仍不生成名字行。首字采用独立 `Typography.MessageAvatar()`，可与名字字号分别配置。

目的快照只在飞行期间建立；绘制到期后释放源图、目的图与捕获句柄，再通过Services.Invalidate重建，已构建但稍后才执行的Effect不会重新保留目的图。宿主长期保存消息State也不保留完成的快照。发送后宿主滚动沿用mygo ScrollState.Y的“布局时夹取”合同（ui/scroll.go:10-18,63-66），不以旧MaxY创建ScrollTo目标。

54 DIP 顶栏、32 DIP 未读条和 22 DIP 计数角标是默认字号下的最小高度。系统字号放大后，组件以实际文字度量扩展高度，顶栏仍保留来源中的上下 8 留白；不把固定高度用于裁掉或重叠文字。

`Message.Info.Label` 为完整日期时间，`Prefix` 为宿主按优先级选定的编辑/更新等前缀，`Signature` 独立省略。`Views/Replies` 接收准备好的短计数字符串；发送中（含Failed）隐藏计数，回复上下文/Shortcut或0回复也隐藏回复。`Delivery` 为sending/sent/read/failed；普通入站无单/双勾。空Info不留占位；HideInfo显式隐藏；MediaBottom将时间交媒体入口并将Footer放到壳外。

多行系统提示先以平台排版取得每行宽度，合并不足以容纳内外圆弧的相邻宽差，再对各行带留白矩形的并集描一次轮廓，避免半透明表面叠加变深。等宽窄行沿用前一行的连接缩进，来源为 `history/view/history_view_service_message.cpp:366-380`。外半径为 `(fontHeight+3+4)/2` 的整数部分，内半径为 `max(0,fontHeight−outerRadius)`，缩进为 `2×innerRadius/3` 的整数部分；来源为 `ui/chat/chat_style.cpp:94-115` 与 `history/view/history_view_service_message.cpp:392-428`。轮廓缓存按文字、字体、宽度、方向和留白失效。

`Bubble` 的 leading `content func()` 位于名字后、正文前；自定义尾部文字放入 `BubbleOptions.Body func(ui.Span)` 并传给 `MarkdownOptions.Trailing`。`Footer func(ui.Span)` 取得同一S×K，将其传给 `ReactionOptions.Trailing`。列表先用完整W给胶囊折行，只有最后一行的e+S>W时增加K。Footer上方5 DIP，内胶囊左移3；日期下沿比胶囊下沿低5 DIP，所以数字与时间不是相同文字基线。`MediaOptions/AlbumOptions.BottomInfo` 在媒体底部插入 `chat.ImageInfo`，显隐由媒体选项与悬停决定。调用方按视觉内容顺序设置MediaBottom；照片下有文字时保留普通壳时间。

`RetainedBody(c, state, spans, BodyTextOptions)` 的 `Width` 必须为当前正文实际可用的正 DIP 宽度；`Font`、`Direction` 显式传当前值。内容、字体、宽度、方向或 `Trailing` 改变时重排；只改变宽度复用本征宽度测量。正文标签由 `Label` 提供，语义为 RoleText；链接按半开 rune 范围给 `BodyLink`，独立 RoleLink、Label 和 URL Value，换行产生多个命中框。`LinkTip` 在构建中接收每个命中框，浮层身份必须按框区分；宿主取 `ClickedLink()` 后自行校验打开。

每壳保留一个 `BodySelection`，在内容根上 `Attach(c, root, BodySelectionOptions{CopyData:copyData, PaddingTop:top, PaddingBottom:bottom})`，按视觉顺序构建段落，最后 `Finish()`。上下留白由宿主按壳实际数值传入；媒体所在的内部空白不启动文字拖选。宿主只注册一次菜单：普通正文用 `root.ContextMenu(selection.EditItems)`，有自定义动作时在自己的菜单里调用 `EditItems`。支持跨段拖选、Shift 选区、字素方向键、Home/End/上下移动、全选、双击词与三击行、快捷键及右键复制。`SelectedTexts` 的 Text 不含时间尾槽，ID 属于原正文段，Start/End 为 rune；同段的双向文字碎片直接连接，段与段之间空一行。内部命中和选择使用 TextLayout 的 UTF-16 偏移与 affinity。`DrawObject` 接收 rune 索引和 Painter 坐标，包含尾槽对象，宿主按内容对象表过滤。宿主必须按窗口/消息/段隔离状态，仅为进入虚拟视口的段建立状态，离屏后丢弃引用；组件不建立全历史缓存。

分词复用已有的 `go-text/typesetting/segmenter` Unicode 实现，仅在双击/拖词时运行；不另写 ASCII 分词器。正文保留布局参考 mygo 的 retained text API，选择协议参考其只读文字控件，没有复制 Telegram 实现。

## 使用

```go
// c 在当前窗口构建中有效；messageID 是宿主持久消息 ID。
c.Key(messageID)
chat.Row(c, chat.Message{
    SenderName: "Aoi",
    SenderNameColor: theme.T(c).Colors().Avatar().Peer1NameForeground(),
    AvatarInitials: "A",
    AvatarLabel: "Aoi 的头像",
    AvatarColor: theme.T(c).Colors().Avatar().Peer1UserpicBackground(),
    Text: "Prepared message text",
    Info: chat.Info{Label: "18:42", Delivery: chat.NoReceipt},
}, chat.RowOptions{
    BubbleOptions: chat.BubbleOptions{
        Side: chat.Incoming,
        Group: chat.Group{Next: true},
        Selected: selected,
    },
    ReserveAvatar: true,
}, func() {
    // 插入宿主准备好的引用或媒体组件。
})
```

列表继续使用宿主的 `ui.ListState` / `ui.ScrollState`，按消息 ID 起 key。`FloatingDate` 和 `LatestButton` 放在滚动容器外、同一个有 `.Clip()` 的 stationary viewport 中。宿主传当前顶部消息日期、滚动偏移和当天底部相对视口的坐标；这支持宿主已有的虚拟列表，不引入第二套滚动/历史锚点。

`FloatingDateOptions.DayBottom=0` 表示当前日期还未接近视口顶，不限制其下沿；非零时按当前日期组底部推走。调用方切会话时使用该会话自己的 `DateState`。`LatestVisible` 只计算普通距底阈值和未加载到底条件；回复返回目标、未读是否在下方由宿主 OR 入 Visible。

`RevealState` 按消息 ID 放在宿主或显式状态中；收到新消息事件时 Start。`CaptureDraft` 包住编辑器，字段以持久 `ui.Handle` 绑定；接受发送回调在清稿前 `source := capture.Take(c, &field)`，新消息自己的 `SendingState.Start(source)`。Capture 轮换图层身份来保留旧位图并恢复字段焦点；持久草稿仍归宿主。消息用 `SendingTarget(c, &sending, content)` 包住，页面末尾调用 `SendingFlight`。飞行使用280ms总时长、160ms横向Quint、纵向Bezier(0.25,0.1,0.25,1)、110ms图像混合，目标随滚动/重排更新。1000ms内目标不出现则结束；取消调用 Finish。回调里的计时由下一构建帧启动，不从回调读 c.Now。旧150ms媒体发送路径不冒充当前文字快照路径。逐字重排变形差异见 exceptions.md。


尾巴固定版本只提供 PNG 掩模：`Resources/icons/bubble_tail{,@2x,@3x}.png`，`ui/chat/chat_style.style:216-219` 选方向，`ui/chat/message_bubble.cpp:188-204,277-295` 定位。填色范围6×10 DIP，阴影另在下方2 DIP；此前“10含阴影”的登记已纠正。本库独立二次曲线以最终DIP栅格化。对3倍参考掩模逐行累计亮度比较覆盖边界：最大0.847059 DIP、平均0.376209、RMS0.410071；只验证轮廓边界≤1 DIP，不代表像素颜色一致，也没有复制参考素材。
## 画廊与验收

`gallery/chat.go` 注册原有八页，加 Sending snapshot 和 Group-end viewport avatars。发送页复用可输入、清稿、回复及消息追加的完整聊天；头像页提供可滚动长分组。

`gallery/chat_identity.go` 单独注册 `chat/identity`：缺图/加载中/失败/就绪、资源状态切换、名字颜色与选中开关、无名字的私聊以及可滚动的首字分组。不改其他路画廊页。

自动测试使用 `ui.NewTester`，按主题 token 量布局、从软件渲染图像采样圆角/尾巴/选中色、真实点按、键盘、右键、悬停及推进测试时钟。测试中的坐标采样误差上限 1 DIP。具体运行和逐条红灯结果在交付记录中列出。

已实际启动 Windows 125% DPI 的原生组合页，通过真实滚轮和鼠标点击跳到最新；截图记录最后一条 Message 71、未读分隔、Today 浮动日期，以及鼠标停在胶囊外顶部空白处时日期正常淡出。默认字号下的原生画面已检查；大字号边界由 Tester 的布局断言覆盖。截图和临时启动器在本次任务指定的 scratchpad/chat 中，不进入库。本次未做同版本 Telegram 截图并排及逐像素验收。

集成验收：全库 `go test -count=1 ./...`、mygo v0.7.4 vet 通过；79/79 条实际断言路径逐一反转均变红。旧实现的五项身份、宽度、轮廓及悬停边界已复现。八页原生明暗界面共 16 张截图完成。尾巴路径按最终 DIP 尺寸栅格化并缓存，避免归一化小路径先栅格化再放大造成轮廓丢失。时钟测试在指定时刻显式提交当前帧；日期位移取绘制矩形，图标取完整区域的实色笔画，保留原曲线、时长与颜色要求。

a2-impl验收及性能记录见 [消息底部画法规格](paint/message-bottom.md#a2-impl-验收实测)。该轮共31条新增断言语句逐一改坏、4项实现故障验证，覆盖同行/1 DIP边界、各消息状态高度、基线偏移、头像与图片显隐；全库检查和两页明暗原生提交通过。原生测试DPI为96，100/125/150%几何由Tester覆盖；官方截图叠图仍缺输入。

聊天身份扩展验收：CGO=0全库、Go/mygo vet、CGO=1 chat/theme/gallery及恢复检查退出0；19条断言扰动与3项实现故障均按预期退出1；10张软件状态/窄窗图、11阶段原生交互（DPI120）完成。原生应用工作P95=0.6ms；启动present长帧、automation代理交互超100ms与软件读回超10ms样本均保留。详细条件、分遍数字及未验证边界见 [身份验收实测](paint/chat-identity.md#验收实测2026-11-04)。

