# 设置行按钮画法规格

基线、DIP/RTL和净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。`T/`=tdesktop/Telegram/SourceFiles/，`L/`=lib_ui/ui/。参考官方 SettingsButton 的“全宽命中区、文字和尾部独立布局、按下涟漪”做法；仅借几何和行为，不沿用C++组织。来源 `L/widgets/buttons.cpp:977-1055`、`T/settings/settings_common.cpp:425-532,589-601`。

## 样式与状态色

| 项 | Telegram 常量 / 数值 | 来源 |
|---|---|---|
| 内容基高 | defaultSettingsButton.height=20 DIP | `L/widgets/widgets.style:1519` |
| 有图标行边距 | settingsButton.padding=(60,10,22,10) DIP，总高40 | `T/settings/settings.style:13-17`；`L/widgets/buttons.cpp:1038-1039` |
| 无图标行边距 | settingsButtonNoIcon.padding=(22,10,22,8)，总高38 | `T/settings/settings.style:25-27`；同上 |
| 标题 / 右值 | boxTextStyle→boxTextFont=14 regular；右值maxHeight=20 | `T/settings/settings.style:14`；`L/widgets/widgets.style:1505-1519`；`L/basic.style:56-57` |
| 图标left | settingsButton.iconLeft=20；大小由所选图标画布决定 | `T/settings/settings.style:16`；`T/settings/settings_common.cpp:439-450` |
| 右值距右 | settingsButtonRightSkip=23 DIP | `T/settings/settings.style:89`；`T/settings/settings_common.cpp:504-511` |
| 默认背景/hover/涟漪 | windowBg / windowBgOver / windowBgRipple，浅色 #ffffff/#f1f1f1/#e5e5e5 | `L/widgets/widgets.style:1513-1526,703-706`；`L/colors.palette:9-12` |
| 默认标题/hover | windowBoldFg / windowBoldFgOver，浅色均#222222 | `L/widgets/widgets.style:1511-1512`；`L/colors.palette:16-17` |
| 默认右值 | windowActiveTextFg，浅色#168acd，暗色#6ab3f3 | `L/widgets/widgets.style:1505-1508`；[颜色表](../theme-colors.md) |
| Light / Attention标题 | lightButtonFg/Over (#168acd)；attentionButtonFg/Over (#d14e4e) | `T/settings/settings.style:28-50`；`L/colors.palette:45-49` |
| Disabled样式标题 | windowSubTextFg，浅色#999999 | `T/settings/settings.style:78-88`；`L/colors.palette:14` |

暗色所有状态按现有同名palette读取，不按浅色乘透明度推导。[颜色表](../theme-colors.md)已有各项。

## 行、图标、标题

行局部框 `(0,0,W,H)`，H=`paddingTop+20+paddingBottom`。背景覆盖全框，无独立圆角或描边。标题原点 `(paddingLeft,paddingTop)`，单行尾部省略，基线为 `paddingTop+实际ascent`。内容20是样式布局槽，不是字体行高。来源 `L/widgets/buttons.cpp:977-1015,1038-1039`。

可选左图标以实际允许分发的图形画布 `(iw,ih)` 绘于 `(20,floor((H-ih)/2),iw,ih)`，不把图标大小强制设成字体14；不随鼠标事件取得独立命中区域。Telegram icon descriptor可提供静态图标、圆角色底或动画资源；本库只接宿主准备的资源，圆底半径及颜色也须显式取主题。来源 `T/settings/settings_common.cpp:398-410,425-456`。默认建议使用现有Lucide24画布，标为素材替代，见 [SET-ICONS](../exceptions.md#set-icons)。无图标时选无图标样式，而不是保留60的空槽。

## 右侧当前值与截断优先级

官方默认右值是蓝色，不是灰色。任务要求的灰色变体取 windowSubTextFg；作为显式样式变体登记于 [SET-MUTED-VALUE](../exceptions.md#set-muted-value)，不修改源码事实。

设标题自然宽T、右值自然宽V，左右边距为L/R，右值距右S=23。右值允许宽 `A=W-L-R-T-S`；正常A>0时宽为 `min(V,A)`，右边界为 `W-S`，顶部与标题同为paddingTop。官方先保留标题完整自然宽，再缩短右值；因此右值先截断，标题超过本行可用宽 `W-L-R` 才省略。标题与右值都不换行。来源 `T/settings/settings_common.cpp:485-532`；`L/widgets/buttons.cpp:997-1015`；`L/widgets/labels.cpp:1091-1142`。

注意：右值是透明鼠标子标签；它没有反向更新父标题可用宽。灰值不随hover转蓝。A≤0时官方组合没有正的右值空间，不能推导出“给右值固定半行宽”。我定的窄宽合同：右值不绘制，标题按 `W-L-R` 省略，以免负宽参与排版；登记 [SET-VALUE-OVERFLOW](../exceptions.md#set-value-overflow)。A恰好为0也隐藏右值。右值为空时不占宽。

## 可选右箭头

基础 SettingsButton 无内置箭头，绘制只有背景、ripple、文字和可选Toggle。官方Premium/Business行另外叠了箭头子控件，位置取 settingsPremiumArrowShift=(-5,-1)，依附backButton尺寸；不能把该促销行的特殊偏移冒充普通行通用指标。来源 `L/widgets/buttons.cpp:977-1031`、`T/settings/sections/settings_business.cpp:261-274`、`T/settings/settings.style:512-514`。

本任务需要可选箭头，按 [SET-ROW-ARROW](../exceptions.md#set-row-arrow) 的“我定的”统一扩展：允许分发的chevron画布24×24，右沿距行右22，垂直居中；颜色menuIconFg/Over。值的右沿位于箭头左沿再向左6，6复用defaultMenu.itemRightSkip；有值时相应A改为 `W-L-R-T-(22+24+6)`。标题也为箭头保留 `24+6`，其余仍右值先截断。无箭头不留槽；RTL箭头朝左并镜像位置。该组合没有Telegram现成同款，不声称逐像素相同。

## 悬停、按下、禁用与动效

| 状态 | 画法与转换 | 来源 |
|---|---|---|
| 常态 | windowBg，标题windowBoldFg | `L/widgets/buttons.cpp:980-1009` |
| hover或down且enabled | 立即切windowBgOver / windowBoldFgOver；无hover淡入、无缩放 | 同上；状态刷新 `:1042-1055` |
| 鼠标按下 | 在鼠标点起圆形涟漪，裁切于整行矩形；背景在下、涟漪其上、标题和图标最上 | `L/widgets/buttons.cpp:185-218,977-990` |
| 按住 | 650 ms扩至最远角半径；完成后保持涟漪 | `L/widgets/widgets.style:703-706`；`L/effects/ripple_animation.cpp:59-86,106-149` |
| 松开/取消down | 当前涟漪200 ms线性淡出；扩张进度仍按原时间轴 | `L/widgets/buttons.cpp:201-203`；`L/effects/ripple_animation.cpp:153-155` |
| Disabled | 禁止点击和新涟漪，paintOver=false；基类没有统一0.5淡化；选择settingsOptionDisabled或settingsButtonDisabledWithIcon时标题灰色 | `L/widgets/buttons.cpp:980,1042-1055`；`T/settings/settings.style:78-88` |
| Light / Attention | 只替换标题色；settingsAttentionButton未覆盖背景为红色hover | `T/settings/settings.style:28-50` |

扩张缓动 `q=1-(1-t)^5`；圆半径为 `round(q×max到四角距离)`，起始半径为0；淡出alpha=`1-t`。来源 `L/effects/ripple_animation.cpp:59-86,111-137,153-155`。键盘激活不会伪造鼠标位置的涟漪，只有ByPress才起新波；`L/widgets/buttons.cpp:194-199`。禁用视觉不要与Checkbox的0.5混同；mygo原语自动0.5衰减与此不同，登记 [SET-DISABLED](../exceptions.md#set-disabled)。

## 后续验收输入

覆盖有/无图标、有/无值、有/无箭头的组合；普通/Light/Attention、hover/down/release/disabled；长标题、长值、A=1/0/-1；RTL、明暗和三档缩放。测量40/38高、左20图标、左60/22文字、右23值；采样涟漪0/325/650ms及松开后100/200ms。此处是未来验收要求，未实施UI或性能测试。

## theme 要补的

| 建议键 | Telegram常量、值、来源 |
|---|---|
| settings.buttonContentHeight | defaultSettingsButton.height=20；`L/widgets/widgets.style:1519` |
| settings.buttonPaddingLeft/Top/Right/Bottom | settingsButton.padding=60/10/22/10；`T/settings/settings.style:15` |
| settings.buttonNoIconPaddingLeft/Top/Right/Bottom | settingsButtonNoIcon.padding=22/10/22/8；`T/settings/settings.style:26` |
| settings.buttonIconLeft | settingsButton.iconLeft=20；`T/settings/settings.style:16` |
| settings.buttonValueRight | settingsButtonRightSkip=23；`T/settings/settings.style:89` |
| settings.buttonValueMaxHeight | defaultSettingsRightLabel.maxHeight=20；`L/widgets/widgets.style:1508` |

已有可复用：typography.dialogBody=14/400、motion.rippleShow/Hide=650 Quint/200 linear；全部颜色已有。箭头24使用现有header.iconSize，6使用menu.rightSkip，22使用本页buttonPaddingRight；这些是我定的用途组合，不需再发明数值。若实现要让设置标题/值字阶独立于dialogBody编辑，可增加同源用途别名，但不是本页尚缺的数值。
