# qq-intake：消息入库、去重、来源

Go 的跑法：`native/internal/channel/shared_intake_test.go`（真实 `Intake`、临时库、两个号 a/b 各绑一个角色）。

## 一个场景

`{id, about, source_tests[], items[], final}`。`items[]` 依次提交：

```
account      a 或 b：收到它的号
conv         g1、g2（两个群）；pa、pb（A 号 / B 号和朋友的私聊）
kind         message（默认）或 self（她自己的号发的）
platform_id  平台消息号字符串；空串 / "0" = 协议端没给
from         friend、owner、a、b、stranger
text, time_s 文字；时间 = 基准时刻 + 秒数（可为负）
backfill     真 = 补录来的（和实时共用一个入库）
```

## expect（逐字比）

每条 `items[i].expect`：`duplicate`（键已在库里，只补"看到了"）、`dropped`、`character`（给哪个角色看的）、`origin`、`actor_kind`、`observed`。

`final`：`events[]`（库里所有事件按入库先后：`conv`、`platform_id`（空 = 没进映射表）、`actor_kind`、`actor_id`、`origin`、`text`、`time_s`）、`observations[]`（谁看到了第几个事件，`event` 从 1 起）。

**不比**：事件 id、seq 原值（只比名次）、日志。

## 没覆盖

准入检查（陌生人私聊、不收）、引用链接、并发提交的顺序（20 路并发、2000 条规模）、关闭时丢排队：见 `channel/intake_test.go`。
