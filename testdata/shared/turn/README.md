# turn：本地私聊的输入语义

Go 的跑法：`native/internal/turn/shared_inputs_test.go`（真实 `Hub`、临时库，模型是假网关；每次模型请求到达后卡住，等场景 `release` 才回，所以先后由场景决定，不靠睡眠）。

## 一个场景

```
id, about, source_tests[]
replies[]        每次模型请求回什么，按请求到达顺序：text（一句话收尾）| tool（先调一个不用审批的工具，下一次请求才收尾）
steps[]          依次执行：
  do: submit        label、mode（start | steer | queue）、text、client_key（默认 = label）、
                    expected_run（steer 用：目标那一轮，写开出它的那句话的 label；ghost = 不存在的一轮）、
                    conv（空 = 主会话；neighbor = 同一角色的另一个会话）
  do: wait_request  n：等到第 n 次模型请求到达（按到达先后，从 1 起）
  do: release       n：放行第 n 次请求（让它回话）
  do: stop          run（哪一轮，写开出它的 label）、through（label，可空：取消到这句话的序号为止，含）
  do: wait_run      run：等这一轮排空，记结局
  do: withdraw      label：撤回排队中的那句
  expect
final            整个场景结束后的状态（所有请求放行、所有轮排空之后）
```

label 是场景里给每句话起的名字。"一轮"用开出它的那句话的 label 称呼；steer 插进别人的一轮就属于那一轮。

## expect（逐字比）

| 步 | 字段 |
|---|---|
| submit | `error`（空、busy、conflict、not_pending 或其他错误原文）；成功时 `state`（回执里这句话的状态）、`run`（挂在哪一轮）、`seq_rank`（回执序号在本场景所有成功序号里的名次，从 1 起；**序号本身是每个会话自己的计数，不同会话可以相等**，所以只比名次） |
| stop、withdraw | `error` |
| wait_run | `outcome`（done、stopped 等）；有收尾时才到被改排成新一轮的插话时 `requeued[]` |
| 其他 | 空 `{}` |

`final`：
- `inputs[]`：每句话的 `label`、持久状态 `state`（accepted、admitted、cancelled 等）、`run`；
- `runs[]`：每一轮的 `state`（settled）和 `outcome`；
- `requests[]`：每次模型请求（按到达顺序）的 `contains[]` = 请求正文里出现了哪些 label 的原话（**含历史**：被取消的排队话的原文仍在聊天记录里，所以后面的请求里能看到）；
- `steers[]`：插话事件 `{phase（delivered、dropped 等）, input, requeue}`。

**不比**：运行号、输入号、时间、请求正文的其余部分、模型回的内容。

## 没覆盖

- 收尾时才到的插话被改排成新一轮（`requeue`）：需要在回合收尾的一瞬间到达，没法用公开接口稳定复现，Go 的测试是直接调内部函数（`TestRequeuedSteerReportedOnSnapshot`、`TestRequeuedSteerBudgetDeadline`）。
- 重启后的 held（没处理的话）放行 / 放弃：`input_held_test.go`。
- 停止三段（宽限、升级）：`stop_test.go`；循环里的插话 / 停止时序见 `turn/testdata/cases/steer-*.json`、`stop-*.json`、`live-interrupt.json`（引用，见上级 README）。
- 审批等待中的停止和插话、四个场合的权限（`TestFourSceneInputAuthority`）。
