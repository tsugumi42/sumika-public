# backfill：QQ 断线补录一轮

规格：[`docs/规格-补录覆盖.md`](../../../docs/规格-补录覆盖.md)。Go 的跑法：`native/internal/app/shared_backfill_test.go`（真实 `qqBackfill.round`、真实入库、临时库、固定时钟；只把协议端换成按脚本回页的假历史源）。

## 一个场景

```
id, about, source_tests[]        说明和对应的 Go 测试，不比
scene                            private（主人私聊，号 100 和 200）| group（群 300）
setup:
  watermark_ago_ms               开始时水位在多久以前（决定起点 = 水位 - 60 秒）
  peer: { latest, continuous, unsupported }
                                 协议端每页回包的属性：latest = 这是最新页没退回缓存；continuous = 协议保证页内无遗漏；
                                 unsupported = 没有历史能力
  stored[]                       开始前库里已经有的实时消息
  gaps[]                         开始前库里已经有的缺口（from_ago_ms / to_ago_ms / state / reason / tries / last_try_ago_ms）
steps[]:                         依次执行，每步是"推进时钟 -> 实时消息到达 -> 跑一轮补录"
  advance_ms                     先把时钟拨快
  live_before[]                  这一轮开始前到达的实时消息（走和补录同一个入库）
  peer                           （可选）这一步换协议端属性
  pages[]                        按调用顺序回的历史页：messages[]、error（非空 = 这次查询失败，messages 仍交回）、
                                 live_during[]（回这一页之前先到的实时消息）、repeat（真 = 之后所有查询都回这一页）、
                                 cancel（真 = 回页之前先取消这一轮）
  expect                         见下
```

消息 `{id, ago_ms, seq, from}`：`id` 平台消息号（字符串）；`ago_ms` 消息时间在"这一步开始时的现在"之前多久；`seq` 群历史的协议序号（0 = 没有）；`from` 空或 `other` = 别人，`self` = 她自己的号。

脚本页不够用（查询次数超过 pages 且最后一页没有 `repeat`）算场景写错，Go 的 runner 会报错。

## expect（每步一份，全部逐字比）

| 字段 | 含义 |
|---|---|
| `error` | 这一轮返回的错误；成功是空串。取消是 `context canceled` |
| `calls[]` | 向协议端发的历史查询：`anchor`（空串 = 不带锚点）、`count`（每页条数，现在恒 100）。顺序就是调用顺序 |
| `stored[]` | 库里所有消息（含这一步之前存的），按入库先后：`id`、`ago_ms`（消息时间在"这一步结束时的现在"之前多久）。同一会话 |
| `gaps[]` | 库里所有缺口，按 `from_ago_ms` 从大到小、`to_ago_ms` 从大到小、`state` 排序：`from_ago_ms`、`to_ago_ms`、`state`、`reason`、`tries`、`last_try_ago_ms` |
| `watermark_ago_ms` | 这一步结束时水位在多久以前。0 = 推到了现在 |
| `continuity` | 这个会话的连续证明区间 `{from_ago_ms, through_ago_ms}`；从没结算过是 `null` |
| `complete_coverage` | 没有任何 `open / blocked / abandoned` 缺口 |

**不比**：缺口 id、创建 / 更新时间、日志、进度计数（`Inserted` / `Duplicates` / `Pages`）。

## 没覆盖

连接防抖和在线水位的定时推进、多会话的查询预算、取消的并发细节、手动补录、对齐任务、媒体补扫、10000 条规模：见规格第七节。
