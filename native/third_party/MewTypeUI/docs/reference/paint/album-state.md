# Album 逐项状态与尺寸通知（mw-gaps2）

## 任务卡

用户目标：逐格看到真实下载进度/失败信息，打开、下载、取消、重试准确作用于媒体 ID；宿主依据实际格子尺寸选缩略图档位。入口：消息相册 → 对应格子；状态/文案/资源和动作归宿主，组件只画状态并交付意图。即时生效，作用域为单个 Media.ID，无配置存储。沿用 1–10 张输入合同及原有布局；宽窄、重排、加载/取消/失败/重试/就绪、禁用、选中均验收。

## 格子与控制

布局、外角、图像 Cover 和4 DIP间隙沿用 content.md 和 album_layout.go 的已登记合同。参考 T/history/view/media/history_view_photo.cpp:971–1036：每格中心画圆形控制，加载/上传显示取消与进度，未获取显示下载，就绪照片无中心控件。圆直径 historyGroupRadialSize=44、弧线 historyGroupRadialLine=3，来源 T/ui/chat/chat.style:511–512；与现有 media.actionSize=44、media.progressWidth=3 相同，复用 token 并追加此用途登记，不新增一对同值别名。图标槽 media.actionIconSize=24，沿用已登记 Lucide 替代；浮点共圆心与抗锯齿按 centering.md。

参考 T/history/view/media/history_view_photo.cpp:1056–1076：整格命中依状态交付打开、取消或下载。MediaFailed 以下载轮廓表示重试，回调区分 OnRetry。失败文字及重试是本任务扩展，并非官方照片逐格错误标签；附加文字遵守下节底板规格。Selected 蒙层保留，Disabled 不响应鼠标/键盘。

## 状态文字底板

无 StatusText 时，圆形控制仍独立居于整格中心。存在 StatusText 时，把圆形控制和下面的单行状态胶囊作为整体居中；两者间距及组合的水平外留白均复用 media.badgeInset=4，避免窄格文字与控制重叠。胶囊最大宽度为格宽减两侧外留白，文字超长显示省略号；完整文案保留为格子的辅助描述。

胶囊字阶沿用 MessageTime=13、内边距 media.badgePaddingX/Y=8/2；圆角为单行测量字高加两倍垂直内边距的一半。形状、字阶和留白来源沿用已登记的 T/ui/chat/chat.style:102–103 与 history/view/media/video_status.cpp:36–53，不新增数字或字号。

这是我定的宿主状态扩展：底板复用不透明 imageBg（L/colors.palette:28），前景复用 msgDateImgFg（:379），选中仍叠加整格 msgSelectOverlay（:353）。普通时间底板 msgDateImgBg（:380）仅有 0x54 alpha，白图上不足以保证操作/失败文案可读；不把新底板和组合布局称为官方照片行为。默认亮色白图上的文字/底板对比度由原先无底板的 1:1 提升到 21:1。单图与相册共用此规格。

可读性验收按[WCAG 2.2 SC 1.4.3](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html)普通文字的4.5:1门槛；Tester以真实绘制的文字像素和旁侧底板像素检查，同时量完整文字盒子与格子边界。13 DIP状态字不按大号文字放宽。

## 进度与切换

Media 增加 StatusText、Progress，原 MediaOptions 同名字段迁入模型，单图/相册一致消费；全仓迁移，不提供两份竞争数据源。Progress=[0,1]。加载圆环复用 content/progress.go：350 ms非线性追赶、3000 ms旋转，目标被打断从当前可见值追赶，ReduceMotion/Instant立即目标。来源 lib_ui/ui/basic.style:124–125、effects/radial_animation.cpp:32–69。进入/退出控件使用既有即时显隐合同（exceptions.md），不为终态等待旧动画；新加载重新开始圆环。

## 只读尺寸通知

AlbumOptions.OnTileSize func(id string, width, height float32) 在已提交布局尺寸第一次可用或变化时交付 DIP，不是图片像素。每格按 ID 保持局部通知缓存，重排不串项；宿主把通知乘自己的资源缩放，再请求缩略图。绘制回调只观察实际完整盒子的宽高（不受视口裁切）并在变化时请求构建，下一次构建比较后通知宿主；相同尺寸不重发。仅通知尺寸变化，不通知位置变化。宿主异步结果经 Window.Update 发布。通知从不在绘制回调执行。

尺寸通知借鉴 Telegram 根据 geometry.size() 选缓存，T/history/view/media/history_view_photo.cpp:1105–1133；公开回调和 DIP 单位映射为我定的 API，库不主动下载。只读指回调不获得可修改的布局缓存，不禁止宿主记录新尺寸。

## 参考取舍

参考官方 grouped Photo 的逐项几何、状态优先级和按格尺寸取缓存；保留现有 Go 布局算法、动作原语、进度路径与主题。没有移植 C++ 实现，没有新增资源加载或布局副本。
