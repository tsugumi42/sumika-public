# 通用图标按钮画法规格

ctl-spec；版本、DIP、T/L、RTL、净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。参考官方 IconButton 的“外框命中、圆形ripple、图形画布独立”结构，避免把外框高当圆直径。来源 `L/widgets/buttons.cpp:618-723`。按钮根和必填可访问名称参考 `CrychicUI/button/icon_button.go:10-27`；不借其40方框、1边框、16图标、Snappy和点击音。

## 样式与几何

defaultIconButton只给iconPosition=(-1,-1)，没有通用默认尺寸/图形/时长；具体用途必须选择有来源的样式。本库独立默认采用官方backButton，顶栏动作则保留自己的专用样式，不能凭感觉补统一44圆底。来源 `L/widgets/widgets.style:1064-1066,1487-1503`。

| 用途 | 外框 / 图形定位 / ripple | 来源 |
|---|---|---|
| 独立backButton | 40×40；iconPosition继承(-1,-1)，双轴居中；ripple40、起点(0,0) | `L/widgets/widgets.style:1064-1066,1487-1497` |
| Info返回 | 60×54；x=11、y居中；ripple42、起点(6,6) | `T/info/info.style:138-149` |
| Info关闭 | 48×54；x=5、y居中；ripple42、起点(0,6) | `T/info/info.style:158-166` |
| 聊天顶栏 | 外框40×54（更多44×54）、ripple40 | 直接引用 [chat](../chat.md#组件与状态)，不重写专用Header |

backButton的图标使用menuIconFg/Over，ripple=windowBgOver；图形本身是允许分发的替代资源，画布尺寸随资源。独立按钮默认24×24 Lucide画布是本库已有素材替代选择，见 [CTL-ICON](../exceptions.md#ctl-icon)。

设外框W×H、图形iw×ih：iconPosition某轴<0时该轴居中 `floor((外框−画布)/2)`，非负则按显式偏移；ripple在自己的 `(rx,ry,d,d)` 椭圆遮罩内。命中范围为整个外框，但按下点不在ripple框内时不起涟漪，仍允许正常点击。先ripple再图形；常态透明，不常驻绘圆底/矩形描边/阴影。来源 `L/widgets/buttons.cpp:648-694,712-723`。

## 状态与动画

| 状态 | 画法 / 行为 | 来源 |
|---|---|---|
| 普通 | icon，透明底 | `L/widgets/buttons.cpp:648-682` |
| hover | 有duration时普通图形保持，Over图形以a叠加；a=1只画Over。无duration即时切 | 同文件 `:642-705` |
| down / forceRippled | Over强度直接为1；forceRippled允许保留淡化涟漪，如菜单已打开 | 同文件 `:138-165,642-645` |
| 按压/松开 | ripple 650ms Quint/200ms linear；裁在上述圆区 | 同文件 `:185-205,712-723`；`L/widgets/widgets.style:703-706` |
| 禁用 | 不响应激活、不起新波；mygo的0.5平台差异沿用SET-DISABLED | [例外](../exceptions.md#set-disabled) |

默认backButton和Info样式未指定duration，因此不凭惯例填150ms。显式非零样式才按其duration用线性a；没有图形Over时不做无效双绘。图标颜色override优先；不因hover改图形框。可访问名称必填，tooltip由宿主传；焦点、Enter/Space走按钮原语。初始active不重播，ReduceMotion直接终态属于本库映射。

## 后续验收输入

三种用途、不同图形画布；圆区内/外按下、拖出释放、forceRippled菜单开闭、disabled；默认0ms与显式duration；RTL、明暗、100/125/150%。分别量外框、ripple、图形画布和中心；验证≤1 DIP，不拿非对称墨迹质心代替画布中心，见 [centering](../centering.md)。

## theme 要补的

| 建议键 | 值 / 来源 |
|---|---|
| iconButton.width / height / rippleSize / rippleLeft / rippleTop | 40/40/40/0/0，backButton；`L/widgets/widgets.style:1487-1503` |

iconButton居中图形为CTL-ICON扩展配置；现有header.iconSize=24、menu.iconForeground/Hover、window.backgroundHover与rippleShow/Hide复用。Info返回/关闭几何在page-title登记。默认hover无动画，不新增猜测的motion。
