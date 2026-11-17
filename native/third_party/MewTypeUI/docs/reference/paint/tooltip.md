# Tooltip 悬停与键盘焦点（mw-gaps2）

## 任务卡

用户目标：鼠标停留或键盘 Tab 到达控件时读到同一提示，Esc 关闭后继续操作原控件。提示仅补充说明，必要状态仍常驻。作用域为单个 anchor 的临时提示，入口为悬停/键盘焦点，无持久配置；宿主提供文字和 anchor，库持有触发/淡化状态。

## 触发与定位

TooltipOptions.Trigger 为位标志 Hover / Focus，可组合；零值默认为 Hover|Focus。Hover 使用已有 Motion.TooltipDelay=1000ms，Delay 可覆盖；Focus 仅当 anchor.Focused() 且 FocusVisible()（键盘来源）时立即显示，鼠标点击获取焦点不显示。焦点触发定位在 anchor 底部中心，悬停仍按指针与 tooltip.ShiftX/ShiftY/EdgeSkip 定位。相同宿主最多一份，不抢焦点，不接收鼠标。

Telegram 固定来源：L/widgets/tooltip.cpp:42–47 的独立 ToolTip 窗口、:53–63 的有效宿主与文本、:99–120 的尺寸与位置、:150–174 的屏幕边缘夹取、:177–197 的高质量圆角与边框。字号/留白/圆角/前景沿用 overlay.md 与 theme-metrics.md。Focus 来源参考 mygo v0.7.4 ui/tooltip.go:36–43,145–167 的键盘焦点触发；这是本任务要求的可访问性扩展，不声称 Telegram 有相同键盘路径。

## 关闭与焦点

Esc 消费一次并锁住当前触发，直到悬停和键盘焦点都离开后才能再次显示；anchor 按下/激活同样关闭。Tab 离开关闭并自然把焦点给下一控件，不把焦点拉回旧 anchor；Esc 保留当前 anchor 的焦点，因为提示窗口从不激活。禁用、空文案、菜单打开、宿主失焦、移除组件均关闭。

## 动效

沿用已登记扩展 Motion.Fade=200ms linear 淡入，并补同一时长淡出。逻辑关闭立即停止提示命中（始终穿透），保留原文字/背景到淡出结束再销毁；关闭期间同 owner 再进入从当前 alpha 反转，不重建窗口或先清空文字。新 owner/新文案替换仍即时释放旧窗口，宿主关闭也即时释放。ReduceMotion 或零时长立即终态。进入/退出/打断的实测使用原 render 路径和真实键盘事件；截图只证明静态成像。

官方普通 tooltip 在 :174 直接 show，新增淡化为我定的既有映射扩展，登记 exceptions.md；不把淡化当官方行为。
