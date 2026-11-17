# 共用测试数据（Go 后台和 Bun 后台验收同一份）

ADR-345 第 0 批（`memory-recall/` 是第 3 批的前置考题，格式和判分口径不同，见该目录 README）。把 Go 后台里最关键的行为场景导出成和语言无关的 JSON：Go 用它跑真实实现并比对（现在就在跑），以后 Bun 后台（`server/`）写一个读同样文件的测试，两边逐场景一致才算过。数据里的号码、昵称、群名、文字全是编的，不含真实聊天内容。

## 目录

| 目录 | 场景 | 测什么 | Go 的跑法 |
|---|---|---|---|
| [`backfill/`](backfill/README.md) | 32 | QQ 断线补录：翻页、锚点、覆盖证据、缺口、水位 | `native/internal/app/shared_backfill_test.go` |
| [`turn/`](turn/README.md) | 11 | 本地私聊一个会话里 start / steer / queue / stop / withdraw 的语义 | `native/internal/turn/shared_inputs_test.go` |
| [`qq-frames/`](qq-frames/README.md) | 6 个文件、88 帧 | 协议端推来的一帧原文翻成什么（消息、通知、状态、丢弃） | `native/internal/channel/snowluma/shared_frames_test.go` |
| [`qq-intake/`](qq-intake/README.md) | 8 | 消息入库：按（渠道、会话、平台消息号）去重、谁说的、来源 | `native/internal/channel/shared_intake_test.go` |
| [`qq-send/`](qq-send/README.md) | 9 | 发送的回执（确认、失败、结果未知、迟迟不回）和回声认领 | `native/internal/channel/shared_echo_test.go` |
| [`rpc/`](rpc/README.md) | 8 | 界面协议的读方法：可见性（观察、角色过滤、本地话题和群的归属）、隐藏行、撤回、翻页、坏请求 | `native/internal/rpcserve/shared_rpc_test.go` |
| [`memory-recall/`](memory-recall/README.md) | 62 题 / 48 条记忆 | 记忆召回考题（全虚构）：换说法、按时间、改口取新、场合可见性、传闻、过期、待办、没有答案、角色隔离。**考题不是回归场景**：分数记在 `baseline.json`，退步才红 | `native/internal/memory/shared_recall_test.go` |
| [`memory-write/`](memory-write/README.md) | 23 | 记忆写入第一档：remember、memory_forget、写入内核、取代保护、同文查重、待确认攒证据、期限、工具层的参数检查和只读的说法 | `native/internal/memory/shared_write_test.go` |

规格：补录的规则在 [`docs/规格-补录覆盖.md`](../../docs/规格-补录覆盖.md)。

## 引用的已有对拍数据（不重复拷贝）

这些文件本来就是和语言无关的对拍数据（输入带结果），留在原处，Bun 直接读这些路径：

| 路径 | 内容 | Go 的跑法 |
|---|---|---|
| `native/internal/policy/testdata/decide.json`、`decide-cases.json` | 审批判定：命令 + 场合 → 放行 / 问 / 拒 | `policy/golden_test.go` |
| `native/internal/policy/testdata/shell.json`、`shell-cases.json` | 命令拆解 | 同上 |
| `native/internal/policy/testdata/risk.json`、`risk-cases.json` | 风险分类 | 同上 |
| `native/internal/channel/testdata/asent.json` | 发送前改写：去句尾句号、按空行 / 规则拆条 | `channel/rewrite_test.go` |
| `native/internal/channel/testdata/dialog.json` | 群聊对话链：一条消息接哪句话 | `channel/dialog_test.go` |
| `native/internal/channel/testdata/gate.json` | 群聊闸门算式、保险丝 | `channel/gate_calc_test.go` |
| `native/internal/turn/testdata/cases/*.json` + `golden/` | 回合循环的录像：整段上下文、工具调用、插话 / 停止在循环里的行为（Bun 录的，Go 回归基线） | `turn_test.go` 的 `TestSameAsBun` |

## 没导出的

- **记忆召回的真实考题（`cmd/memory-eval`）**：真实考题放在 `data/dev/memory-eval/`，由真实记忆整理而来，不能进仓库；共用的是另编的全虚构考题 `memory-recall/`。共用池、重排、门控、预算、来源标注没考，见该目录 README 末尾。
- **QQ 协议动作和发送确认的底层（`snowluma` 的 `send_test`、`verify`、`HistoryAfter`）**：需要假 WebSocket 服务端，场景是"收发动作的时序"，形状和这里不同。回执 + 回声认领的结果已经在 `qq-send/`，更底下的（动作参数、回包分类词表、没回执时翻历史核实）还只在 Go 的 `snowluma/*_test.go` 里。
- 其余没做的块见各目录 README 末尾的"没覆盖"。

## 约定

- 每个文件一个场景（`qq-frames` 例外：一个文件一组帧）。字段名英文小写下划线；时间一律用相对值（`ago_ms`、`time_s`），不放绝对时间，也不放随机 id。
- `expect` 由 Go 的实际结果生成，**提交前人读过**，对得上原来 Go 测试的断言才算数（场景的 `source_tests` 写了对应的 Go 测试）。
- 比对口径：`expect` 里出现的字段**逐字比**（用同样的缩进 JSON 编码后字节相同）；各目录 README 写了哪些字段不比、哪些数是名次不是原值。顺序也算（数组按文档里的顺序比）。
- 读场景时**不认识的字段要报错**（Go 的 loader 就是这样），免得拼错的字段悄悄没用。

## 怎么重新生成

```
# 在 native/ 下，SUMIKA_SHARED_UPDATE=1 让测试把实际结果写回各文件的 expect（输入不动）
$env:SUMIKA_SHARED_UPDATE='1'
go test ./internal/app -run TestSharedBackfill
go test ./internal/turn -run TestSharedTurnInputs
go test ./internal/channel -run 'TestSharedQQSend|TestSharedQQIntake'
go test ./internal/channel/snowluma -run TestSharedFrames
# memory-recall 是考题：下面这条重录的是 baseline.json（分数），不是 expect；考题本身是手写的
go test ./internal/memory -run 'TestSharedMemoryRecall$'
# memory-write：同样用 SUMIKA_SHARED_UPDATE=1 把实际结果写回各场景的 expect
go test ./internal/memory -run TestSharedMemoryWrite
```

生成是确定的：重跑一遍，文件字节不变。重新生成后 `git diff testdata/shared` 就是行为变化，要逐条看过再提交。

## 给 Bun 实现的提醒

- 读文件时大整数字段（平台消息号）保持字符串；`qq-frames` 的 `frame` 本来就是字符串原文，别先 `JSON.parse` 再 stringify。
- 每个目录的 runner 都用"真实实现 + 假外部"：Bun 版也该这样，不要为了过场景写特判。
