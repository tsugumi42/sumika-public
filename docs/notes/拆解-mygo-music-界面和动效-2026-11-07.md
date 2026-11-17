# 拆解 mygo-music：界面结构和打破分栏的动效（2026-11-07）

> 对象：github.com/egoist/mygo-music，提交 b021067（2026-11-07），本地浅克隆在 `_external\mygo-music`。**许可证 AGPL-3.0：只学思路，不抄代码。**
> 起因：主人 2026-11-07"它的很多动效过渡打破了普通的框架界限，不知道它是怎么实现的"。
> 做法：Astra（omp codex/gpt-6-astra，只读）读源码；主会话核对了"封面长成全屏不用 mygo 的 Fly / Morph / TransitionMatch，是自己写的 npMorph 按保存的起点矩形插值"（ui_nowplaying.go:181、ui_player.go:379–380），属实。
> 前一份：拆解-mygo-cloudmusic（私有存档）。挂在：待办"界面接入规格"（右栏退场、看大图、过程气泡展开）。

## 结论

1. **它最突出的做法，是让封面、播放器、背景成为同一次连续变化的不同部分。** 一个 `npT` 同时驱动飞行、裁剪、圆角、背景与内容显现。[读码：M/ui_nowplaying.go:94–110、153–240、263–280]
2. **“从底栏封面长成全屏”由应用手工编排**：保存起点矩形、读取终点布局、隐藏原封面，再插值移动新封面；没有使用 `ElementTransition.Fly`、`ContentTransition.Morph` 或 `TransitionMatch`。[读码：M/ui_player.go:375–381；M/ui_nowplaying.go:181–195；全仓搜索]
3. **这个效果默认关闭，是实验选项。** 默认从窗口底部滑入；减少动态效果时原地淡入淡出。[读码：M/settings.go:49–52、90–92；M/ui_nowplaying.go:71–101、127–140]
4. **“打破分区边界”还来自背景连续、渐变遮罩和自绘轮廓**：封面墙消融进同一背景，刘海形状连续长大，唱片与歌词拥有自己的运动方式。[读码：M/ui_wall.go:97–127；M/ui_notch.go:322–389；M/ui_daily.go:407–443]
5. **本次核对的核心 API，mygo v0.8.1 已有。** 没找到必须升级到指定预发布版才能采用这些手法的证据；但预发布源码未缓存，不能保证两版内部行为一致。[读码：G/ui/transition.go:21–88；G/ui/motion.go:16–76；G/ui/mask.go:61–64]
6. **减少动态效果并未覆盖所有自绘路径；实际流畅度未验证。** 舞台歌词、点赞粒子、进度条烟雾仍按时间计算运动；本次严格只读，没有运行程序或测试。[读码：M/ui_stage.go:219–287；M/ui_like.go:208–268；M/ui_player.go:72–169]

出处约定：

- **M/**：`mygo-music@` 的 `internal/app/`。提交依据 `.git/packed-refs:2`。
- **G/**：`<Go 模块缓存>/pkg/mod/github.com/egoist/mygo@v0.8.1/`。
- **S/**：本次 Sumika 工作树。
- **[读码]** 表示静态实现证据；**[推断]** 表示预期观感、设计判断或建议。没有实测结论。

已读旧拆解；以下主要补它未展开的跨区域编排、背景、舞台、每日推荐、热评和电台。旧笔记已有的通用按钮、图片缓存、骨架屏不重复详述。这里的“补充”不表示这些功能全部是在旧提交之后新增。[旧笔记：`docs/archive/notes/拆解-mygo-cloudmusic-2026-11-02.md:235–244`]

许可证为 AGPL-3.0；本文只描述机制与参数，不摘录实现代码。[读码：`mygo-music/LICENSE:1–12`]

## 界面结构

### 主窗口：普通页面之上，还有一个覆盖整窗的播放器

下表是**绘制关系**，不是代码里声明的固定层数。主窗口主要有四组关系；全屏播放器内部再分背景、内容、控制层。[读码：[M/ui_main.go](/_external/mygo-music/internal/app/ui_main.go:48)]

| 层级／区域 | 内容与组织 | 常驻、替换或叠加 |
|---|---|---|
| 普通页面底层 | 左侧固定 232 DIP 导航；右侧按 `route.page` 构建首页、歌单、搜索、电台、歌手、专辑、播客、收藏、浏览页等 | 正常情况下存在；普通路由通过 `switch` 换内容，没有统一共享元素转场。[读码：M/ui_main.go:13、51–74、378–412；M/app.go:25–50] |
| 页面控制层 | 顶栏和底部播放条均绝对定位，页面从它们下面滚过 | 位于右侧内容区内，画在页面之后；滚动边缘材质按需出现。[读码：M/ui_main.go:66–72、355–375；M/ui_player.go:348–361] |
| 整窗 Now Playing | 覆盖侧栏和右侧内容区；包含封面背景、歌词／队列／评论等 | 层容器稳定存在，关闭时不可见；打开且稳定后，底下普通页面和侧栏停止构建。[读码：M/ui_main.go:52–58、75；M/ui_nowplaying.go:66–147] |
| 临时浮层 | 音量、账户菜单、登录、歌单命名／删除等 | 使用 `PopoverBase`、`DialogBase`；与 Now Playing 的普通绝对定位层不同。[读码：M/ui_player.go:187；M/ui_account.go:200、339；M/ui_playlistedit.go:400、437] |

**Now Playing 不是某个右侧页面。** 它位于普通“侧栏＋内容”这一整组的后面，覆盖整个窗口；关闭动画也由它自己保持层级，直到完成。[读码：M/ui_main.go:49–76；M/ui_nowplaying.go:54–65、115–147]

它内部还有三种布局：

| 模式 | 排法与切换 |
|---|---|
| 歌词／队列／评论／节目说明／字幕 | 左侧封面和播放控制，右侧当前面板；`npPane` 决定内容。歌曲与播客的面板集合不同。[读码：M/ui_nowplaying.go:286–420、470–503] |
| Stage 舞台 | 歌词占主要空间，底部改成细播放条；封面缩为底部 44×44 小图。[读码：M/ui_nowplaying.go:323–332、552–578] |
| Wall 封面墙 | 封面队列成为可拖动的空间，上方歌曲信息和下方播放控制叠在墙之上。[读码：M/ui_wall.go:97–127、130–189、372–385] |

`quiet` **不是独立页面**：它在歌曲间奏中，把热评叠到歌词位置，歌词先变淡，再隐藏。`radio` 则属于普通页面内容，拥有自己的叠放封面卡片。[读码：M/ui_quiet.go:88–155；M/ui_radio.go:216–299]

### 主窗口之外

- **设置仍是独立窗口，但已不同于旧拆解的一列长表单**：当前为 780×640，最小 640×420，左侧 200 DIP 分类导航，右侧内容与跨分类搜索结果。分类切换没有找到跨页变形实现。[读码：[M/ui_settings.go](/_external/mygo-music/internal/app/ui_settings.go:32)，:147–233]
- **刘海播放器是另一个透明、无边框窗口**，窗口尺寸预留足够空间，内部黑色形状变大变小；不是主窗口的浮层。生产入口仅在 macOS 创建。[读码：M/ui_notch.go:49–80；M/app.go:377–381]
- 主窗口的基础布局仍然是侧栏＋内容。它在重点场景中增加整窗覆盖、连续几何、自绘背景，因此能跨出原本的栏目范围。[读码：M/ui_main.go:49–76；观感解释为推断]

## 动效逐个拆解

下表“用户看到什么”均为**根据源码推断的观感**，未实际播放观察；实现、状态、数值为 **[读码]**。时长中的弹簧 `Duration` 是参数化的到达时间，**不是精确的动画结束时刻**。[G/ui/motion.go:55–76]

### 跨区域、形状与背景

| 效果／用户看到什么〔推断〕 | 实现、状态与身份〔读码〕 | 时长、曲线与边界 | 出处 |
|---|---|---|---|
| **底栏封面长成全屏播放器；关闭时缩回原处** | `mini-cover.LayoutBounds()` 保存到 `app.npFrom`；大封面读取自己的 `LayoutBounds()`，按同一进度 `npT` 做 `Scale`、`Translate`。整窗通过 `ClipInset` 从小封面矩形展开。原封面在途中透明。**不是两个相同 Match key 自动配对，而是显式保存源矩形。** | `npSpring` 340ms、Bounce 默认 0。源封面 48×48、圆角 8；目标圆角按缩放比例补偿。第一次先透明布局一帧；关闭归位后多保留一帧，避免封面交接闪空。普通双栏、Wall 主封面、Stage 底部封面均接入。 | [M/ui_nowplaying.go:19](/_external/mygo-music/internal/app/ui_nowplaying.go:19)，:115–240、355、560；M/ui_player.go:375–381；M/ui_wall.go:144–145 |
| **面板长大时，背景先扩散，文字和控制随后出现** | 共用 `npT`。背景 `bloom` 的变换原点设在源封面中心；`npRise(e,n)` 按顺序计算各组透明度和位移。封面、背景和控件不是各自自由启动的动画。 | 背景透明度在进度 0.05–0.55 之间变化，初始放大至 1.3 倍。第 n 组从 `0.3+0.07n` 开始，跨 0.4 进度完成，位移 24 DIP；使用 `smoothstep`。 | [M/ui_nowplaying.go:232](/_external/mygo-music/internal/app/ui_nowplaying.go:232)，:263–280、359–393 |
| **向下拉播放器，它跟手、变小变圆；短拉回弹，长拉关闭** | `Dragged()` 累计 `npPull`，`Rubberband` 处理越界，`Jump("pull",…)` 跟手；松开后 `AnimateTo` 回原位。`DragEnded()` 的速度注入关闭弹簧。状态是 `npPull/npShift/npHeld/nowPlaying`。 | 超过窗高 20%，或超过 24 DIP 且向下速度 >700 DIP/s，就关闭。实验展开模式下缩小量最多按 12% 计算，圆角额外增加最多 28；回位用同一 340ms 弹簧。默认滑入模式只跟随位移。 | [M/ui_nowplaying.go:75](/_external/mygo-music/internal/app/ui_nowplaying.go:75)，:103–107、131–164 |
| **鼠标移过封面，卡片向指针倾斜，亮光随之移动** | 指针位置变成两个旋转目标；`AnimateTo("rx"/"ry"/"gx"/"gy")` 平滑追随。`RotateX/RotateY/Perspective` 做透视；亮光由 shader 绘制。拖动横偏另叠旋转。 | 倾斜弹簧 500ms/Bounce .2；指针倾角 ±7°，拖动摆角 ±16°，透视距离 900。亮光显隐 300ms。飞向底栏途中不响应悬停倾斜。 | [M/ui_cover.go:178](/_external/mygo-music/internal/app/ui_cover.go:178)，:194–252 |
| **甩掉封面换歌；按钮切歌时，旧封面滑走、新封面滑入** | `coverState` 保存拖动、方向、曲目位置、退出 Motion；元素身份 `coverKey{tr.ID, gen}`。甩动完成递增 `gen`；普通换歌通过 `Transition.Enter/Exit`，按播放顺序决定方向。没有共享元素配对。 | 拖动回位 480ms/.18；换歌 520ms/.12。鼠标拖动阈值 40% 封面宽，触控板 30%；甩出目标 1.5 倍封面宽，到目标 97% 后切歌。普通换歌进场位移半个封面宽、缩放 .92。 | [M/ui_cover.go:23](/_external/mygo-music/internal/app/ui_cover.go:23)，:79–174、260–274 |
| **换歌时背景沿用封面色，缓慢换成下一首的颜色** | `npArt` 保留上一张已加载封面，下一张未到时不先闪灰。`backdropCache` 保存当前与上一张 Canvas，以 artwork／tint 变化触发交接。主色先从 24×24 缩图的 18 个色相桶选出；模糊图另预制为 48×48。 | 两张背景交叉淡化 700ms，`1-(1-k)^3`；默认背景静止。明度受控：背景顶部 HSV V=.35，底部 V=.24，不直接把白封面铺满屏幕。 | [M/backdrop.go:104](/_external/mygo-music/internal/app/backdrop.go:104)，:140–181、234–242；M/ui_nowplaying.go:248–255；M/artwork.go:528–558、586–607 |
| **可选雾背景缓慢升腾，低音让雾变浓、亮点闪动** | shader 使用五层噪声及噪声扭曲，输入 `Time/Bass/Deep/Fog`。应用保存 `drift/speed/level`；播放推向速度 1，暂停推向 0。不是封面图片本身变形。 | GPU 下约 30Hz 更新；暂停渐停，速度误差 <.005 后归零。无 GPU 或 ReduceMotion 时不持续漂移。默认关闭。 | [M/backdrop.go:32](/_external/mygo-music/internal/app/backdrop.go:32)，:126–205；M/settings.go:74–77、90–92 |
| **封面墙移动到顶部时，逐渐模糊并融进歌曲背景** | 墙面容器负责裁剪；上方叠 `glass.Blur{Mask:渐变}`，再由 `backdropOver` 取**同一背景 Canvas 对应位置的像素**覆盖。换背景时回贴层也按同一淡化进度混合。这个连续性来自像素匹配。 | 顶部过渡带 72 DIP、模糊半径 16；底部 112 DIP、半径 14。背景回贴按 2 DIP 横条变化透明度。墙的惯性／自动定位弹簧 800ms；行摆动幅度 ±36 DIP、单程 7s EaseInOut。 | [M/ui_wall.go:97](/_external/mygo-music/internal/app/ui_wall.go:97)，:214–275、319–331；M/backdrop.go:292–330 |
| **刘海区域像一个黑色物体展开成播放器** | 透明窗口固定，内部 `shape()` 用 Bézier 曲线画轮廓；`w/h/open/hello` 各动画通道驱动同一形状与内容。紧凑封面和展开封面是两套内容，按 open 交替显隐，**不是同一封面跨窗口飞行**。 | 520ms 弹簧；打开 Bounce .28，收起 .12；展开宽 468。底角由 10→28，顶部外扩由 6→14；展开内容从上方 10 DIP 进入。悬停与离开均有约 300ms 门槛。 | [M/ui_notch.go:249](/_external/mygo-music/internal/app/ui_notch.go:249)，:322–407、428–434 |

### 局部编排与自绘

| 效果／用户看到什么〔推断〕 | 实现、状态与身份〔读码〕 | 时长、曲线与边界 | 出处 |
|---|---|---|---|
| **首页唱片从唱片套里抽出，后方封面扇开，波纹扩到整张卡片** | 稳定的 `daily/stack/record/sleeve` 元素；`out` 控制抽出距离；`AnimatePose` 控制后两张封面和唱片套。唱片、沟槽、扫光用 Painter 自绘，`app.dailySpin` 保存角度、速度、波纹进度。 | 抽出 700ms/.25；扇开 600ms/.3；悬停扫光 1100ms EaseInOut，只扫一次。播放目标转速 33⅓ rpm，悬停仅 12% 速度；波纹间距 18 DIP，6s 推进一圈间距。**点进详情只是 open(route)，没找到首页卡片飞成详情封面的实现。** | [M/ui_daily.go:23](/_external/mygo-music/internal/app/ui_daily.go:23)，:38–117、165–169、201–241、251–286、407–443 |
| **电台当前封面压在下一首之上，播放时略放大，换歌时新图进入** | 后封面 `Key(next.ID)`；前框稳定 `Key("front")`，内部图片按 `Key(tr.ID)` 替换并设置 Enter。没有后封面搬到前框的配对。 | 前框播放／暂停缩放 1/.94，600ms/.25；新图从 .92 进入，使用 `Snappy`。后封面目标姿态旋转 6°、X=14，500ms/.2；首次目标即初值，因此不能据此断言它每次都“从平放旋转出来”。 | [M/ui_radio.go:301](/_external/mygo-music/internal/app/ui_radio.go:301)，:311–333；G/ui/motion.go:308–336 |
| **间奏时歌词让位给热评，开唱前热评退下** | `quietState` 保存曲目、静音区间和仍用于退场的评论；稳定 `Key("quiet")`，通过 `ui.Local("seen")` 比较评论 ID，换评论时 `Jump("in",0)`。歌词与评论用独立透明度交接，不做文字 Morph。 | 静音段至少 8s；开始后 1.2s 显示，开唱前 1s 退出；长间奏约每 10s 换一条。歌词 dim 600ms；评论上移 16 DIP、700ms/.1。dim>.98 时歌词隐藏。默认关闭。 | [M/ui_quiet.go:21](/_external/mygo-music/internal/app/ui_quiet.go:21)，:40–83、92–119、140–156 |
| **舞台歌词逐词飞入、变清晰、发光、缓慢漂走** | Flow 使用歌词时间算位置、旋转、缩放、模糊和透明度，非 `AnimateTo`；按行开始时间与词序号作身份，伪随机偏移也由时间／词序号固定，因此 seek 可重建相同姿态。发光是再画一份模糊文字。 | 提前 80ms 进入；自写弹簧响应约半秒，注释给刚度 200、阻尼 20；透明度 100ms、去模糊 200ms；唱完缩放回落 500ms、余光 900ms、漂移最长 5s；旧行 400ms 淡出。 | [M/ui_stage.go:27](/_external/mygo-music/internal/app/ui_stage.go:27)，:33–42、130–136、194–274 |
| **舞台 Steps 分行错落进入；Tilt 按字起伏，部分行倾斜** | Steps 把词分成行，每行平移、缩放，再复用 Flow 单词动画；Tilt 按 `[词序号,字符序号]` 建字形，按歌词时间直接计算；可选火花通过 `DrawOver` 画短线。 | Steps 从侧方 40 DIP、缩放 .85 进入；Tilt 提前 250ms，行进入 550/600ms，字符错开 40/50ms；字的脉冲持续 .2–.9s。火花 600ms，普通词 9 条、行尾 16 条。 | [M/ui_stage_styles.go:194](/_external/mygo-music/internal/app/ui_stage_styles.go:194)，:62–92、272、360–466 |
| **点赞时心形弹起，环和粒子越出按钮附近** | 心形 `AnimateTo("pop",1)`；点击先 `Jump` 到 .2，取消到 .8。粒子画在外层 wrapper 的 `DrawOver`，避免随心形一起缩放；时间保存在 `ui.Local("burst")`。 | 心形 550ms/.55；粒子 750ms，半程内扩环，外飞使用 cubic ease-out；两圈、每圈 7 个粒子。 | [M/ui_like.go:177](/_external/mygo-music/internal/app/ui_like.go:177)，:194–268 |
| **评论数量像机械计数器一样逐位滚动** | `rollState` 保存旧数、增减方向与退出 Motion；槽位 key 从右数，字符自身 key 为字符值；改变的字符退出、新字符进入，未变的留在原位；`ClipY` 裁掉上下越界部分。没有调用 `ContentTransition.Numeric/Morph`。 | 每位 420ms/.15，从个位开始每位延迟 30ms；位移 16 DIP；位数变化使用 Collapse 与 Size 过渡。 | [M/ui_comments.go:108](/_external/mygo-music/internal/app/ui_comments.go:108)，:122–156 |
| **进度条像一颗带烟尾的光点** | Slider 仍管数值与输入；Painter 根据当前时间推算烟团位置，无逐粒子持久状态；低音控制亮度、核心大小和烟量。 | 烟雾整体显隐 600ms；烟团寿命 1.4s，最多 36 个；约 30Hz；暂停、缓冲或拖进度时退出。默认关闭。 | [M/ui_player.go:72](/_external/mygo-music/internal/app/ui_player.go:72)，:96–169 |
| **音量浮层贴着按钮冒出** | `PopoverBase`＋`AttachTo`；自绘一个朝向扬声器的箭头。**没有找到按钮形状连续变成浮层的代码，也没有该浮层自行配置进退场动画。** | 应用未指定时长。v0.8.1 的 `PopoverBase` 仅建立 Overlay、锚定和关闭行为；不能把锚定误认成“长出来”。预发布版内部行为未核对。 | [M/ui_player.go:180](/_external/mygo-music/internal/app/ui_player.go:180)，:187–214；G/ui/base.go:649–683 |

上述动效使用的几何、Painter、shader、Canvas、Transition、PopoverBase 等基础能力均已在本机 v0.8.1 找到；**这不等于已验证整个应用能降级编译运行**。逐项 API 证据见“mygo 版本差异”。

## 动效体系

### 参数按效果组织，没有统一的全局参数表

在 `internal/app` 的动画调用与常量中，没有找到全局 motion token 表或统一速度倍率。存在局部复用：`npSpring`、`coverSwing`、`coverSlide`、`wallGlide`、`artFade`，以及通用控件里的重复时长。[读码：M/ui_nowplaying.go:19–23；M/ui_cover.go:79–83；M/ui_wall.go:30；M/ui_kit.go:96–139、184]

| 动效族 | 源码参数 |
|---|---|
| 短反馈 | 悬停颜色多为 120ms；播放图标 340ms/.3；分段指示 280ms/.08。[M/ui_kit.go:96、118、134、298] |
| 空间变化 | 全屏播放器 340ms；封面 480–520ms；刘海 520ms；封面墙定位 800ms。[前表对应出处] |
| 内容与氛围 | 封面到达淡入 320ms；热评／主题色 600ms；背景换歌 700ms；墙面摆动单程 7s。[M/ui_kit.go:184；M/ui_quiet.go:101；M/ui_daily.go:38；M/backdrop.go:107；M/ui_wall.go:322] |
| 自定义时间函数 | Stage 的 `springStep/easeOutCubic`，唱片速度渐近更新，背景雾渐停。[M/ui_stage.go:33–50；M/ui_daily.go:273–286；M/backdrop.go:187–205] |

未显式指定 Ease 的定时 `ui.Animation`，在 v0.8.1 中默认 EaseOut；`Snappy` 为约 500ms/Bounce .15。[读码：G/ui/motion.go:16–53]

它的统一性主要来自**运动语义**：拿起会倾斜、暂停会缩小、甩走会延续速度、同一个打开动作共用进度。把它理解成一张严格统一的时长表，证据不足。[推断；依据上述实现]

### 减少动态效果：有明确处理，也有漏口

| 路径 | 查到的退化行为 |
|---|---|
| Now Playing | 明确换成 200ms 原地淡化；`NonMotion:true` 保留透明度动画，取消展开／滑入。[读码：M/ui_nowplaying.go:21–23、71–101、131–140] |
| 封面、墙面 | 封面倾斜和拖动摆角目标归零；墙面停止自主摆动；歌词自动追随不启动自定义弹簧。[读码：M/ui_cover.go:191–193；M/ui_wall.go:238；M/ui_nowplaying.go:788] |
| 首页唱片、背景雾 | 唱片目标转速设 0，取消悬停扫光与扇开；雾停止持续漂移。唱片／雾已有速度通过自定义 step 渐停，代码不是立即清零。[读码：M/ui_daily.go:30、45–68、109、273–286；M/backdrop.go:128、149–151、187–205] |
| 热评 | 没有单独分支；按 v0.8.1 的 `AnimateTo` 语义，未设 NonMotion 的 dim 和入场值直接到目标，因而不是保留淡化的退化。[读码：M/ui_quiet.go:101、155；G/ui/motion.go:339–345] |
| Stage、火花、点赞粒子、烟雾 | 在对应实现中没找到 ReduceMotion 判断；它们按时间直接设置几何或画粒子，不能靠 `AnimateTo` 自动替它们关闭。[读码：M/ui_stage.go:219–287；M/ui_stage_styles.go:69–92、401–466；M/ui_like.go:208–268；M/ui_player.go:72–169] |

最后一行是**静态覆盖缺口**，不是本次实测故障。v0.8.1 的直接 `Translate/Scale` 仅设置变换，Painter 的请求帧方法也没有替自绘效果检查 ReduceMotion。[读码：G/ui/transform.go:188–213；G/ui/paint.go:134–165]

### 性能：降低重画成本，停止不必要的更新

旧笔记已经讨论“闲着不要帧”。这次补充的是这些效果具体怎样降低成本：

- **背景先画成小图，再当一张图片使用。** 默认 Canvas 尺寸为逻辑宽高各除 8 再加 1；GPU 雾背景各除 2。只有封面／颜色／模式变化、尺寸变化或运动更新到期时重画；换歌保留前一张 Canvas，淡化结束释放引用。[读码：M/backdrop.go:132–181]
- **背景颜色与模糊图提前派生。** 封面加载工作中产生主色和模糊副本，不在背景每一帧重新读文件或模糊整张封面。[读码：M/artwork.go:130–159、231–243、528–607]
- **优先把连续绘制留在 Painter 或 Pose。** 墙面每行摆动用 `AnimatePose`；歌词亮度也用 Pose。v0.8.1 的 Painter 请求帧只需重新绘制，除非同时还有别的变化；出视口不绘制，就不会从该回调继续要帧。[读码：M/ui_wall.go:268–275；M/ui_nowplaying.go:690–695；G/ui/paint.go:128–165]
- **墙面只构建视口附近的行列。** 范围额外算上摆动幅度；已被顶部不透明区盖住的整行跳过。[读码：M/ui_wall.go:256–268]
- **全屏稳定后不构建底层页面；退出时恢复。** 判定改用实际进度、拖动状态，而非简单等待一个固定时间。[读码：M/ui_main.go:52–58；M/ui_nowplaying.go:47–51、111–114]
- **平放封面不启用 3D。** 旋转总量超过 .05 才设置透视；高光由 shader 直接圆角，源码注释说明这是为了避免倾斜过程中每帧重新栅格化裁剪。[读码：M/ui_cover.go:45–48、224–226、247–252]
- **Stage 按歌词阶段安排帧。** 单词进入和发光时请求连续帧，慢漂时约每 100ms，等待下个词时最长每 250ms；热评完全覆盖歌词后，暂停舞台这条更新路径。[读码：M/ui_stage.go:121–128、165–191]
- **不是所有效果都只走绘制帧。** `AnimateTo` 会请求构建帧；自定义歌词滚动的 `lyricGlide.step` 也逐帧请求构建。它把实际滚动值先设到终点，再用视觉位移补差，误差 <.5 DIP 且速度 <20 DIP/s 时停止。[读码：G/ui/motion.go:359–360；M/ui_nowplaying.go:741–813]

测试源码包含闲置首页不再请求帧、背景淡化后释放旧 Canvas、播放器关闭封面不闪空等断言；**本次没有运行它们**。代码中的 30/60Hz 是请求频率，测试中的 ≤3／≤12 是门槛，都不是本次测出的帧率。[读码：M/perf_test.go:56–122；M/np_test.go:254–283]

## 对 Sumika 的启发

下面每项按“它的优势 → 整体思路 → 转成我们的什么 → 现有底子 → 现在做不做”排列。建议均为 **[推断]**；“现在做不做”指实施建议，本次没有改文件。

| 场景 | 五步判断 |
|---|---|
| **聊天图片 → 看大图** | **优势：** 封面展开保留来源，关闭回到同一位置，且专门处理首帧与末帧交接。**思路：** 图片身份、来源几何、展示层生命周期一起管理。**可转换：** 缩略图到大图的连续放大及原路返回；源图已滚出视口时改用淡化，不能飞回过期坐标。**底子：** v0.8.1 已有 `Match+Fly`；但本次在 `chatui`、`sumika-proto` 搜 `Viewer/lightbox/OpenImage/ImageURL/Media` 等，没找到新界面的完整媒体查看路径，不能把旧根包 Viewer 当成已接入。**现在：** 值得作为媒体功能的候选方案，先明确原有“不位移”规则是否修订。依据：M/ui_nowplaying.go:115–240；G/ui/transition.go:68–88；S/docs/设计语言.md:384。 |
| **会话列表 → 聊天** | **优势：** 运动说明“这个内容从哪里来”。**思路：** 只让同一身份的视觉对象连续变化。**可转换：** 窄窗下，会话头像／标题与聊天头部建立连续性；宽窗列表仍在时，无需让整条会话飞走。**底子：** 原型已经按 `layer-N` 建窄窗三层，并按进入／返回决定进场方向；还没有共享头像配对。**现在：** 先保留现有层级导航，不把普通切会话扩成整页大变形。可在以后评估头像／标题的小范围配对。依据：S/native/cmd/sumika-proto/view.go:192–220、224–241；G/ui/transition.go:68–88；S/docs/界面模式规范.md:33–39。 |
| **右栏展开／收起** | **优势：** Now Playing 在退出期间仍保持完整内容与正确层级，归位后才交接。**思路：** “想关闭”与“已经消失”是两个状态。**可转换：** 右栏保留打开／关闭进度，收起动画结束才摘掉内容；打断关闭重新打开时从当前进度回转。**底子：** 原型有 Overlay、遮罩和 `drawer-x/drawer-o`；但调用条件依赖 `Right && Layer==2`，关闭直接清状态，当前路径没有完整退出阶段。**现在：** 五处里最值得先补这项生命周期；沿用右侧滑入语义，不改成从按钮膨胀。依据：M/ui_nowplaying.go:94–147；S/native/cmd/sumika-proto/view.go:247–251、678–701；S/docs/设计语言.md:388–389。 |
| **过程气泡展开／收起** | **优势：** 刘海把外轮廓、内容透明度和内容位置协调起来，看起来是同一个对象变形。**思路：** 外壳先后关系明确，内容在展开范围内显现。**可转换：** 稳定摘要行不动，详情通过裁剪和高度变化展开，内容再短淡入；文字不整体缩放。**底子：** 已有按消息 ID 的展开／手动收起状态，稳定 `bubble`，进行中最大宽保留；当前详情通过 `if open` 直接建／不建，箭头单独动画。**现在：** 值得做，但应使用不过冲高度，处理虚拟列表高度与阅读锚点；不照搬刘海 .28 的尺寸回弹。依据：M/ui_notch.go:344–389；S/native/internal/chatui/processbubble.go:135–188、242–255；S/docs/设计语言.md:372、396。 |
| **设置页切换** | **优势：** 当前 mygo-music 设置页保持分类、标题、滚动边缘稳定。**思路：** 导航位置稳定比强行跨页形变更重要。**可转换：** 分类选择反馈与内容淡入统一，保留稳定标题区。**底子：** 原型有设置路由和 `routeMotion`；mygo-music 本身也没有可借的设置页共享元素转场。**现在：** 按现有 200ms 内容淡入规则即可，没有证据支持为设置页增加封面式展开。依据：M/ui_settings.go:147–227；S/native/cmd/sumika-proto/view.go:224–229、704；S/docs/设计语言.md:387。 |

### 与现行规则的冲突

1. **来源连续性可以借，尺寸和节奏不能整套搬。** Sumika 已有 Smooth/Bouncy 物理弹簧，以及 Fast=140ms、Msg=200ms、Panel=230ms；mygo-music 的 340–800ms 是音乐界面的局部取值。[读码：S/native/internal/chatui/tokens.go:180–184；M/ui_nowplaying.go:19；M/ui_wall.go:30]

2. **图片飞入与现行“看大图不位移”明确冲突。** 这是需要重新定设计规则的选择，不能包装成普通实现优化。[读码：S/docs/设计语言.md:384；建议为推断]

3. **过程展开不能沿用刘海高度回弹。** Sumika 明确要求高度不过冲；ADR-285 允许回弹的是开关、分段指示和切层位移，并要求 ReduceMotion 下不位移、不回弹。[读码：S/docs/设计语言.md:372、396；S/docs/决策记录.md:3433–3440]

4. **舞台文字、烟雾、持续墙面摆动不适合直接搬进聊天。** 现行规则要求安静、动效表达信息；普通消息不能为了装饰持续移动，历史记录也不播进场。[读码：S/docs/设计语言.md:25、376、386、392；建议为推断]

5. **按下缩放与计数滚动也不能直接照搬。** Sumika 明确“按下不缩放”，频繁跳动数字直接更新；音乐播放器的按钮缩到 .88、评论计数逐位滚动属于不同约定。[读码：S/docs/设计语言.md:392；M/ui_kit.go:119–123；M/ui_comments.go:145–153]

6. **“不用 transition”的原句有 Web 适用范围。** 该条理由针对 React Aria 的单帧 `data-entering` 与 Chrome；不能仅凭 API 同名，就认定 mygo 的 `ElementTransition` 也被禁止。新界面现有代码已经使用它。[读码：S/docs/设计语言.md:393；S/native/internal/chatui/bubble.go:223–231；适用范围判断为推断]

`界面模式规范.md` 仍是草稿，且它引用的动效 skill 说明还是 v0.7.4；本次 API 能力判断以实际 v0.8.1 源码为准。[读码：S/docs/界面模式规范.md:3–12、166；S/native/go.mod:12]

## mygo 版本差异

应用依赖为 `v0.8.4-0.20261107092838-4219d257423d`，Sumika 为 `v0.8.1`。已检查指定模块缓存目录，找到 v0.8.1，**没有该预发布版目录**；按任务约束没有下载或执行版本查询命令。[读码：`mygo-music/go.mod:11`；S/native/go.mod:12；模块缓存目录清单]

| 能力 | v0.8.1 核对结果 | mygo-music 本次使用情况 |
|---|---|---|
| `ElementTransition.Match/Fly/ScaleContent` | 已有；Fly 可跨父级并越过周围裁剪，在所属窗口／Overlay 层内飞行。[G/ui/transition.go:68–88] | 没找到调用；它自己保存矩形完成封面交接。 |
| `ContentTransition.Morph/Numeric` | 已有；Morph 配对前后文本共有字符，不是任意面板形状变形器。[G/ui/content.go:11–44] | 没找到调用；评论数字用槽位、字符 key 和 Enter/Exit 自己实现。 |
| `Router.Transition=TransitionMatch` | 已有，页面交叉淡化并匹配 Match 元素；默认匹配转场约 300ms。[G/ui/router.go:67–70、129–134、556–560、677–678] | 没找到 `ui.Router`；普通页面自行维护 route/history/forward。 |
| `AnimateTo/AnimatePose/Jump`、弹簧速度 | 已有，重定向保留速度；`SpringOptions.Velocity` 可注入松手速度。[G/ui/motion.go:55–76、308–360；G/ui/api_gen.go:3531、5687] | 全屏展开、拖动回位、悬停、数字、卡片等大量使用。 |
| `Animation.NonMotion` | **v0.8.1 已有**，可在 ReduceMotion 下保留纯透明度等动画。[G/ui/motion.go:34–41、339] | Now Playing 的退化淡入明确使用，不能误列成预发布专属。 |
| `LayoutBounds/ClipInset/DragEnded` | 已有。[G/ui/api_gen.go:4180、4689、5871；G/ui/mask.go:61–64] | 封面展开与跟手关闭的核心。 |
| 3D 变换、Painter、shader 圆角 | 已有 `RotateX/RotateY/Perspective`，以及 `shader.Options.Radius`。[G/ui/transform.go:247–266；G/ui/shader/shader.go:217–250] | 封面倾斜、高光、雾。 |
| Canvas、GPU 判断、局部帧调度 | 已有 `NewCanvas/Draw/Bitmap`、`Painter.Accelerated/AnimationFrameRate/After`。[G/ui/canvas.go:9–48；G/ui/paint.go:116–165] | 背景缓存、软件渲染降级、唱片与烟雾限频。 |
| 渐变磨砂、锚定浮层 | 已有 `glass.Blur.Mask`、`PopoverBase/AttachTo`。[G/plugins/glass/blur.go:20–28；G/ui/base.go:637–683] | 封面墙边缘、音量与账户浮层。 |

**可确认的是：本次拆出的主要手法，其基础 API 在 v0.8.1 都存在。** 未查到“某个核心动效必须用指定预发布版”的实例；两版的修复、性能和平台渲染差异无法据此排除，也没有验证应用整体降级兼容。

## 没查到的

| 项目 | 查了哪里／目前能说到哪 |
|---|---|
| **预发布版源码及精确差异** | 查了指定 `github.com/egoist` 缓存目录；没有目标版本。无法核对它的 Transition 退出层级、Popover 默认行为、3D 栅格化、Canvas 缓存是否相较 v0.8.1 修过。 |
| **统一的跨页面 Hero／Morph 系统** | 全仓搜索 `Fly`、`Morph`、`ContentTransition`、`TransitionMatch`、`ui.Router` 无命中；读取普通路由、Now Playing、首页和电台，找到的是局部手工编排。 |
| **按钮连续变成音量面板／账户面板** | 查 `ui_player.go:180–214`、`ui_account.go:338–342` 及 v0.8.1 `base.go:649–683`；只找到锚定浮层与自绘箭头，没有形状交接。 |
| **从首页卡片飞到详情、从电台后封面飞到前封面** | 查 `ui_daily.go:112–117`、`ui_radio.go:301–333`；前者直接换路由，后者两套元素分别绘制，未找到配对。 |
| **完整 ReduceMotion 兜底** | 扫 `internal/app` 的 ReduceMotion 使用点，读 Stage、粒子、烟雾与 Painter 路径；没找到统一拦截自绘时间动画的应用层机制。 |
| **新界面的图片查看器接线** | 搜索 `native/internal/chatui` 与 `native/cmd/sumika-proto` 的 Viewer／媒体相关符号；没有找到完整路径。这里只说明新界面调查结果，不代表旧 `native` 根包没有 Viewer。 |
| **真实掉帧、耗电、CPU/GPU 时间和实际观感** | 仅读测试与实现；没有启动应用、执行测试或生成截图。源码的频率、尺寸和测试门槛不能作为实测结果。 |

全程仅使用文件读取与搜索；没有创建、修改、删除文件，没有运行程序，也没有把报告写入仓库。
