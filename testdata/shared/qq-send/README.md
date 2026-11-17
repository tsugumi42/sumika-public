# qq-send：发送回执和回声认领

Go 的跑法：`native/internal/channel/shared_echo_test.go`（真实 `Outlet`、临时库、假适配器、假钟）。

## 一个场景

```
id, about, source_tests[]
receipt     协议端对每次 Send 怎么回：
              unknown      每次都"结果未知"
              confirmed    确认，平台消息号 8000 + 第几次
              failed_first 第一次明确失败（等重试），之后确认（号 6000 + 第几次）
              rejected     每次都明确拒绝（重试用完即失败）
              hang_first   第一次 Send 迟迟不回（等 release），之后确认；迟到的回包带号 9999
steps[]     依次：
  say        label、texts[]（每条一段）、as（ca = A 号，cb = B 号，默认 ca）
  wait_sent  n：等适配器收到第 n 次 Send
  wait_part  label、idx、state：等某段到某状态
  wait_plan  label、state：等整条计划到某状态
  pump       label：一直拨钟（重试间隔、段间停顿）直到这条计划不再是发送中
  wait_timer ms：等假钟上出现一个这么长的计时器
  advance    ms：拨钟
  claim      account（a 或 b）、conv（g1 或 g2）、platform_id、text、media（真 = 回声是图片）：协议端把我们发的消息推回来，认领
  release    放行迟到的回包，并等它处理完
final       结束后的状态
```

## expect（逐字比）

- `claim`：`claimed`（认领了没有）；
- `wait_part`、`wait_plan`、`pump`：`plan` = 那一刻这条计划 `{label, state, parts[]}`，每段 `{state, via（reply、echo 或空）, platform_id, attempts}`；
- `final`：`plans[]`（同上）、`sends`（适配器一共收到几次 Send）、`mapped`（登记了映射的平台消息号 -> 指向哪条计划）、`unmapped`（claim 用过但没登记映射的号）。

状态取值：计划 `sending、done、unknown、failed`；段 `pending、sending、retrying、confirmed、unknown、failed、aborted`。

**不比**：计划 id、事件 seq、时间戳、日志。

## 没覆盖

回声在入库一站之前认领的完整链路（`TestEchoClaimedBeforeIntakeStoresIt`，要和 `qq-intake` 串起来）；同一平台消息号只认一段、别的会话已映射的号（`TestEchoSamePlatformIDClaimsOnlyOnePart` 等）；转发段；协议端底层的回包分类、没回执时翻历史核实、登录号核验（`snowluma/send_test.go`、`verify_test.go`）。
