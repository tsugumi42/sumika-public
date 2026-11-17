# 设置页分段画法规格

set-spec 净室规格；只记录几何、画法、状态与行为，不摘录或译写 C++，不分发 Telegram 素材。实现者只读本目录规格。基线为 tdesktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`、lib_ui `1ff7be77d578aaf8ad17d2fa6e51bae1ee567c1b`，与 [版本登记](../README.md) 一致。

## 坐标、来源与边界

本组六页统一使用：`T/` = tdesktop/Telegram/SourceFiles/，`L/` = lib_ui/ui/，`I/` = lib_ui/icons/。引用中的路径加此前缀即完整来源；资源只登记尺寸及用途，不复制轮廓。矩形 `(x,y,w,h)` 的右/下边界为 `x+w/y+h`；边距次序为左、上、右、下。几何与字号均为 100% 逻辑像素，等于 DIP；ms、颜色 alpha、比例不乘缩放。设备比例只在栅格化时乘一次，字体的宿主 TextScale 也只乘一次，行高和 ascent 用实际字体度量，不能拿字号充当行高。主题接口依据 `theme/theme.go:64-71`、`theme/apply.go:12-30`；Telegram 字体声明见 `L/basic.style:53-57,84-94`。

先描述 LTR；RTL 将布局矩形 `(x,w)` 映射为 `(W-x-w,w)`，保持文字自身方向和数字内容。Toggle、Radio、Check 的镜像调用见 `L/widgets/checkbox.cpp:95-108,250-252,413-427`，设置图标和右值定位见 `T/settings/settings_common.cpp:445-451,504-511`。

参考官方 tdesktop 的三条做法：分隔带占满页面宽度；标题、按钮和说明分别拥有自己的留白；skip 是独立高度而非所有行通用 gap。这避免重复叠加22 DIP边距和把分隔带缩成短线。依据 `T/ui/vertical_list.cpp:19-80`、`L/widgets/box_content_divider.cpp:29-56`。文档写法参考 details-a 的 `docs/reference/paint/message-bottom.md`，只借规格组织；未借入第三方实现。

## 样式数值登记

| Telegram 常量 | 值 / 用途 | 来源 |
|---|---|---|
| defaultVerticalListSkip | 6 DIP，独立白色空段 | `L/basic.style:130`；`T/ui/vertical_list.cpp:19-24` |
| defaultSubsectionTitlePadding | (22,7,10,9) DIP | `L/layers/layers.style:155` |
| defaultSubsectionTitle | 14 semibold；minWidth=240 DIP；windowActiveTextFg | `L/layers/layers.style:148-154`；`L/basic.style:56` |
| boxDividerHeight | 8 DIP，不是线宽 | `L/widgets/widgets.style:675-681` |
| defaultBoxDividerLabelPadding | (22,8,22,16) DIP | `L/widgets/widgets.style:690` |
| boxDividerLabel | 13 regular；minWidth=245 DIP；左上；windowSubTextFg | `L/widgets/widgets.style:684-689` |
| settingsButton / settingsButtonNoIcon padding | (60,10,22,10) / (22,10,22,8) DIP | `T/settings/settings.style:13-27` |
| boxRowPadding | (24,0,24,0) DIP，仅通用 Box 行，不是设置页边距 | `L/layers/layers.style:87` |
| boxDividerBg / boxDividerFg | 浅色 #f1f1f1 / #000000；暗色按现有同名角色 | `L/colors.palette:146-147`；[颜色表](../theme-colors.md) |
| windowBg / windowActiveTextFg / windowSubTextFg | 浅色 #ffffff / #168acd / #999999；暗色 #17212b / #6ab3f3 / #708499 | `L/colors.palette:9,14,20`；[颜色表](../theme-colors.md) |

## 小标题

段起点 `(0,y)`，宽 W：文字行框起点 `(22,y+7)`，可用宽 `W-22-10`，自然排版高度记 Ht；该块总高 `7+Ht+9`。额外 addPadding 逐边相加，不替换默认边距。小标题不是可点击按钮，没有 hover/ripple，不画圆底、横线或卡片边框。字体14 semibold、色 windowActiveTextFg。自然换行高度由文字测量决定；240是自然宽度约束，不能把标题最终宽硬设240再居中。依据 `T/ui/vertical_list.cpp:70-80`；`L/layers/layers.style:148-155`。

## 分隔带

普通 divider 是 `(0,y,W,8)` 的实色矩形，先填 boxDividerBg，再画顶/底阴影条；无圆角、无中间细线。Top/Bottom 两部分可分别关闭。顶条 `(0,y,W,5)`，底条 `(0,y+8-5,W,5)`；二者有2 DIP重叠，这是原始资源的布局。5来自固定版本 `I/box_divider_top.png` 和 `I/box_divider_bottom.png` 的1×5画布元数据；定位与绘制次序依据 `L/widgets/box_content_divider.cpp:29-56`，引用资源见 `L/widgets/widgets.style:675-681`。

阴影条实际明暗梯度来自资源，不是 boxDividerFg 的实黑5 DIP线。素材限制下不复制其位图；替代画法与精度边界登记于 [SET-DIVIDER-SHADOW](../exceptions.md#set-divider-shadow)。实现者按登记的替代绘制，不把原版阴影当作已复现。

## 带说明文字的分隔带

说明文字在灰色带内，绝不是“8高分隔线下面再放白底说明”。块宽 W；正文矩形 `(22,y+8,W-44,Hd)`，Hd 为13 regular按 `W-44` 排出的自然文字高度；总高 `8+Hd+16`。先画覆盖整个块的 divider 背景，再画文字；顶阴影位于 y，底阴影位于 `y+块高-5`，均可由 RectParts关闭。来源 `T/ui/vertical_list.cpp:50-66`、`L/widgets/labels.cpp:1146-1160`、`L/wrap/padding_wrap.cpp:24-45,54-65`；数值见上表。

说明长文本自然换行；包含链接时保留文本的链接交互，说明块本身不变成整行按钮。颜色 windowSubTextFg，链接继承 defaultTextPalette.linkFg→windowActiveTextFg。来源 `L/widgets/widgets.style:684-693`、`L/basic.style:70-86`。没有默认显隐动画；调用方若使用 SlideWrap，那是外围组合，不是 divider 自己动效。

## skip 与页面内边距

skip 只是高度6的空白布局项，不画线、不接收焦点。调用处可传别的已登记高度；相邻skip相加，不做网页 margin collapse。来源 `T/ui/vertical_list.cpp:19-24`。

设置页没有统一“整页先 Padding(22)”的实现：行背景和divider均铺满W，标题左22/右10，无图标行文字左22/右22，有图标行文字左60，说明左/右22。不能给整页再叠22，造成无图标标题左44、有图标左82。真实组合例：`T/settings/sections/settings_advanced.cpp:99-138,1397-1402`，按钮布局见 `T/settings/settings.style:13-27`。通用Box的24边距另见 choose-box，不继承到设置行。

建议模型页的段落顺序、角色名、场合和数值标签由宿主提供；本规格不决定业务内容。无额外自定段间距：可组合“skip→小标题→若干行→skip→divider→skip”，每个skip显式计入。

## 后续验收输入

| 输入 / 状态 | 观察点 |
|---|---|
| 有/无图标行夹在两个段标题之间 | 左22与左60分别成立；hover底色和divider仍从x=0开始 |
| 一行与三行说明 | 高度等于8+实测文字高度+16；灰底覆盖全文；不裁末行 |
| Top、Bottom、两者、两者关闭 | 只改变边缘阴影，不改变块总高 |
| 连续两个skip | 总高12，不折叠成6 |
| 明暗、RTL、100/125/150% | 使用同名palette；DIP只转换一次；对照误差≤1 DIP |

以上是实现验收条件，非本次UI测试结果。本任务仅规格；未运行Telegram/画廊、未做截图叠图或性能验收，也无Go测试和故意改坏检查。

## theme 要补的

对照 `theme/metrics_gen.go`、`theme/preset.go:19-58`、[现有尺寸表](../theme-metrics.md)：以下均尚无专用键；建议名为本库命名，数值及来源为Telegram事实，未在本任务新增token。各行列出多个名字时按同一顺序展开。

| 建议键 | Telegram 来源 / 值 | 用途 |
|---|---|---|
| settings.sectionSkip | defaultVerticalListSkip，6；`L/basic.style:130` | skip |
| settings.sectionTitlePaddingLeft/Top/Right/Bottom | defaultSubsectionTitlePadding，22/7/10/9；`L/layers/layers.style:155` | 小标题 |
| settings.sectionTitleMinWidth | defaultSubsectionTitle.minWidth，240；`L/layers/layers.style:153` | 自然宽 |
| typography.settingsSectionTitle | defaultSubsectionTitle.font，14/600；`L/layers/layers.style:148-150` | 现有dialogButton同值但不同用途，需独立覆盖 |
| settings.dividerHeight | boxDividerHeight，8；`L/widgets/widgets.style:677` | 分隔带 |
| settings.dividerEdgeHeight | 顶/底画布高5；`I/box_divider_top.png`、`I/box_divider_bottom.png`，样式引用 `L/widgets/widgets.style:675-676` | 阴影范围 |
| settings.dividerTextPaddingLeft/Top/Right/Bottom | defaultBoxDividerLabelPadding，22/8/22/16；`L/widgets/widgets.style:690` | 说明 |
| settings.dividerTextMinWidth | boxDividerLabel.minWidth，245；`L/widgets/widgets.style:685` | 自然宽 |

无需补颜色：window.background/activeTextForeground/mutedText，dialog.dividerBackground/dividerForeground均已有。无需补说明字阶：body=13/400已有。替代阴影的参数复用control.shadowForeground与上述edgeHeight，不冒充原版渐变。
