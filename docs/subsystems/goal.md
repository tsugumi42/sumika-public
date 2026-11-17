# /goal 与斜杠命令表

状态：核心包已写（feat/goal 分支，待合并），尚未接进本地发送链路。规格见[规格-目标和循环](../规格-目标和循环.md)第二节，同行做法见[调研-agent客户端实用功能](../notes/调研-agent客户端实用功能-2026-11-08.md)第 1.1 节。

## 职责与边界

- `native/internal/slash`：登记式斜杠命令表。只做词法和查表：整条消息以 `/名字` 开头才算命令；名字只允许小写字母、数字、`-`、`_`，所以 `/usr/bin 在哪` 不是命令。没登记的返回 `*UnknownError`（宿主照原文发给她并提示“没有这个命令”）。命令在哪个场合认（只本地私聊、只认主人输入框）由宿主判断，包里不管。`/loop` 以后只需 `Register` 一个 `Command`。
- `native/internal/goal`：目标状态、裁判、轮末处理、命令解析、状态行。不依赖 turn、app、llm；模型和存储都由宿主注入。依赖 `slash`、`fileatomic`。

## 对外接口

| 函数 | 作用 |
| --- | --- |
| `goal.NewManager(store, model, cfg)` | 建管理器，并把存储里进行中的目标改成暂停（重启后不自己接着跑） |
| `goal.NewFileStore(dir)` | 默认存储：目录下每个会话一个 JSON，文件名是会话号的十六进制 |
| `Manager.Set / Get / Clear / Pause / Resume / SetBudget` | 命令对应的动作；Resume 会给到顶的预算续一个步长 |
| `Manager.AfterTurn(ctx, session, TurnResult) Decision` | 每轮结束后调；Decision 为 Idle / Continue（带 Prompt）/ Achieved / Paused |
| `goal.ParseCommand(text)`、`goal.Register(reg)`、`goal.StatusLine(g)` | 命令解析、登记进命令表、界面状态行 |
| `goal.Completer`、`goal.Unavailable(err)` | 裁判模型接口；模型不可用、额度用完、认证失败包一层 Unavailable 直接暂停 |

## 轮末规则（AfterTurn）

1. 她在等主人回答（`NeedsUser`）：直接暂停，不调裁判。
2. 裁判只看目标原文、最终回答（保留结尾约 4000 字）、工具调用摘要。出错当没达成继续，连续 3 次出错暂停；模型不可用直接暂停；ctx 取消暂停（原因“已中止”）。
3. 达成：清除存储，返回 Achieved。
4. 兜底（都只会暂停，不加别的确认）：连着两轮说卡住（裁判 JSON 的 `blocked`，或回答结尾含“卡住了”等明确说法）；连着两轮没有工具调用且回答相似度不低于 0.9。
5. 预算（轮数默认 30、token 默认不限）到顶：先返回一轮收尾提示，收尾轮结束后再暂停。收尾轮里达成仍算达成。
6. 裁判调用期间不持锁；回来后若目标被暂停、清除或换成新目标，返回 Idle，不改任何东西。

## 验证

`cd native && go test -short -count=1 ./internal/goal/... ./internal/slash/...`
