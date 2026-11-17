# 调研：Agent 的工具环境、沙箱和 worktree（2026-11-04）

| 项目 | 工具与运行环境 | 命令沙箱，尤其 Windows | worktree / 工作隔离与回退 |
|---|---|---|---|
| Codex | [读码 C1] 安装布局明确分 `bin`、`codex-path`、`codex-resources`，带 rg 与分平台执行助手 | [读码 C2–C4] Linux bwrap + seccomp；Windows 有旧受限令牌、提权安装的沙箱和独立 MXC 路线 | [读码 C5] 校验 Git 共有目录并发现已有 linked worktree；本次所读模块没做每子任务自动建树 |
| Crush | [读码 R1] PATH 找 rg，没有时 Go 搜索回退；Go 内嵌 shell 解释器 | [读码 R2] 命令拦截和解释器执行；所读本地入口没做 OS 沙箱 | [推断 R2] 所读 shell / 工具入口没做任务 worktree；不把多会话当成文件隔离 |
| DeepSeek Harness | [读码 D1] build 提供 Python / Node / pnpm 载荷，运行时工具返回绝对路径；搜索依赖包内 rg | [读码 D2–D4] Linux bwrap / Landlock、macOS Seatbelt；Windows ACL 后端明确仅部分强制执行 | [读码 D5] experimental 服务创建并进入 retained Git worktree；没有自动合回和自动删除 |
| kubectl-ai | [读码 K1–K3] 本地依赖已有 shell / kubectl；远程工具可来自 Kubernetes 镜像 | [读码 K1–K3] Windows 本地 COMSPEC；macOS 所读 Seatbelt profile 实际是 allow-default；K8s 是另一执行端 | [读码 K3] Pod 生命周期，不是 Git 任务分支；所读后端没做代码合回 |
| Eino | [读码 F1] 框架定义工具接口，exe / runtime 由集成方供给 | [读码 F1] 工具接口本身没做进程沙箱 | [读码 F2] graph checkpoint 保存执行状态；不是文件快照 / Git worktree |
| ADK-Go | [读码 F3] Tool / Toolset / confirmation 接口；不在接口里管理 Python / Node 安装 | [读码 F3] 确认是工具调用策略；接口本身没做 OS 隔离 | [推断 F3] 本次接口和工具目录没做任务 Git worktree；不能以 CI 脚本用 worktree 充数 |
| Koishi | [读码 F4] NodeLoader 在项目目录解析插件；本次所读入口没做独立 Node 安装器 | [读码 F4] 插件装载到 Node 进程；不是可执行不可信代码的 OS 沙箱 | [推断 F4] 插件生命周期不等于任务目录快照；所读入口没做 Git worktree |
| Magpie | [读码 M1] 为插件按需下载并核验 Bun，支持 `MAGPIE_BUN` 指定外部版本 | [读码 M2] 插件 JS 宿主进程与主程序分开；所读启动入口没施加 OS 沙箱 | [推断 M2] 插件宿主不是改代码 Agent 的任务树管理器；所读入口没做该功能 |
| Zed | [读码 Z1] 可用系统 / 指定 Node，也能下载 managed Node；自管 npm cache | [读码 Z2–Z3] macOS Seatbelt、Linux bwrap、Windows 经 WSL 跑 bwrap；Windows 网络不是同等细粒度代理策略 | [读码 Z4] Git worktree + 线程归档；保存 staged / unstaged / untracked 状态后移除，支持重建 |
| OpenHands / software-agent-sdk | [读码 H1–H2] 桌面带 Node / npm / npx、uv / uvx；执行端可用预构建镜像 | [读码 H3] Local 与 Docker 路线并存；DockerWorkspace 有单独 API、环境转发清单与端口 | [读码 H4–H5] 每会话可建树 / 配容器；fork 可共享树；清理分缓存、依赖、整个 runtime |
| OpenCode | [读码 O1] PATH / 私有 bin 查 rg，缺失时下载钉死版本并验 SHA-256 | [读码 O2] Environment 抽象可接 workspace driver；本地 driver 直接用宿主 spawner，没在这个入口做 OS 沙箱 | [读码 O3] Git strategy 创建 / 删除 / 列举，支持项目启动命令；snapshot 是另一路文件恢复功能 |
| Claude Code | [读码 A1] 新 npm 包按平台选原生程序；老包确实带 `vendor/ripgrep` | [推断·官方文档 A2] macOS / Linux / WSL2 shell 沙箱；原生 Windows 命令不沙箱化 | [推断·官方文档 A3] `--worktree` / subagent worktree、退出和周期清理、`.worktreeinclude`、worktree 锁 |
| Goose | [读码 G1] 桌面 Unix shim 用 Hermit 管 Node / Python / uv，供 MCP 子进程 | [读码 G2] 扩展可在指定 Docker 容器启动，否则用宿主 Command；没在所读入口做 Windows token 沙箱 | [推断 G2] 所读扩展 / container 入口没做任务 worktree 和自动合回 |
| Cline | [读码 L1] 构建脚本下载分平台 rg；所读入口没做 Python / Bun 安装器 | [读码 L2] plan-mode guard 在审批前拒绝改文件命令；不是 OS 沙箱 | [读码 L3–L4] 私有 Git refs 保存 checkpoint，恢复前保护已有提交；不是每任务独立工作目录 |
| Pi | [读码 P1] 私有 bin 优先，再系统 rg / fd，缺失下载；Node 运行 Agent | [读码 P2] 本地直接 spawn shell；可替换执行接口，默认接口没有 OS 沙箱 | [推断 P2] 本次默认工具和执行入口没做自动任务 worktree；会话分叉不是文件分叉 |
| OMP | [读码 Q1–Q2] Bun + 分平台 `.node` 原生组件；sd、sg、yt-dlp 按需获取，Python CLI 可走 uv | [读码 Q3] 内嵌 brush shell 与进程生命周期管理；任务 clone 本身不限制宿主文件 / 网络 | [读码 Q4–Q5] 多文件系统 clone 后端，私有 Git 元数据；记录脏基线、只合任务增量，冲突时停止 |
| uv（工具，非 Agent） | [读码 U1–U3] 管 Python 发行版、工具 venv、缓存，各目录可指定 | [读码 U1–U3] 依赖隔离和下载管理；不是脚本的安全沙箱 | [读码 U3] 清缓存要拿锁；不负责 Git worktree / 代码合回 |

## 一、范围、版本和可信度

目标：回答“exe 怎样跟 Agent 一起交付”“自管 Python / Bun 能否更干净”“沙箱和并行工作目录怎样落地”。本次只读源码、拆 npm 包、做临时目录冒烟、写笔记；不改 Sumika 产品代码，不决定新权限默认值，不写 ADR，不合 main、不推送。按[拆解笔记的写法](拆解笔记-写法.md)组织，正文先描述各家的实现，建议独立放末节。用途挂在[待办](../待办.md)的“干活”条目。

可信度：`[读码]` 为读过实现；`[实测]` 为本次跑出的结果；`[推断]` 包括设计建议及只看官方文档的产品行为。出处编号在各节给出，格式为仓库快照 + 路径:行号。总表的“没做”严格限定到列出的模块 / 默认执行入口：不是对全仓库和所有社区插件的不存在证明。查过的位置和方法是各节所列的源码目录、符号定位以及实现阅读；未读的发布服务、闭源程序内部、第三方部署差异不外推。

源码根目录为本机 `_external`，初始已有仓库直接读，新获取的仓库为浅克隆。用于事实定位的快照如下；这些是研究时固定的版本，不代表当前官网最新版本。OpenHands 的前端 / 启动器与执行端 SDK 分仓，必须分别记版本。

| 本机目录 / 上游 | 提交（前 12 位） |
|---|---|
| codex / openai/codex | `079e17a42234` |
| crush / charmbracelet/crush | `3b1f8604f002` |
| deepseek-harness / deepseek-ai/deepseek-harness | `687f282e8625` |
| kubectl-ai / GoogleCloudPlatform/kubectl-ai | `2c8ff822bacb` |
| eino / cloudwego/eino | `0a5ca53b9d3b` |
| adk-go / google/adk-go | `4df7bdda94fd` |
| koishi / koishijs/koishi | `97d5c395750c` |
| magpie / yetone/magpie | `5e0d7c058be2` |
| zed / zed-industries/zed | `784374c6794a` |
| OpenHands / OpenHands/OpenHands | `4b76a13ac251` |
| OpenHands-sdk / OpenHands/software-agent-sdk | `e1896aaf7887` |
| opencode / sst/opencode | `8568952e77be` |
| opencode-v2（本机旧对照，仅区分版本，不作为本篇当前实现依据） | `2e8a861e11d6` |
| claude-code / anthropics/claude-code（发布仓库，不是核心源码） | `26382c087582` |
| goose / block/goose | `bfe8ccd23de1` |
| cline / cline/cline | `7a7878cb52fc` |
| uv / astral-sh/uv | `33f003cb85d7` |
| pi-mono / badlogic/pi-mono | `642e30b68930` |
| oh-my-pi / can1357/oh-my-pi | `5dc44d341772` |
| Sumika 文档 worktree 基线 | `1c6cc589414f` |

[读码 Q2] 还对照了本机 npm 安装的 OMP：`@oh-my-pi/pi-coding-agent` 18.15.0；`@oh-my-pi/pi-natives-win32-x64` 18.14.0，包入口 `pi_natives.win32-x64-baseline.node`。源码快照与安装包版本分别记录，不能把源码新功能全部归给安装版。原生包目录实有约 118.2 MB 的 `.node`，这是目录观察值，不是进程常驻内存。出处：安装目录两个 `package.json`，原生包 `package.json:1-25`。

未做：运行各家完整 Agent、Docker / WSL 实际隔离验收、Windows ACL 改写、管理员安装、杀毒软件兼容实验、跨平台性能对比、真实项目任务合回、外部模型调用。本次冒烟只证明第六节列出的 Git / uv 行为。

## 二、工具与运行环境：谁负责什么

### 2.1 Codex：把命令、资源和 PATH 工具分开

[读码 C1] `codex@079e17a42234 scripts/codex_package/layout.py:33-92` 是实际打包入口：`bin/` 放 Codex、code-mode host、exec-server；`codex-path/` 放 rg；`codex-resources/` 放 Linux bwrap、Windows command-runner 与 sandbox-setup。后两类按平台可选。包内 `codex-package.json` 记录 `layoutVersion`、版本、目标平台及三个位置。不是把 rg 的代码改写进 Agent，也不是要求用户把它全局装进系统。

关键原文（同文件 40–45、63–66）：

```python
bin_dir = package_dir / "bin"
resources_dir = package_dir / "codex-resources"
path_dir = package_dir / "codex-path"
bin_dir.mkdir()
resources_dir.mkdir()
path_dir.mkdir()
```

[读码 C1、C2] rg 清单钉 `15.2.0`，分目标记录压缩格式、大小、SHA-256、包内文件路径和下载 URL，见 `scripts/codex_package/rg:1-12,53-65`。bwrap 执行前还从打开的文件验 digest，再通过 `/proc/self/fd/<fd>` 执行，见 `codex-rs/linux-sandbox/src/bundled_bwrap.rs:65-83`。安装包版本与工具清单一起升级，独立资源的来源可查。这个清单未提供通用 Python / Node 环境管理，不能从“Codex 是原生二进制”推成“模型执行的 Python 也内嵌”。

[读码 C1] PATH 注入在 `codex-rs/arg0/src/lib.rs:132-157,440-468`：启动还没建线程时，把包的 pathDir 前置到当前进程 PATH，Windows 用分号；其子进程继承。`install-context/src/lib.rs:151-173` 的搜索调用优先取包内 rg，其次旧 standalone resources，最后才是系统命令名。这里没有写系统 PATH；提供自己的系统 rg 也不保证覆盖已存在的 bundled rg。

### 2.2 Pi、OMP：同一家族已有不同取舍

[读码 P1] `pi-mono@642e30b68930 packages/coding-agent/src/utils/tools-manager.ts:42-90,99-126,247-331,337-397`：管理 rg 与 fd；先用 `getBinDir()` 下的程序，再尝试系统命令，fd 还接受 `fdfind`。确实缺失时通常解析 GitHub latest、下载对应平台压缩包、解压后保留可执行文件；macOS x64 的 fd 例外钉 `10.3.0`（259–260 行）。离线模式会停止自动获取。所读下载 / 解压路径没有像 OpenCode 那样的钉死 SHA-256 清单；不要把“通过 HTTPS 下载”写成“已校验发布物摘要”。

[读码 P1、Q1] Pi 管理二进制默认在 `~/.pi/agent/bin`（`pi-mono/packages/coding-agent/src/config.ts:630-635,663-665`）；OMP 默认辅助程序在 `~/.omp/agent/tools`、任务池在 `~/.omp/wt`，见 `oh-my-pi/packages/utils/src/dirs.ts:833-835,1012-1014`。两者均有 agent 根目录覆盖；OMP 另有 profile / XDG 路径规则（同文件 4–10、364–440）。这些工具内部直接用解析后的路径，不必让用户全局 PATH 永久多出这些目录。

[读码 Q1] `oh-my-pi@5dc44d341772 packages/coding-agent/src/utils/tools-manager.ts:79-150,154-178,249-331,334-360` 管的是 sd、ast-grep 的 sg、yt-dlp，以及 trafilatura。前三者私有工具目录优先，再查系统；下载临时目录和 archive 在 finally 清掉。trafilatura 优先 `uv tool install`，失败可退到 `pip install --user`。后者会写用户级 Python 安装位置，不能据此称 OMP 的全部 Python 依赖都由自身目录托管。

[读码 Q1、Q2] OMP 主程序可经 Bun compile 交付，原生 addon 作为构建资源嵌入，加载时释放；`fastembed`、`onnxruntime-node` 明确外置按需安装。入口 `packages/coding-agent/scripts/compile-binary.ts:9-10,22-30,47-83`、`scripts/ci-release-build-binaries.ts:125-143`。内嵌 addon 和单独分发 exe 是两种打包形态：前者内部就有可复用的原生能力，并不必再带一份同功能命令行程序。当前读到的辅助工具下载器没有统一的容量上限 / 旧版本 LRU 管理；仅清掉下载临时文件不等于清掉所有缓存。

### 2.3 OpenCode、Crush、Cline：rg 可以自备，也可以借系统

[读码 O1] `opencode@8568952e77be packages/core/src/ripgrep/binary.ts:15-54,69-70,84-159`：钉 rg `15.1.0` 与各平台 SHA-256；查可用命令 / 私有 bin，缺失再下载到 `global.bin`。校验通过才写 archive / 解压，成功后删 archive。搜索直接调用解析出的绝对文件；不需要改用户全局 PATH。`ripgrep.ts:118-123` 把执行交给环境 spawner，远端 workspace 可以在自己的镜像中解析 rg。

[读码 O1] Windows 解压特意用 `%SystemRoot%/System32/tar.exe`，避免 PATH 上 Git GNU tar 不认 zip，也避开 PowerShell 执行策略；在临时目录用 ASCII 相对 archive 名，减少 ANSI 参数解码损坏中文用户目录的风险，见 `binary.ts:89-101`。这类安装细节比“Windows 也下载 zip”更接近可交付方案。已存在的 rg 路径会直接复用，不能说每次运行都会重新验它的哈希。

[读码 R1–R2] `crush@3b1f8604f002 internal/agent/tools/rg.go:15-26` 只 `exec.LookPath("rg")`；`grep.go:194-221` 在 rg 不可用 / 出错时走 Go 正则；`glob.go:113-121` 回退到 Go 文件遍历。`internal/shell/run.go:53-58,87-97` 用 mvdan 的 shell parser / interpreter，外部程序仍依赖宿主。好处是 exe 依赖少，代价是回退搜索实现与 rg 的功能、速度不必完全相同。没有读到这里替用户安装 Python / Node 的机制。

[读码 L1] `cline@7a7878cb52fc apps/vscode/scripts/download-ripgrep.mjs:3-7,20-60,65-98,104-158` 在构建时下载 rg `14.1.1` 到 `dist-standalone/ripgrep-binaries/`，包括 `win-x64/rg.exe`。这里有版本常量，所读获取过程未实现 OpenCode 那种摘要比对。此脚本是 VS Code / standalone 路线的打包证据，不自动等于所有 SDK 使用者都装同一份 rg。没有在这个脚本提供 Python / Bun 自管或缓存容量治理。

### 2.4 DeepSeek Harness：提供“可用依赖的位置”，不用猜 PATH

[读码 D1] `deepseek-harness@687f282e8625 packages/skill/tool-workspace-dependencies/src/index.ts:14-22,183-197,200-274,313-318` 接受带 `runtime.json` 和 `dependencies/` 的载荷。默认原地使用；有安装根时首用复制到 Harness home。Python / Node / pnpm 的平台路径由 manifest 算出，返回 interpreter、site-packages、node_modules 等绝对路径及预装发行包版本；模型随后显式调用这份 interpreter。Windows Python 是 `dependencies/python/python.exe`，Node 是 `dependencies/node/bin/node.exe`。它不是每个 skill 一个 Python 发行版。

[读码 D1] 文件搜索 `packages/fs/tool-fs-search/src/search-core.ts:1-18` 调 npm 包提供的 rg，普通 argv 子进程，不走 shell，执行结果受输出预算约束。载荷安装与 Agent 的脚本依赖分开：安装器检查 manifest 与文件 / 目录结构，模型工具负责返回路径；这段校验没有实际启动解释器。所读 workspace-dependencies 安装入口没有通用“过期依赖按容量回收”服务；依赖生命周期不能凭返回路径就算解决。

### 2.5 Magpie、Zed、Goose、OpenHands：管理运行时的现实例子

[读码 M1] `magpie@5e0d7c058be2 internal/plugin/bun.go:32-59,73-112,242-294` 为插件首用下载 Bun，基准版本 `1.4.2`；`MAGPIE_BUN` 非空时直接用指定程序，否则用自身管理的一份。固定版本 hash 在代码内，新版本从官方 `SHASUMS256.txt` 取。Windows x64 选择 baseline 构建以减少 CPU 指令集要求，ARM64 有独立构建。`bun.go:186-211` 把老系统缺入口点和 CPU 指令不支持分开报错。这是“Go 程序为了扩展而管一个 Bun”的直接参考；它管的是插件运行环境，不是整个用户开发环境。

[读码 M1] `internal/plugin/bunupdate.go:31-69,193-220,268-319` 补齐升级 / 清理生命周期：程序放在 catalog cache 同级的 `bun/<version>/magpie-bun.exe`（Unix 没有 .exe；musl 另分子目录），`state.json` 记 current / previous / bad；新版本发布至少 `48h` 后才下载、核验、试运行再切换，`MAGPIE_BUN` 覆盖时不自动升级。prune 只留 current 与 previous，没有 previous 时留出厂基准供退回；其他版本目录删除。这是按版本引用保留，未在该代码设总容量上限。

[读码 Z1] `zed@784374c6794a crates/node_runtime/src/node_runtime.rs:114-205,608-640,682-728,812-840`：可先用设置指定的 Node / npm，允许 PATH 时检测系统版本，否则按需安装 managed Node `v24.22.1` 到 Zed 数据目录 `node/node-v<版本>-<平台>-<架构>`。managed npm 有自己的 cache、空 user / global npmrc；不让项目 npm 配置随意污染它。安装无效时会清旧 managed node 目录重装，初始化时清这份 npm cache。不是多版本容量管理器，也没在该入口为每个任务建独立 Node 安装。

[读码 G1] `goose@bfe8ccd23de1 ui/desktop/src/bin/node-setup-common.sh:26-43,79-97,154-164` 的 Unix shim 在配置目录建 `mcp-hermit`，下载 / 激活 Hermit，再 `hermit install node`。`ui/desktop/src/bin/uvx:149-155` 先装 Python 3.10，再装 uv。目的是给桌面 MCP 子进程供环境，解决 GUI 启动 PATH 不完整的问题。脚本里的日志、临时 Hermit 拷贝和旧锁修复不是通用缓存清理器；所读 `hermit install node` 也没钉具体补丁版。这些 shell shims 不能当成“Windows 也原生支持同样安装流程”的证据。

[读码 H1–H2] `OpenHands@4b76a13ac251 scripts/download-node.mjs:3-36,68-70,109-112` 在构建时把官方 Node `24.22.1`（可由环境覆盖）下载到 `resources/node/`，随桌面 extraResource 交付；包含 npm / npx。原因是 Electron-as-Node 启动 stdio MCP 时曾有管道语义问题，使用独立 Node 可避开该类差异。`scripts/download-uv.mjs:3-16,51-75,80-98` 带 uv / uvx 到 `resources/bin/`；UV_VERSION 没设时解析 latest，不能把它写成固定 uv 版本。Windows Node 布局为根目录 `node.exe`、`npm.cmd`、`npx.cmd`；uv 脚本的 Windows 目标在此快照固定 x64。

[读码 H3] software-agent-sdk 的 `openhands-workspace/openhands/workspace/docker/workspace.py:53-118` 又是另一层：预构建 Agent Server 镜像提供执行环境，不必在 Windows 宿主凑齐镜像内所有 Python / Node / Linux 工具。镜像默认 tag 的存在不等于不可变 digest 钉死，也不能把桌面带的 Node 与容器内的 Node 混为一份。缓存和会话目录的清理由服务器独立负责，见 4.5。

### 2.6 Claude Code：必须把新旧 npm 包分开

[读码 A1] npm `@anthropic-ai/claude-code@2.1.318` 包本体约 28.8 KB，`package.json` 的八个平台 optionalDependencies 都锁同版本；`install.cjs` 的 `PLATFORMS`、`placeBinary`、`main` 把所选原生包的程序硬链接 / 复制到 `bin/claude.exe`，运行后不常驻 Node wrapper。这个体积仅是 wrapper tarball，不是完整安装体积。没有获取平台原生大包，因此不声称已拆到该版二进制内的 rg。

[读码 A1] 另外拆了老 npm `2.1.0`（约 25.2 MB），包里确有 `package/vendor/ripgrep/{arm64-darwin,arm64-linux,x64-darwin,x64-linux,x64-win32}`。这是历史交付形态证据，不可用来推断 2.1.318 仍有相同磁盘布局。出处均为对应 `.tgz:package/` 内目录、`package.json`、`install.cjs` 标识符；闭源压缩包采用包内路径定位，不伪造核心源码行号。

[推断·官方文档 A2] 当前 [sandboxing 文档](https://code.claude.com/docs/en/sandboxing) 的 “Set up Linux and WSL2” 说原生二进制内 bundled ripgrep，Linux / WSL2 仍需 bwrap / socat；这是官方陈述，未实测新版平台包。用户能配置 shell / 外部项目环境，不等于 Claude 给每个 skill 自管 Python / Node；本次未读到这样的通用机制。

### 2.7 框架和聊天插件系统：本体没有做的事不要替它补上

[读码 F1–F4] Eino 的 `components/tool/interface.go:25-56` 只规定工具元数据和调用，ADK-Go 的 `tool/tool.go:32-57` 规定工具、工具集合与确认错误；Koishi 的 `packages/loader/src/index.ts:19-30` 在项目 basedir 建插件解析作用域。它们能承载执行工具，但这些入口没有实现 exe 清单、独立 Python / Bun 安装、OS 沙箱或任务 worktree 清理。第三方工具 / 插件可以另做，不能列为框架默认已有。查过本机各项目的工具 / components / loader 目录并读上述实现；未纳入它们的独立扩展仓库。

[读码 K1–K3] kubectl-ai 的本地 executor 使用 Windows `COMSPEC` 或已发现的 Bash，K8s 后端显式给 `/opt/bitnami/kubectl/bin` 加 PATH，工具来自宿主 / 镜像，见 `pkg/sandbox/local.go:27-51`、`kubernetes.go:50-65`。这同“Agent 下载一套 rg / fd”是不同责任分配。

### 2.8 uv、Bun：目录归属、依赖隔离与清理要分开

[读码 U1] `uv@33f003cb85d7 crates/uv-python-managed/src/managed.rs:101-147` 明确优先级：显式 install_dir → `UV_PYTHON_INSTALL_DIR` → 用户数据目录；管理发行版目录有跨进程排他锁。发行版是 Python 本体，venv 是项目 / 工具依赖；多个 venv 可以借同一个解释器，没必要每个任务复制整个 Python。

[读码 U2] `crates/uv-tool/src/lib.rs:30-62,113-118` 的 `ToolEnvironment` 包着一个 PythonEnvironment，InstalledTools 有独立根目录。因此 `uv tool install` 适合长期 CLI，不是把所有工具灌进同一份系统 site-packages。`uv run` 的临时 / 项目环境与 `uv tool install` 的长期工具环境也不能混成同一种清理对象。

[读码 U3] `crates/uv/src/commands/cache_prune.rs:14-43` 与 `cache_clean.rs:16-45,55-78`：prune 清悬空项 / 缓存环境，clean 清全量或指定包；都试拿 cache 排他锁，占用时等待，`--force` 才绕过等待。清 cache 并不卸载 managed Python、长期工具或项目 venv，反过来卸载某个解释器也不能当清缓存。所读这些入口没有一个统一的“总大小永不超过 N GB”自动策略。

[推断·官方文档 U4] [Bun global cache](https://bun.com/docs/pm/global-cache) 说明默认 `~/.bun/install/cache`，可用 `BUN_INSTALL_CACHE_DIR` 或 bunfig `[install.cache].dir` 指定；按包名 / 版本存放，并用硬链接 / 克隆等策略减少重复。它是包缓存，不是每个任务的 node_modules；共享下载缓存不要求共享可写 node_modules。这是文档证据，没在本次跑安装 / 清理，也没有据此宣称 Bun 提供按目录配额的自动 LRU。

## 三、沙箱：审批、文件路径检查和操作系统限制的区别

[读码 P2、D3、O2] “当前目录在任务目录”只改变相对路径的起点；程序仍可能用绝对路径访问其他位置。“先审批命令”决定这次让不让执行；“OS 沙箱”限制已经获批的进程及其子进程实际能碰什么。三者都能用，但不能互相冒充。Pi 的 `spawn`、DSH 的 restricted token 和 OpenCode 的本地 driver 直接体现了这三个层次的差别。

### 3.1 Codex：Windows 不是一个统一后端

[读码 C2] Linux 的 `codex-rs/linux-sandbox/src/bwrap.rs:1-15,108-123` 把只读根文件系统、指定 writable roots、敏感子路径保护和网络命名空间组合起来，另用 seccomp + no-new-privs。网络策略分 FullAccess / Isolated / ProxyOnly，代理单一路由不是只设 HTTP_PROXY 就算隔离。macOS 路线使用 Seatbelt；本次 Windows 重点沿下面三个入口读，不能把 Linux 的限制直接平移到 Windows。

[读码 C3] `codex-rs/core/src/windows_sandbox.rs:126-140` 区分 elevated / unelevated / mxc。`windows-sandbox-rs/src/lib.rs:740-752` 明确拒绝让旧 WRITE_RESTRICTED token 承诺读隔离：受限只读访问及 deny-read overrides 都要求 elevated 后端。旧令牌模式主要约束写入，Windows 权限检查语义不是“所有访问都按 workspace 白名单”。

关键原文（`lib.rs:740-752` 中的边界）：

```rust
if !permissions.has_full_disk_read_access() {
    anyhow::bail!(
        "Restricted read-only access requires the elevated Windows sandbox backend"
    );
}
```

[读码 C3] elevated 是先由安装 / provisioning 准备专用账户、组、目录 ACL 和网络访问条件；不是让模型的命令以管理员身份随便跑。账户 provisioning 见 `windows-sandbox-rs/src/setup_provisioning/sandbox_users.rs:66-99`，网络设置形状见 `windows-sandbox-rs/src/lib.rs:50-58`，core 的准备 / refresh 见 `core/src/windows_sandbox.rs:195-224`。安装会改宿主状态，部署、故障诊断和卸载都要算成本。本次没运行 provisioning，不能声称本机该后端可用。

[读码 C4] `codex-rs/mxc-sandbox/src/lib.rs:1-2,28-42,97-99` 明确只走可用的原生 process security environment，排除旧 AppContainer fallback；`native.rs:18-39` 在能力 / denied paths 不支持时拒绝，调用 mxc_sdk BaseContainerRunner，等待子进程。托管代理要求 dedicated ports 且 allow_local_binding=true，因为 native host loopback 是双向。它是快照中第三条路线，不能把它写成“AppContainer 已全面覆盖”或“任何 Windows 都能用”。本次未审 SDK 内部，也未把它归为微虚拟机。

### 3.2 DeepSeek Harness：诚实地报告 partial

[读码 D2–D4] `packages/sandbox/sandbox-local/src/index.ts:1-6,68-89` 的平台链是 Linux bwrap 再 Landlock、macOS Seatbelt、Windows ACL runner，无法提供所需约束时失败，不默默直接执行原命令。`src/profiles.ts:16-35,51-57` 的 bwrap 根目录只读、私有 PID / proc，workspace-write 才增 workspace bind 与临时 `/tmp`；Landlock 给 `/` 读权限，再给 workspace / tmp 写权限；Seatbelt 是 allow-default 加 deny file-write* 与写白名单。这套 profile 主要承诺文件效果，不是全机密钥不可读 / 网络默认断开的承诺。

[读码 D3] `packages/sandbox/sandbox-windows-acl/src/index.ts:1-42` 给出完整限制：WRITE_RESTRICTED token + Low integrity；workspace 有确定性写 SID，私有 temp 有单独 SID。真实目录 DACL 上加 ACE，workspace ACE 跨会话保留以免反复整树传播；temp 授权 dispose 时撤销。读、网络、进程可见性不受这一写限制控制；控制台隔离也不可用。路径内有 NTFS hard link 时，“同一个文件对象在别处还有名字”影响边界，sandbox-local 报 partial，不能写 full。

最关键原文（`sandbox-windows-acl/src/index.ts:26-31`）：

```ts
 * Known boundaries (inherent to restricted tokens, not this port):
 *  - writes are restricted; reads, network, and process visibility are NOT
 *    (WRITE_RESTRICTED intersects only write accesses);
 *  - console isolation is unavailable — children share the host console
```

[读码 D3–D4] Job 的 kill-on-close 管子进程退出回收，不能据此推成文件 / 网络权限隔离；runner 说明与入口见 `packages/sandbox/sandbox-windows-acl/README.md:110-114`，实际 token / spawn 入口为 `src/index.ts:54-55` 及相应 token / spawn 模块。本次只读，没有改本机 ACL。沙箱不可用、命令被拒、runner 自己启动失败要分开回报，否则模型可能把环境故障误认成业务失败再扩大权限。

### 3.3 Zed：Windows 通过 WSL 执行，语言环境也随之变了

[读码 Z2] `crates/sandbox/src/sandbox.rs:1-7,81-117` 公开 fs / network 策略，macOS Seatbelt、Linux bwrap；HTTP(S) 域名限制由进程内代理配合系统边界执行。文件 Restricted 默认允许读大部分宿主、仅限制写路径；凭这个枚举不能宣称密钥目录都不可读。

[读码 Z3] `crates/sandbox/src/windows_wsl.rs:1-25,37-44,71-95`：Windows 路径经 wslpath 转到发行版，UNC 路径可指定 distro；在 WSL 里用 bwrap。缺 WSL、缺 bwrap、profile 无法运行时返回专门 unavailable 类型，前端可给重试 / unsandboxed 选项；错误路径 / 混 distro 是普通请求错误。Windows 当前只有 allow_network / allow_fs_write 两个布尔放宽量，注释明确没有 loopback-proxy confinement。

[读码 Z3] WSL helper 需要匹配 Zed channel / version 的 Linux Zed，放 `~/.local/libexec/zed/<channel>`，不改 WSL PATH；每 channel 保留一份，dev latest 特例会重下。下载 helper 不会顺便给 WSL 装 bwrap。由此带来两份运行世界：Windows 的 Python / Bun / 路径不能未经转换直接当 WSL 内解释器使用。这条路线的宿主整合成本要单列。

### 3.4 Claude Code：文档清楚划了 shell 范围

[推断·官方文档 A2] 当前 sandboxing 页明确原生 Windows 命令 unsandboxed；Windows 想用这套沙箱要进 WSL2。macOS 用 Seatbelt，Linux / WSL2 用 bwrap / socat。默认 shell 可写工作目录、用户 temp 和已添加目录；读通常仍含凭据文件，环境变量也继承；网络通过本机域名代理。沙箱默认关闭，`failIfUnavailable` 决定不可用时是否启动即退出。未核验该行为在所有发布版一致。

[推断·官方文档 A2] Read / Edit / Write、MCP、hooks、LSP 等跑在 shell 沙箱之外，另循权限策略；某些 excluded commands / 经审批重试也可在沙箱外执行。官方建议整进程容器 / VM 用来覆盖这些入口。不能把“打开 Bash sandbox”描述为“整个 Agent 的一切动作都受同一个 OS 边界保护”。出处：同页 “What runs outside the sandbox”“Get started”“What the sandbox restricts”。

### 3.5 容器、Kubernetes 与只做策略的实现

[读码 H3] OpenHands `DockerWorkspace` 默认 `/workspace`、单独 API server、可找空闲 host port；`forward_env` 默认只列 DEBUG 与会话 API key 变量，`volumes` 显式配置，见 `workspace.py:70-121`。容器有自己的文件树和网络环境，但传进去的凭据、读写挂载仍然属于给出去的能力；默认 network=None 不能写成默认断网。LocalWorkspace 是另一执行模式，不自动得到 Docker 的边界。Windows 要使用容器端仍需可用的容器环境，不能称为原生 Windows token 沙箱。

[读码 H5] Agent Server 每会话容器默认内存 `4g`、CPU `2.0`、PID `512`、启动超时 `120s`，见 `openhands-agent-server/openhands/agent_server/config.py:431-434`。资源限制和读写 / 网络权限是两组东西。这里没有读到微虚拟机启动实现，本篇不把“容器”叫作 microVM。

[读码 G2] Goose 的 `crates/goose/src/agents/extension_manager/stdio.rs:20-34,40-49` 在提供 container 时用 `docker exec -i` 启动扩展，否则普通宿主 Command；builtin 扩展同样可进容器，见 `builtin.rs:16-29`。这证明有容器执行路径，不证明默认桌面 shell 已被 OS sandbox 包住。Unix Hermit 环境只负责依赖，不能作为隔离权限的证据。所读 container / extension_manager 入口没有自动 task worktree、合回及磁盘快照回退流程。

[读码 K1–K3] kubectl-ai Windows local executor 就是 `COMSPEC /c`；K8s executor 通过 Pod 执行、Close 删除 Pod。macOS `seatbelt_executor.go:43-48` 的注释说限制目录，但实际传 `(version 1) (allow default)`；按代码看没有落实该写限制，不能按注释给它打安全能力勾。Kubernetes 的名字空间、RBAC、Pod 网络和镜像权限由部署共同决定，不是 Windows 本地保护。

[读码 R2、P2、Q3、O2、L2] Crush shell blocklist、Pi 的 `BashOperations`、OMP 的 Git guard / brush、OpenCode 的 permission / local driver、Cline plan-mode guard 都是有用的执行策略或可替换入口，不能因此断言已阻止脚本直接打开其他文件。分别见 `crush/internal/shell/run.go:20-47,53-58`；`pi-mono/packages/coding-agent/src/core/tools/bash.ts:70-92,110-117`；`oh-my-pi/packages/coding-agent/src/exec/bash-executor.ts:689-715,766-775`；`opencode/packages/core/src/environment/environment.ts:18-33`、`local.ts:14-24,69`；`cline/sdk/packages/core/src/extensions/tools/command-guard-extension.ts:3-15,42-73`。Cline hook 在审批前把 plan-mode 改文件命令回成 tool error，所读路径没有 OS 令牌包装。

[读码 F1–F4、M2] Eino / ADK-Go 工具契约、Koishi loader、Magpie plugin host 也不能凭“有权限表 / 子进程 / 插件 scope”视为 OS 沙箱。Magpie 的宿主入口见 `magpie@5e0d7c058be2 internal/plugin/host.go:33-40,890-945`：释放 hostJS / piJS 后由 bunCommand 建进程，配置环境和 stdio，再 cmd.Start；进程分离有利于崩溃和生命周期管理，未在该启动入口读到 restricted token / AppContainer / bwrap。这是本次查阅范围内未实现的边界，不评价未读社区扩展。

### 3.6 凭据、环境变量与越权结果

[读码 D3、P2、H3；推断·官方文档 A2] 不泄露主程序密钥至少有三个独立入口：子进程环境、可读配置文件、网络目的地。删环境变量挡不住读配置；文件只读挡不住读取再联网发出；代理环境变量若可绕过也不等于白名单。DSH Windows 写沙箱明确还允许读和联网，Pi 本地 spawn 继承 shell env，OpenHands 则显式列转发变量，Claude 文档也说明默认凭据读与环境继承。不能仅凭“自管 Python”声称解决这些问题。

[读码 C3–C4、D2–D4、Z3、L2] 合适的错误形状在源码里已有：Codex 不支持读限制时明确拒绝；MXC 不支持 denied paths 时拒绝；DSH 报 unavailable / partial / 拒绝信号；Zed unavailable 与坏路径分类型；Cline plan mode 把不允许的调用作为可解释错误返回模型。审批层在此基础上决定是否另开一次、更宽权限的调用；不要把一次失败自动当“应该无沙箱重试”的授权。

## 四、worktree、任务副本、checkpoint 的实际生命周期

### 4.1 普通 Git worktree 解决什么

[实测 V1] 本次 Git 冒烟：从一个已经提交的文件建树，源目录随后改成 `source dirty`，子目录仍是 `committed`；忽略的 node_modules 与 `.env` 未带过去；子树 `.git` 是指针文件。Git worktree 共享对象库 / refs 等 Git 管理区域，自己的 HEAD / index / 工作文件分开。它不等于每任务独立用户、网络、端口或数据库。本次只测文件结果，不拿它当沙箱穿透测试。

[推断 V1] 依赖目录、开发数据库、输出、环境文件必须另定来源。共享包下载 cache 可以省下载；共用可写 node_modules、把数据库连接串仍指向同一个实例，都会把冲突带回来。端口也属于进程 / 网络空间，换目录本身不会让两个 `localhost:3000` 服务各占一个独立端口。这是基于工作目录与资源命名的推断，不声称本次已压测端口竞争。

### 4.2 OMP：先隔开副本，再只收任务增量

[读码 Q4] `oh-my-pi@5dc44d341772 packages/coding-agent/src/task/settings.ts:27-36,40-87` 的 isolated subagent 开关默认 false，开启只是允许 task 用 `isolated: true`；未指定的任务仍可在共享 checkout。后端名为 auto / apfs / btrfs / zfs / reflink / overlayfs / projfs / block-clone / rcopy。不是每台机器都能跑全部后端；文件系统 clone 不是访问控制沙箱。`worktree.clone` 另用于 shell Git 建树 / PR checkout 的 CoW 复用，默认 true，不能和 subagent isolation 开关混为一个。

[读码 Q4] `task/worktree.ts:19-21,648-697` 给任务建短目录段，merged view 统一放 `m/`；先写 owner，再启动 native iso 后端，避免并发清理误删尚在建立的任务。即使底层曾用 git worktree 或复制 `.git`，随后也 detach Git 元数据，借源对象库但拥有独立 HEAD / index / refs。子模块也单独处理。复制或 CoW 能带 ignored 构建物；rcopy / plain worktree 的能力不保证相同，不能承诺所有机器都零成本复用 node_modules。

[读码 Q4] `task/worktree.ts:33-47,292-330,411-443,946-987` 保存启动前 staged、unstaged、untracked 和 nested repo baseline。结束对比任务起点，只把 agent delta 合成 `omp/task/<taskId>` 分支；如果子任务自己提交，也会把对象转回父仓库，脏基线时重写以避免把用户原有 WIP 算作 agent 成果。这解释了为什么它比“git worktree add 一行”复杂：它想保留用户没提交的现场，又自动回收并行任务结果。

[读码 Q5] `task/worktree.ts:1054-1087` 在 repo lock 内逐分支 / 逐 revision cherry-pick，第一处冲突返回已成功、待合和 conflict，不假装全合完；`705-729` 清理先调用 native stop，挂载类卸载失败就保留目录和 backend sidecar，后续再处理，避免递归 rm 穿过仍活着的挂载。`1091-1099` 单独清临时分支。复杂后端因此还需要 owner、retain、恢复与故障路径，不是装上 ProjFS 就算完成。

### 4.3 OpenCode：策略管理和项目启动命令

[读码 O3] `opencode@8568952e77be packages/core/src/worktree.ts:204-248` 从 sourceDirectory 和 branch 调 strategy，名字冲突有限次加后缀，建成后登记项目 / 目录关系，并可在新目录跑项目 `commands.start`，给 `OPENCODE_WORKTREE_BASE`。它提供了装依赖 / 生成配置的接点，但命令执行本身不证明 node_modules 已正确准备，也没有自动分配应用端口 / 数据库。

[读码 O3] `worktree/git.ts:14-36` 把 create / remove / list 委托 Git；`git.ts:1428-1438` 删除明确传 force，模型工具 `tool/plugin/opencode.ts:36-43` 的 force 描述要求用户确认才丢弃修改。该生命周期入口没有自动“把所有任务变更合到主分支”的承诺。普通树删除与代码合并是两件不同的动作。

[读码 O4] `packages/core/src/snapshot.ts:45-49,95-103,133-146,201-219` 暴露 capture / files / diff / restore，以数据目录中的私有 Git 存储配合真实 worktree 捕获文件状态；`git.ts:409-410,714-735` 支持独立 GIT_INDEX_FILE 和临时 index，不需要每回合改用户 index。捕获对未跟踪文件有 `2 MiB` 大小限制，错误只告警并返回 undefined，因此不承诺任意文件都能恢复。文件快照恢复和工作目录隔离分开，不能把“有 snapshot”写成“每个会话都有独立 worktree”。本次未实测 restore，也没测外部数据库 / 未纳入快照的文件。

### 4.4 DeepSeek Harness：能创建，不承诺自动回收结果

[读码 D5] `packages/experimental/worktree/src/index.ts:55-67,95-105,119-173`：默认 `.agents/worktrees`，名字 `worktree-<UUID>`，默认 `HEAD`，解析为固定 commit；创建新 branch，不覆盖已有名字或目录。repo hooks、fsmonitor 与 checkout filter 被禁用以避免建树时暗中执行配置程序；成功后才更新当前 session workingDirectory。默认每命令超时 `60s`、grace `3s`、每输出流 `65,536` 字节。

[读码 D5] 注释明确未提交文件留源目录；创建失败可留新分支 / 目录，不自动删，报错包含路径。服务 dispose 只取消 / 等待创建操作，没有自动 merge / remove。也没有在这里复制 node_modules、分配端口或按 TTL 删任务树。它在 experimental 包里，应按“保留树的创建服务”理解，不补写成完整并行任务管理器。

### 4.5 OpenHands：会话、工作树、容器和磁盘回收分层

[读码 H4] `OpenHands-sdk@ openhands-agent-server/openhands/agent_server/conversation_service.py:164-196,220-303` 规划每会话树为 `<conversation_worktree_root>/<conversationId>/<repoName>`，branch 由 prefix + ID 组成；可用默认分支 / HEAD / 指定 ref。不带用户没提交的修改；非 Git 工作区可能不建树。创建代码有清同名残留的 force 路径，不应未经所有权设计原样搬到一个只凭目录名判断的宿主程序。

[读码 H4] 同文件 `1875-1891` 明确 fork 共享原 worktree，只有没有其他会话记录引用它时才删。因此“会话已 fork”不代表“文件已分叉”；对同时写的 fork 必须另看执行策略。删除会话、删 runtime、合代码不是一回事，本次没在该创建 / 删除流程看到自动代码 merge。

[读码 H5] 清理重点在 `storage/model.py:11-15`、`storage/reclaimer.py:72-77,107-110,120-185`：

| 层级 | 什么时候 | 可以清什么 | 不能据此推成什么 |
|---|---|---|---|
| CACHES | 启动 / stop 等生命周期点 | sandbox home 的可再建 cache | 不等于把整个会话删掉 |
| DEPENDENCIES | 开了 disk budget 且所在文件系统使用率超阈值 | 闲置 runtime 中由服务创建的工作区里可重装的 ignored 目录 | 不代表所有 ignored 文件、所有用户工作区都能删 |
| RUNTIME | 配了 retention 且闲置到期 | 整份 sandbox home / 工作区，保留历史与身份供阅读 | 已归档会话不再可恢复执行 |

[读码 H5] `config.py:435-462` 的 disk_budget 是**所在文件系统已用比例**，不是每个会话 N GB 配额；默认 None 关闭。retention_days 也默认 None；不是默认几天后删任务。reclaim 时先取 idle 锁，活跃 runtime 跳过；只在锁内把目录移进 trash，真正删除另做。这降低持锁时长并避免清理正在使用的目录。依赖删了能重装、用户工作没自动删，是所有权区分的结果，不能只靠 `.gitignore` 盲扫整盘。

### 4.6 Zed：归档之前把现场保存下来

[读码 Z4] `crates/agent_ui/src/thread_worktree_archive.rs:77-92,172-182,484-515` 只归档 Zed 管理根目录下的 linked worktree，主 checkout、用户在外面创建的目录不自动接管。创建两个 detached WIP commit：staged 匹配 index，unstaged 包含未跟踪工作文件；DB 存原 HEAD、branch、路径和 commit，`refs/archived-worktrees/<id>` 防 Git GC，线程引用指向归档记录。

[读码 Z4] 同文件 `210-216,264-268,641-689` 删除前让所有打开项目释放引用，核对记录的目录创建时间；外部删后重建的同名目录不能当原工作区删。恢复时从原 commit 重建树，再恢复 staged / unstaged 状态。它解决的是长时间保存又不占整份工作目录，代价是 Git / DB 两份状态必须协调。ignored 的构建缓存、运行中端口和外部数据库不由上述 WIP commit 自动恢复。

### 4.7 Cline：checkpoint 不是并行任务目录

[读码 L3] `cline@ sdk/packages/core/src/hooks/checkpoint-hooks.ts:11-48,341-355,594-632` 用 stash 兼容提交捕获工作树，并为非 ignored 的 untracked 文件构造第三父提交。放 `refs/cline/checkpoints/<sessionId>/<runCount>`，不占用户常用 stash 列表。每 cwd + session 哈希一个持久临时 index，复用 stat cache，避免每轮重新 hash 所有未跟踪字节。

[读码 L3] 同文件 `14-19,57-68,437-459`：scratch 在 Cline 私有数据目录，14 天 mtime 老化时机会清；删会话清专用 refs 与 scratch。14 天是 scratch 清理，不是“14 天后删除项目代码”。快照失败会告警并尝试只记 HEAD，`603-621` 明确 head_fallback 与正常 head_clean 不同；HEAD fallback 没保存脏文件，不能宣传完全可回退。

[读码 L4] `sdk/packages/core/src/session/checkpoint-restore.ts:393-425` 在还原前检查当前 HEAD 是否仍等于快照 base；若新提交 / rebase / 切支改变了它，拒绝恢复文件，仍可只恢复聊天。恢复还涉及对未跟踪文件的清理与备份，见同文件 `43-49`。这套机制回退共享目录现场，不会给并行 agent 自动隔开修改。ignored 数据库 / 服务端副作用不属于 Git 快照的自动撤销范围。

### 4.8 Claude Code：只可按文档描述闭源生命周期

[推断·官方文档 A3] [Worktrees](https://code.claude.com/docs/en/worktrees)：“Start Claude in a worktree”“Customize worktree creation” 规定默认 `.claude/worktrees/<name>`、branch `worktree-<name>`，可自动起名；baseRef 默认 fresh（远端默认分支），可用 head；缺远端等情况回落本地 HEAD。新树只含 tracked 文件，依赖要安装；`.worktreeinclude` 可额外复制 ignored 文件，包括 `.env`，这同时意味着把凭据带入子树，不能默认全部复制。

[推断·官方文档 A3] “Clean up worktrees”“Clean up subagent and background-session worktrees”：干净 unnamed 交互会话可自动移除，named / 有工作 / 状态无法核实时提示；`-p` 非交互留树。运行任务持 git worktree lock；周期清理按所有权 marker、保留期限、活动锁、修改 / unpushed commits 决定，用户自己建的树不随便删。Windows junction / symlink 删除只移除链接是文档承诺，本次未实测该删除器。

[推断·官方文档 A3] “How Claude Code enforces isolation” 明确只检查 Edit / Write 目标、命令 cwd、Git 重定向与可分析命令形状；不追踪 shell 实际写了哪些文件。普通 cp / redirect 仍取决于沙箱 / 权限。因此同页也没有把 worktree 检查说成安全沙箱。合回依靠 Git / PR 工作流，不是退出即自动 merge。

### 4.9 其余项目和 Codex 的本次读码边界

[读码 C5] `codex@ codex-rs/git-utils/src/worktree.rs:14-23,33-89,92-135` 从 `.git` 指针、commondir、反向 gitdir 检查 repository identity，枚举 sibling checkout 的对应 cwd。这里是已有树发现，不是创建 / 自动合回入口。所读 CLI / git-utils 没形成“每个子 agent 自动建树”的证据；桌面产品的 worktree 生命周期未纳入本次源码证明。当前 `git-utils/src/baseline.rs:74-89` 的 reset 是内部 derived-data baseline，会替换内部目录的 .git，明确禁止用于用户仓库；不能把这个函数推荐为用户 rewind。

[读码 F2] Eino `compose/checkpoint.go:53-88` 是 graph 序列化 / checkpoint store / 分叉写入 ID，不快照任意文件。ADK-Go 的 tools / confirmation、Koishi loader、Magpie plugin host、Crush shell、Pi 本地 Bash、Goose 扩展容器启动入口均没有在所读路径提供任务树创建→合回→失败保留→清理的一条龙实现。[推断] 这些项目可以由上层集成 Git worktree，但“可以集成”不能写为默认已有。kubectl-ai 的 Pod Close 删除容器资源，也不会自动还原 Kubernetes API 已经产生的远端变更。

## 验证记录与证据边界

[实测 V1] 临时脚本 `smoke.mjs` 用本机真实 Git / uv，在任务 scratchpad 自建仓库与编的文件，没有读写真实 Sumika data。结果时间 `2026-11-04 23:02:11`；Git `2.56.0.windows.3`、uv `0.13.6`、Bun `1.4.4`。完整结果保留在任务 scratchpad `agent-env/smoke-result.json`；关键结果抄在这里，不依赖临时文件长期存在：

| 操作 | 观察结果 |
|---|---|
| 源仓库提交 code.txt 后再修改，再建树 | 源 `source dirty`；子树 `committed` |
| 源 ignored node_modules、.env | 子树两者均不存在 |
| 检查子树 Git 元数据 | `.git` 是文件 |
| 子树有修改时普通 `git worktree remove` | exit `128`，报 contains modified or untracked files，目录保留 |
| 子树恢复干净再普通 remove | 子树移除，源目录未提交文字仍在 |
| 设置 UV_CACHE_DIR 后 `uv --no-config cache dir` | 返回指定自管 cache 目录 |
| 设置 UV_PYTHON_INSTALL_DIR 后 `uv --no-config python dir` | 返回指定自管 Python 根 |
| 设置 UV_TOOL_DIR 后 `uv --no-config tool dir` | 返回指定自管工具环境根 |

[实测 V1] 冒烟脚本最终一次退出 `0`，耗时 `1.74s`。前一版脚本尝试仅把文件内容改回原文后删除树，Git 仍以 dirty 拒绝；修正为显式更新临时树 index 后才走干净删除。这个过程不作为性能对比，`1.74s` 也不是 worktree 创建速度基准。uv 仅查询目录，未下载 Python / 包，不能声称已证明环境创建、包安装、空间占用和清理效果。

[读码 / 未实测] 其余结论由各节路径证明；未执行各家完整客户端、沙箱、容器或 checkpoint restore。没有新增产品测试或修改测试断言。文档提交只跑现有文档 / 隐私 / 冲突 / 提交说明检查；结果以实际提交输出为准。没有调用外部模型，没有运行 / 停止 Sumika 或连接真实 QQ 协议端。

## 五、对 Sumika 的建议（均是我的建议，未决定实施）

### 5.1 先核对现有基础

[读码 S1] 文档 worktree 基线 `sumika@1c6cc589414f` 的 `scripts/fetch-tools.ts:1-18,47-92` 已为 rg / fd 钉版本与 archive SHA-256，下载后校验，放 repo 的 `vendor/bin`；存在就跳过，失败警告。`native/internal/tools/search.go:20-41` 却先查 `AgentBinDir()` 下程序，再 PATH；`native/internal/tools/shell.go:81-96,106-126` 的 AgentBinDir 沿用 `PI_CODING_AGENT_DIR` 或 `~/.pi/agent/bin`，shellEnv 前置该目录。两份位置需要沿当前桌面 / 后台装配核对，不能只凭旧 daemon 曾改 PATH 就断言所有入口统一了。本次没改它，也没启动 Sumika。

[读码 S1] `native/internal/tools/shell.go:43-78` 当前选择系统 pwsh / PowerShell，普通 `exec.Command` + shellEnv，超时和进程回收另管。去掉少数 PI_* 会话变量不等于剥除所有服务密钥；审批也不等于 OS sandbox。现有底子是搜索工具分发、命令入口、审批和生命周期，不是已经有完整运行时 / 沙箱 / task worktree 管理器。

### 5.2 目录：本体、可执行工具、缓存、任务成果各有归属

[推断·我的建议；借 C1、M1、U1、H5] 沿已定的数据根选择规则，提供一份**自管根目录**，概念结构如下，具体安装路径不在本次拍板。只让 Sumika 的执行子进程用这份 PATH，程序内部调用用绝对路径；不改系统 / 用户 PATH。

```text
<Sumika 自管根>/
  tools/<tool>/<version>/<platform>/       # rg、fd、uv 等可重取程序
  runtimes/bun/<version>/<platform>/       # 需要脚本时才安装
  runtimes/python/                         # UV_PYTHON_INSTALL_DIR
  environments/<skill-or-tool>/<lock-id>/ # 确有依赖冲突时的 venv / JS 依赖
  cache/uv/                               # UV_CACHE_DIR
  cache/bun/                              # BUN_INSTALL_CACHE_DIR
  tasks/<task-id>/work/                    # 普通文件任务
  tasks/<task-id>/tmp/                     # 每任务临时目录
  tasks/<task-id>/outputs/                 # 交付物，先保留再按规则处理
```

[推断·我的建议] Git worktree 位置要按获准的仓库写入范围确定，可在仓库私有目录或专用工作区根；不能为了统一路径悄悄扩大审批范围。Git 元数据在 common dir，沙箱写白名单除了 worktree 还得明确哪些 Git 控制路径能写。借普通 Git / DSH retained-tree 的方案，先不引 ProjFS / CoW 框架；无 Git 的资料任务用 task work 目录，不为其伪造 Git 仓库。

[推断·我的建议；借 C1、O1、S1] 工具清单最少记录 tool、精确版本、OS/arch、来源 URL、SHA-256、许可、可执行相对路径。更新在运行任务之外下载到临时位置，验证能启动后切换；任务持有具体版本路径，旧版本等无人使用再删。默认自带版本可复现；如提供系统路径覆盖，明确把它标为用户管理，不自动升级 / 删除。先统一已有 rg / fd 下载与查找口径，不另起第二套下载器或泛化包管理框架。

### 5.3 哪些 exe 值得带

| 工具 | 本次参考 | 我的建议与理由 | 有无底子 / 现在做不做 |
|---|---|---|---|
| rg、fd | Pi / OpenCode / Sumika S1 | 保留；搜索与找文件的直接依赖 | 已有下载与调用；未来统一目录时一起改调用，不新开功能 |
| uv | OpenHands / uv U1–U3 | 作为 Python 安装与依赖工具，按需带，版本 / hash 钉死 | 用户当前已有 uv 使用基础；Sumika 自管交付仍要接安装器，不在本次实现 |
| Bun | Magpie M1 | 跑需要的 TS / JS 脚本；按需一份 runtime，不为每任务装一份 | Go 后台独立于 Bun；需要脚本能力时补，不能把退役 Bun 后台搬回来 |
| Python | DSH D1 / uv U1 | 用 uv 管一份或明确需要的少数版本，再按依赖建 venv | 与生产嵌入迁移分开；脚本环境不是重启旧 Python 嵌入服务 |
| Git | Codex / DSH / OpenCode 的 Git 调用 | 首先检测已有 Git；做 worktree 需要它，但完整 PortableGit 还有 shell、SSH、证书和升级负担 | 当前开发机有 Git；给终端用户是否捆绑需按实际任务选，不默认全带 |
| Node / npm / npx | Zed / OpenHands H1 | 只有依赖真实 Node 行为的 MCP / 工具才增加；Bun 不能未经验证代替全部 Node 程序 | MCP 尚有自己的推进顺序；出现真实兼容需求再做 |
| sd、sg | OMP Q1 | sd 是替换工具、sg 是语法树搜索；已有 Edit / search 是否缺能力先量 | 备选，不因别人带了就默认加 |
| yt-dlp、ffmpeg、pandoc、jq | OMP 证实 yt-dlp；其余在本篇仅为任务例子 | 媒体 / 转码 / 文档 / JSON 工具由对应能力按需声明；不宣称上述 Agent 都内置 | 有明确任务再列清单；本次不增加 |
| rtk | 本次所列打包入口未给出默认捆绑证据 | 它的输出压缩需与现有输出预算 / 可续读原文对照；不是运行 shell 的必需品 | 候选，先量 token 与丢信息情况再决定；不安装 |

[推断·我的建议] 用户原话里的 `rd` 保留原意未擅自对应某个项目名；本次确认的找文件程序是 fd。`grep` 工具名也不必对应另带 GNU grep.exe：后台可以调用 rg，或用原生库提供搜索。exe 清单以实际消费者为准，避免把一整套 Unix 环境当作工具箱最低要求。

### 5.4 Python / Bun 的隔离和清理

[推断·我的建议；借 U1–U4、D1] Python 解释器集中共享，长期 CLI 交 uv tool，固定 skill 的外部包用 skill / 工具自身锁文件建环境；无依赖脚本直接用 managed Python。临时任务确需特有依赖再建临时 venv。不要默认一会话一个完整 Python，也不要共用一份可无限 `pip install` 的万能环境。Bun runtime 可共享，固定脚本 / 工具的 package.json、lock 和 node_modules 有自己的归属；共享下载 cache，不默认共享可写 node_modules。

[推断·我的建议；借 H5、Z1、U3] 清理按“是否可重建 + 是否有人使用 + 是否用户成果”分：下载临时包在成功 / 失败后清；运行时旧版本待任务引用释放后清；uv cache 用其原生命令 / 锁；临时依赖可在任务结束后删；outputs、未提交 worktree、未合分支不能按 cache 自动删。Bun cache 在无安装活动时才能考虑由应用清自管目录，不碰用户全局 cache。没有实测磁盘占用前不编一个 2 GB / 7 天的默认门槛；先显示大小和清理类别，配额与保留期限作为单独产品决定。

[推断·我的建议] “干净可控”的实际收益是能回答“文件在哪、谁创建、还被谁用、删了能否重建”，不是目录名换成 Sumika 就得到安全。Windows 被占用文件、杀软隔离、中文 / 空格路径、x64 baseline / ARM64、解压器选择要借 O1 / M1 的处理，发布签名 / Defender 误报则本次未验证；将来验收用真实干净安装，不写成已支持。

### 5.5 Windows 沙箱分两条候选，不改现有审批默认值

| 候选 | 借谁 / 能力边界 | Sumika 已有基础 | 我的建议 / 实施时机 |
|---|---|---|---|
| 宿主执行 + 任务目录 + 现有审批 | Pi / 当前 S1；可审调用，不能限制获批脚本实际访问 | 有 shell、审批、超时、进程回收 | 如继续现状，界面准确写“宿主执行”；本次不新开权限 |
| 原生受限进程 | DSH 写限制 / Codex elevated；读隔离、网络策略、ACL 安装成本不同 | 有命令统一入口，可接 launcher；未有 token / ACL / 身份安装器 | 先用临时目录和少量真实程序做可撤销验证，明示 partial；不直接承诺全盘秘密不可读 |
| 原生 MXC | Codex C4；依赖可用 native process security environment，能力不符拒绝 | 未有对应集成 | 作为额外原生候选，先核系统支持和维护成本；不是默认选中 |
| WSL2 / Docker 执行端 | Zed / OpenHands；另一个运行世界，解释器、路径与安装都要成套 | 当前原生 Windows 工具不能直接照搬 | 用户任务确需强环境隔离再做，不能偷偷把 PowerShell 任务改成 Linux 任务 |

[推断·我的建议；借 D2、A2、Z3] 启用某种沙箱后，工具结果应带执行位置 / 模式 / 实际约束 / 失败类别。后端不可用直接解释原因；扩大范围是另一次审批，不自动降级为宿主裸跑。应用自己的配置和密钥只留在宿主，子进程使用任务需要的环境变量；若任务必须读真实凭据，按既有授权显式供给，而不是复制整份 `.env`。这会涉及权限默认值与真实配置，必须另立规格 / 决定，本次仅给候选。

### 5.6 worktree 怎样接后台 Agent

[推断·我的建议；借 D5、O3、Q4–Q5] 只读调研共享源目录即可；多个会修改代码的任务才各建 Git worktree。从已知 base commit 起，用 task ID 命名，记录 repo common dir、branch、路径和 owner；未提交源目录修改不自动搬入，确有需要再选择是否记录 / 复制基线。先采用保留任务树、交回 commit / diff、主会话串行合回的流程；不要第一步就实现 OMP 那套脏基线自动重写与多文件系统后端。

[推断·我的建议] 任务生命周期最小链条：创建任务记录 → 建树 → 在该 cwd 配必要环境 → 执行 / 停止 → 留 commit 与产出 → 合回并解决冲突 → 确认成果已回收后清树。后台重启先识别自己的 owner / 活动状态，不能凭 `task-*` 名字直接递归删除。失败 / 合并冲突保留树与分支，告诉主会话位置；同一父仓库合回串行，复用已有任务状态与审批，别建第二套调度框架。

[推断·我的建议；借 V1、A3、H5] 依赖用各树自己的锁文件安装，共享包缓存；端口按任务分配或串行运行，数据库用任务私有路径 / 连接目标。Git worktree 的 `.git` 是文件，路径保护和建树识别不能只接受 `.git/` 目录。回退代码可用 Git / checkpoint，但网络发送、真实库写入、服务部署不能承诺跟着回退。现有 Sumika 正在用的真实数据目录、QQ 服务和宿主主配置不属于任务树。

[推断·我的建议] 实施顺序建议是：①统一已有工具目录与来源清单；②按实际脚本任务接自管 uv / Bun 和可重建依赖目录；③任务确有并行修改需求时接普通 Git worktree；④Windows 沙箱单独验证选型。依据是已有 S1 能复用、V1 能先给明确文件行为、D3 / C3 / Z3 表明沙箱还有平台成本。现在只交这份调研，不把建议转成已批准的 ADR / 功能承诺。

