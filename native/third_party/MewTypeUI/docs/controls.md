# 通用控件 API 与宿主边界

`control`、`sound` 包名是我定的。`control` 承担跨聊天/设置页面的控件与审批卡；`settings` 保留设置专用行、开关、Radio/Check、可搜索选择框与数值输入/滑块；`overlay` 保留原生菜单、模态框和窗口所有权。`content.ForwardSummary` 是消息内容，停止生成状态属于 `compose`，播放请求属于不依赖 UI 的 `sound`。控件不读文件、不联网、不执行工具、不加载或播放音频。

本路实现只读取本仓库画法规格、mygo v0.7.4 和 CrychicUI 的 MIT 参考，未打开 tdesktop/lib_ui 源码。参考 CrychicUI `button/icon_button.go` 的 ButtonBase 与可访问名称、`navigation/vertical_tabs.go` 的选择/内容分离，采用稳定 ID，未借实现代码。mygo 的编辑器、Handle、Scroll、Effect 和 Tester 提供编辑身份、原生输入法与快照边界。图形沿用已有 Lucide 授权，未带入 Telegram 素材。

## API 与状态

所有 UI 函数先接收 `*ui.Context`，返回根 `ui.Element`；持久值由宿主提供。列表与请求使用 ID，名称可重复；临时动画、按压和测量缓存放组件私有 `ui.Local`。

| 入口 | 状态与合同 |
|---|---|
| `control.RoundButton` | Primary/Light、透明 Light、自然/定宽/全圆角/四角、图标/数字后缀；即时 hover、按压 ripple、disabled；`FullWidth>0` 固定宽、`<0` 增加自然宽。数字后缀保留最新排队目标，150ms Circ。 |
| `control.LinkButton` | 自然文字框、hover 下划线、窄宽省略、键盘与 disabled；无 ripple 和打开 URL 副作用。 |
| `control.IconButton` | 独立 40×40、Info 返回 60×54、关闭 48×54、固定图形偏移与圆形 ripple；`ForceRippled` 可维持菜单反馈。 |
| `control.InputField` | 单行、多行 55…148 DIP；焦点线、浮动占位、宿主 Error、只读/禁用、选择/剪贴板/撤销/IME。单行提交 Enter，多行默认换行，显式 `SubmitOnControlEnter` 才用 Ctrl/Cmd+Enter 提交。组词键不触发提交。 |
| `control.Dropdown` | 独立或设置行入口；`DropdownState` 保存窗口相关状态；`Popup` 复用原生菜单，`SearchBox` 显式选择可搜索框；回调交 ID，关闭恢复入口焦点。 |
| `control.SettingsSlider` | 普通/设置配色的标签分段，区别于数值 `settings.Slider`；按下/拖动预览，释放按 150ms 选择截止时间提交；长文横滚、空/单/禁用项、手动键盘激活。 |
| `control.Tabs` | UnderlineTabs 43 高、Info ProfileTabs 48 高；焦点与 active 分离；右键、横拖不激活、选中项滚入视野、竖滚保护；空项根高 0。 |
| `control.SideCategories` | 由设置行组成的常驻分类，支持分组、图标、选中/禁用、滚动和键盘；宿主保存各页草稿并负责窄窗导航。 |
| `control.PageTitle` | 单/双标题、返回/关闭/尾 badge/多动作，普通 54、Layer 56 高；Inline 自然换行与窄宽动作换行；显式 Hidden 使用 150ms 线性快照显隐、反向接当前透明度。 |
| `control.StatusBadge` / `CountBadge` | 前者为 10/600、4/1/4/1 padding、radius4 的语义文字标签，后者为独立的 19 高未读计数；支持长文限制和明暗状态。 |
| `control.Collapse` | 150ms 线性高度、上下方向、最小预览、反向和内容动态测高；保持子树身份，收起时移走焦点并停止隐藏内容命中。 |
| `control.ApprovalCard` | Awaiting/Submitting/ApprovalFailed/Resolved/Expired、显式允许一次/拒绝、可折叠详情、窄宽动作堆叠；每次请求仅报告一次在途动作，失败后需显式允许重试。 |
| `content.ForwardSummary` | 宿主准备节点；标题最多两行、摘要合计三行、count 独立；SummaryReady/Loading/Failed 与 disabled；打开/重试意图分开，保留已有数据。 |
| `compose.StopGeneration` / `Editor` | `Generation{RunID, Phase}` 支持 Idle/Generating/Stopping/StopFailed/Stopped/Completed；`EditorOptions.Generation` 和 `OnStop` 复用提交槽，Stopping 阻止重复，失败显式重试；保留草稿和输入宽度。 |
| `sound.Dispatch` | `Request` 提供事件/范围/cue ID 与准备好的资源句柄；`Policy` 先判全局、静音、资格和 OS 声音所有权；未设音量是 100%，显式 0 静音；`Options.Preview` 可越过会话静音，仍尊重全局禁音/零音量。 |

宿主应在审批动作回调中更新状态，并在生成停止回调中将对应 RunID 转为 Stopping；执行结果、超时、重试权限、转发节点获取、导航和通知去重均由宿主维护。`sound.Dispatch` 不是音频引擎，500ms 同范围合并与设备错误由宿主处理；构建/paint 不调用它。

分段选择在提交截止前若整条禁用、目标项禁用或移除，会取消未提交预览；重排仍按原 ID 提交。禁用透明度只由一个层级承担：输入框、标签条与摘要卡的子控件继承禁用状态，不重复乘 mygo 的0.5。滚轮回调捕获构建时的悬停状态，不保留或读取跨构建 Element。

Profile Tabs 的文字宽缓存同时依赖 tabPadding；只改主题内边距也会立即重新分配项宽。分段的 StrictSkip 选项为零时读取 segments.strictSkip 主题值，非零选项显式覆盖该值。Dropdown 已打开后禁用入口或清空全部选项会关闭选择层，不交付旧选择。

发送/停止动作仅在120ms切换中使用快照；终态释放快照并直接绘制矢量，避免中间位图网格破坏分数缩放下的浮点居中。原有0.05物理像素居中回归保持原门槛。

Dropdown 入口保留调用者 key；原生菜单和搜索层的 key 由入口 ID 与模式组成。同页多个独立/设置行入口不会覆盖身份或串选择状态。

## 数值与差异

[规格索引](reference/controls.md) 列出十五份画法；[尺寸登记](reference/theme-metrics.md)、[颜色](reference/theme-colors.md)、[字阶/动效](reference/theme-behavior.md) 对应 JSON v9 的 614 色、806 尺寸/比例/计数、34 字阶、74 动效角色。控件原有 83/2/2/9 项与聊天身份字阶保留，替代图标新增74个图形指标；v1–v8完整快照直接拒绝。`go run ./internal/cmd/themegen -recorded` 从仓库登记重建颜色访问器与尺寸，不读取上游源码。

我定的映射集中在 [CTL-COMPOSITION 至 CTL-SOUND](reference/exceptions.md#通用控件与宿主扩展画法规格ctl-spec)；本次另登记 CTL-INPUT-OWNERSHIP。常驻分类、Inline 标题、审批/摘要/AI 生成语义与提示音接口是宿主扩展。Profile Tabs 两端渐隐为程序化替代；mygo 字体、IME、禁用透明度、阴影核函数仍按例外验收，不能冒称与 Qt 素材逐像素一致。

Collapse 完全收起且零预览时隐藏辅助子树；动画中或 MinimumHeight>0 时 mygo 的 IgnoreInput 仍向辅助技术公开子内容，不能保证辅助操作隔离，详见 CTL-COLLAPSE-ACCESSIBILITY。这项未按通过计。

## 画廊与验证入口

`gallery/controls.go` 登记十五页，命名为 `controls/round`、`link`、`icon`、`input`、`dropdown`、`segments`、`tabs`、`categories`、`title`、`badge`、`collapse`、`approval`、`forward`、`stop`、`sound`。例如 `go run ./cmd/gallery -page controls/input -focus`。每页以宿主示例展示静态状态与可点交互。

`control/control_test.go` 测量实际根框、间距、颜色与字体材料，真点点击/拖动/键盘/IME、动画中点与终点；`compose/stop_test.go`、`content/forward_summary_test.go`、`sound/sound_test.go` 覆盖状态所有权与回调。数值误差限 1 DIP。新增断言的改坏、完整测试、画廊实跑和性能实测记录在本页后续验收段。

## 性能口径

工具链为 Go 1.27.3 windows/amd64、mygo v0.7.4，CPU 为 Ryzen 5 5600X（6核12线程）。系统枚举包含 RTX 5070 Ti 和两个虚拟显示适配器；不以设备列表冒称具体 GPU 被使用，实际 surface 路径看帧日志。测试使用共享重任务锁和低优先级，不与其他长时间 CPU 工作并跑。

采用任务指定的 Sumika《标准-性能与效率-2026-11-03》；交互 100ms 的依据为 [Nielsen](https://www.nngroup.com/articles/response-times-3-important-limits/)，帧预算参考 [RAIL](https://web.dev/articles/rail)。数值在采样前固定：输入/点击 P95≤100ms、P99≤200ms；60Hz 应用帧 P95≤10ms，120Hz≤5ms；缓存后的新进程启动 P95≤1s；空闲十分钟单核 CPU 平均≤0.5%、一秒样本 P95≤2%；主 UI 私有内存≤256MiB。这些分位数与资源线沿用项目暂定预算，不称为通用行业认证。

原生场景固定为780×920 DIP、十五页各自打开及实际指针状态、200次文本输入后原生截图、30次缓存后新进程启动、三遍各12秒数字/Tabs/标题/折叠动效、600个一秒空闲样本；记录 DPI、窗口所在显示器的 EnumDisplaySettings 刷新率、UI 进程 CPU/私有内存/工作集与 mygo frame stats。首进程与其后启动分开；没有清空系统文件缓存，不把首次本次启动称作系统冷启动。输入→截图包含 IPC 与 PNG 读取，不能代替物理键盘→屏幕呈现。实际窗口另以本进程 PrintWindow 抓图检查。

物理呈现错失比例、独立120Hz专场、GPU显存、72小时稳定性与宿主音频设备不在本轮采样口径内；不填假值或按通过计。组件本身不落文件；验证产物与文件大小单列在任务 scratchpad，测试写入仅使用编造数据或模型生成的临时夹具。

## 实测几何与回归（2026-11-04）

共享锁与 Idle 优先级下，`CGO_ENABLED=0 go test -count=1 ./...` 的11个测试包通过；`go vet ./...`、锁定的 mygo v0.7.4 vet、原生画廊构建通过。`-recorded` 在临时目录逐字节复现颜色/尺寸访问器和尺寸登记页；完整生成器用586个编造 palette 名称验证，不读取上游源码。十五页在明暗、LTR/RTL、100/125/150% 下均能静止，生成180张图、6,731,167字节，并运行窄宽和150%文字重排。

以下为 `ui.NewTester`、浅色默认字体、500 DIP父宽下的实际值；文字带小数是实际塑形结果，不按字号猜行高。完整记录为任务 scratchpad 的 `geometry-report.json`。

| 场景 | 实测 DIP |
|---|---|
| RoundButton “Action” | 72.2827×34；文字起点17/8、自然宽38.2827、行高17.2910；默认角4与显式全圆角分别做像素断言 |
| Link “Link” / 独立 Icon | 23.0864×17.2910 / 40×40 |
| 通用输入 | 500×55；正文起点0/28、行高18.6211；长多行上限148另有断言 |
| Dropdown “Alpha” | 98.3091×34；同页独立/设置入口保留各自 key、selected 与搜索层 |
| 两段 SettingsSlider / 下划线 Tabs | 高39 / 43；500宽两段累计取整后间隙2（名义3，误差1）；下划线y40、可见3行像素 |
| Profile Tabs | 根高48；项起点8/6、项高36；“Alpha”宽66、“Beta”宽59；active四边内缩4 |
| 分类 / 双标题 | 无图标分类行38高；标题根54、主副行起点16/8与16/28 |
| StatusBadge “Ready” / CountBadge “88” | 35.6025×15.3008、文字起点4/1 / 23.8047×19、文字起点5/1.5195 |
| Collapse | 编造的完整内容100高；75ms为50、150ms为0；反向/动态测高与焦点转移另验 |
| 审批卡 | 宽364；两个动作各157×36，起点x24/183、间距2；窄宽改为上下排列 |
| 三行摘要卡 | 430×108.4551；标题10/5，count顶84.1641，count独立于三行摘要预算 |
| StopGeneration | 槽44×46、实心框14×14、圆角3；发送/停止切换不改变字段框和草稿 |

114条新增命名断言逐条反转后均变红，恢复后通过；此项只证明断言执行，另用15种生产故障验证会抓住真实错误：按钮高度/前景、折叠缓动、待提交失效、重复禁用衰减、过期Element、关闭淡出仍抢焦点、禁用下拉仍开着、主题padding缓存、strictSkip未读取、摘要总行数、停止重复提交、零音量播放、终态快照偏心、同页下拉key冲突。故障通过 Go overlay 注入，仓库未保留错误实现。

修复前后复现还显示：目标段禁用/移除后回调由1次变0次；输入、三种条和摘要重试的禁用像素从重复衰减恢复为仅一次0.5。原有分数缩放居中测试曾发现约0.5物理像素偏移；改为终态直接绘制后，原0.05像素门槛通过。下划线像素夹具因相邻岛阴影混色而失败时，隔开夹具，未放宽颜色断言。

真实模型 `auto/deepseek-v4.1-flash` 返回HTTP200、4.275秒、finish_reason=stop；生成的数据只写任务 scratchpad。该夹具在真实组件中完成摘要打开、允许一次、停止各1次，草稿保留；该耗时是网关整次响应，不能当首字或用户呈现延迟。


## 原生性能与文件实测（2026-11-04）

实测为 Windows、Ryzen 5 5600X（6核12线程）、120 DPI／125%、窗口所在显示器240 Hz；780×920 DIP内容对应975×1150像素。Go默认优化构建，保留调试符号；验证宿主另含automation与帧日志。观察到GPU绘制路径，未用设备枚举推断具体适配器。以下仅覆盖本UI进程，桥接器是验证工具，远程模型不在资源范围内。

| 指标 | 实测 | 预定门槛／结论 |
|---|---|---|
| 200次输入事件→完整capture返回 | P50 23.8535、P95 35.6520、P99 40.1603、最大45.9818 ms | P95≤100、P99≤200；代理口径通过，包含IPC与PNG，不是物理键盘→呈现 |
| 本次首进程到可capture | 676.9518 ms | 缓存条件未清空，不称系统冷启动 |
| 30次缓存后新进程启动 | P50 599.5545、P95 912.5245、最大969.5398 ms；30次退出码均0 | P95≤1s通过 |
| 动画第1遍／12s | 1641帧；P95 0.7、P99 1.1、最大5.7 ms | 应用产帧P95≤10ms通过 |
| 动画第2遍／12s | 1638帧；P95 0.6、P99 1.1、最大3.1 ms | 同上 |
| 动画第3遍／12s | 1588帧；P95 1.0、P99 1.6、最大13.1 ms | 同上；三遍均无>100ms帧 |
| 整段5695帧 | P95 1.1 ms；启动首帧235.9 ms，启动后无>100ms帧 | 启动帧单列且保留 |
| 600个一秒空闲样本 | 单核CPU平均0.07475%、P95 0%、最大15.4483% | 平均≤0.5%、P95≤2%通过；单秒尖峰保留 |
| 动画三遍CPU平均 | 单核47.8670%／9.4760%／4.8577% | 三遍均报告，差异未归因；不是纯文本流式场景，未验收该场景25%预算 |
| 空闲结束内存 | PrivateUsage 134.7461 MiB，Working Set 101.0664 MiB | 空闲私有≤256MiB通过 |
| 访问过程采样最大内存 | 私有228.8242 MiB，工作集193.8477 MiB；OS记录工作集峰值263.5078 MiB | 采样最大值与OS峰值分别报告，未测私有提交真实峰值 |

`native-report.json` 保留逐次启动、200次输入、600个空闲样本、内存与CPU；`native-main.log` 保留启动长帧和三遍活动区间，`verification-summary.json` 按预先门槛判定通过。采集脚本修正Windows PowerShell的UTF-8 BOM、进程句柄/退出码读取与GetWindowRect的DPI虚拟化后，保留已完成的输入/动画/空闲原始样本，再补齐30次热启动和十五页完整物理窗口截图。原先被虚拟化裁小的截图仍留作诊断，最终完整外框使用 `native-physical-*-window.png`。

原生独立Dropdown经过3次真实指针动作：打开4项Radio菜单、点击第二个重复标签得到ID b、再次打开验证只有第二项checked；Escape恢复入口焦点。声音画廊还实际输入非法音量，显示错误而不请求播放，再输入37验证gain=0.37恢复；详情见 `native-popup-report.json`、`native-report.json`。Collapse非零预览时辅助树仍出现1个预览内字段，确认CTL-COLLAPSE-ACCESSIBILITY限制。

产物全部在任务 `scratchpad/ctl-impl`：常规画廊exe 20,656,640字节、验证宿主exe 21,034,496字节；333张PNG共12,269,258字节，163份log共2,321,689字节。`artifact-inventory.json` 记录验收目录快照640个文件、114,191,326逻辑字节（包括测试二进制、编造生成器输入和脚本）；这些验证产物不随库分发。库组件运行不产生文件。实际呈现错失比例、物理输入延迟、独立60/120Hz专场、GPU显存、72h常驻、原生IME候选窗及音频设备仍未验收；官方截图叠图仍缺输入。

