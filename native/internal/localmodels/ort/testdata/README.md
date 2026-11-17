# ort/testdata

对拍用的参考数据，样本都是编的（`cases.json` 42 句、`pairs.json` 6 对），不含真实聊天数据。

- `ref-ids-gte.json`：每句 gte 的 token id（服务的分词器路径，含首尾特殊符号）
- `ref-raw0-gte.f32`：每句池化前的第 0 行（`token_embeddings[0,0,:]`，未归一化），小端 float32，42 × 768
- `ref-vecs-gte.f32`：每句最终向量（第 0 行 L2 归一化），小端 float32，42 × 768
- `ref-logits-bge.json`：6 对 bge 重排的 token id 和 logit
- `ref-meta.json`：生成时的 CPU、各包版本、模型文件哈希。测试遇到 CPU 型号不同就 `t.Skip`，不放宽门槛

`engine` 包的真模型测试也读这里的 `cases.json`、`ref-*`（GM3），不另拷一份。

不放在这里的东西：模型和 dll（本机文件，用环境变量指过来）。

## 再生成

生成器 `tools/local-models` 已从仓库删掉（ADR-389，提交 及以前在 git 历史里）。数据是冻结的对拍快照：要重新生成，先从那个提交取回目录，再按原来的命令导出（`parity_ref.py engine`）。

换了 CPU、升了 onnxruntime 或 tokenizers，重新生成并看 `git diff`：`ref-meta.json` 变了，数据可能跟着变。
参照由 `server.py` 实际走的那条路算出（`load_specs`、`build_tokenizer`、`LoadedModel`），Python 的会话是 onnxruntime 默认的 arena 开；
Go 这边 arena 关，两边逐位相同（`TestORTGTEMatchesReference`、`TestArenaOffSameVectors`）。

## 跑真 dll、真模型的测试

    set SUMIKA_TEST_ORT=<onnxruntime.dll 的绝对路径，哈希要在 known.go 的清单里>
    set SUMIKA_TEST_MODELS_ROOT=<模型根>
    cd native
    go test -short -count=1 -v ./internal/localmodels/ort/

重的几个（GC 压力、内存、耗时、长输入）各有开关，见 `real_test.go` 开头，一次只跑一样。
