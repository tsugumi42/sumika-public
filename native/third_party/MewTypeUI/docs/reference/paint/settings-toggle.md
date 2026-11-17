# 设置开关行画法规格

坐标、基线与DIP见 [settings-section](settings-section.md#坐标来源与边界)。`T/`=tdesktop/Telegram/SourceFiles/，`L/`=lib_ui/ui/。本页描述设置行使用的ToggleView，不套Material开关。参考“行负责点击、ToggleView只负责绘制；逻辑框含描边留量；同一进度驱动位置和颜色”的做法，避免开关成为行内第二个点击目标。来源 `L/widgets/buttons.cpp:901-924,1018-1031`、`L/widgets/checkbox.cpp:70-108`。

## 样式数值登记

| 常量 | 值 / 含义 | 来源 |
|---|---|---|
| defaultToggle.border | 2 DIP，圆钮描边宽；逻辑框两边各预留2 | `L/widgets/widgets.style:868` |
| diameter / width / shift | 16 / 14 / 1 DIP；width为圆钮行程，不是总宽 | 同文件 `:869-871` |
| duration | universalDuration=120 ms | 同文件 `:867`；`L/basic.style:135` |
| toggledBg / untoggledBg | windowBg，浅#ffffff、暗#17212b | 同文件 `:863,865`；[颜色表](../theme-colors.md) |
| toggledFg | windowBgActive，浅#40a7e3、暗#5288c1 | 同文件 `:864`；[颜色表](../theme-colors.md) |
| settings untoggledFg / Over | infoProfileToggle→menuIconFg / menuIconFgOver；浅#999999/#8a8a8a，暗#6c7883/#dcdcdc | `T/info/info.style:605-609`；`T/settings/settings.style:13`；[颜色表](../theme-colors.md) |
| settings toggleSkip | 20 DIP，继承infoProfileButton；不是defaultSettingsButton的22 | `T/info/info.style:611-619`；`L/widgets/widgets.style:1524` |
| xsize/vsize/vshift/stroke | 均0；普通开关没有X/勾符号 | `L/widgets/widgets.style:872-875` |
| rippleAreaPadding | 8 DIP；仅单独作为Checkbox view时使用 | 同文件 `:876`；`L/widgets/checkbox.cpp:204-214` |

## 开关与行的位置

行高、标题、图标、背景和行涟漪按 [settings-button](settings-button.md)，无图标38高、有图标40高。开关逻辑大小 `Wt=2b+d+travel=34`，`Ht=2b+d=20`；行中逻辑框左上 `tx=W-20-34=W-54`，`ty=floor((H-20)/2)`。因此有图标ty=10，无图标ty=9。来源 `L/widgets/checkbox.cpp:78-79`、`L/widgets/buttons.cpp:1025-1031`。

标题不会盖到开关：官方标题可用宽 `W-paddingLeft-paddingRight-(W-tx)`，即 `W-L-R-54`；右侧减了原R也仍保留，不擅自精简成 `tx-L`。标题仍从paddingTop绘制并单行尾省略。来源 `L/widgets/buttons.cpp:997-1015`。行不同时使用当前值标签和Toggle，业务如需解释写到相邻说明区。

## 轨道和圆钮

以逻辑框左上为 `(tx,ty)`，b=2，d=16，s=1，a为当前0…1进度：

| 层 | LTR矩形 / 形状 | 来源 |
|---|---|---|
| 轨道 | `(tx+b+s,ty+b+s,d+travel-2s,d-2s)` = `(tx+3,ty+3,28,14)`，填充圆角矩形，半径7，无笔触 | `L/widgets/checkbox.cpp:86-102` |
| 圆钮 | `(tx+b+round(14a),ty+b,16,16)` 的椭圆；笔宽2，填windowBg | 同文件 `:95-108` |
| 圆钮可见外轮廓 | 描边居中于16直径路径，名义可见直径18；不要把16解释为含描边的外圆 | 同文件 `:104-108` |

轨道填色与圆钮描边共同为 `C(a)=mix(untoggledFg,toggledFg,a)`；圆钮背景为 `mix(untoggledBg,toggledBg,a)`，此样式两端相同。高质量抗锯齿，无投影、无额外外框。圆形用真实椭圆路径；不能用折线近似。来源 `L/widgets/checkbox.cpp:90-108`。RTL镜像两个矩形，开的位置在左；不能只翻标题。

## 状态与动效

状态切换时间120 ms，默认线性；完整off→on时 `a=t/120`，on→off时 `a=1-t/120`。中途改目标从当前a0向新目标a1插值 `a=a0+(a1-a0)t/120`，重新计满120ms，而非跳回端点。设置行初始绑定值后finishAnimating，首帧直接到终态，不从off播入。重复设置相同布尔值不启动，不能每帧重新起120ms。来源 `L/widgets/checkbox.cpp:21-45,58-63`、`L/effects/animations.h:59-77,430-439,471-479`、`L/widgets/buttons.cpp:915-924`。完整切换线性中点逻辑行程7、端点14，颜色也应在相同进度；不复用theme.control现有ease-out。

hover立即把off前景换menuIconFgOver，on前景不变；开关位置不因hover移动。按下反馈由整行650/200ms涟漪负责，设置行不再给开关周围叠第二个圆形涟漪。来源 `L/widgets/buttons.cpp:977-990,1042-1052`。禁用行及样式差异见 [settings-button](settings-button.md#悬停按下禁用与动效)。

Locked是独立于Disabled的状态：在圆钮原点叠lockIcon，按C(a)染色；它不自动阻止行点击。锁定行使用settingsButtonNoIconLocked；宿主决定点击解释还是拒绝切换，不将“有锁图形”等同禁用。来源 `L/widgets/checkbox.cpp:110-118,217-220`、`T/settings/settings.style:34-40`、`L/widgets/buttons.cpp:938-940`。锁图标素材替代沿用 [SET-ICONS](../exceptions.md#set-icons)，不得复制Telegram图标。

普通设置开关的xsize=0，因此不画X→勾形变；`paintXV`只在其他显式非零样式下启用，不属于本页默认设置开关。来源 `L/widgets/checkbox.cpp:110-118`、`L/widgets/widgets.style:872-875`。

“我定的”可访问性映射：ReduceMotion或零时长直接取终态；mygo禁用默认0.5与Telegram设置行差异见 [SET-DISABLED](../exceptions.md#set-disabled)。这些不是新增Telegram视觉常量。

## 后续验收输入

开/关、hover、按住/松开、disabled开/关、locked开/关；默认与无图标行；初始on不得播动画；0/60/120ms位置和颜色；60ms反向、重复同值；RTL、明暗、100/125/150%。逻辑框34×20，轨道28×14，路径钮16、可见18，right skip20应分别量，不能只断言一个总宽。此页尚未实现或运行UI测试。

## theme 要补的

| 建议键 | Telegram常量、数值、来源 |
|---|---|
| settings.toggleBorder | defaultToggle.border=2；`L/widgets/widgets.style:868` |
| settings.toggleDiameter | defaultToggle.diameter=16；同文件 `:870` |
| settings.toggleTravel | defaultToggle.width=14；同文件 `:871` |
| settings.toggleTrackInset | defaultToggle.shift=1；同文件 `:869` |
| settings.toggleRight | infoProfileButton.toggleSkip=20；`T/info/info.style:619` |
| settings.toggleRipplePadding | defaultToggle.rippleAreaPadding=8；`L/widgets/widgets.style:876`，独立view变体用 |
| motion.settingsToggle | defaultToggle.duration→universalDuration=120 ms，linear；`L/widgets/widgets.style:867`、`L/basic.style:135`、`L/widgets/checkbox.cpp:36-40`、`L/effects/animations.h:67` |

全部颜色已有，见上表；行几何补项在settings-button登记。34×20、28×14、半径7、可见直径18均是上述token推导值，不再存第二份。Locked使用的允许分发图标画布由宿主交付，不凭原版资源猜一个锁大小。
