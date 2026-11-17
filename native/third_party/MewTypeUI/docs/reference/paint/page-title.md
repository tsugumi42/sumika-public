# 页面标题画法规格

ctl-spec；版本、T/L、DIP、RTL与净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。参考Info TopBar和Settings Intro的固定顶区与右侧操作独立布局，来源 `T/info/info_top_bar.cpp:79-149,395-443,505-515`、`T/settings/settings_intro.cpp:119-174`。设置小节标题继续引用settings-section；聊天标题继续引用 [chat](../chat.md)，不把这两者冒充任意页面大标题。

## 官方页面顶栏数值

| 项 | 常量 / 数值 | 来源 |
|---|---|---|
| 基高 / 背景 | infoTopBarHeight54 / windowBg | `T/info/info.style:138,233-251` |
| 单标题 | infoTopBarTitle=14 semibold、windowBoldFg、maxHeight20；origin(24,17) | 同文件 `:151-156,237` |
| 带副标题 | 标题13 semibold，origin(16,8)；副标题13 regular、windowSubTextFg、maxHeight20、origin(16,28) | 同文件 `:238-246` |
| 返回槽 | infoTopBarBack.width60、高54；图形x11/y居中；ripple42、origin(6,6) | 同文件 `:139-149` |
| 关闭槽 | 宽48、高54；图形x5/y居中；ripple42、origin(0,6) | 同文件 `:158-166` |
| 尾部文字badge间隔 | badgeSkip8 | 同文件 `:247-250` |
| Layer变体 | 高56；主标题boxTitle=16 semibold、origin(24,17)；副标题版top9/30 | 同文件 `:269,355-373`；`L/layers/layers.style:73` |

以上字阶与聊天HeaderTitle13/600不同，普通设置页单标题不可直接沿用13。

## 几何和层次

根W×54，背景整宽平面，无四边卡片边框；Layer只圆上两角、半径boxRadius8。右侧动作按传入顺序从最右向左连续摆，无额外猜测gap；设总动作宽R。尾文字badge若有，在动作左边先留8、画badge，再留8，R增加badge宽+16。返回存在时占左60的标题起点（官方返回命中区可延伸覆盖标题区，但图形仍固定），无返回则使用上表单/双标题x。

标题可用宽 `max(W-R-x,0)`，按自然字体单行省略；副标题同样独立扣宽。行顶按上表，基线再加实际ascent，不能以字体size当行高。来源 `T/info/info_top_bar.cpp:395-443`。背景先画，标题/副标题/只读badge与按钮在其上；上游 `setTitle` 的只读badge透明鼠标，返回存在时标签也透明鼠标，来源同文件 `:120-146`。

标题、返回和关闭是不同语义：普通PageTitle标题不作为返回按钮；本库只让显式返回按钮命中其槽，避免误点文字导航，这是 [CTL-TITLE](../exceptions.md#ctl-title) 的映射。标题允许辅助技术heading，操作顺序仍稳定。

## 内嵌页面标题（我定的）

非固定顶栏的“标题+说明+动作”采用同一视觉材料：单标题14/600、颜色windowBoldFg，左/右24、顶17；可选说明13/400、windowSubTextFg，在标题实际行底后加defaultVerticalListSkip6；底部留defaultSubsectionTitlePadding.bottom9。标题和说明按当前可用宽自然换行；动作在右，窄到文字无正宽时移下一行，行间复用6。此组合没有官方同款，登记CTL-TITLE，不能把其总高称作官方54。

## 状态与动效

普通标题和文案改变即时更新，不自行播入场。官方Info在搜索/选择/故事状态会将标题/副标题/尾badge用FadeWrap以150ms、scale参数.7显隐；条件由所属页面定义，来源 `T/info/info_top_bar.cpp:91-139`、`T/info/info.style:264-265`。opacity线性0→1/1→0，内容快照按中心缩放而布局不变。5倍透明扩边快照使最终可见比例为 `.2+.8×(.7+.3×opacity)=.76+.24×opacity`，不能直接把.7当可见起始尺寸；整数目标矩形取整单列。来源 `L/effects/fade_animation.cpp:15,44-71,86-107,143-164`；默认线性 `L/effects/animations.h:67`。初始状态直接终态；反向从当前opacity接续，结束切回活内容。通用PageTitle只在宿主显式切模式时使用此变体，不能将所有换标题文本都缩放。父页面导航使用layout.page，不混用标题150ms。

超大字体时最小高扩展以容纳实际标题和副标题行，保持至少原来的上下空隙；该适配见CTL-TITLE，未经原生对照不称逐像素等价。隐藏系统标题栏时先让layout处理系统按钮避让，不能再在PageTitle写固定32高占位。标题本身无hover/ripple；按钮按icon-button；禁用只作用于操作，不把整页文字变淡。

## 后续验收输入

单/双标题、返回/关闭/多操作/尾badge、长标题、W-R-x=0、layer56与普通54、内嵌换行/动作换行、搜索显隐、系统按钮交叠；明暗/RTL/三档缩放。量14/13字阶差异、24/16/60起点和17/8/28行顶。未运行本页UI。

## theme 要补的

| 建议键 | 数值 / 来源 |
|---|---|
| pageTitle.height / left / top / textMaxHeight | 54/24/17/20；`T/info/info.style:138,153,237` |
| pageTitle.subtitleLeft/titleWithSubtitleTop/subtitleTop/badgeGap | 16/8/28/8；同文件 `:241,246,250` |
| pageTitle.backWidth/backIconLeft/backRippleSize/backRippleLeft/backRippleTop | 60/11/42/6/6；同文件 `:140,145,147-148` |
| pageTitle.closeWidth/closeIconLeft/closeRippleLeft | 48/5/0；同文件 `:164-166` |
| pageTitle.layerHeight/layerTitleTop/layerSubtitleTop | 56/9/30；同文件 `:269,360-361` |
| typography.pageTitle | 14/600；同文件 `:154-156` |
| motion.pageTitle / pageTitle.hiddenScale | 150ms linear / .7参数；`T/info/info.style:264-265`；实际可见比例按本页公式推导，不另存.76常量 |

单/双标题色、13/600 contentTitle、13/400 body、Layer的16/600 dialogTitle、8圆角、内嵌6/9留白均已有同源token。快照扩边复用compose.swapScale5（同值同画法），字形缩放和取整的mygo映射见CTL-TITLE，不借旧motion.control的ease-out。新字段仅待补，不修改theme源码。
