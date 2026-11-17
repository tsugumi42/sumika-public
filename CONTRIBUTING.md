# 参与 Sumika 开发：上手指南

给想改 Sumika 代码的人。先读 [README](README.zh-CN.md) 知道它是什么，再照这份把环境搭起来、知道该读什么、怎么验收和提交。

## 先知道的几件事

- **目前只支持 Windows。** 桌面程序、git 钩子（会调 PowerShell）、rg 和 fd 的下载、读密钥（DPAPI）都只按 Windows 做过。
- **主线是 Go。** 后台和原生界面都在 `native/`（Go 1.27），界面用 mygo + CrychicUI 自己画，不是网页。旧的 Bun 后台和网页界面已经退役（ADR-203），2026-11-01 分到了单独的仓库 sumika-bun（不带历史；私有远端只做备份，本机不留，要看就克隆），只当历史资料，不再加功能、不再修；Go 代码注释里写的 `daemon/…`、`web/…` 和主仓库已经没有的 `scripts/…` 脚本（对拍生成器 `go-golden-turn.test.ts`、`gen-go-policy.ts` 等；照 JS 行为生成样本的 `go-golden-json.ts`、`gen-go-unicode.ts` 还在主仓库）指那边的同名文件。
- **脚本还用 Bun。** 退役的是 Bun 后台，不是 Bun 本身：git 钩子、文档检查、隐私扫描这些脚本（`scripts/*.ts`）都由 Bun 跑，所以开发机上仍要装 Bun。
- **数据不进 git。** 角色卡、配置、聊天记录、记忆都在数据目录 `data/` 里，被 `.gitignore` 挡着。仓库里只有一张示例角色卡（`characters/example/`）和示例配置（`sumika.example.yml`）。
- **测试绝不碰真实数据。** 测试只用临时目录（Go 的 `t.TempDir()`），不读写 `data/`，也不读写 MaiBot、AstrBot、SnowLuma 的文件。测试和示例里的 QQ 号、群名、昵称一律编（`100`、`200`、"编的甲"这类）。

## 一、搭环境

1. 装 [Go](https://go.dev/dl/) 1.27.3 以上（以 `native/go.mod` 里的 go 行为准）。作用：编译后台和界面、跑测试。
2. 装 [Bun](https://bun.sh/)。作用：跑 git 钩子和 `scripts/` 里的检查脚本。
3. 在仓库根目录执行：

   ```bash
   bun install
   ```

   作用：装脚本用的依赖；装上 git 钩子（lefthook）；把 rg、fd 两个小工具下载到 `vendor/bin/`（按钉死的版本和 SHA-256 核对，来源只有两个项目的官方发布页）。内核的工具对拍测试要用这两个工具，缺了会红。

   装完自查两样：`.git/hooks/` 里有没有 `pre-commit`、`commit-msg`、`pre-push`，没有就执行 `bunx lefthook install`；`vendor/bin/` 里有没有 `rg.exe`、`fd.exe`，没有就执行 `bun scripts/fetch-tools.ts` 重试（下载失败时 `bun install` 只警告、不报错）。
4. 可选，按要做的事装：
   - [Magpie](https://github.com/yetone/magpie) 模型网关（默认 `127.0.0.1:3425`）：要真调模型时才需要。
   - SnowLuma：要接真实 QQ 小号时才需要。Sumika 只连接它，不负责安装和登录。
   - 本地嵌入服务：记忆的向量检索用，可选。Go 后台现在借用 MaiBot-Embeddings 项目的 `server.py`，在配置的 `embed` 一节写全 `python`、`serverDir`、`model` 三项（见 `sumika.example.yml`），receive 模式会自己把它当子进程起来。不配也能跑：记忆检索只用全文，发送前守卫只比原文。（`tools/local-models/` 是新的多模型服务，Go 还没接上，见 规格-本地模型服务（私有存档）。）

开了新的 git worktree 时，`node_modules/` 和 `vendor/bin/` 都不会跟着过去（被 git 忽略），在 worktree 里再执行一次 `bun install`；只缺工具的话，也可以把主工作区的 `vendor/bin/` 拷过去。

## 二、编译和运行

在 `native/` 文件夹里：

1. 先确认能编过：

   ```bash
   go build ./...
   ```

2. 编出桌面程序并打开：

   ```bash
   go build -trimpath -ldflags "-s -w -H windowsgui" -o sumika.exe .
   ```

   ```bash
   sumika.exe -root ..
   ```

   `-root` 指向有 `sumika.example.yml` 的仓库根目录；不给时用 exe 所在的目录，在 `native/` 里编出来的 exe 旁边没有这个文件，所以开发时要带上。还没有 Go 的数据时窗口只显示"后台没在运行"和原因，这是正常的；只想看界面、不碰任何库，用 `sumika.exe -demo` 看编的演示数据。这样编出来的程序不带命令行窗口，出错信息在窗口的状态页里；想看命令行输出，编的时候去掉 `-H windowsgui`。
3. （可选，已经有 Go 的数据目录时）跑无界面的后台，只读打开它（不连 QQ、不调模型、不建库）：

   ```bash
   go run ./cmd/sumika-go -mode archive -data <数据目录>
   ```

   数据目录不存在或没初始化会直接报错退出。它和桌面程序抢同一把数据锁，两个不能同时开（关桌面窗口只是隐藏，要从托盘菜单退出）。想用自己的配置时，把 `sumika.example.yml` 复制成 `data/config/sumika.yml` 再改；不复制就全用默认值。

所有命令行入口（`native/cmd/` 下十几个）和参数见 [docs/subsystems/go-tools.md](docs/subsystems/go-tools.md)；界面的参数见 [native/README.md](native/README.md)。

## 三、测试

下面的 `go test`、`go vet` 都在 `native/` 里跑，`bun scripts/...` 在仓库根目录跑。

- **改的过程中**：只跑你动到的包，比如：

  ```bash
  go test ./internal/channel/snowluma
  ```

- **合进 main 之前**：在分支上把改动都提交、先合上最新的 main，然后在仓库根执行：

  ```bash
  bun scripts/go-verify.ts
  ```

  它挑出改了文件的包和它们的直接导入方（根包除外，只多一层，ADR-362），低优先级对整个模块跑 `go vet`、对这几个包跑 `go test -short -count=1`，退出码都要是 0（根包、间接导入方和慢的测试由每天的巡检整套跑）；最后一行写着跑了几个包、结果如何，抄进合并的提交说明。过了的包它会记下"这份代码上验过了"，合并后推送时钩子见到同一份代码就不再跑；有几个包红了，改好再执行一遍，只跑没过的。工作区里还有没提交的改动时它不跑。想先看会挑哪些包，用 `bun scripts/go-affected.ts <文件>`；如果挑出来的是"整套"，说明你改的东西（`go.mod`、提示词、示例角色卡、示例配置等）会被很多测试读到。
- **整套**：`go test ./...` 很吃 CPU，手动跑时在 `native/` 里用低优先级跑，退出码原样带出来：

  ```bash
  powershell -NoProfile -ExecutionPolicy Bypass -File ../scripts/run-low.ps1 go test ./...
  ```

  整套平时由维护者那边的每日巡检跑；想在推送时让钩子跑整套，设环境变量 `SUMIKA_PUSH_FULL=1`。
- **文档检查**：改了 `docs/`、配置示例或 ADR 时跑 `bun run check:docs`（提交时钩子也会跑）。

## 四、提交

提交和推送前的检查都挂在 git 钩子上（`lefthook.yml`），按改了哪些文件自动挑：

| 时机 | 查什么 |
|---|---|
| 提交前 | 隐私扫描（号码、昵称、密钥）、文档检查、漏解决的冲突标记、改了 `.ts` 时的 TypeScript 类型检查、Go 格式、`go vet` |
| 写提交说明时 | 标题 40 个字以内、不带分号、不带 ADR 编号 |
| 推送前 | 这一段提交的隐私扫描，整个模块 `go vet`，改到的 Go 包 `go test -short`（合并前验过的不再跑） |

红了就改代码或补基线，**不要用 `--no-verify` 绕过**。

写提交的几条规矩：

1. 一个提交只做一件事；顺带发现的另开提交。提交前 `git diff --stat` 看一眼，出现两类不相干的文件就拆开。
2. 标题写这次改完以后会怎样（"协议端填的号码 0 不再当成真号"），不写过程；正文三到八行，写读的人用得上的，ADR 编号放正文。
3. 修 bug 要带一个在旧代码上会红的测试；新加的检查和断言先故意改坏一次，确认它真的会红。
4. 写进提交说明的检查结果，从输出里照抄数字，不写"都过了"。

## 五、先读什么

按这个顺序：

1. [docs/词汇表.md](docs/词汇表.md)：先分清**四种场合**（本地私聊、本地群聊、QQ 私聊、QQ 群）。谁能看到什么、谁能做什么都按场合定，讨论和代码里一律四种都点到。
2. [AGENTS.md](AGENTS.md)：项目约定（验收、文档分层、改什么要同时改哪里、技术约束、安全边界）。
3. [LESSONS.md](LESSONS.md)：之前做错过什么、当时能拦住它的规则。改代码前先读。
4. [docs/待办.md](docs/待办.md) 顶部"现在的顺序"：现在先做什么、什么卡着。
5. [docs/README.md](docs/README.md)：所有文档的索引，以及"每种事实以哪份为准"。
6. 要改哪一块，就读那一块的规格：[规格-Go后台](docs/规格-Go后台.md)（内核）、规格-Go后台装配（私有存档）（怎么接成一个能跑的程序）、[规格-渠道中间件](docs/规格-渠道中间件.md)（收、存、认人、闸门、发送）、[规格-记忆-v2](docs/规格-记忆-v2.md)。规格是设计和进度混写的长文，先看开头的"状态"。

注意：[docs/subsystems/](docs/subsystems/README.md) 里除了 `go-tools.md`，其余参考页写的还是 Bun 版，Go 的现状以规格和代码为准。

AGENTS.md 里提到的"上级目录的 AGENTS.md"是维护者本机放通用约定的地方，不在这个仓库里；和提交、验收有关的要点已经写在这份和 AGENTS.md 里。

## 六、代码地图

`native/` 下面：

| 位置 | 是什么 |
|---|---|
| `main.go` 等根目录文件 | 桌面程序：主窗口、托盘、单实例、只读界面 |
| `cmd/sumika-go` | 无界面的后台入口（`archive` 只看历史、`receive` 连一个 QQ 小号） |
| `cmd/` 其余 | 开发和评测工具：录 SnowLuma 帧、记忆和守卫评测、压测、闸门报告…… |
| `internal/app` | 桌面和命令行共用的后台装配：把下面各块接起来 |
| `internal/turn` | 对话内核：一轮怎么拼请求、流式回复、调工具、写库；插件从这里挂进来 |
| `internal/tools`、`approval`、`policy` | 她能用的工具，执行前的审批关和判法 |
| `internal/llm`、`prompts`、`persona` | 模型调用（OpenAI 兼容接口）、提示词、角色卡 |
| `internal/channel` | 渠道中间件：收消息、入库、认人、闸门决定要不要开口、发送；`snowluma` 是 QQ 的 OneBot 适配器，`qq`、`repeat` 是 QQ 平台插件（戳一戳、复读等），`sendlock` 是发送权的跨进程锁，`types` 是各渠道共用的类型，`fake`、`contract` 是测试用的假协议端和适配器契约测试 |
| `internal/audience` | 这一轮谁在听、在哪听：决定能用哪些记忆和工具 |
| `internal/embedinputs` | `memory-eval -dump-embed-inputs` 写、`cmd/models-parity` 和 `tools/local-models/parity_ref.py` 读的"生产实际会送去嵌入和重排的字符串"文件格式（含真实原文，写之前问 git，在工作树里没被忽略就拒绝） |
| `internal/memory`、`memplugin`、`embed` | 记忆：召回、写入、发送前守卫；接进内核的适配器；文本向量 |
| `internal/embed/embedtest` | 测试程序扮演的假 `sumika-models.exe`（embed、app、memory-eval、guard-eval 的测试共用；登记在 archcheck 的 `toolOnly` 里，不进常驻程序） |
| `internal/pyspace` | Python 的 `str.strip()` 认的空白集合，只依赖标准库：嵌入服务（`engine`）判空和记忆切句（`memory.OutboundSentences`）调同一份，切句不会切出服务眼里的空句 |
| `internal/localmodels`、`third_party/goSentencePiece` | Go 版本地模型（规格-Go本地模型，GM1～GM7 做完了分词器、ORT 封装、引擎、模型池、HTTP 服务五层、对拍命令，和后台起它：`embed.Local` 在 `embed.backend: go-models` 下起 `cmd/sumika-models`，默认仍是 `legacy`）：`tokenizer` 加载 gte 和 bge 的分词器；`ort` 是 ONNX Runtime 的薄封装（动态库哈希门）；`engine` 是登记表、池化、归一化、重排和每行一锁；`pool` 是模型池（按需加载、空闲卸载、同时上限、按实测内存记账）加上从模型目录加载的 `FileLoader`；`server` 是 HTTP 层加进程外壳（路由、令牌、错误码、启动检查和退出码、看门狗、stdin 关闭就退出），只有它可以 `net/http`；`cmd/sumika-models` 只是入口；`cmd/models-parity` 是对 Python 版的对拍命令（起 Go 版和 Python 版各一份，比分词 id、向量、重排、相似度差和翻转、速度、内存，只出计数和统计）；`third_party/goSentencePiece` 是拷进来自己改的 tggo fork（来源和改动见它的 `PROVENANCE.md`）。只给 `cmd/sumika-models` 和以后的对拍命令用，桌面程序和 `sumika-go` 不许链接 |
| `internal/linkreader`、`linkplugin` | 链接解析（X、B 站）和它接进渠道、内核的适配器 |
| `internal/store`、`history`、`paths`、`config`、`secrets` | 聊天库和运行记录库、重建给她看的历史、数据目录路径、配置、密钥（只读 Bun 的密钥文件） |
| `internal/api`、`desktopread`、`alert` | 给界面的只读接口和它的实现、"现在有什么事没恢复"的提醒登记 |
| `internal/datalock`、`mediastore`、`bunseg` | 数据目录的排他锁、读写 Go 的媒体库、Bun 旧消息段的格式 |
| `internal/skills`、`projectcontext`、`textclean`、`jsdiff`、`jsjson`、`jsstr`、`bench` | 技能和项目资料读取、模型回复的清理、照 JS 行为写的几个小工具、评测执行器 |

扩展点（插件）都是编进程序的 Go 代码，不在运行时加载第三方插件。现在有四类：内核的 `turn.Plugin`、渠道的拦截器（`channel/types` 的 `Interceptor`）、入库后的观察者、平台适配器。怎么统一还在讨论（GitHub issue #16），写新插件之前先看那里。

## 七、安全和隐私的底线

- 代码里不写任何供应商的 key；key、令牌不进 git、不进日志。
- 工具权限按"谁在说话、从哪个渠道来"决定：只有维护者本人在本地主窗口能用执行类工具，而且要审批；QQ 渠道里不出现任何本地执行能力。
- 改"谁能看到什么、谁能做什么"的代码：先把四种场合和敌对输入（冒充名字、怪号码、伪造字段）列成表再动手；身份按号认，不按名字；取不到、出错时的兜底只能收紧，不能放宽。
- 不修改 MaiBot、AstrBot、NapCat、SnowLuma 的配置和数据；不自动部署或启动 SnowLuma（它的许可限制），只连接和监控。
