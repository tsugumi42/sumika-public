# 工具与审批

开发规则见 [开发指导/权限安全隐私](../开发指导/权限安全隐私.md)、[开发指导/插件与扩展](../开发指导/插件与扩展.md)。

Go 共用身份在 `feat/kernel-int` 统一：`turn.ToolCall.Identity` 和 `PreparedCall.Invocation` 使用 [`kernel.Invocation`](../../native/internal/kernel/types.go)。`Store.StepStart` 在同一条 SQL 中保存宿主 ToolID、StepID、ProviderCallID 和 ordinal；provider ID 仅用于模型协议配对，不能作为步骤主键。steps 的 identity/provider/ordinal 由迁移 0007 所有，step_id/result_ref 由 0008 所有，0009 只增加模型账本字段。审批记录携带同一 Invocation。

## Go 编辑预览与执行

[`edittext.Parse / Apply`](../../native/internal/edittext/edit.go) 是编辑参数与正文计算的唯一实现；[`Gate.Describe`](../../native/internal/approval/approval.go) 的人工审批、自动批准记录与 [`tools.editLocked`](../../native/internal/tools/edit.go) 的实际写入都使用它。所有替换对着同一份原文定位，`newText` 的 `$` 标记按字面处理；空旧文本、找不到、多处匹配、重叠和无变化返回相同错误，预览显示原因，不伪造成功差分。当前工具没有“全部替换”参数。

共用计算处理模糊匹配、BOM 和换行恢复；未改的原始字节沿用执行器规则。审批差分来自最终将写入的正文，超过 6000 个 UTF-16 码元只截显示并标 `Truncated`，实际写入不截断。测试矩阵见 [`edit_preview_test.go`](../../native/internal/edittext/edit_preview_test.go)。这里保证同一文件内容与参数的计算一致；审批等待期间文件被外部修改的并发保护不在本次 A01 修复范围。


## Go 文件检查点（feat/checkpoint-lsp，未合 main）

[`internal/checkpoint`](../../native/internal/checkpoint/service.go) 在 `tools.checkpoints.enabled=true`（默认）时，为正式 write/edit 和 PowerShell 执行保存前后原始字节。捕获位于原审批与项目前置之后、实际执行之前；前态保存失败不执行，后态失败返回基础设施错误并停止后续工具。PowerShell 只保护冻结 CWD 中未排除的普通文件，不能撤销网络、数据库、工作区外写入或后台进程。

对象与索引保存在 `<data>/go/work/checkpoints/<ID>/`；不接触用户 Git。恢复使用完整 Scope、内容 CAS 和单次审批，YOLO/已有授权不能跳过；先持久保存逆检查点再逐文件恢复。步骤及审批复用 Invocation，完整结果进 ResultStore；下一轮收到检查点回执，恢复事实进入持久工作资料。范围、限额及当前验收状态见[检查点规格](../规格-检查点与回退.md)。

恢复发布由最多 32 个 worker 执行，每个只复用一个父目录句柄（[`parent.go`](../../native/internal/checkpoint/parent.go)）；实际文件和发布前的父目录仍逐次核对。Windows 源打开及目标改名均相对目录句柄，改名后刷新同一文件句柄；不按 Root.Name 拼绝对路径。每个目标继续同目录临时写入、Sync/Close、持久 rename；失败停派并等待所有实际 I/O 退出，保存真实 Done 和 inverse ID。

[`progress.go`](../../native/internal/checkpoint/progress.go) 将准备、发布和收尾阶段按精确 Scope 跟踪；`WatchProgress` 同步绑定活动操作或登记等待观察器，Restore 在同一状态锁下绑定新操作的匹配观察器，不靠轮询发现。`ProgressWatch.Run` 每 200ms 发布累计已恢复数/总数，`api.CheckpointActions.WatchCheckpointProgress` 将同源事件交给桌面。无活动操作先发 idle，历史结局不关闭订阅；绑定后只按该操作的 done 发送终态并结束，即使回调暂停或 Run 尚未启动，后续恢复也不能挤掉该终态。绑定、context 取消和 Run 返回均释放等待登记。ctl `checkpoint status/cancel` 不取变更门，长 restore 进行中仍可调用。取消等全部 I/O 和结局持久化退出后返回 Completed、Remaining 和逆 ID；冷查询从逆记录投影。对该逆 ID 显式 recovery 撤回已完成部分，resume 使用预先冻结目标续做，两者均重新 preview/version/单次审批。

进度的 OutcomeSaved 区分逐文件发布与最终结局保存，Error 保留实际操作错误；桌面 API 和 ctl JSON/text 均公开。结局保存失败时当前返回 interrupted、实际 Completed 和逆 ID，OutcomeSaved=false；Cancel 等收尾后返回同一结局。冷开从 pending 显示 interrupted，未落盘 Done 不假装已确认，显式 recovery/resume 重新扫描。元数据发布成功后才切换返回记录的新包索引；旧包回收失败保持 ready/OutcomeSaved=true 并暴露错误。

只读快照恢复先用可写自有临时文件完成写入和持久发布，再由同一发布句柄设置并刷新只读属性，之后才能登记成功；属性失败保留 partial，不修改已有用户目标的权限。

轮换只处理未 pin 的 ready：先持久摘除 record.json 为 record.delete 再删包，冷打开继续回收无正式元数据的自有 ID 目录；有正式元数据却缺包仍作为损坏返回。

恢复的删除先在目标目录持久改名为自有墓碑，再清理墓碑；目标卷屏障成功前不登记 Done、不发布 ready，失败或硬退出保留逆材料供 recovery。

Windows 连续步骤及跨域重叠按实际父目录身份和 FileCaseSensitiveInfo 匹配；不区分大小写时用系统 RtlUpcaseUnicodeString 大写表生成叶子键（包括缺失叶子），句柄期望路径用 CompareStringOrdinal(TRUE) 核验。保留目标名与当前名，支持仅大小写改名及撤销；K/K、i/İ、ß/ẞ 等系统独立名称不会因通用 Unicode 折叠而误合并，敏感目录继续保留精确拼写。

显式 recovery 冻结当前态时按 Windows 已核验文件句柄取真实目录项名称，保留逆记录 Before/After 的实际名称差异；持久发布后硬退出的仅大小写改名也可显式撤回。

冷打开按逆记录的目标及逆 ID/序号检查精确材料名，并读取独立的 `materials.jsonl` 身份日志；正文写入/墓碑改名前先持久登记父目录与文件身份。工作根、父目录、文件身份全匹配才回收，Windows 删除绑定已核验句柄。缺登记、登记截断、精确同名外来文件和普通目录替换均保留并告警；其它平台缺少句柄删除原语时拒绝回收，不退回按名删除。

Windows 材料打开在平台边界将 NTStatus 转为系统 Win32 错误；名称不存在可按 os.ErrNotExist 跳过，冷清理继续后续登记项。共享冲突等其它错误继续返回，不当作缺项。

工作区变动或材料清理失败只记录告警、保留材料，不阻断后台打开检查点存储；恢复时仍按原根身份和路径边界拒绝。正式元数据损坏或引用包缺失仍报错。






## Go 独立审阅（feat/kernel-k5，未合 main）

`internal/advisor` 实现 `turn.AdvisorPort` 与 `CompletionPort`。只接 `kernel.ReviewRequest` 的目标、显式验收、工作状态及证据；没有工具表、角色思考、私有配置或完整聊天历史。证据通过 `ResultStore` 按精确 Scope 读取，逐页检查 SHA256 和 UTF-8 字节半开范围。

`agent.advisor` / `agent.verify` 默认关闭、模型为空；开启需合法独立用途路由及 K1 的 Lifetime/当前授权/持久输入接纳、K2 的 Results/Work、K3 的预算化 ModelCaller。`app.ConfigureKernelReviews` 缺端口即报错，不使用裸模型客户端替代。完整配置见 内核补强 §9（私有存档） 与示例配置。

正式本地宿主以 `App.RunWork(ctx, ConversationKey, goal, criteria)` 显式进入 `Hub.RespondWork`；key 仍由已有本地私聊所有权检查核准，普通聊天不创建审阅或续轮。审阅前复核当前 Scope、Run/Generation、工作状态、证据和根预算。首次压缩前的工作 revision=0 合法；证据的 read 等来源标签不替代验收文字，核验模型逐项返回真实证据索引。advisor 同根串行、全局最多两个、每根最多三次（含失败）；`kernel_reviews` 的 cursor、verdict 和确认持久化。finding 与资料输入在接纳事务内提交，并发 Take 只有首次提交者返回。

候选先检查当前证据和逐项验收，再独立请求核验。状态为 done/continue/blocked/partial；失败不默认通过。最多续一次，保留 Root/Scope/Generation，ParentRunID 指候选运行；证据未变不再核验，第二次 continue 变 partial。未通过的候选不进入 Settled.Said；本地已流出的文本保留，最终 WorkOutcome 带核验状态。通过也不表示发送成功，K5 不直接调用渠道出口。

## 职责与边界

tools.ts（私有存档）（`ctx.tools`）：

- `forAudience(characterId, audience)`：按"谁在说话 × 哪个渠道"给出这一回合的工具（挂在回合事件链第 4 站 `turn/tools`，建对话实例时调用）。**所有渠道都走这一个函数**；以后 QQ 渠道传进来的 audience 不是主人私聊，只会拿到记忆工具。
- `gate(characterId, audience, call, signal)`：挂在 pi 的 `beforeToolCall` 上。按工具的声明判（policy.ts（私有存档） 的 `decide`，[规格-工具权限](../规格-工具权限.md)）：放行、生成审批请求等主人决定、或拒绝；拒绝、超时、取消都返回 `{ block: true, reason }`，原因会作为工具结果交给模型。
- `decide(id, choice, reason?)` / `cancelFor(characterId)`：主窗口按钮（允许一次 / 这个对话里都允许 / 始终允许 / 拒绝带原因，[规格-审批分级](../规格-审批分级.md)）；主人发新消息或实例被释放时取消该角色的全部待审批。
- 自动审批方可在内部结算时给 `denied` 设置完整 `feedback`（Go 为 `approval.Decision.Feedback`，Bun 为 `settle` 的第四参数），直接作为拦截原因交给模型。只影响拒绝反馈，不改变批准规则或取消语义；主窗口 `decide` 仍使用主人填写的 `reason` 和默认拒绝提示。表二批准版用它说明工作目录边界，见执行器规格第九节（私有存档）。
- **生态工具的档位和放行规则（ADR-330，[规格-审批分级第七节](../规格-审批分级.md)）**：`policy.Spec.Tier`——`web_search`、`web_fetch`、`wiki_lookup` 是 `open`（默认放行，留痕，防 SSRF、大小、时限、预算照旧）；`mcp_call`、`task` 是 `scoped`（手动问、自动只放只读的子任务、YOLO 放行，卡片上能记到“这个对话”或“记住”）。`Gate.Clear` 返回 `Clearance`（`By` 是哪一档放的、`Recheck` 是执行前再查一遍：批准后目标变了、规则被撤销了就不执行）；`Gate.CheckMCP` 按“服务器加原始工具名”记，规则认服务绑定加 schema 摘要；不是主人发起的（`NotOwner`）不吃任何自动放行。放行规则（`Grants` 的记住、`ConvGrants` 的这个对话）在设置 → 工具与审批顶部列出、能撤销（`api.ApprovalRules`，宿主在 `internal/app/desktop_chat.go`，页面 `native/workbench_rules.go`）；每步的放行记录写进 `approval_json`（`by`、`rule`、`label`），`turn.StepRule` 转成给主人看的一句。
- **`ask` 规则和规则作用范围（[审批分级 8.7](../规格-审批分级.md)）**：`policy.Decide` 原流程出结论以后再过一道 `MatchAsk`，结论是放行又命中 ask 规则的改成“这一次问”（`Limit: once`），问和拒绝的结论不碰；顺序 deny > ask > allow。规则（allow、ask）可带 `project`（绝对目录，按工作目录归一后比）、`session`（会话号，ask 还认派出子任务的上级会话）；拒绝不分范围；看不出场合时 allow 不生效、ask 照问。存储在 `tool-grants.yml` 的可选字段，不需要迁移。
- `grantList()` / `revokeGrant(key)`：设置里"已允许的操作"（接口 `GET /api/tools/grants`、`DELETE /api/tools/grants?key=`）。"始终允许"存在 `data/config/tool-grants.yml`（grants.ts（私有存档）），"这个对话里都允许"只在内存里。同一类：命令按用到的命令名（`git` 带子命令，`npx`/`pnpm dlx` 这类带包名），写文件按目录，别的按工具名。受保护路径只能允许一次；灾难性命令最多"这个对话"；管道、分号、重定向、看不清的命令和 `rm`、`powershell`、`python`、光杆 `git` 这些不给"始终"；改系统状态、装到用户目录、改全局配置的命令（`mustAsk`：`setx`、`winget`、`npm config`……）也不给"始终"、判定里也只认"这个对话"（ADR-115 第 4 条）；碰到工作目录外面的命令也不给"始终"（ADR-115 第 3 条、ADR-117 第 1 条：不为它改文件格式，以前点过的也只管工作目录里面）；手改文件加进去不该有的"始终"不算数。**命令的授权带范围**（ADR-115 第 3 条）："这个对话里都允许"记着当时那条命令碰到的外部目录（`convGrants`：会话 → key → 目录，真实路径；碰到的是已有目录记它自己；不存在的，建目录、删除这类命令记它本身，其余记它所在的目录，但上级是盘根、用户目录根或它们的直接子目录时也只记它本身（	ools.ts 的 grantDirOf，免得建 D:\newproj 记成整个 D 盘）；卡片按钮上写"「set-content」动到 D:\out 里的"），之后同一类命令要改的东西都看得清（看不清的只许是"从管道来、看得出出自哪里"）、碰到的外面（`policy.ts` 的 `outsideTargets`：会动的文件、按路径调用的程序、管道源头）都落在这些目录或下面才放行，不然照模式问；"始终"没有目录，只管工作目录、允许目录里的。灾难性命令的"这个对话"同样要过这一关（covered() 提到灾难性那一步前面，2026-10-19 复核）：批过删 C:\Users\me，删 Z:\ 照样问。

具体的文件和命令工具直接用 `@earendil-works/pi-coding-agent` 的 `createReadTool` / `createLsTool` / `createWriteTool` / `createEditTool` / `createPowerShellTool` / `createGrepTool` / `createFindTool`（输出截断、文件写入排队、图片读取都由 pi 处理）。

搜内容和找文件用的 `rg`（ripgrep）、`fd` 由 Sumika 自己管理：`bun install` 时 [scripts/fetch-tools.ts](../../scripts/fetch-tools.ts) 从 GitHub 发布页下载最新版到 `vendor/bin/`（已存在就跳过，`bun run setup` 可手动重跑），main.ts（私有存档） 启动时把 `vendor/bin` 放到 PATH 最前面，不依赖也不影响系统里装的版本。

## Go 生态工具（2026-11-04，feat/kernel-k4，未合 main）

`skill`、`mcp_search`、`mcp_call`、`mcp_result_read`、`mcp_cancel`、`web_search`、`web_fetch`、`wiki_lookup`、`task`、`task_result`、`task_list`、`task_message`、`task_cancel` 的公开声明集中在 [skills.PublicTools](../../native/internal/skills/resources.go)；插件按宿主 `ToolHost` 的可信 Access、Invocation 和当前可见性挂入。参数 JSON 不接受多余身份字段，来源文本不影响能力声明。对应状态和配置见[运行时 Go 生态来源](runtime.md#go-生态来源2026-11-04featkernel-k4未合-main)。

[mcpplugin.Plugin.Prepare](../../native/internal/mcpplugin/plugin.go)生成冻结 `turn.PreparedCall`：K3 单一路径须在顺序准入阶段调用并使用该 Spec/Invocation/参数，执行前复核 Valid。`mcp_call` 始终要求 [Gate.CheckMCP](../../native/internal/approval/kernel_ecosystem.go)对本次冻结 server/raw/public/schema/参数的一次审批，不创建工具名级持久授权；实际提交的 SDK Writer 与撤销共用服务锁，不能只在调度层判断 Valid 后发普通请求。

普通插件工具在准入审批前复核当前注册及听众，实际执行前继续复核冻结绑定；停用或替换的注册不会先发起一次已失效审批。child 的旧内置工具同样受委托能力限制，reader 不曝光也不执行写入、PowerShell 或记忆工具。

`skill` 返回的 `Page.Disposition` 由 K2 的统一 finalize/接纳/丢弃路径恰一次处理。工具执行 ctx 在返回时取消不能等同结果丢弃；ContextID 由宿主的实例身份给定，实例释放调用 `Catalog.Forget`。MCP 和 child 全文都由 K2 的 `kernel.ResultStore` 保存/授权读取；新来源默认关闭，四场合授权和接口装配边界见内核补强 §8.3（私有存档）。

## 工具表和声明（spec.ts（私有存档））

每个工具登记一份声明：会做什么（`self` 只碰自己的记忆 / `read` / `write` / `exec` / `net`）、给谁用（`any` 只允许全是 `self`，否则开机就报错）、能不能并行（没写就一个一个跑）、默认超时。`forAudience` 给出的每个工具都经过 `declare()` 套上执行方式和超时：到时间返回"超时了（N 秒），没做完"，标成出错（`isError`）；会改东西的（按参数收窄以后还有 write / exec / net）再提醒"可能已经执行了一部分，先确认状态再决定要不要重做"，只读的给"缩小范围再试"（ADR-094）。**没登记的工具**（以后的 MCP、子代理给的）照最危险的一档：`exec + write + net`、只给主窗口、三种模式都问。

| 工具 | 名称 | 声明 | 超时 |
|---|---|---|---|
| 查记忆 / 找原话 / 记住 / 忘掉 | `memory_search` / `recall_raw` / `remember` / `memory_forget` | self，所有渠道；`remember`、`memory_forget` 只在主人在场的私聊（主窗口、主人的 QQ 私聊），群里、朋友私聊、自检会话不给（ADR-088） | 30 秒 |
| 看链接详情 | `read_link_detail` | self，所有渠道；只按消息编号查本会话她看到过的消息的链接资料，不收网址、不联网（Go 的 linkplugin，规格-链接解析 8.1） | 10 秒 |
| 日历 | `calendar` | self，所有渠道和主动联系的线；查某天（默认今天，本机时区）：星期、农历、节气、传统节日在本地算（lunar-go），法定假日和调休联网取 holiday-cn（走免费接口注册表：jsDelivr、GitHub raw、timor.tech 依次回退；用户改了 `calendar.holidayUrl` 就只用他的）并缓存（`go/calendar/<年>.json`，有缓存马上用、旧了在后台更新）；该年还没公布、数据坏了、断网又没缓存都如实说，不按周末猜；不弹审批（Go 的 [lifetools](../../native/internal/lifetools/plugin.go)，规格-主动联系 第十九节） | 15 秒 |
| 天气 | `weather` | self，所有渠道和主动联系的线；先用 Open-Meteo 编码，再按地区走免费接口注册表（国内先腾讯新闻天气，其余先 Open-Meteo，备用 MET Norway，挂了自动回退；腾讯只发省、市），给当前温度、体感、天气、今明两天高低温和降水概率；`location` 由她传，不传时按场合用设置里的所在地（[规格-主动联系](../规格-主动联系.md) 第十九节）：主人本人的场合（含提醒到点那一轮）完整的，主人开放的群只到城市（省、市名和取整到 0.1 度的城市坐标，区县不出现），别人的私聊回“不知道在哪”，不透露所在地；存好的所在地直接给坐标和省、市，不再编码；走 `linkReader.proxy`（国内站直连）；每人每天 20、每群每小时 15 次、主人不限（`freeApi.limits`）；不弹审批；实现在 [`lifetools`](../../native/internal/lifetools/plugin.go)，判定在 [`app/lifewire.go`](../../native/internal/app/lifewire.go) | 15 秒 |
| 汇率 | `exchange_rate` | self，所有渠道；`from`、`to` 写币种代码，`amount` 可选；国内线路先 fawazahmed0（jsDelivr，备用 pages.dev），国外先 Frankfurter（备用 ExchangeRate-API），日更，缓存到下次更新前；次数同天气；不弹审批（[规格-免费接口](../规格-免费接口.md)） | 15 秒 |
| 改设置 | `preview_setting_change` / `apply_setting_change` / `rollback_setting_change` / `list_setting_changes` / `cancel_setting_change` | self，只在主人本地私聊和主人 QQ 私聊可见，执行时再核一次身份（群里即使是主人也不行）；真正的确认在工具里：apply 发审批卡、只认主人点的结果，不吃 yolo 和“这个对话都允许”；写入走设置页同一套服务，模型没有文件权限；白名单是宿主的表（所在地加 10 项链接插件设置），不认识的键拒绝；每次改动记到 `go/settings/changes.jsonl`，回滚只恢复那一项（[规格-客户端自知 第十一节](../规格-客户端自知与设置工具.md)，`internal/settingtool`、`app/settingtool.go`） | apply 330 秒（盖住 5 分钟确认），别的 5 到 20 秒 |
| 向主人提问 | `ask_owner` | owner-console，只在主人 × 主窗口的本地私聊；2～4 个选项，打字也算回答；回答不是批准（`internal/askowner` 不 import `internal/approval`）；无人值守不等，子任务不能用（ADR-341，规格-向主人提问） | 等主人 `tools.approvalMinutes`，且不超过本轮总时限减 45 秒 |
| 工具结果续读 | `result_read` | self；仅有可信工作 Invocation 的回合提供；精确匹配角色、会话、Scope、账号和 authority 代次，另受原工具的场合限制 | 10 秒 |
| 读文件 / 列目录 / 搜内容 / 找文件 | `read` / `ls` / `grep` / `find` | read，只给主窗口，能并行 | 30 秒 |
| 写文件 / 改文件 | `write` / `edit` | write，只给主窗口 | 60 秒 |
| 运行命令 | `powershell` | exec（每段都只读时算 read），只给主窗口 | 命令没写 `timeout` 补 120 秒，最多 600 秒 |
| 计划清单 | `todo_write` | self，只给主窗口（[规格-计划清单](../规格-计划清单.md)） | 5 秒 |

**失败和空结果都带下一步**（ADR-094，自己写的工具都照这条写）：没做成的标 `isError`；失败和"没找到"都写一句"可以怎么办"——换个说法、拆成短词、改用哪个工具、先查什么再来。例：`memory_search` 没找到建议换说法或用 `recall_raw` 找原话；`recall_raw` 没找到建议换更短、原话里一定有的词（每个词都要出现），或用 `memory_search`。

**记错了能改**（ADR-094）：`memory_search` 的每条事实前面带编号（事实 id 的末 6 位，"#K3Q9ZP"），片段写"（片段）"、不带编号；`remember` 的可选参数 `replaces` 填编号，新的记下、旧的标被取代（`supersedes`，留着，记忆页看得到）。编号对不上（抄错、已经被改过）就不记，标出错，叫她先查到现在的编号（记忆插件的 `factByCode`）。

**忘掉**（`memory_forget`，`memory.Forget`）：参数 `id`（`memory_search` 的 # 编号）、`reason`（一句话，≤100 字，记进撤销修订）、`source_quote`（和 `remember` 一样要引用本轮已接纳的主人原话）。效果是软删除：`facts.status` 改 `revoked`，行和修订都留着，不再被想起、整理和 `remember` 都不会复活同文。门槛和 `remember` 一样，只会更窄：只有主人私聊；用同一个写入身份（`remember-ai`），`confirmed_by` 是 `ai`、`owner_said` 的才动得了，主人放行过的和历史导入的受保护；只动关于主人（owner）或她自己（self）的有效事实，别人的事实、片段、待确认的、已经忘掉的、编号不存在的都报错；自己库里找不到才找主人共用池。Go 这边还没有“恢复”入口：记忆页（`memory.Browser`）能列出已撤销的和修订，但恢复（Bun 管理页的 `restore`）没有迁过来，要找回只能直接改库。

**先说要做什么**（规格-工具权限 ①b）：`write`、`edit`、`powershell` 多一个必填参数 `purpose`（排在最前，一句话），审批卡上显示"她说要做的事：……"，工具卡片标题写成"运行命令：……"；执行时去掉，不传给 pi 的工具（`withPurpose`）。这句话是她自己写的，只用来让主人看出"说的和做的对不上"，不当作放行的理由。

**命令先拆开看**（①c，shell.ts（私有存档））：`gate` 收到 PowerShell 命令先交给常驻的 pwsh（PowerShell 自带的语法解析器，不执行；第一次约 0.6 秒，之后几毫秒，闲 10 分钟自己关）拆开，`judge()` 挑出会改动什么、写在命令里的路径、看不清的地方（参数用了变量、`iex`、别的程序、直接调 .NET 改东西、语法错）。实例方法（`对象.方法()`）只有字符串、集合那些只读的放过，别的（`(Get-ChildItem …).Delete()`、`.MoveTo()`、`HttpClient` 联网……）都算改东西、看不清；有这种调用的命令不按"同一类"放行，"这个对话""始终"的授权都不算数（`methodRisk`，2026-10-19 体检）。自动模式下文字规则和拆开看都说只读才放行；拆开的路径也查受保护路径；审批卡上一条一行列出来（`ApprovalRequest.notes`）。拆不开（没装 pwsh、超时 20 秒、被取消）当看不清。分析器还给每条命令三样结构信息（只看语法树、不执行）：**参数绑定**——PowerShell 自带的 `StaticParameterBinder` 按 cmdlet 的参数元数据把位置参数、缩写（`-LP`）、别名（`-PSPath`）换成正式参数名（`bind`），只绑已加载的命令：分析器先加载 Management、Utility、Archive、Security 四个自带模块再关掉自动加载，查命令、绑参数时不会顺手加载别的模块（加载模块会跑模块里的代码；冷启动因此约 0.78 秒）；**命令名**——按这个 pwsh 自己的别名表（`Get-Alias`，启动时读一次）解析成原名（`canon`），是不是 PowerShell 命令看已加载的 cmdlet / 函数或 Verb-Noun 且动词在 `Get-Verb` 里（`cmdlet`）；**管道**——前面接了管道（`piped`）。静态方法再给类型全名。`judge()` 另给 YOLO 四样（ADR-112，2026-10-19 两轮复核后按"只有写死的才算看得清"、按结构判）：`opaque`（看不清：会改东西的命令参数里有变量、括号表达式、子表达式、拼出来的字符串——网络 cmdlet 只要求地址、`-Proxy`、`-WebSession`、`-OutFile`/`-InFile` 写死，`-Body`/`-Headers`/`-Method` 这类在白名单 `NET_FREE` 里的可以用变量；改文件的 cmdlet 写进去的内容 `-Value`/`-InputObject`/`-Encoding`/`-Stream` 这类（`CONTENT_FREE`，ADR-115 第 6 条）可以用变量，已知工具链里值是内容的参数同理（`CONTENT_ARGS`，现在只有 `git commit` 的 `-m`/`--message`，`git commit -m $msg` 不算看不清；`-F` 的值是文件路径不在表里，写死的照路径查、用变量算看不清；别的工具逐个加，ADR-117 第 2 条），`New-Item` 建链接时的 `-Value` 是指向的位置、除外；改文件的 cmdlet 没写死被改的那一侧——按绑好的参数名看 `-Path`/`-LiteralPath`/`-FilePath`（`New-Item` 还认 `-Name`），复制、压缩、解压写死目的地就行，目标从管道来、没写、绑不上都算；换到看不清的目录以后的相对路径（pwsh 自带的 `C:` 这类换盘函数也算）；`iex`、`cmd`/`powershell` 这类 shell；解释器自己位置上的内联代码开关 `node -e/-p`、`python -c`、perl / ruby 合写的 `-ne`……；解释器接了管道又没写脚本、或写了单独的 `-`（代码从标准输入来）；`npx -c`/`npm exec --call`/`pnpm dlx --shell-mode`；算出来的命令名；直接调 .NET（静态方法只放 `SAFE_STATIC` 白名单里的纯计算：String、Math、IO.Path 不含 GetTempFileName、DateTime、Regex、Convert……）；名字不是 Verb-Noun 的函数；语法拆不完整；看不清的原因另分类别 `opaqueKinds`——parse / dynamic / loose / unbound / cwd / piped / download，policy 按类判、不靠原因文字，ADR-115 第 5 条）、`fileTargets`（会改动的路径：改文件 cmdlet 写死的值（内容串也在里面，多问不漏）、重定向目标、git 的 `-C`/`clone`/`worktree add`/`init` 目标、git 只读子命令的 `--output` 和指到外面的参数、程序参数里的绝对路径 / `~` / 带 `..` 的、`--prefix` 这类目录开关；换过目录的相对路径接在新目录后面）、`runs`（按路径调用的程序、脚本）、`fetches`（下载一个包来跑的，ADR-115 第 1 条：`npx`/`bunx`/`pnpx`/`uvx`/`npm exec`/`pnpm dlx`/`yarn dlx`/`bun x`/`npm create x` 要跑哪个包、在哪个目录跑、能不能按 `package.json` 认——带版本号、网址的、`uvx`、`npm create` 不能；`gate` 只在有这一项时用 `projectToolsOf` 读跑的目录往上到工作目录每一级的 `package.json` 的 `dependencies`/`devDependencies` 和 `node_modules/.bin`，交给 `decide` 的 `projectTools`；声明过的 YOLO 下放行，没声明的问；"同一类"带上包名，如 `npx cowsay`）、`mustAsk`（改系统状态的程序和 cmdlet、`git config --global/--system`、全局安装、直接写的 `pip`/`python -m pip` 的 `install`/`uninstall` 和带 `--user` 的（ADR-115 第 2 条；按路径用工作目录里虚拟环境的放行）、认得的工具链不在子命令白名单里的）。工具链白名单（`TOOLCHAINS`：npm、pnpm、yarn、bun、pip、cargo、go、dotnet、gem、uv、pipx、poetry、conda）：装依赖、跑脚本、构建、测试、格式化、加减依赖这类放行，改用户级配置、装到用户目录、登录、发布、清全局缓存的问；pnpm / yarn / bun 省略 run 的（`bun build:web`）当跑脚本。参数先拆成"开关 / 写死的值 / 看不清的写法"一串（数组里的常量、`-Param:常量` 照常取出来，`-Force:$true` 不算看不清）；只读动词白名单以外的 Verb-Noun 都当会改东西；命令名去掉目录和 `.exe` 再查表，"同一类"的名字按路径调用的再带上路径。

Go 后台的对应入口是 [`approval.NewGate`](../../native/internal/approval/approval.go)、[`policy.Analyzer`](../../native/internal/policy/analyzer.go)：建门时后台预热一次（最多一分钟），首条业务分析复用常驻 pwsh；未就绪、失败照旧按拆不开处理。业务分析的 20 秒时限（`approval.defaultAnalyzeBudget`，点停止由 ctx 马上返回，超时杀进程后后台重新预热）包含排队、写 stdin 和等回复；超时或关闭可绕过排队锁中止进程，等写入、读管道和进程退出后再返回。空闲回调同时核对进程和代次，不回收刚用过的进程；关闭后不再重启。

Go 的解析和审批卡片对拍默认逐字比较 PowerShell 错误文案，同时比较全部语法结构、说明条数和顺序；不再屏蔽文案或只比错误条数。当前 golden 使用 zh-CN，未加其他语言的放宽分支；同样条数的不同错误必须判为不一致。

Go 审批的 [`Approvals.Decide`](../../native/internal/approval/decide.go) 在锁内认领卡片，锁外写“始终允许”，再提交事务结局；其他卡片的登记、回答、取消、超时和授权读取不等磁盘。认领后即使授权写盘或等待写盘锁不返回，取消和到期也立即放开 `Ask`；事务仍留在 `Pending` 跟踪，始终授权写成后补发批准，写失败返回错误、记日志，已停止的卡片补发停止结局并带失败原因，未停止的继续等回答。“这个对话里都允许”延后到批准持久化成功、`Ask` 放行且 ctx 未停止时才合并，写库失败或未放行的操作不留对话授权。

步骤持久化走 [`WireApprovalSteps`](../../native/internal/turn/approvalsteps.go) 接入的审批流程，不经过通知队列：等待写成才发卡，批准写成才放行工具。普通库操作不加人为短时限，连接释放后继续写完；写失败明确记日志，等待方仍在时返回拒绝及原因，不把失败当批准。普通取消、到期最多等持久化和通知收尾共 250 毫秒，之后回合先退出，写入继续跟踪到成功或报错；正常结局写成后最多等通知 250 毫秒。迟到等待不覆盖结局或已结束步骤，迟到批准只补审批、不复活已结束工具。A9 清理保持原分类：已持久化的等待归取消、未执行；已持久化的批准在执行前被杀也保守归结果未知，不能重跑。

不写授权文件的决定（允许一次、这个对话、拒绝及降为一次的选项）在同一把锁里认领、取走卡片并提交，不向到期暴露中间的已认领状态；“始终允许”保留锁外写文件的事务路径。

最终结局正在写库、卡片已被取走时，`Ask` 仍响应 ctx 取消；到期只在真正收成到期时发信号，抢不到已认领的批准就不发到期信号。返回时若结局已经写成，优先交回该结局；执行上下文停止仍可阻止后续工具执行，不为它合并对话授权。计时器在持久化结束后收掉，回合停止不改写后台事务的已认领结局。

[`Subscribe(onRequest, onResolved)`](../../native/internal/approval/subscribe.go) 为一个成对订阅方串行投递；已开始投递的等待先完成再发结局，尚未投递的等待可被结局合并，不能要求每张卡片都收到中间等待。保留的 `OnRequest` / `OnResolved(Request, Decision)` 每次注册各算一个独立的单边订阅方。各订阅方互不阻塞，每个至多一条投递协程，空闲退出；回调 panic 记录日志并收尾当前回执，积压非空就重新拉起，之后的通知继续。每张卡片只留一个待投递状态，结局不能被等待覆盖，版本不倒退；已投递结局释放版本索引。不同卡片的未交付结局不丢，因此积压上限是尚未交付的卡片数，而非固定条数。

`Snapshot()` 返回全部等待和最近 256 张已结束记录，成对注册原子重放并衔接增量。256 是本次暂定的重连缓存预算：完整结局已在步骤库，内存只保留最近卡片，避免按进程累计；1000 张突发测试会淘汰最早 744 张缓存，已有订阅仍收到全部 1000 张结局。淘汰不影响独立的投递队列。跨进程不恢复待批准卡片，按步骤库和启动清理收尾。

[`Approvals.Drain(ctx)`](../../native/internal/approval/decide.go) 先关闭新审批、调用 `CancelAll`，再等每张卡片的授权写入和步骤持久化结束或明确失败；连已移出 `pending`、仍在写结局的卡片也计数。返回 `(未收尾张数, error)`，ctx 到点返回剩余数量及 ctx 错误，可再次调用继续等；通知回调不挡关库。装配在关库前调用它并检查返回值，`CancelAll` 不能代替这一步。2026-11-03 起正式装配不再自动拒绝：Gate 使用已有 `tools.approvalMode`、`approvalMinutes`、`autoApproveDirs`；Request 由 Gate 绑定 `Conversation`，桌面经 `api.LocalChat` 取真实卡片、按会话和 ID 回答允许一次或拒绝；没有桌面或无人回答仍按原期限到期。只给主人 × 本地私聊主窗口交出执行工具；`turn.ownerConsoleAudience` 同时约束工具清单、插件执行和审批转换，显式群场合或群会话键都拒绝，不能拿本地群的 PrivateOK 当作执行许可。本地群文字入口走 `LocalGroupWriter`，不绘制审批卡；QQ 私聊、QQ 群只读记录。界面与关闭顺序见[本地私聊规格](../规格-界面接Go后台.md#441-本地私聊写入口2026-11-03)、[本地群聊规格](../规格-本地群聊.md)。

Go 的 K3 分支使用 [`turn/scheduler.go`](../../native/internal/turn/scheduler.go) 与 [`prepare.go`](../../native/internal/turn/prepare.go)：按调用顺序整理参数和审批，明确只读的 shared 工具按会话 4、全局 8 的滚动池执行，write/edit/powershell/todo_write/remember 与未声明工具形成局部独占屏障。等审批不占槽；timeout 先记可见结局，实际工具和受管回调退出后才释放槽。完成事实及时保存，模型结果、重复提醒和接纳回调按原 ordinal。K2 通过 `Config.FinalizeTool` 接唯一完整结果入口；K1 的活动/渠道身份接线见整合清单（私有存档）。本地私聊、本地群聊、QQ 私聊、QQ 群的现有 Gate 边界不变。


**删除进回收站**（①c，trash.ts（私有存档））：PowerShell 工具的每条命令前面接一段定义（pi 的 `spawnHook`），把 `Remove-Item` 和别名 `rm`、`del`、`ri`、`rd`…… 换成挪进回收站（Windows 的 SHFileOperation，不弹窗）。不是本机固定硬盘的、单个超过 2 GB 或盘的 5% 的、目录里超过 10 万个文件的不删、报错交给主人（Windows 对回收站装不下的、网络盘 U 盘会直接彻底删）。只是兜底：`cmd /c del`、`[IO.File]::Delete` 绕得过去，拆开看会把它们标成看不清。测试用环境变量 `SUMIKA_TRASH_TEST_DIR` 挪到临时目录，不碰真的回收站。

**防卡死**（"干活"第③步）：工具的默认超时见上表；同一个工具、同样的参数（键序、`purpose` 不算）、同样的结果连着出现，第 3 次在这次结果末尾追加一句温和的提醒，第 5、8 次具体提醒（util/tool-guard.ts（私有存档），挂在 pi 的 `afterToolCall`，agent.ts（私有存档））；只提醒不拦，主人说了新的话就从头数，计划清单不算。追加在结果末尾而不是单独一条消息：缓存前缀不动，也不会被 `slimOldToolResults` 当成"这一轮的开头"。大输出：pi 自己对命令输出截尾 2000 行 / 50 KB、全文存临时文件；按 token 封顶和清理等有真实数据再做（待办）。

Go 工作回合的完整输出由 [`artifact.Store`](../../native/internal/artifact/store.go) 保存到 `go/work/results/<随机ID>`，SQLite `result_refs` 保存 owner、原工具、SHA256、字节数和 Complete；命令输出边解码边写有界 sink，不再创建系统临时全文文件。单结果默认 16 MiB、根 64 MiB，达到单结果硬限取消生产者并标部分；根容量不足时拒绝新采集。模型预览连引用包装最多 16 KiB，`result_read` 的 offset/limit 都按 UTF-8 字节；页尾收齐字符，不能容纳首字符时明确报错。缺失和越权句柄的外部响应相同，损坏或文件错误明确报 unavailable，不拿 steps.preview 冒充原文。

[`turn.finalizeTool`](../../native/internal/turn/result.go) 是完整结果、步骤引用和工作日志的唯一入口；文件或数据库保存失败阻止下一次派发，不重新执行工具补证据。`result_read` 不读任意路径，也不赋予原工具的本地能力：本地文件/命令结果只可在原本地私聊读取，本地群聊、QQ 私聊、QQ 群只读自己在原场合已获准产生的结果。同群另一角色、另一会话和旧权限代次都拒绝。


## 怎么判（policy.ts（私有存档），测试 test/tool-policy.test.ts（私有存档））

顺序：渠道（只给主窗口的工具出现在别的听众那里 → 拒绝）→ 只碰自己的记忆 → 放行 → **受保护路径**（Sumika 的数据目录、`~/.ssh`、`~/.aws`、`~/.gnupg`、`.env` 文件；读、列、搜、写、命令都算，搜索起点是它们的上级也算；命令里写到了这些路径或 `secrets.bin`、`sumika.yml`、`tool-grants.yml`）→ 任何模式都问 → **后台自己的地址 + 端口**（ADR-106，`touchesSelfEndpoint`：命令里同一段既有本机地址 `127.x`/`localhost`/`::1`/`0.0.0.0`、又有主窗口端口或开发端口 7721）→ 任何模式都问、只能允许一次（免得她调自己的接口改设置、切档、批自己的审批）→ **灾难性命令**（risk.ts（私有存档） 的 `isCatastrophic`：格式化、分区、删盘符根，以及 ADR-106 加的删用户目录 / 它的直接下一级 / 凭据目录 `deletesUserDir`）→ 任何模式都问 → **YOLO 下拆不开、动态执行、编码过又带删除动词**（ADR-106，范围按 ADR-115 第 5 条：`hasDeleteVerb` + 拆不开或 `opaqueKinds` 里有 parse / dynamic，没有类别的当最严的算）→ 问、只能允许一次；目标从管道来、没写死、参数绑不上的删除不在这里，到下面 YOLO 那步照样问、能给"这个对话"（看 `opaque` 不看 `unclear`——`unclear` 里还有"bun 会运行别的程序"这种说明） → 主人授权过的同一类（`mustAsk` 的只认"这个对话"，ADR-115 第 4 条；命令的授权只管看得清、碰到的外面都在当时批准的目录里的，ADR-115 第 3 条）→ 按审批模式（见下表，YOLO 见 ADR-112）→ 兜底问。卡片上写为什么要问（`ApprovalRequest.why`）。读、写、改、列、搜、找工具的路径先用 `toolPath` 归一化（照 pi 的 `resolveToCwd`：去开头的 `@`、展开 `~`、转 `file://`、Git Bash 的 `/c/x`），受保护路径、在不在工作目录里、写文件的"同一类"目录都按归一化以后的判，和工具真去碰的是同一个路径。联接 / 符号链接：`gate` 先用 `realOf`（`realpathSync.native`，新文件往上找最近存在的上级）把 `linkPaths` 列出的路径解析好，作为 `real` 交给 `decide`（policy 保持纯函数）；"在里面"要字面上和解析以后都在里面，工作目录里指向外面的联接按外面算。命令里 `~` 开头、两个字母以上的盘符（`HKCU:`）、provider 写法（含 `::`）当在外面（`OUTSIDE_FORM`）；查受保护路径时命令里的 `~` 按用户目录展开。灾难性命令"这个对话里都允许"的授权单独一个 `catastrophic:` key 前缀（grants.ts（私有存档） 的 `kindOf`），免得普通 `Remove-Item old.txt` 的授权让同名的 `Remove-Item ~ -Recurse` 跟着放行（ADR-106）。

审批模式（ADR-021）：主窗口输入框下方切换，写回配置文件 `tools.approvalMode`，立即生效，对所有角色有效。

| 模式 | 规则 |
|---|---|
| 手动 `manual`（默认） | 读、列、搜、找放行；写、改、命令每次都问（命令哪怕只读也问） |
| 自动 `auto` | 命令：每一段开头都在只读白名单里（`git` 只允许 status / log / diff 等），且没有重定向、`&` 调用、改动类 cmdlet、脚本块里的删除 / 下载 / 启动程序，才放行。写、改文件：目标在工作目录或 `tools.autoApproveDirs` 里就放行（路径先 resolve，带 `..` 的绝对路径不算在里面）。其余照样问 |
| YOLO `yolo`（ADR-112） | 读放行。写、改文件（`write`/`edit` 工具）和会改东西的命令：看得清（只有写死的才算）、程序本身和 `fileTargets` 都在工作目录或 `tools.autoApproveDirs` 里才放行；`opaque`（看不清）、下载来跑的包没在项目里声明过（`fetches`，ADR-115 第 1 条）、`mustAsk`（系统状态、全局安装、全局 git 配置、工具链白名单以外的子命令）、碰到外面的一律问。和自动档的差别只在执行类命令：自动只放只读的执行，YOLO 下看得清、只碰工作目录里的执行（`npm`/`bun`/`node build.js`、`tsc`、`git clean`、`cargo build`……）也放。网络取数：碰后台自己的端口照上面一律问；地址（`-Uri`、位置参数）、代理、落地文件是变量的看不清、也问，请求体、请求头可以用变量；访问别的本机服务（3425 Magpie）和外网照旧放行。没登记的、受保护路径、后台地址+端口、灾难性命令（含删用户目录）仍然要问 |

本来要批准的（会写、会跑命令）被模式放行时，工具卡片上显示"已自动批准（理由）"，日志里记一条"自动批准"；读、只碰记忆的放行不记。手动审批的结果也记日志。

注意：pi 会先把同一批工具调用的审批全部过一遍再统一执行，所以一个已经自动放行的调用，可能要等同一批里另一个需要人工审批的调用处理完才开始执行。

## 运行路径

1. 模型发起工具调用 → pi 发 `tool_execution_start`（主窗口出现工具卡片）→ `beforeToolCall` → `Tools.gate`。
2. 需要审批：生成 `ApprovalRequest`（标题、命令全文或绝对路径、写文件的内容预览或与原文件的 diff；Go 编辑预览按上方共用计算生成 diff 或错误）→ 发 `tools/approval` → 主窗口广播，卡片挂在对应的工具卡片下；角色状态变为 `waiting`。
3. 主人点按钮（WebSocket `{type:'decide', id, choice, reason?}`）→ `tools/resolved` → 批准则执行，否则拦截；状态回到 `thinking`，回合计时从这里重新开始（等审批的时间不算首字耗时）。
4. 超过 `tools.approvalMinutes`（默认 10）分钟未处理按超时取消。

## 配置

`tools.workdir`：读写文件和命令的默认目录，不填是用户目录；也可以用绝对路径。测试和预览必须指向临时目录（`SUMIKA_CONFIG`）。

Go 后台的项目说明（`AGENTS.md` / `CLAUDE.md`，[规格](../规格-项目说明文件与skill.md)，代码 `native/internal/projectcontext/`，键在 `native/internal/config/projectcontext.go`）：`projectContext.enabled` 默认 false；`projectContext.trustedRoots` 默认空，工作区根在其中某个之下才读（ADR-206"问一次信不信任"的界面做出来以前用它代替）；`projectContext.userContext` 默认 false，数据目录里的用户说明暂按审批未通过处理。只有本地私聊读，本地群聊和 QQ 都不读。

## 约束

- Go 改文件预览与执行使用同一套编辑计算；模糊匹配也必须一致。上方 Go 编辑契约取代旧 pi 预览允许细微差别的约定。
- 审批请求只在内存里，重启后丢失（相当于取消）。
- 提示词 [scene-home.md](../../prompts/scene-home.md) 告诉角色哪些操作要审批、被拒绝后先问主人。
- 跑命令的说明（ADR-125）：`shellKind()` 照 pi 的找法（PATH 上先 pwsh.exe，再 powershell.exe）在本机查版本（shell-info.ts（私有存档），只留大版本.小版本），第一次等查完，之后最多每 10 分钟后台重查；主窗口按它挑 [shell-pwsh.md](../../prompts/shell-pwsh.md) 或 [shell-powershell5.md](../../prompts/shell-powershell5.md) 单独拼一段，只在建实例时拼，对话中途不换。没装就不拼。

## 验证

2026-10-13 在临时工作目录实测：新建 hello.txt → 审批卡片显示路径和内容 → 批准后文件写入；修改 hello.txt → 卡片显示正确的红绿 diff → 拒绝后文件未变，角色没有重试而是询问主人；拒绝后下一轮首字 2.9 秒（审批等待已排除）。

### 工具周期补全（`feat/tool-lifecycle`）

- `task_message(task_id, message)`：给自己派出、还在跑的子任务追加指示。`turn.Hub.SteerInternal` 把话放进子运行实例的内存插话队列，下一步请求前由 `deliver()` 带"上级追加"前缀送到（不用"主人亲口说"，不拼记忆参考信息，不扩大授权）。已结束报"已结束，可用 `task` 再派"，没有的报"没有这个任务"，别的会话、别的父运行拒绝。收尾后才到的指示被丢弃，所以"已排队"不保证她来得及看到。
- `task_result` 的 `wait_seconds`（0 到 300）：任务没结束最多等这么久，结束、被取消立刻返回；工具时限放宽到 310 秒。
- `task_list`：列本会话授权下自己派出的任务（编号、类型、状态、开始时间、摘要，最多 50 条）；`this_run` 为假的是以前回合派的，列得出但不能用 `task_result` 等操作。开始时间存在任务载荷里，不进去重摘要。
- `mcp_call` 等超过 `DetachAfter`（默认 10 秒）转后台，返回 `call` 号；`mcp_result_read` 的 `result` 传 call 号查状态和结果；`mcp_cancel(call)` 取消，用后台调用自己的 context（父运行寿命加取消键，Lifetime 登记），取消即断开那一次请求。快调用行为不变。登记表在 `mcpclient.Client`。取消后这次调用结果未知：远端服务被隔离，直到主人用 `sumika-ctl mcp release` 带原因接受风险；本机服务关掉进程树后记已终止并自动重连；同一服务一次只跑一个调用，后台调用占着槽时下一次会等。

## 用户钩子与编辑未命中提示（feat/user-hooks，未合 main）

配置式的用户钩子（[规格-用户钩子](../规格-用户钩子.md)）：[`turn/stage_hooks.go`](../../native/internal/turn/stage_hooks.go) 给内核加了 `OnToolResult`（一批工具跑完、结果落库之后，只对成功的工具）和 `OnStop`（模型没有工具调用、核验之前）两个同步挂点，只在 `Input.local()`（本地私聊、本地群聊，不含子运行）发；续跑次数由内核数，`StopEvent.Last` 为真时钩子照问但不再续。[`userhooks`](../../native/internal/userhooks/plugin.go) 是挂在上面的编译内插件：读数据目录的 `config/hooks.json` 和工作区的 `.sumika/hooks.json`（形状照 Claude Code，只认 PostToolUse、Stop；项目级要过项目说明的信任根，联接指到工作区外也不跑），用 [`tools.RunShellCommand`](../../native/internal/tools/shellcommand.go) 跑命令（PowerShell 把退出码包一层才传得回 2；超时和停止杀整棵进程树）。工具参数只进环境变量和标准输入。`hooks.*` 五个参数见规格第八节。

写 `.sumika` 目录每次都要审批（[`policy.Decide`](../../native/internal/policy/policy.go) 3.4，Sticky、只能允许一次，YOLO 和始终允许都不例外），因为项目钩子里的命令不经审批就会跑；读不拦。

`edit` 找不到或不唯一时，[`edittext.Hint`](../../native/internal/edittext/hint.go) 在原报错后面追加提示（newText 已在文件里、忽略空白逐行投票定位并画出缩进差异、字二元组相似行、重复的行号）；提示带着文件内容，所以 [`withEditHintsIfAllowed`](../../native/internal/turn/edit_hints.go) 只对读它本来就允许的文件开（受保护路径和联接指进去的不开），审批预览不带。
