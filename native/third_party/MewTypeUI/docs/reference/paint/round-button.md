# 通用圆角按钮画法规格

ctl-spec；基线、T/L 路径、DIP、字体度量、RTL、净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。本页覆盖独立 RoundButton；全宽设置行继续使用 [settings-button](settings-button.md)。参考官方“背景框、内容宽、命中框分开”的做法，避免给文字按钮套设置行几何；来源 `L/widgets/buttons.cpp:394-475,478-614`。CrychicUI 的 ButtonBase 交互根只作 API 参考，见 [控件总索引](../controls.md)。

## 样式数值登记

| 项 | Telegram 常量 / 值 | 来源 / theme 用途 |
|---|---|---|
| 默认高 / 自然宽增量 | defaultActiveButton.height=34；width=-34 表示文字及附加内容自然宽再加34 | `L/widgets/widgets.style:718-736`；button.height / horizontalExtra |
| padding / textTop | (0,0,0,0) / 8 | 同上 `:730,732`；button.padding* / textTop |
| 字体 / 圆角 | semiboldTextStyle=13/600；radius未指定时buttonRadius=4 | `L/basic.style:53-55,92-94`；`L/widgets/widgets.style:716,736` |
| 次数间隔 | numbersSkip=7 | `L/widgets/widgets.style:726`；button.numbersGap |
| Primary | activeButtonBg/Over/Ripple、Fg/Over、SecondaryFg/Over | `L/widgets/widgets.style:719-724,738-740`；已有control.primary* |
| Light | lightButtonBg/Over/Ripple、Fg/Over；次数与主字同色 | 同文件 `:753-763`；已有control.surface* |
| 涟漪 | 650 ms Quint扩张、200 ms linear退出 | [settings-button](settings-button.md#悬停按下禁用与动效) |

默认不是胶囊；fullRadius显式开启后半径为背景高整数除2。无描边、无阴影；允许显式笔刷/描边覆盖，但不能宣称所有 Outline/Ghost 命名都对应官方 defaultActiveButton。调用方需要的透明底 Light 组合见 [CTL-COMPOSITION](../exceptions.md#ctl-composition)。

## 几何与层次

设文字自然宽T、附加内容宽E、padding为L/U/R/D、样式高h。默认根宽 `T+E+34+L+R`、高 `h+U+D`，背景为 `(L,U,W-L-R,h)`；先画普通底，再在hover或down时画Over底，再画裁于背景圆角的ripple，最后文字和图标。圆角填充启用高质量抗锯齿，不用四段线拼圆角。来源 `L/widgets/buttons.cpp:413-443,478-531,584-614`。

文字行顶 `U+textTop`，基线再加实际ascent；内容居中起点 `L+floor((W-C-L-R)/2)`，C为当前允许内容宽。单行省略，不换行。FullWidth正值为指定宽；fullRadius时再加L+R；负值表示自然内容宽再加其绝对值，此时背景从x=0开始、文字从该增量一半开始。无FullWidth时，样式width>0是定宽、≤0是自然宽加绝对值。正FullWidth过窄时按源码保留 `h-fontHeight` 的宽差后截断，不能把长文字撑出父区。来源 `L/widgets/buttons.cpp:413-475,481-485,533-567`。

图标位置x≥0时按指定x画、不挤文字；x<0时图标在文字前，附加宽为 `图标宽−x`，图标左沿 `文字左+x−图标宽`；y≥0是绝对顶，y<0是相对文字顶偏移。图标采用宿主允许分发的资源，不复制上游掩模。数字后缀使用同一文字顶，绘制起点为主文字右沿加7（主文字为空不加）；自然附加宽由数字动画的当前宽给出，不能按字符串长度固定槽。注意本版addedWidth先加数字宽，此时不加numbersSkip，但实际绘制非空主文字后仍加7；这是源码当前非对称行为，不把自然宽额外加第二次7。来源 `L/widgets/buttons.cpp:334-356,449-457,542-580`。

自定义四角时背景和ripple必须共用同一轮廓；RTL镜像框与图标位置，文本保留本地方向。来源 `L/widgets/buttons.cpp:404-410,486-513,584-609`。

## 数字后缀与文字变换

默认不改变文字大小写，显式ToUpper才先变换后测量；可访问名称仍为原始标签。来源 `L/widgets/buttons.h:138-153,219`、`L/widgets/buttons.cpp:413-422`。后缀的数值value只决定增减方向，显示字符串由宿主本地化。

数字变化按右端对齐字符槽；默认每个字符使用同一宽，取该字体字符0…8的最大测量宽（固定版本构造循环不含9），字形在槽内居中；默认不是用9测最大。位数减少时旧高位留到退场结束，宽从旧宽向新宽插值；多一次变化发生在动画中时只保留最新延迟目标，当前动画结束后再启动，不立即跳到第三串。来源 `L/effects/numbers_animation.cpp:16-24,35-105`、`L/effects/numbers_animation.h:65`。

后缀完整动画150ms，线性时间轴再套easeOutCirc得到q；改变的旧字符alpha=1−q、新字符alpha=q。数值增大时旧字符下移q×F、新字符从−(1−q)×F上移进入；减小时方向反转，F为实际字行高；相同字符不移动。宽也用同一q插值，RTL仅镜像整体文字槽，不反转数字串。来源 `L/effects/numbers_animation.cpp:108-112,130-172`；时长来源`:20`→`L/basic.style:100`。清空后缀立即移除；初始静态与ReduceMotion调用finish直接终态，不因每帧绑定重播。数字区域无额外自身裁切，根控件裁切可见边界；每帧宽变化只更新受影响的按钮尺寸。


## 状态与交互

普通、hover、down立即切换对应色，无hover缩放或弹簧；按压仅产生ripple。拖出释放不激活，禁用不新建ripple、不交付动作。Telegram此paint路径没有统一disabled alpha；mygo禁用固定0.5的差异沿用 [SET-DISABLED](../exceptions.md#set-disabled)。键盘 Enter/Space 与无障碍名称使用按钮原语，键盘不捏造鼠标涟漪。颜色改变不重排，文字、数字、图标/字体/宽度改变才重测。来源 `L/widgets/buttons.cpp:185-205,478-582`；宿主异步busy是组合状态，保留标签并禁用，不自改“成功”。

## 后续验收输入

Primary/Light；短/长/空主文字+数字、图标左右/绝对位置、natural/fullWidth/fullRadius/四角；hover/down/拖出释放/disabled；RTL、明暗、100/125/150%。量默认34高、T+E+34宽、文字顶8、圆角4，ripple起/中/终态。此为未来验收，不是本次UI通过记录。

## theme 要补的

| 建议键 | 值 / 来源 |
|---|---|
| button.height / horizontalExtra / textTop / numbersGap | 34 / 34 / 8 / 7；`L/widgets/widgets.style:726-732` |
| button.paddingLeft/Top/Right/Bottom | 0/0/0/0；同文件 `:730` |
| motion.buttonNumbers | 150ms ease-out-circ；`L/effects/numbers_animation.cpp:20,108-112,134`；`L/basic.style:100` |

圆角直接复用dialog.buttonRadius=4（同源buttonRadius）；typography.contentTitle=13/600、Primary/Light颜色和motion.rippleShow/Hide已有。fullRadius取几何推导，不新存999半径。后续实现按metrics.tsv登记生成；本次不改theme契约。
