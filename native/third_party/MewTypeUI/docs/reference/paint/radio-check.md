# 单选与多选画法规格

版本、净室边界、DIP/RTL见 [settings-section](settings-section.md#坐标来源与边界)。`T/`=tdesktop/Telegram/SourceFiles/，`L/`=lib_ui/ui/，`I/`=lib_ui/icons/。参考官方将Radio/Check绘制与Checkbox交互壳分开的做法；同一视图可嵌入普通文字行或LanguageBox双行列表，不能把列表行高当成圆点直径。来源 `L/widgets/checkbox.cpp:224-258,381-440,552-597`。

## 样式数值登记

| 常量 | 值 | 来源 |
|---|---|---|
| defaultRadio.diameter/thickness | 22 / 2 DIP | `L/widgets/widgets.style:855-856` |
| defaultRadio.outerSkip | 声明10，使用时÷10=1 DIP | 同文件 `:857`；`L/widgets/checkbox.cpp:413` |
| defaultRadio.skip | 声明60，使用时÷10=6 DIP | 同文件 `:858`；`L/widgets/checkbox.cpp:426` |
| defaultCheck.diameter/thickness | 22 / 2 DIP | `L/widgets/widgets.style:845-846` |
| Check外圆角 | roundRadiusSmall=3 DIP，路径半径3−2/2=2 | `L/basic.style:108`；`L/widgets/checkbox.cpp:250-252` |
| Radio/Check duration | universalDuration=120 ms，linear | `L/widgets/widgets.style:848,859`；`L/basic.style:135`；`L/widgets/checkbox.cpp:27-40`；`L/effects/animations.h:67` |
| bg/untoggledFg/toggledFg | transparent / checkboxFg / windowBgActive | `L/widgets/widgets.style:841-854` |
| 单选LanguageBox背景 | langsRadio.bg=boxBg，非透明 | `T/boxes/boxes.style:175-177` |
| defaultCheckbox.margin/checkPosition/textPosition | (8,8,8,8) / (8,8) / (10,2) DIP | `L/widgets/widgets.style:883-886` |
| defaultBoxCheckbox.textPosition/style | (12,1)，14 regular | `L/layers/layers.style:65-68`；`L/widgets/widgets.style:1024-1025` |
| settingsCheckbox.textPosition | (15,1)，其余继承defaultBoxCheckbox | `T/settings/settings.style:170-172` |
| settingsCheckboxPadding | (22,10,10,10) DIP，宿主布局边距 | 同文件 `:173` |
| rippleAreaPadding / rippleAreaPosition | 8 / (−8,−8) DIP | `L/widgets/widgets.style:849,860,890` |
| disabledOpacity | 0.5 | 同文件 `:895` |
| 默认勾素材位置 | (4,7)，资源画布14×10 | 同文件 `:839`；`I/default_checkbox_check.png`元数据 |

颜色浅/暗：checkboxFg=#b3b3b3/#4f6276；windowBgActive=#40a7e3/#5288c1；勾overviewCheckFgActive两主题均白。来源 `L/colors.palette:18,79,482`、[颜色表](../theme-colors.md)。普通标签windowFg，选中仍windowFg；不把整行文字转蓝或反白。来源 `L/widgets/widgets.style:879-880`。

## Radio圆环与内点

以22×22布局槽左上 `(x,y)` 为原点，中心 `(x+11,y+11)`。外环路径矩形为 `(x+2,y+2,18,18)`：缩进=outerSkip/10+thickness/2=2；笔宽2，外部可见直径20，内部可见直径16。外环填bg、笔色在unchecked/checked前景间按进度a线性混色。高质量抗锯齿。来源 `L/widgets/checkbox.cpp:397-414`。

内点无描边，同一中心，边距 `11(1-a)+6a=11-5a`，因此内点直径 `10a`。a=0无点，a=1直径10，a=0.5直径5；点颜色同外环插值色。槽22并不表示可见环22。来源 `L/widgets/checkbox.cpp:416-427`。RTL镜像整个槽，保持圆形同心。

## Check方框与勾

22×22槽，路径矩形 `(x+1,y+1,20,20)`，路径圆角2、笔宽2，所以可见外框22×22、外角3、内角1。无阴影。设U为unchecked前景、V为checked前景、B=transparent，当前前景 `F=mix(U,V,a)`；描边用F，填充用 `mix(B,F,a)`。不可简化成“背景直接B→V”：中间态的RGB也跟随F，透明度随a增长。来源 `L/widgets/checkbox.cpp:236-252`。

a>0时在 `(x+4,y+7,14,10)` 的画布上画完整勾，颜色overviewCheckFgActive；它不是沿路径逐段长出的勾，也未在此处乘a淡入。官方是资源掩模，不能从该调用虚构线头或笔宽。a=0完全不画。来源 `L/widgets/checkbox.cpp:255-257`、`L/widgets/widgets.style:839`。本库用允许分发的勾图形，完整放入14×10槽，轮廓差异见 [SET-ICONS](../exceptions.md#set-icons)。不抄该资源轮廓坐标。

没有indeterminate三态或“横杠半选”分支；这两个view只有checked布尔状态。来源 `L/widgets/checkbox.cpp:21-45,236-258,397-440`。如果宿主需要三态，应另立规格，不能声称本页覆盖。

## 在行中的位置

普通Checkbox壳的**完整widget矩形**以 `(wx,wy)` 为原点；默认checkRect=`(wx+8,wy+8,22,22)`。标签x=`wx+8+22+textPosition.x`，标签y=`wy+marginTop+textPosition.y`。所以默认、Box、Settings三种文字原点相对widget分别为 `(40,10)`、`(42,9)`、`(45,9)`；对齐引用check槽，不是按字号垂直居中。来源 `L/widgets/checkbox.cpp:584-597,719-744`。

默认单行 `resizeGetHeight`返回内容高22，完整widget另含上下margin8；外部布局取得的是去margin后的内容尺寸。settingsCheckboxPadding的上下10位于内容外，因此标准设置勾选行占高 `10+22+10=42`，不是10+38+10=58；布局时必须扣一次widget margin，不能在mygo又加8。外部行坐标和widget坐标不可混用。来源 `L/widgets/checkbox.cpp:905-923`、`L/widgets/checkbox.h:241-242`、`L/wrap/vertical_layout.cpp:103-130`；外部padding见 `T/settings/settings.style:173`。需要多行时启用文字自然测高，内容高取max(22,文字高+textPosition.y)，而非继续硬裁22，见同函数 `:918-923`。

LanguageBox的双行选择列表自行定位view：槽 `(22,rowY+floor((rowHeight-22)/2),22,22)`；文字左66。这个行不使用上述Checkbox的8 margin和15 gap，完整几何见 [choose-box](choose-box.md#列表行)。来源 `T/boxes/language_box.cpp:978-981,1010-1020`。

## 交互状态和动效

Radio组更改值时前后两个圆点分别播放120ms线性a插值；点不因hover扩大。Checkbox释放于仍hover的行时翻转，Radio已选项再次激活仍保持选中。来源 `L/widgets/checkbox.cpp:668-674,879-902,942-950`。初始checked直接终态；动画中反向从当前a向新目标重新跑完整120ms，来源 `L/effects/animations.h:430-439,471-479`。

Checkbox壳的hover本身不画全行灰底。按下涟漪为38×38圆形遮罩（22+2×8），相对check槽左上偏(−8,−8)；按下位置必须落在该区域才启动，远处点击文字仍切值但不强造涟漪。涟漪颜色从windowBgOver→lightButtonBgOver随checked进度插值；扩张650ms Quint、退出200ms linear。来源 `L/widgets/checkbox.cpp:260-269,442-451,695-705,926-939`、`L/widgets/widgets.style:890-895,703-706`。LanguageBox使用整行矩形涟漪和hover背景，见choose-box，不再叠此圆形波。

Disabled：停止动画，将view缓存为终态，整块check和文字opacity=0.5，不绘涟漪、不切值；恢复enabled清除缓存，保留checked状态。来源 `L/widgets/checkbox.cpp:695-713,879-889`。这与SettingsButton的禁用画法不同。错误提示扩展：CheckView可将未勾前景从defaultCheck.untoggledFg向boxTextFgError线性过渡120ms；一旦勾选立即清除override。来源 `L/widgets/checkbox.cpp:278-316`。Radio只提供显式前景override，没有自动错误语义，来源 `:454-462`。

## 后续验收输入

分别量22槽、20外环可见直径、10内点、22方框及3外角；a=0/0.5/1；长文本单/多行；鼠标点check、近文字、远文字；键盘切组和已选再次激活；禁用切入/恢复、错误未选→选中；RTL、明暗和100/125/150%。勾出现方式、涟漪区域和原始图形槽分别验收。未实施组件、UI烟测或故意改坏检查。

## theme 要补的

| 建议键 | Telegram常量、值、来源 |
|---|---|
| choice.radioDiameter / radioStroke | defaultRadio.diameter/thickness=22/2；`L/widgets/widgets.style:855-856` |
| choice.radioOuterInset / radioDotInset | outerSkip/10=1、skip/10=6；同文件 `:857-858`，换算 `L/widgets/checkbox.cpp:413,426` |
| choice.checkDiameter / checkStroke | defaultCheck.diameter/thickness=22/2；`L/widgets/widgets.style:845-846` |
| choice.ripplePadding | Radio/Check.rippleAreaPadding=8；同文件 `:849,860` |
| choice.marginLeft/Top/Right/Bottom | defaultCheckbox.margin=8/8/8/8；同文件 `:883` |
| choice.checkLeft/Top | defaultCheckbox.checkPosition=8/8；同文件 `:886` |
| choice.textGap / textTop | defaultCheckbox.textPosition=10/2；同文件 `:885` |
| choice.boxTextGap / boxTextTop | defaultBoxCheckbox.textPosition=12/1；`L/layers/layers.style:67` |
| settings.checkTextGap / checkTextTop | settingsCheckbox.textPosition=15/1；`T/settings/settings.style:171` |
| settings.checkPaddingLeft/Top/Right/Bottom | settingsCheckboxPadding=22/10/10/10；同文件 `:173` |
| choice.checkMarkLeft/Top/Width/Height | defaultCheckboxIcon位置4/7、画布14/10；`L/widgets/widgets.style:839`，`I/default_checkbox_check.png`元数据 |
| motion.choice | Radio/Check.duration=120ms linear；`L/widgets/widgets.style:848,859`、`L/widgets/checkbox.cpp:36-40`、`L/effects/animations.h:67` |

已有：control.radiusSmall=3、dialogBody=14/400、body=13/400、rippleShow/Hide；palette颜色全部已有。disabledOpacity=0.5由mygo原语提供，不额外乘第二次，已有平台边界见 [exceptions](../exceptions.md)。未为“不存在的indeterminate”增加token。
