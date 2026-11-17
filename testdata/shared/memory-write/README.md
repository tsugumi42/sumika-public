# memory-write：记忆写入第一档的共用场景

ADR-345 第 3 批 3.2。remember、memory_forget、写入内核（Batch 和各种动作）、取代保护、同文查重、改口、待确认攒证据、期限。**Go 跑真实实现生成 `expect`，Bun 跑同一批文件逐场景比。** 数据全部编造，不含真实聊天、昵称、号码。

| 谁跑 | 在哪 |
|---|---|
| Go（生成和验收） | `native/internal/memory/shared_write_test.go`，`TestSharedMemoryWrite` |
| Bun（验收） | `server/test/memory-write.test.ts` |

```
# 在 native/ 下重新生成 expect（输入不动），生成完逐条读过再提交
$env:SUMIKA_SHARED_UPDATE='1'
go test ./internal/memory -run TestSharedMemoryWrite -count=1
```

生成是确定的：重跑一遍，文件字节不变。**`expect` 提交前人读过**：每个场景的 `source_tests` 列了对应的原 Go 测试，断言对得上才算数。

## 一个场景文件

一场景一文件，字段英文小写下划线，读到不认识的字段两边的 loader 都报错。

| 字段 | 意思 |
|---|---|
| `about`、`source_tests` | 测什么、对应哪些原 Go 测试（`TestXxx`） |
| `writable` | 不写 = 真；假 = 写入没打开的只读角色（Go 以只读打开，Bun 不建写入器） |
| `options` | `auto_confirm`（待确认攒够证据真提升）、`confirm_occasions`（要几次，不写 = 3）、`confirm_kinds`（不写 = fact、preference） |
| `events` | 事件来源里的消息，remember / memory_forget 的 `source_quote` 要对着它。`seq`、`conversation`、`text`、`ago_s`（多少秒以前）；`from` 是 `owner`（默认）、`peer`（群友）、`character`（她自己）；`kind`（默认 message）、`not_seen`、`revoked`、`unsent`、`backfill`、`selftest` |
| `start` | 起始库：`facts`、`episodes`、`revisions`（只用来放 `wants_supersede`）、`evidence`。事实的 `confirmed_by` **不写 = NULL**（Bun 导入的、受保护），写 `""` 是空串，`ai`、`owner`、`owner_said` 照写 |
| `new_ids` | 新写的事实 / 经历依次用的编号（运行时不随机）。用完了就报错；有剩的不要紧。被回滚的批次也会用掉编号（编号在写之前就取了） |
| `steps` | 按顺序做的步骤，见下 |
| `expect` | 结束时相关表的行，见下 |

### 步骤（`do`）

- **`remember`、`forget`**：`args` 是模型给工具的原始参数（`memory_forget` 在步骤里叫 `forget`），`input_seqs` 是宿主绑定的本轮输入，`place` 是听众（下表，不写 = `local_private`），`valid_until_in_s` 是“现在 + 这么多秒”填进 `args.valid_until`（RFC3339、UTC）。经过**工具层**（类型检查、取值、内部错误换成固定的话）再到 `Remember` / `Forget`。
- **`batch`**：直接给写入内核一批动作。`by` 是 `remember`（主人本人）、`remember-ai`（模型）、`summary`（整理）、空或别的（交给 validate）；`on_dup` 是 `report` / `merge`（不写：`summary` 用 merge，其余 report）；`place` 给转述的证据判能不能看见这条候选用（不写 = 只认证据所在会话）；`ops` 的 `op` 是 `fact`、`revoke`、`episode`、`evidence`、`confirm`。时间字段都是相对的、单位是秒（可以带小数）：`valid_to_in_s`（失效时间 = 现在 + 这么多）、`event_ago_s`、`evidence.ago_s`。
- **`advance`**：时钟往后走 `s` 秒。
- **`close`**：关库；之后的写回内部错误。

`expect`（步骤里的）：工具步骤给 `text` 和 `is_error`；批次给 `outcomes`（每个动作的 `result`、`ref`、`status`，有就带 `replaced`（被取代那条原来的正文）、`promoted`、`confirmed`）和 `counts`，或者给 `error`（`code` 是 `invalid`、`busy`、`closed`、`replace_gone`、`replace_protected`、`replace_ambiguous`、`remember_expired`……，`message` 是完整文字）。空的字段不写。

### 听众（`place`）

| place | 听众 | remember / forget |
|---|---|---|
| `local_private` | 本地私聊，主人在场 | 行 |
| `qq_private_owner` | 主人的 QQ 私聊 | 行 |
| `local_group` | 本地群聊，主人在场 | 拒 |
| `qq_group_owner_asked` | QQ 群，主人问起 | 拒 |
| `qq_group` | QQ 群，主人在场没问 | 拒 |
| `qq_group_no_scene` | QQ 群，`Scene` 空着 | 拒 |
| `qq_private_other` | 别人的 QQ 私聊 | 拒 |
| `local_private_no_owner` | 本地私聊，主人不在场 | 拒 |
| `qq_private_no_owner` | 主人的 QQ 私聊，主人不在场 | 拒 |

### 结束时的表（`expect.facts` 等）

每个表一个数组，一行一个对象，键就是列名；排序固定（事实、经历按 `id`，修订按写入顺序，证据按 `fact_id, day, conversation`）。

- **时间一律写成“距结束时的时钟多少秒前”**（秒，可以带小数）：`created_at` → `created_ago_s`，`event_at` → `event_ago_s`，`learned_at` → `learned_ago_s`，`valid_from`、`valid_to` 同理；负数是将来（`valid_to_ago_s: -604800` = 七天后失效）；NULL 还是 `null`。为什么是秒：毫秒数一长（九位以上）就和隐私扫描认的“号码”撞上。证据的 `day` 换成 `days_ago`（距结束那一天几天，按 UTC）。
- `facts.scopes`、`episodes.scopes` 是 `item_scopes` 里这一条的范围，排好序。
- `facts` 是整行：`visibility`、`source_seqs`（JSON 字符串，如 `[2]`）、`speech_act`、`only_her`、`no_mention`、`whisper_json` 都在——Go 和 Bun 共用同一个 `memory.db`，Bun 写的行 Go 要读得懂，所以整行都比。
- `revisions`：`fact_id`、`op`、`by`、`reason`、`at_ago_s`，`before` / `after` 是修订里那段小 JSON **解开后的对象**（`null` 就是没有）；里面的 `valid_to`、`event_at`（非 0 时）换成 `valid_to_ago_s`、`event_ago_s`。（JSON 字符串本身的写法——键排序、没有空格、`< > &` 转义——由 `server/test/memory-write-core.test.ts` 单独钉住。）
- 自增 id、绝对时间一律不比。

### 比对口径

步骤的 `expect` 和结束时的四张表都**整个比**：对象的键序不管，数组的顺序要对，多一个少一个字段都算不一样。

## 固定的装置

- 时钟：`2026-10-26T12:00:00Z`（Go 里的 `mwBase`，Bun 里的 `BASE`），`advance` 才会动。证据的“哪一天”按 UTC。
- 角色 id `suzune`；事件来源是内存里的数组，`Events(会话, afterSeq, limit)` 给 seq 大于 afterSeq 的最早 limit 条（和真实实现一样，所以 `seq` 对不上的绑定会被拒）。
- 编号由场景给：种子事实 `SEED…`、新写的 `NEWF…`；短码是编号末 6 位。
- 没有嵌入、没有后台模型、没有主人共用池（Go 里 `Shared` 为 nil）、不在悄悄话会话里、不带 `only_her`。

## 工具层不是同一份代码

工具层（参数取值、`temporal`、`valid_until`、`source_quote`、`id`、`reason` 给了不是字符串整个拒、别的字段不是字符串当空串、多出来的字段不看、内部错误换成固定的话）在 Go 的 `memplugin` 包里，`memory` 包的测试够不着，所以 Go 的 runner 用 `mwToolRemember` / `mwToolForget` **照 `memplugin/plugin.go` 的 `tool` 函数抄了一份**。这部分的期望是“抄的那份”和 Bun 真实工具层的对拍；`memplugin` 那边改了参数取值，这里要同步改，否则两边会各自对。其余（`Remember`、`Forget`、`apply` 及以下）都是 Go 的真实实现。

## 没覆盖

- 整理（`Job` 幂等键和游标、世代号、`SummarizeBatch`）、启动补提升（`ConfirmSweep`）、主人共用池、悄悄话、止血隔离（`QuarantineSince`）、写入场合名单（`WritePlaces`，只管整理）、向量补齐：Bun 这批不做，场景里没有。
- 写锁的等待和关库（`TestWriteLockWaitsThenGivesUp`、`TestCloseWaitsForAnInFlightWrite`……）：时序类的，不适合放进文件，Go 在 `write_test.go`，Bun 在 `memory-write-core.test.ts`。
- 前提检查和开写前备份（`internal/memwrite`、`paths.CheckMemoryPath`、`BackupMemoryDB`）：要真文件和联接，Go 在各自包的测试里，Bun 在 `memory-write-precheck.test.ts`；备份的文件名和指纹拿 Go 实际算的值钉住。
