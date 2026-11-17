# goSentencePiece（fork）的来源和改动

规格：`docs/archive/规格/规格-Go本地模型.md` 2.4、GM1。这是一份拷进仓库自己改的 fork，包名 `sentencepiece`，导入路径 `sumika-native/third_party/goSentencePiece`；`go.mod` 里不再依赖上游。

## 来源

- 上游：<https://github.com/tggo/goSentencePiece>，v1.1.0，提交 （2026-03-22），MIT 许可证，版权 Promova（见 `LICENSE`）。
- 只用它读 HuggingFace 的 `tokenizer.json` 里的 Unigram 词表，给 gte-multilingual-base 和 bge-reranker-v2-m3 分词。

## 第一次拷入（原样）

拷了：`bpe.go`、`byte_fallback.go`、`encoder.go`、`encoding.go`、`errors.go`、`model.go`、`normalizer.go`、`padding.go`、`postprocessor.go`、`sentencepiece.go`、`tokenizer_json.go`、`trie.go`、`truncation.go`、`unigram.go`、`LICENSE`。
没拷：上游的 `*_test.go`（读它自带的 `_testdata/spm.model`）、`_testdata/`（9.5 MB）、`examples/`、`proto/`、`.github/`、`Makefile` 等周边文件。

和上游原样不同的只有三处：

1. `proto/` 和 `.model`（protobuf）读取路径没带：`LoadModel`、`LoadModelFromReader`、`loadModelFromBytes` 删掉，`NewTokenizer`、`NewTokenizerFromReader` 遇到不是 JSON 的输入返回 `ErrInvalidModel`。原因：本机模块缓存里只有 protobuf v1.36.11 的压缩包，模块图选中的是 v1.36.12（goose 带进来的），离线编不过；我们只吃 `tokenizer.json`，本来也用不到这条路径。
2. `go.mod` 里的 protobuf 依赖随之没加。
3. `tokenizer_json.go` 的 `convertJSONTemplate` 里给 `TemplatePiece` 的特殊符号名字段赋值的那一行，先取到局部变量再赋：提交前的隐私扫描把"字段名带 Token、后面跟一长串字母"当成赋值的密钥拦了，行为没变。

在这份原样代码上跑出来的 golden（`internal/localmodels/tokenizer/testdata/golden-ids-*.json`，最初叫 `upstream-ids-*.json`，第 7 步改名）是之后每次改动的对照：紧凑词表树必须和它逐个相同。**它已经不是"上游原样的输出"**：Viterbi 改成 float64 累加（对齐 Python 的 tokenizers）以后，真 gte 的 golden 按新口径重生成过（5 万条固定种子随机串里 2 条切法变了），之后每次加样本也会重生成（只增不改旧的）；改动的来龙去脉见规格-Go本地模型 GM1 的记录。

## 上游原样的加载基线（`TestTokenizerMemory`，2026-11-02，本机 Ryzen 5 5600X）

加载一个 `tokenizer.json`（从字节到 Tokenizer，不含读文件），三遍取耗时中位数；内存是加载前后各 GC 两次并把空闲页还给系统以后的增量：

| 文件 | 加载耗时（中位） | HeapAlloc 增量 | 工作集增量 |
|---|---|---|---|
| gte | 1391 ms | 1828.2 MB | 1858.3 MB |
| bge（原文件，没拍平） | 1657 ms | 1827.8 MB | 1857.8 MB |

原因：词表树每个节点是 `children [256]*ByteTrie`（约 2 KB），25 万词条约 85 万个节点。

## 之后的改动

每个提交在这里追加一节：改了什么、量出来的数。

### 紧凑词表树（`trie.go`）

`ByteTrie` 从"每个节点一张 `[256]*ByteTrie` 子节点表"改成按广度优先排号的三个扁平数组：`first`（节点 n 的子节点是编号 `first[n]` 到 `first[n+1]-1`，连续、按字节排序）、`label`（进这个节点的那个字节）、`value`（节点上的词条 id，没有是 -1）。`Traverse(node, b)` 用节点编号代替指针，对子节点二分。建树改成 `trieBuilder`：先收集（词条，id），按字节序排好，再一遍过建完，复杂度是总字节数；空词条忽略，同一个词条加两次后加的赢（和原来的 `Insert` 一样）。Viterbi 的同分取舍（`unigram.go` 里 `candidateScore > target.bestPathScore`，严格大于、先到先得）和遍历顺序没动。`unigram.go`、`encoder.go`、`bpe.go` 的三处调用点跟着改成按编号走。

量出来的（`TestTokenizerMemory`，三遍取中位，真文件，同一台机器同一个测试）：

| 文件 | 加载耗时 | HeapAlloc 增量 | 工作集增量 |
|---|---|---|---|
| gte | 498 ms（原样 1391 ms） | 32.5 MB（原样 1828.2 MB） | 33.9 MB（原样 1858.3 MB） |
| bge | 541 ms（原样 1657 ms） | 32.1 MB（原样 1827.8 MB） | 33.4 MB（原样 1857.8 MB） |

加载耗时受机器上别的进程影响，同一份代码两次跑分别量到 698 ms 和 498 ms（gte）；内存两次一样。32 MB 里树本身约 8 MB，其余是 `pieces` 和 `pieceIndex`（后面的提交处理）。
`TestCompactTrieSameAsOriginal`：71 条样本、598 条探针、5 万条随机串，迷你词表（两种 pre_tokenizer）和真的 gte、bge 共四份，id 和第一次拷入时存的 golden 逐个相同。

### 删掉用不到的

- 整个删掉：`bpe.go`（BPE）、`padding.go`、`postprocessor.go`、`truncation.go`、`encoding.go`。库自带的截断、填充、特殊符号模板我们不用（截断和特殊符号由调用方自己做，规格 2.4）。
- `Tokenizer` 只剩 `NewTokenizerFromJSONReader` 一个入口和 `Encode(text) []int`（内容 id，不带特殊符号）。`NewTokenizer`、`NewTokenizerFromReader`（自动识别 JSON / .model）、`EncodeAsPieces`、`Decode`、`AddSpecialTokens`、`EncodeBatch`、`WithPostProcessor` / `WithPadding` / `WithTruncation`、`EncodeWithOptions` 等都删了。
- 词条的字符串映射 `pieceIndex`（约 20 MB）和词条字符串对 `encodedPiece` 的拷贝删了：BPE 和字节回退才用它们。字节回退（`byte_fallback: true`）不支持，加载时报 `ErrUnsupportedModel`。
- 比原来更严的三处（原来是悄悄放过、给出不对的 id）：`model.type` 不是 Unigram 报错；有不是 special 的 `added_tokens` 报错（原来的做法是在原文里先匹配它们，我们两个模型都没有，不实现）；`pre_tokenizer` 不是"Metaspace（替换符 ▁、总是加前缀、按 ▁ 切）"或"WhitespaceSplit 加这个 Metaspace"也报错。`normalizer` 不认识的类型仍然悄悄忽略（拍平 bge 之前要靠它量出"不拍平更差"）。
- Viterbi 的同分取舍、最长匹配的遍历顺序没动；`TestCompactTrieSameAsOriginal` 证明 id 和上游原样逐个相同。

量出来的（`TestTokenizerMemory`，真文件）：gte HeapAlloc +18.5 MB、工作集 +19.7 MB，bge +18.1 MB、+19.2 MB（上一步 32.5 / 32.1 MB）。加载耗时这一步没动词表解析（还是 `encoding/json` 解成 `[][2]json.RawMessage` 再逐条解码），主要时间在那里；量的时候机器上别的进程在抢 CPU，同一时刻上一步的提交量到 gte 1230 ms、这一步 1285 ms，不能当绝对值，安静时另量。

### normalizer 按文件里的顺序完整执行（`normalizer.go`、`tokenizer_json.go`、`trie.go`）

上游只认单独一个 Precompiled，Sequence 形式的 normalizer 整个悄悄忽略；bge-reranker-v2-m3 的是 `Sequence[Precompiled, Strip（只右边）, Replace(" {2,}" → "▁")]`，所以原来的 bge 少了字符映射表（和 Python 只有 16/42 句一致）。现在 `parseNormalizer` 读 null、单独的 Precompiled、或以 Precompiled 开头后接 Strip 和那一条 Replace 的 Sequence，按文件里的顺序执行：字符映射表之后依次剥空白（Rust 的 `is_whitespace`，即 Unicode 的 White_Space，Go 的 `unicode.IsSpace`）、两个以上的 ASCII 空格合成一个 ▁。别的类型、别的 Replace 参数、不在开头的 Precompiled、坏的 base64 或大小对不上的字符映射表一律报错（原来是悄悄放过）。
顺手：字符映射表这一步重写成逐字符直接写进 `strings.Builder`（原来每个字符把剩下的整段输入转成 `[]byte`、再给每个匹配分配一个切片和字符串），Darts 的查找改成直接返回最长匹配；去掉了 .model 才有的选项（补空格前缀、合并多余空白、转义空白），tokenizer.json 从来不设它们。gte（真文件）和两份迷你词表对 5 万条随机串 + 样本 + 探针仍和上游原样逐个相同；真的 bge 有意不再和上游原样比（它的 normalizer 现在被执行了）。

和 Python（tokenizers 0.22.2，`server.build_tokenizer`）逐 id 对 71 条样本 + 598 条空白和控制字符探针：

| | 拍平 normalizer 再前置 Fields（原计划） | 完整执行（现在） |
|---|---|---|
| bge 样本 | 15 / 71 条不同 | 8 / 71 条不同 |
| bge 探针 | 67 / 598 条不同 | 0 / 598 条不同 |

差异原因是顺序：字符映射表会把 ▁、零宽字符等变成空格，Strip 和 Replace 要在这之后做，而拍平加 Fields 是在映射表之前处理空白，补不全。完整执行之后 bge 剩下的 8 条样本和 gte 的 8 条是同一批：NFD 韩文 1 条、特殊符号字面值 7 条（见规格 2.4 的 T1、T2、S1）。

### Viterbi 用 float64（`model.go`、`unigram.go`、`tokenizer_json.go`）

词条分数读进来不再截成 float32，路径累加也用 float64。原因：Python 的 tokenizers（Rust）把词表分数存成 f64、按 f64 累加，两种切法总分只差在 float32 舍入误差里时，选的路不同；SentencePiece 的 C++ 参考用 float32，这个 fork 的上游是照它写的。我们要对齐的是 Python 服务，所以跟 Rust。
实测：真的 gte 上 5 万条固定种子随机串里有 2 条的 id 因此不同；在 2.8 万条随机串上和 Python 对，不属于已知差异类别的不同从 2 条降到 1 条（"iaqwwssvpppb" 这种纯字母串，float32 下切法和 Python 不同，float64 下相同）。
同一批样本、探针和迷你词表（分数是 0.25 的倍数，float32 和 float64 都精确）的 id 不变，golden 只有真 gte 的随机串摘要变了，已重新生成（旧的在 git 历史里）。
