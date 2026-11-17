# memory-recall：记忆召回考题（全部虚构）

这是**考题，不是回归场景**：一份编造的世界（一个主人、四个群友、两个角色）、一批记忆、62 道题。Go 现在就用真实的 `Memory.Recall` / `CheckOutbound` 跑它；以后 Bun 后台（ADR-345 第 3 批）照同一份验收。不要求全过：分数记在 `baseline.json`，退步到约定的幅度才红。

数据里的名字、地名、事件、群、会话 id 全是编的（`qq:u-xxx` 是编的代号，不是 QQ 号）。真实的考题在 `data/dev/memory-eval/`，由真实记忆整理，不能进仓库，也没有拿来参考。

Go 的跑法：`native/internal/memory/shared_recall_test.go`

```
cd native
go test ./internal/memory -run TestSharedMemoryRecall -count=1 -v        # 看每题和总分
go test ./internal/memory -run TestSharedMemoryRecallData -count=1       # 只查考题数据自己一致
$env:SUMIKA_SHARED_UPDATE='1'; go test ./internal/memory -run 'TestSharedMemoryRecall$' -count=1   # 重录 baseline.json
$env:SUMIKA_RECALL_REPORT='<路径>'                                        # 另把每题明细（给了哪些条目、为什么没过）写成 JSON
```

## 文件

| 文件 | 内容 |
|---|---|
| `world.json` | `base_time`（所有相对时间的基准，也是召回的"现在"）、主人、群友（id 和名字，名字会进别名表）、两个角色、10 个会话（id、场合、属于哪个角色） |
| `memories.json` | 记忆：2 个角色共 48 条（雾岛 43、小满 5） |
| `questions.json` | 62 道题 |
| `baseline.json` | Go 当前的分数（总分、各题型、每题过没过）；换了嵌入替身或考题要重录 |

## 一条记忆

```
id, character          编号（全局唯一）、属于哪个角色（每个角色一个库，互相看不到）
type, kind             fact | episode（片段）；fact 的 kind：fact | preference | promise（约定）| boundary（边界）；片段 kind = episode
text                   记忆原文
subject                这件事说的是谁："owner"、人物 id，或空（群约定）；片段不写
scope                  范围："owner"（主人私事）| "public" | "conv:<会话 id>"（只在那个会话里说过）| "person:<人物 id>"（只在和这个人私聊时说过）
status                 active | candidate（待确认，传闻、听说）| superseded（已被新的取代，不再召回）
superseded_by          取代它的那条（同角色、同主体、同范围、有效）；只有 superseded 写
confirmed              owner（主人确认过）| owner_said（主人亲口说的，来源已绑定原话）| none（其他人说的、没人确认）；片段不写，candidate 必须是 none
temporal               stable | temporary
valid_until_days       有效期，相对 base_time 的天数（负 = 已过期）；只配 temporary。temporary 没写这项的，说出口 7 天后不再召回（TempDays）
source                 谁说的（speaker）、在哪种场合（place）、哪个会话（conversation）、哪个本地话题（session，只有本地私聊写）、多少天前说的（said_ago_days，相对 base_time）
```

覆盖：事实、偏好、约定和待办（周五面试、周六搬道具箱、下周出差）、改过口的旧记忆（住址、颜色、游泳时间、阿渡的工作四条取代链）、只在私聊说过的私事（owner / person: 范围）、群友传闻（candidate）、已过期的（显式 valid_until 过了、temporary 超过 7 天各有）、片段（episode）。

## 一道题

```
id, type, character   编号；题型；问的是哪个角色
q                     问题原文
place, asker, conversation   提问场合（local_private | local_group | qq_private | qq_group）、提问人（"owner" 或人物 id）、会话 id
recall                对召回结果的期望（见下）
disclosure            只有 visibility 题带：回复草稿 + 守卫该拦下哪几条
```

听众怎么算（和渠道中间件同口径）：本地两种场合都是主人在主窗口（主在场、主人问）；QQ 私聊里主人本人是主在场、主人问，别人只带自己的 id；QQ 群里主人本人问才算"主人问"，群友只带自己的 id。

### recall（逐项检查，全部满足这一题才算过）

| 字段 | 检查 |
|---|---|
| `expect[]` | 这些条目都要出现在召回的 `Hits` 里（`memory_search` 的口径：明确回忆、TopK 12），且不能被标成"听说" |
| `ordered` | 真时，`expect` 的先后就是 `Hits` 里的先后 |
| `hearsay[]` | 待确认的传闻：要召回，且必须带"听说"标记（不当事实）；这些不写进 `expect` |
| `previous[]` | 取代链：新值要带出这些旧版本（问"以前怎样"才答得出）；旧版本自己不在 `Hits` 里 |
| `forbid[]` | 这些条目不能出现在 `Hits` 里（过期的、被取代的旧值、别的角色的、近似但不对的） |
| `empty` | 真时，`Hits` 必须是空的（没有答案就别硬凑） |

**只看 `Hits`**：固定要点（`Pinned`：约定和边界）每次都会给，不算召回，不参与上面任何检查。

### 场合可见性：召回和表达是两件事

ADR-242：同一个角色在四种场合都能**想起**全部有效记忆（召回不分场合），是否能**说出口**由发送前的守卫管。所以 visibility 题的 `recall.expect` 在私聊和群里是**一样的**，差别在 `disclosure`：

```
draft     角色可能说出口的一句话（这一题里"泄露"的样子）
blocked[] 守卫应该拦下的记忆（逐条，集合相等；空 = 这句话在这个场合可以说，一条都不该拦）
```

每对题同一件事问两次：主人在场（本地私聊 / 本地群聊 / QQ 私聊主人 / QQ 群主人本人问）`blocked` 为空；群友或别人问 `blocked` 是那条私事。草稿大多原文引用（字面那一层一定拦），这样考的是"身份和范围算对没有"；换种说法的泄露要靠真实嵌入 + 小模型确认，这里没有（见下）。

### 题型（62 题）

| 题型 | 题数 | 考什么 |
|---|---|---|
| `paraphrase` | 10 | 换说法问（问法和记忆原文几乎不共用字） |
| `time` | 8 | 按时间问（"上周说的那个""上个月底"）。召回没有按时间筛选的机制，这里靠问题里的内容词；时间只作线索 |
| `supersede` | 7 | 改过口：取新值、旧值不出现；其中 3 题还要新值带出旧版本 |
| `visibility` | 10 | 场合可见性（5 对，见上） |
| `hearsay` | 6 | 传闻 / 待确认：要想起，但标"听说"，不当事实 |
| `expired` | 6 | 已过期的不召回 |
| `schedule` | 5 | 约定和待办（有时间的），其中 1 题要按时间先后排 |
| `no_answer` | 6 | 没有答案：召回应为空；其中 2 题带近似但不对的诱饵（别的人的宠物） |
| `isolation` | 4 | 角色隔离：问小满不能想起雾岛的事，反过来也一样；小满自己的记忆要想得起 |

## 基线怎么比

`baseline.json` 记录：嵌入替身名、TopK、总分、每个题型的分数、每题过没过。

| 退步（任一条成立，测试变红） | |
|---|---|
| 总分 | 比基线少 3 题以上（约 5%） |
| 题型 | 任何一个题型比基线少 2 题以上 |
| 场合可见性 | `visibility` 比基线少 1 题就算（隐私，零容忍） |
| 基线过期 | 考题数、题号、嵌入替身名或 TopK 和基线对不上：先重录 |

变好了不红，日志会列出"新过 / 新挂"的题；确认变好是真的，再用 `SUMIKA_SHARED_UPDATE=1` 重录，`git diff baseline.json` 逐题看过再提交。改了考题（加题、改期望）也要重录。

**当前基线：47/62**（2026-11-10 词法加相关度下限后，原 45/62）。paraphrase 6/10、time 8/8、supersede 5/7、visibility 7/10、hearsay 6/6、expired 6/6、schedule 3/5、no_answer 3/6、isolation 3/4。

没过的题多数是替身的局限（见下），不代表实现有 bug：
- `paraphrase` / `visibility` 的召回项：问法和原文字面重叠少，全文那一路抓不到，替身的向量又懂不了同义。
- `no_answer`：全文那一路原来只要有一两个字重叠就给一条，现在加了词稀有度下限（规格-记忆-v2 第七节第 3 步），q56 过了；剩下 q54、q57、q58 都是靠人名撞上（别名命中本来就带出这个人的事实），词法分不出"问的那一项没有"。
- `isolation` 剩 q59：小满的库只有 5 条，不到 30 条不设词稀有度下限（统计没意义），"周五"撞上她的周五提醒约定就给了；q61 已随下限修好。

## 替身：用了什么，和真实模型差在哪

| 部分 | 用的 | 和真实的差别 |
|---|---|---|
| 嵌入 | `stubHashEmbed`（测试文件里）：汉字 / 字母及相邻两字哈希进 256 维再归一化，确定性 | 只认字面重叠，不懂同义；同义说法的余弦很少到 0.6 的底线，向量那一路几乎不出力。真实嵌入（gte-multilingual 一类）懂语义，换说法、传闻、按时间这几类应该更高 |
| 重排 | 不用 | 线上默认也不开；开了会改 `memory_search` 的顺序 |
| 守卫的小模型确认 | 没有（`Gen` 为空） | 守卫只剩字面和句向量相似度两层；"换种说法的泄露"在真实线上靠小模型确认，这里不考 |
| 守卫的句向量 | 同上面的替身 | 阈值 0.70 是在真实嵌入上标定的，对替身没意义 |
| 来源标注 | 不接事件库 | 召回里的"谁说的"标签（`Hit.Source`）要回查真实事件，这里只有 `memories.json` 的 `source` 写着，Go 只用其中的会话和时间（写进库的 `conversation`、`created_at`） |

Bun 实现要点：读文件时用同样的口径（`Hits` 的 TopK 12、固定要点不算、待确认带听说标记、守卫按听众算范围）。它有自己的嵌入就用自己的，把替身名写进自己的基线，不要和 Go 的 `baseline.json` 混着比。

## 没覆盖

- 共用池（主人的事实跨角色共享，ADR-162）：这份考题只有两个互相独立的角色库。
- 别名误触发、重排、近重复去重、字数预算（`Budget`）、门控（`Gate`，这里一律用明确回忆，不受门控）。
- 写入侧（怎么从聊天里记下这些）：见 `docs/规格-记忆写入上线.md`。
- `Hit.Source` 的来源标注、冒充主人的来源伪造：见 `memory/` 包里的单元测试。
