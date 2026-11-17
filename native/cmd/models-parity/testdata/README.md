# models-parity/testdata

| 文件 | 是什么 |
|---|---|
| `corpus.json` | **全是编的**：326 条记忆式条目（12 个编的名字 × 一批话题，一部分是同一件事的两种说法，一部分带第二句）、100 条查询、10 个重排的查询（各 20 个候选）。`embedinputs` 的格式，用 `go:embed` 编进命令。没有真实人名、聊天、记忆。用途：让相似度有足够多种取值（同主语不同事、同事不同主语、近义改写），好量相似度差 D 和门槛附近的翻转。守卫的句子不存在这个文件里，加载时由条目经 `memory.OutboundSentences` 切出来，和 `memory-eval -dump-embed-inputs` 导出时一样 |

命令还读 `internal/localmodels` 下别的包的编的样本（`-testdata` 指过去，缺省在 `native/` 里跑就对）：`tokenizer/testdata` 的 `cases.json`（86 条）、`probes.json`（598 条空白和控制字符探针）、`pairs.json`（重排的 16 对，含刚好在 1024 token 上下的边界）、`ref-ids-*.json`、`ref-pairs-bge.json`、`known-diffs.json`、`categories.json`，`engine/testdata/ref-whitespace.json`（Python 的空白集合，造"空白动物园"用），`engine/testdata/ref-registry.json`（登记表单测用）。

## 重新生成 corpus.json

一次性脚本，不进仓库（确定性：同一份脚本永远生成同一份文件）；要换样本就改脚本重新生成，再看 `git diff`。生成时的做法：

- 12 个编的名字，每个名字从 35 个话题模板里挑 24 个，各写成一句陈述；30% 的条目再接一句"补充"（让守卫切句有两句的）；8 个话题另有近义改写的说法，随机放进去一部分；
- 查询：每个名字挑 9 个话题的提问句，共 100 条；
- 重排：10 个查询，每个配 10 个话题相关的条目加 10 个随机条目，打乱顺序。

改了 `corpus.json` 以后，`-ref-in` 存的 Python 参照里的样本清单哈希就对不上了，要重新存（命令会拒绝用旧的，不会悄悄比错）。
