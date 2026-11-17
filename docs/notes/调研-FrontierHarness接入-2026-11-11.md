# 调研：FrontierHarness Eval 能不能当 Sumika 的外壳质量测试（2026-11-11）

> 状态：只读调研，没下载运行别人的代码、没花钱跑模型、没改仓库代码。不提交 git。
> 标记：**[核实]** = 读到原始数据或源码；**[推测]** = 凭读码和公开资料推断，没跑过。
>
> 参考：
> - 站点 https://frontierharness.org/（页面要 JS 渲染；数据取自它自己的 `/api/summary`、`/api/live`，2026-11-11 抓取）
> - GitHub `frontier-harness-eval/eval`（README、`benchmark.json`、`skills/frontierharness-eval/reference.md`、`tasks/`）
> - Harbor `harbor-framework/harbor`（原 laude-institute/harbor，已改组织）：`README.md`、`docs-mintlify/agents/custom-agents.mdx`、`src/harbor/agents/base.py`、`agents/installed/{base,pi,codex}.py`、`sandboxes/pre-integrated-sandboxes.mdx`、`registry.json`
> - Sumika：`server/src/local/{tools,chat,model,gate}.ts`、`server/src/prompt/{system,history}.ts`、`prompts/{core,scene-home}.md`、`docs/subsystems/bun-server.md`、`docs/archive/规格/规格-Bun后台第2批.md`、[对照-Agent内核-Codex和Crush](对照-Agent内核-Codex和Crush-2026-11-03.md)

## 结论（先看这几条）

1. **能当测试，而且 Pi 就在榜上。** Sumika 的内核是 `@earendil-works/pi-agent-core`，Pi 在 DeepSeek-V4.1-Flash 上成绩不差（快照原始 81.8%）、每题最便宜（$0.042）。所以内核不是短板，被测的是 Sumika 包在外面那层：提示词、工具表、预算、审批门。[核实 + 推测]
2. **现在不能下“Codex 比别人强”的结论。** v1.1 只放出约 36%（9,418 / 26,040 条），各 harness 见过的题不齐；按共同题逐题配对，Codex 在 DeepSeek 上比 mini-SWE-agent 低 1.2 个点。唯一完整的数据是 v1.0（Kimi K3、30 题、没有 DeepSeek）。[核实]
3. **接入路径可行但要先补洞。** 用 Harbor 的 `BaseInstalledAgent` 做适配（Apache-2.0、`uv tool install harbor`），Sumika 这边至少要补：无头命令行入口、Linux 的 bash 工具、直放的审批门、可配置的模型地址、放宽运行预算。目前 Bun 后台 `local/tools.ts` 只实现了 read / ls / write / powershell / memory_search，**而提示词明文让模型用 `edit`、`todo_write`——模型被要求用它没有的工具**（现成 bug，没动）。[核实]
4. **本机没装 Docker**，Harbor 默认沙箱就是 Docker；`uv` 已有。[核实]
5. 题目包：v1.0 的 30 题公开在 GitHub 仓库里（**仓库没有 LICENSE**）；v1.1 的 62 题包没查到公开位置（不在 Harbor 的 `registry.json`）。[核实]

## 一、v1.1 站点（实时，2026-11-11 快照）

### 1.1 它是什么、数据真不真

- 21 个 harness 配置 × 5 个模型（GLM-5.3、GLM-5.3-Flash、DeepSeek-V4.1-Flash-1017、Muse Spark Open、Kimi-K3）× 62 题 × 每题 4 次；数据集 `frontier-harness-eval@v1.1.0`，题来自 Terminal-Bench 2.1（45 题）和 TB 4.0（17 题）；沙箱 Runta，1~4 核、2~8 GB，agent 超时 900~28800 秒，服务商 Baseten，effort 全部 max。[核实，`/api/live` 的 settings]
- 列了的 harness：mini-SWE-agent（基线）、**Codex CLI 0.164.0（带 baseten-catalog 补丁）**、**Claude Code 2.1.308**、**Pi 1.4.0**、Oh My Pi、Hermes、OpenCode、DeepSeek Harness 四档、KimiCode、ZCode、Antigravity CLI、Grok Build、Cline CLI、MiniMax Code、Maka、Alma、GitHub Copilot CLI、Exo。没有 OpenHands。[核实]
- “sample data”：前端 `app.js` 用 `/api/summary` 的 `sample` 字段决定显示不显示角标，现在该字段不存在，角标是隐藏的（主会话读到的是隐藏元素的文字）。站点实际是**重放**：`/api/live` 按一个所有访客共享的时钟逐步放出已有试验，每条有 token、成本、耗时、结果，看着是真实记录，但页面还写着“est. left 3d”，放出的只是一部分。数据是不是真跑出来的，我没法证实。[核实：前端源码；真伪：推测]
- `trials`、`runtime` 两页被站点设为隐藏视图（`hidden_views`）；`live`、`efficiency` 是同一份数据的不同画法。[核实]

### 1.2 快照数据的口径（别当最终榜）

- 已放出 9,425 条，总共应有 5 个模型 × 5,208 = 26,040 条。每个 harness × 模型只有 40~125 条，见过的题数不齐（Codex×DeepSeek 只见过 62 题里的 43 题）。95% 区间大约 ±10 个点。
- 耗时三个字段不同：`real_seconds` 含排队等位；`run_seconds` = agent + verifier（站上 race 图用的就是它）；`sandbox_seconds` 是 Runta 计费口径。下面耗时一律用 `run_seconds`，**含 verifier**。
- 成本 = Harbor 报的，没有则 tokens × Baseten 标价。DeepSeek-V4.1-Flash-1017 标价：输入 $0.30、缓存输入 $0.007、输出 $1.20 / 百万 token（站上标注 2026-10-29 核对）。**主人实际走 Magpie 或 DeepSeek 直连，价格可能不同。**

### 1.3 DeepSeek-V4.1-Flash-1017 上各 harness（2026-11-11 快照）

“原始”= 该 harness 自己跑过的题的通过率；“配对差”= 只看它和 mini-SWE-agent 都见过的题，按题平均后的差（百分点，和榜上 harness gain 同一思路，我自己算的、没带区间）。

| harness | 条数 | 原始通过率 | 配对差 | 平均成本/条 | run 时间中位 / 均值 | 输入 / 缓存 / 输出 token（均值） |
|---|---|---|---|---|---|---|
| mini-SWE-agent（基线） | 98 | 76.5% | 0 | $0.117 | 1023 s / 1491 s | 3868k / 3768k / 51k |
| **Codex CLI** | 67 | **85.1%** | **-1.2** | **$0.068** | 1150 s / 1465 s | 1816k / 1749k / 30k |
| Claude Code | 80 | 81.3% | +4.9 | $0.117 | 1166 s / 1729 s | 2341k / 2245k / 60k |
| **Pi** | 66 | 81.8% | -7.9 | **$0.042** | 1085 s / 1184 s | 522k / 483k / 22k |
| Oh My Pi | 89 | 71.9% | -5.8 | $0.123 | 1029 s / 1610 s | 3612k / 3513k / 52k |
| OpenCode | 87 | 67.8% | -6.6 | $0.086 | 1075 s / 1577 s | 2504k / 2422k / 38k |
| Hermes | 59 | 71.2% | -8.5 | $0.183 | 1300 s / 1785 s | 3658k / 3571k / 97k |
| DeepSeek Harness Standard | 86 | 74.4% | -3.8 | $0.128 | 1259 s / 1730 s | 3857k / 3756k / 60k |
| KimiCode | 95 | 75.8% | -0.9 | $0.123 | 909 s / 1429 s | 3330k / 3227k / 58k |
| Cline CLI | 78 | 80.8% | +1.0 | $0.176 | 1181 s / 1708 s | 3047k / 2818k / 73k |
| ZCode | 40 | 80.0% | +8.3 | $0.133 | 1000 s / 1876 s | 4239k / 4129k / 59k |

（其余配置略，可从 `/api/live` 重算。21 个配置的 DeepSeek 原始通过率在 61.8%~85.1% 之间。）

怎么读：
- Codex 的原始 85.1% 比配对差（-1.2）好看得多，是因为它见到的题偏容易。**现在不能说 Codex 领先**；能说的是它**便宜**（$0.068，是基线的 58%）、token 用得少（输入 182 万 vs 基线 387 万）。
- Pi 最省：每条 $0.042、输入只有 52 万 token，但配对差 -7.9（只见 38 题，样本更小）。时间上没有哪个 harness 明显更快，中位都在 15~22 分钟。
- Codex 在 DeepSeek 上超时 5/67 条（7.5%）。

### 1.4 其他模型上的 Codex（快照，原始通过率）

| 模型 | mini-SWE | Codex | Claude Code | Pi |
|---|---|---|---|---|
| GLM-5.3 | 64.8% | 68.7% ($0.41) | 59.2% | 69.9% ($0.36) |
| GLM-5.3-Flash | 61.5% | 75.4% ($0.096) | 66.2% | 72.0% ($0.050) |
| Muse Spark Open | 65.6% | 80.8% ($0.50) | 66.3% | 63.2% |
| Kimi-K3 | 80.4% | 83.1% ($0.62) | 76.1% | 77.5% |

## 二、v1.0（已完成，GitHub 上有完整数据）[核实，README 表]

Kimi K3、30 题（21 个 Terminal-Bench 2.1 + 9 个 DeepSWE）、每个配置每题 1 次（canonical = 第一次有效尝试）。

| 配置 | 通过率 | 每次通过的成本中位 | 缓存 | 时间中位 |
|---|---|---|---|---|
| **Codex 0.148.0** | **66.7%** | $3.47 | 88.0% | 6m43s |
| DSH Creator | 63.3% | $3.28 | 84.3% | 6m44s |
| Claude Code | 63.3% | $18.34 | 67.8% | 9m38s |
| **Pi 0.84.2** | 60.0% | **$2.43** | 79.4% | 7m33s |
| DSH Standard | 60.0% | $3.46 | 86.5% | 6m17s |
| Oh My Pi | 56.7% | $4.75 | 82.2% | 6m46s |
| OpenCode | 50.0% | $3.24 | 78.4% | 6m27s |
| Hermes | 50.0% | $2.90 | 85.9% | 6m58s |

30 题时 1 个点 ≈ 0.3 题，差距在统计上基本站不住；这份没有 DeepSeek 的数。

## 三、题目包、运行器、许可证

| 东西 | 在哪 | 许可证 | 状态 |
|---|---|---|---|
| 结果与任务说明 | GitHub `frontier-harness-eval/eval`（`tasks/<题>/instruction.md` + `task.toml`、`results/eval-data.json`、`skills/frontierharness-eval/`） | **没有 LICENSE 文件**（API 的 licenseInfo 为空，`LICENSE` 404），按默认是“保留所有权利”，不能当开源 | **只有 v1.0 的 30 题**（最后推送 2026-09-08）[核实] |
| v1.1 的 62 题包 `frontier-harness-eval@v1.1.0` | **没查到公开位置**：不在 Harbor 的 `registry.json`（80 个数据集，有 terminal-bench@2.0 的 89 题、terminal-bench-sample、terminal-bench-pro）；可能在 Harbor Hub，未登录没法确认 | 没查到 | [核实：registry；其余没查到] |
| 题的来源 | Terminal-Bench 2.0/2.1（`-d terminal-bench@2.0`）、TB 4.0（`terminal-bench@4.0.0`，66 题）、DeepSWE（`datacurve-ai/deep-swe`） | 没逐个查 | [推测：TB 系列和 Harbor 同团队，多半 Apache-2.0，没核] |
| 运行器 Harbor | `harbor-framework/harbor` v0.25.0 | **Apache-2.0** [核实] | `uv tool install harbor`，Python ≥3.12 |
| 官方复现流程 | 仓库里的 `frontierharness-eval` 技能：用 **Runta**（商业运行时，要 `runta login`）做“金色检查点 + 每题新恢复” | — | 不是必须：Harbor 本身可用 Docker 在本机跑 |

要点：
- Harbor 默认沙箱 Docker。文档沙箱表里 Windows 一栏列了 `docker`、`daytona` 等，源码有 `docker_windows.py`，但那是“任务环境是 Windows”的支持；**Windows 宿主 + Linux 容器（Docker Desktop）能不能顺利跑，文档没写**，我也没跑过。[没查到]
- 想排进榜单要用同样的 Runta 环境和出网白名单；新跑的结果默认 `methodology_comparable: false`、没有名次（README）。**用本机 Docker 跑只能和自己对比，不能和榜上数字直接比。**
- 任务形态 [核实，`regex-log` 的 `task.toml`]：容器里一个工作目录（如 `/app`），`instruction.md` 是给 agent 的提示，agent 超时（例 1200 s）、verifier 超时（900 s），镜像预置好，`allow_internet=false` 的题 agent 本身也出不了网；验证脚本在容器里跑，只有它通过才算过。需要的工具：shell、读写文件；个别题要看图（`code-from-image`）。
- 站上 DeepSeek 等模型是 Baseten 渠道、`effort=max`、Codex 带 Baseten 补丁。Sumika 经 Magpie 走同名模型，线路、推理强度、缓存都可能不一样，数字只能作参考。

## 四、怎么把自定义 harness 接进 Harbor [核实，源码和文档]

Harbor 有两种自定义 agent（`docs-mintlify/agents/custom-agents.mdx`）：

| 类型 | 做法 | 例子 |
|---|---|---|
| **Installed agent**（官方推荐） | 继承 `BaseInstalledAgent`：`install()` 在容器里装好它的命令行，`run()` 用 `exec_as_agent` 在容器里执行一条命令；`populate_context_post_run` 解析用量 | `src/harbor/agents/installed/claude_code.py`、`codex.py`（2253 行）、`pi.py`（1129 行）、`mini_swe_agent.py` |
| **External agent** | 继承 `BaseAgent`，agent 循环在 Harbor 进程里，通过 `environment.exec(...)` 操作容器 | `src/harbor/agents/terminus_2/` |

运行自定义的：`harbor run -t <task> -a my_module:MyAgent -m <模型> --ak k=v --ae ENV=val`，模块要能被 Harbor 进程 import；`MODEL_CONNECTION`（`ModelConnectionSpec`）声明怎么拿到服务商的 key 和 base url。

**对 Sumika 最有参考价值的是 `pi.py`**：它在容器里用 nvm 装 node，`npm install -g` 装 pi，然后跑
`pi --print --mode json --session-dir … --model … "<指令>" 2>&1 </dev/null | grep -v '"type":"message_update"' | stdbuf -oL tee output.jsonl`，再解析 jsonl 取 token 和轨迹。`codex.py` 用 `codex exec --dangerously-bypass-approvals-and-sandbox --json --enable unified_exec …`：**直接关掉审批和沙箱，因为容器本身就是沙箱**。

## 五、把 Sumika 本地私聊回合当 harness：要做什么

### 5.1 现状 [读码]

- `server/` 是 WebSocket JSON-RPC 服务，等主窗口来连；**没有“一次性无头跑一个任务就退出”的入口**。一轮回合在 `server/src/local/chat.ts`，用 pi 的 `Agent`。
- 工具（`local/tools.ts`）：`read`（偏移 + 行数）、`ls`、`write`（整文件覆盖）、`powershell`（默认 `pwsh -NoProfile -NonInteractive -Command`，`env.shell` 可换；不支持 `background=true`）、`memory_search`；再加记忆写入、联网三件套、工作类工具、`task` 派活（`app.ts` 的 `tools:` 回调）。
- **Go 的 `declarations_gen.go` 声明了 `edit`、`grep`、`find`、`todo_write`、`process_output`、`process_stop`，Bun 这边没有实现；而 `prompts/scene-home.md` 让模型“改文件”“先 `todo_write` 列步骤”“edit 的原文要和文件逐字一致”。** 日常也是个 bug（模型去调不存在的工具），评测里会直接拉低成绩。
- 预算：`RUN_BUDGET = { requests: 24, tools: 60, ms: 15 分钟, childRequests: 300 }`（`chat.ts:278`），超了就 `budget_exhausted`。
- 工具执行 `toolExecution: "sequential"`（`chat.ts:754`）。
- 模型：`model.ts` 写死 `contextWindow: 128000`、`maxTokens: 8000`、`reasoning: false`，只走 Magpie `127.0.0.1:3425`（可用 `SUMIKA_GATEWAY` 换地址）。
- 上下文：只在“开新一轮时”按 0.85 窗口硬门槛裁剪（`history.ts hardLimit`）；**一轮之内没有压缩**（`chat.ts` 里 grep `compact` 为零）。
- 审批门：`app.ts` 里 `gate: (lc.gate ?? policyGate(...))`，有注入点，可以换成直放的 `ToolGate`。
- 提示词：`core.md`、角色卡、`scene-home.md`、shell 说明拼成系统提示；`core.md` 第一条是“始终使用中文思考和回复”，`scene-home.md` 要求“动手前先用一两句话说清楚”，对打榜是负担。

### 5.2 接入要做的步骤 [推测，没跑]

正路：

1. **装 Docker Desktop**（本机没有）并 `uv tool install harbor`。先用 Harbor 自带的 `-a pi` 或 `-a codex` 在 `terminal-bench-sample` 的 1 题上跑通，确认本机链路。
2. **给 `server/` 加无头入口**：新文件如 `server/src/headless.ts`，读指令、模型、工作目录，不开 WebSocket、不碰 Go 的库和数据锁、不用角色卡记忆；跑一轮直到模型不再调工具，把 token 用量和轨迹以 jsonl 写出来（对照 `pi --print --mode json`）。
3. **评测模式**（环境变量或参数打开）：
   - 工具：补 `edit`（精确替换）、`grep`、`find`、`todo_write`；声明里把 `powershell` 换成 `bash`（Linux 容器里 `env.shell` 改成 `["bash","-lc"]`）；`background` 和持久 shell 最好一并做。
   - 门：换成放行的 `ToolGate`。容器就是沙箱，这是合规的放行方式，不是去掉生产里的审批门；生产路径不变。
   - 预算：`RUN_BUDGET` 改成按任务 `agent.timeout_sec` 走，请求数和工具数放宽。
   - 提示词：关掉人设、中文强制、聊天场景，只留执行类规则（先读后改、验证、只改要求的）。
   - 模型：base url、key、模型名、`contextWindow` 从环境变量读（Harbor 的 `MODEL_CONNECTION` 就是喂这个的）。
4. **写 Harbor 适配器**：`SumikaAgent(BaseInstalledAgent)`，`install()` 里装 Bun、拷 `server/` 和依赖，`run()` 里 `bun server/src/headless.ts …`，解析 jsonl 填 `AgentContext`。放在 Sumika 仓库的 `scripts/` 下，以 `module:Class` 方式传给 `--agent`。
5. 先跑 `oracle` 和 1 题确认适配器和 verifier 连通，再跑小样（见第七节）。

便宜探路（不动 Sumika 代码）：用 Harbor 的 `-a pi` 跑一批题，看能不能用 `--ak` 换系统提示词（没查到 `pi.py` 是否支持传提示词，需读它的选项）。这不等于测 Sumika，但能先把“提示词那一层值多少分”剥出来。

## 六、Sumika 和 Codex CLI 的差距（推测）与改进清单

“差在哪”是读码加公开资料的推测，没有跑过对照。Codex 的具体设计差别见已有的[对照-Agent内核-Codex和Crush](对照-Agent内核-Codex和Crush-2026-11-03.md)，这里不重拆。Pi 的成绩（比基线便宜、通过率相近）说明 pi 内核本身够用。

| # | 改进 | 差在哪 | 预期收益 | 备注 |
|---|---|---|---|---|
| 1 | **放宽运行预算** | 24 次模型请求 / 60 次工具 / 15 分钟，而题目 agent 超时 900~28800 秒、Codex 平均要二十多分钟 | **最大**：多数难题会被预算掐掉，等于直接记 0 | 评测模式按任务超时走；`budget_exhausted` 防失控的意义在容器里不需要 |
| 2 | **补上 `edit`、`grep`、`find`、`todo_write`**，提示词和工具声明对齐 | 现在只能 `write` 整文件覆盖和在 `powershell` 里自己 grep；提示词却让模型用它没有的工具 | 大：改文件易整文件重写出错，找代码要多轮 shell | 声明已有、只缺实现；Codex 用 `apply_patch`，Pi 用精确替换 `edit` |
| 3 | **一轮内压缩上下文** | 只在开轮时裁剪；`contextWindow` 写死 128k；预算放宽后长题可能撑爆窗口 | 中到大（预算放宽后才显现） | 放宽预算后必须同时做 |
| 4 | **评测模式关掉人设和中文强制** | 系统提示里有角色、“始终中文思考”、“动手前先说一两句” | 中：对 DeepSeek 这类模型提示越短越稳，也省钱 | Pi 的 token 只有 Codex 的三成，少说话有收益 |
| 5 | **并行工具调用** | 现在顺序执行；一次要读几个文件时要多轮 | 中（主要省时间） | pi 的 `Agent` 支持，改一个参数；注意审批门并发 |
| 6 | **持久 shell / 后台进程** | `powershell` 每次新开进程、不支持 `background=true`；起服务、长编译做不了 | 中：部分题必需 | Codex 开着 `unified_exec`；对应 Go 的 `process_output` / `process_stop` |
| 7 | **模型参数可配置** | `reasoning: false`、`maxTokens: 8000`；站上 effort 全是 max | 中：长输出被 8000 截断、不开推理 | 要和 Magpie 的模型能力对齐，实测 DeepSeek-V4.1-Flash 有没有推理档 |
| 8 | **自检和重试习惯** | 只有提示词里“改完要验证”一句；预算一到就停 | 小到中 | 提示词层面最便宜，可做成评测提示词的一段 |

建议先做 1~4（都在评测模式里，不动生产行为），测一遍，再决定做不做 5~7。

## 七、成本和时间估算（估算，依据站上 DeepSeek-V4.1-Flash 快照）

站上 DeepSeek 全部 1,706 条的平均：输入 307 万 token（绝大部分缓存命中）、输出 5.9 万、平均 $0.125/条、中位 $0.069/条。各 harness 差异大（见 1.3），Sumika 落在哪一档要看第六节改完后的行为。

| 方案 | 条数 | Pi 档 $0.042 | Codex 档 $0.068 | 基线档 $0.117 | 全站平均 $0.125 |
|---|---|---|---|---|---|
| 完整一轮：62 题 × 4 次 | 248 | $10 | $17 | $29 | $31 |
| 小样：10 题 × 1 次 | 10 | $0.4 | $0.7 | $1.2 | $1.3 |

- 价格是 **Baseten 标价**（输入 $0.30 / 缓存输入 $0.007 / 输出 $1.20 每百万）。主人走 Magpie 或 DeepSeek 直连的实际价格不同，要按那条线路重算。缓存命中率取决于 Sumika 提示词前缀是否稳定（LESSONS 里已有这条规则）。
- 沙箱费：站上每条约 $0.12（Runta 计费）。**本机 Docker 没有沙箱费**，但要自己的机器开得起并发。
- 时间：站上 DeepSeek 单条 agent+verifier 均值约 25 分钟（中位 19 分钟）。248 条 × 25 分钟 ≈ 103 小时；并发 3~4 约 1~1.5 天，并发 2 约两天。10 题 × 1 次：串行约 4 小时，并发 3~4 约 1 小时。
- 小样选题：v1.1 的 62 题没公开包，只能用 v1.0 公开的 30 题里挑 10 个（TB 和 DeepSWE 各取一部分）。

## 八、没查到 / 待确认

- v1.1 的 62 题包 `frontier-harness-eval@v1.1.0` 的下载位置和许可证。
- 站点最终榜（还要约 3 天）；本笔记 v1.1 数字都是 2026-11-11 快照。
- Windows 宿主 + Docker Desktop 跑 Harbor 的实测。
- `pi.py` 能否通过 `--ak` 传系统提示词。
- 站点“sample data”角标的真正含义（作者说明），只确定前端现在把它藏起来了。
- 没做：下载运行 Harbor、跑 oracle、查 DeepSWE 许可证、读 Codex 源码（沿用旧笔记）。
