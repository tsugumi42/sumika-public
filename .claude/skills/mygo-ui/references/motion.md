# 状态与动效

使用时机：控件反馈、展开、切页、消息到达、浮层进退场。所有 API 和数值核对 M v0.7.4；版本见 sources.md。

## 先定离散状态

区分业务 pending/running/done/error、视觉 hover/pressed/focus/selected、动画中间值。按下优先于 hover，选中记录优先于普通 hover；disabled/loading 表达可操作性。L 的非活动选中用另一颜色，不能仅靠 hover 假装选中。出处：M ui/widgets.go:155–190；L desktop/sidebar.go:460–483、composer.go:388–432；L 为 GPL，仅状态关系；[源码]。

## 按变化选 API

| 变化 | v0.7.4 API | 行为/取值 | 出处 |
| --- | --- | --- | --- |
| 目标数值 | AnimateTo(key,target,Animation) | 首个 target 就是初值；更换目标保留 spring 速度 | M docs/ui/motion.md:5–53；[源码] |
| 颜色 | AnimateColor | 预乘 alpha 混合，不是 main 新增的 Oklab 语义 | M docs/ui/motion.md:59–65；[源码] |
| 平移/缩放/旋转/淡出 | AnimatePose | 动画帧只 paint；Pose.Scale=0 保持大小，Fade=0 不透明、1 消失 | M docs/ui/motion.md:67–96；[源码] |
| 布局变化 | Transition(ElementTransition) | 默认 Position/Size/Colors 全开；显式字段只动该组 | M ui/transition.go:20–50；[源码] |
| 增删 | Transition.Enter/Exit *Motion | 父在上一帧已存在；退出副本不占布局也不接输入 | M docs/ui/transitions.md:92–123；[源码] |
| 多段可暂停/倒放 | Timeline/Tween | 共享播放头；仅编排需要时用 | M docs/ui/motion.md:297–393；[文档] |

MIT API 组合：bg 来源于已定义 Theme，不另造色值；需 import time。

```go
b := ui.ButtonBase(c.Key("save")).Label("保存").Padding(6, 14)
bg := c.Theme().Surface
if b.Pressed() { bg = c.Theme().SurfacePressed } else if b.Hovered() { bg = c.Theme().SurfaceHover }
b.Background(b.AnimateColor("background", bg,
    ui.Animation{Duration: 120 * time.Millisecond})).Children(func() { ui.Text(c, "保存") })
```

出处：M docs/ui/motion.md:59–65、ui/widgets.go:155–190；MIT，独立组合；[源码]。动画 key 是单元素内部的通道名；元素身份必须稳定。不要每帧换随机 key。M docs/ui/motion.md:18–22；[源码]。

## 取值和零值不能混

- Animation.Duration=0 选择弹簧；Smooth/Snappy/Bouncy 默认约 500ms，Bounce 分别 0/.15/.3；Duration>0 为曲线，Ease=nil 用 EaseOut。物理参数 Mass/Stiffness/Damping 与 Duration/Bounce 两套不可混填。M ui/motion.go:9–61、77–80；[源码]。
- Spring.Precision 默认 .001，单位是被动值的单位；不能把所有缩放/角度都粗化为 1 DIP。M ui/motion.go:51–61、70–75；[源码→推断]。
- ElementTransition 未设 Animation 时，Duration<=0 都回退到200ms，Ease=nil 为 EaseOut；负值也不是「立即到位」。AnimateTo/AnimateColor 的负 Duration 或 Delay 则会 panic，不能把 A 路的负时长建议当开关。M ui/transition.go:190–220、70–71；ui/motion.go:302–305、466–469；[源码]。
- Motion.Opacity=0 是透明，Scale=0 保持大小；与 Pose.Fade 的含义不同。M ui/transition.go:55–67、docs/ui/motion.md:84–87；[源码]。

L 首张 sheet 淡入/scale .96 为 150ms，上层 sheet 直接显示，遮罩一份；这是该产品参数，不是 mygo 强制默认。L desktop/overlay.go:74–98；GPL，仅取值；[源码]。切页时长另查 M navigation.md，别用 sheet 参数冒充所有页面转场。[推断]。

## 减少动态与停止

ReduceMotion 时 AnimateTo 直接到目标；Transition 保留颜色与不塌缩的淡入淡出、取消位移和塌缩。v0.7.4 没有 Animation.NonMotion。自绘 shimmer、脉冲、媒体播放分别判断偏好与可见性，不能推断框架替所有 Painter.AnimationFrame 做了保护。M ui/motion.go:299–322、ui/transition.go:85–88；L desktop/avatar.go:101、136、chat.go:1037 是未统一处理的观察；[源码→推断]。

先建初始静止状态再改变目标；AnimatePose 首次目标不会自动变成入场。新增元素在既有父层上用 Enter；虚拟 List 的行本身不执行普通布局进退场，内容的业务入场另管。M docs/ui/motion.md:18–22、docs/ui/transitions.md:113–123、167–169；[源码]。

验收用 Advance 推进中段、反向和结束，检查几何/透明度/标题内容，静态末帧图不能证明动过。普通短动效收敛后 FramePending=false；自定义持续动画注明停止条件，ReduceMotion 再验一次。M docs/ui/testing.md:52–79；[源码→项目验收]。
