实现状态：`theme`、`layout`、`dialogs`、`chat`、`content`、`overlay`、`compose`、`settings`、`control` 与 `sound` 已落地，原生入口为 `cmd/gallery`。设置 API 与宿主示例见 [settings.md](settings.md)，通用控件、审批/摘要/停止生成与提示音接口见 [controls.md](controls.md)，各组件画法规格见 `reference/paint/`；其余包的 API、源码调用条件与差异以 [reference/README.md](reference/README.md) 列出的对照页为准。下文保留首批组件的架构设计背景。

源码核对区分声明数值与实际调用行为。输入区已核实 history_widget.cpp:9234-9253：72 是继承样式的初始上限，实际聊天输入设置为 min(224, available)，还会扣除附加条、上下留白和保留的历史高度。完整条件与实现见 [reference/compose.md](reference/compose.md)。

迁移方案会保留 Sumika 已有的滚动、媒体缓存和身份处理逻辑，先替换展示组件；AI 特有面板和设置页排在聊天区之后。
# `MewTypeUI`：面向聊天桌面的 Go 组件库骨架设计

建议新建独立仓库，直接基于 mygo，按 Telegram 桌面版组织聊天组件和主题。学习 CrychicUI 的组件 API、画廊和行为测试，学习 mygo-m3e 的主题分层与独立仓库做法；不整体 fork，也不把两个库包一层当作新库。

本文是设计提案。**源码事实、建议和未核实项分别标明；全部新 API 都是草图。**本次只读源码，执行了定位依赖的 `go list`，没有修改文件、运行测试或访问 Sumika 的 `data/`。

## 1. 这个库是什么，和现有库是什么关系

`MewTypeUI` 提供会话列表、聊天顶栏、消息气泡、引用、回应、媒体、输入区和菜单，以及支撑它们的通用控件。Sumika 负责把自己的数据转换成展示模型、处理操作和维护后台连接。

| 项目 | 职责 | `MewTypeUI` 借鉴什么 |
|---|---|---|
| mygo | 窗口、布局、绘制、输入、焦点、无障碍、虚拟列表 | 直接作为底座，优先使用 `ButtonBase`、`TextInputBase` 等基础控件 |
| CrychicUI | 沿用 mygo 外观的大型通用组件库 | 函数式构造、末尾 Options、返回 `ui.Element`、画廊登记、Tester 行为测试 |
| mygo-m3e | Material 3 Expressive 组件库 | 自有主题令牌映射到 `ui.Theme`、主题缓存、字阶/尺寸/动效分开、独立画廊 |
| `MewTypeUI` | Telegram 风格的聊天组件与可编辑主题 | 自己实现目标外观和组合方式，保留清楚的宿主边界 |
| Sumika | 会话、角色、渠道、消息、媒体获取、权限和 AI 行为 | 组合组件，决定显示什么、操作能否执行 |

**不建议整体 fork CrychicUI，原因在目标差异：**

- 它的 README 列出 38 类、1500 个组件；Sumika 的聊天库用不到其中大部分领域组件。
- 它主要从 `c.Theme()` 取色，气泡直接采用 `Accent` / `Surface`，不足以表达 Telegram 的入站、出站、选中、引用和服务消息角色。
- 它要求每个组件有动效、每个交互组件有音效。Telegram 目标应逐项对照，不能直接继承这条规则。
- `ConversationList` 当前逐行构建、用下标作为 key；直接 fork 后仍需改变列表和身份契约。
- Sumika 已经自行实现普通气泡、引用、回应、图片组、贴纸和大图查看器。整体 fork 不会自动收回这些重复实现。

可以选择性复用 CrychicUI 的 MIT 代码，但应明确来源，并把被采用的实现迁成 `MewTypeUI` 自己维护的一份代码。**新库核心不依赖 CrychicUI；过渡期由 Sumika 同时引用两库。**

已核对的版本和现状：

- Sumika：CrychicUI `v0.0.0-20261027050735-bf15ddf6600a`，mygo `v0.7.4`。
- CrychicUI 自己的文档仍写 mygo `v0.5.5`；本报告按 Sumika 实际选中的 `v0.7.4` 核对接口。
- mygo-m3e 模块名为 `github.com/tsugumi42/mygo-m3e`，其 `go.mod` 声明 mygo `v0.7.3`。
- **当前 Sumika 的 `native/go.mod` 没有引用 mygo-m3e，也没有相应 replace。**其 README 写的是替换意图，不能当成已接入。
- mygo-m3e 根目录未找到 `AGENTS.md`；本次没有读到该文件，不能归纳它的 AI 规则。

依据：[Sumika 依赖](native/go.mod:1)、[CrychicUI 规则](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/AGENTS.md:1)、[mygo-m3e README](<mygo-m3e 源码目录>/README.md:1)、[mygo-m3e 依赖](<mygo-m3e 源码目录>/go.mod:1)。

## 2. 仓库结构和 API 约定

建议按“界面区块 + 通用控件”分包。下面是职责划分；实现时随着组件落地创建目录，不预建空包。

```text
MewTypeUI/
├─ go.mod
├─ README.md
├─ AGENTS.md
├─ LICENSE
├─ THIRD_PARTY_NOTICES.md
├─ theme/                 主题定义、明暗预设、解析、导入导出、mygo 映射
├─ control/               按钮、图标按钮、文本框、搜索框、开关、选择控件
├─ display/               头像、未读徽章、状态标记、快捷键标签
├─ layout/                分栏、可拖分隔线、面板顶栏、收窄后的容器排法
├─ navigation/            左侧竖栏、会话筛选标签、侧栏标签
├─ dialogs/               会话行、会话分组、虚拟会话列表
├─ chat/                  顶栏、消息行、气泡、引用、回应、日期和未读分隔
├─ compose/               多行输入区、回复/编辑栏、附件预览
├─ media/                 图片、相册、贴纸、媒体查看器
├─ overlay/               右键菜单、弹出菜单、对话框、提示
├─ internal/kit/          已有多个组件确实共用的小型辅助函数
├─ gallery/               组件页、主题编辑预览、完整聊天组合场景
├─ cmd/gallery/           原生画廊入口
├─ docs/
│  ├─ architecture.md
│  ├─ theme-format.md
│  └─ reference/          Telegram 数值、状态和交互对照表
└─ testdata/              编造的主题与媒体夹具
```

依赖方向保持简单：

- `theme` 不依赖组件包。
- 通用控件依赖 `theme` 和 mygo。
- 聊天区块依赖实际需要的通用控件、媒体和浮层。
- 组件不依赖 `gallery` 或 Sumika，不出现 `sumika-native/internal/api`。
- 不照搬 CrychicUI 的七层分级；检查循环依赖和宿主依赖即可。

**API 采用 CrychicUI 已有的基本写法：**

```go
// 以下为拟议 API。
func Button(c *ui.Context, label string, o ButtonOptions) ui.Element

func ConversationRow(
    c *ui.Context,
    item Conversation,
    o ConversationRowOptions,
) ui.Element

func Bubble(
    c *ui.Context,
    o BubbleOptions,
    content func(),
) ui.Element
```

统一约定：

1. 第一个参数是 `*ui.Context`；必填数据在前，Options 在后；容器的 `content func()` 最后。
2. 返回根 `ui.Element`，允许继续 `.Grow(1)`、`.MinWidth(0)`、`.Bind(&handle)`。
3. 多个需要独立操作的控件返回命名元素，根元素在前；不把元素装进长期存活的结构体。
4. 数据绑定用持久字段指针；交互通过 `Changed`、`OnClick` 或明确的回调交付。
5. 调用方在构造前设置稳定 key；列表项按 ID，不能按显示名或当前下标认身份。
6. `ui.Local` 只放组件私有的临时 UI 状态。会话草稿、阅读位置、媒体查看器状态归宿主或显式 State。
7. 图片下载、解码、数据库读取和网络发送均由宿主完成。组件接收已准备好的状态和资源。

mygo 明确规定：元素在下一次构建前失效，同一帧也可能多次构建。持久身份用 `ui.Handle`，后台发布用 `Window.Update`。依据：[ui/doc.go](/<Go 模块缓存>/github.com/egoist/mygo@v0.7.4/ui/doc.go:20)。

## 3. 主题：命名颜色、尺寸、字体、动效和半透明

### 3.1 保留完整主题，再映射到 mygo

Telegram 的 `colors.palette` 使用语义名字、颜色别名和不同状态的独立角色。例如：

- `dialogsBg`、`dialogsBgOver`、`dialogsBgActive`
- `msgInBg`、`msgOutBg` 及各自的 Selected
- `msgInReplyBarColor`、`msgOutReplyBarColor`
- `historyComposeAreaBg`、`historyToDownBg`
- `sideBarBg`、`sideBarTextFgActive`

建议借这套组织方式，使用 `MewTypeUI` 自己的键名，另设 Telegram 对照列。

| `MewTypeUI` 颜色组 | 主要角色 | Telegram 对应 |
|---|---|---|
| `window.*` | 背景、正文、次要文字、边线 | `windowBg`、`windowFg`、`windowSubTextFg` |
| `control.*` | 常态、悬停、按下、禁用、焦点 | `activeButton*`、`lightButton*`、`inputBorderFg` |
| `conversation.*` | 行背景、标题、预览、时间、未读；常态/悬停/选中 | `dialogs*` |
| `message.in.*` / `message.out.*` | 背景、文字、链接、代码、时间、阴影；普通/选中 | `msgIn*`、`msgOut*`、`historyText*` |
| `quote.in.*` / `quote.out.*` | 底色、竖条、名字、摘要 | `msgInReplyBarColor` 等 |
| `service.*` | 日期、服务消息、未读分隔 | `msgService*`、`historyUnreadBar*` |
| `reaction.*` | 普通、自己已回应、消息选中时的底色和文字 | 具体完整映射未核实 |
| `compose.*` | 输入区、占位文字、附件、发送、回复栏 | `historyCompose*`、`historySend*`、`historyReply*` |
| `sidebar.*` | 背景、普通/选中图标文字、徽章 | `sideBar*` |
| `menu.*` / `tooltip.*` | 浮层、悬停、禁用、危险项、分隔线 | `menu*`、`tooltip*` |

源码中的浅色例子包括：`msgOutBg = #effdde`、`dialogsBgActive = #419fd9`，入站和出站时间也分别有自己的颜色。**不能把“出站气泡颜色”继续等同于应用 Accent。**

依据：[颜色基础表](/<Telegram 源码目录>/lib_ui/ui/colors.palette:8)、[消息角色](/<Telegram 源码目录>/lib_ui/ui/colors.palette:349)、[侧栏角色](/<Telegram 源码目录>/lib_ui/ui/colors.palette:645)。

### 3.2 主题内部结构

建议区分两种表示：

- **Definition**：可编辑、可序列化；允许稀疏覆盖和颜色别名。
- **Theme**：解析后的只读值；颜色、尺寸等使用有类型的字段，组件构建时直接读取。

```go
// 拟议 API；具体字段随首批组件定稿。
type Theme struct {
    Colors    Colors
    Metrics   Metrics
    Type      Typography
    Motion    Motion
    Materials Materials
}

func Compile(d Definition) (*Theme, error)
func Apply(c *ui.Context, t *Theme)
func T(c *ui.Context) *Theme
```

颜色别名在编辑或导入时解析；检查未知键、缺失引用和循环引用。组件构建时不解析字符串，不遍历颜色表，不复制整套主题。

主题没有变化时复用解析结果。颜色变化不应使消息文档重新解析；字体和尺寸变化才使相关测量失效。mygo-m3e 已有相近做法：`Apply` 按选项、明暗、系统文字缩放缓存主题，再映射给 mygo。[实现依据](<mygo-m3e 源码目录>/m3e/theme.go:110)。

### 3.3 尺寸和字阶按用途命名

不要用一个 `Spacing` 倍数推出所有尺寸。Telegram 本身也没有处处落在统一网格上。

| 参数 | 已读到的 Telegram 声明 | 来源 |
|---|---:|---|
| 基础字号 | 13 | `basic.style`：`fsize` |
| 通用大/小圆角 | 6 / 3 | `basic.style`：`roundRadiusLarge/Small` |
| 普通会话行高 | 62 | `dialogs.style`：`dialogsRowHeight` |
| 会话头像 | 46 | `defaultDialogRow.photoSize` |
| 会话行四边留白 | 左右 10、上下 8 | `defaultDialogRow.padding` |
| 消息最大宽 | 430 | `chat.style`：`msgMaxWidth` |
| 消息内容留白 | 左右 11、上下 8 | `msgPadding` |
| 消息头像 | 33 | `msgPhotoSize` |
| 气泡大/小圆角 | 16 / 6 | `bubbleRadiusLarge/Small`；小圆角引用通用大圆角 |
| 媒体最大尺寸、贴纸最大尺寸 | 430、224 | `maxMediaSize`、`maxStickerSize` |
| 相册格间距 | 4 | `historyGroupSkip` |
| 回应项间距 | 4 | `reactionInlineBetween` |
| 日期淡出时长 | 200 ms | `historyDateFadeDuration` |
| 通用页面滑动时长 | 240 ms | `basic.style`：`slideDuration` |

这些是**源码声明值**，不是本次量出的运行尺寸。高度扩展、分组圆角、尾巴和窄窗口下的实际使用条件，仍需核对调用代码。

尤其不能混用：

- `historyComposeField.heightMax = 72` 是初始声明；HistoryWidget 的 computeMaxFieldHeight 实际用 `historyComposeFieldMaxHeight = 224`，并受剩余窗口高度限制。compose.Field 已按该调用点实现。
- 跳到最新的外框是 `52×62`，其中 ripple 区域是 42，不能把外框尺寸当成圆形按钮直径。
- `lineHeight = 0` 表示没有声明固定行高，不能写成行高为零。
- Sumika 原来的 ADR-240（大角 24、小角 6）已被 Sumika ADR-247 取代：圆角直接照 Telegram（16/6 和它的小角规则），不留旧值覆盖。

建议 Metrics 至少分为 `Conversation`、`Header`、`Message`、`Quote`、`Reaction`、`Compose`、`Sidebar`、`Menu`。圆角支持四角和消息分组状态，内嵌引用、相册边角随外层几何计算。

字体独立定义正文、会话标题、预览、时间、顶栏标题、菜单等字阶。系统 `TextScale` 只乘一次；放大后的内容允许撑高，不能被固定行高裁掉。

依据：[基础尺寸](/<Telegram 源码目录>/lib_ui/ui/basic.style:54)、[会话行](/<Telegram 源码目录>/tdesktop/Telegram/SourceFiles/dialogs/dialogs.style:88)、[消息尺寸](/<Telegram 源码目录>/tdesktop/Telegram/SourceFiles/ui/chat/chat.style:14)、[气泡圆角](/<Telegram 源码目录>/tdesktop/Telegram/SourceFiles/ui/chat/chat.style:326)。

### 3.4 映射到 `ui.Theme`

`ui.Theme` 是基础控件的通用主题。完整聊天角色保留在 `MewTypeUI.Theme` 中。

| mygo 字段 | 建议来源 |
|---|---|
| `Dark`、`Background` | 解析后的模式、`window.background` |
| `Surface`、`SurfaceHover`、`SurfacePressed` | 通用控件三个背景态 |
| `Border` | `control.border` |
| `Text`、`TextMuted` | 通用正文、次要文字 |
| `Accent`、`AccentHover`、`AccentPressed`、`AccentText` | 主操作按钮对应角色 |
| `Danger`、`Warning`、`Success` | 独立语义色 |
| `Selection`、`Focus` | 通用文字选区、键盘焦点环 |
| `Inverse`、`InverseText` | 通用反色表面 |
| `Scrollbar`、`ScrollbarWidth` | 通用滚动条 |
| `Radius`、`Spacing` | 基础控件的默认值 |
| `FontSize`、`Font` | 基础正文与字体族 |

会话选中态、入站/出站链接和选区、引用竖条、媒体蒙层等，由组件读取完整主题。mygo 没有的角色不强塞进现有字段。

依据：[ui.Theme 完整字段](/<Go 模块缓存>/github.com/egoist/mygo@v0.7.4/ui/theme.go:5)。

### 3.5 半透明必须区分窗口材质和内容背景模糊

| 目标 | 已核实接口 | 设计边界 |
|---|---|---|
| 模糊窗口外的桌面或其他窗口 | `WindowOptions.Vibrancy`、`Window.SetVibrancy` | 系统材质；Windows 窗口创建时就须具备材质条件 |
| 模糊窗口内已经绘制的内容 | `glass.Blur{Radius: …}`，通过 `.Material(...)` 使用 | 可调 DIP 模糊量；适合浮动顶栏和面板 |
| 折射、亮边等玻璃效果 | `glass.Glass{Style, Tint, Clarity, …}` | 是可选外观，不默认代替 Telegram 的平面表面 |
| 表面半透明 | 颜色 alpha | 背景和文字分别绘制 |
| 整个窗口透明度 | `WindowOptions.Opacity` | 会影响整窗，不宜拿来实现“背景透明、文字不透明” |

主题参数应表达：

- `windowMaterial`：none / mica / acrylic 等系统材质。
- 各表面的背景颜色及 alpha。
- 各表面的 `backdropBlur`。
- 可选玻璃模式及 clarity。

**不能承诺通过 Vibrancy 任意调整系统模糊半径。**本次核实的公开接口提供材质选择，未见对应的系统 blur/tint 数值旋钮。Windows 外窗圆角也不接受任意数值效果；mygo 注释明确说明采用系统圆角。

实际能力通过 `c.Vibrancy()` 判断。不支持时使用主题定义的实色背景。组件不调用 `Window.SetVibrancy`；由宿主应用主题时处理窗口设置。`Element.Blur` 会模糊组件及其文字，不能用来冒充背景模糊。

依据：[窗口选项](/<Go 模块缓存>/github.com/egoist/mygo@v0.7.4/window.go:144)、[Vibrancy 平台说明](/<Go 模块缓存>/github.com/egoist/mygo@v0.7.4/vibrancy.go:3)、[内容背景模糊](/<Go 模块缓存>/github.com/egoist/mygo@v0.7.4/plugins/glass/blur.go:10)。

### 3.6 用户编辑与导入导出

建议先用一个有版本的 JSON 格式，使用 Go 标准库，不为主题引入新的配置依赖。

```json
{
  "version": 9,
  "base": "desktop-light",
  "colors": {
    "window.background": "#ffffff",
    "conversation.background": "@window.background",
    "message.out.background": "#effdde"
  },
  "metrics": {
    "message.radiusLarge": 16,
    "message.radiusSmall": 6,
    "conversation.rowHeight": 62
  },
  "materials": {
    "sidebar": {
      "backdropBlur": 0,
      "background": "#293a4cff"
    },
    "window": {
      "kind": "none"
    }
  }
}
```

上述颜色、尺寸用于展示格式；材质默认项是本提案选择，不声称来自 Telegram。

编辑流程：

1. 在副本中编辑，实时预览组件和完整聊天场景。
2. 解析成功后整体替换当前主题；失败保留原主题，定位到错误键。
3. “缺少覆盖”和“显式为 0”分开：圆角 0、模糊 0、全透明颜色都合法。
4. 导出完整的可移植定义，保留别名，并包含所依赖的基底值，避免换一个库版本就变样。
5. 文件选择、读写、保存用户偏好由宿主完成；库只提供编解码和主题编辑组件。

Telegram 的 `.palette` / `.tdesktop-theme` 文件兼容导入不列为首批承诺；其完整格式和资源容器本次未核实。圆角、字号和材质是 `MewTypeUI` 自己的主题扩展，不能宣称 Telegram 的 `colors.palette` 本身包含这些参数。

## 4. 组件清单、现有对应物和 API 草图

下表全部 API 为拟议设计。`c` 是构建上下文；State 只存数据和句柄。

| 组件及 Telegram 区块 | CrychicUI 对应与可借部分 | `MewTypeUI` API 草图 |
|---|---|---|
| 会话列表行：Dialogs | `chat.ConversationList`；借头像、标题/预览/时间/未读结构，重做样式与身份 | `dialogs.Row(c, item, RowOptions{Selected: on})` |
| 虚拟会话列表：Dialogs | 现有 ConversationList 不虚拟化；不沿用逐行循环 | `dialogs.List(c, &state, items, ListOptions{OnSelect: fn})` |
| 左侧导航：Filters sidebar | `navigation.NavRail`；借键盘、选择和标签行为 | `navigation.Rail(c, items, &activeID, RailOptions{})` |
| 会话筛选标签 | `UnderlineTabs` 等；借值绑定，不照搬尺寸和动画 | `navigation.FilterTabs(c, items, &activeID, TabOptions{})` |
| 搜索框：Dialogs filter | `input.SearchInput`、`DebouncedSearch`；延迟只在确实需要时配置 | `control.Search(c, &query, SearchOptions{})` |
| 聊天顶栏：History top bar | `chat.ChatHeader`；它固定带头像、语音和视频键，不能直接套用 | `chat.Header(c, info, HeaderOptions{Actions: fn})` |
| 消息行和分组 | `chat.MessageList`；借分组结构；Sumika 已有虚拟列表 | `chat.Row(c, row, RowOptions{}, content)` |
| 气泡壳：History message | `Bubble`；需独立入站/出站色、分组圆角和可控入场 | `chat.Bubble(c, BubbleOptions{Side: side, Group: group}, content)` |
| 富文本正文 | Sumika 已用 `ui.RichText` 及行内附件 | 继续组合 mygo 富文本，库提供正文样式和容器 |
| Markdown 正文 | `chat.MarkdownMessage`；借代码块、链接和复制行为；去除其自带气泡壳 | `chat.Markdown(c, source, MarkdownOptions{})` |
| 回复引用 | `chat.ReplyQuote`；借点击跳转、名字和摘要；扩展失效/缺失状态 | `chat.Quote(c, quote, QuoteOptions{OnActivate: fn})` |
| 转发头/合并转发预览 | `chat.Forwarded`；只确认存在，复杂交互未核实 | `chat.Forward(c, preview, ForwardOptions{OnOpen: fn})` |
| 链接预览 | `chat.LinkPreview`；可作结构参考，加载和打开仍由宿主处理 | `chat.LinkPreview(c, preview, LinkOptions{OnOpen: fn})` |
| 表情回应 | `chat.Reactions`；它自行改计数且只画文字 emoji，不能直接借其数据行为 | `chat.Reactions(c, items, ReactionOptions{OnActivate: fn})` |
| 图片 | `chat.ImageMessage`；借打开查看器交互，避免查看器绑在虚拟行内 | `media.Photo(c, photo, PhotoOptions{OnOpen: fn})` |
| 相册 | 未核实到等价组件；Sumika 已有图片组 | `media.Album(c, photos, AlbumOptions{OnOpen: fn})` |
| 贴纸/GIF | `chat.Sticker`；借透明底、contain；播放由宿主媒体状态驱动 | `media.Sticker(c, frame, StickerOptions{})` |
| 媒体查看器 | `overlay.Lightbox`；Sumika 已有独立实现，应优先保留现有交互 | `media.Viewer(c, &state, items, ViewerOptions{})` |
| 日期分隔 | `chat.DayDivider`；现有是横线分隔，Telegram 是服务消息样式 | `chat.DateMarker(c, label, DateOptions{})` |
| 未读分隔 | `chat.UnreadDivider`；视觉重做 | `chat.UnreadMarker(c, label, UnreadOptions{})` |
| 跳到最新 | `chat.JumpToLatest`；它在 count=0 时隐藏，不符合“离开底部但没有新消息”场景 | `chat.LatestButton(c, LatestOptions{Visible: away, Unread: n})` |
| 消息右键菜单 | 用 `overlay.ContextMenu` 研究行为；`chat.MessageMenu` 实际是悬停工具条 | `overlay.ContextMenu(c, target, items, &state, MenuOptions{})` |
| 输入区 | `chat.Composer`；借 Enter/Shift+Enter、附件/发送布局，明确输入法和提交边界 | `compose.Editor(c, &draft, EditorOptions{OnSubmit: fn})` |
| 输入区回复/编辑栏 | `ReplyQuote` 只能作局部参照 | `compose.ReferenceBar(c, reference, ReferenceOptions{OnCancel: fn})` |
| 附件预览 | `chat.Attachment` 可参考文件项；图片队列未核实 | `compose.Attachments(c, items, AttachmentOptions{OnRemove: fn})` |
| 右侧栏标签 | `navigation.UnderlineTabs`；保留宿主内容槽 | `navigation.SideTabs(c, items, &activeID, TabOptions{})` |
| 通用按钮、头像、徽章、提示、确认框 | `button`、`display`、`overlay` | 同类函数 + Options，直接返回 `ui.Element` |

这里有几个必须写进契约的边界：

- **回应数据由宿主提供。**点击只交付意图，不自行把服务端计数加一。当前 Sumika 的 QQ 回应是只读显示，迁组件不能顺便打开回应发送。
- **查看器在窗口常驻层构建。**缩略图滚出虚拟列表后，已打开的大图继续存在。
- **正文不自带气泡。**普通文本、Markdown、引用和相册能够进入同一个壳，避免 Sumika 现在 Markdown 引用另套小壳的问题。
- **入场由明确的新消息事件驱动。**稳定 ID 解决身份，不能代替“这条历史消息是否应播放入场”的判断。
- **输入区提交表示请求。**草稿何时清除由宿主接受结果决定，不能把触发回调当成发送成功。
- **菜单只展示宿主提供的操作。**本地私聊、本地群聊、QQ 私聊、QQ 群的能力差异由 Sumika 决定，库不推导权限。

已核对的关键实现：[ConversationList](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/chat/conversation_list.go:23)、[Composer](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/chat/composer.go:19)、[Reactions](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/chat/reactions.go:25)、[ContextMenu](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/overlay/context_menu.go:10)。

### Sumika 当前实际使用的 CrychicUI 包

生产 Go 文件直接引用以下 16 个包。下面区分实际调用、演示路径和仅类型引用；不把注释里列的组件算成在用。

| 包 | 实际组件/符号 | 位置和用途 |
|---|---|---|
| `chat` | `ConversationList`、`Conversation`、`MarkdownMessage`、`Message`、`DayDivider`、`Composer` | `ui.go` 会话列表及演示输入；`bubble.go` Markdown；`row.go` 日期；`avatar.go` 会话头像适配 |
| `navigation` | `NavRail`、`UnderlineTabs`、`VerticalTabs` | 主导航、右侧栏、设置分类、记忆详情 |
| `button` | `IconButton`、`Outline`、`Ghost` | 聊天顶栏、分栏折叠、设置操作、记忆操作 |
| `input` | `SearchInput`、`DebouncedSearch`、`NumberField`、`UnitField`、`TextField`、`AutoTextArea`、`ListField` | 会话搜索、记忆搜索、演示设置 |
| `choice` | `ChipChoice`、`Dropdown`、`Segments`、`ToggleRow`、`Check`、`OptionCard` | 记忆筛选、设置和状态页 |
| `datetime` | `TimePicker`、`TimeRangeSlider`、`PresetRange` | 演示设置的时间与日期范围 |
| `display` | `Avatar`、`StatusBadge`、`Badge`、`Legend`、`DescriptionList` | 收窄会话列表、设置状态、记忆详情 |
| `layout` | `PageHeader`、`Section`、`Collapse` | 设置、后台状态、记忆页 |
| `typography` | `Heading`、`Caption`、`Label`、`KeyCombo` | 顶栏、说明、时间、快捷键 |
| `overlay` | `DangerDialog`、`RichTooltip` | 演示清理确认、真实设置来源提示 |
| `feedback` | `EmptyState`、`SkeletonList`、`ErrorState` | 记忆页空态、加载和失败 |
| `data` | `AuditTrail`、`Audit` | 记忆修订过程 |
| `kit` | `Press`、`Arrive`、`Muted`、`Shade`、`Clicked`、`Settle`、`Log` | 自写气泡、引用、图片、分栏和查看器 |
| `icon` | `Must` | 大图查看器图标 |
| `sound` | `Play` 和相应 cue | 图片打开/关闭、查看器翻页、会话选择 |
| `agent` | `Decision` | `main.go` 的字段类型；当前读取路径未见 `Approval` 组件调用 |

`chat.Bubble` 另见测试、基准和 `_spike`；普通生产消息已经走自写壳。`chat.ImageMessage`、`chat.Sticker`、`chat.ReplyQuote`、`chat.Reactions` 主要出现在说明其不足的注释中，不能列成生产调用。

依据：[主界面](native/ui.go:22)、[消息壳与 Markdown](native/bubble.go:327)、[回应](native/reaction.go:1)、[演示设置](native/settings.go:132)、[记忆页](native/memoryview.go:137)。

## 5. 从 CrychicUI 迁移的顺序

建议按完整区块切换。每步删除对应旧展示实现，不在 `MewTypeUI` 里增加兼容 CrychicUI 的转发层。

| 顺序 | 替换范围 | 暂时保留 | 这一阶段证明什么 |
|---|---|---|---|
| 1 | 主题、必要的通用控件、组件画廊 | Sumika 原界面 | 新库能独立展示，主题覆盖和 mygo 映射成立 |
| 2 | 会话行、虚拟会话列表、会话搜索 | 聊天内容、设置、记忆页 | ID 选择、空会话、未读、长名称、过滤及前插重排成立 |
| 3 | 聊天顶栏、导航和分栏表面 | 现有消息列表与滚动逻辑 | 多栏/窄窗、系统标题按钮避让、焦点和拖拽成立 |
| 4 | 气泡壳、日期、引用、回应、无壳正文 | 取页、阅读锚点、身份与消息适配 | 一条图文消息只套正确的壳；历史滚回不重播 |
| 5 | 图片、相册、贴纸、窗口层查看器 | 现有下载、解码、缓存和播放机制 | 图片晚到、失败、离屏、查看器持续存在成立 |
| 6 | 右键菜单、跳到最新、输入区及附件栏 | 后台能力和发送策略 | 键盘/输入法、只读状态、操作意图交付成立 |
| 7 | 设置、记忆、状态页的通用控件 | 尚未替换且仍有实际用途的 CrychicUI 组件 | 最后一批调用清零，再移除依赖 |

过渡期主题按子树隔离：旧区域用 `ui.Themed` 保持原有主题，新区域读取 `MewTypeUI` 的主题。不要一开始全局改 `Radius`、`Spacing`，让所有未迁组件一起变形。mygo 的 `SetTheme` 会影响根和后续控件，`Themed` 才是局部主题作用域。[接口依据](/<Go 模块缓存>/github.com/egoist/mygo@v0.7.4/ui/context.go:332)。

优先保留的 Sumika 机制：

- `ui.List`、稳定消息 ID、取历史、滚动锚点和已读标记。
- 实时新消息的入场判定。
- 行内小黄脸的复制还原。
- 媒体获取、缓存、解码和离屏播放控制。
- 引用定位、回应身份归并、平台数据适配。

这些机制已经承担业务或性能约束，不能为“统一组件库”重新实现一遍。

CrychicUI 的设置、日期、记忆反馈组件可持续使用到最后；`agent.Decision` 等类型也只在对应界面真正切换时移除。AI 特有卡片仍按 ADR-245 的例外单独设计，不硬映射成 Telegram 功能。

## 6. 画廊、测试和 AGENTS.md

### 6.1 画廊

借 CrychicUI 的登记方式，每个组件有稳定 ID、名称、说明、演示和事件显示；借 mygo-m3e 的原生画廊和主题检查器，不先增加网站部署链路。

画廊包含两类页面：

- **单组件页**：普通、悬停、按下、禁用、选中、空、失败等实际相关状态；旁边展示最小调用代码。
- **组合页**：完整聊天界面，覆盖本地私聊、本地群聊、QQ 私聊、QQ 群的展示差异，以及收窄窗口、加载历史、引用、回应、图文和流式 Markdown。

主题检查器应能实时改颜色、圆角、字阶和材质，并展示每项默认值的源码出处、当前覆盖值及平台实际支持情况。

数据使用编造的完整对话和可分发夹具。主题调整不能重置输入内容、列表位置、选中项或查看器。

依据：[CrychicUI 登记与示例源码](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/gallery/registry.go:18)、[mygo-m3e 画廊](<mygo-m3e 源码目录>/gallery/app.go:16)。

### 6.2 测试方式

本次没有运行测试。以下是实施时的验收设计。

- **交互用 `ui.NewTester`**：按 Label、角色或句柄找到目标，执行点击、键盘、输入和滚动，再断言绑定值、操作意图和最终状态。
- **布局按对照表量**：默认预设在约定窗口、字体和缩放条件下核对边界、间距、圆角和对齐，遵守 ADR-246 的不超过 1 DIP 误差。字体回退引起的差异单列。
- **动画用 Tester 时钟**：检查起点、中间态、终点和结束后不继续请求无用帧；不用 sleep。
- **保留能抓住实际回归的测试**：滚出再滚回、前插历史、换会话、图片晚到、查看器触发行消失、菜单焦点恢复、输入法确认。
- **主题测可见行为**：修改出站颜色只影响相应角色；导入导出后颜色/尺寸一致；显式 0 被保留；错误导入不覆盖现有主题。避免只断言字段被复制。
- **规模检查用画廊固定场景**：十个角色、上万条消息、长历史、千条会话；记录构建行数、元素数和帧耗时，不先编性能门槛。
- **原生效果另验**：Tester 能模拟有无 Vibrancy，不能证明 DWM 材质、GPU 帧耗时或真实输入法表现；实施时必须补真实窗口检查。

CrychicUI 的 Composer 测试已展示“输入 → Enter → 仅提交一次 → 空白不提交”的行为检查；mygo-m3e 有“聚焦再失焦后占位字清除”的回归测试。这些写法值得借，不必继承它们所有快照和默认值断言。

依据：[Composer 测试](/<Go 模块缓存>/github.com/!e!l!y-!open-!source/!crychic!u!i@v0.0.0-20261027050735-bf15ddf6600a/chat/compose_test.go:11)、[文本框回归测试](<mygo-m3e 源码目录>/m3e/text_field_test.go:57)。

### 6.3 AGENTS.md 应写成什么

建议收录以下硬规则：

1. **默认视觉数值必须有对照表来源**：Telegram 版本、文件、符号、值、单位、使用条件、本库参数、验收方式。没有来源写“未核实”，不得凭截图补一个值。
2. **数值基线和用户覆盖分开**：源码对齐约束默认预设；用户改主题后按用户值验收。Sumika 特例注明 ADR。
3. **动效逐项取证**：时长、缓动、触发、打断、退场都要有依据；不得统一替换成 `Snappy` 或 M3 弹簧。
4. **组件不写颜色和任意几何常量**：从主题读取；算法必要常量说明用途，不能把它冒充设计参数。
5. **沿用 mygo 的元素生命周期和基础输入行为**：不保存元素、不在后台碰 Context/Element、不在绘制回调执行 I/O。
6. **列表按稳定 ID；长期状态归宿主**：滚出视口不是新消息，查看器不归缩略图行所有。
7. **每个组件同时交付可操作画廊与必要行为验证**：不以“不 panic”“存在节点”代替验收。
8. **参考 Telegram 的结构、数值和外部可观察行为，独立写 Go 实现**：不逐段翻译 C++，不复制其素材、logo、提示音和贴纸。
9. **复用 MIT/ISC 等允许复用的实现时保留来源和许可**，不把改名当作独立实现的证明。
10. **库不带 Sumika 业务**：不读真实数据目录、不连接 QQ、不决定权限、不打开执行工具、不主动发送消息。

对照版本沿用 ADR-246 记录的 tdesktop `250fc73`、lib_ui `1ff7be7`；本次未重新核对本地 Git HEAD。需要更新版本时先更新对照表，再改默认值。

## 7. 许可证与 Sumika 引用方式

### 许可证

建议与 mygo-m3e 一致，原创代码采用 **MIT**。其根 LICENSE 已核实为 MIT；mygo-m3e 自己也明确把 MCU、图标、Mermaid 等第三方内容分开声明。

`MewTypeUI` 同样维护 `THIRD_PARTY_NOTICES.md`：

- 复用 CrychicUI 的实质代码：保留其版权和 MIT 许可。
- 使用 Lucide 等图标：附对应 ISC/MIT 等许可。
- 不使用 Telegram 的图标、logo、贴纸和提示音。
- 不因新库采用 MIT 就把引入的第三方内容全部标成 MIT。
- 不为借用主题架构引入整套 mygo-m3e 的 MCU、音频或 Mermaid 依赖；确有组件需要时再单独评估。

这里建议的是独立实现的许可选择，不能仅凭“换成 Go、改了函数名”认定不存在衍生代码问题。依据：[mygo-m3e LICENSE](<mygo-m3e 源码目录>/LICENSE:1)。

### 本机接入

假设新仓库位于 `<MewTypeUI 源码目录>`，模块名拟为 `github.com/tsugumi42/MewTypeUI`，则 Sumika 的 `native/go.mod` 可采用：

```go
// 示意；MewTypeUI 待正式名称确定后替换。
require github.com/tsugumi42/MewTypeUI v0.0.0

replace github.com/tsugumi42/MewTypeUI => ../../MewTypeUI
```

相对路径从 `sumika/native` 出发，因此是 `../../MewTypeUI`。开发期用本机 replace；发布时引用已提交的真实版本或伪版本，避免构建依赖本机路径。

模块版本选择由消费方共同决定：Sumika 当前使用 mygo `v0.7.4`，新库也应按这一版本核对接口。mygo-m3e 声明的 `v0.7.3` 不会因此成为 Sumika 的实际运行版本；兼容性本次未通过编译或测试验证。

仍需在对应组件实施时核实顶栏和窄窗参数、相册排布、回应完整配色及其他区块的动画轨迹。输入区两个高度上限已由 compose 路核实，状态与来源见 reference/compose.md。
