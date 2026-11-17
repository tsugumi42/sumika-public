# 数值类设置与数字输入画法规格

版本、DIP、净室边界见 [settings-section](settings-section.md#坐标来源与边界)。`T/`=tdesktop/Telegram/SourceFiles/，`L/`=lib_ui/ui/。查证结论：Telegram有数字文本输入，代理设置的端口使用NumberInput；自动下载大小使用带右值行+MediaSlider；缓存上限也使用离散MediaSlider。没有在这些设置调用中发现一个统一的“左标题、右边直接编辑任意整数、带加减箭头”的设置行。不能把“下载用滑块”写成“Telegram没有数值输入”。依据 `T/boxes/connection_box.cpp:1666-1696`、`T/boxes/auto_download_box.cpp:136-175`、`T/settings/sections/settings_local_storage.cpp:1955-1993`。

参考官方的两条选择：小范围可列举档位用滑块并实时显示格式化值；需要精确键入的端口用普通下划线输入框，提交校验归具体设置。模型上下文数值采用哪一种由宿主选择，不将网络字节档位硬套为token数或秒数。

## 数字文本框：真实设置调用

高级设置的连接入口打开代理管理，代理编辑框在地址行右侧放端口，最大65535；空端口保存时显示错误并聚焦。来源 `T/settings/sections/settings_advanced.cpp:125-135,1423-1433`、`T/boxes/connection_box.cpp:1592-1615,1666-1696`。这是设置弹框中的真实数字字段，不是截图猜测。

| 样式项 | Telegram常量、值 | 来源 |
|---|---|---|
| 端口宽 / 地址初始宽 | connectionPortInputField.width=55 / connectionHostInputField.width=160 DIP；地址实际宽随容器变化 | `T/boxes/connection_box.style:10-15`；`T/boxes/connection_box.cpp:1689-1695` |
| 地址行外边距 / 字段间距 | proxyEditInputPadding=(22,0,22,0)，proxyEditSkip=16 DIP | `T/boxes/connection_box.style:45-46` |
| defaultInputField | heightMin55、heightMax148、widthMin64；端口显式width55，不被“64通用最小”改成64 | `L/widgets/widgets.style:1058-1060`；`L/widgets/fields/masked_input_field.cpp:59-70` |
| 文字 | 14 regular，textMargins=(0,28,0,4)，左上对齐 | `L/widgets/widgets.style:1033-1034,1024-1025,1055`；`L/basic.style:56` |
| 占位 | 14 semibold，缩小到.9，向上20，无额外margins | `L/widgets/widgets.style:1036-1043` |
| 边框 | 仅下划线，普通1 / active2 DIP，radius0 | 同文件 `:1046-1053` |
| 过渡 | duration150ms，linear | 同文件 `:1044`；`L/widgets/fields/masked_input_field.cpp:237-251,269-274,335-338,370-374`；`L/effects/animations.h:67` |
| 颜色 | textBg=windowBg、textFg=windowFg；border=inputBorderFg；active=activeLineFg；error=activeLineFgError | `L/widgets/widgets.style:1028-1048` |

浅色输入普通/active/error线分别#e0e0e0/#37a1de/#e48383；占位普通/active/error=#999999/#168acd/#d14e4e。暗色按同名palette，已有 `control.border/focus/activeLineForegroundError`、`window.mutedText/activeTextForeground`、`control.dangerForeground`；来源 `L/colors.palette:14,20,39-40,48,75`、[颜色表](../theme-colors.md)。不得把错误下划线#e48383换成错误占位#d14e4e。

## 字段矩形与绘制

地址行宽W；内容 `(22,0,W-44,55)`，端口框 `(W-22-55,0,55,55)`，地址框 `(22,0,W-44-55-16,55)`。字段自身不再叠22。来源 `T/boxes/connection_box.cpp:1673-1696`。

单个字段局部 `(0,0,w,55)`：背景填windowBg；普通下线 `(0,54,w,1)`。文字名义内容矩形 `(0,28,w,23)`，其中23=55−28−4，使用实际14字体度量，横向过长保留编辑光标可视的水平滚动，不做Label省略。Qt去掉原生frame并用contentsMargins减(2,1,2,1)补偿QLineEdit内置余量；该补偿不能在mygo再直接照搬一遍。来源 `L/widgets/fields/masked_input_field.cpp:68-71,97-106,142-154,223-224`。本库对文字行顶/ascent的映射与原生选择/光标差异登记 [SET-INPUT-PLATFORM](../exceptions.md#set-input-platform)，不宣称仅凭55高已逐像素一致。

焦点激活下线从鼠标x（键盘焦点则w/2）向两边铺开；令线性进度a、起点p被夹在[0,w]，左沿 `round(p(1-a))`，右沿 `p+round((w-p)a)`，高2，顶53；完全展开为全宽。失焦且无错误时opacity150ms由1→0；失焦但仍error保持线。已在淡出时重新聚焦则改做opacity恢复。error颜色按另一条150ms线性进度从active线色→error线色。来源 `L/widgets/fields/masked_input_field.cpp:156-172,237-259,269-280,335-339`。

占位移起条件：聚焦或非空（这个样式scale>0），不只是非空。a驱动位置y从28移到8，比例从1变.9；颜色按focus进度从普通到active，再按error进度向错误色混合。占位使用填充字形路径、高质量抗锯齿，不画边框盒。来源 `L/widgets/fields/masked_input_field.cpp:176-199,370-379`。清空但仍聚焦保持上浮；清空且失焦才回原处。hover无独立底色、无ripple、无spinner或加减按钮。来源同文件 `:148-224`。

## 数字编辑与提交边界

NumberInput继承MaskedInputField，提示系统只接受数字、关闭预测；编辑纠正先保留Unicode isDigit字符、移除其他字符并修正光标，再按Qt的有符号int解析。解析结果为0或解析失败会清空；超过正limit时恢复上次整段文字及光标，不把值夹成limit；空串是编辑中间态。初始构造检查可解析且非零和上限，但不负责通用下限。来源 `L/widgets/fields/number_input.cpp:11-51`。因此没有可直接用于“允许0、负数、小数或超int范围”的现成合同；范围和单位不能由绘制组件猜测。

| 输入场景（端口limit65535） | 源码结果 | 来源 |
|---|---|---|
| 输入/粘贴12a3 | 移除a得到123，光标随删除修正 | `L/widgets/fields/number_input.cpp:34-43` |
| 输入0、清空、不可解析的大整数 | 清空；不显示字面量0 | 同文件 `:44-46` |
| 65535后追加一位 | 恢复原文字与光标 | 同文件 `:47-49` |
| 文本带负号 | 编辑纠正删符号，不能据此声称支持负数 | 同文件 `:37-42` |
| 空端口点击保存 | showError并聚焦；不自动填80/443 | `T/boxes/connection_box.cpp:1592-1615`；`L/widgets/fields/masked_input_field.cpp:320-324` |
| Enter / Escape | 发submitted / cancelled；具体保存/关闭由宿主绑定 | `L/widgets/fields/masked_input_field.cpp:391-417` |

不强加step、滚轮增减、上下键计数器：该NumberInput没有这些实现。输入中的临时空值、业务下限/上限、回退默认、持久化和单位格式全部属于宿主状态，组件不读配置文件。若模型上下文允许0表示自动，必须使用 [SET-NUMBER-ROW](../exceptions.md#set-number-row) 的显式扩展，不能照NumberInput悄悄把0清掉。

## 自动下载大小：右值行加离散滑块

上方只读值行采用autoDownloadLimitButton：继承settingsButtonNoIcon但padding=(22,10,22,0)，行高30，标题14 regular，右值按settingsButtonRightSkip=23定位、默认蓝色；该行设鼠标穿透，不是点击弹数字编辑框。值格式为“最多 N MB”，MB单位按1024×1024字节。下方slider外边距(22,8,22,8)，控件高15，因此这一slider布局块高31；相邻值行+滑块块合计61。来源 `T/settings/settings.style:313-315`、`T/boxes/boxes.style:164-167`、`T/boxes/auto_download_box.cpp:33-35,150-174`。默认下载10MiB、自动播放50MiB是业务初值，非主题token。

| 滑块样式项 | 数值 | 来源 |
|---|---|---|
| defaultContinuousSlider.width | 3 DIP轨道高；绘制radius用整除3/2=1 | `L/widgets/widgets.style:1211`；`T/ui/widgets/continuous_sliders.cpp:350-352` |
| autoDownloadLimitSlider.seekSize | 15×15 DIP；常驻圆钮 | `T/boxes/boxes.style:164-165`；`T/ui/widgets/continuous_sliders.h:184` |
| enabled active/inactive / hover | mediaPlayerActiveFg / mediaPlayerInactiveFg，两态hover角色同值 | `L/widgets/widgets.style:1212-1215` |
| disabled active/inactive | mediaPlayerInactiveFg / windowBg；隐藏圆钮 | 同文件 `:1216-1217`；`T/ui/widgets/continuous_sliders.cpp:385-395,509-511` |
| hover duration | 150ms linear（此样式颜色相同、圆钮常驻，通常没有可见hover变形） | `L/widgets/widgets.style:1220`；`T/ui/widgets/continuous_sliders.cpp:212-217,275-276` |
| border | transparent、width0；无投影 | `L/widgets/widgets.style:1221-1222` |

以slider局部w×15、值u∈[0,1]：可拖中心范围使用seekRect=`(7,0,w-15,15)`，圆钮绘制左边 `round(7+u(w-15))-7`，顶0，尺寸15；轨道y=`floor((15-3)/2)=6`、高3。轨道active/inactive分界中点为 `round(7.5+u(w-15))`，注意原版轨道用半个15的浮点7.5，而seekRect用了整除7，二者取整差最多1 DIP；不能把所有点独立吸附再宣称完全同心。轨道分段各画带1半径的圆角矩形并在中点裁切，钮用椭圆，高质量抗锯齿。来源 `T/ui/widgets/continuous_sliders.cpp:50-55,367-395,408-438,454-480,509-528,549-565`。这里登记原始整除行为；宿主DIP栅格化仍只乘一次设备比例。

颜色浅/暗active=#40a7e3/#5288c1、inactive=#e1eaef/#364554，对应现有player.activeForeground/inactiveForeground。来源 `L/colors.palette:506-507`、[颜色表](../theme-colors.md)。不误用control.sliderBackgroundActive的暗色#4a81bc。

按下/拖动立即计算seekRect内u并夹[0,1]，再吸附最近档位；拖动中实时报告格式化值，释放报告结束并保存当前u，没有值缓动或弹簧。键盘方向键±.01、Page±.10，以足够多个小步推进到下一个不同档位；Home/End到两端。源码默认不启用滚轮，只有调用setMoveByWheel才有。来源 `T/ui/widgets/continuous_sliders.cpp:36-47,84-114,143-202`、`.h:195-206`。

实际下载提供100档，u=index/99；初始值落在两档间时选择第一个≥当前值的档位，并非总选最近。来源 `T/export/view/export_view_settings.h:28`、`T/ui/widgets/continuous_sliders.h:184-205`。档位表按固定版本整理如下，表格是值域规格而非程序翻译：

| 一基档号 | MiB值域 / 步长 |
|---|---|
| 1–10 | 1–10 / 1 |
| 11–30 | 12–50 / 2 |
| 31–40 | 55–100 / 5 |
| 41–60 | 110–300 / 10 |
| 61–70 | 320–500 / 20 |
| 71–80 | 550–1000 / 50 |
| 81–90 | 1100–2000 / 100 |
| 91–100 | 2200–4000 / 200 |

表来源 `T/export/view/export_view_settings.cpp:98-121`。这些是下载业务档位，不属于主题。缓存设置同样把标题和值显示在上方、MediaSlider伪离散绑定自定义表，来源 `T/settings/sections/settings_local_storage.cpp:1955-1993`；不将缓存或下载的范围写死进通用Slider。

## 模型页的数字行选择（我定的）

Telegram没有模型上下文设置这一业务。建议模型页沿用本组设置行显示当前格式化数值，点击后打开标准Box，在正文放一个全宽NumberInput外观的字段与说明，底部保存/取消；精确上下文数不宜强制吸附成100个网络下载档位。字段横向边距22，采用上述55高下划线外观；具体最小/最大、是否允许0、整数宽度、单位和错误文案由宿主显式交付。此为现有设置行+Box+输入框的组合，标 [SET-NUMBER-ROW](../exceptions.md#set-number-row)，不宣称Telegram有同款组件。允许0时保留“0”并交宿主验证，与NumberInput的清0行为明确分开；本任务不实现该扩展。

## 后续验收输入

数字字段：空/非空、聚焦/失焦、错误/解除、0、最大/超限、粘贴夹杂符号、超过int范围、光标位于中间时删字符、Enter/Escape；焦点线与占位0/75/150ms。滑块：首/末档、半档吸附边界、初值落两档间、拖出左右、键盘Home/End/Page、disabled钮消失；明暗、RTL、100/125/150%。原生文本选择/光标另验，不能用Label代替输入测试。本文无运行代码和UI测试结果。

## theme 要补的

| 建议键 | Telegram常量、值、来源 |
|---|---|
| input.numberPortWidth / hostInitialWidth / fieldGap | connectionPortInputField.width55 / connectionHostInputField.width160 / proxyEditSkip16；`T/boxes/connection_box.style:10-15,46` |
| input.paddingLeft/Right | proxyEditInputPadding=22/22；同文件 `:45` |
| input.heightMin/heightMax/widthMin | defaultInputField=55/148/64；`L/widgets/widgets.style:1058-1060`，端口显式55优先 |
| input.textLeft/Top/Right/Bottom | textMargins=0/28/0/4；同文件 `:1033` |
| input.placeholderScale / placeholderRise | .9 / 20（向上）；同文件 `:1041-1042` |
| typography.inputPlaceholder | 14/600；同文件 `:1043` |
| input.border / borderActive | 1/2；同文件 `:1050-1051`；radius0可复用现有compose.fieldRadius的来源，但推荐按本组件配置0 |
| motion.inputField | duration150ms linear；同文件 `:1044`、`L/widgets/fields/masked_input_field.cpp:237-251` |
| settings.limitPaddingLeft/Top/Right/Bottom | autoDownloadLimitButton.padding=22/10/22/0；`T/settings/settings.style:313-315` |
| settings.sliderPaddingLeft/Top/Right/Bottom | autoDownloadLimitPadding=22/8/22/8；`T/boxes/boxes.style:167` |
| slider.trackHeight / markerWidth/Height | defaultContinuousSlider.width3、autoDownloadLimitSlider.seekSize15/15；`L/widgets/widgets.style:1211`、`T/boxes/boxes.style:165` |
| motion.sliderHover | defaultContinuousSlider.duration150ms linear；`L/widgets/widgets.style:1220`、`T/ui/widgets/continuous_sliders.cpp:212-217` |

已有颜色全部可复用；正文dialogBody=14/400已有。placeholderRise用正20表达“向上”，因为现有theme指标拒绝负值（`theme/compile.go:155-162`）。宿主数字范围、100档业务表、0策略不进入theme；轨道半径1、输入内容高23、按钮块61等只按上述token推导。
