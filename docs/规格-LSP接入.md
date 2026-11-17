# 规格：LSP 接入

> 状态：候选，未排期（2026-11-16 整理）。
> 2026-11-04 状态：规格，2026-11-05 随 feat/checkpoint-lsp 合 main，仅规格，Sol xhigh 独立只读审查第 2 轮 PASS；本轮不实现。实施等工具箱规格冻结语言服务器的发现、安装、版本和进程启动接口。
> 来源：主人 2026-11-04 要做“LSP 接入（让 Agent 改代码时更懂代码结构）”。以下参数和接口为我定的工程方案，不新增执行权限、自动安装许可或 ADR。

## 一、参考与取舍

参考 [成熟 Agent 对照](notes/对照-成熟Agent与Sumika后台-2026-11-03.md)、内核补强（私有存档）、[检查点规格](规格-检查点与回退.md)、[常见边界情况](notes/清单-常见边界情况-2026-10-26.md)、[性能标准](notes/标准-性能与效率-2026-11-03.md)。同任务的工具箱调研预期为 `docs/notes/调研-Agent的工具环境沙箱和worktree-2026-11-04.md`；本规格起草时当前分支尚未出现，不能冒称已对齐其接口。合入前再次核对。

直接阅读本机源码/文档：

| 来源 | 核对与借用 |
|---|---|
| Crush `internal/lsp/manager.go` `Start/startServer` | [读码] 按文件类型/根标记懒启动，现成客户端复用，unavailable 有冷却。借根范围、启动合并、可用状态；不扫描并启动全部语言 |
| Crush `internal/lsp/client.go` `Initialize`、`openFiles/diagnostics` | [读码] 客户端寿命独立于工具请求，initialize 前注册 server handlers，诊断版本缓存。借长寿命进程与短请求分开；不每调用重启 |
| opencode-v2 / beta `packages/web/src/content/docs/lsp.mdx`、`schema/src/config/lsp.ts` | [文档/读码] 当前默认关闭；按扩展名和项目依赖启动，文档列 gopls/typescript/pyright 等。当前本机两棵 core/src 及 packages 检索 `textDocument/didOpen` / `publishDiagnostics` 未找到客户端实现，不能引用旧路径假装当前源码存在。借显式启用和诊断反馈；不搬自动下载、不把诊断等于测试通过 |
| opencode v1.0.100，`packages/opencode/src/lsp/{index,client,server}.ts`、`tool/write.ts` | [读码] 浅克隆到 `_external/opencode-lsp-reference` 后核实：按 root+server 合并启动，didOpen/didChange 每文件版本递增，write 后 touchFile 等诊断并附到工具结果。借同步与工具结果反馈；不借手拼 file URI、诊断超时吞错、无版本诊断当当前、bunx 临时下载和全项目诊断无界附加 |
| Zed `docs/src/configuring-languages.md` | [文档] Tree-sitter 管语法、LSP 管语义；language_servers 和语言覆盖配置独立。借语言/服务器映射、导航和重命名分离；不实现编辑器、补全和格式化 |
| [LSP 3.17 官方规范](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/) | [协议依据] initialize/initialized/shutdown/exit、取消、PositionEncoding、版本化文档与 WorkspaceEdit。实现时用维护中的 Go JSON-RPC/LSP 库，不手搓 Content-Length/RPC 取消匹配 |

沿用：工具 Gate、可信 Invocation/Workspace、生命周期、ResultStore、根工具次数预算和检查点写路径。新设计：按项目根隔离的语义服务 manager、文档版本与结果投影。一次性：协议夹具、量测脚本。无迁移旧 LSP 数据。

## 二、语言与工具箱接口

先接实际代码语言：

| 语言 | 服务器 / 启动参数 | 项目根（从目标向上，止于本轮 Workspace.Root） |
|---|---|---|
| Go | gopls，`serve` | 最近 go.work 或 go.mod；Go 为首个实施和验收语言 |
| TypeScript / JavaScript | typescript-language-server，`--stdio`；需 TypeScript runtime | 最近 tsconfig.json / jsconfig.json，其次 package.json；只管仍在用的 scripts，Bun 后台/网页已退役 |
| Python | basedpyright-langserver，`--stdio` | 最近 pyproject.toml / pyrightconfig.json，其次本轮根；主要用于 embed 脚本 |

本次用 `where.exe gopls typescript-language-server pyright-langserver basedpyright-langserver` 检查 PATH，未发现上述命令；只代表 PATH 未装，不代表磁盘上不存在。禁止为验证规格自动安装。

exe 来源：由工具箱规格提供。此处定义需求接口，待 agent-env 对齐具体类型名：

```go
type ServerRequest struct { Language, WorkspaceID, Root string }
type ServerLease interface {
    Command() (absoluteExecutable string, args []string, env []string)
    Version() string
    Release() error
}
type ServerProvider interface {
    Resolve(context.Context, ServerRequest) (ServerLease, error)
}
```

Resolve 只发现已批准安装、固定版本的程序，不联网安装，不执行项目提供的同名 exe；找不到返回 unavailable 和可操作原因。工具箱负责完整性、runtime、环境白名单、Windows 无窗口进程/杀树、资源隔离；LSP manager 负责协议、租约与请求寿命。接口不是实际实现的替代物；缺 provider 时不提供工具。项目里的命令、AGENTS、模型参数不能指定 exe、env 或安装源。语言服务器会读取项目并可能启动编译工具，必须在工具箱信任边界内启动，不当作纯字符串只读。

## 三、给模型的能力

统一 `lsp` 工具，中央 policy 声明 read、owner-console、Parallel=true；参数严格解码，未知字段拒绝。仅可信主人 × 主窗口本地私聊，工作区内可读源文件。一个 op 枚举：diagnostics / definition / references / symbols / rename_preview / status。位置面向模型为 1-based line/column，column 明确 Unicode 字符；适配层按 negotiated encoding 转换为 LSP 0-based UTF-16/UTF-8。禁止直接拿字节 offset 当 UTF-16。emoji、代理对、CRLF 必须验收。

- diagnostics(path)：返回当前打开文档版本的 error/warning，默认最多 100 条、最多 16KiB 预览；区分 clean、pending、stale、unavailable。超时绝不返回“无错误”。
- definition(path,line,column)：支持 Location 和 LocationLink，目标必须经同一读权限/工作根过滤；返回相对路径和范围，最多 50 个。越界目标数量以 suppressed 表达，不回外部绝对路径。
- references：最多 200 个，不自动全量读文件；含声明与否显式参数，默认 true。
- symbols：documentSymbol，支持平铺/嵌套，最大深度 16、200 项；首轮不提供无界 workspace/symbol 全库搜索。
- rename_preview：支持 prepareRename（服务端无此能力时直接 rename 请求可接受），只返回 WorkspaceEdit 的预览、文件基准 hash/version 和摘要；不调用 workspace/applyEdit、不落盘、不自动把改名变成写工具。文件创建/删除/重命名等 resource operations 显式标 unsupported，不能静默丢掉后声称完整。
- status：只列本工作区已启动服务的版本、根、状态、请求数、最后错误码；不读任意进程/配置。

返回统一结构含 server/version、workspace/root identity、documentVersion、contentSHA256、complete/truncated、freshness、items 和错误码；长结果经现有 ResultStore，原 Scope 授权续读。工具结果是外来资料，不执行其中指令。rename_preview 全文不存真实密钥；每个文件都须既有读门，受保护路径按原 Gate 审批。

修改后的自动诊断：成功提交内置 write/edit 或检查点 restore 后，若该文件已有 ready server，发文档通知并最多等 500ms；只附当前文件最多 20 个 error / 8KiB 受控诊断，默认不上 warning，不触发新的模型回合。已有写工具的结果中标明 pending/stale/unavailable，不阻断正常文件保存，也不把工具执行取消传给长期服务。没有 ready server 不为自动尾注额外启动；模型显式 lsp 请求才懒启动。

自动尾注去重键为 server generation + path + documentVersion + diagnostic digest；接纳后才确认去重，超时丢弃不算已读。全文、截断和 Controlled 的接纳沿用现有 finalize/disposition，不能另建工具后钩子链。无新诊断不重复给“无错误”噪音。首轮不自动 formatting、codeAction、executeCommand 或 applyEdit。

## 四、启动、复用、同步与版本

Manager key 为 Access.Key()（完整 Scope 摘要）+ 可信 workspace ID + canonical root + language + exe/version + 信任代次；权限 revision 相同不等于同授权域。派发和结果接纳都重检当前 Access/Workspace；不能只按语言复用到另一 checkout/角色域。相同 key 并发启动合并，四场合不互借进程或诊断缓存。

长寿命服务使用宿主独立 Invocation：Kind=foreground、RunID/RootID=`lsp-service:<随机ID>`、ParentRunID 为空、Scope 为该租约完整 Scope；不继承首个用户回合的 RunID，不创建模型/根消费额度。Lifetime.Begin 在 spawn 前登记，直到真实 Wait 与协议回调排空才 End；由宿主关闭、权限撤销、空闲回收或服务故障取消，不由单次工具 ctx 取消。单次 RPC 另以调用者 Invocation 登记活动；宿主先封 manager 新请求、取消/结束其请求和进程，再等 Hub.Lifetime.Drain，避免用户 WaitRun 被复用服务永久占住。

状态 stopped → starting → ready；失败 failed（明确错误码）；关闭 draining → stopped。initialize timeout 10s，普通请求 5s，诊断首轮最多 2s，自动尾注最多 500ms；每服务最多 4 个在途 RPC、整个宿主全部 manager 合计最多 8 个（排队也计请求总时限），宿主合计最多 3 个服务进程；64 个 opened 文档/服务、每文档 1MiB、宿主总打开正文 16MiB。诊断缓存每服务最多 1,000 项且总计 4MiB，单项含正文/relatedInformation 最多 8KiB、关联项最多 8；RPC 单消息 8MiB、stderr 环形 64KiB。超限在投影前按字节截断并保留 truncated，不得将截空结果报 clean。配额是我定的工程预算，不按遇到的失败临时放宽。

已派发 RPC 超时/取消由库发送 `$/cancelRequest`，模型可先得到 timeout；RPC 槽和其 Lifetime 活动仍占用，直到响应到达或连接/进程真实终止。等待取消超过 2s 的连接进入 draining，拒新请求；已有未取消有效请求可在自己的原截止前完成，之后统一关闭该连接并等进程树退出，不额外无限续期。迟到结果只收尾不接纳；排队尚未派发的直接取消不占槽。测试须证明取消一项不会立即杀死其它有效请求，也不能靠反复 timeout 绕过 4/8 上限。

复用 10 分钟无请求/打开文档活跃则回收；定时器只在有服务时存在。shutdown 请求最多 2s，再 exit；仍不退由工具箱终止进程树并等 Wait，不能取消句柄后就放槽。启动失败/崩溃 30s 冷却，最多每工作区每小时 3 次自动重启；只有后续显式请求能触发，禁止定时无限重启。冷却/次数不足照实 unavailable，不阻止无 LSP 的普通工具。

服务器 initialize capabilities 只宣告实现的协议：声明 positionEncodings、文本同步、diagnostics version 支持；初始化前装 handler。服务端 workspace/configuration 返回宿主固定选项；workspace/applyEdit 一律 applied=false；workspace/executeCommand 不提供；注册动态能力只接已支持只读能力，其余明确不支持。

| op | 服务端能力 | 缺失分支 |
|---|---|---|
| definition | definitionProvider | unsupported，不发请求 |
| references | referencesProvider | unsupported，不发请求 |
| symbols | documentSymbolProvider | unsupported，不发请求 |
| rename_preview | renameProvider；对象值的 prepareProvider=true 才 prepareRename | 缺 rename 则 unsupported；缺 prepare 直接 rename，仍只预览 |
| diagnostics | 首轮仅 push publishDiagnostics | 未收到当前版本诊断为 pending；无 version 为 unversioned；不实现 pull diagnostic |

positionEncoding 缺省为 UTF-16，只认协商后的 UTF-16/UTF-8，未知返回 unsupported。textDocumentSync 为 None 则变更后标服务 out-of-sync，不宣称可用；数字 1/2 按协议 Full/Incremental，options 额外遵守 openClose 和 save/includeText。openClose=false 不发 didOpen/Close；save 未宣告不发 didSave。Full 用整文；Incremental 用同步队列中上一版本全文范围的合法替换；不能把 Full 形状无条件发给 Incremental 服务器。

文档在首次查询前：从可信读路径取当前原始字节/UTF-8，binary/无效 UTF-8 拒绝；版本从 1 递增 didOpen。成功写入后从磁盘重读本次真实提交，不用模型 args 拼内容；同步用服务端宣告的 full 或 incremental（首轮实现 full，incremental 能力需要适配成合法全范围替换），按顺序 didChange、didSave。改名/删除发 didClose + watchedFiles；恢复一组文件之后按真实完成列表通知。PowerShell 结束后关闭受影响工作区已开文档并失效诊断，下一查询重开，不能猜完整变更范围。

同一个文件一条同步队列，通知、查询、诊断版本单调。诊断带 version 必须匹配；无 version 的 publishDiagnostics 只能标 unversioned，不能作“当前版本 clean”的证据。变更后先撤下旧结果，迟到 generation/版本丢弃。服务崩溃清本 generation 的能力和诊断，重启重开仍在租约内的文档；不重放 rename 或其他修改请求。

每次查询前按原读门重读/核对磁盘正文，变化就先同步；请求冻结 generation/version/hash，响应只对这份基准有效。响应前再检查磁盘和同步队列水位，期间有变化标 stale，不把当前 hash 贴到旧计算结果上。rename_preview 的 versioned documentChanges 必须与服务器已同步版本一致；未打开文件、无版本 changes 或 null version 无法证明跨文件计算基准时标 baseUnverified，hash 仅注明“响应后观察值”，不能作为可直接应用的 CAS 凭据。只预览，不开放落盘入口。

## 五、路径、权限与资源边界

所有工具参数不能带 Actor/Authority/RootID/工作目录或 exe；来源来自本轮 Invocation 和冻结 Workspace。工作区根发现不出界，不追 worktree 的 .git 到另一个 checkout；file URI 经官方 URI 编码，不能手拼 `%`、`#`、空格、盘符。拒非 file scheme、UNC 外部目标、ADS、符号链接/联接越界和受保护根；definitions 的标准库/依赖代码如果在工作根外，先只返回 suppressed，后续扩范围需单独规格。

| 形状 | 工具显式入口 | 自动诊断 | server→client 请求 | 结果续读 |
|---|---|---|---|---|
| 主人本地私聊 + 合法工作区 | 处理了：原工具门 | 处理了：已有服务 | 处理了：只读 handler | 处理了：精确 Scope |
| 本地群聊、QQ 私聊、QQ 群 | 处理了：不可用 | 不可能：不装服务 | 不可能：不启动 | 处理了：拒绝借域 |
| 无 owner/缺权限/伪造参数/旧代次 | 处理了：拒绝 | 处理了：不附加 | 处理了：关闭旧租约 | 处理了：拒绝 |
| 路径缺/空/0/类型错/越界/URI 编码 | 处理了：边界校验 | 处理了：真实提交路径 | 处理了：拒绝越界/applyEdit | 处理了：不收文件路径 |
| 版本缺失/迟到/崩溃/重启 | 处理了：freshness 显式 | 处理了：不报 clean | 处理了：generation 隔离 | 处理了：保留历史版本标记 |
| Unicode/CRLF/二进制/超限/大量结果 | 处理了：位置转换和限额 | 处理了：有界截断 | 处理了：有界消息 | 处理了：字节续读 |
| archive/demo/关闭/取消/工具 timeout | 处理了：不启动 | 不可能：无活服务 | 处理了：真实退出 | 处理了：原门 |

资源上限不能仅写文档：Go manager 内部计数/字节硬限；服务器进程 CPU/内存硬限由工具箱契约提供。若 provider 无硬限能力，状态必须标 resourceLimitUnsupported，不声称有 OS 强隔离；正式启用前验收资源范围。初始暂定服务器工作集合计 <1GiB、空闲 60s CPU 单核口径 <1%，具体项目索引负载与机器条件随验收记录；未知不填零，不把模型 RTT 混入服务请求延迟。

## 六、禁止做法、实施计划和验收

禁止任意 RPC 透传、自动安装、从项目 PATH 捡同名程序、把 LSP 当沙箱、自动 applyEdit/format、每轮启动/扫描所有语言、用无诊断当 clean、忽略能力协商、一个全局诊断 map 跨工作区、把请求取消当进程退出、无限重启、自己写第二套 JSON-RPC。

接线同时登记中央 policy、authorizeTool 的 local.read 映射、ResultStore 的 lsp 来源复核。child 本轮不提供 lsp：读者/工作者能力模板都不加，避免 internal 会话绕过本地私聊范围；以后开放需单独验收。

实施顺序：

1. 工具箱 agent-env 冻结 provider/lease、信任、版本、资源和进程树接口；核对本规格后只选维护中的一个 Go LSP/JSON-RPC 库并单独记录选型依据，不在本轮加依赖。
2. Go-only manager + 协议夹具验证 initialize/sync/cancel/shutdown、版本和边界；临时目录启动真实 gopls。
3. lsp 工具和原 ResultStore/Gate/根工具次数/Lifetime 接通，诊断、定义、引用、符号、rename_preview 全部覆盖，再接 write/edit/restore 的真实提交通知。
4. 同样接 TypeScript、Python，验证工具箱缺依赖错误、两个 workspace/两个角色、不同时序结果；更新配置和参考页。
5. 独立代码复核、改坏、性能和相关包 go-verify 通过后提交合入候选；界面另做。

验收：

| 编号 | 需要的实际证明 |
|---|---|
| L1 | 临时 Go/TS/Python 多文件工程，diagnostics 有错→修复→当前版本结果；definition/references/symbols 精确位置；rename_preview 不修改磁盘，所有目标齐全或明确 unsupported |
| L2 | 同 key 并发只起一次、不同 root/版本/权限隔离；请求取消不杀其他请求；shutdown 等真实进程退出 |
| L3 | didOpen/change/save/close、shell 后失效、检查点 partial 实际路径同步；版本缺失、迟到/乱序、崩溃重启不能报 clean |
| L4 | 四场合和恶意路径/外来 URI/服务端 applyEdit/executeCommand 拒绝；原读门/结果域不可借用 |
| L5 | 超时、缺 exe/runtime、冷却/三次、排队/消息/正文/缓存边界；没有静默成功或假 fallback |
| L6 | 实际模型请求有 lsp 工具、结果和自动尾注进入下一轮；普通聊天不启动服务器、不自动续轮、不越过原工具预算 |
| L7 | 三遍真实小工程/本仓临时副本量冷启动、热查询 P50/P95/P99、索引 CPU/WS/private、60s idle CPU、子进程数/产物路径和字节；热导航 P95 <100ms、冷初始化 <10s、资源按第五节；超线定位不放宽 |
| L8 | 每个新增测试拆关键保护改坏确认红，再恢复；相邻 read/write/edit、检查点、child、四场合回归及相关包 short/vet；临时产物清除 |

本轮交付仅此规格及独立审查。L1～L8 属后续实现验收，不能写成已经运行。

## 七、独立审查记录

2026-11-04，omp `magpie/codex/gpt-6.1-sol --thinking xhigh`，仅开放 read/grep/glob。第 1 轮 VERDICT: FAIL，指出授权域、服务活动归属、取消占槽、能力协商、结果基准和诊断缓存字节预算六项；均补入第四节。第 2 轮原文：`LSP VERDICT: PASS`，核对最终规格和旧版源码出处，未发现必须修复项。审查为静态规格评价；未运行 LSP、未安装语言服务器。L1～L8 未执行。

2026-11-05 补核 agent-env 已交回的调研：主工作区 `docs/notes/调研-Agent的工具环境沙箱和worktree-2026-11-04.md` §5.2、5.5、5.6。借其“来源清单含精确版本 / OS / arch / SHA-256 / 相对 exe、任务固定具体版本、无人引用才清旧版”与“宿主进程不等于 OS 沙箱”的边界；它明确只交调研，没有冻结工具箱 provider/lease 或 LSP 服务器交付接口，因此第一步依赖仍未满足。本补记不改变已审通过的协议、权限或默认值。
