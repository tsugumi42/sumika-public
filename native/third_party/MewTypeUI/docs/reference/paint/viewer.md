# Viewer 固有尺寸和点击契约（mw-gaps2）

基线：tdesktop 250fc73、lib_ui 1ff7be7；单位为 DIP。仅记录结构、公式和行为，不包含上游实现代码。

## 任务卡

用户目标：在独立查看器里打开图片，缩略图换原图时尺寸稳定，双击在适应窗口和原始大小间切换，打开时不被残留点击关闭。入口为消息媒体 → 窗口级 Viewer；资源、ID 和打开状态归宿主，缩放与拖动作用于当前 ID，即时生效，无全局配置或持久化。复用现有 viewer 画廊；验收键盘、鼠标、加载/失败/就绪、重排和删除。

## 图片矩形

参考 OverlayWidget::resizeContentByScreenSize，T/media/view/media_view_overlay_widget.cpp:2817–2857：固有宽高 W、H 独立于已加载的显示位图；适应比例 f=min(窗口宽/W,窗口高/H)。首次显示大图采用 f，小图采用 1；图片中心为窗口中心加 Pan。离散缩放和边界夹取沿用 overlay.md 的合同。IntrinsicWidth/IntrinsicHeight 同时为正时使用这对宿主尺寸，否则 Ready 使用位图尺寸。Loading/Failed 可带尺寸，不生成伪造位图。

同一 ID 换更清晰位图，不重置 Zoom/Fit/Pan。首次 Ready 仍按上述规则初始化。原始大小指宿主声明的逻辑尺寸，宿主自行从物理像素和媒体缩放换算。

## 双击

参考 zoomReset 的原始大小/适应切换与重新居中，T/media/view/media_view_overlay_widget.cpp:2976–3006；事件计数使用 mygo v0.7.4 ui/handler.go:29–33 的 Clicks。双击左键图片执行一次重置，第二次按下不启动拖动；单击图片不关闭。

差异：该固定 Telegram 版本 handleDoubleClick 只处理视频全屏（同文件:8158–8182），图片双击缩放是本任务明确要求。双击复用现有 Ctrl+0/中键的即时切换，不引入额外缩放动画；进入/退出均即时，后续手势立即打断，ReduceMotion 相同。

## 打开保护

每次 Show 或从关闭变为打开，记录查看器首次构建时间；保护窗口固定为 500 ms，这是输入有效性合同而非绘制动效。该期间空白点击和关闭按钮点击不关闭；Esc、宿主主动 Close、失焦、删除当前 ID 仍按既有生命周期关闭。保护只在打开时重置，翻页和资源到达不延长。

差异：500 ms 来自用户任务，未在本版本上述点击释放路径（同文件:8658–8684）找到对应常量；登记 exceptions.md。它是等待时间，不插值，也不因 ReduceMotion 取消。

## 参考取舍

参考 Telegram 的固有几何、适应比例、缩放重置，及 CrychicUI overlay/lightbox.go:17–18,35–48,82–89 的宿主资源/打开状态边界与键盘导航。没有借用 CrychicUI 实现、资源或任意动画参数。原生窗口仍按 overlay.md 即时开关；控件、加载环保留原有独立动画合同。
