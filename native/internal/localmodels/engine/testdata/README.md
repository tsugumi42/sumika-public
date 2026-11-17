# engine/testdata

引擎的参考数据，**全是编的或 Python 自己导出的**，不含真实聊天（规格-Go本地模型 第五节）。

| 文件 | 谁写的 | 是什么 |
|---|---|---|
| `ref-registry.json` | `tools/local-models/parity_ref.py rules` | gte、bge 两条登记：Python 的 `server.load_specs` 解出来的值（缺省值已补上），加 `tools/local-models/models.toml` 的 SHA-256（换行统一成 LF 再算）。`TestRegistryMatchesPython` 拿它逐字段比 Go 的登记，并核对 models.toml 现在的哈希：Python 的表一改，哈希对不上，测试先红，提醒重导再对 |
| `ref-whitespace.json` | 同上 | Python 的 `str.isspace()` 为真的全部码点（Python 3.12，Unicode 15.0，共 29 个；`server.py` 用 `not text.strip()` 判空，strip 和 isspace 是同一个集合）。`TestPySpaceMatchesPython` 对全部 0x110000 个码点逐个比 |

真模型测试用的参考向量和 logit 在 `../ort/testdata/`（GM2 生成，两个包共用，不拷第二份）。

## 再生成

按 `tools/local-models/requirements.txt` 用 uv 建的独立环境（python.exe 写它的路径，下面记作 <独立环境>\Scripts\python.exe）。在仓库根目录：

    <独立环境>\Scripts\python.exe -I -B tools/local-models/parity_ref.py rules --models-root <模型根> --out native/internal/localmodels/engine/testdata

## 跑真 dll、真模型的测试

    set SUMIKA_TEST_ORT=<onnxruntime.dll 的绝对路径，哈希要在 ort.KnownDLLs 的清单里>
    set SUMIKA_TEST_MODELS_ROOT=<模型根>
    cd native
    go test -short -count=1 -v -run TestEngine ./internal/localmodels/engine/

没设环境变量就 SKIP 并写明缺什么。参照在另一种 CPU 上生成的（`../ort/testdata/ref-meta.json`），也会 SKIP，不放宽门槛。
