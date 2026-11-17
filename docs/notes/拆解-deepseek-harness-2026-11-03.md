# 拆解 deepseek-harness：插件内核、请求重建与后台协作（2026-11-03）

## 阅读范围与证据口径

- [读码] 主体是本地 `_external\deepseek-harness` 的 DeepSeek Harness（dsh），根包版本 `0.2.1-rc.2`、MIT、ES module，Node 要求 `^22.19.0 || >=24.0.0`，pnpm `11.28.5`。本文以包版本固定来源，不填写未经核实的 Git 提交号。出处：`H/package.json:1-18`。
- [读码] 对照目录 `_external\dsh-superagent-prototype` 的包版本为 `0.1.0`，其 peerDependencies 面向 dsh `^0.2.1-alpha.2`，不能当作 rc.2 的内置模块。出处：`P/package.json:1-8`、`:57-86`。
- [文档] prototype 自称研究参考原型、不是 DeepSeek 正式产品，并明确说发布版本不等同内部版本或文章里的完整设计。出处：`P/README.md:1-7`。因此下文将它的行为单独列出。
- 依照主工作区 `docs/notes/拆解笔记-写法.md:1-49`：先写 dsh 自身，Sumika 对照只在末节；每项区分 `[读码]`、`[文档]`、`[推断]`。本次未运行 dsh、prototype、测试或模型请求，未联网，未读取 Sumika 的 `data`；没有 `[实测]` 数字。
- 引用缩写：`H/路径:行号` = `deepseek-harness@0.2.1-rc.2` 下的该路径；`P/路径:行号` = `dsh-superagent-prototype@0.1.0` 下的该路径；`S/路径:行号` = 当前 Sumika worktree 下的该路径。后文的单独 `:行号` 沿用紧前面的同一文件。
- 已读主路径：core 的 agent-loop / tools / system-prompt / session / scope，compaction，token-meter，两种 LLM 适配边界，规则和 skill 注入，文件读搜与 observation policy，MCP，审批和沙箱，subagent，headless / SDK，hooks，goal / workflow / schedule / team 的主要入口，以及 prototype 的 Engine / Runtime / Effects / Outbox / DshExecutor。未逐页拆 UI、未追完全部远程后端和原生沙箱实现；依赖 pi-ai 的底层缓存断点生成代码不在本次可读依赖目录中，相关结论止于 dsh 的适配边界。

## 1. 整体架构

### 1.1 产品入口、进程与模块

[读码] dsh 是 TypeScript / Node 的插件宿主。服务契约、具体 provider、模型可见 tool、UI 分在不同包：例如 `ctx.subagents` 是服务定义，spawn / fork / ACP 是 provider，`tool-subagent` 是模型入口；它们通过 Cordis 的 Context 服务和事件协作。`inject` 声明服务依赖，YAML 行顺序不负责加载顺序，服务可用性才决定激活。出处：`H/packages/subagent/subagent/src/index.ts:1-28`；`H/packages/bundle/base/cordis.patch.yml:1-13`。

| 层 | 实际结构与流程 | 证据 |
| --- | --- | --- |
| 启动与组合 | [读码] CLI 解析 profile、patch overlays，把后续参数交给 profile；base bundle 插入共享服务，web / headless 等继续覆盖 | `H/apps/cli/src/args.ts:1-15`、`:90-98`；`H/packages/bundle/base/cordis.patch.yml:1-13` |
| Agent 生命周期 | [读码] `ReactLoopAgent` 持有 Session、scope、inbox、单个活动驱动 promise；状态为 idle / maintenance / running，每会话不为每步新建线程 | `H/packages/core/agent-loop/src/agent.ts:42-52`、`:100-146`、`:211-239` |
| 持久事实 | [读码] append-only Session log 为真源，surface 是当前模型可见的有序投影；压缩替换 surface，不删除原日志 | `H/packages/core/session/src/surface.ts:1-8`、`:78-107` |
| Web | [读码] Vite / React 前端，build 结果由 `dsh web` 宿主提供；宿主侧通过 Typert 服务契约暴露能力 | `H/apps/web/package.json:1-4`、`:25-31`、`:39-71`；`H/packages/interaction/permission-presets/src/index.ts:184-208` |
| Desktop | [读码] Electron shell，复用 dsh runtime 与插件，Electron 依赖为 `^44.7.0` | `H/apps/desktop/package.json:1-8`、`:59-80` |
| SDK / ACP | [读码] 有独立 SDK JSON-RPC server、TS 客户端包和 Python SDK 目录；subagent provider 还包括 ACP、Codex、Claude Code、dsh SDK | `H/packages/sdk/server/src/server.ts:1-5`、`:103-161`；目录 `H/packages/sdk/`、`H/python/`、`H/packages/subagent/` |
| 无界面 | [读码] headless 直接创建或恢复 Agent，最终文字到 stdout，reasoning 到 stderr，`--json` 输出 NDJSON；不组合 Host / HTTP / browser | `H/packages/bundle/headless/src/index.ts:1-10`、`:42-56`、`:105-166` |
| TUI | [读码] 本次所查默认入口和客户端目录未发现随包交付的 TUI 实现；CLI 帮助里的 `dsh tui` 是 profile 示例，不能据此断言已有 TUI | `H/apps/cli/src/args.ts:90-98`；目录 `H/apps/`、`H/packages/client/`、`H/packages/bundle/` |

[读码] 并发 Agent 默认是同一宿主里的多个异步对象；外部工作由 subprocess、MCP stdio 或外部 subagent provider 承担。PTC 的 Node provider 可以每次独立 Node 进程，也可以选 `persistent-in-memory` 保留执行状态；它不是“每个 Agent 一个 worker”。`AsyncLocalStorage` 用于绑定执行上下文，不等于线程隔离。出处：`H/packages/ptc-runtime/ptc-runtime-node/src/index.ts:1-24`、`:35-69`、`:104-135`；`H/packages/mcp/mcp-client/src/transport.ts:1-43`；`H/packages/subagent/subagent/src/activation.ts:81-94`。

### 1.2 一轮的完整路径

[读码] 主链是：`followup` 持久入 next-turn → 唤醒唯一 driver → `turn/start` → claim 全部 next-step 加至多一条 next-turn → 组装 prompt → `agent/pre-step` → `step/start` → `agent/request` 决定路由 → `llm.prepareCall` 绑定适配器 → 同步提交 system / user / header / context → 从日志派生并冻结请求 → streaming → assistant settlement → 工具批次 → `step/end` → 有工具或插话则下一步，否则 `agent/turn-stopping` → `turn/end`。出处：`H/packages/core/agent-loop/src/agent.ts:151-176`、`:274-395`、`:398-448`、`:516-554`、`:562-705`。

[读码] 这里的“先准备，再准入”有明确边界：异步 assemble / request / prepare 期间取消，不把未准入的用户消息冒充为模型已经看过；模型请求的 route、adapter defaults、tools 和 messages 来自已记日志的状态。header 的原因分 initial / resume / change / series；工具集合变化还追加 developer 的 tool-addition / tool-removal。出处：`H/packages/core/agent-loop/src/agent.ts:398-448`、`:565-607`、`:624-705`。

[读码] 成功调用落 `assistant/message`，失败尝试落 `assistant/attempt`；两者都可以保存紧凑的带时间流记录。`max-tokens` 不执行可能被截断的工具调用，并作为整轮结果保留，即使后续步骤正常也不降成 completed。出处：`H/packages/core/agent-loop/src/agent.ts:338-342`、`:448-542`；`H/packages/core/agent-loop/src/assistant-stream.ts:59-63`、`:112-114`。

## 2. 工具系统

### 2.1 定义、注册、可见性与输出

[读码] `ToolDefinition` 不只有 input JSON Schema：还强制声明 canonical output 的 schema、`render(args, value)`，可附 UI `presentationMeta`；`execute` 返回可无损 JSON 化的值。模型内容、程序消费值、持久 UI 元数据有各自投影，不能把任意对象直接塞给模型。`projectContent` 在 post policy 之前安装执行时准备的富内容，`finalizeContent` 在最后做同步 content-only 不变量。出处：`H/packages/core/tools/src/index.ts:206-259`、`:1071-1095`。

[读码] 注册是 Cordis effect，返回 disposer；scope 内同名项可覆盖外层视图，`restrict({allow, deny})` 只允许 agent scope，且会在执行时约束实际可调用性。不是把 schema 隐藏后还允许直接调用。全局注册变化发 `tools/change`，下一步重新 assemble。guard 是独立的收紧层：任一 guard 可拒绝，不能替其他 guard 强制放行。出处：`H/packages/core/tools/src/index.ts:193-202`、`:1071-1152`；`H/packages/core/scope/src/index.ts:31-39`、`:133-146`。

[读码] 执行管线为 pre-execute waterfall → approval（若 ask）→ monotonic guards → execute waterfall / body → projectContent → post-execute → normalize / finalizeContent → frozen `tools/result` → durable `tool/result`。around 层可改取消 signal，调用身份仍受保护；同进程工具必须协作取消并收尾，registry 无法硬杀任意 JS。出处：`H/packages/core/tools/src/index.ts:141-190`、`:220-259`；`H/docs/tool-execution-pipeline.md:5-60`。[文档] 后一出处是仓库维护的流程图，用于说明管线全貌，不冒充本次运行证明。

### 2.2 并行：有界执行、串行边界、确定回填

[读码] `isConcurrencySafe(args)` 恰好返回 `true` 才是 parallel；没声明、工具未知或不可见、返回非 true、分类器抛错，一律 exclusive。默认并行上限 10。exclusive 形成屏障：读 A、读 B 可以重叠，随后写 C 等前面的组排空，后面的读 D 再开下一组；不会因一条写工具而把整批都降为串行。出处：`H/packages/core/tools/src/index.ts:1304-1321`；`H/packages/core/agent-loop/src/constants.ts:1-6`；`H/packages/core/agent-loop/src/tool-calls.ts:84-103`、`:205-242`。

关键判断（`H/packages/core/tools/src/index.ts:1311-1320`）：

```ts
executionMode(exec: ToolExecutionInput): ToolExecutionMode {
  const tool = this.resolveExecution(exec.name, exec.agent, exec.parent !== undefined)
  if (!tool?.isConcurrencySafe) return { kind: 'exclusive' }
  try {
    const concurrencySafe: unknown = tool.isConcurrencySafe(exec.arguments)
    return concurrencySafe === true ? { kind: 'parallel' } : { kind: 'exclusive' }
  } catch {
    return { kind: 'exclusive' }
  }
}
```

[读码] 调度器维护 `slots[]`、`callSeqs[]`、`inFlight: Map<index, Promise>`，`fillPool` 顺序 await prepare，因此审批 / pre 阶段按模型顺序，但已过门的 execute 可以同时进行。`Promise.race` 收到任一完成就补位；`commitReady` 只推进从头连续已完成的 slot，按模型顺序跑 finalize/post、记结果、投递 additionalContexts。后完成的前置调用会挡住后面结果提交，但不会阻止池中其他执行体完成。出处：`H/packages/core/agent-loop/src/tool-calls.ts:134-242`。

[读码] 每个尚未启动的后续调用会重新查 execution mode，工具注册或限制的改变能把它变成屏障。取消时停止补位、等待已经启动的调用收尾，尚未开始的工具补“aborted before dispatch”错误结果，维持 call/result 配对；调度器内部失败同样先 drain，再由 step 的恢复器补缺失结果。出处：`H/packages/core/agent-loop/src/tool-calls.ts:205-267`；`H/packages/core/agent-loop/src/agent.ts:328-359`。

[读码] 同批依赖没有自动数据流求解，也没有按参数解析 DAG：模型必须把依赖调用放到后续 step，或在 PTC / workflow 程序里显式 await。exclusive 只给执行顺序，不能让模型在同一 response 中预知前一结果。实际调度输入仅为已生成的 `ToolCallBlock[]` 和 safety classification。出处：`H/packages/core/agent-loop/src/tool-calls.ts:60-103`；`H/packages/workflow/tool-workflow/src/index.ts:159-171`。

### 2.3 动态工具、延迟 schema、PTC 与 MCP

[读码] native 和 ptc 是两种工具呈现模式，默认 native。ptc 模式只允许模型直接调用 `run_code`，其余能力通过根据当前 scope 生成的 TS / Python SDK 调用；直接点名其他 tool 会失败。SDK 文本按可见工具稳定排序生成，切回 native 的 scope 不带它。工具依然走同一 registry / policy，canonical value 可完整回给程序，只有程序打印和返回的内容进入外层结果。出处：`H/packages/core/tools/src/index.ts:57-65`、`:811-814`、`:864-957`；`H/packages/ptc-runtime/ptc-runtime-node/src/index.ts:44-48`。

[读码] `deferLoading` 是 tool schema 的正式字段，经 `schemaOf` 保留，DeepSeek serializer 写成 `defer_loading: true`。这证明具备“声明延迟加载”的协议能力；不能据此把全部本地工具说成默认只注入名字索引。generic pi-ai 适配器对“全 deferred 的 Anthropic 工具表”有 placeholder 处理。出处：`H/packages/core/tools/src/index.ts:1290-1301`；`H/packages/llm/llm-deepseek/src/serialize.ts:165-175`；`H/packages/llm/llm-pi-ai/src/adapter.ts:71-78`。

[读码] MCP client 支持 stdio 子进程与 Streamable HTTP。stdio 使用 scrubbed parent env 后显式叠加配置 env。工具发现是 `listTools` 后一代一代同步：拉取失败保留原代；替换注册出现冲突，回滚新代成“该 server 一个工具也没有”，不发布半套。公开名字保留 `(serverName, rawName)` 独立身份，通常为 `mcp__server__tool`；字符归一化或 64 字符截断时追加 12 个 SHA-256 十六进制字符，wire 仍传 rawName，不从公开名反解。出处：`H/packages/mcp/mcp-client/src/transport.ts:15-43`；`H/packages/mcp/mcp-client/src/tools.ts:27-53`、`:68-166`。

### 2.4 结果截断与回填

[读码] 第一层是每个工具自己的输出界限：read 的行 / 字节窗口、grep / glob 的内联结果和完整 spill。第二层 `spill-policy` 按估算 token 留头尾并给可恢复 locator，base 配为 12,500 token；省略 maxInlineTokens 则停用。它跳过普通 read；纯文本 PTC inner call 的大值完整给程序，只限制日志副本。第三层是在上下文压缩时对历史大工具结果剪中间，见第 3 节。出处：`H/packages/spill/spill-policy/src/index.ts:24-52`、`:94-175`；`H/packages/bundle/base/cordis.patch.yml:423-442`；`H/packages/fs/tool-fs-search/src/grep.ts:201-222`。

[读码] spill 不是任何失败都截掉：没有 owner、storage、图片定价或连提示 locator 都放不下时，logger 警告并保留原内联内容，避免假装能恢复却丢失正文。因此 12,500 不是无条件硬上限；read 本身则另有界限。出处：`H/packages/spill/spill-policy/src/index.ts:99-146`、`:149-175`。

## 3. 上下文管理、缓存与 token

### 3.1 日志与模型可见面分离

[读码] Session surface 的节点按 seq 引用日志事件，eligible 类型只有 system / developer / user / assistant / tool-result。replacement 节点遮蔽某个旧范围，原事件仍可回放。人类 transcript 应读取 append-origin 事件，不能用压缩后的 surface 代替“用户曾看过什么”。出处：`H/packages/core/session/src/surface.ts:22-45`、`:49-107`、`:121-159`。

[读码] header 保存 provider / model / call config / 工具表，request/context 保存该次 route 的 contextWindow 与 systemPromptUpdate。发请求时从 surface 派生 messages 并冻结；不另外维护一个可任意修改却不同步记日志的隐形对话数组。出处：`H/packages/core/agent-loop/src/agent.ts:624-705`。

### 3.2 自动压缩与摘要模型

[读码] base 默认挂 compaction-basic、token-meter、pruner、image-offload 和 `/compact`。自动检查发生在 `agent/pre-step`、请求派生之前；模型明确返回 `CONTEXT_WINDOW_EXCEEDED` 时另走恢复。配置可按精确 provider + model 覆盖，默认如下。出处：`H/packages/bundle/base/cordis.patch.yml:347-358`、`:434-448`；`H/packages/compaction/compaction-basic/src/index.ts:158-235`；`H/packages/compaction/compaction-basic/src/config.ts:19-23`、`:74-108`。

| 项 | 默认 / 算法 | 证据 |
| --- | --- | --- |
| 压力触发 | [读码] `floor(min(W × 0.8, W − O − headroom))`，W 为路由窗口，O 为输出预留 | `H/packages/compaction/compaction-basic/src/config.ts:147-194` |
| headroom | [读码] 65,536 token；小窗口不适用时必须改配置，不能假设任意模型可直接使用 | 同文件 `:75-77`、`:174-189` |
| 原文尾巴 | [读码] `floor((W − O) × 0.16)`，或显式 retainTokens；必须小于 threshold | 同文件 `:195-203` |
| 摘要输出 cap | [读码] 默认跟 headroom，为 65,536；可单独指定，包括 provider 算作输出的 reasoning | 同文件 `:75-77`、`:104`；`H/packages/compaction/compaction-basic/src/types.ts:23-28` |
| 再压次数 | [读码] compactionRetries 默认 1，即首次之后最多一次额外压缩 | `H/packages/compaction/compaction-basic/src/config.ts:104-106` |
| overflow 重试 | [读码] 默认 1；必须已经发生 durable surface replacement 才重试 | 同文件 `:106`；`H/packages/compaction/compaction-basic/src/index.ts:190-235` |
| 摘要模型 | [读码] 显式 summarization provider/model → 最新 request header 的 route → AgentOptions route | `H/packages/compaction/compaction-basic/src/summarizer.ts:125-144` |

[读码] 先用 meter 定价，从末尾反向保留 tail，切口往前移动到工具配对平衡处；压缩范围从首个非 system 节点开始，绝不遮蔽 surface[0] 的 system。不可分的一组不能凭 token 数截一半；system / tool schema 本身过大也不是历史压缩能解决的。出处：`H/packages/compaction/compaction-basic/src/region.ts:108-155`；`H/packages/compaction/compaction-basic/README.zh.md:243-246`。[文档] 后一处是作者记录的限制。

[读码] 压力满足后先执行无模型 pruner：单个工具结果超过 8,192 Unicode code points，保留头 4,096、尾 1,024，中间替换固定标记。它用 replacement 事件保留溯源，不原地改历史。剪枝后若压力已足够低则可免掉摘要；低于触发压力时不先偷偷改历史。出处：`H/packages/compaction/compaction-tool-result-pruner/src/config.ts:6-14`、`:23-29`；`H/packages/compaction/compaction-tool-result-pruner/src/index.ts:125-131`；`H/packages/compaction/compaction-basic/src/index.ts:273-300`、`:304-345`。

[读码] 摘要直接请求 LLM：原 system、工具表、被选历史前缀原样保留，末尾追加压缩指令，`purpose: 'compaction'`；只取安全文本，拒绝无正文、达到 max-token cap 等不完整结局。摘要外包 `<compacted-summary>`，作为 user-role checkpoint 替换旧范围。替换前还验证选中范围稳定，且带框架的摘要确实比原范围更小。出处：`H/packages/compaction/compaction-basic/src/summarizer.ts:74-91`、`:125-179`、`:188-208`；`H/packages/compaction/compaction-basic/src/region.ts:386-428`、`:443-455`。

[读码] overflow 恢复也先 prune，再用零尾巴保留预算挑一个可压范围；即使摘要后续失败，只要此前 prune 已落成 replacement 且未取消，仍有证据重试原请求。完全没推进 surface 就保留原失败。手动 `/compact` 则走 idle maintenance，运行中收到的用户输入在维护结束后处理。出处：`H/packages/compaction/compaction-basic/src/index.ts:202-235`、`:287-300`；`H/packages/core/agent-loop/src/agent.ts:181-204`；`H/packages/compaction/command-compact/README.md:32-50`、`:69`（手动命令用户行为为 `[文档]`）。

### 3.3 缓存前缀如何保持

[读码] 稳定次序由 registry 明确实现：prompt section 先按 order、再按 code-unit name；tool 默认按 name 排序，配置 toolOrder 时必须有 `<unlisted-tools>`，未列项也稳定排序。进程路径、审批与沙箱当前状态放动态 context，不放可复用 system 前部。动态 context 只在当前值变化时追加 user 快照，清空也写“先前快照已失效”。出处：`H/packages/core/system-prompt/src/index.ts:139-180`、`:205-250`；`H/packages/core/agent-loop/src/runtime-context.ts:146-162`；`H/packages/sandbox/sandbox-policy/src/index.ts:140-153`。

[读码] system 更新不是一律重写首条：adapter 声明 `systemPromptUpdate: 'in-history'` 且仍属连续 series 时，把变更后的 system 追加在历史尾部；不支持、series 断裂、system 清空时，才归并到头节点并清空后面的有效 system。工具同样记录 addition/removal，由路由能力决定如何投影。出处：`H/packages/core/agent-loop/src/runtime-context.ts:57-110`；`H/packages/core/agent-loop/src/agent.ts:411-424`、`:647-667`。

[读码] generic pi-ai 的能力判定区分：Anthropic 支持完整 in-history tool changes；OpenAI Completions 可配置 addition-only；Responses / Azure Responses / Codex Responses 依支持 additionalTools 或 toolSearch 声明 addition-only。不是把同一种更新法硬套所有服务商。出处：`H/packages/llm/llm-pi-ai/src/models.ts:70-93`。

[读码] 压缩辅助请求复用原前缀，fork 子代理保留父已完成轮次且默认不开放换 route，以争取 KV cache。这里是请求构造策略，不能等同于“命中率保证”。源码内有需要 key 的 cache e2e：检查首请求后的 cacheReadTokens 大于 0 和工具值最终到答复；本次只读了测试，没有执行，也不引用其结果为本地测量。出处：`H/packages/compaction/compaction-basic/src/summarizer.ts:113-160`；`H/packages/bundle/base/cordis.patch.yml:386-396`；`H/packages/core/agent-loop/tests/request-cache.e2e.ts:14-22`、`:73-112`。

### 3.4 cache_control 的位置与证据边界

- [读码] dsh 原生 DeepSeek Messages serializer 的完整 request body 组装中没有注入 `cache_control`，只发 system / messages / tools 等；没有依据把 Anthropic 的显式缓存标记套到这个 adapter。出处：`H/packages/llm/llm-deepseek/src/serialize.ts:62-176`。
- [读码] generic pi-ai adapter 透传 `cacheRetention`，配置枚举 none / short / long，并暴露 `cacheControlFormat`、`supportsLongCacheRetention`、`supportsCacheControlOnTools` 等 compat。出处：`H/packages/llm/llm-pi-ai/src/adapter.ts:130-148`；`H/packages/llm/llm-pi-ai/src/config.ts:276-280`、`:339-341`。
- [推断] 该路径的缓存标记最终由 pi-ai provider serializer 负责，dsh 本层没有选取“第几个消息块”的断点逻辑。本地所查 root / llm-pi-ai 的 pi-ai dependency 路径不可读；因此本笔记不声称已经核实它究竟标 system、最后一个 tool 还是最后一个 user block。这是依赖实现的未覆盖点，不是 dsh 没有缓存支持。

### 3.5 token 计数、缓存用量与成本

[读码] token-meter 统一提供压力、范围选择和 UI composition 的估算。文本为 4 characters/token，block overhead 4，role overhead 4；工具 schema 为 JSON 长度 / 4 加 framing。图片若 route 有 calculator，则按实际 request representation 重新定价。meter 从上次已成功请求的 provider usage 作 anchor，再加 surface signed delta；仅 canonical header 相同且 usage 不低于估算基线时复用这个 anchor，否则重新估算。`measure` 还复制当前 surface，所以一次读取是 O(surface)，不能说所有压力检查都是 O(1)。出处：`H/packages/llm/token-meter/src/estimate.ts:12-19`、`:41-61`、`:94-103`；`H/packages/llm/token-meter/src/index.ts:128-188`；`H/packages/llm/token-meter/src/route-pricing.ts:32-66`。

[读码] TokenUsage 的 inputTokens 是未命中输入，与 cacheReadTokens / cacheWriteTokens 不重叠；reasoningTokens 是 outputTokens 的子集。usage projection 用最终 assistant settlement 替换同一 attempt 的流式样本，retry-started 才开启新的计费 attempt，避免一轮流式与最终值重复加总。出处：`H/packages/llm/llm/src/types.ts:167-188`；`H/packages/llm/token-meter/src/usage-projection.ts:16-42`、`:52-82`；`H/packages/llm/token-meter/src/turn-usage.ts:82-155`。

[推断] 由这些 bucket 可计算缓存读取比例 `cacheRead / (input + cacheRead + cacheWrite)`；这只是统一口径的派生公式，不是本次找到并实测的 dsh 专用百分比指标。服务商未报告的字段应保留 unknown，不能把所有缺值当作确定的零。依据：`H/packages/llm/llm/src/types.ts:167-188`；`H/packages/llm/token-meter/src/turn-usage.ts:19-25`、`:103-110`。

[读码] 有 token 统计，没有统一货币花费汇总：pi-ai catalog 明说 harness 不消费 cost metadata、无 consumer 报 spend，历史 replay 所需 cost 字段归零；统一 TokenUsage 也没有金额字段。不要把 request image pricing 的“定价”误解成美元计价。出处：`H/packages/llm/llm-pi-ai/src/catalog.ts:33-38`；`H/packages/llm/llm-pi-ai/src/replay.ts:55-66`；`H/packages/llm/llm/src/types.ts:167-210`。

## 4. system、规则、skill 与记忆注入

### 4.1 system assembly

[读码] `PromptAssembly` 是 `{sections, contexts, tools, variables}`，不是一次拼完永不改变的大字符串。sections 支持静态文本或每次 assembly 计算的 provider、变量插值、`interpolate:false`；scope 层覆盖同名外层项。`complete` section 可以接管整个 system，assemble waterfall 后会还原成唯一完整段，避免别的 listener 悄悄补 system。出处：`H/packages/core/system-prompt/src/index.ts:20-35`、`:51-77`、`:121-137`、`:435-493`。

[读码] 内置 system identity、部署 persona prefix/suffix、plan、各工具指导、PTC SDK 都有固定 order slot；可见工具也带 scope-aware guidance，例如 read 不可见时对应提示段为空。动态工作目录、sandbox、approval 用 context 快照进入历史。出处：`H/packages/core/system-prompt/src/index.ts:139-180`、`:454-474`；`H/packages/fs/tool-fs/src/read.ts:70-77`；`H/packages/interaction/user-approval/src/index.ts:166-179`。

### 4.2 项目说明文件

[读码] 默认候选为 AGENTS.md / CLAUDE.md，local overlays 为 AGENTS.local.md / CLAUDE.local.md；默认 `.git` 为 project root marker。先读 dsh home 与 shared agents home 的全局说明，再按 projectRoot → cwd 祖先链收集，目录内不同候选都可贡献，trimmed content 相同的去重，不是永远只挑其中一种文件。base 给每批渲染 65,536 UTF-8 bytes，单源默认 1,048,576 bytes；后者超限直接忽略。出处：`H/packages/context/agent-instructions/src/config.ts:11-42`；`H/packages/context/agent-instructions/src/files.ts:83-102`、`:225-269`、`:321-355`；`H/packages/bundle/base/cordis.patch.yml:307-311`。

[读码] baseline 在首次请求的 pre-step 准入为带来源的 user context。成功 read / write / edit 的 file_path touch 会发现下层目录规则，并对已知规则的改动和删除作 reconciliation；工具结果先结算，touch 等 step/end 才排投影，下一步等待同 agent 已排的投影完成。失败结果或已取消执行不触发成功 touch。grep / glob 不在这条 FILE_TOUCH_TOOL_NAMES 集合内，不能声称“只要搜到路径就注入该目录全部规则”。出处：`H/packages/context/agent-instructions/src/index.ts:75-85`、`:128-154`、`:275-379`；`H/packages/context/agent-instructions/src/files.ts:270-286`。

### 4.3 skill 的索引、正文和显式调用

[读码] skill-filesystem provider 支持目录 bundle 与 flat Markdown、YAML frontmatter、project / custom / user / bundled roots。排名中 project dsh 为 100、project agents 为 200、custom 300、user dsh 400、user agents 500；目录 watcher 默认开启，稳定阈值 200ms、轮询 100ms，最多监控 128 个项目根。这里只列实现参数，不声称已测实际热更新延迟。出处：`H/packages/skill/skill-filesystem/src/index.ts:1-9`、`:35-43`、`:77-90`、`:191-231`。

[读码] 模型初见的是 durable skill catalog，含 name 与裁到最多 500 的规范化 description；只列模型可调用项。调用 `skill(name)` 才取完整正文和 resourceBase。catalog 与实际那一份 tool definition 的可见性绑定：同名工具被遮蔽，也不继承这份目录指导；snapshot incomplete 时不把暂时读不到误发布成“所有 skill 都删了”。出处：`H/packages/skill/tool-skill/src/index.ts:27-40`、`:72-84`、`:128-154`、`:211-249`。

[读码] 另有确定性用户入口：仅 `source.kind === 'user'` 的首行 `/<name>`，并且该 skill 允许 user invocation，才自动读取正文并追加 instructions context。`disable-model-invocation` 的 skill 不出现在模型目录中，但可由这个用户动作进入。外部文本无法靠伪造一段 slash 内容被这一分支当作用户点击。出处：`H/packages/skill/tool-skill/src/index.ts:163-205`。

### 4.4 记忆能力的范围

[读码] 在已查 base、context、compaction、session-query、skill 与 package 全目录中，没有找到“从会话自动抽取用户事实 → 跨会话召回注入”的内置记忆系统；存在的是规则文件、持久会话、session reference / query、摘要 checkpoint 和技能。base 的内容全文搜索默认 `openAt: never`，只是精确会话读取仍可用；这些不能合称为语义长期记忆。出处：`H/packages/bundle/base/cordis.patch.yml:156-170`、`:307-358`；目录 `H/packages/context/`、`H/packages/compaction/`、`H/packages/session-query/`、`H/packages/skill/`。

[读码] prototype 的 `core/memory.ts` 是 Map 实现的 Repository 测试替身，存 Trigger / Thread / Ticket / Decision 并提供事务回滚，名字不是 LLM memory。出处：`P/src/core/memory.ts:1-27`。是否由用户另装插件实现长期记忆，超出本文所读组合，不能据此否认生态可扩展性。

## 5. 工作区读取、搜索、二进制与快照

### 5.1 有界 read 与图片

[读码] read 是 `file_path + 1-based offset + limit`，默认及最大 2,000 行，单行最多 2,000 characters、选中行正文最多 50 KiB；10 MiB 及以上或 size 未知时走 streaming。renderer 的行缓冲只保留 maxLineLength + 1，但为了准确 totalLines，即便已取够窗口仍扫描剩余文件。因此内存有界不等于 I/O 只读请求的几十行。出处：`H/packages/fs/tool-fs/src/read.ts:14-32`、`:55-62`、`:143-158`；`H/packages/fs/tool-fs/src/read-render.ts:10-15`、`:106-146`。

[读码] provider 层要求 regular file、fatal UTF-8 解码，并检查前 8,192 bytes 内 NUL；二进制拒绝为 FS_NOT_TEXT，不把乱码混进 prompt。图片走单独 read_image，只在 attachment store 存在时注册，执行还查 route 的 image input 能力，字节通过附件引用持久化而不是塞进 Session JSONL 正文。出处：`H/packages/fs/fs-local/src/fsio.ts:17`、`:342-400`；`H/packages/fs/tool-fs/src/index.ts:83-90`；`H/packages/fs/tool-fs/src/read-image.ts:201-205`；`H/packages/bundle/base/cordis.patch.yml:148-153`。

### 5.2 grep / glob / LSP

[读码] grep 直接 spawn 打包的 `@vscode/ripgrep`，使用 argv 而非 shell、`rg --json --regexp=...`，路径放 `--` 后；解析 JSON 的 path / line_number / lines，避免冒号文件名破坏拆分。默认内联 250 match、每个预览 2,000 bytes，截断后尽量保存完整格式化结果及 locator，保存失败明确让模型缩小条件。无匹配是成功空结果。出处：`H/packages/fs/tool-fs-search/src/grep.ts:1-10`、`:24-35`、`:108-118`、`:137-166`、`:201-222`；`H/packages/fs/tool-fs-search/src/search-core.ts:200-204`。

[读码] glob 用 `rg --files --sort=modified --no-ignore --hidden`，排除 `.git/.svn/.hg/.bzr/.jj/.sl` 元数据，默认内联 100 个。可选择跨顶层条目轮询采样，避免一页被一个目录占满，但 base 显式设 `sampleOverCapGlobResults:false`；不能把可选采样写成默认行为。grep 的构造没有 `--no-ignore --hidden`，两者默认扫描范围不同。出处：`H/packages/fs/tool-fs-search/src/glob.ts:21-37`、`:80-106`、`:159-173`；`H/packages/bundle/base/cordis.patch.yml:301-305`；`H/packages/fs/tool-fs-search/src/grep.ts:113-118`。

[读码] 可选 LSP 有四个语义操作：definition、references、implementation、hover；契约使用 0-based UTF-16，模型入口处理 1-based 坐标转换，server 管文档与索引。它不等于 embedding repo search。本次搜 fs / workspace / lsp 的 semantic、embedding、vector index 并核对各入口，未发现内置向量代码索引；workspace 的 index 是 canonical cwd 到 session header 的索引。出处：`H/packages/lsp/lsp/src/types.ts:1-24`、`:38-46`；`H/packages/workspace/workspace/src/index.ts:164-168`、`:795-815`；`H/packages/lsp/lsp-stdio/src/index.ts:279-285`。

### 5.3 先读再改、工作区变更与撤销边界

[读码] `fs-observation-policy` 用 `WeakMap<session, Map<targetKey, present(version)|absent>>` 记该 owner 看过什么。edit 未观察时报 FS_NOT_OBSERVED，确认不存在时报 FS_NOT_FOUND；write 未见过只能 createIfAbsent，见过才 replaceIfVersion；最终 freshness / no-clobber 由 provider 原子检查。未装这个 policy 时工具允许 provider 的无条件 mutation，所以这是组合能力，不是所有裸 fs provider 的天然承诺。出处：`H/packages/fs/fs-observation-policy/src/index.ts:1-7`、`:21-99`、`:114-135`。

[读码] 有实验性 worktree service：新 branch + checkout，成功后切 session cwd；默认池 `.agents/worktrees`，60 秒命令上限、3 秒 termination grace、65,536 bytes 输出上限。原工作区未提交文件不自动搬过去，失败保留已建工件供检查，不自动删 branch / worktree。出处：`H/packages/experimental/worktree/src/index.ts:54-65`、`:92-104`、`:134-180`。

[读码] 所查 core/session、session-controller 和文件工具没有统一“恢复本轮前文件树”的 undo/rewind API。找到的是 session fork、压缩 surface replacement、持久化 checkpoint、版本新鲜度 guard、worktree 隔离；不是文件系统快照。查找范围包括 `H/packages/api/session-controller/src/*.ts`、`H/packages/core/session/src/*.ts`、`H/packages/fs/tool-str-replace-editor/src/index.ts` 中的 undo / rewind / revert / checkpoint，以及 worktree 实现。能确定的分叉入口：`H/packages/api/session-controller/src/index.ts:442-452`、`H/packages/core/session/src/index.ts:1380`；checkpoint 的实际含义见 8.4。此处“未找到”限于所查公开接口，不扩大为整个生态绝不支持。

## 6. advisor / 第二意见

[读码] 所查 core tools、subagent、experimental 包目录与源码中没有专名 advisor / second-opinion 的独立工具。可以用 `subagent` 选另一个 provider/model，或用 `subagent_fork` 让它拿父会话已完成轮次作 review；spawn 要自包含任务，fork 看不到当前尚未完成这一轮。默认 fork 不暴露模型选择，以保留同 route 的缓存前缀。出处：`H/packages/subagent/tool-subagent/src/index.ts:111-142`、`:211-258`；`H/packages/subagent/subagent-fork-in-process/src/index.ts:38-78`；`H/packages/bundle/base/cordis.patch.yml:386-396`。

[读码] 找到一个真实的第二次模型判断机制：实验性 `auto-review`。选 current-session-only Auto permission preset 后，每个 native call 与每个启动的 PTC inner call，在工具 body 前请求一次授权审查；outer run_code transport 不审两次。它用当前 request header 的 provider/model，不保证是另一家或更强模型；不是代码 diff advisor。出处：`H/packages/experimental/auto-review/src/index.ts:1-6`、`:363-375`、`:616-637`、`:683-731`。

[读码] reviewer 输入是冻结快照：固定 REVIEW_POLICY + cwd + project instructions + retained history + pending action（tool 描述、schema、参数）。history 把 human-instruction、direct-parent-instruction、constraint、checkpoint、fact 分开，历史工具调用能证明事实但不能自行升级授权。不是把整个 raw assistant/tool-output transcript 无差别当命令；project instructions 只作 constraint。出处：`H/packages/experimental/auto-review/src/index.ts:54-60`、`:71-113`、`:363-462`、`:470-500`。

[读码] 输出协议严格限定 low/medium allow、medium/high deny，可选 reason 仅限 deny。解析拒绝重复 JSON member、不合法对象和异常流结尾。reviewer 异常则工具拒绝；deny 在 approval policy=never 时最终拒绝，在 ask 时交给用户。审核请求 temperature=0，不带 sessionId、也不进入 Session log；不能指望普通会话 token fold 自动记录这笔辅助调用成本。出处：`H/packages/experimental/auto-review/src/index.ts:64-68`、`:561-611`、`:616-677`、`:710-725`。

[读码] Auto 的权限组合是 danger-full-access + ask，靠 LLM review 作 gate。卸载 auto-review 时，它会把仍处在 Auto 的会话切到 danger-full-access，再取消、等待在途 review。这一生命周期选择确有实现，不能把它描述成“卸载只会收紧到只读”。出处：`H/packages/interaction/permission-presets/src/index.ts:84-92`；`H/packages/experimental/auto-review/src/index.ts:734-751`。[推断] 对要求强失效收紧的宿主，这是需要单独评估的风险点，不能直接照搬权限预设。

## 7. 打断、插话、排队

### 7.1 三个输入 API 的区别

| API | 放在哪 | 是否唤醒 | 处理时机 / 状态 | 证据 |
| --- | --- | --- | --- | --- |
| followup | [读码] next-turn | 是 | 每轮最多 claim 一个普通 prompt；前一轮完整收尾再开下一轮 | `H/packages/core/agent-loop/src/agent.ts:151-166`；`H/packages/core/agent-loop/src/inbox.ts:106-120` |
| steer | [读码] next-step | 是 | 当前 step（模型 + 工具批）结束后认领；空闲时也可唤醒一轮 | `H/packages/core/agent-loop/src/agent.ts:168-170`、`:360-372` |
| inject | [读码] next-step | 否 | 留给后续步骤；单纯注入不令空闲 Agent 自行开口 | 同文件 `:172-174` |
| cancel | [读码] AbortController | 不适用 | 默认清两条 inbox，可用 keepInbox 保留尚未认领项；停止不撤销已发生副作用 | 同文件 `:176-180`；`H/packages/core/agent-loop/src/tool-calls.ts:239-267` |

[读码] inbox 的 next-turn / next-step 都通过 `agent/inbox/spliced` 写入 Session log，以 projection 重建；有 prepend、replace、remove、clear，消息 ID 跨两条队列唯一。认领顺序是全部 next-step，再一条 next-turn；`claimed` 与后来的 `user/message` 准入是两个事件，不能把“收下了”当作“模型已读”。出处：`H/packages/core/agent-loop/src/inbox.ts:20-65`、`:106-120`、`:124-156`、`:209-257`。

[读码] 用户在取消后的收敛期间再发可唤醒输入，会被归到 next-turn，并通过 wakeRequested latch 等旧活动收尾后唤醒；maintenance 时也只锁存唤醒，不开第二个 driver。`whenIdle` 不是仅 await 一次旧 promise，它循环等 activityDone 不再变化。出处：`H/packages/core/agent-loop/src/agent.ts:151-159`、`:181-239`、`:260-272`。

### 7.2 停止时具体保留什么

[读码] 生成中取消：若已形成可保留的 interruptedBlocks，记带 interrupted=true 的 assistant/message；否则记 assistant/attempt。未完成 tool call 不拿来执行。工具执行中取消：新调用不再补位，已启动调用等待收尾，未执行项合成错误配对。完整 step/end 和 turn/end 在 finally 里落下。出处：`H/packages/core/agent-loop/src/agent.ts:343-359`、`:374-395`、`:461-504`；`H/packages/core/agent-loop/src/tool-calls.ts:239-267`。

[读码] 后台 jobs 不挤进 next-turn prompt FIFO：jobs-local 以 owner 桶限额，超额 start 直接拒绝，不是无限排队；子代理 activation 也先抢容量，失败返回 limit reached。消息排队、后台输出环、子代理容量是三个不同机制。出处：`H/packages/jobs/jobs-local/src/index.ts:31-61`、`:205-225`；`H/packages/subagent/subagent/src/activation.ts:15-35`。prototype 才另外提供 durable effect queue，见第 9 节。

## 8. 子代理、计划、权限、持久化与可观测性

### 8.1 子代理与并行任务

[读码] `subagent` 立即返回 child ID，runtime 后续通知完成；可续接 provider 用 send_message 报告、steer 或冷恢复。同进程 spawn 是新上下文，fork 则固定复制父最后一个 turn/end 之前的平衡前缀；冷恢复不会再复制父的新历史。provider capabilities 分别声明 agentOptions、agentPreset、outputSchema、depthLimit，不支持所需能力会挂载失败。出处：`H/packages/subagent/tool-subagent/src/index.ts:59-77`、`:159-187`、`:220-231`；`H/packages/subagent/subagent-fork-in-process/src/index.ts:38-78`。

[读码] 管理器维护 resident map、每 child 的 promise-chain lock、祖先身份 WeakSet、共享 ActivationPool、ownedChildren。child 至多一个 live activation；输入接收、关闭与冷恢复按 child 串行；slot 在构造前预留，清理后释放；父清理会先收子图，不能只 cancel 父模型流就遗留后台子任务。外部 provider 的 run handle 和本地可恢复 Session 是不同 resource union。出处：`H/packages/subagent/subagent/src/activation.ts:15-94`、`:167-188`；`H/packages/subagent/subagent/src/manager.ts:137-180`、`:188-240`。

[读码] 多模型委派有 schema 和 settings policy，支持 provider/model 成对、effort 兼容检查、列出可用 child models。最大深度省略时读 Host setting（注释给默认 1），并非无限递归；外部 provider 可用 provider-managed 明确让其自行管深度。出处：`H/packages/subagent/tool-subagent/src/index.ts:59-101`、`:211-258`。

[读码] 更大批量编排有 workflow：模型写 JS script，`agent` 启动并等待子代理，`pipeline` 各 item 可跨 stage 无全局屏障，`parallel` 才等待整组；返回可校验的结构化输出。run_in_background 可注册成 owner job 并立即给 ID。此工具的执行接口没有 filesystem / network / timer / Node API，实际工作交给子代理。出处：`H/packages/workflow/tool-workflow/src/index.ts:1-12`、`:43-62`、`:159-171`。

### 8.2 todo、plan 与 Team task 的区别

- [读码] todo_write 是全量替换的 `{content,status}` 列表，状态 pending / in_progress / completed，无 id / priority；空文、重复 content 拒绝。base 允许多个 in_progress。关键细节：虽然事件持久化，当前 `todos` projection 会在下一次 turn/start 清空，因此不是跨每轮一直保留的长期任务数据库。出处：`H/packages/todo/tool-todo/src/types.ts:12-31`；`H/packages/todo/tool-todo/src/index.ts:73-101`、`:121-138`；`H/packages/bundle/base/cordis.patch.yml:449-452`。
- [读码] plan mode 是持久协作状态：active / wanted / pending，用户选择直到下一次被接受的 pre-step 才生效。exit_plan_mode 提交完整计划供批准或继续规划；退出工具一直注册，减少工具表变动。plan mode 不修改 sandbox 或 approval，只靠 guidance 约束计划阶段，不能当作写保护边界。出处：`H/packages/plan/plan-mode/src/index.ts:1-18`、`:65-90`、`:137-170`、`:197-216`。
- [读码] 实验性 Team 有共享任务 DAG、owner、revision、writeScopes；claim 只有 pending 且 blockers 满足时可做，更新带 expectedRevision，缺失 / 重复 / 环形依赖报专门错误。write scopes 是 advisory，没有目录锁；队员共享同一 cwd/filesystem，shell、formatter、generator 不由 fs observation guard 全面保护。出处：`H/packages/experimental/agent-team/src/task-board.ts:21-24`、`:44-68`、`:108-179`；`H/packages/experimental/tool-agent-team/src/index.ts:35-39`。

### 8.3 审批、权限与沙箱

[读码] approval 只认本次 allowed-once / rejected / cancelled / unavailable。policy=ask 委派给 answerer，没有 answerer 时 unavailable 并拒绝；policy=never 是拒绝所有 ask，不是把所有操作放行。撤销 signal 让 pending question 取消，迟到答案丢弃；request/outcome 审计属于请求 Agent 的 Session。出处：`H/packages/interaction/user-approval/src/index.ts:54-79`、`:114-141`、`:154-179`。

[读码] sandbox policy 分 read-only / workspace-write / danger-full-access，共享解析入口供 filesystem、shell、terminal 读取。service 默认 read-only，但 shipped base 显式覆成 workspace-write，fresh session 配 ask；不能把 service 安全默认误写成产品默认。显式批准的 mode > session override > deployment default，workspace 边界来自 session cwd。出处：`H/packages/sandbox/sandbox-policy/src/index.ts:68-87`、`:115-120`、`:155-178`；`H/packages/bundle/base/cordis.patch.yml:237-250`；`H/packages/interaction/permission-presets/src/index.ts:187-204`。

[读码] 本地 runner 按平台：Linux bwrap 优先、失败探测 Landlock；macOS Seatbelt；Windows restricted token + ACL。provider 明确把 Windows 标 partial：读访问不受这条写沙箱限制，hard link 可能别名到 workspace 外的同一 file object，其他 AppContainer 的 ACL 目录可不可读也有限制。runner 缺失或不可用不退回裸 argv。这里没有读取到可据以宣称“跨平台完整网络隔离”的保证。出处：`H/packages/sandbox/sandbox-local/src/index.ts:1-21`、`:153-193`。

[推断] 最重要的权限区分是“模型看得到什么工具”“registry 接不接受这个调用”“provider 在 OS 层能造成什么副作用”三层。dsh 都有对应接口，但只有正确 composition 才共同成立；plan guidance、fs observation、新鲜度 guard、sandbox 不能互相替代。依据：`H/packages/core/tools/src/index.ts:1105-1152`；`H/packages/fs/fs-observation-policy/src/index.ts:1-7`；`H/packages/plan/plan-mode/src/index.ts:1-18`；`H/packages/sandbox/sandbox-policy/src/index.ts:155-178`。

### 8.4 持久化、恢复与 checkpoint

[读码] JSONL backend 有 per-handle mutation promise chain、live write-behind buffer（最长主动延迟 200ms）、单 writer、cross-process lease、torn-tail 修复。它返回物理有效日志；恢复时语义闭合在 Agent factory 层补 missing tool results、step/end、turn/end，不能由存储偷偷决定工具已成功。出处：`H/packages/session/session-persistence-jsonl/src/storage.ts:1-9`、`:35-82`、`:84-104`；`H/packages/core/agent-loop/src/index.ts:883-887`。

[读码] session-checkpoint-policy 的 checkpoint 是 `sessions.flush`：模型发出前刷完整 logged prefix，top-level 工具 body 前刷 tool/call，下一 pre-step 刷前一步 response/result。flush 失败就不派模型或工具副作用。nested dispatch 复用已落盘 outer call，不逐次假装独立顶层事务。它不备份或撤销工作区文件。出处：`H/packages/session/session-checkpoint-policy/src/index.ts:20-38`、`:51-84`。

关键边界（`H/packages/session/session-checkpoint-policy/src/index.ts:69-74`）：

```ts
ctx.on('tools/execute', async (exec, next): Promise<ToolExecutionResult> => {
  if (exec.agent === undefined || exec.parent !== undefined) return next()
  await ctx.sessions.flush(exec.agent.session)
  if (exec.signal.aborted) return abortedBeforeDispatchResult()
  return next()
})
```

[读码] fork 提供独立会话续走，日志保留 parent lineage；持久化投影缓存用于降低列表和状态重建成本，base 每 200 events 或 5 秒 write-behind；这与 LLM prompt cache 不是同一种缓存。出处：`H/packages/core/session/src/index.ts:1380`；`H/packages/bundle/base/cordis.patch.yml:198-214`；`H/packages/subagent/subagent-fork-in-process/src/index.ts:38-78`。

### 8.5 hooks、无界面与模型失败

[读码] 原生扩展点是 typed Cordis waterfalls / events。另有实验性 Claude Code command hooks bridge：SessionStart、prompt/tool pre/post、Stop、subagent start/stop；updatedInput 只记日志警告，不实际改参数。Codex bridge 支持五类 points、regex matcher、snake_case payload，只接受 blocking decision，没有 pre-tool approval 或 rewrite。两者 configPath 在进程加载时读一次，没有每 session 自动发现 hooks.json。出处：`H/packages/experimental/hooks-claude-code/src/index.ts:1-8`、`:51-75`、`:108-137`；`H/packages/experimental/hooks-codex/src/index.ts:1-8`、`:48-63`、`:128-150`。

[读码] headless 输出 final text 或 JSON stream，退出前 flush 和 dispose；恢复 `--session-id` 时拒绝缺失 session、不受该 bundle 支持的 preset、子代理/分叉 session，避免换一个工具组合假装无损恢复。SDK server 另以 JSON-RPC `session.event/status`、subagent.started/finished 通知给外部程序，不要求浏览器常开。出处：`H/packages/bundle/headless/src/index.ts:1-10`、`:171-220`；`H/packages/sdk/server/src/server.ts:103-161`。

[读码] 多模型路由由 adapter registry 和 `agent/request` waterfall 管；prepareCall 把一次请求的 route / adapter config snapshot 冻结，设置变化下一步生效。pi-ai 内层 maxRetries=0，agent 可观察的 retry policy 才拥有尝试次数。默认重试执行器按 provider policy 计算指数退避 / jitter，尊重合法 Retry-After，写 llm/retry、等待后写 retry-started；支持 normal 和 always。出处：`H/packages/core/agent-loop/src/agent.ts:565-607`；`H/packages/llm/llm-pi-ai/src/adapter.ts:3-14`、`:130-148`；`H/packages/llm/llm-retry/src/index.ts:59-81`、`:153-269`。

[读码] 在所查 llm-retry、LlmRuntime、default-model 和 loop 的失败路径里，未找到自动按“便宜模型 → 强模型”或服务商 fallback chain 逐级切换的内建配置。存在可扩展 recovery / routing seam、压缩与图片 offload 后重试，但不能把“可用 plugin 写回退”说成默认已经实现跨模型故障切换。证据：`H/packages/llm/llm-retry/src/index.ts:194-261`；`H/packages/core/agent-loop/src/agent.ts:503-523`、`:565-607`；`H/packages/llm/llm/src/index.ts:64-71`。

### 8.6 jobs、用量与日志

[读码] jobs-local 是纯进程内 registry，生命周期 running / stopping / completed / killed / failed；默认每 exact owner 的 active jobs 上限 10、live ring 256 KiB、terminal ring 16 KiB、pull poll 150ms。未被模型消费的 terminal output 额外保留到首次 terminal read，modelCursor 与 UI readAt 分开。服务或 Agent dispose 取消并等 compliant producer，不能宣称断电后还能恢复进程。出处：`H/packages/jobs/jobs-local/src/index.ts:1-10`、`:31-61`、`:77-126`、`:128-130`。

[读码] 观察面包括 durable request header / context、assistant message / attempt 中的 stream、tool call/result、approval、hook invoked/result、retry 事件；sessionStats 统计 turns/steps、LLM 和 tool wall time、TTFT、decode tokens/time。toolMs 是每调用耗时相加，并行时不等于用户等待墙钟；中断 step 的部分时间不全部记入这些 figure，代码注释明确其口径。出处：`H/packages/session/session-stats/src/projection.ts:1-24`、`:32-49`、`:133-186`；`H/packages/core/agent-loop/src/agent.ts:448-540`。

[读码] OpenTelemetry backend 默认 FEEDBACK_ONLY，仅当前 Session 显式反馈事件授权采集；fork 继承的 feedback 不授权上传，DISABLED 不建 SDK pipeline。base 的 exporter 会批量传日志，有 3 秒外层 shutdown budget、单请求 1 秒 timeout / 1.5 秒 watchdog，完整历史未必在关机 deadline 内全部送完；本次没有连接 endpoint。出处：`H/packages/session/session-telemetry-otel/src/index.ts:30-62`、`:139-166`；`H/packages/bundle/base/cordis.patch.yml:216-235`。

## 9. 额外通用能力与 prototype 对照

### 9.1 dsh 本体值得加入横向比较的能力

| 能力 | 有没有、怎么做 | 出处 |
| --- | --- | --- |
| 持久目标自动续轮 | [读码] goal-round-driver 用 goalId / revision / round 和 queued / claimed / admitted 状态管理自动续轮；等 idle、无竞争用户输入、子代理已静止、durability checkpoint 完成才预约下一轮；有最大轮数 | `H/packages/goal/goal-round-driver/src/index.ts:22-49`、`:103-121`、`:148-180` |
| 定时提醒 | [读码] Host-wide schedule，支持 after / at / every / daily / weekly / cron，通过 storage domain 持久化；delivery history 默认 30 天且最多 200 条，读取和修改定时本身不激活会话 | `H/packages/schedule/schedule/src/index.ts:14-19`、`:72-108`、`:128-164` |
| 结构化子任务输出 | [读码] provider capabilities 明示 outputSchema，workflow 的 agent(schema) 返回验证对象；缺失结果和普通错误有分支，不全靠解析自然语言 | `H/packages/subagent/subagent-fork-in-process/src/index.ts:59-68`；`H/packages/workflow/tool-workflow/src/index.ts:159-171` |
| 多种调用前端共用状态 | [读码] UI、SDK、headless 从同一 Session 事件与 projection 派生，不要求模型输出兼作 UI state transport | `H/packages/sdk/server/src/server.ts:103-161`；`H/packages/bundle/headless/src/index.ts:74-103`；`H/packages/todo/tool-todo/src/types.ts:28-31` |
| 原生 reasoning replay | [读码] DeepSeek serializer 保留 thinking signature，pi-ai adapter 维护最小 replay envelope；route / model 不兼容时可诊断 degradation，不把所有 reasoning 都降成普通 text | `H/packages/llm/llm-deepseek/src/serialize.ts:26-38`；`H/packages/llm/llm-pi-ai/src/replay.ts:37-40`、`:68-75`；`H/packages/llm/llm-pi-ai/src/adapter.ts:112-115` |
| 运行时组合热变更 | [读码] profile config 默认可重载；工具与 prompt 注册带 effect disposer，调用路由绑定 snapshot，让 HMR 与正在执行的请求有明确边界 | `H/packages/bundle/base/cordis.patch.yml:28-34`；`H/packages/core/tools/src/index.ts:1071-1095`；`H/packages/llm/llm-pi-ai/src/adapter.ts:3-14` |
| 远程执行能力的接口分离 | [读码] 存在 SSH / E2B 的 fs 与 subprocess provider、ACP/CLI 子代理适配器，核心 loop 不按不同平台各写一版 | 目录 `H/packages/ssh/`、`H/packages/e2b/`、`H/packages/subagent/`；本次只核对模块结构，未验证各 provider 的传输与权限实现 |

### 9.2 prototype 增加的是长期主动工作的领域层

[读码] prototype 不是替换 dsh 的 ReAct loop，而是在它上面增加 Sources → Trigger → Thread/Evaluation → Decision/Mandate → Ticket → Assignment/worker Session → Reply/Outbox。`Engine` 是同步事务入口，角色只得到相应 Pick 接口；副作用通过 ports 写 durable queue，离开事务再执行。每个 thread 最多一个 evaluation，期间新 triggers 留在 pending，下一批一起评估。出处：`P/src/core/engine.ts:18-46`、`:72-88`；`P/src/core/model.ts:23-41`、`:143-184`；`P/src/app/runtime.ts:53-63`、`:120-168`。

[读码] `Mandate` 是接受决定产生的不可变授权，携带 explicit / inferred、policyVersion、basis、actor、scope。accept mode 有 off / observe / auto；observe 可以记录 would_accept 而不真正开工作。worker 对 proactive inferred 工作收到更窄指令：小范围内部改动，遇接口、行为或产品决策扩大就停并报告。context-only 的 thread subscription 只提供资料，不能当新请求授权。出处：`P/src/core/model.ts:101-141`、`:170-175`；`P/src/executor/dsh.ts:89-114`、`:150-163`。

[读码] Effects 是 SQLite `(kind,key)` 幂等 upsert 队列，seq 保持排位，generation 随 payload 更新递增；执行完成只能删同 seq+generation 的行，避免旧 attempt 抹掉新任务。per-kind handler 可设并发、ordered、retry；默认初始退避 2 秒、最大 60 秒，Later 只延迟不记失败，drain 停止新执行并保留待跑行。出处：`P/src/app/effects.ts:25-40`、`:43-86`、`:105-159`。

[读码] worker 每 ticket 独立持久 Session 和 workspace directory；WorkerSlots 同时计运行中、启动预留、模型轮次结束后还运行的 jobs/subagents。reserve 在 await 之前同步占位，人的 follow-up 允许额外一个 slot，避免新任务把人的回应长期挡住。transient launch/turn 有 3 次 retry、15 秒初始 backoff，background 超 30 分钟会收尾；工作区 prepare 命令失败不盲目当作已准备成功重试。出处：`P/src/executor/dsh.ts:1-33`、`:38-57`、`:163-203`。

[读码] Outbox 在同一事务记 receipt 和 reply effect，默认全局逐条发送，按同 ticket / conversation 的前序 pending 控顺序；暂停、source unavailable 等待，目标 ignored / gone 则终止，不永远重发。重试区间 5 秒到 300 秒。receipt 能阻止本地重复 enqueue，但本次没有读到足够证据宣称“外部发送成功后、receipt 落库前崩溃仍恰好一次”。出处：`P/src/app/outbox.ts:13-50`。[推断] 横向比较应写可靠 outbox / 重试与回执，不写未经证明的 exactly-once 外发。

[读码] Runtime 启动先锁状态目录、再开 `orchestrator.sqlite`，锁 stale 30 秒；有 health board、setup gates、pause、主 assessor Session、worker sessions、source registry。默认不是任何远程消息都直接进入执行，而先经过 source / policy / decision。出处：`P/src/app/runtime.ts:92-120`、`:140-168`、`:183-211`。

## 10. 这一家做得突出的设计

以下“好”是基于实现结构的判断，不表示已与所有竞品跑过性能对比；每条保留收益与边界。

1. [读码；评价为推断] **请求从事件日志重建，并把准入与模型执行分开。** header、surface、attempt、tool/result 都有持久身份；一次失败或重试可以解释“模型究竟读了哪些输入”，比 UI 消息数组与真实 request 各自漂移更容易审计。收益依赖插件遵守记日志契约，checkpoint 也不提供外部副作用回滚。出处：`H/packages/core/agent-loop/src/agent.ts:398-448`、`:624-705`；`H/packages/core/session/src/surface.ts:78-107`。
2. [读码；评价为推断] **并行体与有序政策/结果分开。** rolling pool + exclusive barrier 让混合读写批不必整批串行，又保留审查和回填的模型顺序；取消 drain 与合成结果维持 transcript 配对。代价是排头慢调用可能延迟后续结果可见。出处：`H/packages/core/agent-loop/src/tool-calls.ts:134-267`。
3. [读码；评价为推断] **把缓存前缀稳定性做到协议能力层。** 稳定排序、变化才追加的 context、in-history system/tool updates、同路由 fork、原前缀摘要互相配合，避免只在 prompt 上写“请利用缓存”。代价是不同 route 能力不一样，换模型或 surface replacement 仍会断缓存。出处：`H/packages/core/system-prompt/src/index.ts:205-250`；`H/packages/core/agent-loop/src/runtime-context.ts:88-110`、`:152-162`；`H/packages/llm/llm-pi-ai/src/models.ts:75-93`；`H/packages/compaction/compaction-basic/src/summarizer.ts:125-160`。
4. [读码；评价为推断] **同一个工具同时有结构化值、模型投影和回放 UI 投影。** PTC 可处理完整结果，仅打印摘要给模型；普通 native 调用也能按 token spill 并给真实 locator，UI 不必反解析模型文本重建卡片。边界是 spill 失败时保留全文，因此预算不是硬隔离。出处：`H/packages/core/tools/src/index.ts:206-259`；`H/packages/fs/tool-fs/src/read.ts:112-137`；`H/packages/spill/spill-policy/src/index.ts:94-175`。
5. [读码；评价为推断] **两种 durable inbox 明确区分后续任务和中途引导。** splice 使改排队消息、取消、claim、恢复有同一来源；外部宿主能分清 accepted / claimed / admitted，取消后再来的输入有 latch，而非靠 UI 猜 busy flag。出处：`H/packages/core/agent-loop/src/inbox.ts:20-65`、`:106-156`、`:209-257`；`H/packages/core/agent-loop/src/agent.ts:151-239`。
6. [读码；评价为推断] **会话级文件 observation + provider 原子版本检查。** 防止模型基于旧读覆盖用户或其他 agent 的新写，scope 的 owner 信息不从模型参数取；跨进程 freshness 由 provider 处理。边界是 shell 或 generator 不自动享受这条 guard。出处：`H/packages/fs/fs-observation-policy/src/index.ts:21-99`；`H/packages/experimental/tool-agent-team/src/index.ts:35-39`。
7. [读码；评价为推断] **子代理的持久身份与活动期资源拆开。** durable child Session 可恢复，Activation 是当前驻留 epoch；ChildLock、pool、ancestry、child-first drain 同时控制并发与关闭，不把“模型回答完”误当成“整个任务图静止”。出处：`H/packages/subagent/subagent/src/activation.ts:15-94`、`:167-188`；`H/packages/subagent/subagent/src/manager.ts:137-240`。
8. [读码；评价为推断；仅 prototype] **业务状态与异步副作用用事务性队列连接。** Mandate 保留授权由来，Effects 的 generation 防旧完成误删新 payload，Outbox receipt 与调度状态一起落库，适合常驻主动 worker；它是上层领域设计，不应强加给每次普通聊天。出处：`P/src/core/model.ts:101-141`；`P/src/app/effects.ts:43-86`、`:139-159`；`P/src/app/outbox.ts:21-45`。

## 给 Sumika 的启发

1. [读码；建议为推断] **借“屏障 + 有界 rolling pool”，保留既有听众和审批边界。** Sumika 当前 `runTools` 只要批内有一项 sequential 就整批串行，否则先全部 prepare 再开 goroutine，并按调用顺序记结果；dsh 的局部屏障与上限能减少只读任务等待和一次性并发峰值。对应：`S/native/internal/turn/loop.go:549-572`、`:633-732`；声明在 `S/native/internal/policy/policy.go:17-25`、`:64-88`；dsh 参考 `H/packages/core/agent-loop/src/tool-calls.ts:134-242`。若实现，验收应覆盖 read/read/write/read、审批期间取消、后完成先返回、插件停用后未启动调用，不能为了并行绕过 Gate。
2. [读码；建议为推断] **借输入的 durable lifecycle，先统一 accepted / claimed / admitted / dropped 的事实。** Sumika `Steering` 目前是 mutex + 内存 slice，Push 发 accepted，take 后才 emit dropped；dsh 的队列事件能解释进程退出前收下却没进模型的插话。对应：`S/native/internal/turn/steer.go:13-78`；dsh 参考 `H/packages/core/agent-loop/src/inbox.ts:106-120`、`:209-257`。建议先用于本地主窗口的排队/重连；本地群、QQ 私聊、QQ 群仍按各自入口决定如何入轮，不能凭持久输入把外部文字提为主人授权。
3. [读码；建议为推断] **借统一 token meter 与“压缩必须产生可证明缩减才重试”，保持 Sumika 摘要的可见性过滤。** Sumika 压缩材料主动去掉每轮参考、工具结果与思考，估算取真实输入与自估的较大值；dsh 把 pressure、tail selection、摘要 shrink check 都交同一个 meter，并明确 overflow 无进展不 retry。对应：`S/native/internal/turn/compact.go:37-76`、`:141-187`；dsh 参考 `H/packages/llm/token-meter/src/index.ts:145-188`、`H/packages/compaction/compaction-basic/src/index.ts:202-235`。不能为了缓存照搬“保留全部工具结果做摘要”：Sumika 的范围由 `S/native/internal/memory/scope.go:53-108` 决定，压缩过滤是实际权限约束。
4. [读码；建议为推断] **借来源类型化给未来第二意见和授权审查使用，保留确定性 policy 为最终门槛。** Sumika 已区分 memory.DataNotice 与固定约定、链接正文也按外部资料转义，但通用 `Msg.Kind` 仍主要是 said/context/tool/summary；dsh reviewer 把 human、parent、constraint、checkpoint、fact 分开。对应：`S/native/internal/memory/render.go:17-36`；`S/native/internal/linkplugin/detail.go:70-76`、`:134-148`；`S/native/internal/turn/loop.go:30-69`；现有审批入口 `S/native/internal/approval/approval.go:61-80`、`:109-165`。建议借来源 metadata 和冻结快照，不借 Auto 的 danger-full-access 默认或卸载降级；摘要和工具返回都不能重新授予用户未给的权限。参考 `H/packages/experimental/auto-review/src/index.ts:71-113`、`:363-462`、`:734-751`。
5. [读码；建议为推断] **借结构化工具结果的“预览 + 可恢复正文”，先用在已有受限读取上。** Sumika 链接工具已经只按当前会话可见消息编号读取，每页最多 1,500 rune / 20 块，用 offset 续页；这是明确的能力边界，适合继续保留。可借 dsh 把 canonical value 与模型预览、UI 元数据分开，避免卡片和模型分别从自由文本猜字段。对应：`S/native/internal/linkplugin/detail.go:17-21`、`:35-68`、`:70-120`；常量 `S/native/internal/linkplugin/plugin.go:34-47`；dsh 参考 `H/packages/core/tools/src/index.ts:206-259`、`H/packages/spill/spill-policy/src/index.ts:99-143`。恢复 locator 必须仍受同一角色、会话和可见消息限制，不能把分页优化变成可读任意本地路径的新工具。
