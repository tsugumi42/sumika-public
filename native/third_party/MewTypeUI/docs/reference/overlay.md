# 浮层对照与使用契约

版本沿用 README.md 的 tdesktop `250fc73`、lib_ui `1ff7be7`。尺寸逐项登记于生成的 theme-metrics.md；颜色复用 theme-colors.md，字阶、定时器和曲线登记于 theme-behavior.md。以下描述实际构建契约，平台差异见 exceptions.md。

| 组件 | 覆盖的展示与交互 | Telegram 依据 |
|---|---|---|
| ContextMenu / PopupMenu | 独立原生 flyout、宿主外溢出、屏幕工作区翻转、普通与图标样式、禁用/危险/快捷键/勾选/单选、分隔线、逐层原生子菜单、长菜单滚动、方向保护、键盘与首字母、焦点回收、200 ms展开/150 ms退出 | lib_ui/ui/widgets/widgets.style:952-998,1690-1712; menu/menu_action.cpp:62-64,123-150; popup_menu.cpp:39,158-161,494,847-851,921; effects/panel_animation.cpp:325-412 |
| Tooltip | 不抢焦点的独立 flyout、1000 ms悬停/键盘焦点立即触发、移动重计时、按下/离开/Escape/宿主失焦关闭、禁用不显示、12行上限、主题边框/圆角、屏幕边缘翻转；200 ms淡入/淡出与原位反转 | [净室规格](paint/tooltip.md)；lib_ui/ui/widgets/widgets.style:1276-1288; widgets/tooltip.cpp:42-47,99-120,150-197; tdesktop/history/history_inner_widget.cpp:5698；焦点与淡化扩展见exceptions.md |
| Box | 有/无标题、附加标题、关闭键、普通/宽版、可滚动正文、左/右按钮、默认/危险/禁用/保持打开的操作、遮罩、模态焦点范围、Escape/遮罩关闭或锁定 | lib_ui/ui/layers/layers.style:37-139; box_layer_widget.cpp:110-117,294-310; layer_widget.cpp:205-221 |
| Toast | 贴底滑入/滑出、居中淡入/淡出、单行/多行、图标、操作按钮、有限/无限时长、重显重计时、无操作时指针穿透 | lib_ui/ui/widgets/widgets.style:1544-1565; toast/toast.h:46,85-86; toast.cpp:32-44,109-113; toast_widget.cpp:433-491 |
| Viewer | 独立全屏窗口覆盖宿主显示器（含任务栏）、图片居中与适配、原始大小、离散缩放与适配档、鼠标锚点缩放、中键/键盘重置、拖动及边界、左右翻页/首末页、关闭、顶部信息、caption、控件自动显隐、圆形加载进度/失败/就绪、重试意图、ID重排/删除 | tdesktop/Telegram/SourceFiles/media/view/media_view.style:18,266-289; media_view_overlay_widget.cpp:188-190,1309-1313,1435-1465,2580-2605,2796-2857,2942-3006,3218-3261,6614-6670,7544-7598,7663-7681,7690-7734,8152-8153; lib_ui/ui/basic.style:122-125; effects/radial_animation.cpp:22-113 |

所有组件接收 `*ui.Context`，返回当次构建的根占位元素。ContextMenu、PopupMenu、Tooltip、Viewer 必须在 `Native(c, window, content)` 内构建；Box、Toast 仍使用内容区 Overlay。Native 每个窗口调用一次、每帧调用，放在 theme.Apply 之后，绑定确切的宿主窗口，不猜测当前焦点窗口。需要宿主定位的调用在构建前使用 `c.Key(id)`；查看器身份不能跟随可滚动缩略图的创建/销毁。列表项、菜单操作、查看器图片均以稳定 ID 标识。

菜单 `MenuState.ShowAt` 接收相对 target 的位置。ContextMenu 处理右键、Menu键和Shift+F10；PopupMenu 接收已构建的 ButtonBase/Button，处理Click、Enter、Space和Down。回调仅报告叶项ID，宿主维护操作能力、勾选状态及业务结果。`Icons` 决定左54 DIP的图标布局；传入的SVG由宿主预先准备。Right/Left、Up/Down、Home/End和Escape可导航，方向随RTL调整。

Native 只保留一个菜单树和一个 tooltip；新菜单替换旧树，各层分别创建焦点型 flyout。外部按下/窗口激活由 mygo 原生关闭事件驱动，父子菜单切换焦点不关闭整个树。宿主隐藏、最小化、销毁或对应组件离开构建树会关闭相关窗口。选项、数据和主题在宿主每次构建时同步；关闭回调只交付一次，键盘关闭后把控件焦点交回 opener。组件状态与回调只能在 UI 线程访问，异步宿主更新通过 Window.Update。

`EditContextMenu(c *ui.Context, scope ui.Handle, build func(*ui.Menu)) ui.Element` 为已绑定的 Selectable 正文生成一套原生标准菜单：build 添加宿主项，库追加 Copy/Select All，标准命令由 mygo 执行并经过正文 CopyData。关闭菜单保留原段落焦点和选区；请勿再对该正文同时调用 ContextMenu。它采用原缺口建议的标准 ui.Menu 对象路径，v0.7.4 未公开直接按 Handle dispatch 的 API。系统菜单外观差异及净室依据见 [paint/edit-menu.md](paint/edit-menu.md)；示例页 `overlay/edit-menu`。

`TooltipOptions.Trigger TooltipTrigger` 使用 `TooltipHover`、`TooltipFocus` 位标志，可按位或组合；零值取默认的两者。Hover 使用 Delay（nil为主题1000 ms，指向零时长为立即），Focus 指可见键盘焦点并立即显示，不受 Delay 影响。Esc/激活抑制到该 anchor 的悬停和键盘焦点两类触发都离开；Esc保留anchor焦点，Tab正常前移，不夺回焦点。非激活提示窗口从不持有键盘。正常离开保留文字淡出200 ms，同owner返回从当前alpha反转；ReduceMotion立即终态，宿主销毁/更换owner即时释放。

```go
// win 是该 View 所属的 *mygo.Window；在首个构建前由宿主设置。
theme.Apply(c, compiledTheme)
overlay.Native(c, win, func() {
    button := ui.Button(c, "Actions")
    overlay.PopupMenu(c, button, items, &menuState, overlay.MenuOptions{})
    overlay.Viewer(c, &viewerState, preparedImages, overlay.ViewerOptions{})
})
```

mygo v0.7.4 的原生 UI 不支持 flyout FitContent，因此内容按主题字阶测量后设置窗口尺寸，过长菜单由工作区夹取并在内部滚动。Parent、Anchor、Placement、OnClose 与隐藏建窗/首帧显示的用法参考 mygo 的 flyout.go、flyout_test.go 和 examples/flyout/main.go；只借 API 使用与生命周期做法。逐层独立窗口及保持最终文字布局的揭示参考 lib_ui PopupMenu，Go 实现独立编写。

Viewer 使用独立、无边框、透明、无任务栏按钮的 owned window，覆盖宿主所在显示器的完整物理矩形（含任务栏），再进入全屏。Windows 使用 MonitorFromWindow/GetMonitorInfoW/SetWindowPos 传物理坐标，避免混用 mygo 屏幕与窗口各自投影的 DIP；屏幕配置变化时重新定位。Esc、空白点击、外部失焦、宿主最小化/隐藏均关闭。显示器选择来源 media_view_overlay_widget.cpp:1126-1151,1280-1300；尺寸取当前显示器，不新增猜测的样式数值。非 Windows 使用 mygo Screen.DisplayMatching/SetFullScreen；平台验收范围见 exceptions.md。


Box 的必填数据为显式 `BoxState` 和标题，Options 后的 `content func()` 构建正文。正文继承14 DIP正文与22 DIP行高；第一个右侧操作在最右，Left操作靠左。Default操作处理Enter；Disabled不触发；KeepOpen由宿主在操作接受后决定何时关闭。Locked只禁止Escape和遮罩关闭，显式按钮仍可操作。

Toast 每次调用 `Show()` 重启计时；Options.Duration=0使用主题1500 ms，Infinite忽略期限。贴底使用160 ms滑动，Center使用200/1000 ms淡入/淡出。普通toast透传指针；带Action的通知只交付回调。

Viewer 接收宿主准备的 `[]ViewerItem`。Ready要求Image有效，Loading/Failed不读文件或联网。宿主以`state.Show(id)`打开；已打开后用ID查找，重排不换图，删除当前ID则关闭。初次显示大图缩小到窗口、小图保持原始大小。Zoom负档按`1/(1-Zoom)`、非负档按`1+Zoom`；普通递增/递减以默认−7…+7为界。Fit是额外的窗口适配档，可在普通范围之外；按源码保留相同可见尺寸的适配/普通两个阶段，从适配进入其邻近负档时也可能超出−7。Ctrl+加减、Ctrl+0、中键、Ctrl+滚轮、捏合与拖动可操作。OnRetry只交付ID。

`ViewerItem.IntrinsicWidth/IntrinsicHeight float32` 可独立声明逻辑固有尺寸，两者同时为正时优先于 Image.Size；同 ID 缩略图换原图保留缩放和拖动。图片双击与 Ctrl+0/中键共用适应/原始大小切换。每次打开后的 500 ms 内空白与关闭按钮点击被忽略，Esc/主动关闭仍立即生效；翻页不延长保护。净室画法、来源及输入阈值说明见 [paint/viewer.md](paint/viewer.md)。

Loading 的 Progress 由宿主提供0…1进度。圆形指示直径50 DIP、线宽3 DIP、350 ms淡入/完成淡出、3000 ms一圈；进度平滑追踪宿主目标，资源就绪后在图片上完成淡出。LoadingLabel仅作为无障碍名称，普通照片路径不额外画加载文字。ReduceMotion禁用旋转及过渡。

`gallery/overlay.go` 注册 Menus、Tooltip、Box、Toast、Media viewer、Native window edges 六页；最后一页提供右下角原生菜单/tooltip与40项滚动菜单。完整聊天页和画廊自身的页面下拉菜单均使用 Native。

先前内容区版本的集成验收：全库测试与 mygo vet、93条断言反转及七张原生交互截图。当前原生窗口版本改为直接用 ui.NewTester 运行各窗口实际使用的菜单/tooltip/viewer内容；跨窗口矩形、焦点和销毁由实际 gallery 进程验收，不用内容区绘制代替原生窗口证明。

2026-11-03 原生版本验收：CGO_ENABLED=0 全库测试及 mygo v0.7.4 vet 通过；新增27条断言逐条反转均失败。实际 gallery 的19项场景通过，覆盖替换/禁用/右键与Shift+F10、宿主外溢出、工作区翻转、子菜单Esc、真实外点/失焦/最小化、长菜单End+Enter、tooltip延时/Esc/离开、viewer全屏/翻页/空白/加载失败重试及宿主关闭联动。96 DPI 下宿主矩形(80,60)-(696,549)，菜单(588,542)-(744,655)，确实伸出宿主；屏幕边缘菜单(1757,880)-(1913,993)保持在1920×1032工作区内；viewer为完整1920×1080。

Release（mygo_noinspector、-ldflags=-s）预热5次后，真实鼠标与Esc开关100次：43.140342秒，出现延迟P50/P95/最大为60.8024/72.1570/97.8294 ms；整次P50/P95为430.0701/441.3014 ms（含固定200 ms入场等待和实际150 ms退出）。出现时刻只证明首帧后HWND可见，不是物理呈现时间，不能据此宣布通过端到端响应或掉帧标准。进程工作集92573696→107876352 bytes，私有提交155275264→171462656 bytes，句柄951→1000；单核CPU平均21.0795%。仅本进程，不包含GPU显存；100次不代替长期回收验证。逐次数据、Win32脚本、截图及断言反转记录保留在任务指定scratchpad/native-overlay目录。

另开MYGO_FRAME_STATS=all，独立进程跑三轮，每轮预热5次再测100次。全窗口日志分别5557/5586/5605帧（含启动、宿主与未变帧），P95均1.0 ms，P99为35.9/35.6/35.6 ms；最大225.0/220.5/211.0 ms，各有一个超过100 ms的帧，均为gallery首帧，present阶段占221.4/216.9/206.8 ms。日志精度0.1 ms；保留启动长帧，不把所有窗口日志的分位数当成活动动画呈现/掉帧统计。完整原始日志在frame-proof；没有采集ETW/DWM的实际呈现时间。

按公用规矩通过127.0.0.1:3425实际调用auto/deepseek-v4.1-flash（返回opendesign/deepseek-v4.1-flash），复核菜单、tooltip、viewer三张合成截图，5.296486秒，1467输入/515输出token。模型未指出明显裁切、控件重叠或不可读标签；这只是静态视觉辅助复核，不代替窗口/输入实测或Telegram逐像素对照。

## mw-gaps2 原生键盘量测（2026-11-04）

固定 Tooltip 页、800×720 DIP、120 DPI，活动显示路径144 Hz；Ryzen 5 5600X / 12逻辑处理器、高性能电源方案。Release 使用 `mygo_noinspector -ldflags=-s`，公开 UI Automation 将焦点放到前一个页头按钮，再发真实 Tab/Esc；每轮预热5次，3个独立进程各测200次，同时检查宿主保有 Win32 前台、Tab落在Timestamp、Esc后仍是该anchor。退出码通过持有的进程句柄读取，三轮均0。

| 轮次 | P50 / P95 / 最大（ms） | 单核CPU均值 | 工作集 bytes 前→后 | 私有提交 bytes 前→后 | 句柄前→后 |
|---|---|---|---|---|---|
| 1 | 45.4260 / 61.0759 / 138.1424 | 26.1747% | 100257792→122867712 | 157872128→179560448 | 917→951 |
| 2 | 45.3894 / 61.3101 / 89.7852 | 25.1093% | 99131392→122527744 | 160198656→181121024 | 927→950 |
| 3 | 45.3701 / 60.8462 / 76.4381 | 23.4308% | 101097472→127143936 | 161320960→178212864 | 925→948 |

上述延迟从Tab事件到HWND可见，配置2 ms轮询；不是物理呈现时间，不能据此宣布输入呈现或P99预算通过。CPU覆盖含焦点准备、窗口创建和退出动画的整个量测段，不是纯文本流式场景。72小时/10分钟空闲、Go堆与GPU显存没有在此场景验收，短期句柄/私有提交增量不作无泄漏结论。

`MYGO_FRAME_STATS=all` 原始日志为8008/7994/8012帧（含未变帧），全帧P95为0.7/0.7/0.6 ms；每个原生窗口首帧单列，其P95为44.4/45.4/39.8 ms。除首帧且实际绘制的6333/6336/6381帧，P95均0.5 ms，描述性P99为1.1/1.1/1.0 ms。每轮保留1个>100 ms的gallery启动帧：225.7/208.2/212.3 ms；不能把首帧排除后的分布当全场景分布，也没有DWM/ETW错过呈现周期统计。

Release画廊二进制14578688 bytes。三轮原始日志、逐次样本与资源JSON共10文件、7839495 bytes（汇总器写入metrics.json之前）；完整产物留在任务scratchpad/mw-gaps2/release-uia120。临时125%缩放按每批开始时的显示接口快照恢复并读回，记录在*-dpi-before/during/restored.json；各批起始值既有100%也有125%，均按各自快照恢复。DPI辅助工具仅用于验收，ABI/相对档位参考imniko/SetDPI（Unlicense），没有进入库。

