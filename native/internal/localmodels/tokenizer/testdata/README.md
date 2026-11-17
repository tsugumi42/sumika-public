# tokenizer/testdata

分词器对拍和回归用的数据，**全是编的**，不含真实聊天（规格-Go本地模型 第五节）。

| 文件 | 谁写的 | 是什么 |
|---|---|---|
| `cases.json` | 手写（调研的 42 句加边界样本） | Go 和 Python 共用的输入；大的用 `unit` 重复 `count` 次的食谱 |
| `probes.json` | `go test -update` 生成 | 空白和控制字符探针：约 100 个字符 × 6 种位置 |
| `pairs.json` | 手写 | 重排的（查询，文档）对，含刚好在 1024 上下的边界 |
| `categories.json` | 手写 | 已知差异的类别（T1 NFD 韩文、T5 字素簇扩展字符、S1 特殊符号字面值）：原因，和"去掉哪些字符算去掉" |
| `ref-ids-gte.json`、`ref-ids-bge.json`、`ref-pairs-bge.json` | `tools/local-models/parity_ref.py` | Python 版服务（`server.build_tokenizer`，含截断）给的 id；含类别字符的样本另带"去掉以后"的 id |
| `known-diffs.json` | `go test -update` 生成，人看过再提交 | 登记的已解释差异：测试要求实际的差异和它一模一样 |
| `golden-ids-*.json` | `go test -update` 生成 | fork 自己的回归基线（迷你词表两份、真 gte 一份）；最初在上游原样代码上生成 |

## 重新生成

样本（`cases.json`、`pairs.json`）、探针的生成方式或 `categories.json` 改了，按顺序做（要真模型文件的两步，`SUMIKA_TEST_MODELS_ROOT` 指到模型根目录；包路径写在 `-update` 前面）：

1. 探针和 golden（探针由这一步的 `-update` 重写）：

   ```
   cd native
   go test ./internal/localmodels/tokenizer -run TestEncodeGolden -update
   ```

2. 参考数据：用能 import `tools/local-models/server.py` 的 Python（按 `tools/local-models/requirements.txt` 用 uv 建的独立环境，版本钉在 `requirements.txt` 里），在仓库根目录：

   ```
   python -I tools/local-models/parity_ref.py tokenizer --models-root <模型根> --testdata native/internal/localmodels/tokenizer/testdata
   ```

3. 登记的差异：

   ```
   cd native
   go test ./internal/localmodels/tokenizer -run TestTokenizerParity -update
   ```

   `git diff` 看一遍：多出来的差异是不是真的属于登记的类别，再提交。

真文件的测试没设 `SUMIKA_TEST_MODELS_ROOT` 就跳过；迷你词表的测试不要真文件。

## 类别表的区间怎么来

`categories.json` 的 T5（`Grapheme_Extend`）区间不是手工拼的：由 Go 的 `unicode` 表生成（Mn ∪ Me ∪ `Other_Grapheme_Extend`），再并入原来 Python 15.0 列过的那些（`U+1171E` 在 16.0 起改成了 Mc，靠旧表留着）。
`TestT5IsGraphemeExtend` 对着 Go 的表核：缺一个就红，Go 升级 Unicode 表后也会红，到时重新生成。生成的办法（在任何临时目录里跑，输入是现在的 `categories.json`）：

```go
// 遍历 0..0x10FFFF，保留旧表里的码点，再加上 unicode.Is(unicode.Mn/Me/Other_Grapheme_Extend, r) 为真的，合成升序不相接的 [lo,hi]，每行 8 个区间
```

## 全部码点的 NFD、NFKD 形式（不进 git）

类别表漏了一类字符时，日常的样本里不一定有。`parity_ref.py sweep` 把全部码点的 NFD、NFKD 形式（去重后约 1.5 万条）的 Python id 导进临时目录，
`TestUnicodeSweep` 逐条比（命令在 `tools/local-models/parity_ref.py` 的文件头）：

```
cd native
SUMIKA_TEST_MODELS_ROOT=<模型根> SUMIKA_TEST_SWEEP_DIR=<临时目录> go test ./internal/localmodels/tokenizer -run TestUnicodeSweep -v
```

要求每个和 Python 不一致的文本都归得到类别、去掉类别字符以后一致；输出一行各类别的计数。改了 `categories.json` 要重导一遍（"类别和参考数据对不上"就是这个）。
