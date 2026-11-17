---
name: mygo-ui
description: 写、改或审查 Sumika native/ 的 mygo 界面时使用；先读设计规则，按数据形状选 internal/winshell 的控件，填页面蓝图，再用 Tester 和帧统计验收。
---

# Sumika mygo 界面

## 什么时候读这页

写新页面、新控件，改布局、状态或文案，审查别人的界面改动时读。只改后台、只改文档时不读。

## 先读什么

1. [开发指导/界面与交互](../../../docs/开发指导/界面与交互.md)：八条规则、组件选用表、页面蓝图、审查。和本页冲突时以它为准。
2. [设计语言](../../../docs/设计语言.md)：层次、颜色、间距、圆角、字号、动效。四边等距、圆角同心（里层圆角 = 外层圆角 − 间距）是硬规矩。
3. [设计自查](../../../docs/设计自查.md)：交付前逐条过的验收表。
4. [native/README.md](../../../native/README.md) 的「编译和打开」「独立交互原型」「真窗口自动巡视」：启动、`MYGO_FRAME_STATS=all` 量帧、`ui.NewTester`、真窗口巡视。
5. 本技能 `references/` 只当背景，见文末。

版本以 `native/go.mod` 为准：本页核对 mygo v0.9.1。受控控件在 `internal/winshell`，入口是 `winshell.Kit`。

## 八条短规则

1. **先填蓝图，再写代码。**（见下）
2. **控件查表选，不凭感觉。** 表里没有的形状，先在表里加一行并写理由。
3. **页面只通过受控接口面用组件。** 现在是 `internal/winshell` 的 `winshell.Kit`；不直接 import 组件库实现，不写死颜色、尺寸、圆角、字号、时长（ADR-374、ADR-347）。
4. **设置只在目录里加。**〔目标〕`internal/settingsreg` 加一条，界面自动出；现在 `settingsreg.Entry` 有 `Effect`，没有读取处字段，还要照 `notifications.*` 的写法接上后台读取处。不为一个设置手写状态机。
5. **一行一件事。** 一个标题、至多一行说明、一个控件；说明写“这个会怎样”，不写字段名和原理。
6. **每个状态都画出来：** 加载、空、读不到（带原因）、只读（带原因）、写入中、没存上（带原因）、要重启。
7. **数量乘十再看：** 十个角色、几百个群、最长的名字、最窄的窗口。
8. **没人读的设置不上架，** 界面不出现可编辑控件。

## 现在还没有的

- 独立的门面包：还没有；页面现在引 `internal/winshell` 的 `winshell.Kit`。
- 机器可读的页面清单：还没有；蓝图先贴规格或提交说明。
- 设置目录自动出界面、目录里的读取处字段：还没有（〔目标〕）。
- 文案目录：还没有，文字直接写在代码里。
- 通用对象管理页样板：还没有；设置页外壳是 `winshell.SettingsSpec`，对象页照 `workbench_reminders.go` 的结构，状态搬出 `app`。
- 下列控件：选项卡片、本机路径、密钥、时间段、字符串列表、多选片、表格、带“测试”的外部地址。都先在 `internal/winshell` 补受控控件，再上页面。

## 数据形状选控件

先看数据形状，再看改了什么时候生效，再看选项数量和要不要说明后果，最后才是好不好看。

| 数据形状或场合 | 用什么 | 现成入口 | 不要 |
|---|---|---|---|
| 布尔，改了就生效 | 开关 | `winshell.SwitchValue` | 勾选框 |
| 布尔，属于一次提交的表单 | 勾选 | `winshell.CheckValue` | 开关（暗示立刻生效） |
| 2～4 个互斥选项，名字短 | 分段 | `winshell.Segment` | 下拉 |
| 2～3 个互斥选项，每个要一句后果说明 | 选项卡片 | 缺 | 分段加一大段说明 |
| 5 个以上互斥选项 | 下拉 | `winshell.Dropdown` | 一排单选 |
| 选项很多或要搜（模型、字体、群） | 带搜索的下拉 | `winshell.Picker`、`winshell.RemotePicker` | 长下拉 |
| 小范围整数，常微调 | 加减框 | `winshell.Stepper`（规则用 `winshell.StepperSpec`） | 滑块 |
| 连续量，要边拖边看效果 | 滑块，旁边显示数值 | `winshell.SliderRow` | 不需要即时预览的数用滑块 |
| 大范围的数（字节、token、毫秒） | 数字输入带单位 | `winshell.NumberField`（单位换算缺） | 让人填毫秒和字节 |
| 比例 | 百分比的加减框或滑块 | 用 `winshell.Stepper` 或 `winshell.SliderRow` 组合 | 让人填 0.85 |
| 短文字、长文字 | 单行输入、多行输入 | `winshell.TextField`、`winshell.TextArea` | 单行框里塞长文 |
| 搜索 | 搜索框，停手才报 | `winshell.SearchField`、`winshell.DebouncedSearch` | 每敲一个字就查 |
| 本机路径 | 输入框加“选择…”，路径不存在时行内提示 | 缺 | 纯文本框 |
| 密钥、令牌 | 只显示“已存、没存”，“更换…”开对话框，永不回显 | 缺 | 任何能看到原文的控件 |
| 时间点、时段 | 时间选择 | 缺 | 文本框填“04:30” |
| 字符串列表 | 列表编辑，可加可删，加之前校验 | 缺 | 逗号分隔的文本框 |
| 有顺序的列表 | 可拖动排序，带键盘的上移下移 | 缺；手写过一份，还没抽成控件 | 让人填序号 |
| 每个会话或每个角色一行的覆盖值 | 表格 | 缺 | 每个对象一个分类页 |
| 对象集合（账号、提醒、定期任务、MCP、技能、规则） | 管理页：列表加详情，建、看、改、完成或取消、删五件齐 | 设置页外壳 `winshell.SettingsSpec` 现成；对象页还没有通用样板 | 塞进设置行 |
| 只读事实（版本、路径、状态） | 名字加值的列表 | `winshell.KeyValues` | 混在可编辑行中间 |
| 进入下一层、同一项下的细项 | 带箭头的值行、可展开的行 | `winshell.ValueRow`、`winshell.DisclosureRow` | 另开一页放三个细项 |
| 一行设置、节标题、组间分隔 | 设置行、小标题、分隔 | `winshell.Row`、`winshell.SectionHeading`、`winshell.Break` | 每页自己排一套 |
| 说明、筛选片 | 弱一档的说明、会换行的筛选 | `winshell.Note`、`winshell.Chips` | 说明和正文一个灰度 |
| 危险或不可撤回的动作 | 确认对话框，写清后果，按钮写动作名 | `winshell.ActionDialog` | “确定、取消” |
| 没数据、出错、状态字 | 空态、错态、徽标 | `winshell.EmptyState`、`winshell.ErrorState`、`winshell.Badge` | 空白；只写“失败”；只靠颜色 |
| 需要验证的外部地址 | 输入框加“测试”，结果分类报告 | 缺 | 存了才知道错 |

整窗用 `winshell.Frame`，参数是 `winshell.State` 和 `winshell.Spec`；设置页的导航数据是 `winshell.SettingsSpec` 和 `winshell.Category`。

## 页面蓝图（动手前填）

```
页面或小节：
1. 这页帮主人做的一件事（一句话）：
2. 对象和数量级（几个角色、几百个群、最长的名字多长）：
3. 层级：页 → 小节（最多 7 个，每个一句目的）→ 行；哪些默认收起：
4. 每个数据项：名字｜形状｜控件（选用表里哪一行）｜生效时机｜档（常用、进阶、开发）：
5. 各状态显示什么、显示在哪：加载｜空｜读不到｜只读｜写入中｜没存上｜要重启：
6. 主操作；危险操作和确认话术：
7. 窄窗口怎么变：
8. 文案：每个标题、说明（各不超过一行，用主人的词，同一件事只一个叫法）：
9. 照的是哪个现成页面；和它不同的地方及原因：
```

## 审查清单

**机器能查的（多数还没有自动检查）**
- 页面不直接 import 组件库实现（MewTypeUI、CrychicUI、mygo-m3e）：`internal/archcheck` 里的 `newui_test.go` 已按 ADR-374 拦住，只有 `internal/winshell` 放开 CrychicUI；老页面的存量登记在 `newui_baseline_test.go`，只许变少。新页面一条例外都没有。
- 没有写死的颜色、尺寸、圆角、字号、时长，值取自 `internal/design`。
- 设置都来自 `internal/settingsreg` 目录；每条有生效方式（`settingsreg.Entry` 的 `Effect`）和后台读取处（读取处字段还没有）。
- 一页不超过 7 节，一节不超过 8 行；没有空页。
- 每个可点元素有读屏名，命中区不小于 24×24，常用 32。
- 状态矩阵里每个状态都构建得出来。
- 十倍数据下的元素数、帧耗时有数（ADR-056）。
- 四边等距、圆角同心。

**人看的，写进提交说明**
- 第一眼看到的是不是这页最重要的那件事。
- 遮住说明文字，光看标题和控件能不能猜对每项是干什么的。
- 有没有两处说同一件事；有没有内部词（键名、ADR 号、英文枚举值）漏到界面上。
- 常用的三件事各要点几下。
- 和相邻页面放在一起，行高、间距、标题档次是不是同一套。

## 参考目录

`references/` 全是背景，按旧版 mygo 核对，行号和 API 名可能对不上；和本页冲突时以本页为准。

- [sources.md](references/sources.md)：来源、版本和复制边界；版本已过期，只看出处方法。
- [structure.md](references/structure.md)：从完整场景拆结构、状态所有权。
- [theme.md](references/theme.md)：令牌与主题的消费链。
- [components.md](references/components.md)：组件分层、复用和 helper 契约。
- [lists-chat.md](references/lists-chat.md)：列表、聊天、前插、浮动输入区。
- [motion.md](references/motion.md)：离散状态和动效 API。
- [layout-density.md](references/layout-density.md)：包装层、间距、长中文、DPI。
- [interaction.md](references/interaction.md)：焦点、快捷键、IME、命中框、浮层退层。
- [async-performance.md](references/async-performance.md)：加载、搜索、流式、图片、卡帧。
- [testing.md](references/testing.md)：Tester、验收矩阵、真窗口。
- [pitfalls.md](references/pitfalls.md)：常见坑、输入时序、反模式。
- [recipes.md](references/recipes.md)：会话列表、气泡、审批卡等配方。
