# server/testdata

`python-http.json`：Python 版（`tools/local-models/server.py` 的 `create_app`，令牌、正文上限、Pydantic 校验、路由里的检查都是真的，
配一个假的池）对各种请求的状态码。生成器已从仓库删掉（ADR-389），这份是冻结的对拍快照。
Go 版的 `TestWhitespaceZoo` 逐条比 `zoo`，`TestStrictBody` 逐条比 `shapes`；
Go 版比 Python 版更严的地方在 `server_test.go` 的 `stricterThanPython` 里逐条登记，多一条少一条都红。

输入全是编的。要再生成，先从 git 历史（提交 及以前）取回 `tools/local-models`，再按原来的命令导出：

```
python -I tools/local-models/parity_ref.py http --models-root <模型根> --out native/internal/localmodels/server/testdata
```

再生成以后先看 `git diff`：FastAPI、Pydantic 的版本变了，状态码可能跟着变（`python-http.json` 开头记了各包版本）。
