# Bun 后台（server/）

状态：ADR-345 分批搬家中，第 1～3 批已合 main；第 3 批收尾和第 5 批（插件宿主、链接读取、生活工具、复读判定）在 `feat/bun-server-rpc` 待合。现在只接了**本地私聊**，QQ 是第 4 批的事；只在 Go 的 `-backend-url` 指向 Bun 时生效，Go 后台照常是默认。规格：第 1 批（私有存档）、第 2 批（私有存档）、第 3 批（私有存档）、第 5 批（私有存档）、本地工具补齐（私有存档）；方案见[方案-后台换Bun加pi](../notes/方案-后台换Bun加pi-2026-11-10.md)。

## 职责与边界

- 和 Go 后台实现**同一份界面协议**（`protocol/sumika-rpc.json`，`scripts/gen-rpc.ts` 生成 Go 和 TS 两边的线上类型），主窗口换后台只是换地址。
- 和 Go **读写同一个数据目录、同一套表**：库结构直接用 Go 的迁移文件（`native/internal/store/migrations/`），Bun 不自己建表；要加表先在 Go 的迁移目录占号。
- **什么时候开写方法**（规格-本地私聊切到Bun（私有存档））：配置 `backend.local` 是 `bun`，**并且**拿到本地私聊的写锁 `go/local.lock`（和 Go 同一把，`LockFileEx` 独占），**并且** `run/` 收成了只给当前用户；三样缺一样就只开读方法，原因写在 `runtime.read` 的 `reason` 里。配置不是 `bun`（默认 `go`）时即使锁空着也只读。Bun **不碰** `go/backend.lock`（那把是“Go 后台在跑”，Go 两种模式都拿）。
- **谁写什么**：`bun` 模式下 `local.db` 只有 Bun 写，Go 只读打开；`trace.db`、`memory.db` 两边都写（WAL，`busy_timeout` 5000，写事务短，Bun 用 `BEGIN IMMEDIATE`；`native/internal/store/dualwrite_test.go` 守着）。共用的 `trace.db` 里启动收拾只动自己渠道的在途行：Bun 的 `LocalStore.recover()` 只收 console 的回合、步骤、输入、运行，Go 在 `bun` 模式下排除 console。
- **谁起它**：`backend.local: bun` 时由 `sumika.exe` 拉起并看护（`native/bunchild.go`，**临时：Electrobun 接管进程后去掉**）；`run/rpc.json` 归 Bun，Go 的 `-rpc-listen` 写 `run/rpc-go.json`。宿主先拿数据锁、Go 的后台 Open 成功才拉 Bun，blocked 时收掉；每次拉子进程给一个随机的 `SUMIKA_HOST_NONCE`，Bun 写进 `rpc.json` 的 `nonce`，宿主靠它认出地址文件是自己拉起的那个写的；`-root` 传资源根，Bun 找配置文件的顺序和 Go 一样（`SUMIKA_CONFIG`、数据目录里的 `config/sumika.yml`、资源根的 `sumika.yml`）。`run/` 的权限不管哪种模式都先收再写 `rpc.json`，收不紧不写（后台照常开着读方法）。
- **`selftest.*`（只给 `sumika-ctl` 用）**：`selftest.chat`、`selftest.slash`、`selftest.whisper`、`selftest.whisper_status`、`selftest.whisper_cancel` 在写模式下登记（`src/methods/selftest.ts`，逻辑在 `src/local/selftest.ts`），界面不调。会话由后台按角色拼成角色自己的自检话题（`console:<角色>:topic:selftest…`），参数里没有会话 id；自检话题的事件带 `selftest` 标记，不给记忆写入和插件；`isLocalPrivate` 和 `LocalChat.accepts` 仍把自检话题排除在外，只有 `acceptsOwner`（自检形状 + 话题已登记）和 `selftest.*` 进得去。不替主人批审批。见规格-本地私聊切到Bun（私有存档）第十一节。
- 行为以 Go 的测试样例为准：审批判定、联网、记忆召回和写入、回合输入语义都和 Go 对拍（见“验证”）。

## 目录

| 目录 | 管什么 |
| --- | --- |
| `src/app.ts`、`src/main.ts` | Cordis 装配：存储、协议服务、本地私聊 |
| `src/config.ts` | 读 `backend.local`（`SUMIKA_CONFIG` 优先，否则 `<数据目录>/config/sumika.yml`）；缺、坏、写错一律按 `go`（只读）并写明原因 |
| `src/rpc/`、`src/protocol/`、`src/methods*` | JSON-RPC 2.0 over 本机 WebSocket、令牌、`run/` 权限、方法表 |
| `src/store/` | 读写 Go 的库（`local.db`、`trace.db`）、本地私聊写锁（`datalock.ts`，`go/local.lock`）；`links.ts` 是链接表的读写（`message_links`、`link_snapshots`、`link_results`、`link_aliases`，SQL 照 Go `store/links.go`） |
| `src/link/` | 链接读取库（纯库，规格 5.2）：各站取数、共享缓存和在途合并；出网由调用方注入（`LinkFetch`），自己不联网、不碰宿主实现；插件用 `#link/*`（`package.json` 的 `imports`）引它，改它的代码不会热加载，要重启后台；`testkit.ts` 是测试用具 |
| `src/local/` | 本地私聊和本地群聊：一轮（pi `Agent` + Magpie）、工具、审批门、工作目录、角色卡、记忆接线、分块消息（`parts.ts`）、悄悄话和它的日志（`whisper.ts`、`whisperlog.ts`）。工具在 `tools.ts` 登记：`read`、`ls`、`write`、`powershell`、`memory_search` 和规格-Bun本地工具补齐（私有存档）里的 `edit`（`edit.ts`，定位替换用 `policy/edittext.ts`）、`grep`、`find`（`search.ts`，rg、fd 取仓库 `vendor/bin`；`find` 范围大时可用 Everything 的 `es.exe`，`everything.ts`）、`todo_write`（`todo.ts`）；名字、说明、参数表都从 Go 的 `declarations_gen.go` 读；执行路径和审批判的是同一个函数（`policy/decide.ts` 的 `toolPath`）。设置读 `tools.everything`（`enabled`、`path`、`timeoutMs`，见 `sumika.example.yml`） |
| `src/text/` | 回复后处理：剥句尾乱码、清多余的“】【”（对拍 Go `textclean` 的向量） |
| `src/policy/` | 审批判定和授权（从 Go `policy`、`approval` 移植，对拍 Go 的测试数据） |
| `src/memory/` | 记忆召回（只读连接）和写入第一档（`remember`、`memory_forget`） |
| `src/web/` | 联网和查百科：七家搜索、本机读网页、`wiki_lookup`、余额、出网地址检查 |
| `src/plugins/`、`src/plugin-api/`、`plugins/` | 插件宿主（第 5 批）：扫 `plugins/<id>/index.ts`，每个插件一个 Cordis 作用域，清单核对、按 `needs` 发能力对象（`log`、`settings`、`state`、`context`、`tool`、`seen`、`fetch`、`store`、`onStop`）、出错隔离、热加载（拷目录换路径）。`plugin-api/` 是插件能 import 的公共类型；`plugins/` 是仓库内置插件（`example-echo` 默认关；`life-tools` 默认开：日历、天气、汇率和免费接口注册表；`repeat` 默认关：只有复读判定的纯函数，没有触发入口；`link-reader` 默认开：链接读取，用 `store:links`（宿主实现、`src/plugins/linkstore.ts`）读写 Go 建的链接表）。开关在 sumika.yml 的 `plugins.<id>.enabled`（没写用清单的 `defaultEnabled`，下一轮生效），设置在 `plugins.<id>.settings`。只接本地私聊；本地群聊、悄悄话这一批不给插件。数据目录的 `plugins/`（她自己写的）不扫描。热换拷贝放 `server/.plugins-hot/`（不进 git）。`test/plugin-boundaries.test.ts` 守插件的 import 边界。每个插件有自己的结构化日志（`cap.trace`，宿主也自动记装载、卸载、工具调用等）：`<数据目录>/logs/plugins/<id>/<日期>.jsonl`，每天最多 2 万行、留 30 天；汇总用 `bun server/src/plugins/stats-cli.ts --data <数据目录> [--days 7] [--plugin id] [--json]`（规格第 5 批 9.7；运行页和 `sumika-ctl` 还没接） |
| `src/agents/`、`agents/*.md` | 执行者档案和 `task` 派活 |
| `src/prompt/` | 系统提示词（照 Go 的段落和顺序，读同一份 `prompts/` 和角色卡）、当前时间、参考信息位置、重建历史；会话实例在 `src/local/chat.ts` 里留着上一轮的工具调用和结果 |
| `src/work/` | 工作、触发、运行（trace 迁移 0020）：建、暂停继续取消、到点触发、合并补跑、运行的阶段；上面建目标（`/goal`）、定期任务（`/loop`）、提醒、后台整理（只登记不整理），`runtime.ts` 的 `WorkService` 管调度、授权（`authorizeLoop`、`authorizeDelivery`）和状态订阅 |

## 配置（`backend` 段，Go 和 Bun 都读，只在启动时读）

| 键 | 默认 | 意思 |
| --- | --- | --- |
| `backend.local` | `go` | 本地私聊由谁写：`go` 或 `bun`；写错 Go 启动报错、Bun 退成只读。改了要重启 sumika.exe（它会连带重启 Bun 子进程） |
| `backend.bun` | 空 | Bun 可执行文件；空 = `PATH` 里的 `bun`，再找 `%USERPROFILE%\.bun\bin\bun.exe`；写了但不存在就报错，不悄悄换别的 |
| `backend.server` | 空 | `server/` 目录；空 = 资源根下的 `server/`，里面要有 `src/main.ts` |

退回 Go：把 `backend.local` 改回 `go` 再重启，数据不迁移；Bun 上建的提醒、目标、定期任务（`works` 表）和“记住”的授权（`run/bun-grants.json`）Go 看不到。示例见 `sumika.example.yml`。

## 依赖

版本写死在 `server/package.json`，`server/bun.lock` 一起提交。升级照 AGENTS“定时任务”一节：正式小版本发布满一天就升，跨大版本先读更新说明；升完跑下面的验证。

| 包 | 版本 | 许可证 | 用在哪 | 为什么用它 |
| --- | --- | --- | --- | --- |
| `@earendil-works/pi-agent-core` | 1.5.1 | MIT | `src/local/chat.ts`、`src/agents/task.ts` | 一轮里的模型和工具循环（`Agent`、`steer`、`beforeToolCall`、`prepareRequest`），ADR-345 |
| `@earendil-works/pi-ai` | 1.5.1 | MIT | `src/local/model.ts` | 模型接口，走 Magpie（OpenAI 兼容，占位 apiKey） |
| `@earendil-works/pi-schema` | 1.5.1 | MIT | 工具参数表 | pi 的参数表类型 |
| `cordis` | 4.0.0-rc.12 | MIT | `src/app.ts` | 服务装配和生命周期，以后的插件热加载（第 5 批） |
| `lunar-javascript` | 1.7.8 | MIT | `plugins/life-tools/calendar.ts` | 日历插件本地算农历、节气、传统节日（作者 6tail，和 Go 用的 lunar-go 是同一个作者的同一套算法；没有类型声明，插件目录里的 `lunar.d.ts` 只声明用到的方法） |
| `@mozilla/readability` | 0.6.0 | Apache-2.0 | `src/web/html.ts` | 本机读网页时挑正文（Go 用 `go-readability`，ADR-348） |
| `linkedom` | 0.18.15 | ISC | `src/web/html.ts` | 给 Readability 一个不跑脚本的 DOM |
| `turndown` | 7.2.5 | MIT | `src/web/html.ts` | 正文 HTML 转 Markdown |
| `turndown-plugin-gfm` | 1.0.2 | MIT | `src/web/html.ts` | 表格、删除线转 Markdown |
| `@types/turndown`（开发） | 5.0.6 | MIT | 类型检查 | — |
| `@types/bun`、`typescript`（开发） | 见 package.json | MIT / Apache-2.0 | 类型检查 | — |

YAML 用 Bun 自带的 `Bun.YAML`，SQLite 用 `bun:sqlite`，Windows 接口（数据锁、DPAPI）用 `bun:ffi`，都不另加依赖。

## 验证

- `cd server && bun test`：整套。本地私聊切换相关：`backend-config`（读配置、配置文件的位置、什么时候进写模式）、`datalock`（local.lock）、`local-recover-scope`（启动收拾只动 console）、`trace-immediate`（共用的 trace.db 写事务一开始就拿写锁，另一个线程里的连接在读和写之间提交）、`run-file-secure`（先收 run/ 权限再写地址文件）。和 Go 对拍的几组：`turn-golden`（Go 的 44 份回合录像，请求逐份比）、`policy-golden`、`policy*`（审批）、`local-tools-golden`（edit 28、grep 20、find 10 条场景和 pi 的 edit 详情对拍 Go 的 `native/internal/tools/testdata`；todo_write 的规则）、`local-tools-everything` / `-gate` / `-shapes`（Everything 的选用和过滤、整条路的审批、路径写法）、`shared-turn`（输入语义）、`shared-rpc`（读方法）、`memory-recall`（召回 47/62 不许降）、`memory-write`（写入共用场景）、`web-*`（联网）。
- `cd server && bunx tsc --noEmit -p tsconfig.json`：类型检查。
- 合 main 前 `bun scripts/go-verify.ts` 会连 server 一起跑。
