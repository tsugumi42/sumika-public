# Sumika 桌面程序（mygo + CrychicUI + MewTypeUI）

一个 exe，用 Go 的 mygo 画原生界面。聊天外壳、会话列表、消息壳与正文、本地输入/停止/审批及聊天外围标签已使用 MewTypeUI，其余页面分步替换 CrychicUI（ADR-245～247）。导航栏 → 会话列表 → 聊天窗口 → 可选右侧栏由 MewTypeUI 自适应布局管理，窄窗切单栏页面。Go 后台和界面同进程，界面直接调 `internal/api` 的读接口和可选本地写接口，不开 HTTP 端口（规格-界面接Go后台，ADR-157、ADR-178）。

- 双击默认**只看历史**（archive）：读 Go 的库，不连 QQ、不查模型（ADR-209）；托盘里勾了"启动后自动接收 QQ"的除外，起来以后会自己走一遍"开始接收 QQ"（默认不勾，ADR-227）。
- 配置里**一个 QQ 小号都没有**时，双击直接进“本地可用（QQ 没开）”：本地私聊和本地群聊能写，QQ 不连；数据目录不存在或是空的会先初始化，还一张角色卡都没有时从资源根复制一份示例卡（[规格-不带QQ也能用](../docs/规格-不带QQ也能用.md)）。这时托盘和 `sumika-ctl receive stop` 的“停止接收”只回“现在没在接收 QQ（只开了本地）”，不动后台。已知的过渡限制：有小号的机器停止接收 QQ 以后本地也跟着回到只看历史，每个平台单独启停等 P5-7。
- 右下角托盘：打开主窗口、查看后台状态、开始 / 停止接收 QQ、启动后自动接收 QQ（默认不勾）、开机自动启动（默认不勾）、退出。
- 关窗口（X、Alt+F4）只隐藏，后台照常；退出要用托盘菜单，退出前先收好后台。
- 后台启动（receive，或零小号时的本地可用）后，本地私聊可发文字、看流式回复、停止生成，并在聊天区处理真实审批卡（允许一次 / 拒绝）；本地群聊共用输入框，每群草稿独立，整轮回复时可停止，错误显示在输入区，已发消息保留。群里没有审批卡或本地执行工具；QQ 私聊和 QQ 群仍只查看记录。archive 输入区提示先启动后台。
- 设置 → 外观可编辑桌面主题，立即生效；设置 → 数据 → 数据位置可改写 exe 旁的 `sumika.root`，重启生效；后台配置只读。
- 可写后台在本地会话栏提供“新建群聊”，选择角色、起名后能查看空群和群历史并发送文字。群发送/停止走可选 `api.LocalGroupWriter`；Reader 不实现它时不显示群输入框，见[本地群聊规格](../docs/规格-本地群聊.md)。
- 旧后台（Bun，7720）还开着时，Go 不开库，状态页写明原因（规格 7.1）。
- 没有命令行窗口，日志写 `<数据目录>/logs/desk-<日期>.log`（后台的行和后台状态的每次变化，包括失败原因）；演示数据不写。
- 开着的时候，命令行 `sumika-ctl`（`cmd/sumika-ctl`）经一条只给当前用户的命名管道做托盘能做的事，主人和 Agent 都能用；用法见 [Go 命令行工具](../docs/subsystems/go-tools.md#sumika-ctlagent-怎么操作开着的程序)。
- 文件检查点默认开启（`tools.checkpoints.enabled`）：write/edit/PowerShell 留工作目录内原始字节，`sumika-ctl checkpoint list/preview/restore` 可查看与回退，`status/cancel` 可查进度并等取消退出。恢复须主窗口同会话单次批准，先保存逆检查点；取消后可对逆 ID 重新预览 recovery 撤回或 resume 续做。PowerShell 的外部副作用和排除目录不受恢复保护；详见[检查点规格](../docs/规格-检查点与回退.md)。
- QQ 视频 ≤100 MiB 自动下载；视频及文件的下载、真实字节进度、取消和重试由 MewTypeUI 内容组件展示，后台契约见 [QQ 视频规格](../docs/规格-QQ视频.md) 与[收图规格](../docs/规格-QQ收图下载和断线补录.md)。完成后重读原消息或转发层，图片文件可直接看、视频文件交系统播放器，其他文件显示完成状态。有 ffmpeg/ffprobe 时生成封面与时长。
- 本地输入支持 Enter 发送、Shift+Enter 换行；生成中原发送位置变为停止，附件与表情选择尚未接入。思考/工具过程仍缺桌面数据接口，当前只显示真实正文草稿与审批。外壳十枚 Lucide 图标独立嵌入（[许可](assets/nav/LICENSE.txt)）；链接复制和图片开关提示音经 MewTypeUI sound 策略交 mygo audio 播放，不读取用户数据。

## 编译和打开

要装 Go 1.27 以上。在这个文件夹里：

界面组件库 MewTypeUI 的源码随仓库放在 [third_party/MewTypeUI](third_party/MewTypeUI/来源.md)，`go.mod` 的 replace 指向这个相对目录，干净克隆后直接能编，不用另外克隆；它来自独立仓库，要更新照那里的 `来源.md`。`go.mod` 的 replace 不许写本机绝对路径，目标目录必须在仓库里、且里面的 go.mod 已被 git 跟踪（`../../MewTypeUI` 这种跳出仓库的写法在本机能编、干净克隆编不出，也不行），仓库里也不放 go.work；`internal/archcheck` 会查。

```bash
go build -trimpath -ldflags "-s -w -H windowsgui" -o sumika.exe .
```

### 打包：把素材放到 exe 旁边

发出去的程序（Windows 便携包）是 exe 加几个文件夹，素材要从开发机拷过去，不在 exe 里：

```powershell
# 在 native/ 里，$out 是打包目录（exe 已经放在里面）
Copy-Item fonts $out\fonts -Recurse        # 文渊圆体、Maple、两份许可证；只拷要用的也行：WenYuanRoundedSCVF-subset.ttf（或完整版）、MapleMono-NF-CN-Regular.ttf、MapleMono-NF-CN-Bold.ttf 和两个许可证文件
Copy-Item twemoji $out\twemoji -Recurse    # 4009 个 SVG，加 LICENSE-GRAPHICS、VERSION（CC-BY 4.0 要带许可证）
```

正式程序只从 exe 旁的 `fonts/`、`twemoji/` 读，不依赖开发机上的 `native/fonts`、`native/twemoji`。找不到不会崩：字体退回系统字体，表情交字体画，日志里各记一行。

exe 文件本身的图标（资源管理器、固定到开始菜单时看到的）不用另外做什么：`rsrc_windows_amd64.syso`、`rsrc_windows_arm64.syso` 在这个目录里，`go build` 会自己把它们链进 exe（只有图标，不带版本信息和清单，程序的名字、开机自启项、单实例都和没有它时一样，ADR-230）。图标改了怎么重新导出见 `docs/assets/logo/README.md`。

| 参数 | 作用 |
|---|---|
| `-root <目录>` | 资源根；优先于 `sumika.root`，不给按下方顺序查找 |
| `-data <目录>` | 数据目录；不给照配置 |
| `-account <号>` | "开始接收 QQ"用的小号；不给时配置里恰好只有一个才用它，一个都没有就只开本地；给了配置里没有的号直接报错 |
| `-demo` | 只看编的演示数据（`mock.go`），标题写"演示数据"，和正式的单实例分开 |

桌面找根顺序（ADR-248）：`-root` → exe 旁的 `sumika.root` → 从 exe 目录往上找有 `sumika.example.yml` 文件的目录 → exe 目录。不依赖当前工作目录，开发机的 `native/sumika.exe` 可以直接双击；`cmd/sumika-go` 等诊断工具仍从当前目录往上找。

`sumika.root` 是 UTF-8 文本，可带 BOM。空文件或只有空白表示用 exe 目录；否则只允许一行非空路径，去掉首尾空白，支持绝对路径和相对 exe 目录的路径。例如 exe 在 `<仓库>\native`，内容写 `..` 就使用上一层；便携包将资源放在 exe 旁时留空。非空路径必须是已有目录，且里面有 `sumika.example.yml` 文件。多行非空内容、路径不存在、指向文件或缺示例文件都报错，状态页显示出错文件和行号，不再尝试其他目录；无法读取文件的系统错误也直接显示。

设置 → 数据显示当前根目录和来源；根解析失败、后台还未加载时也能打开这一项修正。保存采用同目录临时文件写入、同步、关闭再改名；Windows 短时拒绝改名时沿用桌面设置的规则，最多等 2 秒，持续失败保留旧文件并显示原因。当前进程继续使用启动时的根；保存不搬迁数据。若带了 `-root`，下次启动须移除该参数才会使用保存的位置。演示模式不提供改写。

根目录用于定位资源；数据目录仍按 `-data` → `SUMIKA_DATA_DIR` → 配置里的 `dataDir`（相对根目录）确定。配置文件仍按 `SUMIKA_CONFIG` → 已选数据目录的 `config/sumika.yml` → 根目录的 `sumika.yml` 确定；未读配置时的默认数据目录为 `<根>/data`。

演示模式下另有几个给验收脚本用的环境变量：`SUMIKA_SETTINGS=n`（打开就在演示设置第 n 页）、`SUMIKA_DEMO=scroll`（自动压测）、`SUMIKA_SIZE=宽x高`；`MYGO_FRAME_STATS=all` 让 mygo 把每帧耗时写到标准错误。手点的验收见 `docs/archive/notes/手试清单-托盘和窗口-2026-10-31.md`。

## 独立交互原型

`cmd/sumika-proto` 是全占位的交互演练，使用 mygo 自带控件，运行态只在内存中。入口不导入 Sumika 后台，不发现数据目录，也不连接模型或 QQ。具体范围与验收见交互原型规格（私有存档）的第一步实施记录。

在 `native/` 启动：

```sh
go run ./cmd/sumika-proto
```

本地页切私聊／群聊，输入后 Enter 或发送；Shift+Enter 换行。回复中可以停止、排队发送；明确失败在气泡左下的危险色角标点击重试，同一消息ID不变。上翻暂停跟随，点“回到最新”恢复。QQ 页是编造的只读记录，设置页深浅色只影响本次演练。窄窗按“导航与列表 → 聊天 → 资料”逐层返回。“场景”在宽窗导航底部、窄窗当前层标题栏各保留唯一图标入口；场景以窗口内浮层打开，Esc或“收起”关闭，三栏保持完整。

“场景”面板指定会话、角色、延迟、次数、间隔和 seed，可触发回复、两角色交错、下一次发送失败、假读搜及一万条历史，并暂停、单步、重置。场景目标不会随当前聊天悄悄改变。“脚本”可编辑并应用 JSON；当前支持 `wait/trigger/await/endTrack` 和 `A01/A02/M06`，未知版本、字段、操作或归属错误整份拒绝。读搜只有内置假结果；所有命令／写入及审批尚未进入本步。

窗口无边框，圆角由系统画（ADR-284），界面不自画外壳圆角；导航四边12 DIP、动作16 DIP圆角；几何常量及逐层骨架见规格第十一节。连续同一发言人的消息合组，跨后台回合也不重复身份头；组内4 DIP、组间16 DIP，靠头像侧相贴角6 DIP，mygo直接绘制四个不同角。群聊只在他人组首显示32 DIP首字圆头像和姓名，自己的消息只留气泡。头像同族使用角色色浅／深档实底，覆盖消息、会话、顶栏和成员；会话行铺满列表整宽、直角，46 DIP头像、行高62，选中整行填强调色；未读数有胶囊底。导航、会话、聊天使用三档相近中性底色。标题内容、消息列和输入外框共用752限宽竖线，搜索、聊天与右栏顶行基线对齐。右栏活动为空时显示“还没有活动”；空白草稿发送键中性禁用，停止可用。通用快捷键没有常驻提示文字。

开发检查和浏览器打包：

```sh
go test -short -count=1 ./cmd/sumika-proto
go vet ./cmd/sumika-proto
go tool mygo vet ./cmd/sumika-proto
go build -tags e2e -o <任务目录>/sumika-proto.exe ./cmd/sumika-proto
go tool mygo build -platform web -o <任务目录>/web-out ./cmd/sumika-proto
```

上述编译、测试、vet、量测按项目重活锁逐条用 `scripts/run-low.ps1` 调起。普通程序没有 automation；`e2e` 构建设置 `MYGO_AUTOMATION` 为任务目录内的短 socket 路径可用自动化插件。`MYGO_FRAME_STATS=all` 输出实际帧阶段耗时。浏览器产物从 `web-out/web` 通过独立 HTTP 静态服务打开；本步只演练一个页面内的窗口，不表示原生多窗口能力。

几何回归直接枚举Tester提交后的整棵树，包括未人工登记的着色、边框及透明布局节点；同时检查父子四边距离、兄弟间距、内缩圆角、标题基线和消息首尾避让。固定v0.7.4的测试内反射只读内部节点，不进入产品代码；升级mygo须复核这层读取。唯一的3条布局例外位于 `geometry_test.go`，均待主人批准，逐对输出仍保留执行的检查与原始距离。

## 真窗口自动巡视

`automation_e2e.go` 只在 `go build -tags e2e` 导入 mygo 官方插件。Windows 10 1803+ 使用 AF_UNIX socket（不是命名管道）；`MYGO_AUTOMATION` 要指向当前用户独占目录。该构建设置 socket 时必须带 `-demo`；单实例名和控制管道按 socket 隔离，演示只接 `mock.go`，不读配置、不开业务库、不连 QQ。不要把 e2e exe 当发布版。

在 `native/` 的 PowerShell 中，把 `$scratch` 换成本次任务的临时目录；确认 U: 未占用，给很长的 scratchpad 路径建一个短映射，避开 Windows socket 路径长度限制：

```powershell
$scratch = '<当前用户 scratchpad/ui-automation 目录>'
subst U: $scratch
go build -tags e2e -o U:/sumika-e2e.exe .
go run ./cmd/ui-tour -app U:/sumika-e2e.exe -socket-dir U:/ -out U:/tour
subst U: /d
```

构建和巡视按项目重活锁、`scripts/run-low.ps1` 逐命令运行。输出目录必须不存在；巡视只启动自己的 `-demo` 子进程，以官方 `act/input/idle/tree/capture` 依次走宽窗 2048×1030 DIP、窄窗 480×720 DIP 的深浅主题，不点击发送、接收和清空。每状态 PNG/JSON，JSON 带客户区、物理像素、无障碍树、检查器 Issues、正常段与检查器段逐帧日志及六类规则结果；`index.html` 排列全部截图，`report.json` 汇总，四份 `.log` 留完整帧日志。

若页面持续请求帧，`idle` 到 10 秒仍不结束，会把失败写进对应 JSON 的 `idleErrors`，截下当时画面并继续收集其他状态，最后整次巡视退出 1；不会增大超时或把它记成空闲通过。

直接启动供其他客户端使用：
```powershell
$env:MYGO_AUTOMATION = 'U:/manual.sock'
$env:MYGO_FRAME_STATS = 'all'
$env:SUMIKA_SIZE = '2048x1030'
$env:SUMIKA_AUTOMATION_THEME = 'light' # 仅 e2e + socket + -demo 接受 light / night；不写配置
U:/sumika-e2e.exe -demo
```

Windows 125% 时应实际量到约 2560×1288 物理像素；真实截图与客户区为准，不用改 PNG 尺寸伪造缩放。规则覆盖越界/裁剪、裁字候选、32 DIP 点击区、同父导航高度、重复 Key 和空状态文字。AX 不提供字形排版和实际省略号，框内文字截断不能自动证明；重复 Key 也可能来自被 AX 省略的不同布局父级，候选须对照 Issues。检查器在产品截图之后打开，Issues 是进程累积值，检查器引入的帧单列。完整范围和验收见[巡视规格](../docs/规格-原生界面自动巡视.md)。
巡视按启动参数选深浅主题。窄窗中设置控件可能已经越界，不能依赖点击它才能采集该主题；越界仍照实报告。手动省略 `SUMIKA_AUTOMATION_THEME` 时沿用演示默认主题，非法值在创建窗口前返回 2。


`go tool mygo vet .` 使用 `go.mod` 锁定的 mygo 工具。`scripts/go-verify.ts` 和推送入口在根包非测试 Go 文件或 Go 依赖变动时运行；诊断使检查失败，不写通过记录。本任务保留现有诊断，交回主会话处理。


## 文件

- `main.go`：参数、单实例、数据源接线（`attach`、概览订阅）
- `automation_e2e.go`、`automation_release.go`：按构建标签导入插件并隔离演示实例、控制管道和启动主题；普通构建不启用服务
- `cmd/ui-tour/`：官方协议客户端、状态清单、六类树规则、检查器采样和截图报告
- `shell.go`：主窗口、托盘菜单、关窗隐藏、唯一的退出流程
- `host.go`：后台宿主：开 archive、切 receive 和失败回退、退出收尾（全在后台线程）
- `datalocation.go`：启动时固定根目录、状态和设置展示、后台保存 exe 旁的 `sumika.root`；根解析及内容校验在 `internal/app/startup.go`
- `desklog.go`：开日志文件（`internal/filelog`，前缀 `desk`）
- `ctl.go`、`hostgate.go`：本机控制接口接进桌面程序：把 `sumika-ctl` 的命令接到宿主和外壳上（和托盘用同一批函数），群档位的窄接口
- `autoreceive.go`：启动后自动接收 QQ 的开关（存 `<数据目录>/go/desktop-settings.json`，托盘和 `sumika-ctl auto-receive` 是同一个）
- `session.go`：打开的会话：订阅、按行版本并进、往上翻
- `data.go`：展示 DTO → 界面行（`msgFromView`）；`segFromDisplay` 只包装 `DisplaySegment` 和 UI 下载键，正文、媒体、预览直接使用 DTO 种类与字段
- `internal/segview/inline.go`：新旧界面共用的纯行内模型与链接拆分、文本清理、表情回退、空白裁剪；段模型和 `blocksOf` 暂留根包，分步范围见 [新界面主线](../docs/规格-新界面主线.md)
- `localgroup.go`：建群表单、角色选择、后台调用和空群打开；输入框不在这个文件里
- `demo_reader.go`、`mock.go`：演示数据的假只读接口
- `ui.go`、`panes.go`、`titlebar.go`：MewTypeUI 外壳、导航、顶栏、整页路由与分栏偏好；`status.go` 展示后台状态与正式设置，`settings.go` 保留演示后台设置，外观页共用 `appearance.go`
- `convlist.go`：MewTypeUI dialogs.Model/List/Search；宽列表与 66 DIP 头像栏共用虚拟行，按完整 ConversationKey 保持选择。本地空私聊也是普通行；概览、搜索和成功头像完成使快照失效，构建帧复用模型，只请求当前可见行的头像。
- `localchat.go`：本地私聊与本地群聊共用 Composer 和每会话草稿，按会话类型分派发送/停止；流式草稿与审批卡仅本地私聊使用。后台可选接口在 `internal/api/localchat.go`、`local_group.go`，桌面实现为 `internal/app/desktop_chat.go` 与 `internal/desktopread/local_group.go`
- `row.go`、`bubble.go`、`quote.go`、`reaction.go`、`arrive.go`：MewTypeUI Row/Bubble、Quote、Markdown、Reactions 与 Reveal；组尾首字头像、群组名字色、逐条时间和正文复制共用同一套。`body.go` 按窗口、会话、消息段持有 `chat.RetainedBody` 的排版与选择状态，虚拟行离开构建范围即释放；同一布局负责绘制、对象框、命中、链接和选区。`forward_rows.go` 复用消息壳并保留节点内虚拟分块；`picture.go`、`images.go`、`anim.go` 管媒体显示、读取和播放缓存，`inline.go`、`faces.go` 保留 QQ 小黄脸与复制还原。
- `picture.go`：MewTypeUI Photo/GIF/Sticker/Album；相册每十项分组，保留每图打开、网络失败重试与坏格式说明。动贴纸解码请求上限 256 像素，逻辑尺寸仍取主题；`images.go` 的读取/解码/缓存层不随组件迁移改动。
- `card.go`：MewTypeUI LinkPreview + Photo 组合链接卡与稳定封面占位；`video.go`、`video_windows.go`、`video_other.go`：Video 封面、后台准备本地副本、系统播放器打开；测试替换打开动作。
- `file.go`、`media_download.go`：File 组件与视频共用 Download 凭据、订阅、显式请求/取消及完成后重读；状态按消息来源、转发路径、节点和段位置隔离。虚拟行离屏、切会话或换 Reader 结束订阅，不取消后台下载。
- `forward.go`：MewTypeUI ForwardSummary 摘要、Box.FullBody 转发窗口，宿主持有返回栈、节点虚拟列表及退场缓存寿命；`ui.go` 绑定原生浮层 Native，来源、链接及贴纸说明使用原生 Tooltip。Viewer 与链接编辑菜单的待接能力见聊天界面规格。
- `demo.go`：演示回复（按字流式）、自动压测，只在演示模式里能用
- `icons.go`、`icons_windows.go`、`icons_other.go`：托盘图标按当前缩放比例下系统小图标的边长挑一张（`assets/tray/`，go:embed）；窗口图标：exe 带着图标资源就不设，没带才用 `assets/icon/icon-256.png`
- `assets/`、`rsrc_windows_*.syso`：logo 导出的 PNG 和图标资源，源文件在 `docs/assets/logo/`
- `cmd/logotool`：一次性的素材工具（SVG 渲染成 PNG、和原图对拍、PNG 打成 `.syso`），不进发出去的程序

## 外观

设置 → 外观可选择亮色、夜间或跟随系统，调整强调色、两侧气泡颜色、大小圆角、字体、字号（13 到 18，亮色与夜间一起改）、窗口材质与表面不透明度。亮色与夜间各存一份，编辑当前显示的版本。字号叠加系统文字缩放一次；窗口不支持系统材质时使用实色并提示。

主题存在 `<数据目录>/go/desktop-settings.json` 的 `appearance`，不写 `sumika.yml`。缺文件使用 MewTypeUI 默认预设；坏文件回到默认并在外观页显示原因。有效编辑异步原子保存，失败说明没有存上；演示模式只在窗口内保留。

导入与导出使用原生文件对话框和 MewTypeUI 当前版本的主题 JSON（版本号跟 MewTypeUI 的 `theme.Version`，旧版本的文件报错，不做兼容，ADR-247）：导入按文件的深浅属性启用，导出当前版本的完整参数；恢复默认清除两份自定义并跟随系统。`theme.go` 管缓存，`themefile.go` 管宿主文件操作与保存顺序，`internal/desksettings/appearance.go` 管存储边界。详细约定与验收见 桌面主题规格（私有存档）。

消息壳、引用、Markdown、回应和首字头像直接读取 MewTypeUI 字阶与颜色角色；正文最大宽度由当前列表宽度和消息内边距计算。未迁的设置控件仍跟随映射后的 `ui.Theme`；现行查看器的圆按钮与 `kit.Shade(0.88)` 保留到 Viewer 契约补齐，具体缺口见聊天界面规格（私有存档）。

可选截图：设置 `SUMIKA_THEME_SHOT=<目录>`，运行 `go test -short -count=1 -run TestThemeShot .`，输出 `light.png`、`night.png`。视口 2048×1030，像素比 1.25，只截外观设置区域；普通测试不写图。

## 界面字体

软件自带的素材（图标、表情、字体）由 `internal/assets` 统一提供，找法只有一套（`assets.FontDirs`、`assets.Twemoji`）：明说的目录 → 环境变量 → exe 旁边的 `fonts/`、`twemoji/` → 开发时的 `native/fonts/`、`native/twemoji/`（在 worktree 里跑时回主工作区找）。来源和许可证汇总在 [`internal/assets/THIRD_PARTY.md`](internal/assets/THIRD_PARTY.md)。

- **图标**：MingCute 全套（line、fill，1658 个）编进 exe，不用放文件。`internal/assets/icons` 按名字取；表由 `bun scripts/gen-mingcute.ts` 生成，升级版本重跑它。
- **表情**：社区版 Twemoji（jdecked/twemoji，图形 CC-BY 4.0）4009 个 SVG，放在 `native/twemoji/`（不进 git），用 `bun scripts/update-twemoji.ts --yes` 下载或升级，现在是 v17.0.3（不加 `--yes` 只打印计划）。切表情序列、按序列取图在 `internal/assets/emoji`，界面和推特卡片共用。没找到目录就交字体画并记日志。
- **字体**：`internal/assets/fonts` 按用途给出一串字体文件，依次是文渊圆体 → Maple Mono NF CN → Windows 自带字体兜底：
  - 文渊圆体（家族名 `WY Rounded`，可变字重 100 到 900，mygo 用 wght 轴取各档字重）：`WenYuanRoundedSCVF.ttf`（完整版，46.5 MB）或 `WenYuanRoundedSCVF-subset.ttf`（常用字子集，约 15 MB，`scripts/subset-font.py` 从完整版裁出来，命令写在脚本开头；主程序启动时子集优先，推特卡片完整版优先）。环境变量 `SUMIKA_FONT` 可指定文件。许可证 [`fonts/WenYuan-LICENSE.md`](fonts/WenYuan-LICENSE.md)。
  - Maple Mono NF CN（OFL-1.1，[`fonts/MapleMono-OFL.txt`](fonts/MapleMono-OFL.txt)）：只认 `fonts/` 里的 `MapleMono-NF-CN-<字重>.ttf`（Thin、ExtraLight、Light、Regular、Medium、SemiBold、Bold、ExtraBold，按字重取），**不再扫系统字体目录和用户另装的字体目录**。开发机从 `%LOCALAPPDATA%\Microsoft\Windows\Fonts` 把这些文件复制（不是移动）到 `native/fonts/`；程序要用的至少是 Regular 和 Bold。
  - Windows 自带字体兜底：只到 `%WINDIR%\Fonts` 里按固定文件名找（微软雅黑 `msyh.ttc`、Yu Gothic `YuGothR.ttc`、Malgun Gothic `malgun.ttf`、Segoe UI Symbol `seguisym.ttf`、Segoe UI `segoeui.ttf`），不扫目录。
- 字体文件太大，不编进 exe、不进 git（`.gitignore` 忽略 `native/fonts/*.ttf` 和 `native/twemoji/`，许可证文件进 git），和 `onnxruntime.dll`、`sumika-models.exe` 一样放在程序旁边。原型程序（`cmd/sumika-proto`）、截图测试用 `fonts.WenYuanPath()` 取完整字体；没有就跳过要用它的测试。
- 主程序的界面字体只读 exe 旁的 `fonts/`（`fontload.go`），没找到或注册失败时记一行日志、界面退回系统字体，设置 → 外观 → 字体下面有一行提示；用户选了别的字体就按用户的。推特卡片（`internal/linkcard`）按上面整套找，exe 旁没有就用开发目录，都没有就退回主题字体并记日志。

## 模型

设置 → 模型使用 MewTypeUI 设置行和搜索选择框。打开页读取一次，手动刷新更新目录；选完即保存，后台换模型不重启正在运行的回合。失败原因显示在对应行，文件版本冲突采用同次返回的新配置；“已保存，部分没生效”保留已保存的值并显示原因。只看历史时模型选择禁用，思考和上下文仍可保存。`-demo` 通过内存里的假 `ModelSettingsWriter` 使用同一页面，不联网或写配置。

`modelsettings.go` 管页面和异步结果，`demo_models.go` 管演示接口，`modelsettings_test.go` 用假的接口和 `ui.NewTester` 验证交互。字段、范围和错误语义见[模型设置页规格](../docs/规格-模型设置页.md)。

## 测试用的目录链接

`internal/testutil/junction.Create(link, target)` 只给 `_test.go` 使用：Windows 在进程内用已有的 `x/sys/windows` 写 mount-point 重解析数据，非 Windows 用 `os.Symlink`；不再启动 `cmd /c mklink`。调用者准备父目录，并决定创建失败是跳过还是报错；原来优先尝试符号链接的夹具仍保留该顺序。`internal/archcheck` 禁止发出去的程序依赖这个包。

实现参考 Go 标准库 `os/os_windows_test.go` 的 `createMountPoint`、`createDirLink`，以及 MIT 的 [nyaosorg/go-windows-junction](https://github.com/nyaosorg/go-windows-junction)。后者还依赖 go-winio；这里沿用标准库的数据布局，只用仓库已有的 x/sys。`TestCreate` 检查重解析点／符号链接类型、Readlink 目标和经链接读取真实临时文件。
