# Telegram 对照表

本目录记录 MewTypeUI 每个数值对应 Telegram 桌面版源码的哪一处。

对照的源码版本：

- tdesktop `250fc73`（2026-11-02），本机 `<Telegram 源码目录>\tdesktop`
- lib_ui `1ff7be7`（2026-10-30，tdesktop 锁定的版本），本机 `<Telegram 源码目录>\lib_ui`

版本变了在这里记一笔，并重核受影响的数值。

theme-colors.md记录全部586个palette角色和28个用途映射；theme-metrics.md记录806个数值；theme-behavior.md记录34个字阶、74个动效与材质。layout.md记录窗口断点、缓存页面滑动与宿主扩窗；dialogs.md记录会话列表、文件夹切换及故事/角标/搜索范围；chat.md记录消息分组、可见区头像、快照飞行、尾巴量差及消息底部与发送；content.md记录正文、回应、媒体颜色、粒子生成、裁切与进度追赶；overlay.md记录原生flyout菜单、tooltip和独立全屏查看器；compose.md记录编辑器、确认预览、默认Picker停靠、中英文字和IME边界。设置页 API、九页画廊与宿主合同见 [settings.md](../settings.md)，十五页通用控件及审批/摘要/生成停止/提示音接口见 [controls.md](../controls.md)。exceptions.md保留实现和验证边界。完整主题快照使用JSON v9，旧v1–v8拒绝。

[centering.md](centering.md) 记录 100% / 125% / 150% 的居中与抗锯齿改前改后实测、mygo 取整链、Telegram 绘制参照、回归/改坏检查、画廊及性能边界。

[icon-glyph.md](icon-glyph.md) 盘点全部替代图形、原PNG画布与实际亮度包围盒；[图形规格](paint/icon-glyph.md) 定义画布不变、图形限框和线宽。`icons/glyphs` 原生页覆盖宿主Lucide导航、普通文件夹、徽章/锁定、顶栏与输入区。

[fidelity-2026-11-03.md](fidelity-2026-11-03.md) 记录十四张指定本地截图的第一轮区域量差、前二十处偏差、组件/规格归因与改前=改后基线。画廊支持选图、125%换算、半透明/差异叠图及偏移缩放；`cmd/fidelity` 输出本地数值和诊断图。[paint/fidelity-coverage.md](paint/fidelity-coverage.md) 登记完整画法规格及组件的缺口，不把截图配置差异直接称为官方实现错误。

设置页净室规格：[分段](paint/settings-section.md)、[行按钮](paint/settings-button.md)、[开关](paint/settings-toggle.md)、[单选/多选](paint/radio-check.md)、[可搜索分组选择框](paint/choose-box.md)、[数字输入与数值滑块](paint/numeric-input.md)。set-impl已按这些规格实现settings包；每页末尾theme缺项已录入生成表，设置扩展和素材/平台差异见[exceptions.md的设置页登记](exceptions.md#设置页画法规格set-spec)。实现阶段没有重新读取外部GPL源码；固定版本来源沿用规格，全部几何为100%逻辑像素，即DIP。

[paint/message-bottom.md](paint/message-bottom.md) 是消息时间、浏览/回复数、编辑标记、收据和回应的净室画法规格。a2-impl已按现状不符清单实现共享尾部占位、分项底部信息、固定22 DIP回应、图片时间与外部回应；末尾列实现登记与「待补」，其中官方截图叠图仍缺输入，不能以几何测试代替。

[paint/reaction-size.md](paint/reaction-size.md) 补全回应普通位图、完整中心画布、自定义帧和Unicode替代的尺寸/定位合同，说明32画布与16实际资源、18槽和20自定义帧的关系；数字仍为13 semibold。新尺寸均由已有theme值推导，无新增快照字段。

[paint/chat-identity.md](paint/chat-identity.md) 记录聊天首字头像、位图回退、可访问名字、宿主头像颜色、逐消息发送人名字颜色及组尾/视口定位；独立messageAvatar字阶与通用控件字阶共同纳入JSON v8。参考了Telegram圆形/居中文字和CrychicUI的宿主资源边界，未复制实现或素材。

## paint-core 组件进度

状态「规格 / 实现 / 审过」只指本轮完整画法合同的进度；既有组件代码不自动算按新规格实现。审过列明确区分规格审查与实现审查。

| 组件 | 规格 | 实现 | 审过 |
|---|---|---|---|
| [dialogs：会话行、搜索顶栏、rail含54高主菜单](paint/dialogs.md) | 已写；50张@1x图形包围盒、6种SVG尺寸测量；31项公式、163处源码范围/链接检查通过 | 待按规格核对/修改 | 规格：Sol xhigh第1轮FAIL的6项已修，第2轮PASS（2026-11-04）；未作UI验收 |
| [chat：气泡、引用、转发、日期与服务消息](paint/chat.md) | 已写；11张@1x图形包围盒；53项公式、146处源码范围/链接检查通过 | 待按规格核对/修改 | 规格：Sol xhigh第1轮问题已修，第2轮PASS（2026-11-04）；新增boost图形独立复测通过，未作UI验收 |
| [icon-aspect：全部替代图形等比缩放](paint/icon-glyph.md) | 已写；单一比例、原图墨迹约束、独立线宽、完整墨迹视口和等比采样边界 | 已实现；60素材、116条绘制路径均≤2%，新增比例/布局/限框断言先红后绿；全画廊亮暗、DPI120原生对比与全部检查通过 | Sol xhigh三轮；第三轮细描边采样意见已修，定向复核PASS（2026-11-04） |


[controls.md](controls.md) 汇总ctl-spec新增的15份通用控件与宿主扩展画法规格、状态覆盖、来源和theme登记清单：Round/Link/Icon按钮、普通输入、Dropdown、SettingsSlider、两类tabs、侧边分类、页面标题、Badge、Collapse，以及审批卡、合并转发摘要、停止生成、提示音接口。ctl-impl 已按规格实现，已有settings规格直接引用。“我定的”集中登记在exceptions的ctl-spec节；实现未打开外部 GPL 源码。

mw-gaps2 的五项聊天区补缺规格：[Viewer固有尺寸/双击/打开保护](paint/viewer.md)、[正文标准编辑菜单桥](paint/edit-menu.md)、[相册逐项状态与尺寸通知](paint/album-state.md)、[Tooltip键盘/悬停与退场](paint/tooltip.md)、[Video显式播放标记](paint/video.md)。沿用JSON v8及原有尺寸/字阶/动效；相册44/3 DIP在theme-metrics登记额外官方用途。编辑桥采用原缺口建议的标准ui.Menu对象方案，系统菜单差异列在exceptions。

mw-gaps2 实测：CGO_ENABLED=0全库、CGO_ENABLED=1的content/gallery/overlay/theme及Go vet均退出0；每条重命令单独使用共享锁与Idle优先级。五页明暗各一轮原生启动/交互，18张DPI 120实际桌面BitBlt截图；状态文案修复后另跑明暗相册，增加4张截图，22张共876182 bytes，原生进程均退出0。覆盖Viewer打开保护/双击/独立固有尺寸、菜单CopyData/全选/焦点、逐格下载取消重试、真实Tab/Esc/失焦后不重开、未知时长Video显式开关。Viewer/编辑桥/相册/Tooltip/Video分别11/6/12/18/7条断言逐条反转，新增状态文案2条，总计56/56捕获；亮图回归修复前失败、修复后通过，覆盖430/240 DIP、普通/选中、单图/相册。规格内任务卡给出宿主状态归属，无新增设置面。

验收状态：任务走查、组件边界/可读性、键盘焦点、进退场/打断/ReduceMotion均有Tester或原生证据；官方同版本截图叠图未验证（缺参考截图），不以合成画廊替代。三轮Release共600次Tooltip键盘开关、完整应用帧/资源和文件大小见[overlay量测](overlay.md#mw-gaps2-原生键盘量测2026-11-04)；物理输入呈现、DWM错失率、长期空闲与GPU显存未验证，启动>100 ms帧原样保留。

实际调用Magpie的auto/deepseek-v4.1-flash（视觉）与agnes-ai/agnes-2.5-flash（API复核）作为辅助。API歧义已在使用契约说明；最后视觉意见仍称进度文字重叠，实际截图中胶囊位于圆环下方且有空隙。Video顶部旧样例被裁切是验收脚本为显示两个新增开关状态而主动滚动的视口结果；两个目标均完整可见。角标沿用现行theme尺寸，官方叠图差异仍未验证。原始模型响应、逐次量测、PNG、断言反转与显示缩放恢复记录仅保留任务scratchpad/mw-gaps2。

暗色来源是同一 tdesktop 版本的 Telegram/Resources/night.tdesktop-theme 中 colors.tdesktop-theme：467 个声明，其中 447 个覆盖当前 palette、139 个当前角色继承合并表、20 个旧声明不公开。背景图片不使用。对照表由 `go run ./internal/cmd/themegen` 生成；可用 `-sources` 指定两个源码仓库的父目录。

本轮details-a验证：全库测试与mygo生命周期检查通过；新增38条断言语句逐一扰动均变红，含裁切表的46条参数路径；目的快照释放回归另有修复前失败记录。明暗原生共16场景，保存20张离屏快照和2张实际桌面抓图。离屏capture与滚动后的GPU画面存在位置差异，显示验收以实际桌面抓图为准；粒子批次、应用帧、进程资源和25Hz固定到达采样随任务记录交付，不把控件树耗时当物理输入到呈现。尚未验收的项目和实现差异见exceptions.md。

details-b合并验收：在共享重任务锁与Idle优先级下通过CGO_ENABLED=0全库测试、Go vet、mygo v0.7.4 vet和原生画廊构建。明暗主题快照往返及59个Motion访问器逐项运行验证通过；dialogs/folders与layout/chat各完成明暗原生启动及本进程窗口PrintWindow截图（96 DPI）。合并仅协调主题契约、状态字段和文档，没有新增组件功能。

mw-merge2合并验收：共同494项数值一致，settings新增130项与消息底部新增25项的完整登记行均保留，无重复键；主题仍为JSON v6，合计649个数值、31个字阶、65个动效。共享重任务锁与Idle优先级下，全库CGO_ENABLED=0测试通过9个测试包、139个顶层测试（含子测试365项），无跳过；Go vet通过。当前环境未注册`go tool mygo`，改用锁定版本的`go run github.com/egoist/mygo/cmd/mygo@v0.7.4 vet .`通过，`.`为应用目录，命令内部检查`./...`。明暗主题分别验证全部649个默认数值与649个独立覆盖值、31个字阶和65个动效访问器、完整快照往返及两路缺项拒绝；新增快照总数断言先将649故意改为648确认失败，再恢复并通过全库测试。本次没有新增组件行为或原生视觉验收。

mw-merge3合并契约：将main 合入fidelity，保留全部612/614色、649/732数值、32/33字阶、65/74动效的并集；共同612色、649数值、31字阶、65动效的默认值一致，数值完整来源行也一致，无重复键。MessageAvatar保留索引31，PageTitle/StatusBadge顺延到32/33；最终JSON v8包含614色、732数值、34字阶、74动效。唯一版本门在Compile，Import复用该门；两路不同v7均拒绝，不做隐式迁移。现行API说明与示例同步v8，旧批次验收记录保留当时版本。

mw-merge3验收：每条重命令独立拿共享锁、Idle优先级运行。CGO_ENABLED=0全库12个测试包、CGO_ENABLED=1的chat/theme/gallery、Go vet、固定mygo v0.7.4 vet全部退出0。明暗各运行614色/732数值/34字阶/74动效的独立覆盖与全部typed访问器、父分支默认值核对、完整快照往返和canonical字节稳定性；按两路登记表构造的完整v7形状均在Import拒绝。v8浅/暗导出为63832/63831字节，仅放任务scratchpad；仓库没有静态快照JSON。Go overlay临时放行v7时，新增拒绝案例按预期退出1；移除overlay后的上述全库测试通过。完整生成器再次运行以及-recorded均逐字节复现两个生成Go文件和两个来源表，退出0。

mw-merge3原生补验：CGO_ENABLED=1画廊构建退出0；chat/identity、controls/title、controls/badge各完成明暗新进程启动、正尺寸同名窗口PrintWindow捕获和正常关闭，六个进程均退出0。DPI120，外框图像均1018×970物理像素；已核对首字、位图、逐消息名字色及14/10 DIP用途字阶所在页面成像。启动至捕获533.6972–664.6731ms，采样CPU312.5–468.75ms，工作集最大84754432字节、私有提交最大127250432字节；只是六次启动烟测，不是交互延迟、稳定空闲或官方叠图性能验收。exe为20793344字节，六张PNG合计272509字节，仅存任务scratchpad。首轮采集脚本Bitmap构造失败，修正同名窗口筛选后完成；未据此修改库组件或放宽验收。

ui-int 整合验收（2026-11-04）：基于 main b702f1d 合入 feat/perf-body 与 feat/gaps2。chat/body.go 的导入同时保留主线图形校准与 Unicode 分词，变更记录保留两路条目；生成文件未手合。metrics-only 连续生成两次，806 项指标与来源表的 SHA256 各自相同。CGO_ENABLED=0 全库测试、Go vet、固定 mygo v0.7.4 分析器均退出 0。Sumika 使用临时 modfile 指向本整合库，编译、根包与 internal/archcheck 短测试、同范围 vet 及 mygo 分析均退出 0；复制、选区、链接和前插消费者回归通过。独立合成原生窗口实际触发正文链接 Tooltip，退出 0；未复测物理呈现和滚动性能，不改变既有超预算结论。
