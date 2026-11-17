# 界面协议的读方法（`rpc/`）

ADR-345 第 1 批，规格见 规格-Bun后台第1批（私有存档） 第七节。同一份库、同一串 JSON-RPC 请求，Go 服务端和 Bun 服务端回的东西要一样。

## 场景文件

| 字段 | 含义 |
|---|---|
| `name` | 和文件名相同 |
| `doc`、`source_tests` | 测什么、对应原来哪些 Go 测试 |
| `characters` | 配置里的角色（`id`、`name`），就是"能看什么"的范围 |
| `rows` | 按平台（`qq`、`local`）分，迁移跑完后直接插进 `go/chat/<平台>.db` 的原始表行；列名照表结构，`seq` 显式给 |
| `calls` | 依次发的请求：`method`、`params`；`cursor_from: N` 表示把第 N 个调用结果里的 `next_cursor` 填进这次的 `params.cursor`（游标不透明，各实现用自己的） |
| `calls[].expect` | Go 的实际结果：`{"result": …}` 或 `{"error": {"code": …}}`；结果里的 `next_cursor` 换成了占位"（游标）" |

时间都是 1000 左右的小整数（这几个读方法不看当前时间），事件 id 是编的固定串。

## 比对口径

Go（`native/internal/rpcserve/shared_rpc_test.go`）：整个 `expect` 逐字比（统一编码后字节相同）。

Bun（`server/test/shared-rpc.test.ts`）第 1 批只做了下面这些字段，比对时两边都先投影到这些字段、零值（`0`、`""`、`false`、空数组、空对象、缺省）一律当没有：

| 方法 | 比的字段 |
|---|---|
| 报错的调用 | `error.code` |
| `messages.read` | `state`；`value.has_more`、`value.high_water`；`value.items[]` 的 `ref`、`seq`、`actor_kind`、`actor_id`、`kind`、`origin`、`created_at`、`text`、`recalled`、`reply_to`；`reply`、`parts[].quote` 的 `state` 和 `value.{ref, actor_kind, text, recalled, snippet*}`（显示名不比）；`parts[]` 的 `has_quote`、`ref`、`text`、`attachments`（第 3 批补：quotes-and-parts） |
| `overview.read` | `state`；`value.conversations.has_more`；`value.conversations.items[]` 的 `key`、`title`、`observers`、`created_at`、`last.ref`、`last.text`；`value.characters.items[]` 的 `state`、`value.id`、`value.name` |

不比的（第 2 批补）：显示名、头像号、展示段（图、@、表情）、表情回应、未发送 / 分片状态、本地私聊的过程步骤、概览里的 `capabilities` 和 `runtime`、行版本 `version`。

## 怎么重新生成

```
# 在 native/ 下
$env:SUMIKA_SHARED_UPDATE='1'
go test ./internal/rpcserve -run TestSharedRPC
```

生成是确定的；重新生成后 `git diff testdata/shared/rpc` 就是行为变化，逐条看过再提交。

## 没覆盖

- QQ 群名、私聊对方昵称（`qq_groups`、`qq_people`）补的标题：场景里没有这两张表的行，标题都是空的。
- 读前一条（`before`）、媒体、小黄脸、设置、订阅：Bun 第 1 批没做。
- 运行状态 `runtime.read`：两边各报自己的状态，不比；Bun 的在它自己的测试里。
