# 规格：仓库脚本和钩子改写成 Go

> 状态：草稿（2026-11-18）；进度：第 1～4 步已合 main（第 2 步合并提交，第 3 步，第 4 步小关卡见合并 chore/sumika-dev-4 的提交）；第 5 步的通知（`report-notice`）在分支 feat/notice-go 上，已迁成 `sumika-dev notice`，并加了“main 上 CI 失败带进上下文”，待合 main。来源：ADR-389（主人原话见那条）。参考：盘点 `data/dev/research/盘点-脚本迁Go-Sol-.md`（Sol 只读盘点，每个文件一行，带出处；下文“盘点”指它）；[规格-合并走PR](规格-合并走PR.md)（合并队列和导出器的去留跟它走）。

## 开工卡

- **我理解成**：`scripts/`、`test/` 里还在用的工具，一个一个改写成 Go，放进一个开发工具程序 `sumika-dev` 的子命令里；每迁一个，钩子、文档、技能里的调用同提交换成新命令，旧 TS 同提交删掉。全部迁完后去掉 `package.json`、`bun.lock`，CI 不再装 Bun。
  - 正例：`lefthook.yml` 里“文档检查”从 `bun scripts/check-docs.ts --staged` 换成 `native/bin/sumika-dev check docs --staged`；在同一批输入上（当前树、暂存区、故意改坏的几份文档）新旧两版的输出行和退出码一致，才删旧的。
  - 反例：
    - 一次换掉好几个关卡；
    - 迁的时候顺手放宽或收紧规则；
    - 钩子里每条都 `go run`（实测一次 2.5～3.4 秒，见下文“落点”）；
    - 合并队列用主工作区编好的旧程序去验证别的分支（验证的必须是那个分支自己的工具源码）。
- **领域正统做法**：
  - Go 项目把开发工具放在本模块的 `cmd/` 下，用 `go build` 出可执行文件，再由钩子管理器调用。例如 Kubernetes 的 `hack/` 脚本调 `cmd/` 下的 Go 工具，Go 官方仓库的 `cmd/dist`。
  - 子命令用标准库手工分派，不引 CLI 框架：本仓库 `native/cmd/logotool/main.go:25` 已经这样写。
  - 钩子管理器 lefthook 本身是独立程序，只换它调用的命令（ADR-389 代价一节）。
- **最省事的字面做法够不够**：逐个 `go run ./cmd/<名字>` 不够。实测（2026-11-18，`native/cmd/turn-check`）：
  - `go run` 一次 2438～3399 毫秒；
  - 编好的程序一次 191～228 毫秒；
  - 没改源码再 `go build` 一次也要 3289～4857 毫秒；
  - 旧的 bun 脚本一次 306 毫秒。
  - 提交钩子并行跑好几条检查，所以要调用编好的程序，只在开发工具自己的源码改过时才重编。
- **不做什么**：
  - 不翻译只为冻结 Bun 服务的部分。`desktop-check.ts`，以及 `server-verify.ts`、`precommit-fast.ts`、`gen-rpc.ts` 里给 `server/`、`desktop/` 的分支，直接删掉，不迁（ADR-359 冻结，ADR-378 关卡碰到冻结代码就摘掉）。
  - 不改任何检查的规则和结论。
  - 合并队列不在本规格里重写成新的长期本地队列，跟[规格-合并走PR](规格-合并走PR.md) S3 一起做。
  - `export-public.ts` 和 `public-exclude.txt` 在切换开发主仓库之前沿用旧的，切换后整块删除，不迁（2026-11-19 已删）。
  - `bench-t2` 题库、参考解、许可原样留着（ADR-389 第 3 条）。
- **用户能看能试的验收**：
  - 每迁完一批，主人照常提交、推送，钩子输出的中文和以前一样；
  - 全部迁完后，新机器只装 Go 和 lefthook，就能提交、推送、跑合并队列；
  - 公开仓库的语言统计里不再有 TypeScript 和 PowerShell（题库除外）。

## 落点

- **程序**：`native/cmd/sumika-dev`，用标准库分派子命令：
  - `check docs|private|commit-msg|conflicts|gofmt|ui-skill|rpc`
  - `go affected|verify|shard|test-times`
  - `run`（取代 `run-low.ps1` 和 `slot.ts`）
  - `nightly`
  - `fetch tools|ort`
  - `assets mingcute|twemoji`
  - `report notice|dev|friction`
- **逻辑**：放在 `native/internal/devtools/<方面>/`。`internal/archcheck` 加一条规则：devtools 不 import 产品包（app、turn、channel、界面、模型），只许用标准库和 `internal/` 里的纯工具包，例如 `jsjson`、`netsafe`。
- **编好的程序放在** `native/bin/sumika-dev.exe`，每个 worktree 各一份，被 git 忽略。
  - 程序启动时，把 `native/cmd/sumika-dev/`、`native/internal/devtools/`、`native/go.mod`、`native/go.sum` 的内容算一个摘要，和编译时用 `-ldflags -X` 写进去的摘要比。
  - 对不上就先重编自己（写临时文件，成功后换掉），再带原参数重新执行。
  - 编不过就退出码非零、写清原因，不能放行。
- **钩子里的调用**：`lefthook.yml` 用一行 sh，程序不存在时先 `go build` 一次，再调用。
- **合并队列、每日整套**：在被验证的 worktree 里，用那棵树的源码编出程序再执行，不用主工作区的。现在的做法也是调被验证树里的脚本（盘点第 2 节，`scripts/merge-queue.ts:258`、`scripts/nightly.ts:251`）。
- **执行器指纹**：`go-affected` 的 FULL 名单、`go-verify` 的 RUNNERS / WATCH、`server-verify` 的 STEP_PATHS 决定旧的验证记录能不能复用。工具搬家时，这三处要同提交改成新的源码路径，旧跑法的记录不能当新跑法的证据（盘点“核心职责”第 3 条）。

## 照搬（逻辑不变，只换语言）

各文件的出处和要保留的边界见盘点表。

| 组 | 文件 | 要守住的 |
|---|---|---|
| 文档检查 | `check-docs.ts`、`check-spec-merged.ts`、`docs-baseline.json`（原样留） | 工作区 / 暂存区两种读法，错误和提醒分级，`--update` 写基线；基线不重置 |
| 隐私扫描 | `scan-private.ts` | Go 正则没有前后查找，改成显式边界判断；号码、脱敏、误报作用域、缺名单时的退出码不变 |
| 小关卡 | `check-commit-msg.ts`、`check-conflict-markers.ts`、`check-gofmt.ts`（必须查暂存区内容）、`check-ui-skill.ts` | 各自的例外规则 |
| 挑包和分片 | `go-affected.ts`、`go-shard.ts`、`nightly-record.ts`、`go-test-times.ts` | 直接导入方、穿透没有测试的包、根包例外、archcheck；分片退出码聚合 |
| 下载和素材 | `fetch-tools.ts`、`fetch-ort.ts`、`lib/zip.ts`、`lib/tgz.ts`、`gen-mingcute.ts`、`update-twemoji.ts` | 固定摘要、不取最新；解包拒绝越界路径；许可文件 |
| 其他 | `report-notice.ts`（已迁，`sumika-dev notice`）、`dsh-ask.ts`、`dsh-readonly.yml`（原样留） | 通知出错不挡发消息；dsh 默认只读 |
| 测试 | 各 `*.test.ts`、`test/*.test.ts` | 跟被测工具一起迁成 Go 测试，临时目录用 `t.TempDir()` |

## 重新设计

| 文件 | 为什么不能照搬 | 做法 |
|---|---|---|
| `go-verify.ts`、`server-verify.ts`、`precommit-fast.ts`、`pre-push.ts` | 里面混着冻结 Bun 的检查；执行器指纹写死了 TS 和 PS1 路径 | 只留 Go 的部分，指纹改成新路径；多 ref、删除 ref、新分支、记录复用的语义不变 |
| `run-low.ps1` + `slot.ts` | PowerShell 里内嵌 C# 调 Win32；`slot.ts` 转了一手，参数不能带空格 | 合成 `sumika-dev run`，用 `golang.org/x/sys/windows` 做命名 Mutex、Job Object、优先级、内存上限。过渡期锁名不变（`run-low.ps1:100`），新旧工具共用同一套名额。Mutex 要固定在一个系统线程上（`runtime.LockOSThread`） |
| `gen-rpc.ts` | 一半是给冻结的 `server/` 生成 TS | 只留 Go 生成和 `--check` |
| `nightly.ts`、`merge-queue.ts` | nightly 反向 import 队列；队列要跟着合并走 PR 变 | 先抽出共用的 git、锁、记录、worktree 操作，nightly 先迁；队列跟[规格-合并走PR](规格-合并走PR.md) S3 一起做 |
| `go-golden-json.ts`、`gen-go-unicode.ts` | 期望值来自 Bun 的 JSON、ICU 语义，换成 Go 自己算会变成“被测代码给自己出题” | 已录的向量和生成表原样留，作为固定数据；以后要更新再另定独立来源（Unicode 版本写死）。脚本本身删掉 |
| `subset-font.py` | fontTools 的子集、可变轴、name 表，Go 没有等价库（盘点第 3 节） | 本规格不迁，单独挂待办；字体文件已在仓库里，平时不需要跑它 |
| `dev-report.ts`、`friction-report.ts` | 写死了本机盘符和 Everything 路径 | 迁的时候改成参数和配置；输出脱敏不变 |

## 一次性 / 删除

| 文件 | 删除依据 | 什么时候 |
|---|---|---|
| `desktop-check.ts` 及测试 | 只验收冻结网页 | 第一批，同提交从钩子和计划里摘掉 |
| `go-shard.cmd` | 只是两行转发，Go 分片器可以直接当 `-exec` | 分片器迁完那一提交 |
| `test/helpers/tmp.ts`、`test/tmp-helper.test.ts` | Go 用 `t.TempDir()` | 它的最后一个使用者迁完时 |
| `guard-eval/generate.py`、`generate.txt` | ADR-389 第 2 条 | 第一批 |
| `artist-review.ts`、模板 | 待办写明不迁；已排除公开 | 第一批，移到本机 `data/dev/tools/`（不进 git） |
| `export-public.ts`、`public-exclude.txt` | 切换开发主仓库后不再需要 | 切换那一步（2026-11-19 已删） |
| `package.json`、`bun.lock`、`bunfig.toml`、根 TS 配置 | 全部迁完 | 最后一批 |

## 顺序

每一步单独提交、能单独撤回。关卡类（文档检查、隐私扫描、提交说明、冲突标记、gofmt、协议生成、挑包、验证、钩子编排）属于 AGENTS 第 7 条的“检查本身”：

- 新旧两版在同一批输入上逐行对拍，结论一致；
- 做改坏检查（把新版关键判断改坏，测试要红）；
- 派独立复核，过了才删旧的。

1. **骨架加第一批删除**：`sumika-dev` 程序、自我重编、archcheck 规则；删除上表第一批的文件。
2. **文档检查**（`check docs`，含 spec-merged），换掉钩子和巡检里的调用。
3. **隐私扫描**（`check private`），钩子、推送前检查、导出器里的调用一起换。
4. **小关卡**：commit-msg、conflicts、gofmt、ui-skill，一个一个换。
5. **下载、素材、通知、量具**：不挡提交，可以和 2～4 并行。
6. **`run`**（名额和内存上限）、**分片**；AGENTS、技能、派活模板里的 `bun ../scripts/slot.ts` 一起改。
7. **挑包、协议生成、验证、钩子编排**：同提交改执行器指纹。
8. **每日整套**；**合并队列**跟着合并走 PR。
9. **收尾**：删 `package.json` 等，改 CI、AGENTS 技术约束一条、CONTRIBUTING、两份 README 的环境要求、`docs/subsystems/go-tools.md`。

## 验收

1. 每个迁过的关卡都有对拍记录，存到主工作区 `data/dev/scripts-go/<名字>-对拍.txt`：输入清单、新旧输出、退出码，结论是“一致”。
2. 每个关卡都有改坏检查：把关键判断改坏，Go 测试变红；提交说明写改了哪里、报了什么错。
3. 自我重编：改一行 devtools 源码后提交，钩子会先重编再执行，第二次提交不再重编（看输出）；编不过时提交被拦下，并给出原因。
4. 提交钩子总耗时不比迁移前长；用 lefthook 自己的计时，取三次的中位数。
5. 收尾后在一台只有 Go 和 lefthook 的环境里（CI 的 Windows 一格），提交检查、`go verify` 都能跑通。
