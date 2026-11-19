# 运行时：进程、配置、存储、模型、角色

开发规则见 [开发指导/回合内核](../开发指导/回合内核.md)、[开发指导/模型与服务商](../开发指导/模型与服务商.md)、[开发指导/存储与数据](../开发指导/存储与数据.md)。

## Go 内核身份与输入生命周期（分支，2026-11-04）

`feat/kernel-k1` 的 [`kernel`](../../native/internal/kernel/types.go) 只依赖标准库，定义 Scope/Invocation、输入和运行状态、Lifetime、预算及工作证据端口；[`turn/ports.go`](../../native/internal/turn/ports.go) 定义模块消费者。Hub 的 Submit/StopRun/Seal/Drain/WatchRun 共用持久 inbox，`runstate.Manager` 追踪实际退出；trace/0007 保存 inbox、运行、活动和交付链接。App 先封桌面/渠道/本地群入口，再取消、排空、关资源；超时保留库和锁供真实退出后重试。跨块执行器接线及验收边界见内核补强 5.3（私有存档）。

看门狗（[规格-看门狗](../规格-看门狗.md)，ADR-365）：配置 `watchdog` 一节，`heartbeatSeconds=60`（5～600）、`stuckMinutes=5`（2～120，且不小于 `heartbeatSeconds` 的三倍）、`startGraceMinutes=3`（1～60）、`crashLimit=3`（1～20）、`crashWindowMinutes=10`（1～240）、`restartDelaySeconds=3`（0～60）、`notify=true`。桌面程序写 `<数据目录>/run/heartbeat.json`；`sumika.exe -watch` 是守护进程，拉起不带 `-watch` 的自己，写 `run/watchdog.json`、`logs/watch-<日期>.log` 和 `logs/incidents/<时间>.md`；开机自启的 Run 项启动它。代码在 `native/internal/watchdog/` 和 `native/watch.go`。

运行自检（[规格-运行时自检](../规格-运行时自检.md)，ADR-367）：配置 `selfcheck` 一节，`enabled=true`、`intervalSeconds=300`（30～600），另有 S3～S7 的暂定阈值 `receiptUnknownPerDay=3`、`stuckTurnMinutes=30`、`reconnectCount=6` / `reconnectWindowMinutes=10`、`compactFailStreak=3`、`stageFailStreak=5`。桌面程序每 `intervalSeconds` 秒（启动后先等 2 分钟）只读评估各条不变量，写 `<数据目录>/selfcheck/<日期>.json`（原子写，每天一个；`state.json` 放序号水位等小状态）；计数器是 `internal/selfcheck.Counters`，入库一站、适配器、出口往里数，`nil` 时是空操作。读库只用 `store.OpenReadOnlyFile` 开的只读连接，包里不能 import `store`、`channel`、`app`、`turn`、`llm`（`deps_test.go` 守着）。状态变坏写一行 `selfcheck:` WARN，恢复写一行；`sumika-ctl status` 多一行“自检：…”。代码在 `native/internal/selfcheck/`、`native/internal/app/selfcheck.go`。

内核故障（`native/internal/turn/fault.go`，质检 X-07、后续路线 P0-4）：回合收尾写库失败不再让整台后台拒收。出错的地方先分类，类别用类型表达、不看错误文字：**只拒这一条**（插话载荷读不出：标成已取消、界面看到它被丢，不进故障）；**会话故障**（这个会话的运行状态、活动退出、插话重排写不进记录库，库和内存对不上）只让这个会话停止受理新输入，别的会话照常，它排着的下一轮不开始、话在库里改成 `held`（和重启恢复同一个结果，本地的话主人可在“没处理的话”里放行）；**内核故障**只有启动时整库的恢复（`RecoverKernelInputs`）和清扫（`SweepInterrupted`）没做成才有，所有会话先拒。故障只放内存，`Hub.Faults()` / `FaultOf()` 可读；`Submit`、`ReleaseHeld`、重复提交的认领都先查故障，拒收时返回 `*turn.FaultError`（`errors.Is` 认 `ErrConversationFaulted` / `ErrKernelFaulted`，原始错误可 `Unwrap`）。解除只有一个入口：`Hub.RetryFault(ctx, 角色, 会话)`（两个都空 = 内核的）/ `RetryAllFaults`，重写每处没写成的写入并读回确认，没恢复就返回带着新原因的“没有恢复：…”，故障保持；内核故障重试要求没有任何运行和活动在跑（再清扫会把在跑的当成上个进程没收尾的）。入口给主人和运维：`sumika-ctl kernel retry`（界面里的“重试”按钮还没做），不在模型能调的工具里；`FaultError` 故意不 `Unwrap`（原始错误可能是取消，认成取消会让调用方把“被故障拒掉”当成“被取消”），原始错误走 `Cause()`。故障的范围用显式的 `Fault.Scope`（会话 / 内核），不拿会话号为空当哨兵，`validateInput` 也拒空会话号。QQ 闸门（`channel.Responder`）遇到 `Respond` 直接回带错、没有回合号的结果（内核在受理这一步就拒了，没有“回合定了”），让这一批收尾：不回复、判断回填成出错、留一条计划，恢复后只处理新来的批次。没人认领的活动退出记录（`Hub.StrayEndFailures()`）不挡受理，在 S10 的 Note 里提一句，`RetryAllFaults` 一起重写。`runstate.Manager` 的活动退出失败也不再全局累积，按运行放（`WaitRun` 只返回自己运行的，`RetryEnds` 重写）。自检 S10 `kernel.faulted` 读它，见上；退出时（`Drain`）还有没解除的故障就和以前一样保留库和数据锁。

`agent.lifecycle` 默认：`maxPendingPerConversation=32`（1～256）、`maxPendingGlobal=256`（1～2048）、`drainTimeoutMs=10000`（1～30000）、`stopGraceMs=5000`（不小于 0 且小于 `drainTimeoutMs`）、`snapshotIntervalMs=50`（10～200）、`recoverInputs=hold`（唯一值）。停止时先封准入并取消模型和审批，让已派发工具在宽限内完成并保存结果；宽限到期才取消工具执行，`stopGraceMs=0` 表示立即取消。关机从封口前开始计时，宽限和排空共用 `drainTimeoutMs` 截止时间；未真实退出时报告失败并保留库和数据锁。重启只 hold 未准入输入，不自动调模型或发送。草稿只发容量 1 的 dirty，读取累计快照；节流窗口内最后一次更新在窗口末尾补发，模型暂停输出时也能读到当前正文，终态立即通知并撤销待发定时器；不逐 token 写库。工具、插件、模型和内部任务共用同一 Lifetime，排空等待真实退出。

`Respond` 的宿主便捷入口在 `Audience.Scene` 为空时沿用会话标识：含 `:group:` 的本地群聊、QQ 群推导为 group，其余为 private；不按 `PrivateOK` 推导场合或能力。显式场合仍与 Invocation.Scope 一致核验，`Submit` 不补身份。缺场景的群不能因此取得 `remember` 或本地私聊权限。

同步 main 后，本地私聊及话题的链接观察通知由持久 inbox 受理后的新事件分支发出，早于准入和上下文组装；不再依赖已被替换的旧 Send 队列。本地群聊仍走群服务的入库通知。重复 ClientKey 不重复观察。

检查点落点由 [`Paths.CheckpointsDir`](../../native/internal/paths/paths.go) 固定为 `<data>/go/work/checkpoints`，不随工具 CWD 改变；App 打开服务后统一串行捕获和恢复。`Hub.AcquireLocalMaintenance` 要求本地私聊 owner 会话没有真实运行及待受理输入，并在恢复和审计收尾期间拒绝该会话新输入；等审批时不占屏障，批准后重新取得并重算版本。恢复不删除历史、根账本或工具结果；成功或部分修改后重新建立会话 Live，后续 Root 从持久检查点读取恢复事实。实现与存储恢复规则见[检查点规格](../规格-检查点与回退.md)。


## Go 桌面根目录与数据位置（ADR-248）

桌面入口的 [`app.ResourceRoot`](../../native/internal/app/startup.go) 返回 `RootLocation`（目录、来源和 portable 文件位置）；`ResourceRootFrom` 注入 exe 目录供临时目录测试。顺序为 `-root` → exe 旁 `sumika.root` → 从 exe 目录往上找 `sumika.example.yml` 文件 → exe 目录。命令行诊断工具的 `config.FindRoot` 仍从当前目录往上找。

`sumika.root` 去 UTF-8 BOM、逐行去首尾空白；没有非空行时取 exe 目录，有一行时按绝对路径或相对 exe 目录解释，多于一行非空时报第二个非空行的行号。非空路径必须存在、是目录且包含 `sumika.example.yml` 文件；错误包含文件和行号，不退到上级搜索。读文件本身失败则显示文件及系统错误。

[`datalocation.go`](../../native/datalocation.go) 在桌面后台启动前固定根，并独立提供状态页与设置 → 数据的根、来源、错误展示，不依赖后台设置快照。设置在后台线程写入同目录临时文件，Sync、关闭、Rename 后才报告成功；失败清理临时文件、保留旧文件。保存不搬数据，当前宿主和日志继续使用原根，重启后生效；`-root` 继续优先，演示模式没有写入口。

数据目录和配置来源不改：`config.FileIn` 以 `SUMIKA_CONFIG` 优先，再找 `-data` / `SUMIKA_DATA_DIR` / `<根>/data` 下的 `config/sumika.yml`，最后找 `<根>/sumika.yml`；读取后数据目录由 `-data` 优先，否则 `Config.DataDirIn` 使用 `SUMIKA_DATA_DIR`、绝对 `dataDir` 或 `<根>/<dataDir>`。

实现参考：Telegram Desktop `Telegram/SourceFiles/core/launcher.cpp:252` 的 `CheckPortableVersionFolder`，借 exe 旁标记在加载数据前选位置、显式便携配置失败就终止的结构；写入沿用 `internal/desksettings` 的同目录临时文件再改名方式，不引入配置层。相关调研：[业界对照-我们的决定](../notes/业界对照-我们的决定.md)第五节的便携标记；验收参考[常见边界情况](../notes/清单-常见边界情况-2026-10-26.md)的文本、路径、空值、失败与重启类别，以及[性能与效率标准](../notes/标准-性能与效率-2026-11-03.md)。

共享改名边界 `internal/fileatomic.Replace`：直接复用桌面设置已有的 Windows 处理，只对 ERROR_ACCESS_DENIED / ERROR_SHARING_VIOLATION 每 20 ms 重试，调用方预算 2 秒；其他平台、其他错误立即返回，超出预算返回原错误。调用方继续负责关闭临时文件、失败清理和用户提示，不删除目标文件后重建。`sumika.root` 与桌面设置使用同一个函数，既有桌面设置测试覆盖共享冲突和持续占用；根写入另用真实文件短时占用做回归。该提取来自连续保存量测暴露的真实改名失败，不改变配置格式、查找顺序和重启语义。

根解析形状表（桌面后台、日志、配置均复用同一解析器；设置写入复用同一内容校验）：

| 形状 | 桌面解析 | 设置写入 |
|---|---|---|
| 无文件 | 处理了、测了：从 exe 目录查找 | 处理了、测了：创建文件 |
| 空文件、只有空白 | 处理了、测了：exe 目录 | 处理了、测了：空内容 |
| UTF-8 BOM（含 BOM 空文件） | 处理了、测了：去 BOM | 处理了、测了：去 BOM |
| 绝对路径、相对路径、`..` | 处理了、测了：按 exe 目录解释 | 处理了：保存后同规则读取；绝对、相对路径测了 |
| 路径不存在、指向文件、缺示例文件、示例是目录 | 处理了、测了：文件及行号错误、不回退 | 处理了：共用校验、拒写并保留原文件；前三种测了 |
| 多行非空 | 处理了、测了：第二个非空行报错 | 处理了、测了：拒写 |
| `-root` 和文件同时有 | 处理了、测了：命令行优先 | 处理了、测了：提示下次移除参数 |
| 往上找到 / 找不到 | 处理了、测了：找到根 / exe 目录 | 不可能：写入已有明确内容 |
| 目标 sumika.root 是目录（改名失败） | 处理了：读取错误 | 处理了、测了：如实报错并清临时文件 |
| 演示模式 | 不可能：不定位真实数据 | 处理了、测了：页面及宿主均拒绝改写 |

验收入口：`internal/app/root_test.go` 的 `TestDesktopResourceRoot`、`TestRootFileWrite`；桌面 `datalocation_test.go` 的 `TestDataLocationSurface`、`TestDataLocationErrorAndDemo`。前者用临时 exe 目录测试路径形状及 Windows 短时占用，后者用 `ui.NewTester` 在 900 / 2048 宽度下实际输入、保存及触发改名失败，检查当前根不提前切换；解析错误在设置未加载时仍展示。运行包限 `. ./internal/app ./cmd/sumika-go ./internal/desksettings ./internal/fileatomic`，测试与 vet 按共享重活锁及 `run-low.ps1` 执行。

2026-11-03 临时夹具量测（Windows，Idle 优先级，QPC 计时，三轮各 200 次）：找根 P95 为 346.3 / 296.9 / 304.4 µs，保存 P95 为 4.2278 / 3.7918 / 4.0176 ms，600 次读取及 600 次保存均无失败。900 / 2048 宽、1030 高、1.25 倍像素比下，数据位置所在设置页始终 21 个文本元素；Tester 重建 P95 分别为 113.7 / 119.9 / 124.3 µs 与 121.9 / 114.8 / 106.4 µs。每轮 200 次找根 CPU 46.875 ms，保存 CPU 343.75 / 375 / 390.625 ms，按操作系统累计时间差报告。测试进程工作集 54,824,960 字节、私有内存 84,893,696 字节，不能当成真实窗口常驻内存或实际呈现帧耗时。夹具最终只有 5 字节的 `sumika.root`（`root` 加换行）及 0 字节的示例文件，无临时文件残留。改名前的连续保存曾在 600 次里失败 4 次，真实文件短时占用回归在旧代码下报 Access is denied；共享改名处理后通过，持续失败仍如实报告。

## Go 桌面媒体读取（2026-11-04）

[`desktopread.Service.WithData`](../../native/internal/desktopread/media.go) 为当前数据组建立 [`mediastore.Reader`](../../native/internal/mediastore/media_read.go)，解析真实数据根并直接读取 Go 平台媒体目录。`App.Open` 在开库前取得数据组排他锁；`Desktop.Close` 先取消并等完读取，再由 `App.Close` 关库、释放锁。绑定失败保留原错误，后续媒体读取不能退回另一条路径。

| 入口 | 每次读取仍执行的检查 |
|---|---|
| `ReadMedia` | `mediaRow` 的可见事件／合法引用／转发链凭据；平台、hash、路径、句柄；20 MiB 字节上限 |
| `ReadAvatar`、`PrepareVideo` | 各自既有的身份／视频凭据；共用同一 Reader 的逐文件检查 |
| 独立 `mediastore.OpenMedia` | 平台、hash、路径、句柄 |
| `mediastore.CheckMedia`／media-check | 逐文件额外核对全文 SHA-256 |

ADR-260：显示路径不再重算整文件哈希。Reader 不缓存权限或存在状态；文件随后丢失、目录被换成联接时，下一次仍拒绝。文件内容被修改由解码报错或离线 media-check 发现，Put 仍按写入内容计算 SHA-256。原先只用于补元数据的 OpenMediaHead 已移除，元数据读取也共用 OpenMedia。消息页继续只查库。

[`native/images.go`](../../native/images.go) 用两条读取／解码流水线处理图片，队列只留凭据，优先当前可见行，再按最近请求排序；布局稳定后丢弃没有再次请求的测高行任务。量测、保护矩阵和回归证据见本地图片加载量测（私有存档）。

ADR-260 磁盘缓存位于 `go/media-thumbs/<平台>/<分片>/<hash>.v1.<档>.png`。`ReadMedia` 先验证凭据并打开原件，才调用 `Reader.Thumbnail`；命中仍检查源文件和缓存的路径／句柄。缓存只校验 PNG 块 CRC，不重复解码源件；缺失或坏缓存按分片合并生成，临时文件写完 Sync／Close 后由 fileatomic.Replace 发布。GIF／APNG／动态 WebP、小到无需缩放的图返回原字节。缓存不入业务库或备份。完整参数和崩溃、孤儿缓存语义见[规格 5.2](../规格-界面接Go后台.md#52-界面怎么拿到图)。

聊天图、静态表情、卡片与视频封面在 Painter.Scale 得到实际比例前只占位，随后按物理宽选择档。静态灯箱读取 tier=0 原件；未完成时沿用聊天位图。`TestMediaThumbnailLifecycle`、`TestMediaAnimatedBypassesThumbnail`、`TestMediaThumbnailJunctionRejected`、`TestImagePhysicalScaleAndOriginalLightbox` 覆盖重启命中、坏文件、并发重做、崩溃残留、授权、源缺失、动图、缓存联接和屏幕缩放切换。

像素比例每个聊天／转发窗口只在稳定根的绘制回调采样，避开图片自己的入场透明动画；否则实时表情的首帧透明会阻止采样并延迟取图。`TestStickerArriveOnlyWhenLive` 在逐图采样实现下报入场结束后的像素差异，根采样后通过；原有表情框、透明背景、重试、提示及三层转发回归继续保留。过时的固定 2 倍档位断言已删除，实际屏幕档位由物理比例回归验证。

回归入口：`TestReaderChecksEachOpen` 覆盖后续缺失和跨平台联接。诊断内容不符由 `TestMediaCheckFailures` 覆盖；打开时路径替换及重解析点句柄由 mediastore 的 Windows 测试覆盖。`TestImageVisibleBeforeQueuedOffscreen` 锁住可见图优先，`TestImageTransientRowsDoNotReadFiles` 锁住测高行不读文件。数据全在临时目录，媒体夹具持有与生产相同的数据组锁。

## Go 本地群存储（2026-11-03）

本地群的目录和有序成员只存 local 平台库，迁移 `native/internal/store/migrations/local/0002_local_groups.sql` 新增 `local_groups`、`local_group_members`。消息仍在通用 `events`，每个成员在 `observations` 记一份观察关系，trace 仍共用。唯一会话键 `console:group:<id>`；读目录包含无消息群，按角色筛选必须是群成员。archive 不迁移旧库，旧库没有群表时不列本地群。接口和运行路径见[回合与主窗口](agent-and-console.md)。

## Go 补录读取与动作（2026-11-04，feat/gap-backend）

`*app.DesktopReader` 通过可选 `api.GapReader` 提供 ReadGaps / WatchGaps / WatchAlignment，ReadAlignmentGaps 用于状态页与 ctl 的当前账号分页；可选 `api.BackfillActions` 提供 RequestGapBackfill / RequestAlignment / WatchBackfill。`api.Reader` 本身不扩大，读取仍由 desktopread 校验会话和观察范围，动作注入同一个 qqBackfill，不另建平台查询工人。

界面取到 Reader 后断言可选接口：聊天窗口以当前 ConversationKey、Character、可见时间窗订阅 WatchGaps；按钮把返回的完整 GapRef 原样交给 RequestGapBackfill，再以 JobID 订阅 WatchBackfill；状态页、设置、托盘共用 WatchAlignment，账号缺口列表按 ReadAlignmentGaps 的 NextCursor 分页。Subscription.Close 退出旧查询；回界面线程应用快照。completed 仅表示本轮结束，消行依据重新读取的实际缺口，不能只看任务文字。第 2～4 步界面未接入；[QQ 参考页](qq.md)集中维护表结构、调度和媒体边界。

消息页补录联合量测暴露通知和引用目标的全会话扫描；chat/0013_notice_target.sql 给 kind=notice 建 (conversation, reply_to) 部分索引，通知按目标、时间排序；引用目标以 VALUES 外表逐 ID 查，均保留原观察过滤。媒体对齐在 json_each 前筛窗口及本轮新增 seq，正文段与转发根共用一次扫描；原类型、准入和树预算边界不变。查询依据与三档三遍验收数字见补录缺口规格第九节。


## Go 模型页后台（ADR-249，2026-11-03）

`internal/config.Parse(bytes, source)` 是启动与写回自检的共同默认值、类型和范围校验；解码前先按目标类型核对键（`struct_keys.go`）：要解码成结构体的映射里出现 `0:`、`true:`、`null:` 这类非文字键就报错并给行号（goccy v1.19.2 遇到这种键会静默丢掉整个映射），要解码成 map 的地方（如 `senders.groups` 的数字群号）照收；上下文另要求 softRatio ≤ hardRatio。`ModelChange` 用字段枚举、独立 Character ID 和 String / Int / Float / Bool 指针表达单项修改，不接受任意路径。`PatchModel` 用现有 goccy AST 定位、在原文补丁，候选重新解析并连未知键一起做语义对比；CRLF 仅在定位副本转 LF。支持单行标量、块映射和 flow 映射（空 `{}` 转块）；目标路径经过 alias、anchor、merge、多文档或跨行标量时拒绝，原文件不动。

`SaveModel(ctx, source, history, baseVersion, change, now)` 只写配置实际 Source；无 Source 不创建。进程写锁内校验 SHA-256，旧文件以 0600 原样写入数据目录 `config/history/<时间>[-序号]-sumika.yml`，O_EXCL 防同秒覆盖；候选同目录临时文件 Sync、Close，替换前再次比旧文件，最后用 `fileatomic.Replace`。失败不发布运行配置，临时文件清理。

`app.ModelRuntime` 把模型页字段、预计算的 turn.Config 和当版模型列表一起原子发布，不修改 App.cfg / Hub.Cfg，也不重建 Hub。回合、手动压缩、硬门槛恢复、溢出恢复、提前压缩沿用本轮冻结版本；说话模型换了，下一轮重建角色实例，历史从库接回。本地私聊、本地群聊用 local，QQ 私聊用 private，QQ 群用 group。后台总结和守卫确认在开头一次取模型与生成器，生成器固定该版能力和后台思考。`memwrite.EnableSummary` 只在已经可写的记忆上复用原来的授权、路径、schema、首次备份、catchUp 和元数据前提；前提成功才原子开整理与自动提升，绝不开 remember 权限。清空后台模型关整理、重置 catchUp，守卫与发送门继续按现有 guardFull 收紧。

额度按场合分两组（2026-11-09，ADR-334 改）：顶层 `agent.budget.*` 管 QQ、群聊、本地群聊和后台任务（无人值守，默认硬停：24 次请求、60 次工具、524288 令牌、累计输出同 524288、15 分钟）；`agent.budget.local.*` 只管主人在本地主窗口私聊里干活（`kernel.Scope.LocalWork`：本地、console、私聊、主人、会话号不含 `:group:`），**默认全关**（每项 0 = 不设，别家主窗口都没有这几个上限，防跑飞靠 `turn/guard.go` 的重复调用检测和用户打断），要设再写正数。单次输出两组都默认 0 = 跟模型：`requestFor` 按模型的输出上限和窗口余量算好放进请求，`modelcall` 不再套固定值，写正数只是可选封顶；模型没报输出上限按 `llm.FallbackMaxOutput`（32000）。`config.Budget.LimitsFor(scope, 受理时刻)` 受理时挑一组，0 在这里换成 `kernel.NoLimit` 和一百年后的时限（账本只认正数，账本不改）；`Local.loaded` 为假（没按配置加载，手拼的 Budget）时都用顶层。`modelcall.reserveOutput` 的预留只是按并发槽数均分的占位，不等于发送值；"算出 0"分真耗尽（实际已花的到顶才停根）和只被同根在途请求占住（等它们结算再算，不停根）。子代理（`subagents.softRequestBudget`，默认 200，照 omp）：请求到软预算那一次之后往它的追加指示里放一句收尾提示，到 1.5 倍强停，0 = 不设；它的令牌、工具、累计输出只受父根余额管，不再各自写死额度。 `agent.budget.maxCostMicros` 设不了：加载配置就报错，因为模型调用端没有可证明的价格上界，设了账本会拒掉每个请求。宿主自动发起的轮（目标续轮、定期任务，`turn.StoredSend.Auto` → `Input.Auto`，随持久载荷保存）身份是主人但没人看着，用顶层那组（`LimitsForInput`）。本地回合到顶、最后一条回复被截断、或思考占满单次输出被网关回 502 时，`budgetNoticePlugin` 在对话里存一行系统行说明，数的来由见[编程额度实跑](../notes/排查-编程额度实跑-2026-11-09.md)。

模型的窗口和单次输出（ADR-334，`internal/modelmeta`）：每个字段单独按 用户覆盖（`modelMeta.overrides.<模型ID>`）> 服务商或网关 `/models` 返回 > models.dev 本地缓存（只补空，绝不盖服务商已报的值）> 保守默认（窗口 128000、输出 32000，标"未知"）取，来源记在 `llm.Model.Limits`。`app.initModelMeta` 在取模型列表前读 `cache/models-dev.json`，`startModelMetaRefresh` 后台带 ETag 刷新（TTL 4 小时、失败退避 5 分钟、响应上限 16 MB，坏缓存改名隔离，离线用任意年龄的缓存，取不到不影响启动；注入假模型客户端时不联网），`resolveModels` 在取回列表、改覆盖、缓存换内容后重算（`modelmeta.Resolve` 可对同一列表反复算）。models.dev 同名模型原厂优先，各家不一致取最小并标 `DevMixed`，输出不小于窗口当输出未知；窗口来源是默认或不一致时压缩硬线比例最多 0.75（`ContextLimits.hardFor`）。模型页"模型的上下文和输出"一节每个模型一行写窗口、输出和来源角标（手填、资料库、未知；服务商报的不加字），点一行改或恢复覆盖（`config.ModelContextOverride`、`ModelOutputOverride`，写 0 去掉），点"模型资料库"那行立刻刷新。

K3 分支新增 `internal/turnbudget` 持久根账本与 `internal/modelcall` 模型端口，使用纯标准库 `kernel` 契约。调用者先建立有截止时刻的 Root，再按宿主 AttemptID 预留、登记派发、结算；相同预留/结算幂等，重复派发拒绝。缺 usage 保留预留并标不完整，实报超额记录真实账；金额门缺可信硬上界拒派。trace 迁移 0009 保存根预算、执行号及 model_runs 的身份、usage_seen、未知费用；本分支补的步骤协议号/序号列在整合时与 K1/K2 去重。宿主接线见内核补强规格（私有存档）。

`defaults.fallbackModels` 配置有序备选模型链，`characters.<id>.fallbackModels` 省略或 null 继承，显式空表禁用；模型 ID 不能为空、重复或带首尾空格。角色对话和后台调用共用链与 60 秒冷却状态（冷却键是规范引用 `服务商ID/模型ID`，同名模型在两家服务商互不影响，ADR-390 M2），模型能力和配置快照随调用冻结；每次切换使用同一根预算和步骤、独立 attempt，trace 记录切换元数据。只对可切换的模型故障继续，预算、取消及请求参数错误不切换；链为空保留原有行为。备选模型须由使用者自行测试并配置。Go 的独立识图后台尚未实现。

子任务使用独立来源授权；`kernel.Budget.BindChild` 由可信子任务宿主将精确子 Run/Scope/Generation 绑定到父根账本，不能改角色、账号、场合或发起人，也不建立第二份余额。绑定保存在根账本 JSON 中；每次子模型派发仍检查来源授权，撤权后不能靠已有记账绑定继续执行。

工具批次将业务 `Result.IsError` 作为模型可读结果保留，同行调用与后续补位继续；取消、超时、执行器错误或结果保存失败才终止批次。超时结果可提前交回，但槽位和 Lifetime 活动仍等工具真实退出后释放，独占写屏障不因此提前穿过。


桌面通过 `*app.DesktopReader` 的可选 `api.ModelSettingsWriter` 调用，`api.Reader` 不扩张，控制接口和 QQ 没有写入口：

| 方法 | 返回与调用时机 |
|---|---|
| `ModelSettings(context.Context) (api.ModelSettingsView, error)` | 打开模型页时调用；receive 拉一次列表，archive 不联网 |
| `RefreshModels(context.Context) (api.ModelSettingsView, error)` | 只由刷新操作调用；失败保留上次列表和读取时间，ModelsError 给出原因 |
| `SetModelSettings(context.Context, api.ModelSettingsChange, baseVersion string) (api.ModelSettingsView, error)` | 选择后保存单项；baseVersion 必须取上次 View.Version；不为保存重复拉列表 |

视图的 Configured 是磁盘配置，Running 是已发布快照，RunningVersion 是该快照的配置哈希，Active 是在途回合的实际 Model / Thinking / Window / Reserve / Soft / Hard 与 Version；RunningReason 说明下一轮切换。Groups 按 owned_by 分组，Models 提供名称、标签、能力、未报告标记和 Subtitle；Characters 提供配置/运行模型及列表缺失标记；ReadOnly 列话题、识图和地址。archive 的 RunningKnown=false、ModelsEditable=false，思考和上下文仍可保存；无角色目录也可编辑全局值。视图及其 map / slice 是返回副本，调用方修改不会污染运行快照。

普通校验或 I/O 错误表示未保存；`errors.Is(err, api.ErrModelSettingsVersion)` 时使用同次返回的重读视图提示文件冲突；用 `var saved *api.SavedModelSettingsError; errors.As(err, &saved)` 判断已保存但部分没生效，必须展示返回视图及 saved.Reasons / ApplyErrors，不能回成旧值。`native/modelsettings.go` 通过 MewTypeUI settings 构建正式和演示的共同页面，打开与重新进入时读一次，刷新单独调用；worker 调用经 `app.post` / `Window.Update` 回界面，按数据源代次及请求序号拒绝旧结果，在途时暂停编辑，下一次保存使用返回 Version，不轮询。`native/demo_models.go` 是只保存在内存里的假接口；旧的模型原型及假下拉列表已删除。

参考本机 Magpie `internal/edit/yaml_top.go` 的解析定位、目标区域补丁、缺键插入、引号语义，以及 `yaml.go` 的候选重新解析；仅借结构和做法，继续使用已有的 goccy/go-yaml，不引第二个 YAML 库。后台验收覆盖见[模型页规格](../规格-模型设置页.md)第六节 1～7。

总结转换只在某个角色的有效后台模型“没有↔有”时执行；换思考、上下文、说话模型或“有→有”不会重试整理前提，也不会重置 catchUp。未生效原因按角色随运行快照保存，无关保存保留；清空 / 重新配上该角色的有效后台模型才更新对应原因。`TestModelSettingsDoesNotRetrySummaryOnThinking` 用首次整理备份失败→恢复目录→只改思考的顺序拦住附带重试。

模型后台量测（2026-11-03，Windows，Idle 优先级，临时数据、假 Magpie；每轮 100 次读视图 + 100 次同步保存，固定同一秒的时钟）：

| 指标 | 第 1 轮 | 第 2 轮 | 第 3 轮 |
|---|---:|---:|---:|
| 优化前保存 P95 | 30.5958 ms | 54.8709 ms | 76.2088 ms |
| 优化后保存 P95 | 9.143 ms | 9.1672 ms | 8.931 ms |
| 优化后读取 P95 | 1.1443 ms | 1.1059 ms | 1.508 ms |
| 优化前进程 CPU | 1671.875 ms | 3875 ms | 5484.375 ms |
| 优化后进程 CPU | 734.375 ms | 703.125 ms | 609.375 ms |

优化只是在原写锁内记住当前备份前缀的下一个序号，保留 O_EXCL；不再每次从 0 探测所有同秒备份。两版都符合测试前选定的本地操作 P95 <100 ms 预算（[性能与效率标准](../notes/标准-性能与效率-2026-11-03.md)的 NN/g / Windows 交互响应依据；这是后台操作预算，不是界面呈现耗时）。最终版测试进程工作集 39,268,352 字节、私有提交 64,299,008 字节；三轮末 Go 堆分别 1,801,728 / 3,113,360 / 1,850,688 字节。最终临时数据目录 314 个文件、1,704,111 字节，其中 300 份配置历史共 143,819 字节，临时候选文件残留 0；模型列表只请求 2 次（启动、打开页面），300 次保存不额外拉列表。原文、同秒备份份数和权限边界的行为验收保持不变；不将这些测试进程数值当成真实窗口常驻资源或帧耗时。


## Go 链接插件设置（2026-11-04，feat/link-expand，离线检查已执行）

配置来源为 `linkReader.context.enabled`（默认开）及 `linkReader.expand`。群里谁发的链接能触发展开由 `linkReader.expand.senders` 定（`mode` 默认 `all`，`whitelist` / `blacklist` 配 `users` QQ 号，`groups` 按群号单独设；主人永远能触发，私聊不受影响；只在配置文件里改，改完重启接收，或在链接设置页保存任意一项时一并读入，规格-链接解析 8.3）。取数节奏是 `linkReader.rate`（ADR-370）：`perMinute` 默认 60、`platforms` 默认 `bilibili: 10`、`maxWaitSeconds` 默认 45，额度用满排队等空位，平台回限流（冷却）才失败。展开默认关闭、白名单为空、每消息3条；翻译（`translate.*`）默认用后台模型，`translate.model` 填了就用它（空 = 跟后台模型，规格-链接翻译第七节）；只接受QQ完整会话键；各站点设置 `expand.sites.<站>`：`forward`（auto / always / never，没有转发模板的站点只有 auto / never；never 时图多于 `directImageMax` 只发前几张并在文字末尾标“（发出/总数）”）、`forwardOver`（x、telegram 按推特加权字数比、默认 600，B 站按字数，R-380）、`descLimit`、`showDesc`（显示简介，默认关，只有默认去掉过简介的站点能开，R-219）；正文因字数上限被截时统一走 `internal/linktrunc`（截在最近的标点后，补“……”和“完整内容见原文”，R-220）。本地私聊、本地群聊不能加入展开名单；QQ私聊键包含接收账号和对方账号，QQ群键包含群号。展开不再按敏感程度拦截（2026-11-08，旧的 `overrides` 已删）；B站默认卡片模板（旧 `bilibiliFormat` 已并入 `templates`）；推特拆图拼回（`linkexpand/panorama.go`）：多张同高竖条接缝连续时拼成一张发，`panoramaOriginals` 开了再用合并转发附原图；图发不出去时整段换形式：原样 → 整段静态图转 180° 同一个容器重发一次（`rotateOnReject`，卡片和缩略图也转；关着换不转的合并转发）→ 文字版；没确认送达的等回声（默认 2 秒，到了不补发、没到按被拒换形式，翻历史核实改成可选 `send.historyCheck`；≥ 2 MiB 的大图每 MiB 多等 9 秒、最长约 30 秒，等满还没回声也换形式，原来那份的回声晚到就撤回补发的那份，B15；结果不明且一段都没确认送到时不贴完成表情）；回声只剩文字（图被吞）撤回那一条再图文重发，撤不了只补发转好的图；网站可用 `platforms.<网站>.imageMode` 选从哪一级起；每一步记 `link · form_degrade`、转 180° 另记 `link · rotate_retry`（ADR-333）；发送前单图体积上限 `imageMaxMB`（默认 20）；展开时对帖子正文的翻译（`expand.translate.enabled` 默认开 / `target` 默认简体中文 / `timeoutSeconds` 默认 15（单次调用）/ `retries` 默认 1（超时再试一次）；单个网站 `platforms.<网站>.translate`；用后台模型，超时或出错只发原文，不写错误文案；现在只有 X 接了 `$译文`；X 文字版和合并转发的推文节点有译文时只放译文加“（翻译自日语）”，繁体中文也翻成简体，`$原文` 是原文，R-381；规格见 [链接翻译](../规格-链接翻译.md)）；网站和页面类型开关 `expand.platforms.<网站>.enabled` / `.kinds.<类型>`，默认全开，清单见 `linktemplate.Catalog`（[规格](../规格-链接页面类型和样式.md)）；群里三个进度表情默认 289 / 124 / 9（`startEmoji` / `doneEmoji` / `failEmoji`）；视频默认关、60秒/100MB。开关不修改角色档位、观察关系或 `qq.reply`。发送流程按[规格-链接发送改版](../规格-链接发送改版.md) A 节（已合 main，ADR-360）；文字排版按 B 节（分支 `feat/link-format-v2`）：行间不空行，推文“链接 / 名字[@ID]： / 「正文」 / 发帖时间”，pixiv 直发“首行 / 作品链接”，视频“标题 / UP主 | 时长 / 播放 | 点赞 / 投稿时间 / 链接”，分隔竖线一律半角“ | ”，转发前不另发提示，引用推文在转发里标被引节点（`ForwardNode.ReplyTo`，协议端不支持时退回）、`send.forwardVideo` 关着时视频拆出转发单独一条；新键 `expand.directImageMax`（默认 3，1～9）、`expand.text.stripTrailingHashtags`（默认开）。推特卡片（C）还在改版。发送流程的新键在 `expand.send` 子块（`config/link_send.go`）：`echoWaitMS` 默认 2000（500～10000）、`historyCheck` 默认关、实验开关 `forwardVideo` / `forwardQuoteReply` 默认开。推特取数（[规格-链接发送改版](../规格-链接发送改版.md) C 节）：fx_v2、vx 同时取、先到的成功结果为准，都失败再试官方嵌入接口（`linkReader.x.fxTimeoutSeconds` 默认 4、`x.syndication` 默认开），按近期成功率排先后；x 帖子的 `$卡片` 画推特网页样式的卡片（`expand.tweetCard.theme` 默认 auto，按北京时间 6–18 点浅色；`expand.tweetCard.maxHeight` 默认 0 不封顶，要封顶写 480～8000 像素；`expand.tweetCard.textOnly` 默认开，纯文字推文画成卡片加链接，ADR-399）。 画师的推文发原图（[规格-画师原图](../规格-画师原图.md)，ADR-371）：`expand.artist.mode` 默认 `cardAndOriginals`（卡片 + 链接 + 原图，待用户确认；`originalsOnly` 只发链接和原图；`off` 不特殊处理），`expand.artist.autoDetect` 默认开（展开时按需判断不在表里的带图原创推文作者，像画师的记进待确认，不生效）；画师表在 `<数据目录>/go/artists/x.json`，用 `sumika-ctl artist` 管；命中画师时自定义模板和 forward auto / always 不生效、拆图不拼回。 假 IP（fake-ip）：`linkReader.fakeIPRanges`（CIDR 列表，默认 `["198.18.0.0/15", "2001:2::/48", "fdfe:dcba:9876::/48"]`（ADR-392，收图下载也读它），安全项、设置页只读、改了重启接收）——解析到这些段时，取数 / 下载名单内的主机经 `linkReader.proxy`（或环境代理）按主机名连，名单外和没有代理时拒绝并写原因；段不能盖住本机 / 内网 / 保留地址（127/8、10/8、172.16/12、192.168/16、169.254/16、100.64/10、fe80::/10、fd00::/8 里短于 /48 的等），写成这样的配置加载时被拒（规格-链接解析 5.1，2026-11-13）。

`config.LinkChange` 是字段枚举的单项修改；`SaveLink` 复用模型页的进程文件写锁、版本哈希、原文备份、候选重解析及原子替换，不接受任意配置路径。名单模式与成员一起保存；宿主拒绝新增本机目录外的会话，历史失联成员仍可移除。`app.LinkRuntime` 在成功落盘后发布冻结副本，任务沿用入队时格式和参数，每动作前重读当前许可；撤销名单或总开关取消未发部分。

桌面通过可选 `api.LinkSettingsWriter` 的 `LinkSettings` / `SetLinkSettings` 读取、修改；不扩大 `Reader`、QQ、模型工具或控制接口。视图分别返回 Configured / Running、Version / RunningVersion、目录、在途任务版本和已注册插件名。archive 可保存下次接收配置，RunningKnown=false；不联网查询目录或媒体。`native/linksettings.go` 用 MewTypeUI 构建设置 → 插件，其他已注册插件只读列名，不另造权限注册系统；会话选择按稳定ID保存，支持搜索、虚拟列表和失联标记，切换黑名单需确认。异步结果按数据源代次和请求序号丢弃迟到响应，保存失败显示错误并保留已确认配置，重新进入重读；演示写入仅在内存。

验收入口：`internal/config/link_file_test.go`、`internal/app/linkexpand_test.go`、`native/linksettings_test.go`。本轮15个受影响包测试与同批vet退出码0，9项改坏全部检出，恢复后重验通过。Tester在200/1000会话、2048/900/480DIP、1.25倍下实际交互，三轮各200次；交互P95范围5.2976～10.7339ms，活动帧P95为3.2ms，>100ms停顿0。完整证据及未验证范围见[发送端规格第十节](../规格-链接自动展开.md#十实施分步和可撤回提交)；原生呈现和真实QQ未验。

2026-11-18（ADR-391，[规格-链接发送改版 B16](../规格-链接发送改版.md)）：X、Telegram 的直发/合并转发门槛改为 `linkReader.expand.sites.<站>.forwardOverBytes`（默认 16500 字节，范围 1000～17500，0 拒收），按直发模板渲染出的全部文字（含译文、作者、时间、翻译自）UTF-8 字节数比；B 站等仍用 `forwardOver` 字数。旧 `forwardOver` 在 x、telegram 不再生效，配置照旧读入、第一次展开记一行提示。合并转发节点也按同一字节上限拆（多字节字符不切坏）。普通消息（`never`、画师推文、文字版）也按同一字节上限拆多条：文字版前面一条没确认或失败时，后面被出口中止的条原样补发（同一条不重发），任一条确认送到就算 landed，标签分“全部送到 / 部分送到 / 没送到”（部分送到按失败贴表情）；直发拆条中间一条被拒后，同一拆条组的后面的条不再发，结局记部分送到、原因写“第 N 条被拒，后面 M 条没发”，不往群里补说明消息。单个链接本身超过上限时仍整条放进一条，可能被 QQ 拒收（已知限制）。`linkreader.Result.Expansion` 和 `classifyX` 已删，取数层不再参与容器选择。翻译：缓存键加模型 ID，“不用翻”保留 24 小时；超过 4000 字按段落最多 8 段并发翻、任一段失败整篇原文；被引长文和主长文里嵌的推文也翻；每次没用上译文都经 `taskrun.Degrade`（`ClassTranslate`）记一行带原因标签的日志。实现入口 `internal/linkexpand/templated.go` 的 `autoForward`/`directTextBytes`、`linktranslate/translate.go`、`linkplugin/expand.go` 的 `startTranslate`/`takeTranslations`。

## Go 设置注册表（S1、S2，2026-11-08，分支）

[`settingsreg`](../../native/internal/settingsreg/registry.go) 登记配置、桌面习惯、角色卡与尚不可用的规划项，提供中文名称、别名、分类、默认值、存储位置和生效方式。注册表每个键一条记录（`settingsreg/records.go`，加新键只动这一处）：键、名字、生效方式、分类、可写与否、范围、可选值、说明、别名、设置页文案（`ui`）和“后台谁读它”（`readers`，写成 `包路径:函数#选择器`，`readers_test` 核对函数存在、函数体里真的出现这个字段；尚未生效的键必须为空，其余不许为空）。分类、生效方式、类型、存储在记录里是英文标识，Entry 里出去的仍是中文显示串。说明没人写过的键留空，清单在 `testdata/undescribed.txt`，只许缩短；`testdata/entries.golden.json` 钉住每个键除说明、别名外的全部字段。生效方式是唯一的出处，每条记录必须写明，没有默认兜底（少写测试就红），共五种：立即、下一轮对话、重启接收（读取处在 App 启动时取一次）、重启程序（宿主启动时取一次，如 `dataDir`、`watchdog.*`、`qq.accounts` 的号码）、**尚未生效**（后台没有任何功能读它，写了不会变：登记项自动只读、不出可编辑控件、说明里不写“重启后生效”；判定看 config 字段在 config 包之外有没有被读取，只在默认值、校验或只读展示里出现的不算）。设置页的投影（`desktopread/settings.go`）的“已启用”由 `settingsreg.Lookup` 的生效方式推出，不再手写；Agent 设置工具白名单和自知投影的暴露名单只是“哪些键可改 / 可看”，生不生效同样引用它，`app/settings_truth_test.go` 核对四处不矛盾。`Read` 投影当前配置和显式键来源；秘密只交出有无状态，URL 不交出凭据、查询参数或片段。`trace.debug` 已退役，Go 不读、不生效。动态角色、账号、会话以模板登记，再返回具体实例。

| 桌面可选接口 `api.SettingsRegistry` | 行为 |
|---|---|
| `ReadSettingsRegistry(context.Context) (api.SettingsView, error)` | 一次读取全部项目；版本包含 `Config` 与 `Desktop` 两个文件的内容哈希；旧 `Reader.ReadSettings` 保持原契约 |
| `WriteSetting(context.Context, api.SettingChange, api.SettingsVersion) (api.SettingsView, error)` | 单个键和完整 JSON 值；先校验登记和类型，再按存储路由；返回重读快照及生效提示 |

实现位于 [`app/settings_registry.go`](../../native/internal/app/settings_registry.go)。旧模型、链接 writer 继续作为校验和运行发布的入口；注册表不绕过模型列表、角色和本机会话目录限制。新增平台／会话配置通过 `config.SaveSetting` 保存，下一次接收启动才加载。账号整表提交只接受 `id/character/url`，按 ID 保留原令牌；不能用读出的令牌状态直接回写。配置版本冲突返回当前快照，不覆盖别人刚写的值。

所有模型、链接及通用 yml 写入共用备份、保留注释补丁、原子替换和落盘重读核对；核对失败回滚原文，回滚失败单独报告。桌面 `Store` 的旧入口与注册表入口也共用备份及核对，不同 Store 写入串行，备份在桌面文件旁的 `settings-history/`；`SetSetting` 另检查桌面版本。持久化成功后完整快照读取失败用 `api.SavedSettingsError`，旧模型的部分应用失败仍为 `api.SavedModelSettingsError`，调用方不得把这些情况显示成“未保存”。

回滚前核对文件身份；配置路径还会在准备好还原文件后再次确认身份及内容。外部编辑器已原子替换的文件保持原样并报告冲突。未使用跨进程文件锁：外部原地写入与磁盘损坏不能完全区分，最终核对与替换之间仍有竞争窗口；不承诺对任意外部编辑器的原子比较交换。

新接口仅存在于可信桌面的 `DesktopReader`，不挂到模型工具、QQ 或控制管道。S3、S4 负责设置页控件及保存后应用桌面外观；尚未实现的系统登录项设置页入口、关窗选择、新旧界面切换等明确只读，不生成无消费者的开关。验收入口为 `settingsreg/registry_test.go`、`config/setting_file_test.go`、`desksettings/settings_test.go`、`app/settings_registry_test.go`；全部使用临时目录。

下面各节为 Bun 历史参考。

## 职责与边界

一个 Bun 进程（daemon/main.ts（私有存档））按固定顺序手动挂 Cordis 插件（ADR-020，不用 plugin-loader），2026-10-18 核对是 26 个：

server → lifecycle → secrets → store → chain → llm → persona → embed → memory → expression → todo → gate → vision → tools → agent → topics → backup → console → settings → maintenance → qq-local → onebot → qq → router → qq-health → qq-console

下表只列运行时这几个；embed、memory 见[记忆与嵌入](memory.md)，expression 见表达学习（私有存档），tools 见[工具](tools.md)，agent、topics、console、router、settings 见[回合与主窗口](agent-and-console.md)，onebot 见协议端适配器（私有存档），qq 一家（qq、qq-local、qq-health、qq-console）见 [QQ 渠道](qq.md)。

| 插件 | 服务名 | 源码 | 负责 |
|---|---|---|---|
| server | `ctx.server` | `@cordisjs/plugin-server` | HTTP + WebSocket，只监听 `127.0.0.1` |
| store | `ctx.store` | store.ts（私有存档） | `events.db`（原始事件、observations、blob 索引）、`trace.db`（模型调用、每一步思考和工具、按天用量、维护记录、她发到平台上的话里客服腔这类的记数 `outbound_hits`，真发的、试运行的分开，ADR-110）、`blobs/`（按内容哈希存的媒体和长输出） |
| chain | `ctx.chain` | chain.ts（私有存档）、util/chain.ts（私有存档） | 对话链简化版（规格-群聊闸门第二节）：每条平台消息在接哪句话（引用、@、拆条、弱连接），撤回、撤回重发、回错了、更正、@ 错人、复读；连接存 `events.db` 的 `event_links`，每个会话算到哪记在 `chain_state`。收到消息时接着算，查询（`state`、`replyTo`）时先补算；启动 3 秒后把还没算过的会话补上 |
| todo | 没有服务 | todo.ts（私有存档）、tools/todo-tool.ts（私有存档） | 计划清单（规格-计划清单）：工具 `todo_write` 由 tools 给（只给主人 × 主窗口），清单存在 `trace.db` 的 `steps`（最后一次成功调用的参数，不另加表）；这个插件挂在第 3 站 `turn/context`：对话实例重建后的第一轮，这个会话最新的清单还有没做完的，就追加一次提醒；另有 `GET /api/todo?conversation=`（只答主窗口的会话）给计划条用：主窗口一次只读最近 200 条，最新的清单在更早的消息里时前端问这里（`check:ui -- plan-bar` 守着） |
| gate | `ctx.gate` | gate.ts（私有存档）、util/gate.ts（私有存档） | 群聊闸门（规格-群聊闸门第三～五节）：每条群消息判断值不值得让她想一想（被叫到、过门槛、没接、退避、频率 0、合进一串 @），每次存一行 `trace.db` 的 `gate_decisions`（30 天，维护里清）；窗口状态存 `gate_state`，重启接着算；频率 `group.frequency` 默认 0.3，某个群 / 时段 / 临时的存 `gate_rules`。每个群的档位（只记录 / 试运行 / 说话，默认 `group.mode` = 试运行；"只记录"旧名"不说"，ADR-095）存 `gate_modes`，只记录的群她不进来看，消息照存、照样整理进记忆和学说法。放行时发 `gate/enter`，带上这一批消息（最多最近 50 条，`util/gate.ts` 的 `MAX_BATCH`，ADR-109 第 4 条：只取这么多条算特征、交给她，更早的只数个数放进 `earlier` 和判断理由，数到一万就停（`EARLIER_CAP`，多了说"一万条以上"；全量数 10 万条要 50 ms，封顶后 2～3 ms）；她自己的、插件自动发的、补录的在 SQL 里筛，撤回只问取到的这些。2026-10-19 量：攒了 3000 条时一条新消息 362～444 ms → 17～21 ms），路由接着让她看一轮（试运行的话不发，规格-群聊闸门 10.4 第 4 步）；进了回合以后的结果记在 `gate_decisions.outcome`。可能性的权重借自 MaiBot（docs/archive/notes/MaiBot-动态频率.md） |
| vision | `ctx.vision` | vision.ts（私有存档）、util/vision-prep.ts（私有存档）、util/vision.ts（私有存档）、[prompts/vision.md](../../prompts/vision.md) | 识图（规格-识图）：平台收到图（`message/stored`，哪个平台都行，补录、陌生人、自检的不看；转发里的图问收到它的那个平台的 `forwardImages`，ADR-141）就排进队列（p-queue，并发 `vision.concurrency`，默认 2；补看和合并转发树里其余的图走另一条慢队列，道数 `backfillLanes`，默认 2，两条各算各的；现场又发来慢队列里排着的同一张，就标成已开始、重新排进普通队列，慢队列轮到它时直接过去——`setPriority` 只管同一个队列里的，跨不了队列，实测见 `test/p-queue-priority.test.ts`），预处理（静态图长边 1024、长图切段、动图去重复帧拼最多九格）后用 `vision.model`（默认 Luna）经后台调用看一遍，宽松解析成固定格式（ADR-137 起提示词只要 `category`："表情包 / 图片"、`feeling`：第一感觉、`brief`：描述；旧版描述的其余字段照样认）；按"哈希 + 模型 + 提示词版本 + 预处理版本 + 注解"存 `events.db` 的 `media_descriptions`，同一版只看一次，失败的一小时内不重试。主人的注解存 `media_notes`（改了留旧版），有注解时作为"以这个为准"的资料一起发。`describedAs(hash)` 取现在这一版，`wait(hashes, ms)` 等几张看完。补看（主人点"把没看过的都看一遍"，`backlog` / `startBackfill` / `stopBackfill`）走单独的慢队列（`vision.backfillLanes` 道，默认 2），新来的图有自己的道数、不用等它。配置 `vision.direct`（默认开）、`vision.directMax`（默认 4）：私聊里一条消息的图不多、模型又能看图时，直接给她看真图、不等识图描述（规格-真图直给、ADR-138）；关了就等描述。 |
| llm | `ctx.llm` | llm.ts（私有存档） | 模型来源（`ModelSource`，ADR-142）：每个来源是一个 OpenAI 兼容端点，启动时各拉一次 `/models`，合成 `catalog`（每项带 `source`）；现在只有 Magpie 一家（`magpieSource`，不给 `sources` 时的默认），一个来源连不上不影响别的，`catalogError` 写明是哪个；`get(id)` 按名字取模型（同名取排在前面的来源），不在任何列表里返回 `undefined`；`apiKeyFor(服务商)` 给 pi 的 `getApiKey`；`sourceName(id)` 给报错用；`background(model, 提示词, { user })` 是后台岗位的一次调用（低档思考、打转或超时换一次全新的），`user` 不给就是"按要求输出 JSON。"，识图在这里放说明和图。每次调用（含识图）都记进 `model_runs`（util/background.ts（私有存档） 的 `callBackgroundReply`，`stage` 是提示词名，识图是 `vision`），调用本身出错也记一行，`error` 写原因。返回前把句尾多吐的"】【"清掉（util/clean-text.ts（私有存档），免得写进记忆），返回里带删了几处，说明见[回合与主窗口](agent-and-console.md)"状态"一节。**提示词缓存**：`streamFn`（主对话和后台都用它）套了 `withLongCache`，默认 `cacheRetention: 'long'`——pi 对本地网关只有这样才发 `prompt_cache_key`（取 `sessionId`）和 24 小时保留；键用 `cacheKeyOf` 换成哈希（会话编号里有群号、QQ 号，不往上游带）。后台岗位没有会话编号，`callBackgroundReply` 默认按"岗位 + 角色"给 `cacheKey`。2026-10-19 实测：不带键时上游只缓存网关自己那段约 3.3k 的固定前缀，带上以后同样开头的第二次读到 98% |
| maintenance | `ctx.maintenance` | maintenance.ts（私有存档）、maintain.ts（私有存档） | 每天挑没在聊天时清理到期数据、整理各个库、量数据大小；`GET /api/health/data`、`POST /api/maintenance/run` |
| backup | `ctx.backup` | backup.ts（私有存档）、util/backup.ts（私有存档） | 每天自动备份一份（库 VACUUM INTO、角色卡、配置、草稿、回放样本、表情包，媒体和上一份共用硬链接）、有风险的改动前只备份要改的库、轮换（7 天 + 4 周，改动前的 30 天，手动的不删）；放在 `backup.dir`，不写就是数据目录旁边的 `Sumika-备份/`（规格-数据目录与备份 5.3.0、ADR-069）；恢复（只换一个库或整份换上，ADR-070）：先做"恢复前"备份、把要换上的准备好、写 `restore-pending.json`，再让 lifecycle 重启，真正换文件在 main.ts 启动最前面（`util/restore.ts` 的 `applyPending`）；导出（ADR-072，`util/export.ts`）：做一份备份、按勾的去掉回放样本 / 成人向草稿、密钥换成去掉本机那份的副本，用内置的 zip.js 打成 zip（边读边写、媒体只装不压、zip64，ADR-078；以前 tar.exe 打的包照样能恢复）；解包是 util/zip.ts（私有存档） 的 `unzipTo`：一个个文件流式写出、不整个读进内存，包是外面拿来的，路径跳出解包文件夹（`..`、绝对路径、盘符）的一律拒绝；从导出包恢复（`unzipExport` → `restore.ts`）和 `scripts/fetch-tools.ts`（Windows 上的 rg / fd 是 zip，别的系统的 tar.gz 仍用 tar）、`fetch-fonts.ts` 下载后解压都走它，不起外部程序，导入这个文件时顺带把 zip.js 设成不开 Web Worker；接口见回合与主窗口的接口表 |
| secrets | `ctx.secrets` | plugins/secrets.ts（私有存档）、util/secrets.ts（私有存档）、util/dpapi.ts（私有存档） | 加密的密钥 `config/secrets.bin`（ADR-071、规格 5.3.2）：信封加密，数据密钥包三份（本机 DPAPI、主密码、恢复码）。main.ts 启动时打开（本机解不开就是"锁着"），把配置里的明文 QQ 令牌挪进来；QQ 号的令牌配置里没写就从这里取（`qq.<号>.token`），锁着时那个号不连、写明原因。接口只给名字和"有没有" |
| lifecycle | `ctx.lifecycle` | lifecycle.ts（私有存档） | 后台自己重启（ADR-070）：main.ts 给的 `restartSelf` 拉起一个一样的新进程（带 `SUMIKA_WAIT_PID`，新进程先等旧进程退干净）再退出；被开发期的看门脚本管着时（scripts/supervise.ts（私有存档），ADR-109 第 5 条，临时）不自己拉，用退出码 75 退出、由看门脚本马上拉起，免得两边各拉起一个；`POST /api/restart`。后台的退出码在 util/exit-codes.ts（私有存档）：0 正常、69 端口被占、75 要重启、78 配置写错（看门脚本对 0、69、78 不重启），没接住的异常照旧是 1 |
| persona | `ctx.persona` | persona.ts（私有存档） | 读数据目录的 `characters/<id>/card/character.yml` + `character.md`（没有 `card/` 就读 `<id>/` 下的，项目里的示例卡和测试用这种），只监听卡文件的改动（角色文件夹里的记忆库在写，不算；300 ms 去抖），发 `persona/changed` |

## 事实来源

- 配置：项目根目录 `sumika.yml`（config.ts（私有存档） 用 zod 校验，出错拒绝启动）。没有这个文件就全用默认值，示例见 `sumika.example.yml`。
- 环境变量**只给测试和预览用**：`SUMIKA_CONFIG` 换一份配置文件，`SUMIKA_DATA_DIR`、`SUMIKA_PORT` 覆盖数据目录和端口，`SUMIKA_EMBED=off` 关掉嵌入子进程。`SUMIKA_SUPERVISED=1` 是看门脚本给后台设的（后台启动时读完就删掉），不要手写。
- 角色：数据目录 `characters/<id>/card/` 下的文件是唯一来源（ADR-065 第 4 条）；用哪个模型不在卡里，在配置 `characters.<角色>`，主窗口换模型只写配置、不动卡（`Persona.setModel`，ADR-084；卡里以前写的启动时搬过去一次）。同一处还有按角色的开关 `characters.<角色>.groupReminder`（默认关，群聊每轮这一批后面追加提醒，ADR-121；和模型有关所以放这里，不放卡；`setModel` 只改 model / topic 那一键，不会动它）。卡的原文变了，上一版存进 `card/history/<时间>-character.md|yml`。一个角色都没有时把项目的 `characters/example/` 复制成 `example/card/`。
- 角色 ID：文件夹名就是 ID，建好以后不随手改；要改用 `scripts/rename-character.ts`（ADR-085，character-id.ts（私有存档）），改过的记在 `config/character-renames.json`，旧 ID 统一由 `Persona.resolve` 认成新的，恢复旧备份以后按记录再改一遍（`restore.ts`）。
- 配置：`SUMIKA_CONFIG` → 数据目录的 `config/sumika.yml` → 项目根目录的 `sumika.yml`（`config.ts` 的 `configFile`）。2026-10-17 已搬进数据目录，项目根目录那份改名成 `sumika.yml.已搬到data-config` 留着。
- 提示词：`prompts/*.md`，每次组装时重新读文件（prompts.ts（私有存档））。

## 约束

- 所有 SQLite 库：WAL、`journal_size_limit` 4 MB、`auto_vacuum=INCREMENTAL`（老库第一次打开时整理一次）、`busy_timeout=5000`、外键开启、`strict` 绑定（参数名不带 `$`）。写入用同步事务，单进程里天然串行。
- 存什么、存多久见 [规格-数据与日志.md](../规格-数据与日志.md)（ADR-026）：长内容一律过 clip.ts（私有存档）；媒体和大段输出用 `putBlob` 按内容哈希存、记引用；完整请求默认不存（`trace.debug` 打开才存）；日志写 `data/logs/`，先打码（logfile.ts（私有存档））。
- Go 桌面的日志与宿主状态（`Reason`、`NoRecv`）共用 [`app.RedactLog`](../../native/internal/app/log.go)，先遮盖已知令牌、会话号、长数字和用户目录前缀再写文件或发布给界面、本机控制接口；脱敏配置不可用时隐藏详情，`filelog` 仅落盘、不读密钥。
- `character.yml` 改坏时保留上一次能用的版本，并把错误挂在角色上（主窗口显示红色感叹号，输入框禁用并给出原因）。
- 模型不在任何来源的列表里时不自动换模型，只报错（报错写来源的名字，`sourceName`）。
- 通用小问题用现成的或共用的一份，别在各处再写（ADR-120、ADR-122）：时间戳（`stamp`、`dayKey`、`dayTime`、`lineTime`，本地时间，不用 `toISOString`）在 time.ts（私有存档）；字节数统一写 "1.5 MB" 的 `formatBytes` 在 bytes.ts（私有存档）；`median` 在 stats.ts（私有存档）；递归列文件用 `readdirSync(dir, { recursive: true })`（backup.ts（私有存档） 的 `walk`）；后台里排队、限并发、等手上的活做完一律用 p-queue（ADR-122），每个 `add` 自己接住错误、关插件时 `clear()`、等活用 `onIdle()`：缩略图 thumb.ts（私有存档）（并发 2）、脚本的 pool.ts（私有存档）、qq 的事件队列和备份 / 导出的队列和常驻 pwsh 的分析器（并发 1）、话题和表达学习的后台活登记 track.ts（私有存档）（不限并发）、说法的补判（并发 `LANG.reviewParallel`）；ID 用 ulid.ts（私有存档）（`ulid` 库的 `monotonicFactory`，同一毫秒里的几个 id 也递增）；从模型回复里取 JSON 一律走 json-extract.ts（私有存档） 的 `extractJson`（先找第一个 `{` / `[`，原样解析不了才交给 `jsonrepair` 修截断、尾逗号、围栏；被截断时半截的最后一项整项丢掉；修出来的要过调用方的 `accept`，免得把乱七八糟的话修成"空结果"），各处照旧再交给自己的 zod。

## Go 链接解析缓存（2026-10-30，独立切片）

`native/internal/linkreader` 的 `Service` 共用 `Fetcher` 和 `store.Stores`。消费者是 `native/internal/linkplugin`：只在 receive 模式由 `app/channel.go` 装进 Hub（观察者取链接、回合附摘要、`read_link_detail` 工具），见[链接解析规格第十三节第 4 步进度](../规格-链接解析.md)。接口、配置结构和生命周期见[链接解析规格第七节](../规格-链接解析.md#七缓存参数与生命周期)，不沿用本页上方已退役的 Bun 插件装配。

存储入口在 [store/links.go](../../native/internal/store/links.go)：`LinkResult` / `PutLinkResult`、`LinkAlias` / `PutLinkAlias` 使用共享 trace；`RecordLink` / `MessageLink` 使用会话所属平台库。新增迁移为 trace/0006、chat/0004 与 chat/0005（`link_snapshots`，同一份结果只存一次，关联只记内容哈希），结构见[分库规格第三节](../规格-聊天记录按平台分库.md#三表放哪)。`RecordLink` 的事务只访问一个平台库，重放不改变首次的历史次数，成功详情只补空、不覆盖旧快照。

复用结果不授予读取消息详情的权限：`read_link_detail` 仍按角色与会话核验可见性，别的会话、别的角色没看到的消息一律查不到。本地私聊、本地群聊、QQ 私聊、QQ 群各用原有会话 ID，昵称、群名、作者显示名不进入关联键。HTTP 预算和冷却只在进程内，`Close` 先于关库；本步不缓存媒体文件。

## Go bot 配置（2026-11-03，feat/bot-wiring）

配置定义在 [config.go](../../native/internal/config/config.go)、校验在 [bot.go](../../native/internal/config/bot.go)，示例见 [sumika.example.yml](../../sumika.example.yml)。`plugins.sessions` 是完整会话 ID → `repeater/poke` 布尔覆盖；`characters.<角色>.aliases` 是人工叫名表；`reply.deny` 是 `平台:号` → `ai/bot/blocked`。三项默认均空，不预置真实群、账号和角色别名；坏值由启动配置读取报错。Bun 后台（ADR-345）在同一个 `plugins` 段里另有 `plugins.<id>.enabled` 与 `plugins.<id>.settings`（`<id>` 是 `server/plugins/` 下的插件目录名，不能叫 `sessions`）；Go 后台不读这些键，也不会因它们报错（`yaml.Unmarshal` 不是严格模式，2026-11-11 实测），读法见 [bun-server](bun-server.md)。内置插件 `life-tools`（日历 / 天气 / 汇率，默认开）的 `settings` 键名照 Go 的同名配置：`location`（name、city、region、country、countryCode、latitude、longitude、cityLatitude、cityLongitude、timezone）、`calendar.holidayUrl`、`freeApi`（networkRegion、probe、limits.weather / exchangeRate / calendar、providers），另加 `endpoints`（提供方 id → 自建镜像地址）；Bun 后台读不到顶层的 `location` 和 `freeApi`，要写在 `plugins.life-tools.settings` 下；`repeat`（复读判定，默认关，没有设置）；`link-reader`（链接读取，默认开）的 `settings` 键：`context.enabled`（同 Go 的 `linkReader.context.enabled`）、`maxLinks`、`workers`、`queue`、`jobTimeoutSeconds`、`waitSeconds`、`ttlHours`、`requestTimeoutSeconds`、`recentNotes`、`noteAgeHours`（后面这些 Go 写在代码里，Bun 做成了设置，默认值照 Go）；它要的链接表由 Go 的迁移建（缺表时只有它装不上）。示例见 [sumika.example.yml](../../sumika.example.yml)。

[app/channel.go](../../native/internal/app/channel.go) 把同一份开关、别名和名单交给链、流水线、闸门和插件。改配置后重启接收；桌面只读设置显示配置和接收进程加载时的值（[desktop.go](../../native/internal/app/desktop.go)、[bot_settings.go](../../native/internal/desktopread/bot_settings.go)），会话号码脱敏、名单只数分类数量。规则和四种场合边界见 [渠道中间件第七节](../规格-渠道中间件.md#七拦截链处理掉就停)。

## Go 生态来源（2026-11-04，feat/kernel-k4，未合 main）

正式宿主由 [App.openKernelEcosystem](../../native/internal/app/kernel_wiring.go)调用 [OpenEcosystem / EcosystemPorts](../../native/internal/app/kernel_ecosystem.go)，完整 YAML 由 [ParseEcosystem](../../native/internal/config/kernel_ecosystem.go)组合读取。来源仍默认关闭；开启的 MCP 只在启动阶段装载、连接，不在 OnContext 联网。权限句柄由 [ecosystem.Authority](../../native/internal/ecosystem/access.go)按可信本地私聊 Scope 签发；本地群聊、QQ 私聊、QQ 群不取得这些新来源能力。已有授权撤销后不自动重签，普通聊天去掉失效来源身份继续受理。模型参数和来源文字不能补身份。联网三个工具（`web_search`、`web_fetch`、`wiki_lookup`）见[规格-网络搜索和抓网页](../规格-网络搜索和抓网页.md)第八节：搜索和抓网页各家的先后是配置 `web.searchOrder`、`web.fetchOrder`，`Ecosystem.WebOrder` 持有运行中的顺序，设置页拖动后 `DesktopReader.WriteSetting` 落盘并当场改它（`App.applyWebOrder`），不用重启；抓网页 auto 是 `web.FetchChain`，前一家失败换下一家。

| 模块 | 程序入口 | 固定工具 | 持久材料 |
|---|---|---|---|
| skill | `skills.Catalog` / `skills.Plugin` | `skill` | 只读受约束来源；元数据先审批，正文按需；预留由接纳/丢弃恰一次结算 |
| MCP | `mcpclient.Client` / `mcpplugin.Plugin.Prepare` | `mcp_search`、`mcp_call`、`mcp_result_read` | SDK 协商；冻结 binding/schema/参数后审批；全文只走 `kernel.ResultStore` |
| web | `web.Service` / `web.Plugin` | `web_search`、`web_fetch`、`wiki_lookup`（ADR-348：多家服务按顺序自动选，额度看服务商） | 同 Root 共享额度、授权域内缓存；外部消费先记 trace，再出网 |
| child | `subagent.Manager` / `turn.Hub.StartInternal` | `task`、`task_result`（可选 `wait_seconds` 最多等 300 秒）、`task_list`、`task_message`、`task_cancel` | 同一 Hub 的 runTurn、调度器、模型账本；只带任务与委托能力，继承父工作区；最终原文用独立 task-result 步骤托管，task 与原域 mailbox 同事务，持久接纳后确认 |

内部任务复用同一个模型循环并保留 `Said`，由 `StartInternal` 将最后一段正文交给任务结果端口；它不通过写公开聊天事件来取得 `Reply`。任务原文仍按父作用域托管，不能把 child 的内部会话当作角色公开发言。

trace 迁移 [0010_kernel_tasks.sql](../../native/internal/store/migrations/trace/0010_kernel_tasks.sql)创建 `kernel_tasks`、`kernel_task_mailbox`、`kernel_mcp_calls`、`kernel_web_quota`、`kernel_web_charges`、`kernel_web_usage`；[EcosystemStore](../../native/internal/store/kernel_tasks.go)按 owner/Root/ToolID 去重，不读取旧 Bun 数据。MCP unknown 按逻辑服务隔离，不靠重连清除；解除只有 `sumika-ctl mcp release`（原因必填、要 `--accept-unknown-risk`，旧行改 `risk_accepted`，`reason` 列的 JSON 备注记传输种类、进程、时间、原因和解除人，见 [mcp_calls.go](../../native/internal/store/mcp_calls.go)）；启动时 `MCPRecoverAfterRestart` 给上一个进程遗留的已派发收尾（本机服务记 `terminated`，远端和旧行改 `unknown` 继续挡）；本机服务断开后 [recovery.go](../../native/internal/mcpclient/recovery.go) 按 0.5～8 秒退避最多重连 5 次，被隔离的不重连；自检 `mcp.quarantined` 和提醒 `mcp:<服务>` 读同一份状态（[mcp_quarantine.go](../../native/internal/app/mcp_quarantine.go)）；web 不确知用量保守保留扣额。关闭顺序为停止插件、child/web 取消和排空、MCP session/进程树、skill 句柄，最后由宿主关闭库。

插件参考、压缩前回调和通知与工具共用 Lifetime：超时只结束等待，实际回调退出才结束活动；通知在排队时登记，取消时释放未开始项，空闲订阅不产生工作活动。Hub 封入口后取消回调、等真实退出；Drain 超时保留库与数据锁。通知活动只带事件的角色/会话信息，不补来源授权，也不按“最近回合”推断模型预算根。

`skills.enabled`、`web.enabled`、`subagents.enabled` 默认 false；MCP 来源 `sumika.enabled` 默认 false。skill 默认索引 8192 B、20 条/页、正文 32768 B、文件 1 MiB、上下文 128 KiB、扫描 5000 ms；web 默认搜索 `auto`（依次选有 key 的 Tavily、Exa、Brave、Firecrawl，配了地址的 SearXNG，都没有用免 key 的 DuckDuckGo）、读取 `http`（本机抓，免 key；可选 Tavily、Firecrawl）、30000 ms、Asia/Shanghai；各家 key 用 `keyRef`（Tavily）、`exaKeyRef`、`braveKeyRef`、`firecrawlKeyRef` 引用，SearXNG 地址 `web.searxng.url`。web 的 `dailySearch`、`dailyFetch`、`monthlyMicroCredits` 是可选的本地额度：不写表示不限，写了照办，0 表示拒绝消费（2026-11-10 起，原先“启用时必须显式提供”已取消）；搜索服务能查到余额时按服务商真实余额跳过用完的家；子任务启用必须给 `model`。示例见 [sumika.example.yml](../../sumika.example.yml)。

web 的资源限额默认 5 MiB 响应、全局并行 4、每任务 2、每任务查询 4/抓取 8/派发 12、缓存 300/600 秒及 16 MiB/256 项；全局并行工程范围 1～8，其余资源参数只能收紧。child 默认深度 1、每 Root 4/全局 8/每认证派活人 2，research/local 120000/900000 ms、32/64 模型步骤，创建/结果工具均 5000 ms。所有数值在原始 YAML 边界拒绝小数、负数和越界，额度的显式 0 保持禁止消费。创建用短期限，child 运行始终绑定父 Run 寿命；请求者容量不因换会话或重签来源句柄重置。


## 存储统计与清理规则核对（运行页第 2 步，2026-11-08，feat/runstats-storage）

规格：[规格-运行页](../规格-运行页.md) 第五节。代码：`native/internal/storagestat`（类别表、遍历、快照、服务）、`internal/mediastore/media_observer.go`（新增字节回调）、`native/storage.go`（桌面外壳持有服务）、`internal/ctlproto/storage.go`（结构体）、`sumika-ctl stats storage`。这一步**只统计，不清理任何东西**；清理是第 6 步。

### 怎么量

- 类别表在 `storagestat/category.go`，路径全部来自 `internal/paths`（新增 `GoMediaDir`、`GoThumbsDir`、`GoImportDir`、`GoCharactersDir`、`SettingsHistoryDir`、`ConfigHistoryDir`、`BackupsDir`）。`GoMediaDir`（`go/media`）和 `MediaDir`（Bun 的 `shared/media`，旧版媒体）不是一处。
- 每个类别认领若干根（文件或目录）；数据目录里没被任何类别认领的文件归"其他"，不让文件悄悄消失（真实数据里有根下的 `import/`、`shared/` 里的 Bun 库、`cache/` 里不是缩略图的、`go/` 下的锁文件和小 JSON）。认领的路径整个跳过，所以每个文件只算一次；测试断言"各类之和等于磁盘上实际总数"。
- 小类别（聊天库、记忆库、共用记忆库、日志、配置、角色卡、备份、运行记录、工作结果、报告、旧版数据）打开页面时现量，最多花 1.5 秒，超了退回快照的数字；大类别（媒体、旧版媒体、缩略图、导入、开发数据、本地模型）和"其他"只用后台统计的快照，没统计过就是"还没有统计"，**不写 0 当数**。
- 后台统计：`Service.Rescan` 起一个协程，锁线程并降到 Windows 后台优先级（`THREAD_MODE_BACKGROUND_BEGIN`，CPU 和磁盘都让路），每 1000 项检查一次取消并让出；结果走一条 `INSERT` 写进 `maintenance_runs`，取消在写之前检查，已经开始写的让它写完，不会有半行。离开页面不取消，`CancelScan` 和 `Close` 才取消。
- 联接和符号链接不跟进去（计数写在类别的 `links`）；数据目录本身和配置里的外部根（备份目录、模型目录）用句柄取真实路径（Go 的 `EvalSymlinks` 不解联接，和 `mediastore` 的做法一致）。
- `maintenance_runs` 里有 Bun 时代留的 167 行，`sizes_json` 形状不同：Go 写的行带 `"format":2`，读的时候只认这种，其余当没有，一行都不动。`cleaned_json` 写 `{}`。
- 增量：`mediastore.OnNewFile` 登记观察者，`Put` 真正改名落下新文件时通知（同哈希已在的那条路不通知，失败不通知）；服务按"数据目录相同"过滤，按平台累计；`SubMedia` 给第 6 步清理扣减。退出（外壳退出流程第一步）和换数据目录时把"快照加增量"折成一行 `kind:"flush"` 写回，沿用原来的统计时间；从没统计过时不写。统计期间新落的文件可能重复计一次，下次重扫修正，所以页面写"约"。
- 服务放在外壳（`shell.storageService`）而不放进 `backend.App`：App 随接收模式开关重开，增量不能跟着丢。`sumika-ctl stats storage` 和界面读同一个服务，命令行不触发统计。

### 清理规则逐处核对

| 类别 | 实际规则 | 触发时机和阈值 | 位置 |
| --- | --- | --- | --- |
| 媒体写入临时文件 `go/media/<平台>/.tmp/` | `CleanTmp` 删修改时间早于截止的普通文件，不跟联接 | `Fetcher` 启动时一次，截止 = 现在往前 **1 小时**（`tmpMaxAge`）；只清这个 `Fetcher` 的平台 | `mediastore/media_write.go`、`mediafetch/fetcher.go:47、202` |
| 缩略图写到一半的 `.partial` | 写同名缩略图前，顺手删同前缀、超过 **24 小时**的 `.partial` | 只在再写这一张缩略图时；没有全局清扫 | `mediastore/thumbnail.go` `writeThumb` |
| 视频临时副本 `.tmp/` 下的专用目录 | `CleanVideoTmp` 删早于截止的普通文件 | 后台 `Open` 时一次（Archive 模式也跑），截止 = 24 小时前；平台**写死 `qq`**，`local` 不在内 | `mediastore/video_open.go`、`app/app.go:263` |
| 链接展开临时目录 | `MediaManager.Sweep` 删根下不是本实例、不是符号链接、修改时间早于 **1 小时**的子目录 | 装配链接插件时一次（启动或重载链接设置） | `linkexpand/media.go`、`app/channel.go:49` |
| 工作结果 `go/work/results/` | `artifact.Sweep`：预留超过 24 小时回收；无引用的结果超过配置 `agent.work.unreferencedDays`（1 到 3650 天）删；目录里不在库中、不在用的有效 ID 或 `.collect-` 文件超过 24 小时删 | `turn/work.go:51`，Hub 第一次初始化工作结果存储时跑一次（每个进程一次）；没有定时器 | `artifact/retention.go` |
| 检查点 `go/work/checkpoints/` | 按保留时长（默认 30 天）、`MaxRecords`（默认 100）、`MaxStoreBytes`（默认 512 MiB）裁，被钉住的不删；裁完仍超限就拒绝新建（`ErrLimit`） | 新建检查点预留空间时（`reserve`），不是定时；`restore` 另外拒绝过期的 ready 检查点 | `checkpoint/store.go`、`types.go` |
| 记忆库备份 | **没有任何删除旧备份的代码**，只删自己写到一半的 `.partial`；相同指纹已有备份就跳过 | 开写入、开后台整理前各做一次 | `memory/backup.go`；目录 `backup.dir`，空用 `paths.BackupRoot` |
| 设置历史 `go/settings-history/` | **没有清理**：每次保存 `desktop-settings.json` 前把旧版复制一份进来 | — | `desksettings/settings.go` 约 128 行 |
| 配置历史 `config/history/` | **没有清理**：`SaveLink`、`SaveModel`、`SaveSetting` 每次保存留一份 | — | `app/linksettings.go`、`models.go`、`settings_registry.go` |
| 角色卡 `card/history/` | Go 侧不写（`persona` 只读卡）；已有的是 Bun 时代留下的，没有清理 | — | `persona/persona.go` |
| 媒体正文、聊天库、trace.db、日志、`dev/`、`import/`、`reports/`、`backups/`、`Sumika-备份/` | **没有任何清理规则**（日志每天一个文件，没有轮转和删旧） | — | — |

和规格第五节第 2 条的出入：

1. 规格写"超过 24 小时的 `.partial` 在下一次写同名文件前清"当作媒体写入临时文件的规则，实际是两件事：媒体库 `.tmp/` 的残留 **1 小时**、启动时清；缩略图的 `.partial` 才是 24 小时、写同名时清。
2. 规格说 `artifact.Sweep`"只由宿主显式维护触发"，实际在 Hub 初始化工作结果存储时自动跑一次，只是没有定时器。
3. 规格没写 `CleanVideoTmp` 只清 `qq` 平台、`local` 的视频副本没人清。
4. 规格把记忆库备份、设置历史写成"保留份数待核对"：核对结果是两者都没有保留策略，一份都不删。
5. `config/history/` 不在规格的表里，也没有清理。
6. ~~配置里的 `backup.daily`、`backup.dailyMedia`、`backup.alertGB` 在 Go 里只有设置页展示读取，没有任何执行方~~（2026-11-14 R-386 补上，`feat/daily-backup`，合并前仍是旧状态）：`native/internal/backup` 做每天自动备份、手动备份、校验、轮换和占用提醒（`alertGB`），`internal/app/backup.go` 装配，`sumika-ctl backup now|list`，自检 S9；规格见[规格-数据目录与备份第十节](../规格-数据目录与备份.md)。备份目录 `backup.dir`（空 = 数据目录旁边的 `Sumika-备份/`）里现在有四类东西：本程序的备份文件夹（有 `manifest.json`，`program: sumika-native-backup`）、记忆开写前的 `memory-*.db`、Bun 时代的旧备份文件夹、主人自己放的；备份轮换只动第一类。配置键：`backup.dir`、`daily`、`dailyMedia`、`alertGB`，加 R-386 补的三个——`minFreeGB`（默认 5，备份前目标盘至少剩多少，还要至少是预计大小的 2 倍；0 = 不查）、`runMaxHours`（默认 6，一份备份最多做多久）、`closeWaitSeconds`（默认 30，退出时最多等备份多久）。
7. 规格第五节第 1 条的类别表没有列根下的 `import/`、`shared/` 里的 Bun 库（旧版数据）、`go/goals/`、`cache/` 的非缩略图部分：存储页加了"导入数据包含 `import/`"、"旧版数据"（`shared/` 里的 Bun 库 `events.db`、`trace.db` 等，不含共用记忆库）、"共用记忆库"（`shared/owner-memory.db` 含 `-wal`、`-shm`，`paths.OwnerMemoryDB()`；它是 Go 的库、不是 Bun 遗留，原来被误算进旧版数据）、"工作结果含 `go/goals/`"和"其他"来兜住。

## 运行记录与失败原因（质量埋点）

- `model_runs.error_class`（迁移 0014）：模型请求失败的原因类别，词表和判断在 `internal/llm/classify.go`（`ClassifyError`、`ClassifyText`）。写入处：`modelcall.Stream`、回合的 `failedAttempt` 和 `endStep`、`compact.go`；旧行没有，读的时候用 `ClassifyText` 对 `error` 原文现算，不回写。
- `model_runs.prompt_hashes_json`（迁移 0021）：这一次真正发出去的 messages 逐条的角色、字节数和哈希链，加 tools 的字节数和哈希，不存正文（`llm.PromptHashes`，格式见函数注释）。在 `llm.Client.Stream` 按请求体原样的那份算，放进 `llm.Result.Prompt`；`modelcall.Stream` 和回合的 `runRow` 都写，upsert 时空的不覆盖已有的。比相邻两次请求用 `llm.PromptPrefix`：公共前缀到第几条、多少字节。`request_json` 照旧是 NULL。
- `task_runs`（迁移 0015）：模型调用以外的任务（发送、链接取数、媒体退化）的一次结局，列是 `at、module、outcome、class、ms、attempts、root_id、detail`。写入统一走 `internal/taskrun`：`Degrader.Degrade` 写一行"模块 · 类别"日志、按需提醒、记一行；`Finish`、`Span` 记成功的耗时。`internal/app` 装配一个 `Degrader`（日志、提醒、`store.TaskSink`、`RedactLog`），交给出口（`channel.OutletConfig.Degrade`）、链接展开（`linkexpand.Preparer.Degrade`）、链接插件（`linkplugin.Options.Degrade`）；没装配的地方是 nil，什么都不做。
- `detail` 写入前已脱敏、最多 500 字节（`taskrun.Detail`）：遮 URL、邮箱、会话名、令牌、本机路径、IP、长数字；错误原文就是模型输出的类别只留前缀。类别用各模块自己的字符串常量（`taskrun` 里放起步词表），页面只原样显示"模块 · 类别"。
- 读：`runstats.Service.Quality`（`sumika-ctl stats quality` 和界面共用），口径见[运行页规格](../规格-运行页.md)第十二节第 4 步那段。`task_runs` 保留 90 天的清理还没做（第 6 步）。

## 主动联系的导演（规格-主动联系第 1 步，2026-11-09，feat/proactive-thread）

- 包 `internal/proactive`：纯函数（`Decide`、`Interval`、`OnClose`、睡觉时段）加调度壳（`Director.Tick`、`Catchup`），写法照 `internal/loop`，不 import 回合内核；不调模型，到点只写日志"本该醒"，第 2 步经 `Options.Waker` 接模型。后台装配在 `internal/app/proactivewire.go`：`proactive.enabled` 且列了角色才建，关着不跑、不记日志；随 `Run` 起、`stopAuto` 停（和 /loop 一样，archive 模式不跑）。
- `proactive_state`（迁移 0017，trace.db）：每个角色一行 `character_id、factor、quiet_until、cold_streak、next_wake_at、last_wake_at、updated_at`。
- `proactive_decisions`（迁移 0017，trace.db）：每次醒、被拦、续醒、没醒到各一行 `at、character_id、kind（wake/followup/skip/close）、reason、inputs（只有数字和布尔的快照）、next_at、thread、model_note、count`；`count` 是同样的错误合并计数（前两次各一行，之后加在第二行上）。保留 `proactive.decisionLogDays`（默认 90）天，每天最多清一次。`inputs` 不含会话名、文字。
- 原因码：`due`、`due-asleep-active`（睡觉时段内但他在活跃，照常放行）、`snoozed`、`daily-cap`、`already-open`、`owner-chatting`、`asleep-no-activity`、`cold-streak`、`missed-offline`（到点时进程没开，启动时补一行，不补跑）、`error`；第 2 步起再加模型和发送的码。
- 主人活跃信号：每个平台库一句 `Store.OwnerLastSpoke(since)`（`actor_kind='owner'`、`kind='message'`、`created_at > since`，倒序取第一条，只看 `presenceWindowHours` 以内）；本地库那条同时当本地窗口最后输入；窗口前台来自 `app.Options.WindowForeground`（`native/shell.go` 接 `rm.fg`）。活跃迹象含窗口在前台。
- 配置键 `proactive.*`：`native/internal/config/proactive.go`（默认值、范围校验、睡觉时段 `HH:MM`），默认全关；示例在 `sumika.example.yml`，默认值以规格第七节配置表为准，`TestProactiveExampleMatchesDefaults` 守着三处一致。
- 读：`sumika-ctl proactive log`（只读打开 trace.db）。

## 主动联系的话题线（规格-主动联系第 2 步，2026-11-09，feat/proactive-thread）

- 线是一个有库的本地会话 `console:<角色>:topic:thread-<线号>`（`audience.IsThread`、`ThreadConversation`），不登记 `local_topics`，会话列表看不到。听众 `audience.Thread`：本地、私聊，`PrivateOK`、`OwnerAsked` 都为假，`ownerConsoleAudience` 不成立，所以没有执行工具、`/goal`、`/loop`、`remember`。
- 输入身份：`turn.ThreadInput` 预先绑定 `ActorKind=system`、`ActorID=proactive`、权限档 `thread`，不经 `trustedInput` 去推；`validateInput` 里 system 发言人只认线（会话串、发言人 id、权限档、听众四样都对上），线里也只认 system。`bindKernelInput` 对线直接返回，不签本地授权。`history.toMessage` 对线里 `proactive:wake`、`proactive:relay` 两种来源的 system 事件开例外，渲染成用户消息，其余 system 行照旧不进模型历史。`Hub.MarkStale(角色, 会话)` 标记下一轮重建实例，不打断在跑的回合。
- 表（迁移 0018，trace.db）：`proactive_threads`（线状态机的一行）、`proactive_sends`（先写后发：pending → sent / failed / unknown / thought，`plan_id` 线号加序号当幂等键）、`proactive_claims`（被线认领的主人消息，`(conversation, seq)` 主键，pending / replied / handed / lost）。
- 表（迁移 0019，trace.db）：`whisper_links`（悄悄话对话日志的关联：`(batch, character_id)` 主键，来源会话、悄悄话会话、根运行 `root_run_id`、处理记录事件 `note_seq`、状态、时间；索引 `(source_conversation, created_at, batch)`；只存关联，不存聊天内容，规格-群聊引用和路由 15.2）。
- `proactive.Apply` 是状态机纯函数（thinking / awaiting / chatting / closed × 十一种事件，`thread_test.go` 逐格写预期）；`CloseAt`、`FollowUpAt`、`NextCheck` 保证没关的线必有截止时间；`proactive.Threads` 在一把锁里读-改-写并管发送记录、认领记录、重启恢复（`Recover`）。
- 宿主 `internal/app/proactivethread.go`：`Wake` 开线或续醒并起一轮模型（后台协程，随 `autoCtx` 取消），`finishTurn` 收尾，`afterClose` 留结论事件、更新导演系数、写 close 日志，`sweep`（导演每次轮询的 `Housekeeping`）收到时的线和宽限到点的认领，`claimLocal` 在 `sendLocalParts` 进回合之前认领或旁听，`deliverLocal` 往 `console:<角色>` 写她说的话（origin `character`、segments 带 `proactive.thread`、`proactive.reason`）、`MarkStale`、`attend(AttentionProactive)`。工具在 `proactivetools.go`（声明在 `proactive.Decls`，`policy.Specs` 登记为 self / any，可见性由 `For` 定）：`owner_presence`、`thread_send`、`stay_quiet`、`end_thread`、`hand_back` 只在线里，`proactive_snooze` 另在主人本地私聊里。
- 导演的状态读改写在 `Director.stMu` 里：`ThreadClosed`（线关了更新系数并重排）、`SetQuiet`、`SetNextWake`，调度壳 `tickOne` 同锁，`Waker.Wake` 里不能调它们。
- 场合提示词 `prompts/scene-proactive-thread.md`，只写引导。
- 界面：本地私聊右栏多一个“联系”标签（`native/workbench_side_contact.go`，经 `api.ProactiveReader` 读 `proactiveStatus`：线状态、开口理由、去过哪、下次醒、最近 10 次导演决定，快照点开才看；头上“今天别找我”“现在别再找了”经 `api.ProactiveControl`，实现在 `internal/app/proactivestatus.go`）；看不见时不读，5 秒刷新一次。她主动发的话在 segments 里带 `proactive: {thread, reason}`，`desktopread` 只在本地平台、她自己说的话（actor character、origin character）上投成 `MessageView.Proactive`，气泡时间旁多一个“主动联系”小标记，悬停补一行她为什么找你。

## 资源采样与进程登记（运行页第 5 步，2026-11-09，feat/run-resources）

规格：[规格-运行页](../规格-运行页.md) 第四节和第十二节"第 5 步"那段。代码：`internal/procreg`（登记簿）、`internal/runmetrics`（队列注册表）、`internal/resstat`（采样器）、`internal/ctlproto/resources.go`（结构体）、`native/runpage_resources.go`（界面）、`sumika-ctl stats resources`。

- 起子进程的地方在 `Start` 成功后 `procreg.Register(类别, 标签, pid)`，退出时撤销：`embed/local.go`（本地模型）、`mcpclient/process.go`（MCP，标签只写服务 id）、`policy/analyzer.go`（常驻 pwsh）。命令和 ffmpeg 没登记，会以"未登记的子进程"出现。新起进程的地方要登记，不然只能靠兜底按父进程找。
- 队列在装配处 `runmetrics.Register(key, 正常等待时限, 读数函数)`，现在只有模型请求槽位（`app.registerModelSlots`，读 `modelcall.Client.Load`）。读数函数只在页面采样那一刻被调；panic 或出错只影响那一行。
- 采样器没有协程和定时器；`Sample` 只在有人要数据时跑一次，进程列表最多 5 秒重列一次，CPU 靠上一次采样的差。非 Windows 是空实现。
- 读性能计数器（PDH）要 400 多毫秒才能打开，所以页面第一次采样先不开，命令行当场开。

## 验证

```bash
bun run typecheck
```

冒烟检查：`SUMIKA_DATA_DIR=<临时目录> SUMIKA_PORT=7799 bun daemon/main.ts`，再请求 `GET /api/state`。
