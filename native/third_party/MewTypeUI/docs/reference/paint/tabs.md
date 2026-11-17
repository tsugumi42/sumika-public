# 横向标签页画法规格

ctl-spec；基线、T/L、DIP、RTL和净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。官方同版同时存在defaultTabsSlider下划线标签和Info Profile新的岛状标签，不能把二者拼成一个外观。本页分别规定两个显式变体。参考资料页“标签只发ID、页面内容另持状态”的结构，来源 `T/info/profile/tabs/info_profile_tabs_strip.cpp:79-139,411-424`；Info内下划线调用见 `T/info/bot/earn/info_bot_earn_list.cpp:315-345`。

## 下划线变体

| 项 | defaultTabsSlider数值 | 来源 |
|---|---|---|
| 总高 / barTop | 43 / 40 | `L/widgets/widgets.style:1192-1194` |
| gap / stroke / radius | 0 / 6 / 2 | 同文件 `:1195-1197` |
| 普通条 / 吸附 | transparent / barSnapToLabel=true | 同文件 `:1198-1199` |
| 标签top / 字体 | 12 / semiboldTextStyle=13/600 | 同文件 `:1200-1201` |
| 普通/active文字 | windowSubTextFg / lightButtonFg | 同文件 `:1202-1203` |
| ripple | 整段，底部保留1；windowBgOver→lightButtonBgOver | 同文件 `:1204-1207` |
| 选择 / ripple时长 | 继承150ms linear / 650 Quint、200 linear | 同文件 `:1189,1207`；[settings-slider](settings-slider.md) |

段宽与取整完全引用settings-slider。活动宽 `min(段宽,文字自然宽)`，在段内居中。下划线从该范围左右各扩 `stroke/2=3`，再夹根左右边界；矩形y40、高6、半径2，根裁切到43，因此实际只见上面3高，不能把stroke6写成可见6。来源 `T/ui/widgets/discrete_sliders.cpp:155-173,510-518`。无全宽灰底线，无外边框。

文字top12，颜色过渡权重沿用settings-slider。ripple遮罩是段宽×42的矩形；除显式设置首尾上圆角外，没有胶囊底。默认有ripple，因此按下不移动选中，只有同段释放才激活；与无ripple SettingsSlider的按压预览不同。官方setActiveSection先触发激活再设selected，本默认点击路径可立即回调、随后条动画；不能强制所有tabs等动画150ms才通知。来源 `T/ui/widgets/discrete_sliders.cpp:23-40,193-241,270-280,401-428`。

## Info岛状变体

| 项 | infoProfileTabsStrip数值 / 角色 | 来源 |
|---|---|---|
| 总高 / margin | 48 / (8,6,8,6) | `T/info/info.style:718-720` |
| 内skip / tabPadding | 0 / (16,0,16,0) | 同文件 `:721-722` |
| activeSkip | 4 | 同文件 `:723` |
| 字体 / 文字 | 13 semibold；windowSubTextFg / lightButtonFg | 同文件 `:724-726` |
| 岛底 / active底 / ripple | windowBg / lightButtonBgOver / lightButtonBgRipple | 同文件 `:727-730` |
| active动画 / 滚动 | 200ms easeOutQuint / 150ms easeOutCirc | 同文件 `:732`；`T/info/profile/tabs/info_profile_tabs_strip.cpp:229-234,257-262` |
| 阴影 | blur8、offset(0,2)、opacity.18 | `T/info/info.style:734-737` |

根宽W，岛框 `(8,6,W-16,36)`，半径18；每项自然宽 `Ti+32`、高36，连续排列不等分；active框为该项四边内缩4，即高28、半径14，x随项宽变化。文字x=项左+16，y=岛顶+floor((36−实际行高)/2)。先阴影、岛底、被岛圆角裁切的active/ripple/文字、最后岛边线；均高质量抗锯齿。来源 `T/info/profile/tabs/info_profile_tabs_strip.cpp:79-124,169-191,427-509`。

活动框x/y/w/h从当前框向新框200ms Quint插值；旧新标签颜色同一进度反向混合，首次激活直接终态，无active时不画选中底。无hover独立底，按下ripple裁于各项内缩后的胶囊。按下后拖动超过平台startDragDistance转横向滚动，释放不激活；普通同项释放报告ID；右键报告该项ID。来源同文件 `:207-238,286-302,320-340,392-424,468-509`。

内容超宽时在岛内水平裁切，scrollMax=max(Σ(Ti+32)−(W−16),0)。新选项不可完整可见时居中滚到该项并夹端点；滚动150ms Circ。横向触控/滚轮遵守主轴锁，竖向普通轮需距最近内容竖滚250ms（鼠标明确瞄准标签）或1000ms（未瞄准）后才改为横滚；有滚动phase的竖势保留给页面。来源同文件 `:240-277,342-389`。

岛边线与两端渐隐是原版额外层：亮底0.6×lineWidth黑alpha20/255，向外半笔；暗底1×lineWidth白色竖渐变40→0→20/255，向内半笔。分界以底色lightness≥128；来源同文件 `:29-51,449`。边缘fade使用受限fade_horizontal资源，透明度为距端剩余滚动量/(fade宽×.5)，夹[0,1]，来源同文件 `:512-529`；本库替代方式、渐变宽和素材差异明确见 [CTL-TABS](../exceptions.md#ctl-tabs)。

## 共享状态、键盘与可访问性

持久active ID、各页草稿/滚动/惰性读取由宿主保存；高亮/pressed与active独立。重排保留ID；空数组根高0，不擅选首项。禁用项不可提交，Tab只进入当前可用项；Left/Right按界面方向、Home/End移动焦点，Enter/Space提交；采用手动激活以免焦点移动触发宿主网络读取。这些键盘/disabled扩展、减少动效与RTL映射登记CTL-TABS。下划线在文字总宽不足时采用CTL-SEGMENTS的横滚合同；不隐藏标签以凑宽。页面切换动效归layout，不能把条200ms当内容动画。

## 后续验收输入

两变体分别验：短/超宽/空项、初始active、切项/反向/重排、按下拖出/横拖不触发、右键、竖滚保护；键盘禁用、明暗/RTL/三档缩放。下划线y40只见3高；岛框36、active28、文字自然宽+32、端部裁切；0/75/150或0/100/200ms位置颜色。未作本页UI实现和叠图。

## theme 要补的

| 建议键组 | 数值 / 来源 |
|---|---|
| tabs.height/barTop/gap/barStroke/radius/labelTop/rippleBottomSkip | 43/40/0/6/2/12/1；`L/widgets/widgets.style:1193-1204` |
| profileTabs.height/marginLeft/Top/Right/Bottom/tabPadding/activeInset | 48/8/6/8/6/16/4；`T/info/info.style:719-723` |
| profileTabs.shadowBlur/shadowY/shadowOpacity | 8/2/.18；同文件 `:735-737` |
| profileTabs.lightStrokeRatio/lightStrokeAlpha/darkTopAlpha/darkMidAlpha/darkBottomAlpha/lightnessThreshold | .6、20/255、40/255、0、20/255、128；`T/info/profile/tabs/info_profile_tabs_strip.cpp:34-42` |
| profileTabs.fadeFactor | .5；`T/info/profile/tabs/info_profile_tabs_strip.cpp:515`；替代渐变宽复用tabPadding16，见CTL-TABS |
| motion.tabs / profileTabs / profileTabsScroll | 150 linear / 200 Quint / 150 Circ；本页上述来源 |
| motion.profileTabsWheelGrace / profileTabsWheelRest | 250/1000ms定时器；同文件 `:25-27` |

普通tabs颜色和13/600字阶、ripple已有；岛outline白/黑是新用途色，应建立profileTabs专用颜色角色，不把固定RGBA写进组件。fade宽复用本页tabPadding16、fadeFactor=.5取源码，替代渐变见CTL-TABS；算法端点0/1不做theme数值。
