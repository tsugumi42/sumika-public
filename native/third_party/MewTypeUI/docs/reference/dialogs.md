# Dialogs 与 Telegram 对照

版本沿用 README.md：tdesktop 250fc73、lib_ui 1ff7be7。所有数值是 100% 下的 DIP；生成表 theme-metrics.md 记录新增的 conversation/search/sidebar/activity token，每个字段可由主题覆盖。颜色沿用 theme-colors.md 的 conversation.*、control.filterInput*、sidebar.*、search.bar*，包括 light/night 和普通／悬停／选中态。

| 组件 / 状态 | 来源 | 本库使用 |
|---|---|---|
| 普通行 | dialogs/dialogs.style:91-102 | 62 高，46 头像，10/8 留白，标题 68/10、预览 68/34 |
| 标签行 | dialogs/dialogs.style:114-118 | 72 高、预览 top=30、tag top=52 |
| 论坛／带标签论坛 | dialogs/dialogs.style:119-128 | 80/96 高、预览 top=32、topic=21、tag top=77 |
| 未读胶囊 | dialogs/dialogs.style:78-80；dialogs/ui/dialogs_layout.cpp:197-244 | 高 19，水平留白 5，12 bold；未读优先于置顶 |
| 窄栏 | window/window_session_controller.cpp:2754；dialogs/ui/dialogs_layout.cpp:304-319,556-560 | 10+46+10=66；角标右边=56，top=35 |
| 草稿优先级 | dialogs/ui/dialogs_layout.cpp:1144-1159,665-768 | 有未读或论坛时显示消息；输入状态覆盖草稿 |
| 时间／发送状态 | dialogs/dialogs.style:88-89,404-413；dialogs/ui/dialogs_layout.cpp:825-864 | 日期间隙5、发送状态槽20；sending/sent/read，保存草稿使用sending |
| 类型图标／标题让位 | dialogs/dialogs.style:387-396；dialogs/ui/dialogs_layout.style:40-44；相应 PNG 画布尺寸 | 群组16×10+(1,4)、频道12×11+(3,4)、机器人16×11+(1,3)；标题增加图宽+x偏移+3 |
| 静音标记 | dialogs/ui/dialogs_layout.style:107-112；dialogs_mute.png | 8×8，名称后间隔4，竖直居中 |
| 在线点 | dialogs/dialogs.style:185-188；dialogs/dialogs_row.cpp:286-305,675-696 | 10 点、2 描边、底部偏移2、150ms linear |
| 搜索框 | dialogs/dialogs.style:314-339 | 高35、圆角18、文本边距12/8/30/5、普通/聚焦边框1.5/1 |
| 清空按钮 | dialogs/dialogs.style:347-363 | 35×35，图形内缩12；真实输入／清空后恢复焦点 |
| 分组标题 | dialogs/dialogs.style:808-818 | 高28，文字14/5，searchedBarBg/Fg |
| 竖栏 | window/window.style:229-245；lib_ui/ui/widgets/side_bar_button.cpp:153-219 | 宽72，最小高62，文字top40、左右6；最多三行 |
| 竖栏角标 | window/window.style:238-244；lib_ui/ui/widgets/side_bar_button.cpp:200-219 | 12 semibold、高17、stroke2；x=min(width/2+3,width-badgeWidth-10)，y=7 |
| 竖栏模式 | window/window.style:247-255 | 文字模式44高/top15；图标模式54高/图标居中 |
| 锁定文件夹 | lib_ui/ui/widgets/side_bar_button.cpp:20,174-175 | 前景、图标及角标按0.6透明度绘制；背景保持不变 |
| 按下涟漪 | lib_ui/ui/widgets/widgets.style:705-706；ui/effects/ripple_animation.cpp | 扩散650ms easeOutQuint，松开200ms linear，限制在整行矩形 |
| 输入动画 | lib_ui/ui/widgets/widgets.style:1337-1344 | 周期800、半周期320、三点错开150；间距6、半径28/12和16/12 |

列表使用 mygo v0.7.4 的虚拟列表、ID identity、键盘选择、可见区滚动、分组吸顶、右键菜单和拖动排序机制。宿主提供顺序、过滤、资源、时间和本地化字符串，不从组件内推导协议状态。可见元素不持久化。

群组、发送状态等图形使用Lucide，默认文件夹使用本库独立实心路径：各类型及发送状态分别使用源PNG的宽高和样式偏移，置顶／认证使用20画布、竖栏使用36画布。图形轮廓不同，见exceptions.md；本库未复制这些参考资源。CrychicUI的函数式Options/API和画廊登记用法仅供研究；Lucide SVG path数据与原创路径的许可见THIRD_PARTY_NOTICES.md。

## 剩余状态的源码取证（2026-11-03）

- 故事环：dialogs_row.cpp:520-578；dialogs.style:702-703 的未读/已读线宽为 2/1 DIP，头像内缩为未读线宽的 1.5 倍。outline_segments.cpp:20-35 使用最多50段、10度间隔和逆序分段；未读渐变取 groupCallLive1→groupCallMuted1，选中时取 dialogsUnreadBgMutedActive。论坛取圆角外圈。
- 角标：dialogs_row.cpp:462-489、585-697。窄栏计数优先；订阅优先 community，再是在线/通话，再是 TTL。dialogs.style:185-213：在线10、通话16、订阅16、community16、TTL20；在线描边2、订阅轮廓1.5、community线宽1。TTL 使用头像局部模糊、对比不足4.5时加20%黑。
- 群通话频谱：send_action_animations.cpp:491-565：3200ms八阶段，400ms进入/退出，三根圆头柱，笔画2 DIP；闲置三根等高。
- 活动动画：lib_ui/ui/widgets/widgets.style:1346-1363；send_action_animations.cpp:197-229、330-374、635-697。录音四段30度弧，500ms向外移动；上传三个向右箭头，500ms移动；选贴纸双眼2000ms，分段圆形缓动。尺寸与位置登记在 theme-metrics.md，时间登记在 theme-behavior.md。
- 推广：dialogs_layout.cpp:570-587、642-664；右上推广标签替换日期，自定义推广文案覆盖消息预览。验证码专用行：dialogs_layout.cpp:903-934，专用标题与头像，普通计数和选中行为保留；本库使用 Lucide 图形，不复制专用素材。
- 搜索范围：chat_search_in.cpp:266-475；dialogs.style:474-489。顶部28 DIP分组标题，范围/发送者各38 DIP加1 DIP分隔线；头像28、左10、间隔10、名字top9、下拉箭头间隔4。范围菜单支持当前项勾选，取消范围和取消发送者分别回调。

参考 Telegram 的状态优先级、分段时序和布局；参考 CrychicUI 的 Options/API 及画廊结构；实际实现使用 mygo 的稳定身份、绘制和原生输入，不翻译 C++ 代码。

## details-b2：图标与文件夹切换

- 图标：`ui/filter_icons.style:50-51,80-81` 定义 foldersAll/foldersCustom 的普通与选中色；`window/window.style:229-245` 定义72宽、图标top6。采用独立实心文件夹路径，36 DIP槽不变，不分发上游素材。
- mygo v0.7.4 `ui/paint.go:164-170` 的 snap 乘设备比例；`ui/svg.go:172-173,202-209` 的图标缓存键包含物理宽高；图片路径 `:263-274` 同样以物理宽高缓存。125%无需额外放大或二次采样。
- 文件夹内容切换调用 `dialogs/dialogs_widget.cpp:3063-3073` 的 Window::SlideAnimation；`window/window_slide_animation.h:43-44` 使用 anim::easeOutCirc，`.cpp:196-211` 使用 slideDuration；`lib_ui/ui/basic.style:96-98` 为240ms、slideShift=100 DIP。`.cpp:115-151` 旧页只画新页边界左侧，旧页位移为100×进度，新页位移为宽×(1−进度)，旧页叠加slideFadeOutBg；反向交换上下层。因此原始完整页面可重叠，可见裁剪区域不能重叠。


验收：`dialogs/pages_test.go`以10ms步进采样双向240ms切换，检查可见裁剪区互斥、easeOutCirc边界、精确结束、点击屏蔽、反向打断、窗口缩放和ReduceMotion；主题值由basic.style:96-97与window_slide_animation.h:43-44对照。结构上两块裁剪区共享同一边界，连续时间下也不可能同时绘制同一区域。100/125/200%切换后的图像逐字节匹配各自新建Tester，排除错误复用小图缓存。

文件夹底边实测：125% Tester中y=45为#8393a3、y=46为#293a4c，中间过渡像素为0；100%原生PrintWindow中y=88为#5eb5f7、y=89为#17212b。旧版中心为空、底笔画仅两行实色加两行抗锯齿；改善来自实心轮廓而非修改mygo像素比。原生只枚举并捕获测试进程自己的HWND。

工具会话只有1920×1080、96 DPI显示器；未伪造2560×1440/125%原生截图。该条件的原生复核需要主会话在指定屏幕完成；mygo不需要上报像素比缓存错误。画廊文件夹页与完整聊天组合已实点横向标签/竖栏，工作分类实际切换为独立列表。

