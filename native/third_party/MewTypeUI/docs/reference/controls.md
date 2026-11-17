# 通用控件规格索引与 theme 登记清单

ctl-spec 画法规格已由 ctl-impl 按净室边界实现，API 与验证见 [controls.md](../controls.md)。规格来源基线：tdesktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`、lib_ui `1ff7be77d578aaf8ad17d2fa6e51bae1ee567c1b`，与 [版本登记](README.md) 一致。T/=tdesktop/Telegram/SourceFiles/，L/=lib_ui/ui/；全部几何100%DIP、文字用实际行高/ascent、颜色取已登记浅/暗palette，不以64Gram为依据。实现只读规格、本仓库与 MIT 参考，未打开上游 C++ 或复制素材。

## 覆盖与入口

| 本次文件（paint/） | 覆盖状态与边界 | 来源/复用 |
|---|---|---|
| [round-button](paint/round-button.md) | Primary/Light、natural/fullWidth/fullRadius/四角、图标/数字后缀、hover/down/disabled、ripple | L/widgets/buttons.cpp:394-614；设置行仍引用settings-button |
| [link-button](paint/link-button.md) | 文字自然框、hover下划线、截断、禁用/键盘；无ripple | L/widgets/buttons.cpp:63-119 |
| [icon-button](paint/icon-button.md) | 独立40方框、Info返回/关闭、圆形ripple、hover/press/forceRippled、禁用 | L/widgets/buttons.cpp:618-723；T/info/info.style:138-166 |
| [input-field](paint/input-field.md) | 单行、普通多行自增高55…148、内部滚动、浮动占位/焦点线/错误、readOnly/disabled/IME | L/widgets/fields/input_field.cpp:2835-3004；外观引用numeric-input |
| [dropdown](paint/dropdown.md) | 设置行/独立入口、当前ID/高亮分离、空/禁用、短原生菜单/显式搜索Box、关闭焦点 | L/widgets/menu/menu_action.cpp:51-195；引用overlay与choose-box |
| [settings-slider](paint/settings-slider.md) | 普通/设置配色、分段宽度、按下拖动预览、释放时序、窄宽/键盘/禁用 | T/ui/widgets/discrete_sliders.cpp:193-519；数值Slider仍引用numeric-input |
| [tabs](paint/tabs.md) | defaultTabsSlider下划线、Info Profile岛状两变体；active/press/横拖/横滚/竖滚保护/空/禁用 | L/widgets/widgets.style:1192-1207；T/info/profile/tabs/info_profile_tabs_strip.cpp:79-529 |
| [side-categories](paint/side-categories.md) | 分类ID、普通/选中/hover/disabled、分组/滚动/窄布局返回/焦点 | 官方设置入口T/settings/sections/settings_main.cpp:364-449；常驻侧栏为我定的组合 |
| [page-title](paint/page-title.md) | 单/双标题、返回/关闭/尾badge/多动作、54/56高、内嵌标题、模式显隐/长字 | T/info/info_top_bar.cpp:79-149,395-443；小节标题仍引用settings-section |
| [badge](paint/badge.md) | NewBadge文字标签、Neutral/Active/Success/Warning/Error/Pending、截断、数字/muted/active | T/ui/new_badges.cpp:52-125；数字计数引用dialogs |
| [collapse](paint/collapse.md) | 初始开/关、150ms线性、双方向、最小预览、反向/动态测高、隐藏焦点与身份 | L/wrap/slide_wrap.cpp:55-169 |
| [approval-card](paint/approval-card.md) | 详情展开、允许一次/拒绝、Awaiting/Submitting/Failed/Resolved/Expired、窄宽 | Box+bot内联按钮衍生；CTL-APPROVAL |
| [forward-summary](paint/forward-summary.md) | 标题2行、摘要总3行、count、Ready/Loading/Failed/Disabled、只读/打开/重试 | WebPage预览衍生；CTL-FORWARD |
| [stop-generation](paint/stop-generation.md) | Send↔Stop、Generating/Stopping/StopFailed/Stopped/Completed、草稿保留 | 官方SendButton::Stop14方块；AI语义CTL-STOP |
| [notification-sound](paint/notification-sound.md) | 通知资格/静音、音量继承、OS所有权、合并、试听、资源接口/副作用 | 官方notifications_manager/Audio::Track；通用宿主接口CTL-SOUND |

已有设置规格不重写：[分段/说明](paint/settings-section.md)、[行按钮](paint/settings-button.md)、[开关](paint/settings-toggle.md)、[Radio/Check](paint/radio-check.md)、[可搜索分组选择](paint/choose-box.md)、[数值输入/MediaSlider](paint/numeric-input.md)。这些已由settings包实现；其旧“theme要补”已在本分支生成表登记。本次普通多行不能套compose的36/224、SettingsSlider不能套数值MediaSlider、独立NewBadge不能套未读19高。

## 参考了谁、借什么、为什么

官方lib_ui/tdesktop：按钮命中框/内容/ripple分开；输入外围与原生编辑器分工；段宽以真实字宽协商；Info标签保留ID并独立管理页面；SlideWrap只改高度；通知先判资格再播、OS与应用选择一个声音所有者。各项源码行号写在对应规格，供后续净室实现只读规格。

CrychicUI：读取 `button/icon_button.go:10-27` 和 `navigation/vertical_tabs.go:10-41`，只借ButtonBase交互根、必填可访问名称、选择状态与内容分离的API思路；不用其下标身份、Snappy、点击声音、任意边框和旧尺寸。mygo使用任务指定的mygo-maintenance规则，持久数据/Handle不存Element，构建与paint无外部副作用。本次未借入MIT实现，不需新增THIRD_PARTY_NOTICES代码来源条目。

## theme 已实现总表

下表已登记进 metrics.tsv、preset 字阶/动效与生成访问器；当前JSON v9提供全部键。控件原有83个尺寸/比例、2个描边色、2个字阶与9个动效角色保留，另有74个共用图形指标；来源分别登记于theme-metrics/theme-colors/theme-behavior。符号后的省略字段共用行首前缀。

| 类型 / 键组 | 默认值 | 来源与用途 |
|---|---|---|
| button.height/horizontalExtra/textTop/numbersGap | 34/34/8/7 | L/widgets/widgets.style:726-732；RoundButton |
| button.paddingLeft/Top/Right/Bottom | 0/0/0/0 | L/widgets/widgets.style:730 |
| motion.buttonNumbers | 150ms ease-out-circ | L/effects/numbers_animation.cpp:20,108-112,134；L/basic.style:100；后缀宽度和字形位移共用进度 |
| iconButton.width/height/rippleSize/rippleLeft/rippleTop | 40/40/40/0/0 | L/widgets/widgets.style:1487-1497 |
| segments.height/padding/barTop/gap/barHeight/radius/labelTop/strictSkip | 39/0/5/3/3/1/17/0 | L/widgets/widgets.style:1177-1189；strictSkip未覆写，T/ui/widgets/discrete_sliders.cpp:358-366 |
| segments.outerLeft/Top/Right/Bottom | 21/7/21/4 | T/settings/settings.style:91 |
| tabs.height/barTop/gap/barStroke/radius/labelTop/rippleBottomSkip | 43/40/0/6/2/12/1 | L/widgets/widgets.style:1193-1204；可见下划线高由43−40推导 |
| profileTabs.height/marginLeft/Top/Right/Bottom/tabPadding/activeInset | 48/8/6/8/6/16/4 | T/info/info.style:719-723 |
| profileTabs.shadowBlur/shadowY/shadowOpacity | 8/2/.18 | T/info/info.style:735-737 |
| profileTabs.lightStrokeRatio/lightStrokeAlpha/darkTopAlpha/darkMidAlpha/darkBottomAlpha/lightnessThreshold | .6、20/255、40/255、0、20/255、128 | T/info/profile/tabs/info_profile_tabs_strip.cpp:34-42；theme新用途参数，不留组件常量 |
| profileTabs.fadeFactor | .5 | 同文件:515；替代渐变宽复用tabPadding16，CTL-TABS |
| pageTitle.height/left/top/textMaxHeight | 54/24/17/20 | T/info/info.style:138,153,237 |
| pageTitle.subtitleLeft/titleWithSubtitleTop/subtitleTop/badgeGap | 16/8/28/8 | T/info/info.style:241,246,250 |
| pageTitle.backWidth/backIconLeft/backRippleSize/backRippleLeft/backRippleTop | 60/11/42/6/6 | T/info/info.style:140,145,147-148 |
| pageTitle.closeWidth/closeIconLeft/closeRippleLeft | 48/5/0 | T/info/info.style:164-166 |
| pageTitle.layerHeight/layerTitleTop/layerSubtitleTop/hiddenScale | 56/9/30/.7 | T/info/info.style:269,360-361,264；可见缩放由快照公式推导 |
| badge.textPaddingLeft/Top/Right/Bottom/textRadius/afterLabelX/afterLabelY | 4/1/4/1/4/4/1 | T/settings/settings.style:160-161；T/ui/new_badges.cpp:69-70 |
| approval.actionHeight/actionGap/actionPadding/actionTextTop | 36/2/10/8 | T/ui/chat/chat.style:282-285；用途组合CTL-APPROVAL |
| forwardSummary.maxSummaryLines | 3 | 我定的；网页5总行/2标题作参照，T/history/view/media/history_view_web_page.cpp:805-807 |
| compose.stopSize/stopRadius | 14/3 | T/chat_helpers/chat_helpers.style:1538-1539 |
| typography.pageTitle/statusBadge | 14/600、10/600 | T/info/info.style:155；T/settings/settings.style:156 |
| motion.segments/tabs/profileTabs/profileTabsScroll/collapse/pageTitle | 150 linear / 150 linear / 200 Quint / 150 Circ / 150 linear / 150 linear | 各规格末尾的实际动画调用点；collapse不得直接复用现有ease-out slide |
| motion.profileTabsWheelGrace/profileTabsWheelRest | 250/1000ms定时器 | T/info/profile/tabs/info_profile_tabs_strip.cpp:25-27；非插值曲线 |
| colors.profileTabs.outlineLight/outlineDark | 黑RGB、白RGB；alpha由上述比例/暗渐变控制 | 同文件:34-43；我定的用途命名，源值不可硬写进组件 |

已存在、必须复用：input.*与inputField动效、dialog/choose/settings/choice几何、13/14/16已有字阶（上表新用途除外）、RoundButton圆角dialog.buttonRadius4、ripple、composeButton120/swapScale5、入出站quote/preview色、semantic角色。分类、Dropdown、LinkButton、普通输入、提示音不新增独立数值族；音量/静音/500ms通知合并不进入theme。派生H/2、padding求和、渐变中点.5和插值端点不重复存成常量。

## 例外与验收边界

新增例外均标“我定的”：CTL-COMPOSITION、CTL-ICON、CTL-INPUT、CTL-SELECT、CTL-SEGMENTS、CTL-TABS、CTL-CATEGORIES、CTL-TITLE、CTL-BADGE、CTL-COLLAPSE，以及明确要求的CTL-APPROVAL/CTL-FORWARD/CTL-STOP/CTL-SOUND；完整理由在 [exceptions](exceptions.md#通用控件与宿主扩展画法规格ctl-spec)。采用已有SET-DISABLED/SET-INPUT-PLATFORM/SET-ROW-ARROW的地方直接引用，未悄悄移除旧差异。

每页“后续验收输入”是实现验收条件：ui.NewTester 实测与实际点按默认≤1 DIP、100/125/150%、明暗/RTL；原生IME/字体/OS声音另验。ctl-spec 阶段只核对文档，以下保留当时的来源核验记录；ctl-impl 的 Go 测试、逐条改坏、画廊和性能数据集中在 [实现验收](../controls.md#画廊与验证入口)，不以文档核验冒充 UI 证据。

本次实际核验：15份paint规格与3份参考页的118个相对链接/锚点、252处显式T/L源码引用（52个文件）的路径及行号范围、15项关键几何算式和已存在/待补token抽查均通过。行号范围有效不等于每个源码事实已被机器语义证明，声明值仍以逐段阅读为依据。真实模型辅助：DeepSeek请求HTTP200但finish_reason=length、无最终正文，未算通过；按任务切换agnes-ai/agnes-2.5-flash，HTTP200、5.009秒、finish_reason=stop，返回标题快照比例.76+.24a和中点.88，与算式一致。模型只核对代数，不替代源码、UI或音频验收。

替换盘点中的日期/时间段、列表字段、记忆页Empty/Error/Skeleton/Audit等不在本任务点名的控件规格范围；本页未把它们冒称已补齐。全部15项的库API、theme补值与画廊现已实现；Collapse 动画期和非零预览的辅助技术隔离受 mygo 公开API限制，未按通过计，见 CTL-COLLAPSE-ACCESSIBILITY。官方客户端截图叠图仍需宿主提供同版本参考图；未获得该输入前，保留字体/素材差异，不声称完成官方逐像素验收。
