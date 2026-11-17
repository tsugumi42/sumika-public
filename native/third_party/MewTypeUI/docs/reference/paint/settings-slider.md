# SettingsSlider 分段选择画法规格

ctl-spec；基线、T/L、DIP、RTL、净室边界见 [settings-section](settings-section.md#坐标来源与边界)。本页是离散标签段，不是已由 [numeric-input](numeric-input.md#自动下载大小右值行加离散滑块) 覆盖的MediaSlider数值轨道。参考官方按文字自然宽分配段宽、位置和宽度共同插值的做法，来源 `T/ui/widgets/discrete_sliders.cpp:155-173,316-372,431-519`。

## 数值与状态色

| 项 | defaultSettingsSlider | 设置页settingsSlider覆写 | 来源 |
|---|---|---|---|
| height / padding | 39 / 0 | 不变 | `L/widgets/widgets.style:1176-1178` |
| barTop / gap / stroke / radius | 5 / 3 / 3 / 1 | 不变 | 同文件 `:1179-1182` |
| 普通/active条 | sliderBgInactive / sliderBgActive | windowBgOver / sliderBgActive | 同文件 `:1183-1184`；`T/settings/settings.style:92-96` |
| labelTop / 字体 | 17 / 13 regular | 不变 | `L/widgets/widgets.style:1185-1186` |
| 普通/active文字 | windowActiveTextFg / 同色 | windowSubTextFg / windowActiveTextFg | 同文件 `:1187-1188`；`T/settings/settings.style:94-95` |
| duration | 150ms，默认linear | 不变 | `L/widgets/widgets.style:1189`；`T/ui/widgets/discrete_sliders.cpp:237-241` |
| 设置页外围padding | 无 | (21,7,21,4) | `T/settings/settings.style:91`；实际调用 `T/settings/sections/settings_notifications.cpp:1567-1577` |

默认样式未设置ripple，showDuration为0，故按下即可预览选择，不画hover圆底或圆钮；不是普通tabs的650ms涟漪模式。来源 `T/ui/widgets/discrete_sliders.cpp:270-280,379-382`。颜色取现有control.sliderBackgroundInactive/Active、window.backgroundHover/mutedText/activeTextForeground，不能用player.activeForeground冒充其暗色。

## 几何

n个段、根宽W、padding p、段间g、各标签自然宽Ti。可分段总宽 `S=W-2p-(n-1)g`，先取等宽 `S/n`；若任一Ti≥等宽但ΣTi≤S，则每段为 `Ti+(S-ΣTi)/n`；显式strictSkip非0时为 `Ti+strictSkip`，此时不强制填满W。若所有文字总宽>S，官方退回等宽却仍按自然宽画文字，会互压；本库的窄宽合同见 [CTL-SEGMENTS](../exceptions.md#ctl-segments)。来源 `T/ui/widgets/discrete_sliders.cpp:340-371`。

段起点采用累计浮点宽再取整：第i段left为 `floor(p+Σ前段浮点宽)+i*g`，width为 `round(p+Σ到本段浮点宽)−(left−i*g)`；不独立round各段而累积误差。条矩形 `(left,5,width,3)`，半径1，无描边/阴影；文字行顶17、按自然宽在段内居中，基线=17+实际ascent。来源同文件 `:324-336,437-450,467-507`。背景透明；段间3保持空白。

active范围初始为当前段整个宽度。过渡期间把每段条按活动范围与该段相交部分绘成active，其余inactive；label按活动左沿与本段活动左沿距离的归一化权重混色，不只是新旧两段立即交换色。该权重 `1−clamp(|活动左−段左|/max(min(段宽,活动宽),1),0,1)`。来源同文件 `:445-490`。1是避免零宽比例的算法量，不是设计间距。

## 状态、动效与提交时机

按下即setSelected，拖动跨段实时移动预览；释放后setActive。代码先检查上次动画截止时刻再发激活，所以按下后150ms内释放会等到该截止时刻；长按超过150ms再释放立即发。无ripple模式不能误写成“只有同段按下/释放才切换”。来源 `T/ui/widgets/discrete_sliders.cpp:23-59,193-253,279`。

left/width各150ms linear，从当前动画值接续到目标；没有弹簧、背景淡入或刻度点。resizeSections/refresh停止动画；初始setActiveSectionFast直接终态。来源同文件 `:46-59,143-173,228-241,316-338`；动画接续 `L/effects/animations.h:471-479`。宿主回调保存值，重构建不能重启同一目标动画。

键盘、disabled项、稳定ID、超窄防重叠、ReduceMotion和空集合是本库通用扩展，完整合同见CTL-SEGMENTS：方向/Home/End只在可用项中移动，Enter/Space提交；disabled整个控件不预览/提交；单项可显示，零项占高0且不伪选。项重排保留选中ID，不按官方内部index泄漏身份。

## 后续验收输入

1/2/多段、等宽与不等宽标签、ΣTi=S±1、strictSkip；按下/拖动/快释放/长按释放、75ms反向、初始化与resize；普通/设置配色、禁用、键盘、RTL/明暗/三档缩放。量39高、条y5高3、文字top17、间隔3；无圆钮和数值滑块。

## theme 要补的

| 建议键 | 值 / 来源 |
|---|---|
| segments.height / padding / barTop / gap / barHeight / radius / labelTop | 39/0/5/3/3/1/17；`L/widgets/widgets.style:1177-1185` |
| segments.strictSkip | 0，默认未覆写；宽度解释 `T/ui/widgets/discrete_sliders.cpp:358-366` |
| segments.outerLeft/Top/Right/Bottom | 21/7/21/4；`T/settings/settings.style:91` |
| motion.segments | 150ms linear；`L/widgets/widgets.style:1189`；`T/ui/widgets/discrete_sliders.cpp:237-241` |

颜色和typography.body已有；gap和条高虽同为3仍是两个独立用途。不能复用motion.sliderHover来表达选择完成；同数值不等于同职责。无数字值域/档位数theme常量。
