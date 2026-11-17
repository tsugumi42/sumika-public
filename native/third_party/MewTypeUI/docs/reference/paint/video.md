# Video 显式播放标记（mw-gaps2）

## 任务卡

用户目标：未知时长的视频仍显示真实类别和播放入口，宿主可按播放/封面状态明确隐藏标记。入口：消息视频封面；作用域为该视频，数据和播放由宿主负责，无全局设置。ready/remote/loading/failed、空/非空角标、显示/隐藏播放标记均验收。

## 画法与状态

参考 T/history/view/media/history_view_gif.cpp:828–886：中心控制由当前播放/获取动作选择，播放与角标文本不是同一个开关；就绪且非自动播放时显示播放图标，下载/取消状态优先。圆44、图标槽24、颜色和浮点共圆心沿用 content.mediaControl 及已登记 Lucide 替代（content.md、centering.md）；尺寸来自 chat.style:384,427，角标Inset4和Padding8/2来自:102–103。本次不改轮廓与尺寸。

新增 VideoOptions struct { MediaOptions; ShowPlay bool }；Video(c,m,duration,o) 改收 VideoOptions。ShowPlay 控制 Ready 时中心播放标记，空时长也可显示；非空角标与 ShowPlay=false 可共存。Remote/Loading/Failed 的下载/取消/重试控制始终由 Media.Status 决定。所有调用方显式迁移，不保留基于 badge 非空的隐式推断。

GIF/Photo 的原有中心控制合同保留。视频的带正文高度扩展仍按视频种类采用4/3上限，即使duration为空也不退回照片规则。角标为空不绘角标；宿主可传真实“视频”类别，不能伪造0:00。

## 动效与参考边界

播放标记显隐沿用既有媒体控制的即时切换：进入/退出即时，下一状态立即打断，不增加等待；加载进度继续按album-state.md的350 ms追赶和3000 ms旋转，ReduceMotion直接目标。显式ShowPlay是我定的API，表示宿主准备的播放标记显示状态；点击是否交付由OnOpen回调决定，不把空角标当业务能力。

参考Telegram将播放动作与文本状态分离的做法；复用已有Go媒体构造/图标/进度，不复制上游代码或资源。
